//go:build (!linux && !darwin) || (!amd64 && !arm64)

package main

import (
	"errors"
	"testing"
	"time"
)

func TestUnsupportedValuePromptRefusesWithoutReading(t *testing.T) {
	// Not parallel: it signals the whole test process or changes its signal handling (readHiddenAtTerminal, stop, signal.Stop); it sets the package variable clientConfig, which the package's other tests read (readHiddenAtTerminal, watchPromptSignals, run, loadClientConfig, clientConfig =).
	line, err := readHiddenAtTerminal(-1, time.Time{}, func() { t.Fatal("unsupported prompt showed its label") }, func(error) { t.Fatal("unsupported prompt called onDiscard") })
	if line != nil || !errors.Is(err, errNoValuePrompt) {
		t.Fatalf("unsupported prompt returned %q, %v; want %v", line, err, errNoValuePrompt)
	}
}
