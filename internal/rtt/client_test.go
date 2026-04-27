package rtt

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yurii-merker/commute-tracker/internal/timezone"
)

func TestMain(m *testing.M) {
	if err := timezone.Init(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

const tokenResponseJSON = `{"token":"test-access-token","validUntil":"2099-12-31T23:59:59+00:00"}` //nolint:gosec // test fixture, not a real credential

const validResponse = `{
	"services": [
		{
			"scheduleMetadata": {
				"uniqueIdentity": "gb-nr:W12345:2026-04-20",
				"identity": "W12345",
				"operator": {"code": "SW", "name": "South Western Railway"},
				"inPassengerService": true
			},
			"temporalData": {
				"departure": {
					"scheduleAdvertised": "2026-04-20T07:43:00",
					"isCancelled": false
				},
				"displayAs": "CALL"
			},
			"locationMetadata": {
				"platform": {"planned": "7", "actual": "7"}
			},
			"destination": [
				{"location": {"description": "Woking", "shortCodes": ["WOK"]}}
			]
		},
		{
			"scheduleMetadata": {
				"uniqueIdentity": "gb-nr:W12346:2026-04-20",
				"identity": "W12346",
				"operator": {"code": "SW", "name": "South Western Railway"},
				"inPassengerService": true
			},
			"temporalData": {
				"departure": {
					"scheduleAdvertised": "2026-04-20T08:13:00",
					"isCancelled": false
				},
				"displayAs": "STARTS"
			},
			"locationMetadata": {},
			"destination": [
				{"location": {"description": "Woking", "shortCodes": ["WOK"]}}
			]
		},
		{
			"scheduleMetadata": {
				"uniqueIdentity": "gb-nr:F99999:2026-04-20",
				"identity": "F99999",
				"operator": {"code": "FR", "name": "Freight"},
				"inPassengerService": false
			},
			"temporalData": {
				"departure": {
					"scheduleAdvertised": "2026-04-20T08:00:00",
					"isCancelled": false
				},
				"displayAs": "PASS"
			},
			"locationMetadata": {},
			"destination": [{"location": {"description": "Depot"}}]
		},
		{
			"scheduleMetadata": {
				"uniqueIdentity": "gb-nr:W12347:2026-04-20",
				"identity": "W12347",
				"operator": {"code": "SW", "name": "South Western Railway"},
				"inPassengerService": true
			},
			"temporalData": {
				"departure": {
					"scheduleAdvertised": "2026-04-20T08:30:00",
					"isCancelled": false
				},
				"displayAs": "PASS"
			},
			"locationMetadata": {},
			"destination": [{"location": {"description": "Woking"}}]
		}
	]
}`

func newTestServer(searchStatus int, searchBody string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get_access_token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tokenResponseJSON))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(searchStatus)
		_, _ = w.Write([]byte(searchBody))
	}))
}

func newTestClient(srv *httptest.Server) *Client {
	c := NewClient("test-refresh-token")
	c.endpoint = srv.URL
	return c
}

func testDate() time.Time {
	return time.Date(2026, 4, 20, 0, 0, 0, 0, timezone.UK())
}

func TestGetScheduledDepartures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get_access_token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tokenResponseJSON))
			return
		}

		if !strings.HasPrefix(r.URL.Path, "/gb-nr/location") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("code") != "WAT" {
			t.Errorf("expected code=WAT, got %s", r.URL.Query().Get("code"))
		}
		if r.URL.Query().Get("filterTo") != "WOK" {
			t.Errorf("expected filterTo=WOK, got %s", r.URL.Query().Get("filterTo"))
		}

		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-access-token" {
			t.Errorf("expected access token auth, got: %s", auth)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(validResponse))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	results, err := c.GetScheduledDepartures(context.Background(), "WAT", "WOK", testDate(), 480)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results (passenger CALL+STARTS only), got %d", len(results))
	}

	first := results[0]
	if first.ServiceID != "W12345" {
		t.Errorf("expected ServiceID W12345, got %s", first.ServiceID)
	}
	if first.Destination != "Woking" {
		t.Errorf("expected destination Woking, got %s", first.Destination)
	}
	if first.ScheduledDeparture.Hour() != 7 || first.ScheduledDeparture.Minute() != 43 {
		t.Errorf("expected 07:43, got %s", first.ScheduledDeparture.Format("15:04"))
	}
	if first.Platform != "7" {
		t.Errorf("expected platform 7, got %s", first.Platform)
	}
	if !first.IsScheduleOnly {
		t.Error("expected IsScheduleOnly to be true")
	}
	if first.IsCancelled {
		t.Error("expected IsCancelled to be false")
	}
	if first.DelayMins != 0 {
		t.Errorf("expected DelayMins 0, got %d", first.DelayMins)
	}

	second := results[1]
	if second.ServiceID != "W12346" {
		t.Errorf("expected ServiceID W12346, got %s", second.ServiceID)
	}
	if second.Platform != "" {
		t.Errorf("expected empty platform, got %s", second.Platform)
	}
	if !second.IsScheduleOnly {
		t.Error("expected IsScheduleOnly to be true on second result")
	}
}

func TestTokenRefreshCaching(t *testing.T) {
	refreshCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get_access_token" {
			refreshCount++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tokenResponseJSON))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(srv)

	for i := 0; i < 3; i++ {
		_, _ = c.GetScheduledDepartures(context.Background(), "WAT", "WOK", testDate(), 480)
	}

	if refreshCount != 1 {
		t.Errorf("expected 1 token refresh (cached), got %d", refreshCount)
	}
}

