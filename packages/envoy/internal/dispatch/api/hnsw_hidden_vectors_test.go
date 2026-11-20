package api

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/embed"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// hnswQuery is the text the HNSW rows search for: it shares no word with any issue the rows
// write, so only the meaning leg finds them, and the fake embedder answers queryVector() for it.
const hnswQuery = "zorblaxian quandary"

// hnswVisible is how many issues of the project the reader sees the rows place farther from the
// query than every hidden one: fewer than one page of search (searchMaxLimit), so every hit is
// read.
const hnswVisible = 40

// hnswHiddenCounts are the numbers of issues of a project reader does not see that the HNSW rows
// place nearer the query than every visible one: DISPATCH_HNSW_HIDDEN_VECTORS, a comma-separated
// list, or reader.hiddenCounts when it is unset.
func hnswHiddenCounts(t *testing.T, reader hnswReader) []int {
	t.Helper()
	listed := os.Getenv("DISPATCH_HNSW_HIDDEN_VECTORS")
	if listed == "" {
		return reader.hiddenCounts
	}
	var counts []int
	for _, field := range strings.Split(listed, ",") {
		count, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil || count < 1 {
			t.Fatalf("DISPATCH_HNSW_HIDDEN_VECTORS = %q: %q is no positive count", listed, field)
		}
		counts = append(counts, count)
	}
	return counts
}

// hnswShape is which embeddings a seed's meaning search reads: the table the migrations leave,
// one HNSW graph over every project's vectors, or the table partitioned by project that
// store.FillEmbeddingProjectKeys swaps in, one graph a project (migration 20261120105330).
type hnswShape string

const (
	hnswSharedGraph hnswShape = "one graph over every project"
	hnswPartitioned hnswShape = "one graph a project"
)

// hnswLayout is how a row places its issues' vectors besides their cosine to the query, which is
// dimension 0. Spread puts each on an axis of its own, stride apart, with shared on the last
// dimension: points nearly equidistant from each other, as the first measurement placed them, of
// which pgvector's HNSW graph reaches 33 of 40 from the query even with no other vector in it
// (measured on 2026-11-20 in a bare table read through its index, bulk-built or written row by
// row alike), so a search of them is bounded by the graph's own reach. Curve puts them on a
// quarter circle of dimensions 2 and 3, each next to the one before it, as text of one subject
// lies: the graph reaches all 40 of those, so a search that answers fewer has lost rows to
// something other than the geometry.
type hnswLayout struct {
	curve  bool
	shared float64
	stride int
}

var (
	hnswSpreadVisible = hnswLayout{shared: 0.60, stride: 37}
	hnswSpreadHidden  = hnswLayout{shared: 0, stride: 1}
	hnswCurve         = hnswLayout{curve: true}
)

// hnswReader is whose meaning search a row reads, with the project they see, where the row places
// its visible issues and how, a project they do not see, where it places the hidden ones, and how
// many hidden ones its seeds hold.
type hnswReader struct {
	caller          privateCaller
	visible, hidden string
	layout          hnswLayout
	hiddenCounts    []int
}

var (
	// alice is a full member who sees PUB and not PRIV; her issues are spread, and on one graph
	// over every project 100 hidden vectors already take every hit from her.
	hnswAlice = hnswReader{callerAlice, seedPublic, seedPrivate, hnswSpreadVisible, []int{100, 1000, 5000}}
	// bob is a restricted member whose list names PRIV alone, so PUB is hidden from him; his
	// issues lie on the curve, so "in full" is all of them. The curve keeps its rows linked to
	// each other, and on one graph over every project the walk reaches them past 5,000 nearer
	// hidden vectors; what ends it is the scan's budget, hnsw.max_scan_tuples (20,000), which
	// 25,000 nearer hidden vectors spend before the walk reaches any row of his. That is the
	// restricted member's case at production's size, where his projects hold a few of a million
	// rows.
	hnswBob = hnswReader{callerBob, seedPrivate, seedPublic, hnswCurve, []int{5000, 25000}}
)

