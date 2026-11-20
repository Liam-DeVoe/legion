package daemon

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// twelfthRefusedInItsTurn is whether the daemon's answer to the relaunch of w5's twelfth claim
// (status, code and body, read at answeredAt) refuses it as a claim whose process still runs its
// turn, as the check's Expected says. That answer is 409 RELAUNCH_GUARD for one of the two rules of
// supervise.RelaunchGuard that read a process in its turn.
//
// One rule is the session's activity report in that turn. The other is the step rule, which the
// guard reads first. On an Oh My Pi build whose wait returns, the twelfth's wait is called again at
// most every 5 minutes, as the check says, so its worker stream shows a step and the step rule
// answers. The step rule counts only where the run saw that step: steps holds the times of the
// steps the twelfth's worker stream carried, and one of them must fall in the second the answer
// names, less than 20 minutes before the answer. A refusal for a step the run never saw does not
// pass, and neither does any other refusal or a relaunch that went ahead.
func twelfthRefusedInItsTurn(status int, code, body string, answeredAt time.Time, steps []time.Time) bool {
	if status != http.StatusConflict || code != "RELAUNCH_GUARD" {
		return false
	}
	if strings.Contains(body, "its session sent an activity report at ") && strings.Contains(body, "so its process still runs that turn") {
		return true
	}
	_, rest, found := strings.Cut(body, "its worker stream showed a step at ")
	if !found {
		return false
	}
	stamp, _, _ := strings.Cut(rest, ",")
	named, err := time.Parse(time.RFC3339, stamp)
	if err != nil || named.After(answeredAt) || answeredAt.Sub(named) >= supervise.UnreachableAfter {
		return false
	}
	return slices.ContainsFunc(steps, func(step time.Time) bool {
		return step.UTC().Truncate(time.Second).Equal(named)
	})
}

// TestTheTwelfthsRefusalReadsEitherRuleOfAProcessInItsTurn holds w5's check of the twelfth claim to
// the two answers of the relaunch guard that read a process still in its turn, and to nothing else.
// The first row is the answer at 4db2269c2c on Oh My Pi 18.18.6-sami.20261119-162325, whose wait
// returns; the second is the answer at 9c242a3f41 on Oh My Pi 18.16.0, whose wait does not.
func TestTheTwelfthsRefusalReadsEitherRuleOfAProcessInItsTurn(t *testing.T) {
	answeredAt := time.Date(2026, 11, 20, 13, 52, 23, 756_000_000, time.UTC)
	steps := []time.Time{
		time.Date(2026, 11, 20, 13, 43, 1, 101_000_000, time.UTC),
		time.Date(2026, 11, 20, 13, 48, 1, 172_000_000, time.UTC),
		time.Date(2026, 11, 20, 13, 48, 1, 200_000_000, time.UTC),
	}
	stepRefusal := `{"code":"RELAUNCH_GUARD","error":"relaunch refused: the relaunch guard refuses the claim: its worker stream showed a step at 2026-11-20T13:48:01Z, less than 20 minutes ago"}`
	cases := []struct {
		name   string
		status int
		code   string
		body   string
		steps  []time.Time
		want   bool
	}{
		{"the step rule, for a step the run saw", http.StatusConflict, "RELAUNCH_GUARD", stepRefusal, steps, true},
		{"the activity report in its turn", http.StatusConflict, "RELAUNCH_GUARD",
			`{"code":"RELAUNCH_GUARD","error":"relaunch refused: the relaunch guard refuses the claim: its session sent an activity report at 2026-11-20T13:51:03Z, less than 20 minutes ago, in the turn it began at 2026-11-20T13:26:07Z, so its process still runs that turn"}`,
			nil, true},
		{"the step rule, for a step the run never saw", http.StatusConflict, "RELAUNCH_GUARD", stepRefusal, steps[:1], false},
		{"the step rule, for a step 20 minutes before the answer", http.StatusConflict, "RELAUNCH_GUARD",
			`{"code":"RELAUNCH_GUARD","error":"relaunch refused: the relaunch guard refuses the claim: its worker stream showed a step at 2026-11-20T13:32:23Z, less than 20 minutes ago"}`,
			append(slices.Clone(steps), time.Date(2026, 11, 20, 13, 32, 23, 500_000_000, time.UTC)), false},
		{"the step rule, for a step after the answer", http.StatusConflict, "RELAUNCH_GUARD",
			`{"code":"RELAUNCH_GUARD","error":"relaunch refused: the relaunch guard refuses the claim: its worker stream showed a step at 2026-11-20T13:53:01Z, less than 20 minutes ago"}`,
			append(slices.Clone(steps), time.Date(2026, 11, 20, 13, 53, 1, 0, time.UTC)), false},
		{"an open call that is not a wait", http.StatusConflict, "RELAUNCH_GUARD",
			`{"code":"RELAUNCH_GUARD","error":"relaunch refused: the relaunch guard refuses the claim: its open call is bash (call-7), not a wait"}`, steps, false},
		{"background work", http.StatusConflict, "RELAUNCH_GUARD",
			`{"code":"RELAUNCH_GUARD","error":"relaunch refused: the relaunch guard refuses the claim: its last activity report, at 2026-11-20T13:51:03Z, shows background work running: bash"}`, steps, false},
		{"another refusal", http.StatusConflict, "RELAUNCHED_RECENTLY", `{"code":"RELAUNCHED_RECENTLY","error":"relaunch refused"}`, steps, false},
		{"a relaunch that went ahead", http.StatusOK, "", `{}`, steps, false},
	}
	for _, c := range cases {
		if got := twelfthRefusedInItsTurn(c.status, c.code, c.body, answeredAt, c.steps); got != c.want {
			t.Errorf("%s: twelfthRefusedInItsTurn = %t, want %t", c.name, got, c.want)
		}
	}
}
