package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Pricing and validation limits. These are the rules the canary analysis
// exercises: a build that changes one of them changes the 4xx rate, which is
// exactly the kind of regression the rollout should catch.
const (
	maxLineItems     = 50
	maxQuantity      = 100
	maxUnitCents     = 1_000_000
	maxOrderCents    = 5_000_000
	taxBasisPoints   = 875 // 8.75%
	freeShipAtCents  = 5_000
	shippingFeeCents = 599
)

// Rejection reasons, used both in the API response and as the `reason` label on
// orders_rejected_total. Keep the set small and closed so the label stays
// bounded.
const (
	reasonMalformed     = "malformed_body"
	reasonNoItems       = "no_items"
	reasonTooManyItems  = "too_many_items"
	reasonBadQuantity   = "bad_quantity"
	reasonBadPrice      = "bad_price"
	reasonMissingSKU    = "missing_sku"
	reasonUnknownCoupon = "unknown_coupon"
	reasonCouponMinimum = "coupon_minimum_not_met"
	reasonOrderTooLarge = "order_too_large"
	reasonInjected      = "injected_fault"
)

// coupon is a percentage discount that only applies above a subtotal floor.
type coupon struct {
	percentOff  int
	minSubtotal int
}

var coupons = map[string]coupon{
	"SAVE10":  {percentOff: 10, minSubtotal: 2_000},
	"SAVE25":  {percentOff: 25, minSubtotal: 10_000},
	"WELCOME": {percentOff: 5, minSubtotal: 0},
}

// rejectionReasons lists every value the `reason` label can take, so the metric
// can be published at zero before the first rejection happens.
var rejectionReasons = []string{
	reasonMalformed, reasonNoItems, reasonTooManyItems, reasonBadQuantity,
	reasonBadPrice, reasonMissingSKU, reasonUnknownCoupon, reasonCouponMinimum,
	reasonOrderTooLarge, reasonInjected,
}

type lineItem struct {
	SKU       string `json:"sku"`
	Quantity  int    `json:"quantity"`
	UnitCents int    `json:"unit_cents"`
}

type orderRequest struct {
	CustomerID string     `json:"customer_id"`
	Items      []lineItem `json:"items"`
	Coupon     string     `json:"coupon,omitempty"`
}

type orderResponse struct {
	OrderID       string `json:"order_id"`
	CustomerID    string `json:"customer_id"`
	SubtotalCents int    `json:"subtotal_cents"`
	DiscountCents int    `json:"discount_cents"`
	ShippingCents int    `json:"shipping_cents"`
	TaxCents      int    `json:"tax_cents"`
	TotalCents    int    `json:"total_cents"`
}

// rejection carries both the HTTP status and the metric label for a refused
// order. Validation faults are 400; rules the caller could satisfy with a
// different order are 422.
type rejection struct {
	status int
	reason string
	detail string
}

func (e *rejection) Error() string { return e.detail }

func badRequest(reason, format string, args ...any) *rejection {
	return &rejection{status: http.StatusBadRequest, reason: reason, detail: fmt.Sprintf(format, args...)}
}

func unprocessable(reason, format string, args ...any) *rejection {
	return &rejection{status: http.StatusUnprocessableEntity, reason: reason, detail: fmt.Sprintf(format, args...)}
}

// price applies the ordering rules and returns the priced order. Every failure
// path returns a *rejection so the caller can label the metric without
// re-deriving why the order failed.
func price(req orderRequest, orderID string) (orderResponse, error) {
	switch {
	case len(req.Items) == 0:
		return orderResponse{}, badRequest(reasonNoItems, "order must contain at least one item")
	case len(req.Items) > maxLineItems:
		return orderResponse{}, badRequest(reasonTooManyItems, "order has %d line items, limit is %d", len(req.Items), maxLineItems)
	}

	subtotal := 0
	for i, item := range req.Items {
		if strings.TrimSpace(item.SKU) == "" {
			return orderResponse{}, badRequest(reasonMissingSKU, "item %d has no sku", i)
		}
		if item.Quantity < 1 || item.Quantity > maxQuantity {
			return orderResponse{}, badRequest(reasonBadQuantity, "item %s quantity %d outside 1..%d", item.SKU, item.Quantity, maxQuantity)
		}
		if item.UnitCents < 1 || item.UnitCents > maxUnitCents {
			return orderResponse{}, badRequest(reasonBadPrice, "item %s unit price %d outside 1..%d", item.SKU, item.UnitCents, maxUnitCents)
		}
		subtotal += item.Quantity * item.UnitCents
	}

	discount := 0
	if code := strings.ToUpper(strings.TrimSpace(req.Coupon)); code != "" {
		c, ok := coupons[code]
		if !ok {
			return orderResponse{}, unprocessable(reasonUnknownCoupon, "coupon %s is not recognised", code)
		}
		if subtotal < c.minSubtotal {
			return orderResponse{}, unprocessable(reasonCouponMinimum,
				"coupon %s needs a subtotal of %d cents, order is %d", code, c.minSubtotal, subtotal)
		}
		discount = subtotal * c.percentOff / 100
	}

	discounted := subtotal - discount
	shipping := shippingFeeCents
	if discounted >= freeShipAtCents {
		shipping = 0
	}
	tax := (discounted + shipping) * taxBasisPoints / 10_000
	total := discounted + shipping + tax

	if total > maxOrderCents {
		return orderResponse{}, unprocessable(reasonOrderTooLarge,
			"order total %d cents exceeds the %d limit", total, maxOrderCents)
	}

	return orderResponse{
		OrderID:       orderID,
		CustomerID:    req.CustomerID,
		SubtotalCents: subtotal,
		DiscountCents: discount,
		ShippingCents: shipping,
		TaxCents:      tax,
		TotalCents:    total,
	}, nil
}

// orderHandler decodes, prices, and records an order. The injector runs before
// pricing so a canary build can be made to fail on the same path a real
// dependency outage would.
func (s *server) orderHandler(w http.ResponseWriter, r *http.Request) {
	if s.faults.trip() {
		s.metrics.ordersRejected.WithLabelValues(reasonInjected).Inc()
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "order pricing unavailable",
			"reason": reasonInjected,
		})
		return
	}

	var req orderRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		s.reject(w, badRequest(reasonMalformed, "request body is not a valid order: %v", err))
		return
	}

	order, err := price(req, s.nextID())
	if err != nil {
		var rej *rejection
		if errors.As(err, &rej) {
			s.reject(w, rej)
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	s.metrics.ordersCreated.Inc()
	s.metrics.orderValueCents.Observe(float64(order.TotalCents))
	writeJSON(w, http.StatusCreated, order)
}

func (s *server) reject(w http.ResponseWriter, rej *rejection) {
	s.metrics.ordersRejected.WithLabelValues(rej.reason).Inc()
	writeJSON(w, rej.status, map[string]string{"error": rej.detail, "reason": rej.reason})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
