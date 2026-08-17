package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPriceAcceptedOrders(t *testing.T) {
	cases := []struct {
		name     string
		req      orderRequest
		subtotal int
		discount int
		shipping int
		tax      int
		total    int
	}{
		{
			name:     "small order pays shipping",
			req:      orderRequest{Items: []lineItem{{SKU: "widget", Quantity: 2, UnitCents: 500}}},
			subtotal: 1000, discount: 0, shipping: 599, tax: 139, total: 1738,
		},
		{
			name:     "coupon applied and free shipping earned",
			req:      orderRequest{Items: []lineItem{{SKU: "widget", Quantity: 1, UnitCents: 10000}}, Coupon: "SAVE10"},
			subtotal: 10000, discount: 1000, shipping: 0, tax: 787, total: 9787,
		},
		{
			name:     "coupon code is case insensitive",
			req:      orderRequest{Items: []lineItem{{SKU: "widget", Quantity: 1, UnitCents: 10000}}, Coupon: " save10 "},
			subtotal: 10000, discount: 1000, shipping: 0, tax: 787, total: 9787,
		},
		{
			name: "multiple line items sum",
			req: orderRequest{Items: []lineItem{
				{SKU: "widget", Quantity: 3, UnitCents: 700},
				{SKU: "gadget", Quantity: 1, UnitCents: 400},
			}},
			subtotal: 2500, discount: 0, shipping: 599, tax: 271, total: 3370,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := price(tc.req, "ord_test")
			if err != nil {
				t.Fatalf("unexpected rejection: %v", err)
			}
			if got.SubtotalCents != tc.subtotal || got.DiscountCents != tc.discount ||
				got.ShippingCents != tc.shipping || got.TaxCents != tc.tax || got.TotalCents != tc.total {
				t.Errorf("got subtotal=%d discount=%d shipping=%d tax=%d total=%d, want %d/%d/%d/%d/%d",
					got.SubtotalCents, got.DiscountCents, got.ShippingCents, got.TaxCents, got.TotalCents,
					tc.subtotal, tc.discount, tc.shipping, tc.tax, tc.total)
			}
			if got.OrderID != "ord_test" {
				t.Errorf("expected order id to be carried through, got %q", got.OrderID)
			}
		})
	}
}

func TestPriceRejections(t *testing.T) {
	tooMany := make([]lineItem, maxLineItems+1)
	for i := range tooMany {
		tooMany[i] = lineItem{SKU: "widget", Quantity: 1, UnitCents: 100}
	}

	cases := []struct {
		name   string
		req    orderRequest
		status int
		reason string
	}{
		{"no items", orderRequest{}, http.StatusBadRequest, reasonNoItems},
		{"too many items", orderRequest{Items: tooMany}, http.StatusBadRequest, reasonTooManyItems},
		{"blank sku", orderRequest{Items: []lineItem{{SKU: "  ", Quantity: 1, UnitCents: 100}}}, http.StatusBadRequest, reasonMissingSKU},
		{"zero quantity", orderRequest{Items: []lineItem{{SKU: "w", Quantity: 0, UnitCents: 100}}}, http.StatusBadRequest, reasonBadQuantity},
		{"quantity over limit", orderRequest{Items: []lineItem{{SKU: "w", Quantity: maxQuantity + 1, UnitCents: 100}}}, http.StatusBadRequest, reasonBadQuantity},
		{"zero price", orderRequest{Items: []lineItem{{SKU: "w", Quantity: 1, UnitCents: 0}}}, http.StatusBadRequest, reasonBadPrice},
		{"price over limit", orderRequest{Items: []lineItem{{SKU: "w", Quantity: 1, UnitCents: maxUnitCents + 1}}}, http.StatusBadRequest, reasonBadPrice},
		{"unknown coupon", orderRequest{Items: []lineItem{{SKU: "w", Quantity: 1, UnitCents: 100}}, Coupon: "BOGUS"}, http.StatusUnprocessableEntity, reasonUnknownCoupon},
		{"coupon minimum not met", orderRequest{Items: []lineItem{{SKU: "w", Quantity: 1, UnitCents: 100}}, Coupon: "SAVE10"}, http.StatusUnprocessableEntity, reasonCouponMinimum},
		{"order too large", orderRequest{Items: []lineItem{{SKU: "w", Quantity: maxQuantity, UnitCents: maxUnitCents}}}, http.StatusUnprocessableEntity, reasonOrderTooLarge},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := price(tc.req, "ord_test")
			rej, ok := err.(*rejection)
			if !ok {
				t.Fatalf("expected a rejection, got %v", err)
			}
			if rej.status != tc.status {
				t.Errorf("expected status %d, got %d", tc.status, rej.status)
			}
			if rej.reason != tc.reason {
				t.Errorf("expected reason %q, got %q", tc.reason, rej.reason)
			}
			if rej.Error() == "" {
				t.Error("expected a non-empty rejection detail")
			}
		})
	}
}

