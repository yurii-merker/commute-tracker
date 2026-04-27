package darwin

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/yurii-merker/commute-tracker/internal/domain"
	"github.com/yurii-merker/commute-tracker/internal/timezone"
)

const (
	defaultEndpoint   = "https://lite.realtime.nationalrail.co.uk/OpenLDBWS/ldb11.asmx"
	soapActionBoard   = "http://thalesgroup.com/RTTI/2015-05-14/ldb/GetDepBoardWithDetails"
	soapActionService = "http://thalesgroup.com/RTTI/2012-01-13/ldb/GetServiceDetails"
	nsSOAP            = "http://schemas.xmlsoap.org/soap/envelope/"
	nsLDB             = "http://thalesgroup.com/RTTI/2017-10-01/ldb/"
	nsToken           = "http://thalesgroup.com/RTTI/2013-11-28/Token/types" //nolint:gosec
)

type Client struct {
	token      string
	endpoint   string
	httpClient *http.Client
}

func NewClient(token string) *Client {
	return &Client{
		token:    token,
		endpoint: defaultEndpoint,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *Client) GetDepartureBoard(ctx context.Context, fromCRS, toCRS string, timeOffsetMins, timeWindowMins int) ([]domain.TrainStatus, error) {
	reqBody := buildDepBoardRequest(c.token, fromCRS, toCRS, timeOffsetMins, timeWindowMins)

	slog.Debug("darwin API request",
		"from", fromCRS,
		"to", toCRS,
		"offset", timeOffsetMins,
	)

	respBody, err := c.doSOAPRequest(ctx, soapActionBoard, reqBody)
	if err != nil {
		return nil, fmt.Errorf("departure board request: %w", err)
	}

	return parseDepBoardResponse(respBody)
}

func (c *Client) GetServiceDetails(ctx context.Context, serviceID string) (*domain.TrainStatus, error) {
	reqBody := buildServiceDetailsRequest(c.token, serviceID)

	respBody, err := c.doSOAPRequest(ctx, soapActionService, reqBody)
	if err != nil {
		return nil, fmt.Errorf("service details request: %w", err)
	}

	status, err := parseServiceDetailsResponse(respBody)
	if err != nil {
		return nil, err
	}
	status.ServiceID = serviceID
	return status, nil
}

func (c *Client) doSOAPRequest(ctx context.Context, soapAction string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "text/xml; charset=UTF-8")
	req.Header.Set("SOAPAction", soapAction)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &TransientError{Cause: err}
	}
	defer func() { _ = resp.Body.Close() }()

	const maxResponseSize = 1 << 20
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, &TransientError{Cause: fmt.Errorf("reading response: %w", err)}
	}

	if resp.StatusCode >= 500 {
		if isSOAPClientFault(respBody) {
			return nil, &PermanentError{Cause: fmt.Errorf("soap client fault: %s", respBody)}
		}
		return nil, &TransientError{Cause: fmt.Errorf("server error: %d, body: %s", resp.StatusCode, respBody)}
	}
	if resp.StatusCode >= 400 {
		return nil, &PermanentError{Cause: fmt.Errorf("client error: %d", resp.StatusCode)}
	}

	return respBody, nil
}

func escapeXML(s string) string {
	return html.EscapeString(s)
}

func isSOAPClientFault(body []byte) bool {
	return bytes.Contains(body, []byte("<faultcode>soap:Client</faultcode>"))
}

