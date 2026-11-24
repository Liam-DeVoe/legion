package workflow

import (
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/texts"
)

// A design-changes-requested notice is of the request its document's gate records as told where
// it names the told version and the told reason as the notice carries it: cut to texts.OneAct
// (ChangesRequestedNotice), or whole, as a row an older daemon queued carries it. Any other
// reason or version is of a request the person has since replaced, and a gate that records no
// request, or no gate of the notice's document, says nothing against the notice.
func TestChangesSupersededReadsTheNoticesReasonAsItsCutOfTheToldReason(t *testing.T) {
	const issue = "LEGION-208"
	reason := strings.Repeat("r", texts.OneAct+1)
	cut, err := record.NoticeOf(ChangesRequestedNotice(issue, record.DesignStepSpec, 3, reason, "user:alice", time.Date(2026, 11, 24, 9, 30, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("build the changes-requested notice: %v", err)
	}
	if cut.Reason != reason[:texts.OneAct] {
		t.Fatalf("the notice carries a reason of %d units, want the first %d of the told reason's %d", len(cut.Reason), texts.OneAct, len(reason))
	}
	told := func(version int, reason string) map[record.DesignStep]record.DesignGate {
		return map[record.DesignStep]record.DesignGate{record.DesignStepSpec: {Issue: issue, Step: record.DesignStepSpec, ToldChangesVersion: &version, ToldChangesReason: reason}}
	}
	changes := func(version int, reason string) record.Notice {
		return record.Notice{Kind: texts.DesignChangesRequested, Issue: issue, Version: version, Reason: reason, Ref: stepRef(issue, record.DesignStepSpec)}
	}
	for _, tc := range []struct {
		name     string
		gates    map[record.DesignStep]record.DesignGate
		notice   record.Notice
		replaced bool
	}{
		{"a reason over the bound, as the notice cuts it", told(3, reason), cut, false},
		{"a reason over the bound, whole, as an older daemon queued it", told(3, reason), changes(3, reason), false},
		{"a reason inside the bound", told(3, "name the rollback path"), changes(3, "name the rollback path"), false},
		{"another reason at the told version", told(3, "name the rollback path"), changes(3, "cut the migration"), true},
		{"a cut of another reason over the bound", told(3, reason), changes(3, strings.Repeat("s", texts.OneAct)), true},
		{"a shorter head of the told reason", told(3, reason), changes(3, reason[:texts.OneAct-1]), true},
		{"an older version with the told reason cut", told(3, reason), changes(2, reason[:texts.OneAct]), true},
		{"a gate that records no request", map[record.DesignStep]record.DesignGate{record.DesignStepSpec: {Issue: issue, Step: record.DesignStepSpec}},
			changes(2, "cut the migration"), false},
		{"no gate of the notice's document", map[record.DesignStep]record.DesignGate{}, changes(2, "cut the migration"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ChangesSuperseded(tc.gates, issue, tc.notice); got != tc.replaced {
				t.Errorf("ChangesSuperseded = %t, want %t", got, tc.replaced)
			}
		})
	}
}
