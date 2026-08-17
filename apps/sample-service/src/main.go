package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metricsPattern is both the route and the pattern excluded from request
// metrics.
const metricsPattern = "GET /metrics"

// server carries everything a handler needs. The ID source and the fault
// injector are fields rather than package globals so tests can pin both.
type server struct {
	metrics   *metrics
	faults    *faultInjector
	nextID    func() string
	version   string
	startTime time.Time
}

func newServer(version string, faults *faultInjector) *server {
	return &server{
		metrics:   newMetrics(),
		faults:    faults,
		nextID:    func() string { return fmt.Sprintf("ord_%012x", rand.Uint64()&0xffffffffffff) },
		version:   version,
		startTime: time.Now(),
	}
}

// routes returns the fully wired handler chain. Instrumentation sits outside the
// mux so the metric label is the matched pattern, which keeps method-not-allowed
// and not-found behaviour intact instead of swallowing them in a catch-all
// route.
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", s.healthHandler)
	mux.HandleFunc("GET /ready", s.readyHandler)
	mux.HandleFunc("GET /api/v1/info", s.infoHandler)
	mux.HandleFunc("POST /api/v1/orders", s.orderHandler)
	mux.Handle(metricsPattern, promhttp.HandlerFor(s.metrics.registry, promhttp.HandlerOpts{}))

	return loggingMiddleware(s.metrics.middleware(mux, metricsPattern))
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	faultRate := faultRateFromEnv()
	srv := newServer(os.Getenv("APP_VERSION"), newFaultInjector(faultRate))

	httpServer := &http.Server{
		Addr:         ":" + port,
		Handler:      srv.routes(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	if err := run(httpServer, srv.version, faultRate); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

// run serves until SIGTERM, then drains in-flight requests. Rollouts kill pods
// constantly during a canary, and a server that drops connections on shutdown
// shows up as a dip in the same success-rate metric the analysis is reading.
func run(httpServer *http.Server, version string, faultRate float64) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("server starting", "addr", httpServer.Addr, "version", version, "failure_rate", faultRate)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutdown signal received, draining")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

func (s *server) healthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) readyHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *server) infoHandler(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":  s.version,
		"uptime":   time.Since(s.startTime).String(),
		"hostname": host,
	})
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}
