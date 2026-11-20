package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/embed"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/logtest"
)

// The meaning-search bench: a synthetic corpus of production's size and shape, the partition pass
// over it, and a full member's meaning search measured on it. Each step is env-gated so CI never
// runs it, and each is run by a build of its own over its own copy of the corpus: the corpus is
// built by the build before embeddings was partitioned (6cfd282231), so both builds read the same
// rows, and the build after it partitions a copy (TestPartitionEmbeddingsOnBenchCorpus) before it
// searches it. The order is TestBuildMeaningSearchBenchCorpus with the earlier build, a copy of the
// database for each build, TestPartitionEmbeddingsOnBenchCorpus with this one on its copy, then
// TestMeaningSearchOnBenchCorpus with each build on its own copy, in turn.

// benchCorpusSQL builds the corpus on a migrated database that holds no issue: production's
// counts of 2026-11-17 times bench_corpus.scale (embedqueue/testdata/coverage_corpus.sql's,
// which this follows) - 11,151 issues, and document_block 738,255, comment_block 184,555,
// message_block 48,386 and ask_part 12,816 window rows, with their owners: 29,531 documents of 25
// windows each, 92,278 comments, 24,193 messages and 6,408 asks of two windows each. They are spread
// over 20 projects as production's 20 are, the n-th of them holding a share proportional to 1/n
// (production's own split was not readable; its largest project holds most of its rows).
//
// Every row is embedded by embed.BedrockModel at its text, and its vector is one of eight topics of
// its project (a random unit vector each) plus two random vectors of a pool of 4,096 at weights 0.7
// and 0.35, normalised: rows of one topic lie near each other and near no other topic, and no two
// rows share a vector. The queries are the 40 rows of bench_queries: a topic of a project, the
// project's share of the queries following its share of the rows, plus one vector of the pool at
// 0.5, normalised.
//
// The triggers of the tables it fills are off for the load, and every index of embeddings that
// backs no constraint, the HNSW index among them, is dropped first and built again after it, as
// coverage_corpus.sql does; VACUUM and ANALYZE are the caller's.
const benchCorpusSQL = `
begin;
do $$ begin
  if exists (select 1 from issues) or exists (select 1 from embeddings) then
    raise exception 'the bench corpus is built on a database with no issue and no embeddings row';
  end if;
end $$;
alter table issues disable trigger user;
alter table artifacts disable trigger user;
alter table artifact_versions disable trigger user;
alter table comments disable trigger user;
alter table messages disable trigger user;
alter table asks disable trigger user;
alter table embeddings disable trigger user;

create table bench_plan as
select round(11151 * s.scale)::int as issues, round(29531 * s.scale)::int as documents,
       round(92278 * s.scale)::int as comments, round(24193 * s.scale)::int as messages,
       round(6408 * s.scale)::int as asks
from (select current_setting('bench_corpus.scale')::numeric as scale) s;

create table bench_projects as
select n, 'C' || lpad(n::text, 2, '0') as key, (1.0 / n) / (select sum(1.0 / m) from generate_series(1, 20) m) as share
from generate_series(1, 20) n;
create table bench_bounds as
select key, n, coalesce(sum(share) over (order by n rows between unbounded preceding and 1 preceding), 0) as low,
       sum(share) over (order by n) as high
from bench_projects;

insert into projects (key, name, default_assignee) select key, 'Bench ' || key, 'alice' from bench_projects;

-- project_of(x) is the project whose share of [0, 1) holds x.
create function pg_temp.project_of(x float8) returns text language sql stable as $f$
  select key from bench_bounds where x >= low and x < high union all select 'C20' limit 1
$f$;

-- normals() is 1,536 standard normal draws (Box-Muller), and scaled_unit(x, scale) is x normalised
-- to the length scale. pgvector adds two vectors but multiplies none by a number, so each pool
-- vector is kept at the three lengths the rows and the queries add it at.
create function pg_temp.normals(seed int) returns float8[] language sql volatile as $f$
  select array_agg(sqrt(-2 * ln(1 - random())) * cos(2 * pi() * random()) order by d) from generate_series(1, 1536) d
$f$;
create function pg_temp.scaled_unit(x float8[], scale float8) returns vector language sql immutable as $f$
  select (select array_agg(e * scale / norm order by i) from unnest(x) with ordinality u(e, i))::real[]::vector
  from (select sqrt(sum(e * e)) as norm from unnest(x) e) n
$f$;
create table bench_normals as select g as n, pg_temp.normals(g) as x from generate_series(0, 4095) g;
create table bench_noise as
select n, pg_temp.scaled_unit(x, 0.7) as v07, pg_temp.scaled_unit(x, 0.35) as v035, pg_temp.scaled_unit(x, 0.5) as v05
from bench_normals;
create unique index on bench_noise (n);
create table bench_topics as
select p.key, t, pg_temp.scaled_unit(pg_temp.normals(p.n * 8 + t), 1) as v from bench_projects p cross join generate_series(0, 7) t;
create unique index on bench_topics (key, t);

-- vector_of(project, k) is row k's vector in project.
create function pg_temp.vector_of(p text, k bigint) returns vector language sql stable as $f$
  select l2_normalize(topic.v + a.v07 + b.v035)
  from bench_topics topic, bench_noise a, bench_noise b
  where topic.key = p and topic.t = (k * 7919) % 8 and a.n = (k * 104729) % 4096 and b.n = (k * 1299709 + 17) % 4096
$f$;

insert into issues (key, project_key, number, title, created_by, rank, created_at, updated_at)
select p.key || '-' || g, p.key, g, 'Bench issue ' || g || ' of ' || p.key, '{"kind": "user", "id": "alice"}'::jsonb,
       lpad(to_hex(g), 8, '0'), now() - make_interval(days => g % 400), now() - make_interval(days => g % 400)
from bench_plan c cross join generate_series(1, c.issues) g
cross join lateral (select pg_temp.project_of((g * 0.6180339887) - floor(g * 0.6180339887)) as key) p;

create table bench_issue_of as select project_key, array_agg(key order by number) as keys from issues group by project_key;

insert into artifacts (id, issue_key, slug, name, kind, is_primary, created_by, project_key)
select md5('doc:' || d)::uuid, o.keys[1 + (d % cardinality(o.keys))], 'doc-' || d, 'doc-' || d || '.md', 'doc', false,
       '{"kind": "user", "id": "alice"}'::jsonb, p.key
from bench_plan c cross join generate_series(1, c.documents) d
cross join lateral (select pg_temp.project_of((d * 0.7548776662) - floor(d * 0.7548776662)) as key) p
join bench_issue_of o on o.project_key = p.key;
insert into artifact_versions (artifact_id, number, markdown, mime, size)
select id, 1, 'A bench document.', 'text/markdown', 17 from artifacts where slug like 'doc-%';

insert into comments (id, issue_key, author, body)
select md5('comment:' || g)::uuid, o.keys[1 + (g % cardinality(o.keys))], '{"kind": "user", "id": "alice"}'::jsonb, 'A bench comment.'
from bench_plan c cross join generate_series(1, c.comments) g
cross join lateral (select pg_temp.project_of((g * 0.5698402910) - floor(g * 0.5698402910)) as key) p
join bench_issue_of o on o.project_key = p.key;
insert into messages (id, issue_key, author, body)
select md5('message:' || g)::uuid, o.keys[1 + (g % cardinality(o.keys))], '{"kind": "user", "id": "alice"}'::jsonb, 'A bench message.'
from bench_plan c cross join generate_series(1, c.messages) g
cross join lateral (select pg_temp.project_of((g * 0.4142135623) - floor(g * 0.4142135623)) as key) p
join bench_issue_of o on o.project_key = p.key;
insert into asks (id, issue_key, author, question)
select md5('ask:' || g)::uuid, o.keys[1 + (g % cardinality(o.keys))], '{"kind": "user", "id": "alice"}'::jsonb, 'A bench question?'
from bench_plan c cross join generate_series(1, c.asks) g
cross join lateral (select pg_temp.project_of((g * 0.3247179572) - floor(g * 0.3247179572)) as key) p
join bench_issue_of o on o.project_key = p.key;

create temp table bench_indexes as
select x.indexrelid::regclass::text as name, pg_get_indexdef(x.indexrelid) as definition
from pg_index x where x.indrelid = 'embeddings'::regclass
  and not exists (select 1 from pg_constraint k where k.conindid = x.indexrelid);
do $$ declare dropped record; begin
  for dropped in select name from bench_indexes loop execute 'drop index ' || dropped.name; end loop;
end $$;

insert into embeddings (kind, id, text_snapshot, content_hash, embedded_hash, model, embedding, embedded_at, covers, project_key)
select w.kind, w.id, w.text, md5(w.text), md5(w.text), current_setting('bench_corpus.model'), pg_temp.vector_of(w.project, w.k),
       now(), w.covers, w.project
from (
  select 'issue' as kind, i.key as id, 'Bench issue ' || i.key as text, null::text[] as covers, i.project_key as project,
         i.number::bigint * 31 + 7 as k
    from issues i
  union all
  select 'document_block', a.id::text || '#b' || b, 'Block ' || b || ' of ' || a.slug, array['b' || b], a.project_key,
         abs(hashtext(a.id::text)::bigint) * 29 + b
    from artifacts a cross join generate_series(1, 25) b where a.slug like 'doc-%'
  union all
  select 'comment_block', 'comment/' || c.id::text || '#b' || b, 'Comment window ' || b, array['b' || b], i.project_key,
         abs(hashtext(c.id::text)::bigint) * 3 + b
    from comments c join issues i on i.key = c.issue_key cross join generate_series(1, 2) b
  union all
  select 'message_block', 'message/' || m.id::text || '#b' || b, 'Message window ' || b, array['b' || b], i.project_key,
         abs(hashtext(m.id::text)::bigint) * 5 + b
    from messages m join issues i on i.key = m.issue_key cross join generate_series(1, 2) b
  union all
  select 'ask_part', 'ask/' || k.id::text || '#p' || b, 'Ask part ' || b, array['p' || b], i.project_key,
         abs(hashtext(k.id::text)::bigint) * 11 + b
    from asks k join issues i on i.key = k.issue_key cross join generate_series(1, 2) b
) w;

create table bench_queries as
select q as n, l2_normalize(t.v + noise.v05) as v
from generate_series(0, 39) q
cross join lateral (select pg_temp.project_of((q + 0.5) / 40.0) as key) p
join bench_topics t on t.key = p.key and t.t = q % 8
join bench_noise noise on noise.n = (q * 2654435761) % 4096;

do $$ declare built record; begin
  for built in select definition from bench_indexes order by name loop execute built.definition; end loop;
end $$;

alter table issues enable trigger user;
alter table artifacts enable trigger user;
alter table artifact_versions enable trigger user;
alter table comments enable trigger user;
alter table messages enable trigger user;
alter table asks enable trigger user;
alter table embeddings enable trigger user;
commit;
`