// TestMeasureAMeaningSearchBesideNearerHiddenVectors (HNSW, a measurement): logs what each
// reader's meaning search answers when hnswHiddenCounts vectors they may not see sit nearer the
// query than everything they see, against the same seed without them, on both shapes of
// embeddings, under the meaning transaction's own settings and under the scan's budgets raised
// far past them (hnsw.max_scan_tuples 1,000,000, hnsw.scan_mem_multiplier 64). It passes whatever
// it measures, and fails only when a seed without hidden vectors answers no hit, where it would
// measure nothing. The rules the measurement is for are
// TestAMeaningSearchFindsWhatItWouldWithoutNearerHiddenVectors and
// TestARestrictedMembersMeaningSearchFindsHisProjectWhateverTheOthersHold.
//
// The seed is verification v16's with hnswVisible issues of the project the reader sees placed
// at cosine 0.60 down to 0.50 from the query (reader.layout) and, in the seed measured, the
// hidden issues of the other project at 0.95 down to 0.85 (hnswSpreadHidden). The reader
// searches through an index-preferring pool (sequential scans and sorts off, as every plan test
// of this package reads, so the walk is the HNSW index's).
//
// Measured on 2026-11-20 against pgvector 0.8.7 (the test's own log): on one graph over every
// project, alice reads 33 hits of PUB's 40 with no hidden vector, the walk's own reach over the
// spread placement (hnswLayout), and 0 with 100 hidden vectors and 0 with 2,000, under the
// meaning transaction's settings and under the raised budgets alike. The same geometry in a
// bare pgvector table (40 visible rows, then 1,000 or 5,000 hidden ones, ef_search 400 or 1,000,
// iterative_scan relaxed_order, strict_order or off, max_scan_tuples 1,000,000,
// scan_mem_multiplier 64) finds 40 visible rows with no hidden one and 0 with them, while an
// unfiltered walk returns 996 of 1,040 rows and 696 of 5,040: what ends the walk is the shared
// graph's reach, not the scan's budget. (A read of that table that leaves out the index's own
// `embedding is not null` cannot use the partial index and sorts every row exactly, which is the
// 40; through the index the 40 spread rows alone answer 33, as search does.) On one graph a
// project, alice reads the same 33 with 100, 1,000 and 5,000 hidden vectors and bob all 40 of
// his. Run it as
// env DISPATCH_HNSW_HIDDEN_VECTORS=100,1000,5000 go test -v -run TestMeasureAMeaningSearchBesideNearerHiddenVectors ./internal/dispatch/api/
func TestMeasureAMeaningSearchBesideNearerHiddenVectors(t *testing.T) {
	for _, reader := range []hnswReader{hnswAlice, hnswBob} {
		for _, shape := range []hnswShape{hnswSharedGraph, hnswPartitioned} {
			without := hnswSearch(t, hnswSeed(t, reader, 0, shape), reader, nil)
			if len(without.Results) == 0 {
				t.Fatalf("%s: %s's search of the seed without hidden vectors holds no hit of %s's %d: the measurement measures nothing",
					shape, reader.caller, reader.visible, hnswVisible)
			}
			t.Logf("%s, %s, no hidden vector: total %d, %d hits of %s's %d",
				shape, reader.caller, without.Total, len(without.Results), reader.visible, hnswVisible)
			for _, hidden := range hnswHiddenCounts(t, reader) {
				seed := hnswSeed(t, reader, hidden, shape)
				for _, scan := range []struct {
					name     string
					settings map[string]string
				}{
					{"the meaning transaction's settings", nil},
					{"hnsw.max_scan_tuples 1000000 and hnsw.scan_mem_multiplier 64", map[string]string{"hnsw.max_scan_tuples": "1000000", "hnsw.scan_mem_multiplier": "64"}},
				} {
					with := hnswSearch(t, seed, reader, scan.settings)
					t.Logf("%s, %s, %d hidden vectors of %s nearer the query, %s: total %d, %d hits, equal to the seed without them: %t",
						shape, reader.caller, hidden, reader.hidden, scan.name, with.Total, len(with.Results),
						with.Total == without.Total && slices.Equal(hnswHits(with), hnswHits(without)))
				}
			}
		}
	}
}

// TestAMeaningSearchFindsWhatItWouldWithoutNearerHiddenVectors (HNSW): a meaning search answers a
// reader what it would answer were the vectors the reader may not see absent, even when every one
// of them is nearer the query than everything the reader sees: over hnswHiddenCounts vectors of
// PRIV, alice's total and hits, ids and order, equal those of the seed without them. Each project's
// vectors are a partition of embeddings and an HNSW graph of its own, and alice's arms read only
// the partitions of the projects she sees (searchReaderPartitions), so no vector of PRIV is in any
// graph her search walks. On one graph over every project the same seeds answer her 0 hits of 33
// (TestMeasureAMeaningSearchBesideNearerHiddenVectors), which is this row's control.
func TestAMeaningSearchFindsWhatItWouldWithoutNearerHiddenVectors(t *testing.T) {
	requireHiddenVectorsChangeNothing(t, hnswAlice, hnswPartitioned)
}

