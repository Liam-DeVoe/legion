package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// pluginContractOf is the contract the plugin of token's claim declared at its registration, as
// the completion route reads it (supervise.Claim.PluginContract).
func pluginContractOf(t *testing.T, h *harness, token claim.Token) int {
	t.Helper()
	machine, ok := h.supervisor.Machine(token)
	if !ok {
		t.Fatalf("no machine for %s", token)
	}
	return machine.Claim().PluginContract
}

// ratedAs is whether fact carries the rating score with reason.
func ratedAs(fact intake.HandoffComplete, score int, reason string) bool {
	return fact.PlanRating != nil && *fact.PlanRating == (intake.PlanRating{Score: score, Reason: reason})
}

// The completion that ends the retro carries the rating of the plan: the agent that does the retro
// gives it. The route reads which completion that is from phase.Of and never from the role: the
// implementer's completion while the issue is in retro, and its late completion of retro once the
// issue has moved on to merging, which is still of retro. The fact the route applies carries the
// rating and its reason, for the workflow to write on the retro's record.
func TestTheRetrosAgentGivesTheRating(t *testing.T) {
	for _, at := range []phase.Phase{phase.Retro, phase.Merging} {
		t.Run(string(at), func(t *testing.T) {
			if of := phase.Of(claim.RoleImplementer, at); of != phase.Retro {
				t.Fatalf("the implementer's completion at %s is of %s, want retro: this case proves nothing", at, of)
			}
			h, facts := newHandoffHarness(t, newHandoffGitHub(t), nil, nil)
			seedIssueAt(t, h, "LEGION-208", at)
			implementer := newLiveClaim(t, h, "LEGION-208", claim.RoleImplementer)

			completion(t, h.complete(HandoffCompleteRequest{GrantID: implementer.grant(t), Summary: "retro done", Commit: "c0ffee",
				PlanRating: new(4), PlanReason: new("The tester found the bug.")}))
			fact := lastCompletion(t, facts)
			if !ratedAs(fact, 4, "The tester found the bug.") {
				t.Fatalf("the retro's fact carries the rating %+v, want 4 with its reason", fact.PlanRating)
			}
		})
	}
}

// A rating rides the completion that ends the retro and no other. Every other completion that
// names one is refused PLAN_RATING_UNEXPECTED with nothing recorded: a phase of a role that does
// no retro (the tester's testing, the merger's merging), another phase of the very role that does
// it (the implementer's implementing, refused before GitHub is read for its pull request), and a
// report on a look of a closed issue's production check. The same completion without the rating
// is taken, so the rating is what the refusal is about.
func TestARatingOutsideTheRetroIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   phase.Phase
		role claim.Role
		// look is whether the worker's live assignment is look 2 of the issue's production check.
		look bool
		// request is the completion before the grant and the rating are added.
		request func(c liveClaim) HandoffCompleteRequest
	}{
		{name: "the tester in testing", at: phase.Testing, role: claim.RoleTester, request: func(c liveClaim) HandoffCompleteRequest {
			return HandoffCompleteRequest{Summary: "tests pass", Verdict: "pass", HandoffID: c.writeHandoff("test", 1), HandoffVersion: 1}
		}},
		{name: "the implementer in implementing", at: phase.Implementing, role: claim.RoleImplementer, request: func(c liveClaim) HandoffCompleteRequest {
			return HandoffCompleteRequest{Summary: "implemented", Commit: headSHA, HandoffID: c.writeHandoff("implement", 1), HandoffVersion: 1}
		}},
		{name: "the merger in merging", at: phase.Merging, role: claim.RoleMerger, request: func(liveClaim) HandoffCompleteRequest {
			return HandoffCompleteRequest{Summary: "merged", Commit: "c0ffee"}
		}},
		{name: "a report on a look of the production check", at: phase.Done, role: claim.RoleImplementer, look: true, request: func(liveClaim) HandoffCompleteRequest {
			return HandoffCompleteRequest{Summary: "looked", Verdict: "pass"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			github := newHandoffGitHub(t)
			h, facts := newHandoffHarness(t, github, nil, nil)
			seedIssueAt(t, h, "LEGION-208", tc.at)
			var worker liveClaim
			if tc.look {
				seedCheck(t, h, "LEGION-208", tc.role, false, false)
				worker = newCheckLookClaim(t, h, "LEGION-208", tc.role, 2)
			} else {
				worker = newLiveClaim(t, h, "LEGION-208", tc.role)
			}
			request := tc.request(worker)

			rating := request
			rating.GrantID, rating.PlanRating, rating.PlanReason = worker.grant(t), new(3), new("The plan fit the work.")
			message := refused(t, h.complete(rating), http.StatusBadRequest, "PLAN_RATING_UNEXPECTED")
			for _, want := range []string{"of LEGION-208", "only the completion that ends the retro rates the plan", "nothing was recorded"} {
				if !strings.Contains(message, want) {
					t.Fatalf("the refusal = %q, want it to say %q", message, want)
				}
			}
			if got := facts.recorded(); len(got) != 0 {
				t.Fatalf("facts after the refusal = %#v, want none applied", got)
			}
			if served := github.serves(); len(served) != 0 {
				t.Fatalf("GitHub was asked for %v before the refusal, want nothing: the rating is refused before any read", served)
			}

			// The implementer's completion also applies the pull request GitHub shows on its branch
			// (implementationPullRequest) before its own fact, so the completion is the newest fact.
			request.GrantID = worker.grant(t)
			completion(t, h.complete(request))
			got := facts.recorded()
			if len(got) == 0 {
				t.Fatal("no fact after the same completion without the rating, want it applied")
			}
			switch fact := got[len(got)-1].(type) {
			case intake.HandoffComplete:
				if fact.Claim != worker.token || fact.PlanRating != nil {
					t.Fatalf("the completion's fact = %+v, want %s's with no rating", fact, worker.token)
				}
			case intake.CheckReport:
				if fact.Claim != worker.token {
					t.Fatalf("the report's fact = %+v, want %s's", fact, worker.token)
				}
			default:
				t.Fatalf("the newest fact is %T, want the completion or the report", fact)
			}
		})
	}
}

