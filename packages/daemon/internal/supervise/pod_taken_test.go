package supervise

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// The store refuses a write of an operator's suspension of a stopped claim while a release takes
// the claim's pod (PodTakenError), and refuses the same write again until the release ends. Such a
// refusal is no write the database did not take: owed, the repair would make it once the pod was
// gone, so no decision owes it, wrapped or not. A deadline the write ran out, which the database
// never answered, is still owed, which shows the rule is read and not bypassed.
func TestAStoreRefusalOfATakenPodIsNotOwed(t *testing.T) {
	taken := &PodTakenError{Issue: "LEGION-208", Release: 7, Sandbox: "legion-legion-legion-208"}
	for _, err := range []error{taken, fmt.Errorf("put claim legion-legion-legion-208-architect: %w", taken)} {
		if writeOwed(err) {
			t.Fatalf("writeOwed(%v) = true, want the refusal not owed: the same write is refused until the release ends", err)
		}
		var refused *PodTakenError
		if !errors.As(err, &refused) || refused.Release != 7 {
			t.Fatalf("the refusal %v does not unwrap to the PodTakenError", err)
		}
	}
	if !writeOwed(context.DeadlineExceeded) {
		t.Fatal("control: a write cut short by its deadline is not owed, so the rule was not read")
	}
}
