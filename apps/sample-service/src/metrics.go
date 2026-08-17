package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// metrics holds every series the service exports. The canary analysis in
// charts/sample-service/templates/analysistemplate.yaml reads requestsTotal, so
// the label set here is part of the delivery contract: adding a high-cardinality
// label would break the query that decides whether a rollout proceeds.
type metrics struct {
	registry        *prometheus.Registry
	requestsTotal   *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
	ordersCreated   prometheus.Counter
	ordersRejected  *prometheus.CounterVec
	orderValueCents prometheus.Histogram
}

func newMetrics() *metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &metrics{
		registry: reg,
		requestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total HTTP requests by route and response status.",
		}, []string{"method", "route", "status"}),
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency by route.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
		}, []string{"method", "route"}),
		ordersCreated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "orders_created_total",
			Help: "Orders accepted and priced.",
		}),
		ordersRejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "orders_rejected_total",
			Help: "Orders rejected, by rejection reason.",
		}, []string{"reason"}),
		orderValueCents: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "order_value_cents",
			Help:    "Total value of accepted orders, in cents.",
			Buckets: prometheus.ExponentialBuckets(100, 3, 8),
		}),
	}

	// Publish every rejection reason at zero so a query for a reason that has
	// not happened yet returns 0 instead of no data.
	for _, reason := range rejectionReasons {
		m.ordersRejected.WithLabelValues(reason)
	}

	reg.MustRegister(
		m.requestsTotal,
		m.requestDuration,
		m.ordersCreated,
		m.ordersRejected,
		m.orderValueCents,
	)
	return m
}

// observe records one request against a fixed route label.
func (m *metrics) observe(method, route string, status int, d time.Duration) {
	m.requestsTotal.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.requestDuration.WithLabelValues(method, route).Observe(d.Seconds())
}

// routeLabel turns a ServeMux pattern into a bounded metric label. Requests that
// match nothing share the "unmatched" label, so a scanner hitting random URLs
// cannot mint a series per path.
func routeLabel(pattern string) string {
	if pattern == "" {
		return "unmatched"
	}
	if i := strings.IndexByte(pattern, ' '); i >= 0 {
		return pattern[i+1:]
	}
	return pattern
}

// middleware counts and times every request using the pattern the mux matched,
// looked up before dispatch so the label comes from the route table rather than
// the URL. Requests matching skipPattern are served without being counted; the
// scrape endpoint uses this, because counting scrapes would inflate the success
// rate the canary analysis reads.
func (m *metrics) middleware(mux *http.ServeMux, skipPattern string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler, pattern := mux.Handler(r)
		if pattern == skipPattern {
			handler.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		handler.ServeHTTP(rw, r)
		m.observe(r.Method, routeLabel(pattern), rw.status, time.Since(start))
	})
}