// The architect ends no phase: phase.Of names none for it outside admitted and accepting, and the
// route falls back to the issue's phase only to read the handoff record. So an architect's
// completion while its issue stands in retro is not the retro's, though the phase it falls back to
// is: one carrying a rating is refused PLAN_RATING_UNEXPECTED before the workflow reads it, and one
// carrying none is not asked for a rating and reaches the workflow, which refuses an architect's
// completion of its own accord (ACCEPTANCE_IS_SIGN_OFF). The issue stays in retro either way.
func TestAnArchitectsCompletionInTheRetroIsNotTheRetros(t *testing.T) {
	if of := phase.Of(claim.RoleArchitect, phase.Retro); of != "" {
		t.Fatalf("the architect's completion at retro is of %q, want none: this case proves nothing", of)
	}
	h := newWorkflowHandoffHarness(t, newHandoffGitHub(t), io.Discard)
	seedIssueAt(t, h, "LEGION-208", phase.Retro)
	architect := newLiveClaim(t, h, "LEGION-208", claim.RoleArchitect)
	if got := pluginContractOf(t, h, architect.token); got < PlanRatingContract {
		t.Fatalf("the architect's plugin declared contract %d, below PlanRatingContract %d: this case proves nothing", got, PlanRatingContract)
	}

	refused(t, h.complete(HandoffCompleteRequest{GrantID: architect.grant(t), Summary: "accepted", Commit: "c0ffee",
		PlanRating: new(3), PlanReason: new("The plan fit the work.")}), http.StatusBadRequest, "PLAN_RATING_UNEXPECTED")
	assertIssuePhase(t, h, phase.Retro)
	refused(t, h.complete(HandoffCompleteRequest{GrantID: architect.grant(t), Summary: "accepted", Commit: "c0ffee"}),
		http.StatusConflict, "ACCEPTANCE_IS_SIGN_OFF")
	assertIssuePhase(t, h, phase.Retro)
}

