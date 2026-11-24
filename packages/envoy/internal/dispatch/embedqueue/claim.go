package embedqueue

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/embed"
)

// claimRenewal re-extends scanPending's claim on every row of the original top-level batch a
// ProcessBatch call is working through - not just whatever subtree bisection is currently in,
// since every row not yet committed or confirmed permanent is still this call's responsibility
// until it returns - once renewClaimInterval has passed since the last renewal, so work slower
// than claimWindow (a long bisection, or a single wait on the token budget or the pace; see
// renewClaimInterval, reserveTokens' and embedDocuments' own doc comments) never loses its claim
// to a concurrent scanner partway through. Best-effort: a failed renewal is logged and never
// aborts anything, since a row a concurrent scanner reclaims is merely processed twice, not
// corrupted - commitEmbeddings' own content_hash-matched update already makes a doubly-claimed
// row safe, just not free. A nil *claimRenewal (a caller with no claim to protect, such as a test
// exercising reserveTokens alone) is a safe no-op. extend makes each renewal: extendClaim on the
// store of the Deps newClaimRenewal was given, or, in a test on a synctest fake clock, where no
// Postgres statement may run (duty.ExclusiveFunc says why), a record of the renewal.
type claimRenewal struct {
	rows   []pendingRow
	last   time.Time
	extend func(ctx context.Context, rows []pendingRow) error
}

func newClaimRenewal(deps Deps, rows []pendingRow) *claimRenewal {
	return &claimRenewal{rows: rows, last: time.Now(), extend: func(ctx context.Context, rows []pendingRow) error {
		return extendClaim(ctx, deps, rows)
	}}
}

// renew renews c's claim once it is due (due).
func (c *claimRenewal) renew(ctx context.Context) {
	if c == nil || ctx.Err() != nil || c.due() > 0 {
		return
	}
	c.last = time.Now()
	if err := c.extend(ctx, c.rows); err != nil {
		slog.Error("dispatch embedqueue: bisection claim renewal failed", "error", err)
	}
}

// due is how long until c's next renewal falls due, renewClaimInterval after its last, and 0 once
// it has. A nil *claimRenewal, which renews nothing, is never due.
func (c *claimRenewal) due() time.Duration {
	if c == nil {
		return math.MaxInt64
	}
	return max(renewClaimInterval-time.Since(c.last), 0)
}

// extendClaim pushes next_attempt_at of each of rows claimWindow past now, keeping scanPending's
// claim on them.
func extendClaim(ctx context.Context, deps Deps, rows []pendingRow) error {
	kinds := make([]string, len(rows))
	ids := make([]string, len(rows))
	hashes := make([]string, len(rows))
	for i, row := range rows {
		kinds[i], ids[i], hashes[i] = row.kind, row.id, row.contentHash
	}
	// content_hash-matched, exactly as commitEmbeddings and retryRows both are: a row a
	// concurrent write already changed is no longer this claim's to protect, and touching it
	// anyway would delay that write's own, fresher embeddings_enqueue up to claimWindow for no
	// reason - the row's new content_hash makes it eligible again immediately otherwise.
	_, err := deps.Store.Pool.Exec(ctx, `
		update embeddings e set next_attempt_at = now() + $4
		from unnest($1::text[], $2::text[], $3::text[]) as v(kind, id, content_hash)
		where e.kind = v.kind and e.id = v.id and e.content_hash = v.content_hash
	`, kinds, ids, hashes, claimWindow)
	return err
}

// resolve removes committed rows from the set a claimRenewal still protects: once a row commits
// (embedded_hash = content_hash), scanPending's own predicate no longer selects it, so renewing
// its claim further touches nothing a concurrent scanner could ever reclaim.
func (c *claimRenewal) resolve(committed []pendingRow) {
	if c == nil || len(committed) == 0 {
		return
	}
	c.rows = excludingRows(c.rows, committed)
}

// excludingRows returns rows with every row also present in exclude removed, matched by (kind,
// id) - embedRows' own way of turning commitEmbeddings' rows/permanent pair into the set that
// actually committed, for claimRenewal.resolve. A fresh slice, never c.rows[:0] or rows itself
// filtered in place: c.rows is the original top-level batch, which bisectSplit's own recursion is
// still actively slicing into (rows[:mid], rows[mid:]) elsewhere in the same call tree -
// overwriting its backing array in place would corrupt whatever sibling subtree is reading from
// it concurrently with this call.
func excludingRows(rows, exclude []pendingRow) []pendingRow {
	if len(exclude) == 0 {
		return rows
	}
	skip := make(map[string]struct{}, len(exclude))
	for _, row := range exclude {
		skip[row.kind+"\x00"+row.id] = struct{}{}
	}
	kept := make([]pendingRow, 0, len(rows))
	for _, row := range rows {
		if _, ok := skip[row.kind+"\x00"+row.id]; !ok {
			kept = append(kept, row)
		}
	}
	return kept
}

