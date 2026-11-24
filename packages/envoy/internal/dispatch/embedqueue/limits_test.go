package embedqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/embed"
	"github.com/sjawhar/envoy/internal/dispatch/embed/embedtest"
	"github.com/sjawhar/envoy/internal/dispatch/retry"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/logtest"
)

// azureFake stands in for an Azure AI Foundry deployment's embeddings route: it answers 429 while
// throttling is set, 404 DeploymentNotFound while missing is set (the deployment name is not one
// the resource has), the status outage names while it is set (to every request, the canary's
// included: Azure itself failing), nothing at all while hang is set (every request waits until its
// client gives up: Azure not answering in time), 500 to any request holding a text that contains
// poison while it is set (one text Azure fails on every time), and vectors otherwise, recording
// each request's inputs.
type azureFake struct {
	mu         sync.Mutex
	throttling bool
	missing    bool
	outage     int
	hang       bool
	poison     string
	requests   [][]string
}

func (f *azureFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Input []string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.requests = append(f.requests, request.Input)
	throttling, missing, outage, hang, poison := f.throttling, f.missing, f.outage, f.hang, f.poison
	f.mu.Unlock()
	if hang {
		// Read to the body's end, so net/http watches the connection and ends r's context once
		// the client gives up on the request.
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if throttling {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"RateLimitReached","message":"Rate limit is exceeded."}}`))
		return
	}
	if missing {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"DeploymentNotFound","message":"The API deployment for this resource does not exist."}}`))
		return
	}
	if outage != 0 {
		w.WriteHeader(outage)
		_, _ = fmt.Fprintf(w, `{"error":{"code":"InternalServerError","message":"%s"}}`, http.StatusText(outage))
		return
	}
	for _, text := range request.Input {
		if poison != "" && strings.Contains(text, poison) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":"InternalServerError","message":"The server had an error while processing your request."}}`))
			return
		}
	}
	type item struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	}
	data := make([]item, len(request.Input))
	for i := range data {
		data[i] = item{Index: i, Embedding: oneVector()}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (f *azureFake) take() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	requests := f.requests
	f.requests = nil
	return requests
}