// TestARestrictedMembersMeaningSearchFindsHisProjectWhateverTheOthersHold (HNSW, R3): bob, a
// restricted member whose list names PRIV alone, reads PRIV's hits in full - all hnswVisible of
// them, in the order the seed without the crowd answers - when hnswHiddenCounts vectors of PUB, a
// project he does not see, sit nearer his query than every one of them. His arms read PRIV's
// partition and the one of no project, and nothing of PUB's.
func TestARestrictedMembersMeaningSearchFindsHisProjectWhateverTheOthersHold(t *testing.T) {
	without := requireHiddenVectorsChangeNothing(t, hnswBob, hnswPartitioned)
	placed := 0
	for _, hit := range without.Results {
		if hit.Kind == "issue" && strings.HasPrefix(hit.ID, seedPrivate+"-10") {
			placed++
		}
	}
	if placed != hnswVisible {
		t.Errorf("bob's search reads %d of PRIV's %d placed issues, want every one: they lie on one curve, which his project's graph reaches whole",
			placed, hnswVisible)
	}
}

// requireHiddenVectorsChangeNothing is the HNSW rows' one comparison: reader's search of the seed
// without hidden vectors, on shape, against the seed with each of hnswHiddenCounts of them,
// total and hits in order. It answers the search without them.
func requireHiddenVectorsChangeNothing(t *testing.T, reader hnswReader, shape hnswShape) model.SearchResponse {
	t.Helper()
	without := hnswSearch(t, hnswSeed(t, reader, 0, shape), reader, nil)
	if len(without.Results) == 0 {
		t.Fatalf("%s's search of the seed without hidden vectors holds no hit of %s's %d: the comparison proves nothing", reader.caller, reader.visible, hnswVisible)
	}
	t.Logf("%s, %s, no hidden vector: total %d, %d hits", shape, reader.caller, without.Total, len(without.Results))
	for _, hidden := range hnswHiddenCounts(t, reader) {
		with := hnswSearch(t, hnswSeed(t, reader, hidden, shape), reader, nil)
		t.Logf("%s, %s, %d hidden vectors of %s nearer the query: total %d, %d hits", shape, reader.caller, hidden, reader.hidden, with.Total, len(with.Results))
		if with.Total != without.Total || !slices.Equal(hnswHits(with), hnswHits(without)) {
			t.Errorf("%s, with %d vectors of %s nearer the query, %s's search answers a total %d and %d hits\n\t%v\nwant what the seed without them answers, a total %d and %d hits\n\t%v",
				shape, hidden, reader.hidden, reader.caller, with.Total, len(with.Results), hnswHits(with), without.Total, len(without.Results), hnswHits(without))
		}
	}
	return without
}

// hnswSeed is verification v16's seed with the fake embedder answering queryVector() for
// hnswQuery, hnswVisible issues of the project reader sees at cosine 0.60 down to 0.50 from it,
// placed by reader.layout, and hidden issues of the project they do not see at 0.95 down to 0.85,
// spread (hnswSpreadHidden), each embedded as embedqueue would have embedded it, on shape. The
// partitioned shape is the pass a deployment's boot runs (store.FillEmbeddingProjectKeys), over
// the rows already written: it copies them into the table partitioned by project and swaps it in.
func hnswSeed(t *testing.T, reader hnswReader, hidden int, shape hnswShape) privateSeed {
	t.Helper()
	embedder := &fakeEmbedder{vectors: map[string][]float32{hnswQuery: queryVector()}}
	seed := seedPrivateProjectWith(t, testServerOptions{embedder: embedder})
	hnswIssues(t, seed, reader.visible, hnswVisible, 0.60, 0.50, reader.layout)
	if hidden > 0 {
		hnswIssues(t, seed, reader.hidden, hidden, 0.95, 0.85, hnswSpreadHidden)
	}
	if shape == hnswPartitioned {
		if _, err := seed.database.FillEmbeddingProjectKeys(t.Context()); err != nil {
			t.Fatalf("partition embeddings by project: %v", err)
		}
		if _, err := seed.database.Pool.Exec(t.Context(), `analyze embeddings`); err != nil {
			t.Fatalf("analyze embeddings: %v", err)
		}
	}
	return seed
}