// TestBuildMeaningSearchBenchCorpus builds benchCorpusSQL's corpus on DISPATCH_BENCH_BUILD_URL, at
// DISPATCH_BENCH_SCALE (1, production's size, when unset), and logs what it built: the rows of
// each kind, embeddings' size, and how long the load and the index builds took.
func TestBuildMeaningSearchBenchCorpus(t *testing.T) {
	databaseURL := os.Getenv("DISPATCH_BENCH_BUILD_URL")
	if databaseURL == "" {
		t.Skip("DISPATCH_BENCH_BUILD_URL must name an empty database to build the bench corpus on")
	}
	scale := os.Getenv("DISPATCH_BENCH_SCALE")
	if scale == "" {
		scale = "1"
	}
	ctx := context.Background()
	database, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Pool.Close()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	conn, err := database.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	for _, setting := range []string{
		"select set_config('bench_corpus.scale', '" + scale + "', false)",
		"select set_config('bench_corpus.model', '" + embed.BedrockModel + "', false)",
		"set maintenance_work_mem = '8GB'", "set max_parallel_maintenance_workers = 8", "set statement_timeout = 0",
	} {
		if _, err := conn.Exec(ctx, setting); err != nil {
			t.Fatalf("%s: %v", setting, err)
		}
	}
	started := time.Now()
	if _, err := conn.Conn().PgConn().Exec(ctx, benchCorpusSQL).ReadAll(); err != nil {
		t.Fatalf("build the bench corpus: %v", err)
	}
	built := time.Since(started)
	if _, err := conn.Exec(ctx, "vacuum analyze"); err != nil {
		t.Fatalf("vacuum analyze: %v", err)
	}
	rows, err := conn.Query(ctx, `select kind, count(*) from embeddings group by kind order by kind`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var kind string
		var count int64
		if err := rows.Scan(&kind, &count); err != nil {
			t.Fatal(err)
		}
		t.Logf("bench corpus: %s rows=%d", kind, count)
	}
	rows.Close()
	var bytes int64
	var projects int
	if err := conn.QueryRow(ctx, `select (select coalesce((select sum(pg_total_relation_size(relid)) from pg_partition_tree('embeddings')), pg_total_relation_size('embeddings'))::bigint),
		(select count(*) from projects)`).
		Scan(&bytes, &projects); err != nil {
		t.Fatal(err)
	}
	t.Logf("bench corpus: scale=%s projects=%d embeddings_bytes=%d (%.1f GiB) built in %v", scale, projects, bytes, float64(bytes)/(1<<30), built)
}