// An Azure 429, through the real AzureClient, is a throttle: the whole batch is retried as one
// request - never bisected, never confirmed with a canary - however many times it recurs, and
// nothing is dead-lettered; once Azure answers, the batch is embedded under the Azure identity.
func TestAnAzureThrottleRetriesTheWholeBatchAndNeverDeadLetters(t *testing.T) {
	database := storetest.Open(t)
	ids := []string{"AZTH-1", "AZTH-2", "AZTH-3"}
	for _, id := range ids {
		seedIssue(t, database, "AZTH", id, "Title of "+id)
	}
	fake := &azureFake{throttling: true}
	server := httptest.NewServer(fake)
	defer server.Close()
	client, err := embed.NewAzure(embed.AzureConfig{
		Endpoint:        server.URL + "/models",
		Deployment:      "embed-v-4-0",
		TokensPerMinute: 300_000,
		APIKey:          "test-key",
		HTTPClient:      server.Client(),
	})
	if err != nil {
		t.Fatalf("NewAzure: %v", err)
	}
	deps := Deps{Store: database, Embedder: client}
	records := &logtest.Recorder{}
	logtest.SetDefault(t, records)

	for cycle := range retry.DeadLetterAttempts + 2 {
		for _, id := range ids {
			forceEligible(t, database, "issue", id)
		}
		succeeded, failed, blocked, throttled, err := ProcessBatch(context.Background(), deps)
		if err != nil || blocked || !throttled || succeeded != 0 || failed != len(ids) {
			t.Fatalf("cycle %d: ProcessBatch = %d, %d, %v, %v, %v; want 0, %d, not blocked, throttled", cycle, succeeded, failed, blocked, throttled, err, len(ids))
		}
		if requests := fake.take(); len(requests) != 1 || len(requests[0]) != len(ids) {
			t.Fatalf("cycle %d: Azure saw %v, want one request carrying the whole batch", cycle, requests)
		}
		// One WARN for the group at each attempt, naming its size and attempt and no text.
		lines := passRecords(records, slog.LevelWarn, throttledGroupMessage)
		if len(lines) != cycle+1 {
			t.Fatalf("cycle %d: %d throttled-group lines, want %d (one a cycle)", cycle, len(lines), cycle+1)
		}
		attrs := attrsOf(lines[cycle])
		if attrs["rows"].Int64() != int64(len(ids)) || attrs["attempt"].Int64() != int64(cycle+1) {
			t.Fatalf("cycle %d: throttled-group line %v, want rows=%d attempt=%d", cycle, attrs, len(ids), cycle+1)
		}
		for key, value := range attrs {
			if strings.Contains(value.String(), "Title of") {
				t.Fatalf("cycle %d: throttled-group line's %s carries a text: %v", cycle, key, value)
			}
		}
	}
	var dead, confirmed int
	if err := database.Pool.QueryRow(context.Background(), `
		select count(*) filter (where dead), coalesce(sum(confirmed_failures), 0) from embeddings where kind = 'issue'
	`).Scan(&dead, &confirmed); err != nil {
		t.Fatalf("read rows: %v", err)
	}
	if dead != 0 || confirmed != 0 {
		t.Fatalf("%d dead rows, %d confirmed failures after a sustained throttle, want 0 and 0", dead, confirmed)
	}

	fake.mu.Lock()
	fake.throttling = false
	fake.mu.Unlock()
	for _, id := range ids {
		forceEligible(t, database, "issue", id)
	}
	succeeded, failed, _, throttled, err := ProcessBatch(context.Background(), deps)
	if err != nil || throttled || succeeded != len(ids) || failed != 0 {
		t.Fatalf("ProcessBatch once Azure answers = %d, %d, %v, %v; want %d, 0", succeeded, failed, throttled, err, len(ids))
	}
	var models []string
	if err := database.Pool.QueryRow(context.Background(), `select array_agg(distinct model) from embeddings where kind = 'issue'`).Scan(&models); err != nil {
		t.Fatalf("read models: %v", err)
	}
	if len(models) != 1 || models[0] != client.Identity() {
		t.Errorf("models %v, want only the Azure identity %q", models, client.Identity())
	}
}

// throttledGroupMessage is handleGroupFailure's line for a group the model service throttled.
const throttledGroupMessage = "dispatch embedqueue: the model service throttled a group; it is retried whole, counted against no row"

// recordHook hands each record to onRecord, then to the handler it wraps.
type recordHook struct {
	slog.Handler
	onRecord func(slog.Record)
}

func (h recordHook) Handle(ctx context.Context, record slog.Record) error {
	h.onRecord(record)
	return h.Handler.Handle(ctx, record)
}

