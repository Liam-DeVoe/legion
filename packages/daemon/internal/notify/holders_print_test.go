package notify

import (
	"testing"
	"time"

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
	type deletedTwin = struct {
		Deleted bool
		Reason  string
	}
	type deleteAnswerTwin = struct {
		Deleted *bool  `json:"deleted"`
		Reason  string `json:"reason"`
	}
	type registrationTwin = struct {
		Registered       bool   `json:"-"`
		ClientInstanceID string `json:"client_instance_id"`
		LastSeen         int64  `json:"last_seen"`
	}
	type conditionsTwin = struct {
		ClientInstanceID   string
		NotRegisteredAfter time.Time
	}
	kept := false
	at := time.Unix(1762560000, 0).UTC()
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
		{
			Name:     "notify.SessionDeleted",
			Kind:     "fields",
			Boxed:    SessionDeleted{Reason: said},
			Same:     deletedTwin{Reason: oauth2calltest.Words},
			Exported: deletedTwin{Reason: oauth2calltest.Words},
		},
		{
			Name:     "notify.deleteAnswer",
			Kind:     "fields",
			Boxed:    deleteAnswer{Deleted: &kept, Reason: said},
			Same:     deleteAnswerTwin{Deleted: &kept, Reason: oauth2calltest.Words},
			Exported: deleteAnswerTwin{Deleted: &kept, Reason: oauth2calltest.Words},
		},
		{
			Name:     "notify.SessionRegistration",
			Kind:     "fields",
			Boxed:    SessionRegistration{Registered: true, ClientInstanceID: said, LastSeen: 1762560000000},
			Same:     registrationTwin{Registered: true, ClientInstanceID: oauth2calltest.Words, LastSeen: 1762560000000},
			Exported: registrationTwin{Registered: true, ClientInstanceID: oauth2calltest.Words, LastSeen: 1762560000000},
		},
		{
			Name:     "notify.DeleteConditions",
			Kind:     "fields",
			Boxed:    DeleteConditions{ClientInstanceID: said, NotRegisteredAfter: at},
			Same:     conditionsTwin{ClientInstanceID: oauth2calltest.Words, NotRegisteredAfter: at},
			Exported: conditionsTwin{ClientInstanceID: oauth2calltest.Words, NotRegisteredAfter: at},
		},
	} {
		t.Run(h.Name, func(t *testing.T) { oauth2calltest.PrintTest(t, h) })
	}
}
