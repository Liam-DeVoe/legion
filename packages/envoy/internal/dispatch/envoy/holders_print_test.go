package envoy

import (
	"fmt"
	"testing"

	"github.com/sjawhar/envoy/internal/redact"
	"github.com/sjawhar/envoy/internal/redact/redacttest"
)

// The box print test of this package's holders: each holds the probe in every box field, and its
// twins have the same shape with the probe as a string there (redacttest.PrintTest).

type refillingErrorTwin struct {
	RetryAfter string
	route      string
	reason     string
}

func (e *refillingErrorTwin) Error() string {
	return fmt.Sprintf("%s returned 503: %s", e.route, e.reason)
}

type refillingErrorExported struct {
	RetryAfter string
	Route      string
	Reason     string
}

func TestEachHolderPrintsNoSecretByAnyPrinterAtAnyPlacement(t *testing.T) {
	code, words := redact.NewRemoteText(redacttest.Code), redact.NewRemoteText(redacttest.Words)
	type errorTwin = struct {
		Error string `json:"error"`
	}
	type lapsedTwin = struct {
		Error       string      `json:"error"`
		Holder      string      `json:"holder"`
		HolderState holderState `json:"holder_state"`
	}
	type listTwin = struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	errorBody := func(name string) redacttest.Holder {
		return redacttest.Holder{
			Name: name,
			Kind: "fields",
			Boxed: struct {
				Error redact.RemoteText `json:"error"`
			}{words},
			Same:     errorTwin{redacttest.Words},
			Exported: errorTwin{redacttest.Words},
		}
	}
	for _, h := range []redacttest.Holder{
		{
			Name:     "envoy.RefillingError",
			Kind:     "error",
			Boxed:    &RefillingError{RetryAfter: "1", route: "/v1/sessions", reason: words},
			Same:     &refillingErrorTwin{"1", "/v1/sessions", redacttest.Words},
			Exported: &refillingErrorExported{"1", "/v1/sessions", redacttest.Words},
		},
		{
			// The refusal Client.lapsedRoleHolder decodes.
			Name: "envoy.Client.lapsedRoleHolder.refusal",
			Kind: "fields",
			Boxed: struct {
				Error       redact.RemoteText `json:"error"`
				Holder      redact.RemoteText `json:"holder"`
				HolderState holderState       `json:"holder_state"`
			}{Error: words, Holder: code, HolderState: holderStateUnresponsive},
			Same:     lapsedTwin{redacttest.Words, redacttest.Code, holderStateUnresponsive},
			Exported: lapsedTwin{redacttest.Words, redacttest.Code, holderStateUnresponsive},
		},
		// The failures unavailableGetStatus and listenerResponseError decode.
		errorBody("envoy.unavailableGetStatus.failure"),
		errorBody("envoy.listenerResponseError.body"),
		{
			// The body listFailure decodes.
			Name: "envoy.listFailure.body",
			Kind: "fields",
			Boxed: struct {
				Error redact.RemoteText `json:"error"`
				Code  redact.RemoteText `json:"code"`
			}{words, code},
			Same:     listTwin{redacttest.Words, redacttest.Code},
			Exported: listTwin{redacttest.Words, redacttest.Code},
		},
	} {
		t.Run(h.Name, func(t *testing.T) { redacttest.PrintTest(t, h) })
	}
}