// A throttled group's WARN is written once its retry is: at the moment the line is written the row
// already holds the attempt the line names and falls due the line's retryIn after the write. A
// retry write that fails writes no WARN, since no retry was scheduled; the failed write's ERROR
// says so instead. Written before the write, the line named a retry the first cycle never
// scheduled, and the row still held its old attempt count when the second cycle's line was written.
func TestAThrottledGroupsWarningComesOnceItsRetryIsWritten(t *testing.T) {
	database := storetest.Open(t)
	seedIssue(t, database, "THWR", "THWR-1", "Title of THWR-1")
	deps := Deps{Store: database, Embedder: &fakeEmbedder{err: embedtest.FakeThrottleError{Code: "ThrottlingException"}}}
	type stored struct {
		attempts int
		due      time.Time
	}
	var atWarning []stored
	records := &logtest.Recorder{}
	logtest.SetDefault(t, recordHook{Handler: records, onRecord: func(record slog.Record) {
		if record.Message != throttledGroupMessage {
			return
		}
		var row stored
		if err := database.Pool.QueryRow(context.Background(),
			`select attempt_count, next_attempt_at from embeddings where kind = 'issue' and id = 'THWR-1'`,
		).Scan(&row.attempts, &row.due); err != nil {
			t.Errorf("read THWR-1 as its WARN is written: %v", err)
		}
		atWarning = append(atWarning, row)
	}})

	mustExec(t, database.Pool, `create function refuse_retry() returns trigger language plpgsql as $$
		begin raise exception 'the test refuses every retry write'; end $$`)
	mustExec(t, database.Pool, `create trigger refuse_retry before update of attempt_count on embeddings
		for each row execute function refuse_retry()`)
	_, failed, blocked, throttled, err := ProcessBatch(context.Background(), deps)
	if err != nil || !blocked || !throttled || failed != 1 {
		t.Fatalf("ProcessBatch with the retry write refused = %d failed, %v, %v, %v; want 1, blocked, throttled", failed, blocked, throttled, err)
	}
	if scheduleErrors := passRecords(records, slog.LevelError, "dispatch embedqueue: schedule retry"); len(scheduleErrors) != 1 {
		t.Fatalf("%d schedule-retry ERROR lines, want 1: the refused write is the case under test", len(scheduleErrors))
	}
	if lines := passRecords(records, slog.LevelWarn, throttledGroupMessage); len(lines) != 0 {
		t.Errorf("a refused retry write left %d throttled-group WARN lines (%v), want none: no retry was scheduled", len(lines), attrsOf(lines[0]))
	}

	mustExec(t, database.Pool, `drop trigger refuse_retry on embeddings`)
	forceEligible(t, database, "issue", "THWR-1")
	before := time.Now()
	if _, _, blocked, throttled, err := ProcessBatch(context.Background(), deps); err != nil || blocked || !throttled {
		t.Fatalf("ProcessBatch with the retry write allowed = %v, %v, %v; want not blocked, throttled", blocked, throttled, err)
	}
	after := time.Now()
	lines := passRecords(records, slog.LevelWarn, throttledGroupMessage)
	if len(lines) == 0 || len(atWarning) != len(lines) {
		t.Fatalf("%d throttled-group WARN lines, %d rows read as they were written; want the second cycle's line, read once", len(lines), len(atWarning))
	}
	line, row := attrsOf(lines[len(lines)-1]), atWarning[len(atWarning)-1]
	attempt, retryIn := line["attempt"].Int64(), line["retryIn"].Duration()
	if attempt != 1 || retryIn != retry.Delay(1) {
		t.Errorf("the WARN names attempt %d, retryIn %v; want 1 and %v", attempt, retryIn, retry.Delay(1))
	}
	if row.attempts != int(attempt) {
		t.Errorf("as the WARN naming attempt %d was written THWR-1 held attempt %d: the line came before its retry was written", attempt, row.attempts)
	}
	if row.due.Before(before.Add(retryIn)) || row.due.After(after.Add(retryIn)) {
		t.Errorf("as the WARN naming retryIn %v was written THWR-1 fell due at %v, want between %v and %v", retryIn, row.due, before.Add(retryIn), after.Add(retryIn))
	}
}

// azureClientFor is a real AzureClient that calls server.
func azureClientFor(t *testing.T, server *httptest.Server) *embed.AzureClient {
	t.Helper()
	client, err := embed.NewAzure(embed.AzureConfig{
		Endpoint:        server.URL + "/models",
		Deployment:      "embed-v-4-0",
		TokensPerMinute: 300_000,
		APIKey:          strings.Repeat("k", 8),
		HTTPClient:      server.Client(),
	})
	if err != nil {
		t.Fatalf("NewAzure: %v", err)
	}
	return client
}

