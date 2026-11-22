package contracts

import (
	"log"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/logtest"
)

// The verdict App's id and GitHub Actions' App's, as the text of the numbers GitHub's own JSON
// carries them as.
const (
	verdictApp = "5341240"
	actionsApp = "15368"
)

// logged runs f with the standard logger written to a buffer of the test's own, and answers what
// f logged.
func logged(t *testing.T, f func()) string {
	t.Helper()
	restore := logtest.Keep(t)
	defer restore()
	var buffer logtest.Buffer
	log.SetOutput(&buffer)
	log.SetFlags(0)
	f()
	return buffer.String()
}

// ciBody is a check_run or check_suite webhook body for acme/widgets#42 whose object carries
// fields.
func ciBody(event, action string, fields map[string]any) map[string]any {
	object := map[string]any{
		"head_sha":      "abcdef1234567890abcdef1234567890abcdef12",
		"pull_requests": []any{map[string]any{"number": float64(42)}},
	}
	for key, value := range fields {
		object[key] = value
	}
	return map[string]any{
		"action":     action,
		event:        object,
		"repository": map[string]any{"full_name": "acme/widgets", "name": "widgets", "owner": map[string]any{"login": "acme"}},
	}
}

// The verdict rule decides one check run by its name and the App that created it: a run named
// legion-verdict, tester or architect is a verdict only when the verdict App created it, none is
// while the verdict App is unset, and every other run is no verdict at all. A commit status of
// that name is no check run, so no CI observation is mapped from it and it is published nowhere:
// it never stands in for the run.
func TestLegionVerdictRunsCountOnlyFromTheVerdictApp(t *testing.T) {
	if LegionVerdictCheck != "legion-verdict" {
		t.Fatalf("LegionVerdictCheck = %q, want legion-verdict", LegionVerdictCheck)
	}
	for _, name := range []string{LegionVerdictCheck, "tester", "architect"} {
		for _, c := range []struct {
			appID, verdictAppID string
			want                ReviewerVerdict
		}{
			{appID: verdictApp, verdictAppID: verdictApp, want: VerdictTrusted},
			{appID: actionsApp, verdictAppID: verdictApp, want: VerdictUntrusted},
			{appID: "", verdictAppID: verdictApp, want: VerdictUntrusted},
			{appID: verdictApp, verdictAppID: "", want: VerdictNoVerdictApp},
		} {
			if got := ReviewerVerdictDecision(name, c.appID, c.verdictAppID); got != c.want {
				t.Errorf("ReviewerVerdictDecision(%q, %q, %q) = %d, want %d", name, c.appID, c.verdictAppID, got, c.want)
			}
		}
	}
	for _, verdictAppID := range []string{verdictApp, ""} {
		if got := ReviewerVerdictDecision("unit-tests", actionsApp, verdictAppID); got != VerdictNotAVerdict {
			t.Errorf("ReviewerVerdictDecision(unit-tests, %q, %q) = %d, want no verdict", actionsApp, verdictAppID, got)
		}
	}

	status := map[string]any{
		"sha":        "abcdef1234567890abcdef1234567890abcdef12",
		"context":    LegionVerdictCheck,
		"state":      "success",
		"repository": map[string]any{"full_name": "acme/widgets", "name": "widgets", "owner": map[string]any{"login": "acme"}},
	}
	if observations := GithubCIObservations("status", status, verdictApp); len(observations) != 0 {
		t.Errorf("a commit status named %s maps to %+v, want no CI observation", LegionVerdictCheck, observations)
	}
	if envelopes := GithubEnvelopes(GithubEnvelopeInput{Event: "status", Delivery: "d-status", Body: status, EventID: "e", TraceID: "t"}, "@legion"); len(envelopes) != 0 {
		t.Errorf("a commit status named %s is published as %+v, want nothing", LegionVerdictCheck, envelopes)
	}
}

// A check suite the verdict App created is no part of a head's CI record, in every action GitHub
// sends one in: GitHub makes one, queued, on each push for an App that holds Checks at write, and
// it stays queued until the App writes a run in it, so a record that kept it would never settle.
// Each suite left out is logged, a requested one's too. Another App's suite is recorded once it
// completes; a request to run it maps to nothing, since it is no CI result.
func TestVerdictAppSuitesAreLeftOutOfTheCIRecord(t *testing.T) {
	for _, c := range []struct{ action, status string }{
		{action: "requested", status: "queued"},
		{action: "rerequested", status: "queued"},
		{action: "completed", status: "completed"},
	} {
		t.Run(c.action, func(t *testing.T) {
			suite := func(app string) map[string]any {
				return ciBody("check_suite", c.action, map[string]any{
					"id": float64(120620537894), "status": c.status, "app": map[string]any{"id": app},
					"updated_at": "2026-11-19T15:56:07Z",
				})
			}
			var observations []CIObservation
			lines := logged(t, func() { observations = GithubCIObservations("check_suite", suite(verdictApp), verdictApp) })
			if len(observations) != 0 {
				t.Fatalf("the verdict App's %s suite maps to %+v, want it left out", c.action, observations)
			}
			for _, want := range []string{"ci: left out the verdict App's own check suite", "suite=120620537894", "app_id=" + verdictApp, "action=" + c.action, "repo=acme/widgets", "pr=42"} {
				if !strings.Contains(lines, want) {
					t.Errorf("the log %q does not hold %q", lines, want)
				}
			}
			wantOther := 0
			if c.action == "completed" {
				wantOther = 1
			}
			if observations := GithubCIObservations("check_suite", suite(actionsApp), verdictApp); len(observations) != wantOther || wantOther == 1 && observations[0].AppID != actionsApp {
				t.Fatalf("another App's %s suite maps to %+v, want %d observations of it", c.action, observations, wantOther)
			}
		})
	}
}