// A rating is a whole number from 1 to 5 and its reason says why, in one to planReasonMax
// characters that are not all blank; the two come together. Each other shape is refused
// PLAN_RATING_INPUT naming what is wrong, with nothing recorded. The bounds themselves are taken:
// 1, and 5 with a reason of exactly planReasonMax characters of two bytes each, which a count of
// bytes would refuse. Each is the completion that ends the retro of an issue of its own, since the
// workflow moves an issue on to merging at its retro's first completion taken.
func TestARatingIsOneToFiveWithAReason(t *testing.T) {
	h, facts := newHandoffHarness(t, newHandoffGitHub(t), nil, nil)
	seedIssueAt(t, h, "LEGION-208", phase.Retro)
	implementer := newLiveClaim(t, h, "LEGION-208", claim.RoleImplementer)

	for _, tc := range []struct {
		name   string
		rating *int
		reason *string
		says   string
	}{
		{name: "a rating of 0", rating: new(0), reason: new("The plan fit the work."), says: "planRating 0 is outside 1 to 5"},
		{name: "a rating of 6", rating: new(6), reason: new("The plan fit the work."), says: "planRating 6 is outside 1 to 5"},
		{name: "a rating with no reason", rating: new(4), says: "planRating 4 comes with no planReason"},
		{name: "a reason with no rating", reason: new("The plan fit the work."), says: "planReason comes with no planRating"},
		{name: "a blank reason", rating: new(4), reason: new(" \t\n"), says: "planReason is blank"},
		{name: "a reason too long", rating: new(4), reason: new(strings.Repeat("é", planReasonMax+1)),
			says: "planReason is 2001 characters, and it holds at most 2000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := refused(t, h.complete(HandoffCompleteRequest{GrantID: implementer.grant(t), Summary: "retro done", Commit: "c0ffee",
				PlanRating: tc.rating, PlanReason: tc.reason}), http.StatusBadRequest, "PLAN_RATING_INPUT")
			for _, want := range []string{tc.says, "nothing was recorded"} {
				if !strings.Contains(message, want) {
					t.Fatalf("the refusal = %q, want it to say %q", message, want)
				}
			}
		})
	}
	if got := facts.recorded(); len(got) != 0 {
		t.Fatalf("facts after the refusals = %#v, want none applied", got)
	}

	for i, taken := range []struct {
		rating int
		reason string
	}{
		{rating: 1, reason: "The plan cost the work."},
		{rating: 5, reason: strings.Repeat("é", planReasonMax)},
	} {
		key := fmt.Sprintf("LEGION-%d", 300+i)
		seedIssueAt(t, h, key, phase.Retro)
		rater := newLiveClaim(t, h, key, claim.RoleImplementer)
		completion(t, h.complete(HandoffCompleteRequest{GrantID: rater.grant(t), Summary: "retro done", Commit: "c0ffee",
			PlanRating: new(taken.rating), PlanReason: new(taken.reason)}))
		if fact := lastCompletion(t, facts); fact.Issue != key || !ratedAs(fact, taken.rating, taken.reason) {
			t.Fatalf("the retro's fact is %s's, rated %t, want %s's carrying the rating %d with its reason of %d bytes", fact.Issue, fact.PlanRating != nil, key, taken.rating, len(taken.reason))
		}
	}
}

