package main

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/broker/oauthwire"
)

// TestLeaverHoldBrokerCredentials is the broker half of c-leaver-end-to-end. Removing the
// production hold gate from a new enrollment makes the first held assertion answer 201, so the
// red is a real broker still serving ada, not a fixture failure. Every remaining assertion names
// the production route whose gate must refuse her, while bob remains served.
func TestLeaverHoldBrokerCredentials(t *testing.T) {
	r := newLeaverRig(t, withBrokerGate())
	ada, bob := r.person(t, adaEmail), r.person(t, bobEmail)
	recordID, code := r.pendingMachineLogin(t, ada)

	r.refuse(adaEmail)
	r.ageConfirmation(t, adaEmail, confirmationAge)
	r.ageConfirmation(t, bobEmail, confirmationAge)
	// The broker asked Dispatch about ada and bob as the rig gave them their machine logins and
	// sessions, and keeps each not-held answer 30 seconds. They must lapse, or ada's rows below read
	// the answer from before Google refused her rather than ask Dispatch, which now holds her.
	r.pastBrokerCache(t)

	for _, row := range []struct {
		what   string
		status int
		call   func() (int, string)
	}{
		{"bob@example.com's refresh grant after ada is held", http.StatusOK, func() (int, string) { return r.refreshGrant(t, bob) }},
		{"bob@example.com's new enrollment after ada is held", http.StatusCreated, func() (int, string) { return r.enrol(t, bob) }},
		{"bob@example.com's lease renewal after ada is held", http.StatusOK, func() (int, string) { return r.renewLease(t, bob) }},
		{"bob@example.com's broker token mint after ada is held", http.StatusOK, func() (int, string) { return r.mintBrokerToken(t, bob) }},
		{"bob@example.com's broker token at Dispatch after ada is held", http.StatusOK, func() (int, string) { return r.brokerTokenAtDispatch(t, bob) }},
		{"bob@example.com's agent-tier secret request after ada is held", http.StatusOK, func() (int, string) { return r.askAgentSecret(t, bob) }},
		{"bob@example.com's agent-tier grant read after ada is held", http.StatusOK, func() (int, string) { return r.readAgentGrant(t, bob) }},
		{"bob@example.com's human-tier secret request after ada is held", http.StatusOK, func() (int, string) { return r.askHumanSecret(t, bob) }},
		{"bob@example.com's human-tier grant read after ada is held", http.StatusOK, func() (int, string) { return r.readHumanGrant(t, bob) }},
	} {
		expectAnswer(t, row.what, row.status, "").got(row.call())
	}
	expectAnswer(t, "ada@example.com's personal token, the first request after Google refuses her", http.StatusUnauthorized, "MEMBERSHIP_HELD").got(r.personalToken(t, ada.dsp))
	expectAnswer(t, "ada@example.com's refresh grant while Dispatch holds her", http.StatusOK, "").got(r.refreshGrant(t, ada))
	expectAnswer(t, "ada@example.com's new enrollment after Dispatch holds her", http.StatusForbidden, "MEMBERSHIP_HELD").got(r.enrol(t, ada))
	if t.Failed() {
		return
	}

	expectAnswer(t, "ada@example.com's lease renewal while Dispatch holds her", http.StatusOK, "").got(r.renewLease(t, ada))
	expectAnswer(t, "ada@example.com's broker token mint while Dispatch holds her", http.StatusBadRequest, oauthwire.MembershipHeld).got(r.mintBrokerToken(t, ada))
	expectAnswer(t, "ada@example.com's existing broker token at Dispatch while Dispatch holds her", http.StatusUnauthorized, "MEMBERSHIP_HELD").got(r.brokerTokenAtDispatch(t, ada))
	expectAnswer(t, "ada@example.com's agent-tier secret request while Dispatch holds her", http.StatusForbidden, "MEMBERSHIP_HELD").got(r.askAgentSecret(t, ada))
	expectAnswer(t, "ada@example.com's agent-tier grant read while Dispatch holds her", http.StatusForbidden, "MEMBERSHIP_HELD").got(r.readAgentGrant(t, ada))
	expectAnswer(t, "ada@example.com's human-tier secret request while Dispatch holds her", http.StatusForbidden, "MEMBERSHIP_HELD").got(r.askHumanSecret(t, ada))
	expectAnswer(t, "ada@example.com's human-tier grant read while Dispatch holds her", http.StatusForbidden, "MEMBERSHIP_HELD").got(r.readHumanGrant(t, ada))
	decision := bearerRequest(r.handler, http.MethodPost, "/api/v1/credential-requests/"+recordID+"/approve",
		leaverBody(t, map[string]string{"code": code}), ada.dsp.plaintext)
	expectAnswer(t, "ada@example.com's pending machine-login approval while Dispatch holds her", http.StatusUnauthorized, "MEMBERSHIP_HELD").got(dispatchAnswer(decision))
}