func TestOrderHandlerAccepts(t *testing.T) {
	s := newTestServer(t)
	body := `{"customer_id":"c-1","items":[{"sku":"widget","quantity":2,"unit_cents":500}]}`
	rec := doRequest(t, s.routes(), http.MethodPost, "/api/v1/orders", body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", rec.Code, rec.Body.String())
	}

	var got orderResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.OrderID != "ord_test" || got.CustomerID != "c-1" || got.TotalCents != 1738 {
		t.Errorf("unexpected order response: %+v", got)
	}
	if n := testutil.ToFloat64(s.metrics.ordersCreated); n != 1 {
		t.Errorf("expected orders_created_total to be 1, got %v", n)
	}
}

func TestOrderHandlerRejects(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
		reason string
	}{
		{"malformed json", `{"items":`, http.StatusBadRequest, reasonMalformed},
		{"unknown field", `{"items":[{"sku":"w","quantity":1,"unit_cents":100}],"discount":true}`, http.StatusBadRequest, reasonMalformed},
		{"empty body", ``, http.StatusBadRequest, reasonMalformed},
		{"no items", `{"customer_id":"c-1","items":[]}`, http.StatusBadRequest, reasonNoItems},
		{"unknown coupon", `{"items":[{"sku":"w","quantity":1,"unit_cents":100}],"coupon":"NOPE"}`, http.StatusUnprocessableEntity, reasonUnknownCoupon},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			rec := doRequest(t, s.routes(), http.MethodPost, "/api/v1/orders", tc.body)
			if rec.Code != tc.status {
				t.Fatalf("expected %d, got %d (%s)", tc.status, rec.Code, rec.Body.String())
			}
			if got := decodeMap(t, rec)["reason"]; got != tc.reason {
				t.Errorf("expected reason %q, got %v", tc.reason, got)
			}
			labelled := testutil.ToFloat64(s.metrics.ordersRejected.WithLabelValues(tc.reason))
			if labelled != 1 {
				t.Errorf("expected orders_rejected_total{reason=%q} to be 1, got %v", tc.reason, labelled)
			}
		})
	}
}

// TestOrderHandlerInjectedFault covers the path the canary demo relies on: with
// FAILURE_RATE set, the endpoint returns 500 and the failure lands in
// http_requests_total, which is what the analysis query reads.
func TestOrderHandlerInjectedFault(t *testing.T) {
	s := newTestServer(t)
	s.faults = &faultInjector{rate: 1, sample: func() float64 { return 0 }}

	body := `{"items":[{"sku":"w","quantity":1,"unit_cents":100}]}`
	rec := doRequest(t, s.routes(), http.MethodPost, "/api/v1/orders", body)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if got := decodeMap(t, rec)["reason"]; got != reasonInjected {
		t.Errorf("expected reason %q, got %v", reasonInjected, got)
	}
	counted := testutil.ToFloat64(s.metrics.requestsTotal.WithLabelValues(http.MethodPost, "/api/v1/orders", "500"))
	if counted != 1 {
		t.Errorf("expected the 500 to be counted in http_requests_total, got %v", counted)
	}
}

// TestOrderHandlerRejectsOversizedBody guards the MaxBytesReader limit.
func TestOrderHandlerRejectsOversizedBody(t *testing.T) {
	s := newTestServer(t)
	huge := fmt.Sprintf(`{"customer_id":"%s","items":[{"sku":"w","quantity":1,"unit_cents":100}]}`,
		strings.Repeat("x", 128<<10))
	rec := doRequest(t, s.routes(), http.MethodPost, "/api/v1/orders", huge)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an oversized body, got %d", rec.Code)
	}
}
