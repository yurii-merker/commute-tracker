package rtt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/yurii-merker/commute-tracker/internal/domain"
	"github.com/yurii-merker/commute-tracker/internal/timezone"
)

const (
	defaultEndpoint  = "https://data.rtt.io"
	searchWindowMins = 60
	searchMarginMins = 30
	tokenRefreshURL  = "/api/get_access_token"
)

type Client struct {
	refreshToken string
	endpoint     string
	httpClient   *http.Client

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

func NewClient(refreshToken string) *Client {
	return &Client{
		refreshToken: refreshToken,
		endpoint:     defaultEndpoint,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *Client) GetScheduledDepartures(ctx context.Context, fromCRS, toCRS string, date time.Time, targetMins int) ([]domain.TrainStatus, error) {
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting access token: %w", err)
	}

	startMins := targetMins - searchMarginMins
	if startMins < 0 {
		startMins = 0
	}
	timeFrom := time.Date(date.Year(), date.Month(), date.Day(), startMins/60, startMins%60, 0, 0, timezone.UK())

	params := url.Values{
		"code":       {fromCRS},
		"filterTo":   {toCRS},
		"timeFrom":   {timeFrom.Format(time.RFC3339)},
		"timeWindow": {strconv.Itoa(searchWindowMins)},
	}
	reqURL := c.endpoint + "/gb-nr/location?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	slog.Debug("rtt api request", "url", reqURL)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &TransientError{Cause: err}
	}
	defer func() { _ = resp.Body.Close() }()

	const maxResponseSize = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, &TransientError{Cause: fmt.Errorf("reading response: %w", err)}
	}

	slog.Debug("rtt api response", "status", resp.StatusCode, "body_len", len(body))

	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	if resp.StatusCode >= 500 {
		return nil, &TransientError{Cause: fmt.Errorf("server error: %d, body: %s", resp.StatusCode, body)}
	}
	if resp.StatusCode >= 400 {
		return nil, &PermanentError{Cause: fmt.Errorf("client error: %d, body: %s", resp.StatusCode, body)}
	}

	return parseSearchResponse(body)
}

func (c *Client) getAccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.accessToken != "" && time.Now().Before(c.tokenExpiry.Add(-time.Minute)) {
		token := c.accessToken
		c.mu.Unlock()
		return token, nil
	}
	c.mu.Unlock()

	return c.refreshAccessToken(ctx)
}

func (c *Client) refreshAccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.accessToken != "" && time.Now().Before(c.tokenExpiry.Add(-time.Minute)) {
		return c.accessToken, nil
	}

	reqURL := c.endpoint + tokenRefreshURL
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.refreshToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", &TransientError{Cause: fmt.Errorf("token refresh request: %w", err)}
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", &TransientError{Cause: fmt.Errorf("reading token response: %w", err)}
	}

	if resp.StatusCode != http.StatusOK {
		return "", &PermanentError{Cause: fmt.Errorf("token refresh failed: %d, body: %s", resp.StatusCode, body)}
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("parsing token response: %w", err)
	}

	expiry, err := time.Parse(time.RFC3339, tokenResp.ValidUntil)
	if err != nil {
		return "", fmt.Errorf("parsing token expiry: %w", err)
	}

	c.accessToken = tokenResp.Token
	c.tokenExpiry = expiry

	slog.Debug("rtt access token refreshed", "valid_until", tokenResp.ValidUntil)

	return c.accessToken, nil
}

func parseSearchResponse(data []byte) ([]domain.TrainStatus, error) {
	var resp searchResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parsing RTT response: %w", err)
	}

	var results []domain.TrainStatus
	for _, svc := range resp.Services {
		if !svc.ScheduleMetadata.InPassengerService {
			continue
		}
		display := svc.TemporalData.DisplayAs
		if display != "CALL" && display != "STARTS" {
			continue
		}

		status, err := mapServiceToTrainStatus(svc)
		if err != nil {
			continue
		}
		results = append(results, status)
	}

	return results, nil
}

func mapServiceToTrainStatus(svc serviceInfo) (domain.TrainStatus, error) {
	if svc.TemporalData.Departure == nil {
		return domain.TrainStatus{}, fmt.Errorf("no departure data")
	}

	scheduled, err := parseISO8601(svc.TemporalData.Departure.ScheduleAdvertised)
	if err != nil {
		return domain.TrainStatus{}, fmt.Errorf("parsing departure time: %w", err)
	}

	destination := ""
	if len(svc.Destination) > 0 {
		destination = svc.Destination[0].Location.Description
	}

	platform := ""
	if svc.LocationMetadata.Platform != nil {
		platform = svc.LocationMetadata.Platform.Planned
	}

	return domain.TrainStatus{
		ServiceID:          svc.ScheduleMetadata.Identity,
		Destination:        destination,
		ScheduledDeparture: scheduled,
		EstimatedDeparture: scheduled,
		Platform:           platform,
		IsScheduleOnly:     true,
	}, nil
}

func parseISO8601(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.In(timezone.UK()), nil
	}
	t, err := time.ParseInLocation("2006-01-02T15:04:05", s, timezone.UK())
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing time %q: %w", s, err)
	}
	return t, nil
}