// embeddingStates is each issue row's id with whether it is embedded, its confirmed failures and
// whether it is dead.
func embeddingStates(t *testing.T, database *store.Store) map[string]string {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `
		select id, embedded_hash is not distinct from content_hash, confirmed_failures, dead
		from embeddings where kind = 'issue'`)
	if err != nil {
		t.Fatalf("read rows: %v", err)
	}
	defer rows.Close()
	states := map[string]string{}
	for rows.Next() {
		var id string
		var embedded, dead bool
		var confirmed int
		if err := rows.Scan(&id, &embedded, &confirmed, &dead); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		states[id] = fmt.Sprintf("embedded=%v confirmed=%d dead=%v", embedded, confirmed, dead)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read rows: %v", err)
	}
	return states
}

// A text Azure answers 500 for every time, through the real AzureClient, holds up no other text:
// its batch is bisected, so the two texts beside it embed in the first cycle, and its own failure
// is confirmed by the canary Azure answers, one confirmed failure a cycle, until it is dead at
// retry.DeadLetterAttempts. The failing halves of the bisection leave the shared pace where it
// was, since Azure is answering (cmd/dispatch's RateLimitedEmbedder, wrapped here for the first
// cycle). Read as a throttle, the three were retried whole together, in step, and none ever
// embedded.
func TestATextAzureFailsOnEveryTimeHoldsUpNoOtherText(t *testing.T) {
	database := storetest.Open(t)
	for _, id := range []string{"AZPO-1", "AZPO-2", "AZPO-3"} {
		seedIssue(t, database, "AZPO", id, "Title of "+id)
	}
	fake := &azureFake{poison: "AZPO-1"}
	server := httptest.NewServer(fake)
	defer server.Close()
	client := azureClientFor(t, server)
	paced := embed.NewRateLimitedEmbedder(client)
	pace := paced.Interval()
	deps := Deps{Store: database, Embedder: paced}

	succeeded, failed, blocked, throttled, err := ProcessBatch(context.Background(), deps)
	if err != nil || blocked || throttled || succeeded != 2 || failed != 1 {
		t.Fatalf("cycle 0: ProcessBatch = %d, %d, %v, %v, %v; want 2, 1, not blocked, not throttled", succeeded, failed, blocked, throttled, err)
	}
	if got := paced.Interval(); got != pace {
		t.Fatalf("cycle 0 moved the pace from %v to %v, want it where it was", pace, got)
	}
	deps.Embedder = client // the later cycles' pace is the limiter's own rows'; unpaced, they wait nothing
	if requests := fake.take(); len(requests) != 4 || len(requests[0]) != 3 || requests[2][0] != embedCanary {
		t.Fatalf("cycle 0: Azure saw %v, want the batch, the poison alone, the canary and the other two", requests)
	}
	want := map[string]string{
		"AZPO-1": "embedded=false confirmed=1 dead=false",
		"AZPO-2": "embedded=true confirmed=0 dead=false",
		"AZPO-3": "embedded=true confirmed=0 dead=false",
	}
	if got := embeddingStates(t, database); !maps.Equal(got, want) {
		t.Fatalf("after cycle 0: %v, want %v", got, want)
	}
	for cycle := 1; cycle < retry.DeadLetterAttempts; cycle++ {
		forceEligible(t, database, "issue", "AZPO-1")
		if _, _, _, _, err := ProcessBatch(context.Background(), deps); err != nil {
			t.Fatalf("cycle %d: ProcessBatch: %v", cycle, err)
		}
	}
	want["AZPO-1"] = fmt.Sprintf("embedded=false confirmed=%d dead=true", retry.DeadLetterAttempts)
	if got := embeddingStates(t, database); !maps.Equal(got, want) {
		t.Fatalf("after %d cycles: %v, want %v", retry.DeadLetterAttempts, got, want)
	}
}