// A completion names a rating by its members written with a value, whatever the value:
// `"planReason": ""` is a reason named, and empty. So a completion that does not end the retro and
// carries it, or either member alone, is refused PLAN_RATING_UNEXPECTED; the retro's completion
// carrying it alone is refused PLAN_RATING_INPUT for the rating it lacks, from a plugin at
// PlanRatingContract and from one before it alike, rather than told it carries no rating or taken
// unrated; and with a rating it is refused as blank. A member written null names nothing: it
// decodes to nil and reads as left out, so the completion outside the retro that writes both
// members null is taken, and its fact carries no rating. That body alone is written by hand, since
// HandoffCompleteRequest leaves a nil member out rather than writing it null. A refusal records
// nothing, and the completion each case corrects to is taken.
func TestAnEmptyPlanReasonIsAReasonNamed(t *testing.T) {
	type rating struct {
		rating *int
		reason *string
	}
	complete := func(h *harness, grant string, r rating) *httptest.ResponseRecorder {
		return h.complete(HandoffCompleteRequest{GrantID: grant, Summary: "done", Commit: "c0ffee", PlanRating: r.rating, PlanReason: r.reason})
	}

	t.Run("outside the retro", func(t *testing.T) {
		h, facts := newHandoffHarness(t, newHandoffGitHub(t), nil, nil)
		seedIssueAt(t, h, "LEGION-208", phase.Merging)
		merger := newLiveClaim(t, h, "LEGION-208", claim.RoleMerger)
		for _, named := range []rating{{reason: new("")}, {reason: new("fit")}, {rating: new(4)}} {
			refused(t, complete(h, merger.grant(t), named), http.StatusBadRequest, "PLAN_RATING_UNEXPECTED")
		}
		if got := facts.recorded(); len(got) != 0 {
			t.Fatalf("facts after the refusals = %#v, want none applied", got)
		}
		completion(t, h.completeRaw(merger.grant(t), `,"summary":"done","commit":"c0ffee","planRating":null,"planReason":null`))
		if fact := lastCompletion(t, facts); fact.PlanRating != nil {
			t.Fatalf("the completion writing both members null was taken rated %+v, want no rating", *fact.PlanRating)
		}
	})

	for _, band := range []struct {
		name     string
		contract int
		// taken is the rating of the completion the claim's plugin is taken with.
		taken rating
	}{
		{name: "a plugin at PlanRatingContract", contract: PlanRatingContract, taken: rating{rating: new(4), reason: new("fit")}},
		{name: "a plugin before it", contract: PlanRatingContract - 1},
	} {
		t.Run("the retro of "+band.name, func(t *testing.T) {
			h, facts := newHandoffHarness(t, newHandoffGitHub(t), nil, nil)
			seedIssueAt(t, h, "LEGION-208", phase.Retro)
			implementer := newLiveClaim(t, h, "LEGION-208", claim.RoleImplementer)
			// A stale claim is served in its turn (staleClaim), and its grants are minted first, as
			// TestAnOldPluginRetroStillCompletesWhereItIsServed mints its one.
			grants := []string{implementer.grant(t), implementer.grant(t), implementer.grant(t)}
			if band.contract != PlanRatingContract {
				h.makeStale(t, implementer.token, implementer.session, implementer.secret, band.contract)
			}
			if c := h.stored(implementer.token); c.State != supervise.StateWorking || c.PluginContract != band.contract {
				t.Fatalf("the claim is %s at contract %d, want working at %d: this case proves nothing", c.State, c.PluginContract, band.contract)
			}
			for i, row := range []struct {
				named rating
				says  string
			}{
				{named: rating{reason: new("")}, says: "planReason comes with no planRating"},
				{named: rating{rating: new(4), reason: new("")}, says: "planReason is blank"},
			} {
				message := refused(t, complete(h, grants[i], row.named), http.StatusBadRequest, "PLAN_RATING_INPUT")
				if !strings.Contains(message, row.says) {
					t.Fatalf("the refusal = %q, want it to say %q", message, row.says)
				}
			}
			if got := facts.recorded(); len(got) != 0 {
				t.Fatalf("facts after the refusals = %#v, want none applied", got)
			}
			completion(t, complete(h, grants[2], band.taken))
		})
	}
}

// A planReason that holds a NUL character (a JSON body carries it as \u0000) is a reason nothing
// can store: PostgreSQL's jsonb, where the daemon's outbox row holds the rating, and its text,
// where Dispatch's record holds it, both refuse it, and the whole daemon answered the retro's
// completion 500 FACT_APPLY_FAILED naming no field when the engine's transaction failed on the
// rating row. The decoder of the body refuses it before the route reads it, as it refuses a NUL in
// any string of any body (decodeJSON): 400 naming planReason and where in it the NUL stands, with
// nothing recorded, so the agent sends it again corrected.
func TestARatingReasonWithANULIsRefusedAsInput(t *testing.T) {
	h, facts := newHandoffHarness(t, newHandoffGitHub(t), nil, nil)
	seedIssueAt(t, h, "LEGION-208", phase.Retro)
	implementer := newLiveClaim(t, h, "LEGION-208", claim.RoleImplementer)

	wantRefusal(t, h.complete(HandoffCompleteRequest{GrantID: implementer.grant(t), Summary: "retro done", Commit: "c0ffee",
		PlanRating: new(4), PlanReason: new("The plan fit\x00 the work.")}), http.StatusBadRequest,
		"invalid request body: planReason holds a NUL character (U+0000) at character 13, which the daemon cannot store")
	if got := facts.recorded(); len(got) != 0 {
		t.Fatalf("facts after the refusal = %#v, want none applied", got)
	}
}