// The verdict App's own legion-verdict run is the Legion daemon's output, not a CI result, so it
// is no part of a head's CI record either, in any state, and it is logged as left out. A
// legion-verdict run of any other App is still mapped, for the verdict rule to drop as untrusted
// where the caller logs it (ReviewerVerdictDecision), and the verdict App's tester run is mapped as
// a verdict.
func TestLegionVerdictRunOfTheVerdictAppIsLeftOutOfTheCIRecord(t *testing.T) {
	run := func(name, app string) map[string]any {
		return ciBody("check_run", "completed", map[string]any{
			"id": float64(700), "name": name, "status": "completed", "conclusion": "failure",
			"completed_at": "2026-11-20T03:00:00Z", "app": map[string]any{"id": app},
		})
	}
	for _, c := range []struct{ action, status string }{
		{action: "created", status: "queued"},
		{action: "created", status: "in_progress"},
		{action: "completed", status: "completed"},
	} {
		body := run(LegionVerdictCheck, verdictApp)
		body["action"] = c.action
		body["check_run"].(map[string]any)["status"] = c.status
		var observations []CIObservation
		lines := logged(t, func() { observations = GithubCIObservations("check_run", body, verdictApp) })
		if len(observations) != 0 {
			t.Fatalf("the verdict App's %s legion-verdict run maps to %+v, want it left out", c.status, observations)
		}
		for _, want := range []string{"ci: left out the verdict App's own legion-verdict run", "check_run=700", "app_id=" + verdictApp, "repo=acme/widgets", "pr=42"} {
			if !strings.Contains(lines, want) {
				t.Errorf("%s: the log %q does not hold %q", c.status, lines, want)
			}
		}
	}
	if observations := GithubCIObservations("check_run", run(LegionVerdictCheck, actionsApp), verdictApp); len(observations) != 1 {
		t.Fatalf("another App's legion-verdict run maps to %+v, want one observation for the verdict rule to drop", observations)
	}
	if observations := GithubCIObservations("check_run", run("tester", verdictApp), verdictApp); len(observations) != 1 || observations[0].CheckName != "tester" {
		t.Fatalf("the verdict App's tester run maps to %+v, want one observation of it", observations)
	}
}

// Envoy and Dispatch name the verdict App by ENVOY_VERDICT_APP_ID and DISPATCH_VERDICT_APP_ID,
// and read the settings a deployment configured before, ENVOY_REVIEWER_APP_ID and
// DISPATCH_REVIEWER_APP_ID, while the new one is unset or blank: the new one wins when both are
// set. The answer names the setting it read, so a refusal of the value names what the operator
// set.
func TestOldVerdictAppSettingIsRead(t *testing.T) {
	for _, names := range []struct{ current, former string }{
		{current: "ENVOY_VERDICT_APP_ID", former: "ENVOY_REVIEWER_APP_ID"},
		{current: "DISPATCH_VERDICT_APP_ID", former: "DISPATCH_REVIEWER_APP_ID"},
	} {
		for _, c := range []struct {
			name     string
			env      map[string]string
			value    string
			readFrom string
		}{
			{name: "the old name alone", env: map[string]string{names.former: " 5341243 "}, value: "5341243", readFrom: names.former},
			{name: "both names", env: map[string]string{names.current: "5341240", names.former: "5341243"}, value: "5341240", readFrom: names.current},
			{name: "the new name alone", env: map[string]string{names.current: " 5341240 "}, value: "5341240", readFrom: names.current},
			{name: "a blank new name beside the old", env: map[string]string{names.current: "  ", names.former: "5341243"}, value: "5341243", readFrom: names.former},
			{name: "neither name", env: map[string]string{}, value: "", readFrom: names.current},
		} {
			value, readFrom := VerdictAppSetting(func(name string) string { return c.env[name] }, names.current, names.former)
			if value != c.value || readFrom != c.readFrom {
				t.Errorf("%s, %s: VerdictAppSetting = %q from %s, want %q from %s", names.current, c.name, value, readFrom, c.value, c.readFrom)
			}
		}
	}
}
