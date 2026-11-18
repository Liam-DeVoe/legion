package nolisting

import (
	"os"
	"testing"
)

// It reads its own process's entry, which is no listing of /proc.
func TestReadsItsOwnEntry(t *testing.T) {
	if _, err := os.ReadFile("/proc/self/stat"); err != nil {
		t.Skip(err)
	}
}