// Azure itself failing - a 408, a 500, a 502 or a 504 to every request, the canary's included, or
// no answer to any of them in the call's time - through the real AzureClient, is counted against
// no row however long it lasts: each row's failure is met by a canary that fails too, so nothing
// is confirmed and nothing dead-letters. Each such canary backs the shared pace off
// (RateLimitedEmbedder.BackOff, wrapped here for one last cycle), so a cycle that bisects two rows
// and sends two canaries leaves it four times slower. Read as untroubled, as they once were, the
// calls that ran out of time raised the rate, and their canaries did not back it off.
func TestAnAzureWideOutageCountsAgainstNoRow(t *testing.T) {
	for i, tc := range []struct {
		name   string
		outage int
		hang   bool
	}{
		{http.StatusText(http.StatusRequestTimeout), http.StatusRequestTimeout, false},
		{http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError, false},
		{http.StatusText(http.StatusBadGateway), http.StatusBadGateway, false},
		{http.StatusText(http.StatusGatewayTimeout), http.StatusGatewayTimeout, false},
		{"no answer in the call's time", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := storetest.Open(t)
			ids := []string{fmt.Sprintf("AZOU-%d1", i+1), fmt.Sprintf("AZOU-%d2", i+1)}
			for _, id := range ids {
				seedIssue(t, database, "AZOU", id, "Title of "+id)
			}
			fake := &azureFake{outage: tc.outage, hang: tc.hang}
			server := httptest.NewServer(fake)
			defer server.Close()
			if tc.hang {
				server.Client().Timeout = 100 * time.Millisecond
			}
			client := azureClientFor(t, server)
			deps := Deps{Store: database, Embedder: client}
			for cycle := range retry.DeadLetterAttempts + 2 {
				for _, id := range ids {
					forceEligible(t, database, "issue", id)
				}
				succeeded, failed, blocked, throttled, err := ProcessBatch(context.Background(), deps)
				if err != nil || blocked || throttled || succeeded != 0 || failed != 2 {
					t.Fatalf("cycle %d: ProcessBatch = %d, %d, %v, %v, %v; want 0, 2, not blocked, not throttled", cycle, succeeded, failed, blocked, throttled, err)
				}
				canaries := 0
				requests := fake.take()
				for _, request := range requests {
					if len(request) == 1 && request[0] == embedCanary {
						canaries++
					}
				}
				if len(requests) != 5 || canaries != 2 {
					t.Fatalf("cycle %d: Azure saw %v, want the batch, then each row and a canary after it", cycle, requests)
				}
			}
			states := embeddingStates(t, database)
			if len(states) != len(ids) {
				t.Fatalf("read %d rows, want %d: %v", len(states), len(ids), states)
			}
			for id, state := range states {
				if state != "embedded=false confirmed=0 dead=false" {
					t.Fatalf("%s after a sustained %s: %s, want nothing confirmed and nothing dead", id, tc.name, state)
				}
			}
			paced := embed.NewRateLimitedEmbedder(client)
			pace := paced.Interval()
			deps.Embedder = paced
			for _, id := range ids {
				forceEligible(t, database, "issue", id)
			}
			if _, _, _, _, err := ProcessBatch(context.Background(), deps); err != nil {
				t.Fatalf("the paced cycle: ProcessBatch: %v", err)
			}
			if got := paced.Interval(); got != 4*pace {
				t.Fatalf("the paced cycle's two failing canaries took the pace from %v to %v, want %v", pace, got, 4*pace)
			}
		})
	}
}

