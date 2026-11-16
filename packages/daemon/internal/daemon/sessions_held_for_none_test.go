package daemon

import (
	"context"
	"log/slog"
	"slices"
	"strconv"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// An id two claims recorded before the daemon held sessions is held for neither (the
// claim_sessions migration), so each is refused its registration of it; relaunched, either would
// resume that id, be refused 409 and exit, again and again. So the boot suspends each through the
// operator's suspend, before anything relaunches them, and names the id, its claims and the
// statement that settles it at Error; a claim of the same boot holding its own session is left as
// it was, and no launch is made for any claim of the id. A claim in a turn holds the suspension
// for that turn's end (it stops the process at the operator's stop timeout), which is no failure
// of the boot. The id is an agent's own text, so the statement carries it as a SQL literal, its
// quotes doubled.
//
// The control: drop the boot step (start's call of suspendSessionsHeldForNone); the claims are
// re-adopted live, each a relaunch away from the refusal, and the test fails naming them (the pull
// request records the run).
func TestAClaimWhoseIdIsHeldTwiceIsSuspendedAtBoot(t *testing.T) {
	cfg := testConfig(t)
	project, err := claim.ProjectToken(cfg.Project)
	if err != nil {
		t.Fatal(err)
	}
	token := func(issue string, role claim.Role) claim.Token {
		t.Helper()
		tok, err := claim.NewToken(project, issue, role)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	first, second, busy := token("LEGION-901", claim.RoleImplementer), token("LEGION-902", claim.RoleImplementer), token("LEGION-903", claim.RoleImplementer)
	own := token("LEGION-901", claim.RoleTester)
	const twice = "ses_twice'; update claim_sessions set claim = 'x'; --"

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for i, row := range []struct {
		token          claim.Token
		issue, session string
		state          supervise.ClaimState
	}{
		{first, "LEGION-901", twice, supervise.StateIdle},
		{second, "LEGION-902", twice, supervise.StateIdle},
		{busy, "LEGION-903", twice, supervise.StateWorking},
		{own, "LEGION-901", "ses_own", supervise.StateIdle},
	} {
		if err := st.PutClaim(ctx, earlierBootClaim(project, row.token, row.issue, row.session, row.state, i+1)); err != nil {
			t.Fatalf("record %s: %v", row.token, err)
		}
	}
	// What the migration leaves of the rows recording one id, and of the one recording its own.
	if _, err := st.Pool().Exec(ctx, `insert into claim_sessions (project, session, claim) values ($1, $2, ''), ($1, 'ses_own', $3)`, project, twice, string(own)); err != nil {
		t.Fatalf("record the holds: %v", err)
	}
	st.Close()

	rt := fake.NewRuntime()
	logs := &syncBuffer{}
	d := startDaemonLogging(t, cfg, fakeRuntime(rt, &built{}), slog.New(slog.NewJSONHandler(logs, nil)))

	for _, tok := range []claim.Token{first, second} {
		if got := d.claim(tok); got.State != string(supervise.StateSuspended) || got.SuspendedBy != string(supervise.SuspendedByOperator) {
			t.Errorf("%s, whose session is held for no claim, is %s suspended by %q after the boot; want it suspended by the operator's suspend",
				tok, got.State, got.SuspendedBy)
		}
	}
	if got := d.claim(busy); got.State != string(supervise.StateWorking) || !got.SuspensionHeld {
		t.Errorf("%s, in a turn with its session held for no claim, is %s holding a suspension %t after the boot; want the boot to have served with its suspension held for the turn",
			busy, got.State, got.SuspensionHeld)
	}
	if got := d.claim(own); got.State != string(supervise.StateIdle) {
		t.Errorf("%s, which holds its own session, is %s after the boot, want it idle as it was", own, got.State)
	}
	for _, call := range rt.Calls() {
		if (call.Method == "Spawn" || call.Method == "Resume") && call.Spec.Claim != own {
			t.Errorf("the boot launched %s (%s), whose session is held for no claim", call.Spec.Claim, call.Method)
		}
	}
	suspended := []claim.Token{}
	for _, call := range rt.CallsOf("Suspend") {
		suspended = append(suspended, call.Locator.Claim)
	}
	if !slices.Contains(suspended, first) || !slices.Contains(suspended, second) || slices.Contains(suspended, own) {
		t.Errorf("the boot stopped the processes of %v, want %s's and %s's and not %s's", suspended, first, second, own)
	}

	lines := loggedLines(t, logs.String(), "held for none of them")
	if len(lines) != 1 {
		t.Fatalf("the boot logged %d lines naming the id held twice, want 1: %v", len(lines), lines)
	}
	line := lines[0]
	claims, _ := line["claims"].([]any)
	settle := "update claim_sessions set claim = '<token>' where project = '" + project + "' and session = 'ses_twice''; update claim_sessions set claim = ''x''; --'"
	if line["level"] != "ERROR" || line["session"] != twice || len(claims) != 3 ||
		claims[0] != string(first) || claims[1] != string(second) || claims[2] != string(busy) || line["settle"] != settle {
		t.Errorf("the boot logged %v, want an error naming %q, %s, %s and %s, and the statement that settles it, %q", line, twice, first, second, busy, settle)
	}
}

// earlierBootClaim is a claim an earlier boot left in state on issue, recording session, with the
// tmux process its locator names and this daemon's plugin contract: the boot re-adopts it rather
// than launching it.
func earlierBootClaim(project string, token claim.Token, issue, session string, state supervise.ClaimState, n int) supervise.Claim {
	role, _ := token.Role()
	pane := strconv.Itoa(n)
	return supervise.Claim{
		Token: token, Project: project, Tree: issue, Issue: issue, Role: role, Generation: 1,
		Session: session, SessionFile: "/sessions/" + strconv.Itoa(n) + ".jsonl",
		Locator: &runtime.Locator{
			Runtime: runtime.RuntimeTmux, Claim: token, Incarnation: "3100" + pane + ":90000" + pane,
			Tmux: &runtime.TmuxLocator{Window: "@" + pane, Pane: "%" + pane},
		},
		State:          state,
		BootTokenHash:  supervise.HashBootToken("boot-" + string(token)),
		CapabilityHash: []byte{byte(n)},
		PluginContract: api.DaemonAPIVersion,
	}
}