// A plugin at PlanRatingContract or later must rate: its retro's completion with no rating is
// refused PLAN_RATING_REQUIRED, which says how to rate, with nothing recorded, and the same
// completion with a rating is taken.
func TestANewPluginMustRate(t *testing.T) {
	h, facts := newHandoffHarness(t, newHandoffGitHub(t), nil, nil)
	seedIssueAt(t, h, "LEGION-208", phase.Retro)
	implementer := newLiveClaim(t, h, "LEGION-208", claim.RoleImplementer)
	if got := pluginContractOf(t, h, implementer.token); got != PlanRatingContract {
		t.Fatalf("the implementer's plugin declared contract %d, want PlanRatingContract %d: this case proves nothing", got, PlanRatingContract)
	}

	message := refused(t, h.complete(HandoffCompleteRequest{GrantID: implementer.grant(t), Summary: "retro done", Commit: "c0ffee"}),
		http.StatusBadRequest, "PLAN_RATING_REQUIRED")
	for _, want := range []string{"this completion of phase retro of LEGION-208 carries no rating of the plan", "planRating", "planReason", "1 (the plan cost the work) to 5 (the plan fit the work)", "nothing was recorded"} {
		if !strings.Contains(message, want) {
			t.Fatalf("the refusal = %q, want it to say %q", message, want)
		}
	}
	if got := facts.recorded(); len(got) != 0 {
		t.Fatalf("facts after the refusal = %#v, want none applied", got)
	}

	completion(t, h.complete(HandoffCompleteRequest{GrantID: implementer.grant(t), Summary: "retro done", Commit: "c0ffee",
		PlanRating: new(5), PlanReason: new("The plan fit the work.")}))
	if fact := lastCompletion(t, facts); !ratedAs(fact, 5, "The plan fit the work.") {
		t.Fatalf("the retro's fact carries the rating %+v, want 5 with its reason", fact.PlanRating)
	}
}

// "PlanRatingContract or later" holds above the contract too, where TestANewPluginMustRate's claim
// cannot reach: every claim the harness registers declares this daemon's own contract, which is
// PlanRatingContract. The claim here records a later one and the daemon still serves it, a claim
// in its turn (staleClaim), as after a later release rolled back to this daemon while its worker
// was in the retro's turn. Its retro's completion with no rating is refused PLAN_RATING_REQUIRED
// with nothing recorded, and the same completion with a rating is taken.
func TestAPluginAboveTheRatingContractMustRateToo(t *testing.T) {
	h, facts := newHandoffHarness(t, newHandoffGitHub(t), nil, nil)
	seedIssueAt(t, h, "LEGION-208", phase.Retro)
	implementer := newLiveClaim(t, h, "LEGION-208", claim.RoleImplementer)
	grants := []string{implementer.grant(t), implementer.grant(t)}
	later := PlanRatingContract + 1
	h.makeStale(t, implementer.token, implementer.session, implementer.secret, later)
	if c := h.stored(implementer.token); c.State != supervise.StateWorking || c.UpdateRound != "" || c.PluginContract != later {
		t.Fatalf("the claim is %s in round %q at contract %d, want working in no round at %d: this case proves nothing",
			c.State, c.UpdateRound, c.PluginContract, later)
	}

	refused(t, h.complete(HandoffCompleteRequest{GrantID: grants[0], Summary: "retro done", Commit: "c0ffee"}),
		http.StatusBadRequest, "PLAN_RATING_REQUIRED")
	if got := facts.recorded(); len(got) != 0 {
		t.Fatalf("facts after the refusal = %#v, want none applied", got)
	}

	completion(t, h.complete(HandoffCompleteRequest{GrantID: grants[1], Summary: "retro done", Commit: "c0ffee",
		PlanRating: new(2), PlanReason: new("The plan left out a step the tester needed.")}))
	if fact := lastCompletion(t, facts); !ratedAs(fact, 2, "The plan left out a step the tester needed.") {
		t.Fatalf("the retro's fact carries the rating %+v, want 2 with its reason", fact.PlanRating)
	}
}

// stepRecordPayloads is the text of every step record row key's outbox holds, in the order they
// were written: what the workflow sends Dispatch of each run of a step, a retro's rating among it.
func stepRecordPayloads(t *testing.T, h *harness, key string) []string {
	t.Helper()
	rows, err := h.store.Pool().Query(context.Background(), `select payload::text from outbox where issue = $1 and kind = $2 order by id`,
		key, string(record.OutboxKindStepRecord))
	if err != nil {
		t.Fatalf("read the step records of %s: %v", key, err)
	}
	payloads, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read the step records of %s: %v", key, err)
	}
	return payloads
}