// A batch is at most the embedder's own MaxBatchTexts, and its token ceiling is the embedder's
// own BackgroundTokensPerMinute: two rows whose estimates together pass it go in separate calls,
// and the shared bucket is capped at it.
func TestBatchSizeAndTokenCeilingFollowTheEmbeddersLimits(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	// 30,400 characters is 7,600 estimated tokens: one fits under a 15,000 ceiling, two do not.
	long := strings.Repeat("x", 30_400)
	for i := range 2 {
		seedIssue(t, database, "LIMT", fmt.Sprintf("LIMT-%d", i+1), fmt.Sprintf("%d%s", i, long[1:]))
	}
	for i := range 3 {
		seedIssue(t, database, "LIMT", fmt.Sprintf("LIMT-%d", i+3), fmt.Sprintf("Short %d", i))
	}
	embedder := newPlaceEmbedder(configuredIdentity)
	embedder.limits = embed.Limits{MaxBatchTexts: 2, BackgroundTokensPerMinute: 15_000}
	deps := Deps{Store: database, Embedder: embedder}

	succeeded, failed, _, _, err := ProcessBatch(ctx, deps)
	if err != nil || succeeded != 2 || failed != 0 {
		t.Fatalf("first ProcessBatch = %d, %d, %v; want the two long rows", succeeded, failed, err)
	}
	if len(embedder.calls) != 2 || len(embedder.calls[0]) != 1 || len(embedder.calls[1]) != 1 {
		t.Fatalf("calls %d of sizes %v, want two calls of one long row each: together they pass the ceiling", len(embedder.calls), callSizes(embedder.calls))
	}
	var available float64
	if err := database.Pool.QueryRow(ctx, `select tokens_available from embeddings_rate_limit`).Scan(&available); err != nil {
		t.Fatalf("read the bucket: %v", err)
	}
	if available > 15_000 {
		t.Errorf("tokens_available = %v, want at most the embedder's 15,000 ceiling", available)
	}

	scan(ctx, deps)
	if sizes := callSizes(embedder.calls[2:]); len(sizes) != 2 || sizes[0] != 2 || sizes[1] != 1 {
		t.Errorf("the short rows went in calls of sizes %v, want [2 1]: MaxBatchTexts is 2", sizes)
	}
	if got := pendingCount(t, database); got != 0 {
		t.Errorf("pending after the scan = %d, want 0", got)
	}
}

// denseCJK is n runes of CJK ideographs (3 UTF-8 bytes each, 96,000 bytes at maxInputChars),
// varied by seed: text Azure counts at well over EstimateTokens' 4 characters a token, the kind
// that timed out a 40-text call.
func denseCJK(n, seed int) string {
	var b strings.Builder
	b.Grow(3 * n)
	for i := range n {
		b.WriteRune(rune(0x4E00 + (i*7919+seed*104729)%0x5000))
	}
	return b.String()
}

// azureLimits are the real AzureClient's limits at a 300,000 tokens-a-minute quota.
func azureLimits(t *testing.T) embed.Limits {
	t.Helper()
	client, err := embed.NewAzure(embed.AzureConfig{
		Endpoint:        "https://example.services.ai.azure.com/models",
		Deployment:      "embed-v-4-0",
		TokensPerMinute: 300_000,
		APIKey:          "k",
	})
	if err != nil {
		t.Fatalf("NewAzure: %v", err)
	}
	return client.Limits()
}

func batchBytes(texts []string) int {
	total := 0
	for _, text := range texts {
		total += embed.BoundedBytes(text)
	}
	return total
}

// With Azure's limits, a full batch of 96 maxInputChars CJK texts (96,000 bytes each) splits into
// groups of three, each at most Azure's 320,000-byte budget, every row in exactly one group and in
// order; the token ceiling alone (MaxBatchBytes 0) groups the same rows 25 at a time, as before.
func TestChunkForBudgetKeepsEachCallWithinTheByteBudget(t *testing.T) {
	limits := azureLimits(t)
	if limits.MaxBatchBytes != 320_000 || limits.MaxBatchTexts != 96 {
		t.Fatalf("Azure limits = %+v, want 96 texts and 320,000 bytes", limits)
	}
	rows := make([]pendingRow, limits.MaxBatchTexts)
	for i := range rows {
		rows[i] = pendingRow{kind: "comment", id: fmt.Sprint(i), text: denseCJK(32_000, i)}
	}
	check := func(limits embed.Limits, wantSizes []int) {
		t.Helper()
		groups := chunkForBudget(rows, limits)
		sizes := make([]int, len(groups))
		next := 0
		for g, group := range groups {
			sizes[g] = len(group.rows)
			texts := make([]string, len(group.rows))
			for i, row := range group.rows {
				if row.id != rows[next].id {
					t.Fatalf("group %d row %d is %s, want %s: every row once, in order", g, i, row.id, rows[next].id)
				}
				texts[i] = row.text
				next++
			}
			if limits.MaxBatchBytes > 0 && batchBytes(texts) > limits.MaxBatchBytes {
				t.Errorf("group %d carries %d bytes, over the %d budget", g, batchBytes(texts), limits.MaxBatchBytes)
			}
			if group.tokens != embed.EstimateTokens(texts) || group.tokens > limits.BackgroundTokensPerMinute {
				t.Errorf("group %d tokens = %d, want its texts' estimate %d, at most %d", g, group.tokens, embed.EstimateTokens(texts), limits.BackgroundTokensPerMinute)
			}
		}
		if next != len(rows) {
			t.Fatalf("groups hold %d rows, want all %d", next, len(rows))
		}
		if fmt.Sprint(sizes) != fmt.Sprint(wantSizes) {
			t.Errorf("group sizes = %v, want %v", sizes, wantSizes)
		}
	}
	threes := make([]int, 32)
	for i := range threes {
		threes[i] = 3
	}
	check(limits, threes)
	limits.MaxBatchBytes = 0
	check(limits, []int{25, 25, 25, 21})
}