// TestLeaverHoldRelease proves the hold revokes neither Dispatch nor broker state. Removing the
// production release on a fresh Google sign-in leaves the first post-release credential refused;
// deleting or replacing a stored credential changes one of the five independent row snapshots.
func TestLeaverHoldRelease(t *testing.T) {
	r := newLeaverRig(t, withBrokerGate(), withPersonSignIns())
	ada := r.person(t, adaEmail)
	r.personSignIn(t, ada)
	before := snapshotLeaverCredentials(t, r)

	r.refuse(adaEmail)
	r.ageConfirmation(t, adaEmail, confirmationAge)
	// The broker asked Dispatch about ada at her sign-in trade above and keeps that not-held answer
	// 30 seconds. It must lapse for her enrollment below to ask Dispatch, which holds her once her
	// personal token's request has had Google refuse her refresh.
	r.pastBrokerCache(t)
	expectAnswer(t, "ada@example.com's personal token, the first request after Google refuses her", http.StatusUnauthorized, "MEMBERSHIP_HELD").got(r.personalToken(t, ada.dsp))
	expectAnswer(t, "ada@example.com's new enrollment before she signs in again", http.StatusForbidden, "MEMBERSHIP_HELD").got(r.enrol(t, ada))
	if t.Failed() {
		return
	}

	ageSignInCredential(t, r, ada)
	r.signInAgain(t, adaEmail)
	// The broker answered ada held at her enrollment above and keeps that answer 30 seconds. It must
	// lapse for her sign-in trade below, made at once, to ask Dispatch, which this sign-in released.
	r.pastBrokerCache(t)
	r.personSignIn(t, ada)
	waitForLeaverCredentials(t, r, ada, 40*time.Second)
	expectLeaverSnapshot(t, before, snapshotLeaverCredentials(t, r))

	r.refuse(adaEmail)
	r.ageConfirmation(t, adaEmail, confirmationAge)
	// The release wait above had the broker ask Dispatch about ada seconds ago, while she was served
	// again, and the broker keeps that answer 30 seconds. It must lapse for the enrollment below to
	// ask Dispatch, whose refresh of her Google now refuses.
	r.pastBrokerCache(t)
	expectAnswer(t, "ada@example.com's new enrollment while held for the 25-hour release", http.StatusForbidden, "MEMBERSHIP_HELD").got(r.enrol(t, ada))
	if t.Failed() {
		return
	}
	r.ageConfirmation(t, adaEmail, 25*time.Hour)
	r.refuse(adaEmail)
	waitForOutageRelease(t, r, ada, 40*time.Second)
}