// The retro's completion the workflow takes moves the issue on to merging (TriggerRetroCompleted)
// and writes its rating on the retro's record. The same completion sent again from there is still
// the retro's, the late retro's (phase.Of), so the workflow decides it: the implementer's retro
// already stands, so the first retry, rated otherwise, is refused HANDOFF_NOT_CURRENT_PHASE, and
// that refusal is recorded under the completion's key. The rating is left out of the key, as the
// summary is, so each later retry at the same commit, with the first rating and reason, another
// rating or another reason, is that same completion and is answered HANDOFF_ALREADY_RECORDED. The
// retro's record keeps the first rating alone: the step records stand as the first completion
// wrote them, its reason in one of them and no retry's reason in any.
func TestARatedRetroSentAgainKeepsItsFirstRating(t *testing.T) {
	h := newWorkflowHandoffHarness(t, newHandoffGitHub(t), io.Discard)
	seedIssueAt(t, h, "LEGION-208", phase.Retro)
	implementer := newLiveClaim(t, h, "LEGION-208", claim.RoleImplementer)
	retro := func(rating int, reason string) *httptest.ResponseRecorder {
		return h.complete(HandoffCompleteRequest{GrantID: implementer.grant(t), Summary: "retro done", Commit: "c0ffee",
			PlanRating: new(rating), PlanReason: new(reason)})
	}
	const first = "The plan left out a step the tester needed."
	completion(t, retro(2, first))
	assertIssuePhase(t, h, phase.Merging)
	written := stepRecordPayloads(t, h, "LEGION-208")
	if rated := slices.IndexFunc(written, func(p string) bool { return strings.Contains(p, first) }); rated < 0 ||
		slices.ContainsFunc(written[rated+1:], func(p string) bool { return strings.Contains(p, first) }) {
		t.Fatalf("the retro's completion wrote the step records %v, want its reason in exactly one", written)
	}

	const refusedFirst = "The plan fit the work."
	refused(t, retro(5, refusedFirst), http.StatusConflict, "HANDOFF_NOT_CURRENT_PHASE")
	again := []struct {
		name   string
		rating int
		reason string
	}{
		{name: "the first rating and reason", rating: 2, reason: first},
		{name: "another rating", rating: 4, reason: first},
		{name: "another reason", rating: 2, reason: "The reviewer's two rounds were the step the plan left out."},
	}
	for _, retry := range again {
		t.Run(retry.name, func(t *testing.T) {
			message := refused(t, retro(retry.rating, retry.reason), http.StatusConflict, "HANDOFF_ALREADY_RECORDED")
			if !strings.Contains(message, "was already received; this call changed nothing") {
				t.Fatalf("the refusal = %q, want it to say the call changed nothing", message)
			}
		})
	}
	assertIssuePhase(t, h, phase.Merging)
	if got := stepRecordPayloads(t, h, "LEGION-208"); !slices.Equal(got, written) {
		t.Fatalf("after the retries the step records are %v, want them as the first completion wrote them: %v", got, written)
	}
	for _, reason := range []string{refusedFirst, again[2].reason} {
		if slices.ContainsFunc(written, func(p string) bool { return strings.Contains(p, reason) }) {
			t.Fatalf("the step records %v carry a retry's reason %q", written, reason)
		}
	}
}

// A plugin of the release before PlanRatingContract never sends a rating. Where the daemon serves
// its claim — here a claim in a turn, which the stale-caller rule serves so the turn can push what
// it holds (staleClaim) — its retro's completion is taken with no rating, and the fact carries
// none.
func TestAnOldPluginRetroStillCompletesWhereItIsServed(t *testing.T) {
	h, facts := newHandoffHarness(t, newHandoffGitHub(t), nil, nil)
	seedIssueAt(t, h, "LEGION-208", phase.Retro)
	implementer := newLiveClaim(t, h, "LEGION-208", claim.RoleImplementer)
	grantID := implementer.grant(t)
	h.makeStale(t, implementer.token, implementer.session, implementer.secret, PlanRatingContract-1)
	if c := h.stored(implementer.token); c.State != supervise.StateWorking || c.UpdateRound != "" || c.PluginContract != PlanRatingContract-1 {
		t.Fatalf("the claim is %s in round %q at contract %d, want working in no round at %d: this case proves nothing",
			c.State, c.UpdateRound, c.PluginContract, PlanRatingContract-1)
	}

	completion(t, h.complete(HandoffCompleteRequest{GrantID: grantID, Summary: "retro done", Commit: "c0ffee"}))
	if fact := lastCompletion(t, facts); fact.PlanRating != nil || fact.Summary != "retro done" {
		t.Fatalf("the old plugin's retro fact = %+v, want it with no rating", fact)
	}
}

