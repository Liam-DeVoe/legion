package delivery

import (
	"fmt"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// settlementHandedBack is the line deliver logs as it hands back a settlement whose issue it
// could not mark owed.
const settlementHandedBack = "dispatch delivery: a settled CI run's issue was not marked owed; handing the envelope back"

// checksSettlementPayload is the settlement the listener's CI summary loop publishes for #number
// of repo at sha (cistore.Summary): one check run, finished green.
func checksSettlementPayload(repo string, number int, sha string) string {
	return fmt.Sprintf(`{"kind":"checks","repo":%q,"number":"%d","sha":%q,`+
		`"check_runs":[{"name":"go-checks","id":901,"conclusion":"success"}],"generation":1,`+
		`"passed":{"count":1,"checks":["go-checks"]},"failed":{"count":0,"checks":[]},"running":{"count":0,"checks":[]},`+
		`"queued":{"count":0,"checks":[]},"cancelled":{"count":0,"checks":[]},"skipped":{"count":0,"checks":[]},"failing_checks":[]}`,
		repo, number, sha)
}

// checkStateMarksOf is how many marks of issue's check states the database holds.
func checkStateMarksOf(t *testing.T, pool *store.Pool, issue string) int {
	t.Helper()
	var marks int
	if err := pool.QueryRow(t.Context(), `select count(*) from check_states_owed where issue_key = $1`, issue).Scan(&marks); err != nil {
		t.Fatalf("count the marks of %s: %v", issue, err)
	}
	return marks
}

// A settlement whose issue could not be marked owed is handed back, and its redelivery marks the
// issue. No table stores a CI run, so the mark is the one write that has the check states
// settler read a run that concluded, and an acknowledged settlement with no mark would leave the
// state of a check CI proves as it was while the run went on.
//
// The mark cannot be written while check_states_owe is renamed away, as a database that refuses
// the statement refuses it. The test asks NATS, not the handler: once the hand-back is logged,
// the function is put back, and the envelope's second delivery, ownHandBackAfter later, writes
// the one mark and is acknowledged.
func TestASettlementWhoseMarkCouldNotBeWrittenIsHandedBack(t *testing.T) {
	logs := captureLogs(t)
	pool, ctx := deliveryTestPool(t)
	seedDeliverySettings(t, ctx, pool)
	issue := seedHeadIssue(t, ctx, pool, 9201)
	const sha = "5555555555555555555555555555555555555555"
	if _, err := pool.Exec(ctx, `
		insert into pull_request_heads (repo, number, head_sha, state, issue_key) values ($1, 9201, $2, 'open', $3)
	`, deliveryRepo, sha, issue); err != nil {
		t.Fatalf("store the open head of %s#9201: %v", deliveryRepo, err)
	}
	// The head's insert marked the issue owed; this test counts the settlement's mark alone.
	if _, err := pool.Exec(ctx, `delete from check_states_owed`); err != nil {
		t.Fatalf("clear the head's mark: %v", err)
	}
	if _, err := pool.Exec(ctx, `alter function check_states_owe(text) rename to check_states_owe_away`); err != nil {
		t.Fatalf("take check_states_owe away: %v", err)
	}

	natsClient := intakeTestClient(t)
	startIntake(t, newIntake(natsClient, pool, deliveryTestLeases(t, pool),
		newFakeGitHub(t).newTestClient(), events.NewBroker(), loadedSettingsCache(t, ctx, pool)))
	awaitBoundDurable(t, natsClient)
	before := deliveryConsumerInfo(t, natsClient).Delivered.Consumer

	publishGitHubEnvelope(t, natsClient, "notifications.github.acme.widgets.pr.9201.checks", checksSettlementPayload(deliveryRepo, 9201, sha))
	handedBack := func() bool {
		for _, record := range logs() {
			if record["msg"] == settlementHandedBack {
				return true
			}
		}
		return false
	}
	waitFor(t, 20*time.Second, handedBack, func() string {
		return fmt.Sprintf("the settlement whose mark failed was not handed back: %+v", deliveryConsumerInfo(t, natsClient))
	})
	if marks := checkStateMarksOf(t, pool, issue); marks != 0 {
		t.Fatalf("%s holds %d marks while check_states_owe is away, want none", issue, marks)
	}

	if _, err := pool.Exec(ctx, `alter function check_states_owe_away(text) rename to check_states_owe`); err != nil {
		t.Fatalf("put check_states_owe back: %v", err)
	}
	waitFor(t, 3*ownHandBackAfter, func() bool { return checkStateMarksOf(t, pool, issue) == 1 }, func() string {
		return fmt.Sprintf("the redelivered settlement left %d marks of %s, want 1: %+v",
			checkStateMarksOf(t, pool, issue), issue, deliveryConsumerInfo(t, natsClient))
	})
	awaitAcknowledged(t, natsClient)
	if delivered := deliveryConsumerInfo(t, natsClient).Delivered.Consumer - before; delivered != 2 {
		t.Fatalf("the settlement was delivered %d times, want 2: the one whose mark failed and the one that marked", delivered)
	}
}
