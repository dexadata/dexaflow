package main

import "testing"

// TestLowDisk pins the datastore low-disk threshold check used by the janitor.
func TestLowDisk(t *testing.T) {
	const threshold = 1 << 30 // 1 GiB
	if !lowDisk(500<<20, threshold) {
		t.Error("500 MiB free must be flagged low")
	}
	if lowDisk(2<<30, threshold) {
		t.Error("2 GiB free must not be flagged low")
	}
	if lowDisk(threshold, threshold) {
		t.Error("exactly the threshold is not below it")
	}
}
