package main

import "testing"

func TestNewFaultInjectorClampsRate(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{-1, 0},
		{0, 0},
		{0.3, 0.3},
		{1, 1},
		{2, 1},
	}
	for _, tc := range cases {
		if got := newFaultInjector(tc.in).rate; got != tc.want {
			t.Errorf("newFaultInjector(%v).rate = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestFaultInjectorTrip(t *testing.T) {
	cases := []struct {
		name   string
		rate   float64
		sample float64
		want   bool
	}{
		{"disabled never trips", 0, 0, false},
		{"sample below rate trips", 0.5, 0.4, true},
		{"sample above rate passes", 0.5, 0.6, false},
		{"sample at rate passes", 0.5, 0.5, false},
		{"always trips", 1, 0.99, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &faultInjector{rate: tc.rate, sample: func() float64 { return tc.sample }}
			if got := f.trip(); got != tc.want {
				t.Errorf("trip() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFaultRateFromEnv(t *testing.T) {
	cases := []struct {
		name string
		set  bool
		val  string
		want float64
	}{
		{"unset defaults to zero", false, "", 0},
		{"empty defaults to zero", true, "", 0},
		{"parses a fraction", true, "0.25", 0.25},
		{"unparseable defaults to zero", true, "half", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("FAILURE_RATE", tc.val)
			}
			if got := faultRateFromEnv(); got != tc.want {
				t.Errorf("faultRateFromEnv() = %v, want %v", got, tc.want)
			}
		})
	}
}