// hnswIssues writes count issues of project, numbered from 1001, each with the graph node of the
// seed's own issue of project copied to its key (a node no write recorded is hidden while a
// project is private, refs.VisibleKind), and gives each the unit vector of cosine nearest down to
// farthest from queryVector() (dimension 0), in steps, the rest of it placed by layout - never on
// dimension 1, which defaultVector() holds - as the fake embedder's identity embedded it: what
// search reads of a row embedqueue embedded. Spread, the rest is shared on the last dimension and
// the remainder on an axis of its own, stride apart, so the visible and the hidden issues do not
// share axes in step; on the curve, the remainder is on dimensions 2 and 3 at an angle that turns
// a quarter circle from the first issue to the last.
func hnswIssues(t *testing.T, seed privateSeed, project string, count int, nearest, farthest float64, layout hnswLayout) {
	t.Helper()
	ctx, pool := t.Context(), seed.database.Pool
	if _, err := pool.Exec(ctx, `
		insert into issues (key, project_key, number, title, created_by, rank, status)
		select $1 || '-' || (1000 + g), $1, 1000 + g, 'Neighbour ' || g || ' of ' || $1, '{"kind":"user","id":"carol@example.com"}',
			'z' || lpad(g::text, 7, '0'), 'triage'
		from generate_series(1, $2::int) g
	`, project, count); err != nil {
		t.Fatalf("write %d issues of %s: %v", count, project, err)
	}
	noded, err := pool.Exec(ctx, `
		insert into graph_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event, project_key, parties)
		select n.kind, i.key, n.supertag, n.visibility, n.state, n.attributes, n.made_at, n.made_by, n.source_event, n.project_key, n.parties
		from issues i join graph_nodes n on n.kind = 'issue' and n.id = $2
		where i.project_key = $1 and i.number > 1000
	`, project, seed.issue[project])
	if err != nil || noded.RowsAffected() != int64(count) {
		t.Fatalf("copy %s's node to %d issues of %s: %d copied, %v", seed.issue[project], count, project, noded.RowsAffected(), err)
	}
	embedded, err := pool.Exec(ctx, `
		update embeddings e
		set embedding = placed.vector, model = $6, embedded_hash = e.content_hash, embedded_at = now(), project_key = $1
		from (
			select i.key, (
				select array_agg(case
					when d = 1 then p.cosine
					when $9::bool and d = 3 then sqrt(1 - p.cosine * p.cosine) * cos(p.angle)
					when $9::bool and d = 4 then sqrt(1 - p.cosine * p.cosine) * sin(p.angle)
					when $9::bool then 0
					when d = $7::int then $8::float8
					when d = p.axis then sqrt(1 - p.cosine * p.cosine - $8::float8 * $8::float8)
					else 0 end order by d)::real[]::vector
				from generate_series(1, $7::int) d
			) as vector
			from issues i
			cross join lateral (
				select $3::float8 - ($3::float8 - $4::float8) * (i.number - 1001) / greatest($2::int - 1, 1) as cosine,
					3 + ((i.number - 1001) * $5::int) % ($7::int - 3) as axis,
					(i.number - 1001) * (pi() / 2) / greatest($2::int - 1, 1) as angle
			) p
			where i.project_key = $1 and i.number > 1000
		) placed
		where e.kind = 'issue' and e.id = placed.key
	`, project, count, nearest, farthest, layout.stride, embed.BedrockModel, embed.Dimension, layout.shared, layout.curve)
	if err != nil {
		t.Fatalf("embed %d issues of %s: %v", count, project, err)
	}
	if embedded.RowsAffected() != int64(count) {
		t.Fatalf("embedded %d of %s's %d issues, want every one: the issue trigger queued fewer embedding rows than issues", embedded.RowsAffected(), project, count)
	}
	if _, err := pool.Exec(ctx, `analyze embeddings`); err != nil {
		t.Fatalf("analyze embeddings: %v", err)
	}
}

// hnswSearch is reader's GET /api/v1/search for hnswQuery, a page of searchMaxLimit, served by the
// seed's server over a pool whose connections prefer indexes (sequential scans and sorts off, no
// JIT) and take settings besides.
func hnswSearch(t *testing.T, seed privateSeed, reader hnswReader, settings map[string]string) model.SearchResponse {
	t.Helper()
	params := map[string]string{"enable_seqscan": "off", "enable_sort": "off", "jit": "off"}
	maps.Copy(params, settings)
	deps := seed.deps
	deps.Store = &store.Store{Pool: storetest.Traced(t, seed.database.Pool, nil, params)}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/search?q="+strings.ReplaceAll(hnswQuery, " ", "+")+"&limit="+strconv.Itoa(searchMaxLimit), nil)
	request.Header = seed.readerRequest(t, reader.caller).Header
	recorder := httptest.NewRecorder()
	directServer(deps).search(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s's search = %d %s, want 200", reader.caller, recorder.Code, recorder.Body.String())
	}
	response := decodeBody[model.SearchResponse](t, recorder)
	if response.Degraded != "" {
		t.Fatalf("%s's search answered degraded (%s): the meaning leg did not run", reader.caller, response.Degraded)
	}
	return response
}

// hnswHits is each hit of a search answer as "kind/id", in its order.
func hnswHits(response model.SearchResponse) []string {
	hits := make([]string, len(response.Results))
	for i, result := range response.Results {
		hits[i] = result.Kind + "/" + result.ID
	}
	return hits
}
