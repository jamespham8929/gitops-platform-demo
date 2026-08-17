package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestInstrumentUsesRouteLabel is the cardinality guard: two different URLs that
// match the same route must land on one series, or the canary analysis query
// starts measuring noise.
func TestInstrumentUsesRouteLabel(t *testing.T) {
	s := newTestServer(t)
	h := s.routes()

	for _, path := range []string{"/missing", "/also-missing", "/deeply/missing"} {
		doRequest(t, h, http.MethodGet, path, "")
	}

	got := testutil.ToFloat64(s.metrics.requestsTotal.WithLabelValues(http.MethodGet, "unmatched", "404"))
	if got != 3 {
		t.Errorf("expected 3 requests under the unmatched route, got %v", got)
	}
	if n := testutil.CollectAndCount(s.metrics.requestsTotal); n != 1 {
		t.Errorf("expected a single http_requests_total series, got %d", n)
	}
}

// TestMetricsEndpointExcludesItself keeps scrapes out of the success-rate
// numerator and denominator.
func TestMetricsEndpointExcludesItself(t *testing.T) {
	s := newTestServer(t)
	h := s.routes()

	doRequest(t, h, http.MethodGet, "/health", "")
	rec := doRequest(t, h, http.MethodGet, "/metrics", "")

	body := rec.Body.String()
	if !strings.Contains(body, `http_requests_total{method="GET",route="/health",status="200"} 1`) {
		t.Errorf("expected the health request in the exposition, got:\n%s", body)
	}
	if strings.Contains(body, `route="/metrics"`) {
		t.Error("scrapes should not be counted in http_requests_total")
	}
	for _, name := range []string{"http_request_duration_seconds", "orders_created_total", "orders_rejected_total", "order_value_cents", "go_goroutines"} {
		if !strings.Contains(body, name) {
			t.Errorf("expected %s in the exposition", name)
		}
	}
}

func TestObserveRecordsDuration(t *testing.T) {
	m := newMetrics()
	m.observe(http.MethodGet, "/health", http.StatusOK, 12*time.Millisecond)

	if n := testutil.CollectAndCount(m.requestDuration); n != 1 {
		t.Fatalf("expected one duration series, got %d", n)
	}
	if got := testutil.ToFloat64(m.requestsTotal.WithLabelValues(http.MethodGet, "/health", "200")); got != 1 {
		t.Errorf("expected the request to be counted once, got %v", got)
	}
}

// TestRunDrainsOnSignal checks the shutdown path returns cleanly, since a
// rollout terminates pods on every canary step.
func TestRunDrainsOnSignal(t *testing.T) {
	// Grab a free port and hand it straight back, so the server under test binds
	// to something the OS just confirmed was available.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("releasing the port: %v", err)
	}

	srv := &http.Server{Addr: addr, Handler: newTestServer(t).routes()}

	done := make(chan error, 1)
	go func() { done <- run(srv, "test", 0) }()

	deadline := time.After(3 * time.Second)
	for {
		if _, err := http.Get("http://" + addr + "/health"); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("server never became reachable")
		case err := <-done:
			t.Fatalf("server exited early: %v", err)
		default:
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("run returned %v, want nil", err)
	}
}