// TestPartitionEmbeddingsOnBenchCorpus migrates DISPATCH_BENCH_DATABASE_URL with this build and
// runs the fill's whole pass over it, as a deployment's boot does, and logs how long each took and
// what the pass's own lines say: the rows it copied and in how many batches, and how long the swap
// held embeddings. Run by the build that partitions embeddings; on an earlier one it labels
// nothing and logs no copy.
func TestPartitionEmbeddingsOnBenchCorpus(t *testing.T) {
	databaseURL := os.Getenv("DISPATCH_BENCH_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DISPATCH_BENCH_DATABASE_URL must name a bench corpus copy")
	}
	ctx := context.Background()
	database, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Pool.Close()
	started := time.Now()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Logf("bench partition: migrate took %v", time.Since(started))
	records := &logtest.Recorder{}
	logtest.SetDefault(t, records)
	started = time.Now()
	if _, err := database.FillEmbeddingProjectKeys(ctx); err != nil {
		t.Fatalf("fill: %v", err)
	}
	t.Logf("bench partition: the fill's pass took %v", time.Since(started))
	for _, record := range records.Records() {
		var attrs []string
		record.Attrs(func(attr slog.Attr) bool {
			attrs = append(attrs, attr.Key+"="+attr.Value.String())
			return true
		})
		t.Logf("bench partition: %s %s", record.Message, strings.Join(attrs, " "))
	}
	if _, err := database.Pool.Exec(ctx, "vacuum analyze embeddings"); err != nil {
		t.Fatalf("vacuum analyze: %v", err)
	}
}