// TestLeaverHoldBound holds the membership pass off and polls the three broker-facing rows by
// the wall clock. Removing the production gate leaves the real introspection response unheld, so
// the first failure names the broker row rather than a timer or a fake.
func TestLeaverHoldBound(t *testing.T) {
	for _, row := range []struct {
		name  string
		delay time.Duration
		bound time.Duration
	}{
		{name: "an eight-second Google refusal", delay: 8 * time.Second, bound: 120 * time.Second},
		{name: "a half-second Google refusal", delay: 500 * time.Millisecond, bound: 40 * time.Second},
	} {
		t.Run(row.name, func(t *testing.T) {
			r := newLeaverRig(t, withBrokerGate())
			ada := r.person(t, adaEmail)
			r.ageConfirmation(t, adaEmail, time.Hour-time.Second)
			expectAnswer(t, "ada@example.com's broker token at Dispatch before the confirmation lapses", http.StatusOK, "").got(r.brokerTokenAtDispatch(t, ada))
			status, held, code := r.introspection(t, ada)
			if status != http.StatusOK || held || code != "" {
				t.Fatalf("ada@example.com's broker introspection before the confirmation lapses answered %d held=%t %q, want 200 held=false", status, held, code)
			}
			expectAnswer(t, "ada@example.com's session route before the confirmation lapses", http.StatusOK, "").got(r.readAgentGrant(t, ada))
			if t.Failed() {
				return
			}

			// The fake Google waits row.delay and then refuses each refresh invalid_grant, under
			// oidc's 10-second token timeout, so only a refusal (never a confirmation) can end it.
			r.refuse(adaEmail)
			r.google.DelayToken(row.delay)
			r.ageConfirmation(t, adaEmail, time.Hour)
			waitForBrokerHold(t, r, ada, row.bound)
		})
	}
}

// snapshotLeaverCredentials reads exactly the persisted rows c-release protects. It removes only
// the use-time fields the contract permits to move, a refreshed login's expires_at included, and
// rejects an empty table, so a snapshot cannot pass because its query silently saw no fixture data.
func snapshotLeaverCredentials(t *testing.T, r *leaverRig) map[string][]string {
	t.Helper()
	snapshot := make(map[string][]string)
	for _, table := range []string{"launcher_credentials", "enrollments", "grants", "person_signins", "agent_tokens"} {
		excluded := ` - 'last_used_at'`
		switch table {
		case "launcher_credentials":
			excluded += ` - 'expires_at'`
		case "enrollments":
			excluded += ` - 'lease_expires_at'`
		}
		query := `select (to_jsonb(t)` + excluded + `)::text from ` + table + ` as t order by id`
		var rows pgx.Rows
		var err error
		if table == "agent_tokens" {
			rows, err = r.outside.database.Pool.Query(t.Context(), query)
		} else {
			rows, err = r.broker.Store.Pool.Query(t.Context(), query)
		}
		if err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				rows.Close()
				t.Fatalf("read snapshot row of %s: %v", table, err)
			}
			snapshot[table] = append(snapshot[table], row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("finish snapshot of %s: %v", table, err)
		}
		rows.Close()
		if len(snapshot[table]) == 0 {
			t.Fatalf("snapshot of %s has no rows, so it cannot prove a hold leaves that table unchanged", table)
		}
	}
	return snapshot
}

func expectLeaverSnapshot(t *testing.T, before, after map[string][]string) {
	t.Helper()
	for _, table := range []string{"launcher_credentials", "enrollments", "grants", "person_signins", "agent_tokens"} {
		if !slices.Equal(before[table], after[table]) {
			t.Errorf("%s after release = %v, want the pre-hold rows %v except their excluded use-time fields", table, after[table], before[table])
		}
	}
}

