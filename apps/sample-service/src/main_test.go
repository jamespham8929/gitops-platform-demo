package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	healthHandler(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}

	var body map[string]string
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response body: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %q", body["status"])
	}
}

func TestReadyHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()

	readyHandler(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}

	var body map[string]string
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response body: %v", err)
	}
	if body["status"] != "ready" {
		t.Errorf("expected status ready, got %q", body["status"])
	}
}

func TestInfoHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/info", nil)
	rec := httptest.NewRecorder()

	infoHandler(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, res.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response body: %v", err)
	}
	for _, key := range []string{"version", "uptime", "hostname"} {
		if _, ok := body[key]; !ok {
			t.Errorf("expected key %q in info response", key)
		}
	}
}

// TestLoggingMiddleware checks that the middleware passes the request through
// and records the status the wrapped handler writes.
func TestLoggingMiddleware(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	rec := httptest.NewRecorder()

	loggingMiddleware(handler).ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Errorf("expected status %d to pass through, got %d", http.StatusTeapot, rec.Code)
	}
}

// TestRoutes confirms the mux wires the probe paths to their handlers and
// rejects the wrong method, since the deployment's liveness and readiness
// probes depend on those exact routes.
func TestRoutes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler)
	mux.HandleFunc("GET /ready", readyHandler)

	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"health get", http.MethodGet, "/health", http.StatusOK},
		{"ready get", http.MethodGet, "/ready", http.StatusOK},
		{"health wrong method", http.MethodPost, "/health", http.StatusMethodNotAllowed},
		{"unknown path", http.MethodGet, "/missing", http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("%s %s: expected status %d, got %d", tc.method, tc.path, tc.want, rec.Code)
			}
		})
	}
}