// TestMeaningSearchOnBenchCorpus measures a full member's meaning search on
// DISPATCH_BENCH_DATABASE_URL: the 40 queries of bench_queries, each searched once to warm and then
// DISPATCH_BENCH_ROUNDS times (10 when unset), logs p50, p95 and the maximum, and writes each
// query's hits, kind/id in order, as one JSON line to DISPATCH_BENCH_HITS_FILE when it is set, so
// two builds' answers can be compared rank for rank. With DISPATCH_BENCH_EXACT set it also reads
// each query's exact 20 nearest items by a sequential scan of every vector and logs how many of
// them each answer holds. The reader is alice, a full member, and no project is private, so every
// partition is read (searchReaderPartitions' fast path).
func TestMeaningSearchOnBenchCorpus(t *testing.T) {
	databaseURL := os.Getenv("DISPATCH_BENCH_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DISPATCH_BENCH_DATABASE_URL must name a bench corpus copy")
	}
	rounds := 10
	if listed := os.Getenv("DISPATCH_BENCH_ROUNDS"); listed != "" {
		parsed, err := strconv.Atoi(listed)
		if err != nil || parsed < 1 {
			t.Fatalf("DISPATCH_BENCH_ROUNDS = %q", listed)
		}
		rounds = parsed
	}
	ctx := context.Background()
	database, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(database.Pool.Close)
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	queries := benchQueries(t, database)
	embedder := &fakeEmbedder{vectors: map[string][]float32{}}
	for i, vector := range queries {
		embedder.vectors[benchQueryText(i)] = vector
	}
	handler := newBenchMeaningHandler(t, database, embedder)

	hits := make([][]string, len(queries))
	for i := range queries {
		hits[i] = benchSearch(t, handler, i)
	}
	var latencies []time.Duration
	for range rounds {
		for i := range queries {
			started := time.Now()
			benchSearch(t, handler, i)
			latencies = append(latencies, time.Since(started))
		}
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50 := latencies[(len(latencies)-1)/2]
	p95 := latencies[(len(latencies)*95+99)/100-1]
	t.Logf("bench search: %d meaning searches: p50=%v p95=%v max=%v", len(latencies), p50, p95, latencies[len(latencies)-1])

	if path := os.Getenv("DISPATCH_BENCH_HITS_FILE"); path != "" {
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		for i, answer := range hits {
			line, err := json.Marshal(map[string]any{"query": i, "hits": answer})
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(file, string(line))
		}
	}
	if os.Getenv("DISPATCH_BENCH_EXACT") != "" {
		var found, wanted int
		for i, vector := range queries {
			exact := benchExactNearest(t, database, vector, 20)
			held := 0
			for _, item := range exact {
				for _, hit := range hits[i] {
					if hit == item {
						held++
						break
					}
				}
			}
			found, wanted = found+held, wanted+len(exact)
			t.Logf("bench exact: query %d holds %d of its exact %d nearest", i, held, len(exact))
		}
		t.Logf("bench exact: recall@20 %d of %d (%.3f)", found, wanted, float64(found)/float64(wanted))
	}
}

