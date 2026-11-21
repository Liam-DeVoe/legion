package notify

import (
	"testing"

	"github.com/sjawhar/envoy/oauth2call/oauth2calltest"
)

// The box print test of this package's holders (oauth2calltest.PrintTest): each holds the probe in
// every box field, and its twins have the same shape with the probe as a string there.
func TestEachHolderPrintsNoSecretByAnyPrinterAtAnyPlacement(t *testing.T) {
	type notDeliveredTwin = struct {
		Reason  string
		Holder  string
		EventID string
	}
	type refusedTwin = struct{ Holder string }
	type deniedTwin = struct{ Message string }
	said := oauth2calltest.Said(oauth2calltest.Words)
	for _, h := range []oauth2calltest.Holder{
		{
			Name:     "notify.ErrNotDelivered",
			Kind:     "fields",
			Boxed:    ErrNotDelivered{Reason: ReasonReceiptTimeout, Holder: said, EventID: said},
			Same:     notDeliveredTwin{Reason: ReasonReceiptTimeout, Holder: oauth2calltest.Words, EventID: oauth2calltest.Words},
			Exported: notDeliveredTwin{Reason: ReasonReceiptTimeout, Holder: oauth2calltest.Words, EventID: oauth2calltest.Words},
		},
		{
			Name:     "notify.ErrRefusedByHolder",
			Kind:     "fields",
			Boxed:    ErrRefusedByHolder{Holder: said},
			Same:     refusedTwin{Holder: oauth2calltest.Words},
			Exported: refusedTwin{Holder: oauth2calltest.Words},
		},
		{
			Name:     "notify.ErrPublishDenied",
			Kind:     "fields",
			Boxed:    ErrPublishDenied{Message: said},
			Same:     deniedTwin{Message: oauth2calltest.Words},
			Exported: deniedTwin{Message: oauth2calltest.Words},
		},
	} {
		t.Run(h.Name, func(t *testing.T) { oauth2calltest.PrintTest(t, h) })
	}
}