// The control of TestAnOldPluginRetroStillCompletesWhereItIsServed: the same claim out of its turn
// is not served at all, and its completion is refused STALE_PLUGIN_CONTRACT before any rule of the
// rating is read.
func TestAnOldPluginOutsideATurnIsRefusedStale(t *testing.T) {
	h, facts := newHandoffHarness(t, newHandoffGitHub(t), nil, nil)
	seedIssueAt(t, h, "LEGION-208", phase.Retro)
	implementer := newLiveClaim(t, h, "LEGION-208", claim.RoleImplementer)
	grantID := implementer.grant(t)
	endTurn(t, h, implementer.token)
	h.makeStale(t, implementer.token, implementer.session, implementer.secret, PlanRatingContract-1)
	if c := h.stored(implementer.token); c.State == supervise.StateWorking || c.UpdateRound != "" || c.PluginContract != PlanRatingContract-1 {
		t.Fatalf("the claim is %s in round %q at contract %d, want out of its turn in no round at %d: this case proves nothing",
			c.State, c.UpdateRound, c.PluginContract, PlanRatingContract-1)
	}

	refused(t, h.complete(HandoffCompleteRequest{GrantID: grantID, Summary: "retro done", Commit: "c0ffee"}),
		http.StatusConflict, "STALE_PLUGIN_CONTRACT")
	if got := facts.recorded(); len(got) != 0 {
		t.Fatalf("facts after the refusal = %#v, want none applied", got)
	}
}

// The completion that ends the retro, rated, is the one request of the completion route a plugin
// at PlanRatingContract must send: Go writes it from HandoffCompleteRequest (`-update`), and
// `packages/contracts/src/legion-api.test.ts` parses it with LegionHandoffCompleteRequest, a strict
// schema, so a JSON name of the rating Go writes and the contract does not read, or the reverse,
// fails there. The rating and its reason are values no default and no other field of the body
// holds.
func TestHandoffCompleteRequestRatedGolden(t *testing.T) {
	golden(t, "handoff-complete-request-rated.json", HandoffCompleteRequest{
		GrantID: "grant-for-one-command", Summary: "Retro done.", Commit: "c0ffee5a1d2b3c4d5e6f708192a3b4c5d6e7f809",
		PlanRating: new(3), PlanReason: new("The tester found the bug the plan had left out."),
	})
}

// The bounds of a rating of the plan, as planRatingOf holds a completion to them: the scale
// (planRatingLeast, planRatingMost) and the most characters of the reason (planReasonMax). Go
// writes them (`-update`), and `packages/contracts/src/legion-api.test.ts` holds the contract's own
// bounds, which the plugin's tool reads, equal to the file.
func TestPlanRatingGolden(t *testing.T) {
	golden(t, "plan-rating.json", struct {
		Least      int `json:"least"`
		Most       int `json:"most"`
		ReasonMost int `json:"reasonMost"`
	}{Least: planRatingLeast, Most: planRatingMost, ReasonMost: planReasonMax})
}

// planRatingCase is one row of plan-rating-cases.json: the rating and the reason a retro's
// completion carries, a member left out of the body where its pointer is nil, and the verdict the
// completion route answers it with, `accepted` or the code of its refusal.
type planRatingCase struct {
	Name       string  `json:"name"`
	PlanRating *int    `json:"planRating,omitempty"`
	PlanReason *string `json:"planReason,omitempty"`
	Verdict    string  `json:"verdict"`
}