// benchQueryText is query i's text: words no row holds, so the keyword legs match nothing and the
// page is the meaning leg's alone.
func benchQueryText(i int) string {
	return fmt.Sprintf("zorblax%c%c quandary", 'a'+i/26, 'a'+i%26)
}

func benchQueries(t *testing.T, database *store.Store) [][]float32 {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `select v::text from bench_queries order by n`)
	if err != nil {
		t.Fatalf("read bench_queries: %v", err)
	}
	defer rows.Close()
	var queries [][]float32
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			t.Fatal(err)
		}
		fields := strings.Split(strings.Trim(text, "[]"), ",")
		vector := make([]float32, len(fields))
		for i, field := range fields {
			value, err := strconv.ParseFloat(field, 32)
			if err != nil {
				t.Fatalf("parse a query vector: %v", err)
			}
			vector[i] = float32(value)
		}
		queries = append(queries, vector)
	}
	if err := rows.Err(); err != nil || len(queries) == 0 {
		t.Fatalf("read bench_queries: %d queries, %v", len(queries), err)
	}
	return queries
}

func benchSearch(t *testing.T, handler http.Handler, i int) []string {
	t.Helper()
	response := searchRequest(t, handler, url.Values{"q": {benchQueryText(i)}, "limit": {"20"}}.Encode())
	if response.Code != http.StatusOK {
		t.Fatalf("search %d: status=%d body=%s", i, response.Code, response.Body.String())
	}
	answer := decodeBody[model.SearchResponse](t, response)
	if answer.Degraded != "" {
		t.Fatalf("search %d answered degraded (%s)", i, answer.Degraded)
	}
	return hnswHits(answer)
}

// benchExactNearest is the limit items nearest vector by their nearest vector, read exactly by a
// sequential scan, as search names them (kind/id).
func benchExactNearest(t *testing.T, database *store.Store, vector []float32, limit int) []string {
	t.Helper()
	ctx := context.Background()
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "set local enable_indexscan = off; set local enable_bitmapscan = off; set local statement_timeout = 0"); err != nil {
		t.Fatal(err)
	}
	literal := make([]string, len(vector))
	for i, value := range vector {
		literal[i] = strconv.FormatFloat(float64(value), 'g', -1, 32)
	}
	rows, err := tx.Query(ctx, `
		select item from (
			select case kind when 'issue' then 'issue/' || id
			                 when 'document_block' then 'document/' || split_part(id, '#', 1)
			                 when 'comment_block' then 'comment/' || split_part(split_part(id, '#', 1), '/', 2)
			                 when 'message_block' then 'message/' || split_part(split_part(id, '#', 1), '/', 2)
			                 when 'ask_part' then 'ask/' || split_part(split_part(id, '#', 1), '/', 2) end as item,
			       max(1 - (embedding <=> $1::vector)) as r
			  from embeddings where embedding is not null and model = $2
			 group by 1
		) items where item is not null and r >= $4 order by r desc limit $3`,
		"["+strings.Join(literal, ",")+"]", embed.BedrockModel, limit, searchMeaningFloor)
	if err != nil {
		t.Fatalf("read the exact nearest items: %v", err)
	}
	defer rows.Close()
	var items []string
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			t.Fatal(err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return items
}

// newBenchMeaningHandler is newCorpusSearchHandler's server with embedder configured, so its
// searches run the meaning statement.
func newBenchMeaningHandler(t *testing.T, database *store.Store, embedder embed.Embedder) http.Handler {
	t.Helper()
	broker := events.NewBroker()
	documentService := docs.New(docs.Deps{
		Store: database, Events: broker, ServerURL: "https://dispatch.example", Settle: time.Hour,
	})
	t.Cleanup(func() {
		if err := documentService.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown document service: %v", err)
		}
	})
	deps, err := NewDeps(DepsInput{
		Store:       database,
		Identity:    headerIdentity(database),
		AgentTokens: sharedAgentTokens(t, "agent-token"),
		ServerURL:   "https://dispatch.example",
		Docs:        documentService,
		Events:      broker,
		Embedder:    embedder,
	})
	if err != nil {
		t.Fatalf("create bench API dependencies: %v", err)
	}
	mux := http.NewServeMux()
	t.Cleanup(Register(mux, t.Context(), deps).Wait)
	return mux
}
