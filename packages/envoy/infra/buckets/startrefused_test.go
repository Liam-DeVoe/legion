package buckets

import (
	"errors"
	"fmt"
	"testing"

	"github.com/nats-io/jsm.go/api"
	"github.com/nats-io/nats.go"
)

// A restore's start is sorted by the error's type, as jsm.go answers it: an api.ApiError with
// 10130 is retried after the leftover's delete, one with 10049, or nats.ErrNoResponders, is
// retried, and anything else ends the run, an error whose text merely holds 10130 or 10049
// included. The control is the sort by text as it stood, which retried those.
func TestARestoresStartIsSortedByTheErrorsType(t *testing.T) {
	for _, row := range []struct {
		name               string
		err                error
		retry, deleteFirst bool
	}{
		{"10130, the name still taken", api.ApiError{Code: 400, ErrCode: errRestoreNameInUse, Description: "stream name already in use"}, true, true},
		{"10130 wrapped", fmt.Errorf("restore KV_x: %w", api.ApiError{Code: 400, ErrCode: errRestoreNameInUse}), true, true},
		{"10049, the store not made yet", api.ApiError{Code: 500, ErrCode: errCreatingStore}, true, false},
		{"no responders", fmt.Errorf("restore: %w", nats.ErrNoResponders), true, false},
		{"a text holding 10130", errors.New("chunk 10130 of the archive did not write"), false, false},
		{"a text holding 10049", errors.New("read 10049 bytes, want more"), false, false},
		{"another API error", api.ApiError{Code: 400, ErrCode: 10058}, false, false},
	} {
		retry, deleteFirst := startRefused(row.err)
		if retry != row.retry || deleteFirst != row.deleteFirst {
			t.Errorf("%s: startRefused(%v) = retry %t, delete first %t; want %t, %t", row.name, row.err, retry, deleteFirst, row.retry, row.deleteFirst)
		}
	}
}