// reserveBackgroundTokens estimates texts' tokens (embed.EstimateTokens) and reserves them
// against the embedder's BackgroundTokensPerMinute in the shared, database-backed budget
// (reserveTokens), waiting out whatever debt that reservation leaves - renewing renewal's claim
// during a wait longer than renewClaimInterval, not only before or after it. embedRows' own
// per-group reservations call reserveTokens directly with chunkForBudget's already-computed token
// total instead of this helper, so the one-off embedCanary probes (confirmSoloFailure's, and the
// coverage pass's proveEmbedder) are its only callers. Best-effort: a reservation that fails to
// read or write (a transient database error) is logged and never blocks or fails the call -
// embed.RateLimitedEmbedder's own in-process, reactive AIMD backoff is the backstop if this
// proactive ceiling is ever skipped or under-estimates a call.
func reserveBackgroundTokens(ctx context.Context, deps Deps, texts []string, renewal *claimRenewal) {
	reserveTokens(ctx, deps, embed.EstimateTokens(texts), deps.Embedder.Limits().BackgroundTokensPerMinute, renewal)
}

// reserveTokens draws tokens from embeddings_rate_limit's one shared row (migration 0078),
// refilling it continuously at ceilingPerMinute/60 tokens per second and capping it at
// ceilingPerMinute (one statement does both: a plain token bucket), then waits out whatever
// negative balance - debt - that reservation leaves before returning, so the caller's real
// model-service call never races ahead of the budget it just drew down. The row is in the
// database, not in-process memory, because a place's quota belongs to the whole account or
// deployment, not to one caller: every envoy-dispatch process's background work draws from the
// same bucket, and embed.RateLimitedEmbedder's in-process, reactive pacing alone cannot keep a
// live search request in one process safe from throttling another process's background work
// causes. ceilingPerMinute is the configured place's BackgroundTokensPerMinute (two thirds of its
// quota, the rest left to live queries) in estimated tokens (embed.EstimateTokens counts 4
// characters a token), not what the model service counts. The cap and the refill together can
// release about twice the ceiling in one minute, so a minute can pass the quota even when the
// estimate is exact: through LEGION-549's Bedrock backfill (2026-10-11, 72 minutes, 15,117 rows)
// Bedrock counted about 7% more input tokens than the estimate, about 220,600 a minute against
// a 300,000 quota with 8 minutes above it (peak 431,590), and throttled none of it. Dense text
// (hex, CJK) costs about 3.5x the estimate and more on Azure (EstimateTokens' comment), so
// background work on it can pass the deployment's quota; the 429s that follow are throttles,
// which the queue retries without counting them against any row. That is accepted.
//
// Two processes may reserve with different ceilings: two builds whose places set different
// limits, during the release that changes them. Each statement refills at most its own ceiling's
// rate and caps at its own ceiling, so the row never refills faster than the larger ceiling, and
// each process waits out its debt at its own rate, never faster than the larger one: together they
// take no more than one process with the larger ceiling would, the larger ceiling a minute once
// the row is in debt. Only a process whose embedder's identity is active reserves at all
// (ProcessBatch, Cover).
//
// chunkForBudget already keeps one reservation's own tokens at or under ceilingPerMinute, but the
// wait can still run past renewClaimInterval when other debt was already on the row (another
// process's, or this one's own earlier groups) - waitOutDebt renews the claim each time it falls
// due during the wait, not only before or after it, so a claim never lapses mid-wait;
// renewal.renew is a safe no-op on nil (a caller with no claim to protect, such as a test
// exercising the budget alone). ceilingPerMinute is a parameter rather than read from deps here so
// a test can use a tiny ceiling and observe a real, short wait instead of needing to simulate a
// whole minute.
func reserveTokens(ctx context.Context, deps Deps, tokens, ceilingPerMinute int, renewal *claimRenewal) {
	var available float64
	if err := deps.Store.Pool.QueryRow(ctx, `
		update embeddings_rate_limit
		set tokens_available = least($2::double precision, tokens_available + extract(epoch from now() - last_refill_at) * ($2::double precision / 60.0)) - $1::double precision,
		    last_refill_at = now()
		returning tokens_available
	`, float64(tokens), float64(ceilingPerMinute)).Scan(&available); err != nil {
		slog.Error("dispatch embedqueue: background token-budget reservation failed", "error", err)
		return
	}
	if available >= 0 {
		return
	}
	wait := time.Duration(-available / (float64(ceilingPerMinute) / 60.0) * float64(time.Second))
	waitOutDebt(ctx, wait, renewal)
}

// waitOutDebt is the wait loop of reserveTokens and of the pace's wait before an Embed call
// (embedDocuments). Each slice of the wait ends when renewal's next renewal falls due (due),
// renewClaimInterval after its last one however long before the wait that one was, and the
// renewal is made there (renew), and once more when the wait ends if it is due then. So through
// a wait each renewal comes renewClaimInterval, half claimWindow, after the one before it, late
// only by the timer's own lateness, and the claim never runs out mid-wait. A nil renewal (a call
// that holds no claim) sleeps the wait out whole.
func waitOutDebt(ctx context.Context, wait time.Duration, renewal *claimRenewal) {
	for wait > 0 && ctx.Err() == nil {
		renewal.renew(ctx)
		s := min(wait, renewal.due())
		if !pauseFor(ctx, s) {
			return
		}
		wait -= s
	}
	renewal.renew(ctx)
}
