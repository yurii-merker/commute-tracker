package darwin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetDepartureBoard(t *testing.T) {
	tests := []struct {
		name        string
		response    string
		statusCode  int
		wantCount   int
		wantErr     bool
		wantService string
	}{
		{
			name:       "on time service",
			statusCode: http.StatusOK,
			response: `<?xml version="1.0"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <GetDepBoardWithDetailsResponse xmlns="http://thalesgroup.com/RTTI/2017-02-02/ldb/">
      <GetStationBoardResult>
        <trainServices>
          <service>
            <std>07:45</std>
            <etd>On time</etd>
            <platform>3</platform>
            <serviceID>svc123</serviceID>
            <isCancelled>false</isCancelled>
            <destination><location><locationName>Welwyn Garden City</locationName></location></destination>
          </service>
        </trainServices>
      </GetStationBoardResult>
    </GetDepBoardWithDetailsResponse>
  </soap:Body>
</soap:Envelope>`,
			wantCount:   1,
			wantService: "svc123",
		},
		{
			name:       "multiple services",
			statusCode: http.StatusOK,
			response: `<?xml version="1.0"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <GetDepBoardWithDetailsResponse xmlns="http://thalesgroup.com/RTTI/2017-02-02/ldb/">
      <GetStationBoardResult>
        <trainServices>
          <service>
            <std>07:45</std>
            <etd>On time</etd>
            <platform>3</platform>
            <serviceID>svc1</serviceID>
            <isCancelled>false</isCancelled>
          </service>
          <service>
            <std>08:15</std>
            <etd>08:22</etd>
            <platform>1</platform>
            <serviceID>svc2</serviceID>
            <isCancelled>false</isCancelled>
          </service>
        </trainServices>
      </GetStationBoardResult>
    </GetDepBoardWithDetailsResponse>
  </soap:Body>
</soap:Envelope>`,
			wantCount: 2,
		},
		{
			name:       "empty board",
			statusCode: http.StatusOK,
			response: `<?xml version="1.0"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <GetDepBoardWithDetailsResponse xmlns="http://thalesgroup.com/RTTI/2017-02-02/ldb/">
      <GetStationBoardResult>
        <trainServices/>
      </GetStationBoardResult>
    </GetDepBoardWithDetailsResponse>
  </soap:Body>
</soap:Envelope>`,
			wantCount: 0,
		},
		{
			name:       "server error returns transient",
			statusCode: http.StatusInternalServerError,
			response:   "Internal Server Error",
			wantErr:    true,
		},
		{
			name:       "unauthorized returns permanent",
			statusCode: http.StatusUnauthorized,
			response:   "Unauthorized",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := NewClient("test-token")
			client.httpClient = server.Client()

			client.endpoint = server.URL

			results, err := client.GetDepartureBoard(context.Background(), "SMH", "CTK", 0, 20)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(results) != tt.wantCount {
				t.Errorf("got %d services, want %d", len(results), tt.wantCount)
			}

			if tt.wantService != "" && len(results) > 0 {
				if results[0].ServiceID != tt.wantService {
					t.Errorf("got service ID %q, want %q", results[0].ServiceID, tt.wantService)
				}
				if results[0].Destination != "Welwyn Garden City" {
					t.Errorf("got destination %q, want %q", results[0].Destination, "Welwyn Garden City")
				}
			}
		})
	}
}