func TestGetScheduledDeparturesNoContent(t *testing.T) {
	srv := newTestServer(http.StatusNoContent, "")
	defer srv.Close()

	c := newTestClient(srv)
	results, err := c.GetScheduledDepartures(context.Background(), "WAT", "WOK", testDate(), 480)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for 204, got %d", len(results))
	}
}

func TestGetScheduledDeparturesEmptyServices(t *testing.T) {
	srv := newTestServer(http.StatusOK, `{"services": []}`)
	defer srv.Close()

	c := newTestClient(srv)
	results, err := c.GetScheduledDepartures(context.Background(), "WAT", "WOK", testDate(), 480)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestGetScheduledDeparturesServerError(t *testing.T) {
	srv := newTestServer(http.StatusInternalServerError, "internal error")
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.GetScheduledDepartures(context.Background(), "WAT", "WOK", testDate(), 480)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsTransient(err) {
		t.Errorf("expected TransientError, got %T: %v", err, err)
	}
}

func TestGetScheduledDeparturesUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get_access_token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tokenResponseJSON))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.GetScheduledDepartures(context.Background(), "WAT", "WOK", testDate(), 480)
	if err == nil {
		t.Fatal("expected error")
	}

	var pe *PermanentError
	if !errors.As(err, &pe) {
		t.Errorf("expected PermanentError, got %T", err)
	}
}

func TestTokenRefreshFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.GetScheduledDepartures(context.Background(), "WAT", "WOK", testDate(), 480)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "token refresh failed") {
		t.Errorf("expected token refresh error, got: %v", err)
	}
}

func TestGetScheduledDeparturesRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get_access_token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tokenResponseJSON))
			return
		}
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.GetScheduledDepartures(context.Background(), "WAT", "WOK", testDate(), 480)
	if err == nil {
		t.Fatal("expected error")
	}

	var pe *PermanentError
	if !errors.As(err, &pe) {
		t.Errorf("expected PermanentError for 429, got %T", err)
	}
}

func TestGetScheduledDeparturesTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get_access_token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tokenResponseJSON))
			return
		}
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	c.httpClient.Timeout = 50 * time.Millisecond
	// Pre-cache token so timeout only hits search
	_, _ = c.refreshAccessToken(context.Background())
	c.httpClient.Timeout = 50 * time.Millisecond

	_, err := c.GetScheduledDepartures(context.Background(), "WAT", "WOK", testDate(), 480)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsTransient(err) {
		t.Errorf("expected TransientError, got %T", err)
	}
}

func TestGetScheduledDeparturesMalformedJSON(t *testing.T) {
	srv := newTestServer(http.StatusOK, `{not valid json`)
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.GetScheduledDepartures(context.Background(), "WAT", "WOK", testDate(), 480)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestServiceFiltering(t *testing.T) {
	data := []byte(`{
		"services": [
			{
				"scheduleMetadata": {"identity": "P1", "inPassengerService": true},
				"temporalData": {"departure": {"scheduleAdvertised": "2026-04-20T08:00:00"}, "displayAs": "CALL"},
				"locationMetadata": {},
				"destination": [{"location": {"description": "D"}}]
			},
			{
				"scheduleMetadata": {"identity": "P2", "inPassengerService": true},
				"temporalData": {"departure": {"scheduleAdvertised": "2026-04-20T08:10:00"}, "displayAs": "STARTS"},
				"locationMetadata": {},
				"destination": [{"location": {"description": "D"}}]
			},
			{
				"scheduleMetadata": {"identity": "P3", "inPassengerService": true},
				"temporalData": {"departure": {"scheduleAdvertised": "2026-04-20T08:20:00"}, "displayAs": "PASS"},
				"locationMetadata": {},
				"destination": [{"location": {"description": "D"}}]
			},
			{
				"scheduleMetadata": {"identity": "P4", "inPassengerService": true},
				"temporalData": {"departure": {"scheduleAdvertised": "2026-04-20T08:30:00"}, "displayAs": "CANCELLED"},
				"locationMetadata": {},
				"destination": [{"location": {"description": "D"}}]
			},
			{
				"scheduleMetadata": {"identity": "F1", "inPassengerService": false},
				"temporalData": {"departure": {"scheduleAdvertised": "2026-04-20T08:40:00"}, "displayAs": "CALL"},
				"locationMetadata": {},
				"destination": [{"location": {"description": "D"}}]
			}
		]
	}`)

	results, err := parseSearchResponse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results (P1=CALL, P2=STARTS), got %d", len(results))
	}
	if results[0].ServiceID != "P1" {
		t.Errorf("expected P1, got %s", results[0].ServiceID)
	}
	if results[1].ServiceID != "P2" {
		t.Errorf("expected P2, got %s", results[1].ServiceID)
	}
}

func TestNoPlatformField(t *testing.T) {
	data := []byte(`{
		"services": [{
			"scheduleMetadata": {"identity": "S1", "inPassengerService": true},
			"temporalData": {"departure": {"scheduleAdvertised": "2026-04-20T08:00:00"}, "displayAs": "CALL"},
			"locationMetadata": {},
			"destination": [{"location": {"description": "D"}}]
		}]
	}`)

	results, err := parseSearchResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatal("expected 1 result")
	}
	if results[0].Platform != "" {
		t.Errorf("expected empty platform when field missing, got %q", results[0].Platform)
	}
}
