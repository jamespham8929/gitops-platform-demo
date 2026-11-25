package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

const validOrder = `{"customer_id":"c1","items":[{"sku":"widget","quantity":2,"unit_cents":500}]}`

// chain builds a caller whose upstream is a real copy of this service, the way
// the incident gym runs it.
func chain(t *testing.T, upstreamFailureRate float64) (*server, *server) {
	t.Helper()
	inventory := newServer("inventory", newFaultInjector(upstreamFailureRate))
	inventory.nextID = func() string { return "ord_up" }
	ts := httptest.NewServer(inventory.routes())
	t.Cleanup(ts.Close)

	checkout := newTestServer(t)
	t.Setenv("UPSTREAM_URL", ts.URL)
	t.Setenv("UPSTREAM_NAME", "inventory")
	up, err := upstreamFromEnv(checkout.metrics)
	if err != nil {
		t.Fatal(err)
	}
	checkout.upstream = up
	return checkout, inventory
}

func TestOrderGoesThroughTheUpstream(t *testing.T) {
	checkout, inventory := chain(t, 0)
	rec := doRequest(t, checkout.routes(), http.MethodPost, "/api/v1/orders", validOrder)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if got := testutil.ToFloat64(inventory.metrics.ordersCreated); got != 1 {
		t.Errorf("the upstream should have accepted the order once, got %v", got)
	}
	if got := testutil.ToFloat64(checkout.metrics.upstreamRequests.WithLabelValues("inventory", "201")); got != 1 {
		t.Errorf("upstream_requests_total{code=201} = %v, want 1", got)
	}
	if n := testutil.CollectAndCount(checkout.metrics.upstreamDuration); n != 1 {
		t.Errorf("want one upstream latency series, got %d", n)
	}
}

func TestUpstreamFailureIsA502(t *testing.T) {
	checkout, _ := chain(t, 1)
	rec := doRequest(t, checkout.routes(), http.MethodPost, "/api/v1/orders", validOrder)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["reason"] != reasonUpstream {
		t.Errorf("reason %q, want %q", body["reason"], reasonUpstream)
	}
	if got := testutil.ToFloat64(checkout.metrics.upstreamRequests.WithLabelValues("inventory", "500")); got != 1 {
		t.Errorf("upstream_requests_total{code=500} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(checkout.metrics.ordersRejected.WithLabelValues(reasonUpstream)); got != 1 {
		t.Errorf("orders_rejected_total{reason=upstream_unavailable} = %v, want 1", got)
	}
	// The 502 must land in http_requests_total, because the gym's page and the
	// SLO rules both read it.
	if got := testutil.ToFloat64(checkout.metrics.requestsTotal.WithLabelValues("POST", "/api/v1/orders", "502")); got != 1 {
		t.Errorf("http_requests_total{code=502} = %v, want 1", got)
	}
}

func TestUnreachableUpstreamCountsAsError(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close() // nothing listens there now

	s := newTestServer(t)
	t.Setenv("UPSTREAM_URL", url)
	up, err := upstreamFromEnv(s.metrics)
	if err != nil {
		t.Fatal(err)
	}
	s.upstream = up
	rec := doRequest(t, s.routes(), http.MethodPost, "/api/v1/orders", validOrder)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", rec.Code)
	}
	if got := testutil.ToFloat64(s.metrics.upstreamRequests.WithLabelValues("127", "error")); got != 1 {
		t.Errorf("upstream_requests_total{code=error} = %v, want 1", got)
	}
}

func TestSlowUpstreamTimesOut(t *testing.T) {
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); ts.Close() })

	s := newTestServer(t)
	t.Setenv("UPSTREAM_URL", ts.URL)
	t.Setenv("UPSTREAM_NAME", "slow")
	t.Setenv("UPSTREAM_TIMEOUT", "50ms")
	up, err := upstreamFromEnv(s.metrics)
	if err != nil {
		t.Fatal(err)
	}
	s.upstream = up
	start := time.Now()
	rec := doRequest(t, s.routes(), http.MethodPost, "/api/v1/orders", validOrder)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", rec.Code)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("the timeout did not apply, took %s", took)
	}
	if got := testutil.ToFloat64(s.metrics.upstreamRequests.WithLabelValues("slow", "error")); got != 1 {
		t.Errorf("upstream_requests_total{code=error} = %v, want 1", got)
	}
}

func TestInvalidOrderNeverReachesTheUpstream(t *testing.T) {
	checkout, inventory := chain(t, 0)
	rec := doRequest(t, checkout.routes(), http.MethodPost, "/api/v1/orders", `{"customer_id":"c1","items":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if got := testutil.ToFloat64(inventory.metrics.requestsTotal.WithLabelValues("POST", "/api/v1/orders", "201")); got != 0 {
		t.Errorf("the upstream saw %v orders, want 0", got)
	}
}

func TestUpstreamFromEnv(t *testing.T) {
	cases := []struct {
		name, url, upName, timeout string
		wantName, wantURL, wantErr string
		wantTimeout                time.Duration
	}{
		{name: "unset"},
		{name: "name from host", url: "http://inventory.shop.svc/",
			wantName: "inventory", wantURL: "http://inventory.shop.svc/api/v1/orders", wantTimeout: defaultUpstreamTimeout},
		{name: "explicit name and timeout", url: "https://pay.example.com:8443", upName: "payments", timeout: "750ms",
			wantName: "payments", wantURL: "https://pay.example.com:8443/api/v1/orders", wantTimeout: 750 * time.Millisecond},
		{name: "not http", url: "ftp://inventory", wantErr: "not an http or https URL"},
		{name: "no host", url: "inventory", wantErr: "not an http or https URL"},
		{name: "bad timeout", url: "http://inventory", timeout: "soon", wantErr: "not a positive duration"},
		{name: "zero timeout", url: "http://inventory", timeout: "0s", wantErr: "not a positive duration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UPSTREAM_URL", tc.url)
			t.Setenv("UPSTREAM_NAME", tc.upName)
			t.Setenv("UPSTREAM_TIMEOUT", tc.timeout)
			up, err := upstreamFromEnv(newMetrics())
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.url == "" {
				if up != nil {
					t.Fatal("want no client when UPSTREAM_URL is unset")
				}
				return
			}
			if up.name != tc.wantName || up.ordersURL != tc.wantURL || up.http.Timeout != tc.wantTimeout {
				t.Errorf("got %q %q %s", up.name, up.ordersURL, up.http.Timeout)
			}
		})
	}
}