func buildDepBoardRequest(token, fromCRS, toCRS string, timeOffsetMins, timeWindowMins int) []byte {
	var filterCRS string
	if toCRS != "" {
		filterCRS = fmt.Sprintf(`<ldb:filterCrs>%s</ldb:filterCrs><ldb:filterType>to</ldb:filterType>`, escapeXML(toCRS))
	}

	var timeOffset string
	if timeOffsetMins != 0 {
		timeOffset = fmt.Sprintf(`<ldb:timeOffset>%d</ldb:timeOffset>`, timeOffsetMins)
	}

	timeWindow := timeWindowMins

	return []byte(fmt.Sprintf(`<?xml version="1.0"?>
<soap:Envelope xmlns:soap="%s" xmlns:ldb="%s" xmlns:tok="%s">
  <soap:Header>
    <tok:AccessToken>
      <tok:TokenValue>%s</tok:TokenValue>
    </tok:AccessToken>
  </soap:Header>
  <soap:Body>
    <ldb:GetDepBoardWithDetailsRequest>
      <ldb:numRows>10</ldb:numRows>
      <ldb:crs>%s</ldb:crs>
      %s
      %s
      <ldb:timeWindow>%d</ldb:timeWindow>
    </ldb:GetDepBoardWithDetailsRequest>
  </soap:Body>
</soap:Envelope>`, nsSOAP, nsLDB, nsToken, escapeXML(token), escapeXML(fromCRS), filterCRS, timeOffset, timeWindow))
}

func buildServiceDetailsRequest(token, serviceID string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0"?>
<soap:Envelope xmlns:soap="%s" xmlns:ldb="%s" xmlns:tok="%s">
  <soap:Header>
    <tok:AccessToken>
      <tok:TokenValue>%s</tok:TokenValue>
    </tok:AccessToken>
  </soap:Header>
  <soap:Body>
    <ldb:GetServiceDetailsRequest>
      <ldb:serviceID>%s</ldb:serviceID>
    </ldb:GetServiceDetailsRequest>
  </soap:Body>
</soap:Envelope>`, nsSOAP, nsLDB, nsToken, escapeXML(token), escapeXML(serviceID)))
}

func parseDepBoardResponse(data []byte) ([]domain.TrainStatus, error) {
	var env depBoardEnvelope
	if err := xml.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("parsing departure board XML: %w", err)
	}

	services := env.Body.Response.Result.TrainServices.Services
	results := make([]domain.TrainStatus, 0, len(services))

	for _, svc := range services {
		status, err := mapServiceToTrainStatus(svc)
		if err != nil {
			continue
		}
		results = append(results, status)
	}

	return results, nil
}

func parseServiceDetailsResponse(data []byte) (*domain.TrainStatus, error) {
	var env serviceDetailsEnvelope
	if err := xml.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("parsing service details XML: %w", err)
	}

	status, err := mapServiceToTrainStatus(env.Body.Response.Result)
	if err != nil {
		return nil, err
	}

	return &status, nil
}

func mapServiceToTrainStatus(svc serviceXML) (domain.TrainStatus, error) {
	scheduled, err := parseTrainTime(svc.STD)
	if err != nil {
		return domain.TrainStatus{}, fmt.Errorf("parsing scheduled time: %w", err)
	}

	estimated := scheduled
	delayMins := 0
	isCancelled := svc.IsCancelled

	if svc.ETD == "Cancelled" {
		isCancelled = true
	} else if svc.ETD != "On time" && svc.ETD != "" {
		if est, err := parseTrainTime(svc.ETD); err == nil {
			estimated = est
			delayMins = int(estimated.Sub(scheduled).Minutes())
			if delayMins < 0 {
				delayMins = 0
			}
		}
	}

	destination := ""
	if len(svc.Destination.Locations) > 0 {
		destination = svc.Destination.Locations[0].Name
	}

	return domain.TrainStatus{
		ServiceID:          svc.ServiceID,
		Destination:        destination,
		ScheduledDeparture: scheduled,
		EstimatedDeparture: estimated,
		Platform:           svc.Platform,
		IsCancelled:        isCancelled,
		DelayMins:          delayMins,
	}, nil
}

func parseTrainTime(s string) (time.Time, error) {
	now := timezone.Now()
	t, err := time.Parse("15:04", s)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, timezone.UK()), nil
}
