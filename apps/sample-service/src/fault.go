package main

import (
	"math/rand/v2"
	"os"
	"strconv"
)

// faultInjector turns a fraction of order requests into 500s. It exists so the
// canary rollback can be demonstrated end to end: ship an image with
// FAILURE_RATE set, watch the AnalysisTemplate's success-rate query drop below
// its threshold, and watch Argo Rollouts abort the rollout on its own.
type faultInjector struct {
	rate   float64
	sample func() float64
}

func newFaultInjector(rate float64) *faultInjector {
	if rate < 0 {
		rate = 0
	}
	if rate > 1 {
		rate = 1
	}
	return &faultInjector{rate: rate, sample: rand.Float64}
}

// faultRateFromEnv reads FAILURE_RATE, defaulting to zero when unset or
// unparseable so a typo cannot silently break a production deploy.
func faultRateFromEnv() float64 {
	raw := os.Getenv("FAILURE_RATE")
	if raw == "" {
		return 0
	}
	rate, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return rate
}

func (f *faultInjector) trip() bool {
	if f.rate <= 0 {
		return false
	}
	return f.sample() < f.rate
}