func TestGetServiceDetails(t *testing.T) {
	tests := []struct {
		name         string
		response     string
		statusCode   int
		wantErr      bool
		wantPlatform string
		wantDelay    int
		wantCancel   bool
	}{
		{
			name:       "on time",
			statusCode: http.StatusOK,
			response: `<?xml version="1.0"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <GetServiceDetailsResponse xmlns="http://thalesgroup.com/RTTI/2017-02-02/ldb/">
      <GetServiceDetailsResult>
        <std>07:45</std>
        <etd>On time</etd>
        <platform>3</platform>
        <serviceID>svc123</serviceID>
        <isCancelled>false</isCancelled>
      </GetServiceDetailsResult>
    </GetServiceDetailsResponse>
  </soap:Body>
</soap:Envelope>`,
			wantPlatform: "3",
			wantDelay:    0,
			wantCancel:   false,
		},
		{
			name:       "delayed",
			statusCode: http.StatusOK,
			response: `<?xml version="1.0"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <GetServiceDetailsResponse xmlns="http://thalesgroup.com/RTTI/2017-02-02/ldb/">
      <GetServiceDetailsResult>
        <std>07:45</std>
        <etd>07:52</etd>
        <platform>3</platform>
        <serviceID>svc123</serviceID>
        <isCancelled>false</isCancelled>
      </GetServiceDetailsResult>
    </GetServiceDetailsResponse>
  </soap:Body>
</soap:Envelope>`,
			wantPlatform: "3",
			wantDelay:    7,
			wantCancel:   false,
		},
		{
			name:       "cancelled",
			statusCode: http.StatusOK,
			response: `<?xml version="1.0"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <GetServiceDetailsResponse xmlns="http://thalesgroup.com/RTTI/2017-02-02/ldb/">
      <GetServiceDetailsResult>
        <std>07:45</std>
        <etd>Cancelled</etd>
        <platform></platform>
        <serviceID>svc123</serviceID>
        <isCancelled>false</isCancelled>
      </GetServiceDetailsResult>
    </GetServiceDetailsResponse>
  </soap:Body>
</soap:Envelope>`,
			wantPlatform: "",
			wantDelay:    0,
			wantCancel:   true,
		},
		{
			name:       "no platform",
			statusCode: http.StatusOK,
			response: `<?xml version="1.0"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <GetServiceDetailsResponse xmlns="http://thalesgroup.com/RTTI/2017-02-02/ldb/">
      <GetServiceDetailsResult>
        <std>07:45</std>
        <etd>On time</etd>
        <serviceID>svc123</serviceID>
        <isCancelled>false</isCancelled>
      </GetServiceDetailsResult>
    </GetServiceDetailsResponse>
  </soap:Body>
</soap:Envelope>`,
			wantPlatform: "",
			wantDelay:    0,
			wantCancel:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := NewClient("test-token")
			client.httpClient = server.Client()
			client.endpoint = server.URL

			result, err := client.GetServiceDetails(context.Background(), "svc123")

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Platform != tt.wantPlatform {
				t.Errorf("platform = %q, want %q", result.Platform, tt.wantPlatform)
			}
			if result.DelayMins != tt.wantDelay {
				t.Errorf("delay = %d, want %d", result.DelayMins, tt.wantDelay)
			}
			if result.IsCancelled != tt.wantCancel {
				t.Errorf("cancelled = %v, want %v", result.IsCancelled, tt.wantCancel)
			}
		})
	}
}

func TestErrorClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.httpClient = server.Client()
	client.endpoint = server.URL

	_, err := client.GetDepartureBoard(context.Background(), "SMH", "CTK", 0, 20)
	if err == nil {
		t.Fatal("expected error")
	}

	if !IsTransient(err) {
		t.Errorf("IsTransient should return true for 500 errors, got %T", err)
	}
}

func TestErrorClassificationPermanent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.httpClient = server.Client()
	client.endpoint = server.URL

	_, err := client.GetDepartureBoard(context.Background(), "SMH", "CTK", 0, 20)
	if err == nil {
		t.Fatal("expected error")
	}

	if IsTransient(err) {
		t.Error("IsTransient should return false for 401 errors")
	}

	var permanent *PermanentError
	if !errors.As(err, &permanent) {
		t.Errorf("expected PermanentError, got %T", err)
	}
}

func TestErrorMessages(t *testing.T) {
	transient := &TransientError{Cause: fmt.Errorf("timeout")}
	if transient.Error() != "transient darwin error: timeout" {
		t.Errorf("unexpected error message: %s", transient.Error())
	}
	if transient.Unwrap().Error() != "timeout" {
		t.Errorf("unexpected unwrapped error: %s", transient.Unwrap().Error())
	}

	permanent := &PermanentError{Cause: fmt.Errorf("unauthorized")}
	if permanent.Error() != "permanent darwin error: unauthorized" {
		t.Errorf("unexpected error message: %s", permanent.Error())
	}
	if permanent.Unwrap().Error() != "unauthorized" {
		t.Errorf("unexpected unwrapped error: %s", permanent.Unwrap().Error())
	}
}

func TestParseInvalidXML(t *testing.T) {
	_, err := parseDepBoardResponse([]byte("not xml"))
	if err == nil {
		t.Error("expected error for invalid XML")
	}

	_, err = parseServiceDetailsResponse([]byte("not xml"))
	if err == nil {
		t.Error("expected error for invalid XML")
	}
}

func TestMapServiceInvalidSTD(t *testing.T) {
	_, err := mapServiceToTrainStatus(serviceXML{
		STD:       "not-a-time",
		ETD:       "On time",
		ServiceID: "svc1",
	})
	if err == nil {
		t.Error("expected error for invalid STD")
	}
}

func TestEscapeXML(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"ABC", "ABC"},
		{"<script>", "&lt;script&gt;"},
		{"a&b", "a&amp;b"},
		{`"quoted"`, "&#34;quoted&#34;"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := escapeXML(tt.input); got != tt.want {
				t.Errorf("escapeXML(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseTrainTime(t *testing.T) {
	tests := []struct {
		input   string
		wantH   int
		wantM   int
		wantErr bool
	}{
		{"07:45", 7, 45, false},
		{"23:59", 23, 59, false},
		{"00:00", 0, 0, false},
		{"invalid", 0, 0, true},
		{"", 0, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := parseTrainTime(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Hour() != tt.wantH || result.Minute() != tt.wantM {
				t.Errorf("got %02d:%02d, want %02d:%02d", result.Hour(), result.Minute(), tt.wantH, tt.wantM)
			}
		})
	}
}