// The daemon is the rule of a rating, and the contract's schema and the plugin's tool state it
// again in TypeScript, where blank is Go's set written out by hand: goBlankPattern, whose comment
// in `packages/contracts/src/go-blank.ts` names the characters JavaScript's trim and Go's
// strings.TrimSpace part on. So the cases a rule of the scale, of the pair, of blank or of length
// can part on are a golden. Each is posted as its raw body, from a plugin at PlanRatingContract, to
// the completion that ends the retro of an issue of its own: the workflow moves an issue on to
// merging at the first completion of its retro it takes, so each case is that first completion.
// The route's answer is the verdict Go writes (`-update`), never one typed here;
// `packages/contracts` and the plugin hold their own verdicts to the file. An answer that is
// neither a 200 nor a 400 of the rating fails the test, so no trouble of the harness is written
// down as a verdict, and an accepted case's fact is its issue's and carries the rating and the
// reason as they were sent.
func TestPlanRatingCasesGolden(t *testing.T) {
	h, facts := newHandoffHarness(t, newHandoffGitHub(t), nil, nil)

	rated := func(name string, rating int, reason string) planRatingCase {
		return planRatingCase{Name: name, PlanRating: new(rating), PlanReason: new(reason)}
	}
	cases := []planRatingCase{
		rated("a rating of 4 with a reason", 4, "fit"),
		rated("a rating of 0", 0, "fit"),
		rated("a rating of 1", 1, "fit"),
		rated("a rating of 5", 5, "fit"),
		rated("a rating of 6", 6, "fit"),
		{Name: "a rating with no reason", PlanRating: new(4)},
		{Name: "a reason with no rating", PlanReason: new("fit")},
		rated("an empty reason", 4, ""),
	}
	for _, blank := range []struct{ name, text string }{
		{"three spaces", "   "}, {"U+0085", "\u0085"}, {"U+00A0", "\u00a0"}, {"U+FEFF", "\ufeff"},
	} {
		cases = append(cases,
			rated("a reason of "+blank.name+" alone", 4, blank.text),
			rated("a reason of "+blank.name+" before a word", 4, blank.text+"fit"))
	}
	for _, long := range []struct{ name, char string }{
		{"U+0061 (a), one UTF-16 unit each", "a"}, {"U+1F600, two UTF-16 units each", "\U0001F600"},
	} {
		for _, count := range []int{planReasonMax, planReasonMax + 1} {
			cases = append(cases, rated(fmt.Sprintf("a reason of %d characters %s", count, long.name), 4, strings.Repeat(long.char, count)))
		}
	}

	for i := range cases {
		c := &cases[i]
		key := fmt.Sprintf("LEGION-%d", 300+i)
		seedIssueAt(t, h, key, phase.Retro)
		implementer := newLiveClaim(t, h, key, claim.RoleImplementer)
		if got := pluginContractOf(t, h, implementer.token); got < PlanRatingContract {
			t.Fatalf("the implementer's plugin declared contract %d, below PlanRatingContract %d: this case proves nothing", got, PlanRatingContract)
		}
		body := map[string]any{"grantId": implementer.grant(t), "summary": "retro done", "verdict": "", "commit": "c0ffee"}
		if c.PlanRating != nil {
			body["planRating"] = *c.PlanRating
		}
		if c.PlanReason != nil {
			body["planReason"] = *c.PlanReason
		}
		before := len(facts.recorded())
		recorder := h.request(http.MethodPost, "/legion/v1/handoff/complete", body, nil)
		switch recorder.Code {
		case http.StatusOK:
			fact := lastCompletion(t, facts)
			if len(facts.recorded()) != before+1 || fact.Issue != key || c.PlanRating == nil || c.PlanReason == nil || !ratedAs(fact, *c.PlanRating, *c.PlanReason) {
				t.Fatalf("%s was taken with the fact %+v, want one new fact of %s carrying the rating and the reason as sent", c.Name, fact, key)
			}
			c.Verdict = "accepted"
		case http.StatusBadRequest:
			var failure Failure
			decodeInto(t, recorder, &failure)
			if !strings.HasPrefix(failure.Code, "PLAN_RATING_") {
				t.Fatalf("%s was refused %s (%s), want a refusal of the rating", c.Name, failure.Code, failure.Error)
			}
			if got := len(facts.recorded()); got != before {
				t.Fatalf("%s was refused %s with %d facts applied, want none", c.Name, failure.Code, got-before)
			}
			c.Verdict = failure.Code
		default:
			t.Fatalf("%s was answered %d: %s, want 200 or a 400 of the rating", c.Name, recorder.Code, recorder.Body)
		}
	}
	golden(t, "plan-rating-cases.json", struct {
		Cases []planRatingCase `json:"cases"`
	}{Cases: cases})
}
