// Package oauth2calltest is the box print test (internal/redact/redacttest) for a module outside
// this one, which cannot import an internal package: a type of that module that holds an
// oauth2call.RemoteText is a holder the gate's holder census lists (cmd/redact-gate), and the
// holder's print row runs PrintTest in that module's own package, as a holder of this module runs
// redacttest.PrintTest. It is for tests: Said makes a box with no record of an answer, so the
// answer census lists every call of it outside a test file (gate.answers.makers).
package oauth2calltest

import (
	"testing"

	"github.com/sjawhar/envoy/internal/redact"
	"github.com/sjawhar/envoy/internal/redact/redacttest"
	"github.com/sjawhar/envoy/oauth2call"
)

// Holder is redacttest.Holder: a holder of the box for the print test, with its plain-string twins.
type Holder = redacttest.Holder

// Words is redacttest.Words, the probe: words that quote a URL with a made-up credential in its
// user information and its query.
const Words = redacttest.Words

// PrintTest is redacttest.PrintTest: h, holding the probe, printed by every printer at every
// placement, beside its twins.
func PrintTest(t *testing.T, h Holder) {
	t.Helper()
	redacttest.PrintTest(t, h)
}

// Said is s held as another party's words, for a test that builds the value a client would have
// decoded from an answer: it prints only as the rule reads it and compares as it came.
func Said(s string) oauth2call.RemoteText { return redact.NewRemoteText(s) }
