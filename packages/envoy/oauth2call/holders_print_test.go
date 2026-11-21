package oauth2call

import (
	"testing"

	"github.com/sjawhar/envoy/internal/redact"
	"github.com/sjawhar/envoy/internal/redact/redacttest"
)

// The box print test of this package's holders: each holds the probe in every box field, and its
// twins have the same shape with the probe as a string there (redacttest.PrintTest).
func TestEachHolderPrintsNoSecretByAnyPrinterAtAnyPlacement(t *testing.T) {
	type refusalTwin = struct {
		Message string `json:"message"`
	}
	type lookupTwin = struct {
		Holder      string `json:"holder"`
		HolderState string `json:"holder_state"`
		Reason      string `json:"reason"`
		LastSeen    *int64 `json:"last_seen"`
	}
	words := redact.NewRemoteText(redacttest.Words)
	for _, h := range []redacttest.Holder{
		{
			Name:     "oauth2call.GitHubRefusal",
			Kind:     "fields",
			Boxed:    GitHubRefusal{Message: words},
			Same:     refusalTwin{redacttest.Words},
			Exported: refusalTwin{redacttest.Words},
		},
		{
			Name:     "oauth2call.RoleLookup",
			Kind:     "fields",
			Boxed:    RoleLookup{Holder: words, HolderState: words, Reason: words},
			Same:     lookupTwin{Holder: redacttest.Words, HolderState: redacttest.Words, Reason: redacttest.Words},
			Exported: lookupTwin{Holder: redacttest.Words, HolderState: redacttest.Words, Reason: redacttest.Words},
		},
	} {
		t.Run(h.Name, func(t *testing.T) { redacttest.PrintTest(t, h) })
	}
}
