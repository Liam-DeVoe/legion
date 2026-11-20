package projectaccess

import (
	"testing"

	"github.com/sjawhar/envoy/internal/redact"
	"github.com/sjawhar/envoy/internal/redact/redacttest"
)

// The box print test of this package's holders: each holds the probe in every box field, and its
// twins have the same shape with the probe as a string there (redacttest.PrintTest).
func TestEachHolderPrintsNoSecretByAnyPrinterAtAnyPlacement(t *testing.T) {
	type refusalTwin = struct {
		Code string `json:"code"`
	}
	for _, h := range []redacttest.Holder{
		{
			// The refusal refusalCode decodes Dispatch's answer into.
			Name: "projectaccess.refusalCode",
			Kind: "fields",
			Boxed: struct {
				Code redact.RemoteText `json:"code"`
			}{redact.NewRemoteText(redacttest.Code)},
			Same:     refusalTwin{redacttest.Code},
			Exported: refusalTwin{redacttest.Code},
		},
	} {
		t.Run(h.Name, func(t *testing.T) { redacttest.PrintTest(t, h) })
	}
}
