package k8s

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// binarySuffixes and decimalSuffixes are the Kubernetes quantity suffixes.
// Note that "k" is lowercase in the decimal set and "K" is not valid — the
// table is the allowlist, so anything else is rejected rather than guessed.
var binarySuffixes = map[string]float64{
	"Ki": 1 << 10,
	"Mi": 1 << 20,
	"Gi": 1 << 30,
	"Ti": 1 << 40,
	"Pi": 1 << 50,
	"Ei": 1 << 60,
}

var decimalSuffixes = map[string]float64{
	"m": 1e-3,
	"k": 1e3,
	"M": 1e6,
	"G": 1e9,
	"T": 1e12,
	"P": 1e15,
	"E": 1e18,
}

// parseQuantity converts a Kubernetes resource quantity to its value in base
// units (cores for CPU, bytes for memory).
//
// It exists so the renderer can compare a request against its limit and reject
// unbounded or malformed input at the boundary; it is a validator, not a
// general-purpose replacement for the upstream quantity type, and it fails
// closed on anything it does not recognise. Only strictly positive, finite
// quantities are accepted: a zero or negative limit is not a bound.
func parseQuantity(s string) (float64, error) {
	if s == "" {
		return 0, fmt.Errorf("quantity is empty (expected a Kubernetes quantity such as \"500m\", \"2\", or \"512Mi\")")
	}

	num, mult, matched := s, 1.0, false
	if len(s) > 2 {
		if m, ok := binarySuffixes[s[len(s)-2:]]; ok {
			num, mult, matched = s[:len(s)-2], m, true
		}
	}
	if !matched && len(s) > 1 {
		if m, ok := decimalSuffixes[s[len(s)-1:]]; ok {
			num, mult = s[:len(s)-1], m
		}
	}

	// Reject leading/trailing space explicitly: ParseFloat already rejects it,
	// but the error should name the real problem.
	if strings.TrimSpace(num) != num {
		return 0, fmt.Errorf("quantity %q contains whitespace", s)
	}

	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("quantity %q is not a valid Kubernetes quantity (expected a number with an optional suffix: m, k, M, G, T, P, E, Ki, Mi, Gi, Ti, Pi, Ei)", s)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("quantity %q is not a finite number", s)
	}

	value := v * mult
	if value <= 0 {
		return 0, fmt.Errorf("quantity %q must be greater than zero", s)
	}
	return value, nil
}
