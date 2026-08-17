package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestServer pins the order ID and disables fault injection so assertions
// are deterministic.
func newTestServer(t *testing.T) *server {
	t.Helper()
	s := newServer("test-version", newFaultInjector(0))
	s.nextID = func() string { return "ord_test" }
	return s
}

func doRequest(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response body: %v", err)
	}
	return body
}

func TestProbeEndpoints(t *testing.T) {
	h := newTestServer(t).routes()

	cases := []struct {
		path   string
		status string
	}{
		{"/health", "ok"},
		{"/ready", "ready"},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			rec := doRequest(t, h, http.MethodGet, tc.path, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("expected application/json, got %q", ct)
			}
			if got := decodeMap(t, rec)["status"]; got != tc.status {
				t.Errorf("expected status %q, got %v", tc.status, got)
			}
		})
	}
}

func TestInfoHandler(t *testing.T) {
	rec := doRequest(t, newTestServer(t).routes(), http.MethodGet, "/api/v1/info", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := decodeMap(t, rec)
	if body["version"] != "test-version" {
		t.Errorf("expected version test-version, got %v", body["version"])
	}
	for _, key := range []string{"uptime", "hostname"} {
		if _, ok := body[key]; !ok {
			t.Errorf("expected key %q in info response", key)
		}
	}
}

// TestRouting covers the method and path rules the probes and the canary
// traffic depend on.
func TestRouting(t *testing.T) {
	h := newTestServer(t).routes()

	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"health get", http.MethodGet, "/health", http.StatusOK},
		{"health wrong method", http.MethodPost, "/health", http.StatusMethodNotAllowed},
		{"orders wrong method", http.MethodGet, "/api/v1/orders", http.StatusMethodNotAllowed},
		{"unknown path", http.MethodGet, "/missing", http.StatusNotFound},
		{"metrics", http.MethodGet, "/metrics", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(t, h, tc.method, tc.path, "")
			if rec.Code != tc.want {
				t.Errorf("%s %s: expected %d, got %d", tc.method, tc.path, tc.want, rec.Code)
			}
		})
	}
}

func TestLoggingMiddlewarePassesStatusThrough(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	rec := doRequest(t, loggingMiddleware(inner), http.MethodGet, "/anything", "")
	if rec.Code != http.StatusTeapot {
		t.Errorf("expected 418 to pass through, got %d", rec.Code)
	}
}