// ageSignInCredential simulates an over-hour hold without waiting on a wall clock. The later
// person trade must renew its real launcher credential rather than make a second sign-in row.
func ageSignInCredential(t *testing.T, r *leaverRig, p *leaverPerson) {
	t.Helper()
	tag, err := r.broker.Store.Pool.Exec(t.Context(), `update launcher_credentials set expires_at = now() - interval '2 hours' where id = $1`, p.signInCredential)
	if err != nil {
		t.Fatalf("age %s's sign-in credential: %v", p.email, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("age %s's sign-in credential changed %d rows, want 1", p.email, tag.RowsAffected())
	}
}

func waitForLeaverCredentials(t *testing.T, r *leaverRig, p *leaverPerson, bound time.Duration) {
	t.Helper()
	deadline := time.Now().Add(bound)
	for {
		failures := leaverCredentialFailures(t, r, p)
		if len(failures) == 0 {
			t.Logf("%s's credentials were served again within %s", p.email, bound)
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("%s's credentials were still not served after %s: %s", p.email, bound, failures)
			return
		}
		time.Sleep(time.Second)
	}
}

func leaverCredentialFailures(t *testing.T, r *leaverRig, p *leaverPerson) string {
	t.Helper()
	var failures []string
	for _, row := range []struct {
		name string
		call func() (int, string)
	}{
		{"cookie", func() (int, string) { return r.cookie(t, p) }},
		{"personal token", func() (int, string) { return r.personalToken(t, p.dsp) }},
		{"refresh grant", func() (int, string) { return r.refreshGrant(t, p) }},
		{"lease renewal", func() (int, string) { return r.renewLease(t, p) }},
		{"broker token mint", func() (int, string) { return r.mintBrokerToken(t, p) }},
		{"broker token at Dispatch", func() (int, string) { return r.brokerTokenAtDispatch(t, p) }},
		{"agent-tier secret request", func() (int, string) { return r.askAgentSecret(t, p) }},
		{"agent-tier grant read", func() (int, string) { return r.readAgentGrant(t, p) }},
		{"human-tier secret request", func() (int, string) { return r.askHumanSecret(t, p) }},
		{"human-tier grant read", func() (int, string) { return r.readHumanGrant(t, p) }},
	} {
		status, code := row.call()
		if status != http.StatusOK || code != "" {
			failures = append(failures, fmt.Sprintf("%s answered %d %q", row.name, status, code))
		}
	}
	return fmt.Sprint(failures)
}

func waitForOutageRelease(t *testing.T, r *leaverRig, p *leaverPerson, bound time.Duration) {
	t.Helper()
	deadline := time.Now().Add(bound)
	for {
		status, code := r.enrol(t, p)
		if status == http.StatusCreated && code == "" {
			t.Logf("%s's 25-hour hold released within %s of Google's first successful refresh", p.email, bound)
			return
		}
		if status != http.StatusForbidden || code != "MEMBERSHIP_HELD" {
			t.Errorf("%s's 25-hour hold answered %d %q, want 201 after release or 403 MEMBERSHIP_HELD while it waits", p.email, status, code)
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("%s's 25-hour hold did not release within %s", p.email, bound)
			return
		}
		time.Sleep(time.Second)
	}
}

func waitForBrokerHold(t *testing.T, r *leaverRig, p *leaverPerson, bound time.Duration) {
	t.Helper()
	started := time.Now()
	deadline := started.Add(bound)
	for {
		last := ""
		if status, code := r.brokerTokenAtDispatch(t, p); status != http.StatusUnauthorized || code != "MEMBERSHIP_HELD" {
			last = fmt.Sprintf("%s's broker token at Dispatch answered %d %q, want 401 MEMBERSHIP_HELD", p.email, status, code)
		}
		status, held, code := r.introspection(t, p)
		if last == "" && (status != http.StatusOK || !held || code != "") {
			last = fmt.Sprintf("%s's broker introspection answered %d held=%t %q, want 200 held=true", p.email, status, held, code)
		}
		if status, code := r.readAgentGrant(t, p); last == "" && (status != http.StatusForbidden || code != "MEMBERSHIP_HELD") {
			last = fmt.Sprintf("%s's session route answered %d %q, want 403 MEMBERSHIP_HELD", p.email, status, code)
		}
		if last == "" {
			t.Logf("%s's broker rows were held in %s", p.email, time.Since(started))
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("%s within %s", last, bound)
			return
		}
		time.Sleep(time.Second)
	}
}
