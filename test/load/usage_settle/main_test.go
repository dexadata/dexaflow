package main

import (
	"testing"
	"time"
)

func TestMedianAndBest(t *testing.T) {
	// Arrange
	samples := []time.Duration{5 * time.Millisecond, time.Millisecond, 3 * time.Millisecond}

	// Act
	m, b := median(samples), best(samples)

	// Assert
	if m != 3*time.Millisecond || b != time.Millisecond {
		t.Errorf("median, best = %v, %v; want 3ms, 1ms", m, b)
	}
	if samples[0] != 5*time.Millisecond {
		t.Error("median reordered its input")
	}
}
