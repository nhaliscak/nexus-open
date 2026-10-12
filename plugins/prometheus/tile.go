package main

import (
	"fmt"
	"math"
	"strings"
)

// reducer collapses a vector into the single number a tile shows.
type reducer string

const (
	reduceMax   reducer = "max"
	reduceMin   reducer = "min"
	reduceAvg   reducer = "avg"
	reduceSum   reducer = "sum"
	reduceCount reducer = "count"
)

func (r reducer) apply(samples []sample) (float64, error) {
	switch {
	case r == reduceCount:
		return float64(len(samples)), nil
	case r == reduceSum && len(samples) == 0:
		// Problem queries ("pods pending", "restarts > 0") return nothing
		// when healthy; their sum is zero, not an error.
		return 0, nil
	}
	if len(samples) == 0 {
		return 0, fmt.Errorf("query returned no series")
	}
	acc := samples[0].Value
	for _, s := range samples[1:] {
		switch r {
		case reduceMax:
			acc = math.Max(acc, s.Value)
		case reduceMin:
			acc = math.Min(acc, s.Value)
		case reduceAvg, reduceSum:
			acc += s.Value
		default:
			return 0, fmt.Errorf("unknown reduce %q", r)
		}
	}
	if r == reduceAvg {
		acc /= float64(len(samples))
	}
	return acc, nil
}

// extreme returns the series with the highest value, or the lowest when
// lowest is true. Used to name the node behind a cluster-wide number.
func extreme(samples []sample, lowest bool) (sample, bool) {
	if len(samples) == 0 {
		return sample{}, false
	}
	best := samples[0]
	for _, s := range samples[1:] {
		if (lowest && s.Value < best.Value) || (!lowest && s.Value > best.Value) {
			best = s
		}
	}
	return best, true
}

// shortLabel trims a host:port instance label to the host, since a 160 px
// tile has no room for ports.
func shortLabel(v string) string {
	if i := strings.LastIndexByte(v, ':'); i > 0 && !strings.Contains(v[i:], "]") {
		return v[:i]
	}
	return v
}

// thresholds classify a value into ok / warn / crit. A zero threshold is
// unset. With lowerIsWorse, values at or below the threshold trip it (e.g.
// Ready node count).
type thresholds struct {
	warn, crit   float64
	lowerIsWorse bool
}

func (t thresholds) classify(v float64) string {
	trips := func(limit float64) bool {
		if limit == 0 {
			return false
		}
		if t.lowerIsWorse {
			return v <= limit
		}
		return v >= limit
	}
	switch {
	case trips(t.crit):
		return "crit"
	case trips(t.warn):
		return "warn"
	default:
		return "ok"
	}
}

func formatValue(v float64, decimals int) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "—"
	}
	return fmt.Sprintf("%.*f", decimals, v)
}

var severityRank = map[string]int{"ok": 0, "warn": 1, "crit": 2}

// worse returns the more severe of two severities.
func worse(a, b string) string {
	if severityRank[b] > severityRank[a] {
		return b
	}
	return a
}
