package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// upstreamClient sends each accepted order on to another service before this
// one answers. Several copies of this service can then form a call chain, such
// as frontend to checkout to inventory. A fault in one copy then shows up in
// the copies that call it. platform-mcp's incident gym builds its incidents
// this way. With UPSTREAM_URL unset there is no client and nothing changes.
type upstreamClient struct {
	name      string
	ordersURL string
	http      *http.Client
	metrics   *metrics
}

const defaultUpstreamTimeout = 2 * time.Second

// upstreamFromEnv reads UPSTREAM_URL, UPSTREAM_NAME and UPSTREAM_TIMEOUT. The
// name labels the upstream metrics. It defaults to the first part of the host,
// so http://inventory.shop.svc becomes "inventory".
func upstreamFromEnv(m *metrics) (*upstreamClient, error) {
	raw := strings.TrimSpace(os.Getenv("UPSTREAM_URL"))
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("UPSTREAM_URL %q is not an http or https URL", raw)
	}
	name := strings.TrimSpace(os.Getenv("UPSTREAM_NAME"))
	if name == "" {
		name = strings.SplitN(u.Hostname(), ".", 2)[0]
	}
	timeout := defaultUpstreamTimeout
	if v := strings.TrimSpace(os.Getenv("UPSTREAM_TIMEOUT")); v != "" {
		timeout, err = time.ParseDuration(v)
		if err != nil || timeout <= 0 {
			return nil, fmt.Errorf("UPSTREAM_TIMEOUT %q is not a positive duration", v)
		}
	}
	return &upstreamClient{
		name:      name,
		ordersURL: strings.TrimRight(u.String(), "/") + "/api/v1/orders",
		http:      &http.Client{Timeout: timeout},
		metrics:   m,
	}, nil
}

// placeOrder forwards the order and reports whether the upstream accepted it.
// Any answer other than 2xx counts as a failure, and so does a transport
// error. Transport errors get the code label "error".
func (c *upstreamClient) placeOrder(ctx context.Context, order orderRequest) error {
	body, err := json.Marshal(order)
	if err != nil {
		return err
	}
	start := time.Now()
	code, err := c.post(ctx, body)
	c.metrics.upstreamRequests.WithLabelValues(c.name, code).Inc()
	c.metrics.upstreamDuration.WithLabelValues(c.name).Observe(time.Since(start).Seconds())
	if err != nil {
		return err
	}
	if code[0] != '2' {
		return fmt.Errorf("upstream %s answered %s", c.name, code)
	}
	return nil
}

func (c *upstreamClient) post(ctx context.Context, body []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ordersURL, bytes.NewReader(body))
	if err != nil {
		return "error", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "error", fmt.Errorf("upstream %s: %w", c.name, err)
	}
	// Drain the body so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	return strconv.Itoa(resp.StatusCode), nil
}
