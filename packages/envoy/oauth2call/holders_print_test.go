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
	for _, h := range []redacttest.Holder{
		{
			Name:     "oauth2call.GitHubRefusal",
			Kind:     "fields",
			Boxed:    GitHubRefusal{Message: redact.NewRemoteText(redacttest.Words)},
			Same:     refusalTwin{redacttest.Words},
			Exported: refusalTwin{redacttest.Words},
		},
	} {
		t.Run(h.Name, func(t *testing.T) { redacttest.PrintTest(t, h) })
	}
}