// timeoutEmbedder answers like a place whose request timeout a call over its byte budget passes:
// such a call fails, not throttled, as Azure's client does when the 60 s run out; any other call
// embeds. It records every call.
type timeoutEmbedder struct {
	placeEmbedder
}

func (e *timeoutEmbedder) Embed(ctx context.Context, texts []string, inputType embed.InputType) ([][]float32, error) {
	if size := batchBytes(texts); size > e.limits.MaxBatchBytes {
		e.calls = append(e.calls, append([]string(nil), texts...))
		return nil, fmt.Errorf("embed: azure call: %d bytes: %w", size, context.DeadlineExceeded)
	}
	return e.placeEmbedder.Embed(ctx, texts, inputType)
}

// A full batch of 96 pending maxInputChars CJK rows, through ProcessBatch, under an embedder whose
// call over its byte budget would time out: every call stays within the budget and succeeds the
// first time, so nothing is bisected or sent twice, and every row is current.
func TestAFullBatchOfDenseTextIsEmbeddedWithinTheByteBudgetWithoutBisection(t *testing.T) {
	database := storetest.Open(t)
	limits := azureLimits(t)
	// The token pace is not under test: a ceiling this high never waits.
	limits.BackgroundTokensPerMinute = 100_000_000
	for i := range limits.MaxBatchTexts {
		seedIssue(t, database, "DENS", fmt.Sprintf("DENS-%d", i+1), denseCJK(32_000, i))
	}
	embedder := &timeoutEmbedder{placeEmbedder{identity: configuredIdentity, limits: limits}}

	succeeded, failed, blocked, throttled, err := ProcessBatch(context.Background(), Deps{Store: database, Embedder: embedder})
	if err != nil || succeeded != 96 || failed != 0 || blocked || throttled {
		t.Fatalf("ProcessBatch = %d, %d, %v, %v, %v; want all 96 committed", succeeded, failed, blocked, throttled, err)
	}
	seen := map[string]bool{}
	for i, call := range embedder.calls {
		if size := batchBytes(call); size > limits.MaxBatchBytes {
			t.Errorf("call %d carried %d bytes, over the %d budget", i, size, limits.MaxBatchBytes)
		}
		for _, text := range call {
			if seen[text] {
				t.Errorf("call %d sent a text a second time: a call failed and was bisected", i)
			}
			seen[text] = true
		}
	}
	if len(embedder.calls) != 32 || len(seen) != 96 {
		t.Errorf("%d calls carrying %d distinct texts, want 32 calls of three: the fewest the budget allows", len(embedder.calls), len(seen))
	}
	if got := pendingCount(t, database); got != 0 {
		t.Errorf("pending after the batch = %d, want 0", got)
	}
}

func callSizes(calls [][]string) []int {
	sizes := make([]int, len(calls))
	for i, call := range calls {
		sizes[i] = len(call)
	}
	return sizes
}
