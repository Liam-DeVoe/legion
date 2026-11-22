package delivery

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/contracts"
)

// A read the intake makes settles the read a link owed in another letter case. A link by hand owes
// the read of #4297 in the link's own letters, acme/widgets, while no head row of it is stored.
// GitHub's event of the same pull request names the repository Acme/Widgets, and the intake's read
// of it answers. The read settles the link's row (owedRevision finds it in any case, and
// settleOwedRead deletes it by its own name), so no read of #4297 stays owed and the reconcile
// reads it no more. Where the intake read the owed revision in exact case, it found no row,
// settled nothing, and the reconcile read the pull request once more. The control: a link written
// in GitHub's own letters is settled the same way.
func TestTheIntakesReadSettlesTheReadALinkOwedInAnotherCase(t *testing.T) {
	for _, linked := range []string{"acme/widgets", "Acme/Widgets"} {
		t.Run(linked, func(t *testing.T) {
			fastPageRetries(t)
			pool, ctx := deliveryTestPool(t)
			const number = 4297
			const head, branch = "4297429742974297429742974297429742974297", "feature/case"
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := OweLinkedPullRequestReads(ctx, tx, []string{"https://github.com/" + linked + "/pull/" + strconv.Itoa(number)}); err != nil {
				t.Fatalf("owe the read of the link to %s#%d: %v", linked, number, err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			owedInAnyCase := func() int {
				t.Helper()
				var owed int
				if err := pool.QueryRow(ctx, `select count(*) from pull_request_reads_owed where lower(repo) = 'acme/widgets' and number = $1`, number).Scan(&owed); err != nil {
					t.Fatalf("count the reads of #%d owed: %v", number, err)
				}
				return owed
			}
			if owed := owedInAnyCase(); owed != 1 || owedCount(t, ctx, pool, linked, number) != 1 {
				t.Fatalf("after the link to %s#%d, %d reads of it are owed in any case; want the one row under %s", linked, number, owed, linked)
			}
			fake := newFakeGitHub(t)
			github := serveGitHubPulls(t, fake, "Acme/Widgets")
			at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
			github.set("Acme/Widgets", number, "open", head, branch, "", at)
			intake := newIntake(nil, pool, nil, fake.newTestClient(), nil, newSettingsCache(pool, nil))
			payload, err := json.Marshal(prEnvelope("Acme/Widgets", number, "synchronize", head, branch, "", at))
			if err != nil {
				t.Fatal(err)
			}
			intake.deliver(&intakeDelivery{handling: ctx}, intakeMessage{
				msg:      &natsgo.Msg{Subject: "notifications.github.Acme.Widgets.pr." + strconv.Itoa(number)},
				envelope: &contracts.Envelope{Payload: string(payload)},
			})
			var heads int
			if err := pool.QueryRow(ctx, `select count(*) from pull_request_heads where lower(repo) = 'acme/widgets' and number = $1 and head_sha = $2`, number, head).Scan(&heads); err != nil {
				t.Fatal(err)
			}
			if heads != 1 {
				t.Fatalf("after the event's delivery, %d head rows of #%d hold %s; want 1: the intake's read answered", heads, number, head)
			}
			if owed := owedInAnyCase(); owed != 0 {
				t.Errorf("after the intake's read of Acme/Widgets#%d answered, %d reads of it owed under %s stay; want none: the read settles the row the link owed, in any case", number, owed, linked)
			}
		})
	}
}
