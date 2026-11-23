package api

// This file holds search's statements as main ran them at df18d9be, the commit INFRA-4864 starts
// from: the keyword statement searchQuery, search's three meaning statements, the related-item
// check's six meaning statements, and the meaning document arm of search's three apart. Their
// meaning document arm reads each document's latest version through a lateral over
// artifact_versions for its date, and they are the before side this change is held to: the
// plan tests fail on them, the measurement reads them beside the head's, and the equality tests
// hold the head's statements to their answers.
//
// Every piece is written out, including the ones the package builds in Go: refs' visibility
// predicate (refs.VisibleKind, refs.VisibleEnd) over each statement's own credential parameter,
// store.CanonicalUUID's and store.CastableUUID's casts, askBlockNodeSQL, searchWindowLimit's
// `limit 1000`, searchFusedSQL's weights and searchQOpener's parameter numbers. Nothing here
// calls the package's builders, since an oracle built from them would move with the change it
// is the oracle of. A piece several statements share is one constant, written once, and each
// statement is the concatenation of its pieces in the order the package's builders join them,
// so every statement is still a constant whose every byte is in this file.
//
// These are not the main* oracles of document_search_oracle_test.go, which are the release
// before document_search and keep their names; the mainAtDF18 prefix says whose these are. They
// are frozen: they never follow an edit to the package's statements, since that edit is what
// they exist to be compared with.

// mainAtDF18VisibilityHead is the opening of refs' visibility predicate as main rendered it, up
// to its comparison with the acting credential: the read of the node's graph_nodes row and the
// case that names the node's author. Each statement follows it with the comparison over its own
// credential parameter and the graph_nodes key of the node it tests.
const mainAtDF18VisibilityHead = `coalesce((select visible_node.visibility <> 'author' or coalesce(case
		when coalesce(visible_node.made_by->>'owner', '') <> ''
			then jsonb_build_object('kind', 'person', 'id', case when 'person' = 'person' then user_threads_login(visible_node.made_by->>'owner') else visible_node.made_by->>'owner' end)
		when visible_node.made_by->>'kind' = 'user' and coalesce(visible_node.made_by->>'id', '') <> ''
			then jsonb_build_object('kind', 'person', 'id', case when 'person' = 'person' then user_threads_login(visible_node.made_by->>'id') else visible_node.made_by->>'id' end)
		when coalesce(visible_node.made_by->>'id', '') <> ''
		     and (coalesce(visible_node.made_by->>'kind', '') <> 'session' or coalesce((visible_node.made_by->>'proven')::boolean, false))
			then jsonb_build_object('kind', coalesce(visible_node.made_by->>'kind', ''), 'id', case when coalesce(visible_node.made_by->>'kind', '') = 'person' then user_threads_login(visible_node.made_by->>'id') else visible_node.made_by->>'id' end)
		when coalesce(visible_node.made_by->>'service', '') <> ''
			then jsonb_build_object('kind', 'service', 'id', case when 'service' = 'person' then user_threads_login(visible_node.made_by->>'service') else visible_node.made_by->>'service' end)`

// mainAtDF18SearchKeywordLegs is main's searchKeywordLegsSQL: the five kinds' keyword arms, the
// issue kind twice (its matches and the issues the query names by key), each ranked by
// ts_rank_cd with length normalisation (32).
const mainAtDF18SearchKeywordLegs = `
  select 'issue' as kind, 'keyword' as list, i.key as issue_key, null::uuid as owner_artifact_id, null::uuid as artifact_id, i.key as id, null::text as block_id,
         null::text as window_id, null::text[] as covers,
         i.title as issue_title, i.status as issue_status,
         null::text as owner_project, null::text as owner_slug, null::text as owner_name, i.updated_at,
         false as own, ts_rank_cd(i.search, q.tsq, 32) as r
    from issues i, q
   where i.search @@ q.tsq and not (i.key = any(q.own_keys)) and ($2 = '' or i.project_key = $2)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or i.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or i.updated_at >= q.updated_since)
  union all
  select 'issue', 'keyword', i.key, null::uuid, null::uuid, i.key, null::text,
         null::text, null::text[],
         i.title, i.status, null::text, null::text, null::text, i.updated_at,
         true, ts_rank_cd(i.search, q.tsq, 32)
    from q, unnest(q.own_keys) as named(key), issues i
   where i.key = named.key and ($2 = '' or i.project_key = $2)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or i.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or i.updated_at >= q.updated_since)
  union all
  select 'document' as kind, 'keyword', a.issue_key, case when a.issue_key is null then a.id else null::uuid end, a.id, a.id::text, null::text,
         null::text, null::text[],
         i.title, i.status, p.key, a.slug, a.name,
         coalesce(i.updated_at, v.created_at),
         false, ts_rank_cd(v.search, q.tsq, 32)
    from document_search v
    join artifacts a on a.id = v.artifact_id
    left join issues i on i.key = a.issue_key
    join projects p on p.key = a.project_key, q
   where a.kind = 'doc' and v.search @@ q.tsq and ($2 = '' or p.key = $2)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or a.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or v.created_at >= q.updated_since)
  union all
  select 'comment' as kind, 'keyword', c.issue_key, c.artifact_id, coalesce(c.artifact_id, (c.anchor->>'artifact_id')::uuid), c.id::text, null::text,
         null::text, null::text[],
         i.title, i.status, p.key, a.slug, a.name,
         coalesce(i.updated_at, c.created_at),
         false, ts_rank_cd(c.search, q.tsq, 32)
    from comments c
    left join issues i on i.key = c.issue_key
    left join artifacts a on a.id = c.artifact_id
    left join projects p on p.key = a.project_key, q
   where c.search @@ q.tsq and ($2 = '' or coalesce(i.project_key, p.key) = $2)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or c.author->>'id' = q.author_filter)
     and (q.updated_since is null or c.created_at >= q.updated_since)
  union all
  select 'ask' as kind, 'keyword', k.issue_key, case when k.issue_key is null then coalesce(k.artifact_id, k.block_artifact_id) else null::uuid end,
         coalesce(k.artifact_id, k.block_artifact_id, (case when pg_input_is_valid(k.anchor->>'artifact_id', 'uuid') then (k.anchor->>'artifact_id')::uuid end)), k.id::text, case when exists (select 1 from document_blocks d where d.artifact_id = k.block_artifact_id and d.block_id = k.block_id) then k.block_id end,
         null::text, null::text[],
         i.title, i.status, p.key, a.slug, a.name, coalesce(i.updated_at, k.created_at),
         false, ts_rank_cd(k.search, q.tsq, 32)
    from asks k
    left join issues i on i.key = k.issue_key
    left join artifacts a on a.id = coalesce(k.artifact_id, k.block_artifact_id)
    left join projects p on p.key = a.project_key, q
   where k.search @@ q.tsq and ($2 = '' or coalesce(i.project_key, p.key) = $2)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or k.author->>'id' = q.author_filter)
     and (q.updated_since is null or k.created_at >= q.updated_since)
  union all
  select 'message' as kind, 'keyword', m.issue_key, null::uuid, null::uuid, m.id::text, null::text,
         null::text, null::text[],
         i.title, i.status, null::text, null::text, null::text, i.updated_at,
         false, ts_rank_cd(m.search, q.tsq, 32)
    from messages m join issues i on i.key = m.issue_key, q
   where m.search @@ q.tsq and ($2 = '' or i.project_key = $2)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or m.author->>'id' = q.author_filter)
     and (q.updated_since is null or m.created_at >= q.updated_since)`

// mainAtDF18SearchQuery is main's searchQuery, the keyword statement: searchQOpener(9), then
// searchQueryBody over mainAtDF18SearchKeywordLegs and searchFusedSQL with the acting credential
// at $15. It takes the fifteen parameters searchQueryArguments binds.
const mainAtDF18SearchQuery = `
with q as (select
         case when $9 = '' then websearch_to_tsquery('english', $1)
              when $10 = '' then websearch_to_tsquery('english', $9)
              else websearch_to_tsquery('english', $9) && websearch_to_tsquery('english', $10)
         end as tsq,
         $4::text as term, $11::text[] as own_keys,
         $12::text as status_filter, $13::text as author_filter, $14::timestamptz as updated_since),
kinds as (select * from (` + mainAtDF18SearchKeywordLegs + `
) arms where /* visibility predicate: every kind */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($15::jsonb)->>'kind', 'id', case when ($15::jsonb)->>'kind' = 'person' then user_threads_login(($15::jsonb)->>'id') else ($15::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = case arms.kind when 'document' then 'artifact' else arms.kind end and visible_node.id = case case arms.kind when 'document' then 'artifact' else arms.kind end when 'artifact' then (select store_artifact.ref_key from artifacts store_artifact where store_artifact.id::text = arms.id::text) else arms.id::text end), true)
),
ranked as (
  select *, row_number() over (partition by kind order by own desc, r desc, updated_at desc, id) as pos,
         count(*) over (partition by kind) as matches
    from kinds
),
legs as (
  select * from ranked where pos <= $7
),
fused as (
  select kind, id, bool_or(own) as own_lead,
         sum(case list when 'keyword' then 1 * r else 0.4 * r end) / ($8 + 1)
           + case when bool_or(own) then 1 else 0 end as score
    from legs group by kind, id
),
totals as (
  select (select coalesce(sum(matches), 0) from (select max(matches) as matches from legs group by kind) each_kind) as total,
         (select count(*) from fused) as reachable
),
page as (
  select l.*, f.score, f.own_lead
    from legs l join fused f using (kind, id)
   order by f.score desc, case when f.own_lead then l.id end, l.updated_at desc, l.id
   limit $3 offset $6
)
select t.total, t.reachable, r.kind, case when r.owner_artifact_id is null then 'issue' else 'document' end,
       r.issue_key, r.issue_title, r.issue_status,
       r.owner_project, r.owner_slug, r.owner_artifact_id::text, r.owner_name,
       ar.slug, ar.name, coalesce(ar.is_primary, false), r.id, r.block_id, hit.block_id, r.score::float8,
       ts_headline('english',
         search_text(case when q.term <> '' and strpos(lower(txt.text), lower(q.term)) > 0
              then substr(txt.text, greatest(1, strpos(lower(txt.text), lower(q.term)) - 1500), 4000)
              else left(txt.text, 4000) end),
         q.tsq, $5) as headline
  from totals t cross join q
  left join (page r left join artifacts ar on ar.id = r.artifact_id) on true
  -- Mirrors the kind arms in kinds above (issue/document/comment/ask/message -> table and text
  -- column); the two must stay in sync. issue reuses r.issue_title, already carried from the
  -- same issues row by kinds, rather than re-reading it. ask reads the text its search vector is
  -- built from (asks_search: the question, then ask_search_suffix's option labels and
  -- descriptions, answer text and selected labels), so its snippet shows what the query matched.
  left join lateral (
    select case r.kind
      when 'issue' then r.issue_title
      when 'document' then (select v.markdown from artifact_versions v where v.artifact_id = r.artifact_id order by v.number desc limit 1) -- re-resolves the latest version kinds already found once; accepted, bounded by the page size
      when 'comment' then (select c.body from comments c where c.id = r.id::uuid)
      when 'ask' then (select k.question || ' ' || ask_search_suffix(k.options, k.answer) from asks k where k.id = r.id::uuid)
      when 'message' then (select m.body from messages m where m.id = r.id::uuid)
    end as text
  ) txt on true
  -- The block of r.covers that matched, if the row carries a window. Each covered block's own
  -- text comes from the table its indexer writes it to: a document's, a comment's and an ask's
  -- block texts are kept per version or revision, the latest being the current text. A message
  -- is never edited and keeps no text table (refs/item_blocks.go's table map), so its block is
  -- ranked on the excerpt its row holds, the block's first refs.BlockExcerptRunes runes.
  left join lateral (
    select covered.block_id
      from unnest(r.covers) with ordinality as covered(block_id, ord)
      cross join lateral (
        select case r.kind
          when 'document' then (select x.text from document_block_texts x
                                 where x.artifact_id = r.artifact_id and x.block_id = covered.block_id
                                 order by x.version desc limit 1)
          when 'comment' then (select x.text from comment_block_texts x
                                 where x.comment_id = r.id::uuid and x.block_id = covered.block_id
                                 order by x.revision desc limit 1)
          when 'ask' then (select x.text from ask_block_texts x
                                 where x.ask_id = r.id::uuid and x.block_id = covered.block_id
                                 order by x.revision desc limit 1)
          when 'message' then (select x.excerpt from message_blocks x
                                 where x.message_id = r.id::uuid and x.block_id = covered.block_id)
        end as text
      ) block
     where /* visibility predicate: every kind */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($15::jsonb)->>'kind', 'id', case when ($15::jsonb)->>'kind' = 'person' then user_threads_login(($15::jsonb)->>'id') else ($15::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = case r.kind when 'document' then 'block' else r.kind || '_block' end and visible_node.id = case r.kind when 'document' then ar.ref_key else r.kind || '/' || r.id end || '#' || covered.block_id), true)
     order by ts_rank_cd(to_tsvector('english', search_text(coalesce(block.text, ''))), q.tsq) desc, covered.ord
     limit 1
  ) hit on true
 order by r.score desc, case when r.own_lead then r.id end, r.updated_at desc, r.id
`

// mainAtDF18SearchMeaningOpener is main's searchQOpener(12), the q search's three meaning
// statements open with.
const mainAtDF18SearchMeaningOpener = `
with q as (select
         case when $12 = '' then websearch_to_tsquery('english', $1)
              when $13 = '' then websearch_to_tsquery('english', $12)
              else websearch_to_tsquery('english', $12) && websearch_to_tsquery('english', $13)
         end as tsq,
         $4::text as term, $14::text[] as own_keys,
         $15::text as status_filter, $16::text as author_filter, $17::timestamptz as updated_since),
`

// mainAtDF18MeaningDocumentCandidates, mainAtDF18MeaningDocumentCandidatesScoped and
// mainAtDF18MeaningDocumentCandidatesJoined are main's meaning_document_candidates CTE in search's
// three meaning statements (every project, the row's own project_key, and the project read off
// the document's own row), with the acting credential at $18: the nearest document windows,
// each joined to its document, and the document's date read from its latest version through a
// lateral over artifact_versions. Each is the CTE from its name to its closing parenthesis, the
// text the statement holds between `meaning_issue`'s `),` and `meaning_document`.
const (
	mainAtDF18MeaningDocumentCandidates = `meaning_document_candidates as (
  select a.issue_key, a.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, p.key as owner_project, a.slug as owner_slug, a.name as owner_name,
         coalesce(i.updated_at, v.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join artifacts a on a.id = (case when split_part(e.id, '#', 1) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(e.id, '#', 1))::uuid end)
    left join issues i on i.key = a.issue_key
    join projects p on p.key = a.project_key
    join lateral (select created_at from artifact_versions v where v.artifact_id = a.id order by v.number desc limit 1) v on true, q
   where e.kind = 'document_block' and a.kind = 'doc' and e.embedding is not null and e.model = $11
     and /* visibility predicate: artifact */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($18::jsonb)->>'kind', 'id', case when ($18::jsonb)->>'kind' = 'person' then user_threads_login(($18::jsonb)->>'id') else ($18::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'artifact' and visible_node.id = a.ref_key), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or a.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or v.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
)`
	mainAtDF18MeaningDocumentCandidatesScoped = `meaning_document_candidates as (
  select a.issue_key, a.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, p.key as owner_project, a.slug as owner_slug, a.name as owner_name,
         coalesce(i.updated_at, v.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join artifacts a on a.id = (case when split_part(e.id, '#', 1) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(e.id, '#', 1))::uuid end)
    left join issues i on i.key = a.issue_key
    join projects p on p.key = a.project_key
    join lateral (select created_at from artifact_versions v where v.artifact_id = a.id order by v.number desc limit 1) v on true, q
   where e.kind = 'document_block' and a.kind = 'doc' and e.embedding is not null and e.model = $11 and e.project_key = $2
     and /* visibility predicate: artifact */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($18::jsonb)->>'kind', 'id', case when ($18::jsonb)->>'kind' = 'person' then user_threads_login(($18::jsonb)->>'id') else ($18::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'artifact' and visible_node.id = a.ref_key), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or a.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or v.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
)`
	mainAtDF18MeaningDocumentCandidatesJoined = `meaning_document_candidates as (
  select a.issue_key, a.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, p.key as owner_project, a.slug as owner_slug, a.name as owner_name,
         coalesce(i.updated_at, v.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join artifacts a on a.id = (case when split_part(e.id, '#', 1) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(e.id, '#', 1))::uuid end)
    left join issues i on i.key = a.issue_key
    join projects p on p.key = a.project_key
    join lateral (select created_at from artifact_versions v where v.artifact_id = a.id order by v.number desc limit 1) v on true, q
   where e.kind = 'document_block' and a.kind = 'doc' and e.embedding is not null and e.model = $11 and ($2 = '' or p.key = $2)
     and /* visibility predicate: artifact */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($18::jsonb)->>'kind', 'id', case when ($18::jsonb)->>'kind' = 'person' then user_threads_login(($18::jsonb)->>'id') else ($18::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'artifact' and visible_node.id = a.ref_key), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or a.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or v.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
)`
)

// mainAtDF18SearchMeaningArms, mainAtDF18SearchMeaningArmsScoped and
// mainAtDF18SearchMeaningArmsJoined are the five meaning arms of searchQueryMeaningBody as main
// built them for search, in each of the three project scopes, with the acting credential at
// $18: each kind's candidate CTE and the arm over it, from meaning_issue_candidates through
// meaning_message. The document arm's candidates are the constants above. The joined scope's
// comment arm is the one whose joins stand below its candidate limit
// (searchMeaningCommentArmJoined).
const (
	mainAtDF18SearchMeaningArms = `meaning_issue_candidates as (
  select i.key as issue_key, i.key as id, i.title as issue_title, i.status as issue_status, i.updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join issues i on i.key = e.id, q
   where e.kind = 'issue' and e.embedding is not null and e.model = $11
     and /* visibility predicate: issue */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($18::jsonb)->>'kind', 'id', case when ($18::jsonb)->>'kind' = 'person' then user_threads_login(($18::jsonb)->>'id') else ($18::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'issue' and visible_node.id = i.key), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or i.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or i.updated_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit $7
),
meaning_issue as (
  select 'issue' as kind, 'meaning' as list, issue_key, null::uuid as owner_artifact_id, null::uuid as artifact_id, id, null::text as block_id,
         null::text as window_id, null::text[] as covers,
         issue_title, issue_status, null::text as owner_project, null::text as owner_slug, null::text as owner_name, updated_at,
         false as own, r
    from meaning_issue_candidates where r >= $10
),
` + mainAtDF18MeaningDocumentCandidates + `,
meaning_document as (
  select distinct on (meaning_document_candidates.id) 'document' as kind, 'meaning' as list, issue_key, case when issue_key is null then id else null::uuid end, id, id::text, null::text,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_document_candidates where r >= $10
   order by meaning_document_candidates.id, r desc, window_id
),
meaning_comment_candidates as (
  select e.id as window_id, e.covers, (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end) as comment_id,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
   where e.kind = 'comment_block' and e.embedding is not null and e.model = $11
     and /* visibility predicate: comment */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($18::jsonb)->>'kind', 'id', case when ($18::jsonb)->>'kind' = 'person' then user_threads_login(($18::jsonb)->>'id') else ($18::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'comment' and visible_node.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)::text), true)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_comment as (
  select distinct on (c.id) 'comment' as kind, 'meaning' as list, c.issue_key, c.artifact_id, coalesce(c.artifact_id, (c.anchor->>'artifact_id')::uuid), c.id::text, null::text,
         meaning_comment_candidates.window_id, meaning_comment_candidates.covers,
         i.title, i.status, p.key, a.slug, a.name,
         coalesce(i.updated_at, c.created_at),
         false, meaning_comment_candidates.r
    from meaning_comment_candidates
    join comments c on c.id = meaning_comment_candidates.comment_id
    left join issues i on i.key = c.issue_key
    left join artifacts a on a.id = c.artifact_id
    left join projects p on p.key = a.project_key, q
   where meaning_comment_candidates.r >= $10
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or c.author->>'id' = q.author_filter)
     and (q.updated_since is null or c.created_at >= q.updated_since)
   order by c.id, meaning_comment_candidates.r desc, meaning_comment_candidates.window_id
),
meaning_ask_candidates as (
  select k.issue_key, k.artifact_id, k.block_artifact_id, k.anchor, k.id, case when exists (select 1 from document_blocks d where d.artifact_id = k.block_artifact_id and d.block_id = k.block_id) then k.block_id end as block_id, e.id as window_id, e.covers,
         i.title as issue_title, i.status as issue_status,
         p.key as owner_project, a.slug as owner_slug, a.name as owner_name, coalesce(i.updated_at, k.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join asks k on k.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    left join issues i on i.key = k.issue_key
    left join artifacts a on a.id = coalesce(k.artifact_id, k.block_artifact_id)
    left join projects p on p.key = a.project_key, q
   where e.kind = 'ask_part' and e.embedding is not null and e.model = $11
     and /* visibility predicate: ask */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($18::jsonb)->>'kind', 'id', case when ($18::jsonb)->>'kind' = 'person' then user_threads_login(($18::jsonb)->>'id') else ($18::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'ask' and visible_node.id = k.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or k.author->>'id' = q.author_filter)
     and (q.updated_since is null or k.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_ask as (
  select distinct on (meaning_ask_candidates.id) 'ask' as kind, 'meaning' as list, issue_key, case when issue_key is null then coalesce(artifact_id, block_artifact_id) else null::uuid end,
         coalesce(artifact_id, block_artifact_id, (case when pg_input_is_valid(anchor->>'artifact_id', 'uuid') then (anchor->>'artifact_id')::uuid end)), id::text, block_id,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_ask_candidates where r >= $10
   order by meaning_ask_candidates.id, r desc, window_id
),
meaning_message_candidates as (
  select m.issue_key, m.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, i.updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join messages m on m.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    join issues i on i.key = m.issue_key, q
   where e.kind = 'message_block' and e.embedding is not null and e.model = $11
     and /* visibility predicate: message */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($18::jsonb)->>'kind', 'id', case when ($18::jsonb)->>'kind' = 'person' then user_threads_login(($18::jsonb)->>'id') else ($18::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'message' and visible_node.id = m.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or m.author->>'id' = q.author_filter)
     and (q.updated_since is null or m.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_message as (
  select distinct on (meaning_message_candidates.id) 'message' as kind, 'meaning' as list, issue_key, null::uuid, null::uuid, id::text, null::text,
         window_id, covers,
         issue_title, issue_status, null::text, null::text, null::text, updated_at,
         false, r
    from meaning_message_candidates where r >= $10
   order by meaning_message_candidates.id, r desc, window_id
),
`
	mainAtDF18SearchMeaningArmsScoped = `meaning_issue_candidates as (
  select i.key as issue_key, i.key as id, i.title as issue_title, i.status as issue_status, i.updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join issues i on i.key = e.id, q
   where e.kind = 'issue' and e.embedding is not null and e.model = $11 and e.project_key = $2
     and /* visibility predicate: issue */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($18::jsonb)->>'kind', 'id', case when ($18::jsonb)->>'kind' = 'person' then user_threads_login(($18::jsonb)->>'id') else ($18::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'issue' and visible_node.id = i.key), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or i.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or i.updated_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit $7
),
meaning_issue as (
  select 'issue' as kind, 'meaning' as list, issue_key, null::uuid as owner_artifact_id, null::uuid as artifact_id, id, null::text as block_id,
         null::text as window_id, null::text[] as covers,
         issue_title, issue_status, null::text as owner_project, null::text as owner_slug, null::text as owner_name, updated_at,
         false as own, r
    from meaning_issue_candidates where r >= $10
),
` + mainAtDF18MeaningDocumentCandidatesScoped + `,
meaning_document as (
  select distinct on (meaning_document_candidates.id) 'document' as kind, 'meaning' as list, issue_key, case when issue_key is null then id else null::uuid end, id, id::text, null::text,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_document_candidates where r >= $10
   order by meaning_document_candidates.id, r desc, window_id
),
meaning_comment_candidates as (
  select e.id as window_id, e.covers, (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end) as comment_id,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
   where e.kind = 'comment_block' and e.embedding is not null and e.model = $11 and e.project_key = $2
     and /* visibility predicate: comment */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($18::jsonb)->>'kind', 'id', case when ($18::jsonb)->>'kind' = 'person' then user_threads_login(($18::jsonb)->>'id') else ($18::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'comment' and visible_node.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)::text), true)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_comment as (
  select distinct on (c.id) 'comment' as kind, 'meaning' as list, c.issue_key, c.artifact_id, coalesce(c.artifact_id, (c.anchor->>'artifact_id')::uuid), c.id::text, null::text,
         meaning_comment_candidates.window_id, meaning_comment_candidates.covers,
         i.title, i.status, p.key, a.slug, a.name,
         coalesce(i.updated_at, c.created_at),
         false, meaning_comment_candidates.r
    from meaning_comment_candidates
    join comments c on c.id = meaning_comment_candidates.comment_id
    left join issues i on i.key = c.issue_key
    left join artifacts a on a.id = c.artifact_id
    left join projects p on p.key = a.project_key, q
   where meaning_comment_candidates.r >= $10
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or c.author->>'id' = q.author_filter)
     and (q.updated_since is null or c.created_at >= q.updated_since)
   order by c.id, meaning_comment_candidates.r desc, meaning_comment_candidates.window_id
),
meaning_ask_candidates as (
  select k.issue_key, k.artifact_id, k.block_artifact_id, k.anchor, k.id, case when exists (select 1 from document_blocks d where d.artifact_id = k.block_artifact_id and d.block_id = k.block_id) then k.block_id end as block_id, e.id as window_id, e.covers,
         i.title as issue_title, i.status as issue_status,
         p.key as owner_project, a.slug as owner_slug, a.name as owner_name, coalesce(i.updated_at, k.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join asks k on k.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    left join issues i on i.key = k.issue_key
    left join artifacts a on a.id = coalesce(k.artifact_id, k.block_artifact_id)
    left join projects p on p.key = a.project_key, q
   where e.kind = 'ask_part' and e.embedding is not null and e.model = $11 and e.project_key = $2
     and /* visibility predicate: ask */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($18::jsonb)->>'kind', 'id', case when ($18::jsonb)->>'kind' = 'person' then user_threads_login(($18::jsonb)->>'id') else ($18::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'ask' and visible_node.id = k.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or k.author->>'id' = q.author_filter)
     and (q.updated_since is null or k.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_ask as (
  select distinct on (meaning_ask_candidates.id) 'ask' as kind, 'meaning' as list, issue_key, case when issue_key is null then coalesce(artifact_id, block_artifact_id) else null::uuid end,
         coalesce(artifact_id, block_artifact_id, (case when pg_input_is_valid(anchor->>'artifact_id', 'uuid') then (anchor->>'artifact_id')::uuid end)), id::text, block_id,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_ask_candidates where r >= $10
   order by meaning_ask_candidates.id, r desc, window_id
),
meaning_message_candidates as (
  select m.issue_key, m.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, i.updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join messages m on m.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    join issues i on i.key = m.issue_key, q
   where e.kind = 'message_block' and e.embedding is not null and e.model = $11 and e.project_key = $2
     and /* visibility predicate: message */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($18::jsonb)->>'kind', 'id', case when ($18::jsonb)->>'kind' = 'person' then user_threads_login(($18::jsonb)->>'id') else ($18::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'message' and visible_node.id = m.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or m.author->>'id' = q.author_filter)
     and (q.updated_since is null or m.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_message as (
  select distinct on (meaning_message_candidates.id) 'message' as kind, 'meaning' as list, issue_key, null::uuid, null::uuid, id::text, null::text,
         window_id, covers,
         issue_title, issue_status, null::text, null::text, null::text, updated_at,
         false, r
    from meaning_message_candidates where r >= $10
   order by meaning_message_candidates.id, r desc, window_id
),
`
	mainAtDF18SearchMeaningArmsJoined = `meaning_issue_candidates as (
  select i.key as issue_key, i.key as id, i.title as issue_title, i.status as issue_status, i.updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join issues i on i.key = e.id, q
   where e.kind = 'issue' and e.embedding is not null and e.model = $11 and ($2 = '' or i.project_key = $2)
     and /* visibility predicate: issue */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($18::jsonb)->>'kind', 'id', case when ($18::jsonb)->>'kind' = 'person' then user_threads_login(($18::jsonb)->>'id') else ($18::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'issue' and visible_node.id = i.key), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or i.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or i.updated_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit $7
),
meaning_issue as (
  select 'issue' as kind, 'meaning' as list, issue_key, null::uuid as owner_artifact_id, null::uuid as artifact_id, id, null::text as block_id,
         null::text as window_id, null::text[] as covers,
         issue_title, issue_status, null::text as owner_project, null::text as owner_slug, null::text as owner_name, updated_at,
         false as own, r
    from meaning_issue_candidates where r >= $10
),
` + mainAtDF18MeaningDocumentCandidatesJoined + `,
meaning_document as (
  select distinct on (meaning_document_candidates.id) 'document' as kind, 'meaning' as list, issue_key, case when issue_key is null then id else null::uuid end, id, id::text, null::text,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_document_candidates where r >= $10
   order by meaning_document_candidates.id, r desc, window_id
),
meaning_comment_candidates as (
  select c.issue_key, c.artifact_id, c.anchor, c.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, p.key as owner_project, a.slug as owner_slug, a.name as owner_name,
         coalesce(i.updated_at, c.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join comments c on c.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    left join issues i on i.key = c.issue_key
    left join artifacts a on a.id = c.artifact_id
    left join projects p on p.key = a.project_key, q
   where e.kind = 'comment_block' and e.embedding is not null and e.model = $11 and ($2 = '' or coalesce(i.project_key, p.key) = $2)
     and /* visibility predicate: comment */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($18::jsonb)->>'kind', 'id', case when ($18::jsonb)->>'kind' = 'person' then user_threads_login(($18::jsonb)->>'id') else ($18::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'comment' and visible_node.id = c.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or c.author->>'id' = q.author_filter)
     and (q.updated_since is null or c.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_comment as (
  select distinct on (meaning_comment_candidates.id) 'comment' as kind, 'meaning' as list, issue_key, artifact_id, coalesce(artifact_id, (anchor->>'artifact_id')::uuid), id::text, null::text,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_comment_candidates where r >= $10
   order by meaning_comment_candidates.id, r desc, window_id
),
meaning_ask_candidates as (
  select k.issue_key, k.artifact_id, k.block_artifact_id, k.anchor, k.id, case when exists (select 1 from document_blocks d where d.artifact_id = k.block_artifact_id and d.block_id = k.block_id) then k.block_id end as block_id, e.id as window_id, e.covers,
         i.title as issue_title, i.status as issue_status,
         p.key as owner_project, a.slug as owner_slug, a.name as owner_name, coalesce(i.updated_at, k.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join asks k on k.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    left join issues i on i.key = k.issue_key
    left join artifacts a on a.id = coalesce(k.artifact_id, k.block_artifact_id)
    left join projects p on p.key = a.project_key, q
   where e.kind = 'ask_part' and e.embedding is not null and e.model = $11 and ($2 = '' or coalesce(i.project_key, p.key) = $2)
     and /* visibility predicate: ask */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($18::jsonb)->>'kind', 'id', case when ($18::jsonb)->>'kind' = 'person' then user_threads_login(($18::jsonb)->>'id') else ($18::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'ask' and visible_node.id = k.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or k.author->>'id' = q.author_filter)
     and (q.updated_since is null or k.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_ask as (
  select distinct on (meaning_ask_candidates.id) 'ask' as kind, 'meaning' as list, issue_key, case when issue_key is null then coalesce(artifact_id, block_artifact_id) else null::uuid end,
         coalesce(artifact_id, block_artifact_id, (case when pg_input_is_valid(anchor->>'artifact_id', 'uuid') then (anchor->>'artifact_id')::uuid end)), id::text, block_id,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_ask_candidates where r >= $10
   order by meaning_ask_candidates.id, r desc, window_id
),
meaning_message_candidates as (
  select m.issue_key, m.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, i.updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join messages m on m.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    join issues i on i.key = m.issue_key, q
   where e.kind = 'message_block' and e.embedding is not null and e.model = $11 and ($2 = '' or i.project_key = $2)
     and /* visibility predicate: message */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($18::jsonb)->>'kind', 'id', case when ($18::jsonb)->>'kind' = 'person' then user_threads_login(($18::jsonb)->>'id') else ($18::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'message' and visible_node.id = m.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or m.author->>'id' = q.author_filter)
     and (q.updated_since is null or m.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_message as (
  select distinct on (meaning_message_candidates.id) 'message' as kind, 'meaning' as list, issue_key, null::uuid, null::uuid, id::text, null::text,
         window_id, covers,
         issue_title, issue_status, null::text, null::text, null::text, updated_at,
         false, r
    from meaning_message_candidates where r >= $10
   order by meaning_message_candidates.id, r desc, window_id
),
`
)

// mainAtDF18SearchMeaningTail is the rest of searchQueryMeaningBody as main built it for search,
// the same in all three scopes: kinds over mainAtDF18SearchKeywordLegs and the five meaning arms,
// the ranking, searchFusedSQL's fusion, legs_unique, the totals and the page, with the acting
// credential at $18.
const mainAtDF18SearchMeaningTail = `kinds as (select * from (` + mainAtDF18SearchKeywordLegs + `
  union all select * from meaning_issue
  union all select * from meaning_document
  union all select * from meaning_comment
  union all select * from meaning_ask
  union all select * from meaning_message
) arms where /* visibility predicate: every kind */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($18::jsonb)->>'kind', 'id', case when ($18::jsonb)->>'kind' = 'person' then user_threads_login(($18::jsonb)->>'id') else ($18::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = case arms.kind when 'document' then 'artifact' else arms.kind end and visible_node.id = case case arms.kind when 'document' then 'artifact' else arms.kind end when 'artifact' then (select store_artifact.ref_key from artifacts store_artifact where store_artifact.id::text = arms.id::text) else arms.id::text end), true)
),
ranked as (
  select *, row_number() over (partition by kind, list order by own desc, r desc, updated_at desc, id) as pos
    from kinds
),
legs as (
  select * from ranked where pos <= $7
),
fused as (
  select kind, id, bool_or(own) as own_lead,
         sum(case list when 'keyword' then 1 * r else 0.4 * r end) / ($8 + 1)
           + case when bool_or(own) then 1 else 0 end as score
    from legs group by kind, id
),
legs_unique as (
  select distinct on (kind, id) kind, issue_key, owner_artifact_id, artifact_id, id, block_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at
    from legs
   order by kind, id, (window_id is null), r desc
),
totals as (
  select (select count(*) from (select distinct kind, id from kinds) u) as total,
         (select count(*) from fused) as reachable
),
page as (
  select l.*, f.score, f.own_lead
    from legs_unique l join fused f using (kind, id)
   order by f.score desc, case when f.own_lead then l.id end, l.updated_at desc, l.id
   limit $3 offset $6
)
select t.total, t.reachable, r.kind, case when r.owner_artifact_id is null then 'issue' else 'document' end,
       r.issue_key, r.issue_title, r.issue_status,
       r.owner_project, r.owner_slug, r.owner_artifact_id::text, r.owner_name,
       ar.slug, ar.name, coalesce(ar.is_primary, false), r.id, r.block_id, hit.block_id, r.score::float8,
       ts_headline('english',
         search_text(case when q.term <> '' and strpos(lower(txt.text), lower(q.term)) > 0
              then substr(txt.text, greatest(1, strpos(lower(txt.text), lower(q.term)) - 1500), 4000)
              else left(txt.text, 4000) end),
         q.tsq, $5) as headline
  from totals t cross join q
  left join (page r left join artifacts ar on ar.id = r.artifact_id) on true
  -- Mirrors the kind arms in kinds above (issue/document/comment/ask/message -> table and text
  -- column); the two must stay in sync. issue reuses r.issue_title, already carried from the
  -- same issues row by kinds, rather than re-reading it. ask reads the text its search vector is
  -- built from (asks_search: the question, then ask_search_suffix's option labels and
  -- descriptions, answer text and selected labels), so its snippet shows what the query matched.
  left join lateral (
    select case r.kind
      when 'issue' then r.issue_title
      when 'document' then (select v.markdown from artifact_versions v where v.artifact_id = r.artifact_id order by v.number desc limit 1) -- re-resolves the latest version kinds already found once; accepted, bounded by the page size
      when 'comment' then (select c.body from comments c where c.id = r.id::uuid)
      when 'ask' then (select k.question || ' ' || ask_search_suffix(k.options, k.answer) from asks k where k.id = r.id::uuid)
      when 'message' then (select m.body from messages m where m.id = r.id::uuid)
    end as text
  ) txt on true
  -- The block of r.covers that matched, if the row carries a window. Each covered block's own
  -- text comes from the table its indexer writes it to: a document's, a comment's and an ask's
  -- block texts are kept per version or revision, the latest being the current text. A message
  -- is never edited and keeps no text table (refs/item_blocks.go's table map), so its block is
  -- ranked on the excerpt its row holds, the block's first refs.BlockExcerptRunes runes.
  left join lateral (
    select covered.block_id
      from unnest(r.covers) with ordinality as covered(block_id, ord)
      cross join lateral (
        select case r.kind
          when 'document' then (select x.text from document_block_texts x
                                 where x.artifact_id = r.artifact_id and x.block_id = covered.block_id
                                 order by x.version desc limit 1)
          when 'comment' then (select x.text from comment_block_texts x
                                 where x.comment_id = r.id::uuid and x.block_id = covered.block_id
                                 order by x.revision desc limit 1)
          when 'ask' then (select x.text from ask_block_texts x
                                 where x.ask_id = r.id::uuid and x.block_id = covered.block_id
                                 order by x.revision desc limit 1)
          when 'message' then (select x.excerpt from message_blocks x
                                 where x.message_id = r.id::uuid and x.block_id = covered.block_id)
        end as text
      ) block
     where /* visibility predicate: every kind */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($18::jsonb)->>'kind', 'id', case when ($18::jsonb)->>'kind' = 'person' then user_threads_login(($18::jsonb)->>'id') else ($18::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = case r.kind when 'document' then 'block' else r.kind || '_block' end and visible_node.id = case r.kind when 'document' then ar.ref_key else r.kind || '/' || r.id end || '#' || covered.block_id), true)
     order by ts_rank_cd(to_tsvector('english', search_text(coalesce(block.text, ''))), q.tsq) desc, covered.ord
     limit 1
  ) hit on true
 order by r.score desc, case when r.own_lead then r.id end, r.updated_at desc, r.id
`

// mainAtDF18SearchQueryMeaning, mainAtDF18SearchQueryMeaningScoped and
// mainAtDF18SearchQueryMeaningJoined are main's searchQueryMeaning, searchQueryMeaningScoped and
// searchQueryMeaningJoined. Each takes the eighteen parameters searchMeaningQueryArguments binds.
const (
	mainAtDF18SearchQueryMeaning       = mainAtDF18SearchMeaningOpener + mainAtDF18SearchMeaningArms + mainAtDF18SearchMeaningTail
	mainAtDF18SearchQueryMeaningScoped = mainAtDF18SearchMeaningOpener + mainAtDF18SearchMeaningArmsScoped + mainAtDF18SearchMeaningTail
	mainAtDF18SearchQueryMeaningJoined = mainAtDF18SearchMeaningOpener + mainAtDF18SearchMeaningArmsJoined + mainAtDF18SearchMeaningTail
)

// mainAtDF18RelatedMeaningOpener is main's relatedQOpener followed by relatedTermWeights(12), the
// opening the related-item check's six meaning statements share: q over a tsquery already built,
// and the chosen lexemes' idf at $12 and $13.
const mainAtDF18RelatedMeaningOpener = `
with q as (select $1::tsquery as tsq, $4::text as term, '{}'::text[] as own_keys,
                  ''::text as status_filter, ''::text as author_filter, null::timestamptz as updated_since),
term_weights as (
  select quote_literal(w.lexeme)::tsquery as tq, w.idf from unnest($12::text[], $13::float8[]) w(lexeme, idf)
),
`

// mainAtDF18RelatedKeywordLegs is main's relatedKeywordLegs: mainAtDF18SearchKeywordLegs' arms,
// each ranked by relatedCoordinationRank, the sum of the idf of the chosen lexemes the row holds
// plus a thousandth of its normalised ts_rank_cd.
const mainAtDF18RelatedKeywordLegs = `
  select 'issue' as kind, 'keyword' as list, i.key as issue_key, null::uuid as owner_artifact_id, null::uuid as artifact_id, i.key as id, null::text as block_id,
         null::text as window_id, null::text[] as covers,
         i.title as issue_title, i.status as issue_status,
         null::text as owner_project, null::text as owner_slug, null::text as owner_name, i.updated_at,
         false as own, ((select sum(w.idf) from term_weights w where i.search @@ w.tq) + 0.001 * ts_rank_cd(i.search, q.tsq, 32)) as r
    from issues i, q
   where i.search @@ q.tsq and not (i.key = any(q.own_keys)) and ($2 = '' or i.project_key = $2)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or i.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or i.updated_at >= q.updated_since)
  union all
  select 'issue', 'keyword', i.key, null::uuid, null::uuid, i.key, null::text,
         null::text, null::text[],
         i.title, i.status, null::text, null::text, null::text, i.updated_at,
         true, ((select sum(w.idf) from term_weights w where i.search @@ w.tq) + 0.001 * ts_rank_cd(i.search, q.tsq, 32))
    from q, unnest(q.own_keys) as named(key), issues i
   where i.key = named.key and ($2 = '' or i.project_key = $2)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or i.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or i.updated_at >= q.updated_since)
  union all
  select 'document' as kind, 'keyword', a.issue_key, case when a.issue_key is null then a.id else null::uuid end, a.id, a.id::text, null::text,
         null::text, null::text[],
         i.title, i.status, p.key, a.slug, a.name,
         coalesce(i.updated_at, v.created_at),
         false, ((select sum(w.idf) from term_weights w where v.search @@ w.tq) + 0.001 * ts_rank_cd(v.search, q.tsq, 32))
    from document_search v
    join artifacts a on a.id = v.artifact_id
    left join issues i on i.key = a.issue_key
    join projects p on p.key = a.project_key, q
   where a.kind = 'doc' and v.search @@ q.tsq and ($2 = '' or p.key = $2)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or a.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or v.created_at >= q.updated_since)
  union all
  select 'comment' as kind, 'keyword', c.issue_key, c.artifact_id, coalesce(c.artifact_id, (c.anchor->>'artifact_id')::uuid), c.id::text, null::text,
         null::text, null::text[],
         i.title, i.status, p.key, a.slug, a.name,
         coalesce(i.updated_at, c.created_at),
         false, ((select sum(w.idf) from term_weights w where c.search @@ w.tq) + 0.001 * ts_rank_cd(c.search, q.tsq, 32))
    from comments c
    left join issues i on i.key = c.issue_key
    left join artifacts a on a.id = c.artifact_id
    left join projects p on p.key = a.project_key, q
   where c.search @@ q.tsq and ($2 = '' or coalesce(i.project_key, p.key) = $2)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or c.author->>'id' = q.author_filter)
     and (q.updated_since is null or c.created_at >= q.updated_since)
  union all
  select 'ask' as kind, 'keyword', k.issue_key, case when k.issue_key is null then coalesce(k.artifact_id, k.block_artifact_id) else null::uuid end,
         coalesce(k.artifact_id, k.block_artifact_id, (case when pg_input_is_valid(k.anchor->>'artifact_id', 'uuid') then (k.anchor->>'artifact_id')::uuid end)), k.id::text, case when exists (select 1 from document_blocks d where d.artifact_id = k.block_artifact_id and d.block_id = k.block_id) then k.block_id end,
         null::text, null::text[],
         i.title, i.status, p.key, a.slug, a.name, coalesce(i.updated_at, k.created_at),
         false, ((select sum(w.idf) from term_weights w where k.search @@ w.tq) + 0.001 * ts_rank_cd(k.search, q.tsq, 32))
    from asks k
    left join issues i on i.key = k.issue_key
    left join artifacts a on a.id = coalesce(k.artifact_id, k.block_artifact_id)
    left join projects p on p.key = a.project_key, q
   where k.search @@ q.tsq and ($2 = '' or coalesce(i.project_key, p.key) = $2)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or k.author->>'id' = q.author_filter)
     and (q.updated_since is null or k.created_at >= q.updated_since)
  union all
  select 'message' as kind, 'keyword', m.issue_key, null::uuid, null::uuid, m.id::text, null::text,
         null::text, null::text[],
         i.title, i.status, null::text, null::text, null::text, i.updated_at,
         false, ((select sum(w.idf) from term_weights w where m.search @@ w.tq) + 0.001 * ts_rank_cd(m.search, q.tsq, 32))
    from messages m join issues i on i.key = m.issue_key, q
   where m.search @@ q.tsq and ($2 = '' or i.project_key = $2)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or m.author->>'id' = q.author_filter)
     and (q.updated_since is null or m.created_at >= q.updated_since)`

// mainAtDF18RelatedMeaningArms, mainAtDF18RelatedMeaningArmsScoped and
// mainAtDF18RelatedMeaningArmsJoined are mainAtDF18SearchMeaningArms and its two scopes with the
// acting credential at $14, where the related-item check's plain meaning statements bind it.
const (
	mainAtDF18RelatedMeaningArms = `meaning_issue_candidates as (
  select i.key as issue_key, i.key as id, i.title as issue_title, i.status as issue_status, i.updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join issues i on i.key = e.id, q
   where e.kind = 'issue' and e.embedding is not null and e.model = $11
     and /* visibility predicate: issue */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($14::jsonb)->>'kind', 'id', case when ($14::jsonb)->>'kind' = 'person' then user_threads_login(($14::jsonb)->>'id') else ($14::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'issue' and visible_node.id = i.key), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or i.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or i.updated_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit $7
),
meaning_issue as (
  select 'issue' as kind, 'meaning' as list, issue_key, null::uuid as owner_artifact_id, null::uuid as artifact_id, id, null::text as block_id,
         null::text as window_id, null::text[] as covers,
         issue_title, issue_status, null::text as owner_project, null::text as owner_slug, null::text as owner_name, updated_at,
         false as own, r
    from meaning_issue_candidates where r >= $10
),
meaning_document_candidates as (
  select a.issue_key, a.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, p.key as owner_project, a.slug as owner_slug, a.name as owner_name,
         coalesce(i.updated_at, v.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join artifacts a on a.id = (case when split_part(e.id, '#', 1) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(e.id, '#', 1))::uuid end)
    left join issues i on i.key = a.issue_key
    join projects p on p.key = a.project_key
    join lateral (select created_at from artifact_versions v where v.artifact_id = a.id order by v.number desc limit 1) v on true, q
   where e.kind = 'document_block' and a.kind = 'doc' and e.embedding is not null and e.model = $11
     and /* visibility predicate: artifact */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($14::jsonb)->>'kind', 'id', case when ($14::jsonb)->>'kind' = 'person' then user_threads_login(($14::jsonb)->>'id') else ($14::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'artifact' and visible_node.id = a.ref_key), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or a.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or v.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_document as (
  select distinct on (meaning_document_candidates.id) 'document' as kind, 'meaning' as list, issue_key, case when issue_key is null then id else null::uuid end, id, id::text, null::text,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_document_candidates where r >= $10
   order by meaning_document_candidates.id, r desc, window_id
),
meaning_comment_candidates as (
  select e.id as window_id, e.covers, (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end) as comment_id,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
   where e.kind = 'comment_block' and e.embedding is not null and e.model = $11
     and /* visibility predicate: comment */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($14::jsonb)->>'kind', 'id', case when ($14::jsonb)->>'kind' = 'person' then user_threads_login(($14::jsonb)->>'id') else ($14::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'comment' and visible_node.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)::text), true)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_comment as (
  select distinct on (c.id) 'comment' as kind, 'meaning' as list, c.issue_key, c.artifact_id, coalesce(c.artifact_id, (c.anchor->>'artifact_id')::uuid), c.id::text, null::text,
         meaning_comment_candidates.window_id, meaning_comment_candidates.covers,
         i.title, i.status, p.key, a.slug, a.name,
         coalesce(i.updated_at, c.created_at),
         false, meaning_comment_candidates.r
    from meaning_comment_candidates
    join comments c on c.id = meaning_comment_candidates.comment_id
    left join issues i on i.key = c.issue_key
    left join artifacts a on a.id = c.artifact_id
    left join projects p on p.key = a.project_key, q
   where meaning_comment_candidates.r >= $10
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or c.author->>'id' = q.author_filter)
     and (q.updated_since is null or c.created_at >= q.updated_since)
   order by c.id, meaning_comment_candidates.r desc, meaning_comment_candidates.window_id
),
meaning_ask_candidates as (
  select k.issue_key, k.artifact_id, k.block_artifact_id, k.anchor, k.id, case when exists (select 1 from document_blocks d where d.artifact_id = k.block_artifact_id and d.block_id = k.block_id) then k.block_id end as block_id, e.id as window_id, e.covers,
         i.title as issue_title, i.status as issue_status,
         p.key as owner_project, a.slug as owner_slug, a.name as owner_name, coalesce(i.updated_at, k.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join asks k on k.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    left join issues i on i.key = k.issue_key
    left join artifacts a on a.id = coalesce(k.artifact_id, k.block_artifact_id)
    left join projects p on p.key = a.project_key, q
   where e.kind = 'ask_part' and e.embedding is not null and e.model = $11
     and /* visibility predicate: ask */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($14::jsonb)->>'kind', 'id', case when ($14::jsonb)->>'kind' = 'person' then user_threads_login(($14::jsonb)->>'id') else ($14::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'ask' and visible_node.id = k.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or k.author->>'id' = q.author_filter)
     and (q.updated_since is null or k.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_ask as (
  select distinct on (meaning_ask_candidates.id) 'ask' as kind, 'meaning' as list, issue_key, case when issue_key is null then coalesce(artifact_id, block_artifact_id) else null::uuid end,
         coalesce(artifact_id, block_artifact_id, (case when pg_input_is_valid(anchor->>'artifact_id', 'uuid') then (anchor->>'artifact_id')::uuid end)), id::text, block_id,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_ask_candidates where r >= $10
   order by meaning_ask_candidates.id, r desc, window_id
),
meaning_message_candidates as (
  select m.issue_key, m.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, i.updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join messages m on m.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    join issues i on i.key = m.issue_key, q
   where e.kind = 'message_block' and e.embedding is not null and e.model = $11
     and /* visibility predicate: message */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($14::jsonb)->>'kind', 'id', case when ($14::jsonb)->>'kind' = 'person' then user_threads_login(($14::jsonb)->>'id') else ($14::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'message' and visible_node.id = m.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or m.author->>'id' = q.author_filter)
     and (q.updated_since is null or m.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_message as (
  select distinct on (meaning_message_candidates.id) 'message' as kind, 'meaning' as list, issue_key, null::uuid, null::uuid, id::text, null::text,
         window_id, covers,
         issue_title, issue_status, null::text, null::text, null::text, updated_at,
         false, r
    from meaning_message_candidates where r >= $10
   order by meaning_message_candidates.id, r desc, window_id
),
`
	mainAtDF18RelatedMeaningArmsScoped = `meaning_issue_candidates as (
  select i.key as issue_key, i.key as id, i.title as issue_title, i.status as issue_status, i.updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join issues i on i.key = e.id, q
   where e.kind = 'issue' and e.embedding is not null and e.model = $11 and e.project_key = $2
     and /* visibility predicate: issue */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($14::jsonb)->>'kind', 'id', case when ($14::jsonb)->>'kind' = 'person' then user_threads_login(($14::jsonb)->>'id') else ($14::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'issue' and visible_node.id = i.key), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or i.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or i.updated_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit $7
),
meaning_issue as (
  select 'issue' as kind, 'meaning' as list, issue_key, null::uuid as owner_artifact_id, null::uuid as artifact_id, id, null::text as block_id,
         null::text as window_id, null::text[] as covers,
         issue_title, issue_status, null::text as owner_project, null::text as owner_slug, null::text as owner_name, updated_at,
         false as own, r
    from meaning_issue_candidates where r >= $10
),
meaning_document_candidates as (
  select a.issue_key, a.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, p.key as owner_project, a.slug as owner_slug, a.name as owner_name,
         coalesce(i.updated_at, v.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join artifacts a on a.id = (case when split_part(e.id, '#', 1) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(e.id, '#', 1))::uuid end)
    left join issues i on i.key = a.issue_key
    join projects p on p.key = a.project_key
    join lateral (select created_at from artifact_versions v where v.artifact_id = a.id order by v.number desc limit 1) v on true, q
   where e.kind = 'document_block' and a.kind = 'doc' and e.embedding is not null and e.model = $11 and e.project_key = $2
     and /* visibility predicate: artifact */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($14::jsonb)->>'kind', 'id', case when ($14::jsonb)->>'kind' = 'person' then user_threads_login(($14::jsonb)->>'id') else ($14::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'artifact' and visible_node.id = a.ref_key), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or a.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or v.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_document as (
  select distinct on (meaning_document_candidates.id) 'document' as kind, 'meaning' as list, issue_key, case when issue_key is null then id else null::uuid end, id, id::text, null::text,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_document_candidates where r >= $10
   order by meaning_document_candidates.id, r desc, window_id
),
meaning_comment_candidates as (
  select e.id as window_id, e.covers, (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end) as comment_id,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
   where e.kind = 'comment_block' and e.embedding is not null and e.model = $11 and e.project_key = $2
     and /* visibility predicate: comment */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($14::jsonb)->>'kind', 'id', case when ($14::jsonb)->>'kind' = 'person' then user_threads_login(($14::jsonb)->>'id') else ($14::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'comment' and visible_node.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)::text), true)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_comment as (
  select distinct on (c.id) 'comment' as kind, 'meaning' as list, c.issue_key, c.artifact_id, coalesce(c.artifact_id, (c.anchor->>'artifact_id')::uuid), c.id::text, null::text,
         meaning_comment_candidates.window_id, meaning_comment_candidates.covers,
         i.title, i.status, p.key, a.slug, a.name,
         coalesce(i.updated_at, c.created_at),
         false, meaning_comment_candidates.r
    from meaning_comment_candidates
    join comments c on c.id = meaning_comment_candidates.comment_id
    left join issues i on i.key = c.issue_key
    left join artifacts a on a.id = c.artifact_id
    left join projects p on p.key = a.project_key, q
   where meaning_comment_candidates.r >= $10
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or c.author->>'id' = q.author_filter)
     and (q.updated_since is null or c.created_at >= q.updated_since)
   order by c.id, meaning_comment_candidates.r desc, meaning_comment_candidates.window_id
),
meaning_ask_candidates as (
  select k.issue_key, k.artifact_id, k.block_artifact_id, k.anchor, k.id, case when exists (select 1 from document_blocks d where d.artifact_id = k.block_artifact_id and d.block_id = k.block_id) then k.block_id end as block_id, e.id as window_id, e.covers,
         i.title as issue_title, i.status as issue_status,
         p.key as owner_project, a.slug as owner_slug, a.name as owner_name, coalesce(i.updated_at, k.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join asks k on k.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    left join issues i on i.key = k.issue_key
    left join artifacts a on a.id = coalesce(k.artifact_id, k.block_artifact_id)
    left join projects p on p.key = a.project_key, q
   where e.kind = 'ask_part' and e.embedding is not null and e.model = $11 and e.project_key = $2
     and /* visibility predicate: ask */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($14::jsonb)->>'kind', 'id', case when ($14::jsonb)->>'kind' = 'person' then user_threads_login(($14::jsonb)->>'id') else ($14::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'ask' and visible_node.id = k.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or k.author->>'id' = q.author_filter)
     and (q.updated_since is null or k.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_ask as (
  select distinct on (meaning_ask_candidates.id) 'ask' as kind, 'meaning' as list, issue_key, case when issue_key is null then coalesce(artifact_id, block_artifact_id) else null::uuid end,
         coalesce(artifact_id, block_artifact_id, (case when pg_input_is_valid(anchor->>'artifact_id', 'uuid') then (anchor->>'artifact_id')::uuid end)), id::text, block_id,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_ask_candidates where r >= $10
   order by meaning_ask_candidates.id, r desc, window_id
),
meaning_message_candidates as (
  select m.issue_key, m.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, i.updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join messages m on m.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    join issues i on i.key = m.issue_key, q
   where e.kind = 'message_block' and e.embedding is not null and e.model = $11 and e.project_key = $2
     and /* visibility predicate: message */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($14::jsonb)->>'kind', 'id', case when ($14::jsonb)->>'kind' = 'person' then user_threads_login(($14::jsonb)->>'id') else ($14::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'message' and visible_node.id = m.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or m.author->>'id' = q.author_filter)
     and (q.updated_since is null or m.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_message as (
  select distinct on (meaning_message_candidates.id) 'message' as kind, 'meaning' as list, issue_key, null::uuid, null::uuid, id::text, null::text,
         window_id, covers,
         issue_title, issue_status, null::text, null::text, null::text, updated_at,
         false, r
    from meaning_message_candidates where r >= $10
   order by meaning_message_candidates.id, r desc, window_id
),
`
	mainAtDF18RelatedMeaningArmsJoined = `meaning_issue_candidates as (
  select i.key as issue_key, i.key as id, i.title as issue_title, i.status as issue_status, i.updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join issues i on i.key = e.id, q
   where e.kind = 'issue' and e.embedding is not null and e.model = $11 and ($2 = '' or i.project_key = $2)
     and /* visibility predicate: issue */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($14::jsonb)->>'kind', 'id', case when ($14::jsonb)->>'kind' = 'person' then user_threads_login(($14::jsonb)->>'id') else ($14::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'issue' and visible_node.id = i.key), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or i.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or i.updated_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit $7
),
meaning_issue as (
  select 'issue' as kind, 'meaning' as list, issue_key, null::uuid as owner_artifact_id, null::uuid as artifact_id, id, null::text as block_id,
         null::text as window_id, null::text[] as covers,
         issue_title, issue_status, null::text as owner_project, null::text as owner_slug, null::text as owner_name, updated_at,
         false as own, r
    from meaning_issue_candidates where r >= $10
),
meaning_document_candidates as (
  select a.issue_key, a.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, p.key as owner_project, a.slug as owner_slug, a.name as owner_name,
         coalesce(i.updated_at, v.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join artifacts a on a.id = (case when split_part(e.id, '#', 1) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(e.id, '#', 1))::uuid end)
    left join issues i on i.key = a.issue_key
    join projects p on p.key = a.project_key
    join lateral (select created_at from artifact_versions v where v.artifact_id = a.id order by v.number desc limit 1) v on true, q
   where e.kind = 'document_block' and a.kind = 'doc' and e.embedding is not null and e.model = $11 and ($2 = '' or p.key = $2)
     and /* visibility predicate: artifact */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($14::jsonb)->>'kind', 'id', case when ($14::jsonb)->>'kind' = 'person' then user_threads_login(($14::jsonb)->>'id') else ($14::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'artifact' and visible_node.id = a.ref_key), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or a.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or v.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_document as (
  select distinct on (meaning_document_candidates.id) 'document' as kind, 'meaning' as list, issue_key, case when issue_key is null then id else null::uuid end, id, id::text, null::text,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_document_candidates where r >= $10
   order by meaning_document_candidates.id, r desc, window_id
),
meaning_comment_candidates as (
  select c.issue_key, c.artifact_id, c.anchor, c.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, p.key as owner_project, a.slug as owner_slug, a.name as owner_name,
         coalesce(i.updated_at, c.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join comments c on c.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    left join issues i on i.key = c.issue_key
    left join artifacts a on a.id = c.artifact_id
    left join projects p on p.key = a.project_key, q
   where e.kind = 'comment_block' and e.embedding is not null and e.model = $11 and ($2 = '' or coalesce(i.project_key, p.key) = $2)
     and /* visibility predicate: comment */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($14::jsonb)->>'kind', 'id', case when ($14::jsonb)->>'kind' = 'person' then user_threads_login(($14::jsonb)->>'id') else ($14::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'comment' and visible_node.id = c.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or c.author->>'id' = q.author_filter)
     and (q.updated_since is null or c.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_comment as (
  select distinct on (meaning_comment_candidates.id) 'comment' as kind, 'meaning' as list, issue_key, artifact_id, coalesce(artifact_id, (anchor->>'artifact_id')::uuid), id::text, null::text,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_comment_candidates where r >= $10
   order by meaning_comment_candidates.id, r desc, window_id
),
meaning_ask_candidates as (
  select k.issue_key, k.artifact_id, k.block_artifact_id, k.anchor, k.id, case when exists (select 1 from document_blocks d where d.artifact_id = k.block_artifact_id and d.block_id = k.block_id) then k.block_id end as block_id, e.id as window_id, e.covers,
         i.title as issue_title, i.status as issue_status,
         p.key as owner_project, a.slug as owner_slug, a.name as owner_name, coalesce(i.updated_at, k.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join asks k on k.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    left join issues i on i.key = k.issue_key
    left join artifacts a on a.id = coalesce(k.artifact_id, k.block_artifact_id)
    left join projects p on p.key = a.project_key, q
   where e.kind = 'ask_part' and e.embedding is not null and e.model = $11 and ($2 = '' or coalesce(i.project_key, p.key) = $2)
     and /* visibility predicate: ask */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($14::jsonb)->>'kind', 'id', case when ($14::jsonb)->>'kind' = 'person' then user_threads_login(($14::jsonb)->>'id') else ($14::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'ask' and visible_node.id = k.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or k.author->>'id' = q.author_filter)
     and (q.updated_since is null or k.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_ask as (
  select distinct on (meaning_ask_candidates.id) 'ask' as kind, 'meaning' as list, issue_key, case when issue_key is null then coalesce(artifact_id, block_artifact_id) else null::uuid end,
         coalesce(artifact_id, block_artifact_id, (case when pg_input_is_valid(anchor->>'artifact_id', 'uuid') then (anchor->>'artifact_id')::uuid end)), id::text, block_id,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_ask_candidates where r >= $10
   order by meaning_ask_candidates.id, r desc, window_id
),
meaning_message_candidates as (
  select m.issue_key, m.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, i.updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join messages m on m.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    join issues i on i.key = m.issue_key, q
   where e.kind = 'message_block' and e.embedding is not null and e.model = $11 and ($2 = '' or i.project_key = $2)
     and /* visibility predicate: message */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($14::jsonb)->>'kind', 'id', case when ($14::jsonb)->>'kind' = 'person' then user_threads_login(($14::jsonb)->>'id') else ($14::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'message' and visible_node.id = m.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or m.author->>'id' = q.author_filter)
     and (q.updated_since is null or m.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_message as (
  select distinct on (meaning_message_candidates.id) 'message' as kind, 'meaning' as list, issue_key, null::uuid, null::uuid, id::text, null::text,
         window_id, covers,
         issue_title, issue_status, null::text, null::text, null::text, updated_at,
         false, r
    from meaning_message_candidates where r >= $10
   order by meaning_message_candidates.id, r desc, window_id
),
`
)

// mainAtDF18RelatedMeaningTail is mainAtDF18SearchMeaningTail as the related-item check's plain
// meaning statements end: over mainAtDF18RelatedKeywordLegs, fused by relatedFusedSQL's
// reciprocal rank fusion, with the acting credential at $14.
const mainAtDF18RelatedMeaningTail = `kinds as (select * from (` + mainAtDF18RelatedKeywordLegs + `
  union all select * from meaning_issue
  union all select * from meaning_document
  union all select * from meaning_comment
  union all select * from meaning_ask
  union all select * from meaning_message
) arms where /* visibility predicate: every kind */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($14::jsonb)->>'kind', 'id', case when ($14::jsonb)->>'kind' = 'person' then user_threads_login(($14::jsonb)->>'id') else ($14::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = case arms.kind when 'document' then 'artifact' else arms.kind end and visible_node.id = case case arms.kind when 'document' then 'artifact' else arms.kind end when 'artifact' then (select store_artifact.ref_key from artifacts store_artifact where store_artifact.id::text = arms.id::text) else arms.id::text end), true)
),
ranked as (
  select *, row_number() over (partition by kind, list order by own desc, r desc, updated_at desc, id) as pos
    from kinds
),
legs as (
  select * from ranked where pos <= $7
),
fused as (
  select kind, id, bool_or(own) as own_lead, sum(1.0 / ($8 + pos)) as score
    from legs group by kind, id
),
legs_unique as (
  select distinct on (kind, id) kind, issue_key, owner_artifact_id, artifact_id, id, block_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at
    from legs
   order by kind, id, (window_id is null), r desc
),
totals as (
  select (select count(*) from (select distinct kind, id from kinds) u) as total,
         (select count(*) from fused) as reachable
),
page as (
  select l.*, f.score, f.own_lead
    from legs_unique l join fused f using (kind, id)
   order by f.score desc, case when f.own_lead then l.id end, l.updated_at desc, l.id
   limit $3 offset $6
)
select t.total, t.reachable, r.kind, case when r.owner_artifact_id is null then 'issue' else 'document' end,
       r.issue_key, r.issue_title, r.issue_status,
       r.owner_project, r.owner_slug, r.owner_artifact_id::text, r.owner_name,
       ar.slug, ar.name, coalesce(ar.is_primary, false), r.id, r.block_id, hit.block_id, r.score::float8,
       ts_headline('english',
         search_text(case when q.term <> '' and strpos(lower(txt.text), lower(q.term)) > 0
              then substr(txt.text, greatest(1, strpos(lower(txt.text), lower(q.term)) - 1500), 4000)
              else left(txt.text, 4000) end),
         q.tsq, $5) as headline
  from totals t cross join q
  left join (page r left join artifacts ar on ar.id = r.artifact_id) on true
  -- Mirrors the kind arms in kinds above (issue/document/comment/ask/message -> table and text
  -- column); the two must stay in sync. issue reuses r.issue_title, already carried from the
  -- same issues row by kinds, rather than re-reading it. ask reads the text its search vector is
  -- built from (asks_search: the question, then ask_search_suffix's option labels and
  -- descriptions, answer text and selected labels), so its snippet shows what the query matched.
  left join lateral (
    select case r.kind
      when 'issue' then r.issue_title
      when 'document' then (select v.markdown from artifact_versions v where v.artifact_id = r.artifact_id order by v.number desc limit 1) -- re-resolves the latest version kinds already found once; accepted, bounded by the page size
      when 'comment' then (select c.body from comments c where c.id = r.id::uuid)
      when 'ask' then (select k.question || ' ' || ask_search_suffix(k.options, k.answer) from asks k where k.id = r.id::uuid)
      when 'message' then (select m.body from messages m where m.id = r.id::uuid)
    end as text
  ) txt on true
  -- The block of r.covers that matched, if the row carries a window. Each covered block's own
  -- text comes from the table its indexer writes it to: a document's, a comment's and an ask's
  -- block texts are kept per version or revision, the latest being the current text. A message
  -- is never edited and keeps no text table (refs/item_blocks.go's table map), so its block is
  -- ranked on the excerpt its row holds, the block's first refs.BlockExcerptRunes runes.
  left join lateral (
    select covered.block_id
      from unnest(r.covers) with ordinality as covered(block_id, ord)
      cross join lateral (
        select case r.kind
          when 'document' then (select x.text from document_block_texts x
                                 where x.artifact_id = r.artifact_id and x.block_id = covered.block_id
                                 order by x.version desc limit 1)
          when 'comment' then (select x.text from comment_block_texts x
                                 where x.comment_id = r.id::uuid and x.block_id = covered.block_id
                                 order by x.revision desc limit 1)
          when 'ask' then (select x.text from ask_block_texts x
                                 where x.ask_id = r.id::uuid and x.block_id = covered.block_id
                                 order by x.revision desc limit 1)
          when 'message' then (select x.excerpt from message_blocks x
                                 where x.message_id = r.id::uuid and x.block_id = covered.block_id)
        end as text
      ) block
     where /* visibility predicate: every kind */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($14::jsonb)->>'kind', 'id', case when ($14::jsonb)->>'kind' = 'person' then user_threads_login(($14::jsonb)->>'id') else ($14::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = case r.kind when 'document' then 'block' else r.kind || '_block' end and visible_node.id = case r.kind when 'document' then ar.ref_key else r.kind || '/' || r.id end || '#' || covered.block_id), true)
     order by ts_rank_cd(to_tsvector('english', search_text(coalesce(block.text, ''))), q.tsq) desc, covered.ord
     limit 1
  ) hit on true
 order by r.score desc, case when r.own_lead then r.id end, r.updated_at desc, r.id
`

// mainAtDF18RelatedMeaningQuery, mainAtDF18RelatedMeaningScopedQuery and
// mainAtDF18RelatedMeaningJoinedQuery are main's relatedMeaningQuery, relatedMeaningScopedQuery
// and relatedMeaningJoinedQuery: the related-item check's meaning stage for a check that leaves
// nothing out, over the fourteen parameters relatedSearch binds it.
const (
	mainAtDF18RelatedMeaningQuery       = mainAtDF18RelatedMeaningOpener + mainAtDF18RelatedMeaningArms + mainAtDF18RelatedMeaningTail
	mainAtDF18RelatedMeaningScopedQuery = mainAtDF18RelatedMeaningOpener + mainAtDF18RelatedMeaningArmsScoped + mainAtDF18RelatedMeaningTail
	mainAtDF18RelatedMeaningJoinedQuery = mainAtDF18RelatedMeaningOpener + mainAtDF18RelatedMeaningArmsJoined + mainAtDF18RelatedMeaningTail
)

// mainAtDF18RelatedMeaningExcludingArms, mainAtDF18RelatedMeaningExcludingArmsScoped and
// mainAtDF18RelatedMeaningExcludingArmsJoined are mainAtDF18SearchMeaningArms and its two scopes
// with the acting credential at $15, where the related-item check's excluding meaning statements
// bind it, the owners they leave out being $14.
const (
	mainAtDF18RelatedMeaningExcludingArms = `meaning_issue_candidates as (
  select i.key as issue_key, i.key as id, i.title as issue_title, i.status as issue_status, i.updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join issues i on i.key = e.id, q
   where e.kind = 'issue' and e.embedding is not null and e.model = $11
     and /* visibility predicate: issue */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($15::jsonb)->>'kind', 'id', case when ($15::jsonb)->>'kind' = 'person' then user_threads_login(($15::jsonb)->>'id') else ($15::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'issue' and visible_node.id = i.key), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or i.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or i.updated_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit $7
),
meaning_issue as (
  select 'issue' as kind, 'meaning' as list, issue_key, null::uuid as owner_artifact_id, null::uuid as artifact_id, id, null::text as block_id,
         null::text as window_id, null::text[] as covers,
         issue_title, issue_status, null::text as owner_project, null::text as owner_slug, null::text as owner_name, updated_at,
         false as own, r
    from meaning_issue_candidates where r >= $10
),
meaning_document_candidates as (
  select a.issue_key, a.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, p.key as owner_project, a.slug as owner_slug, a.name as owner_name,
         coalesce(i.updated_at, v.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join artifacts a on a.id = (case when split_part(e.id, '#', 1) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(e.id, '#', 1))::uuid end)
    left join issues i on i.key = a.issue_key
    join projects p on p.key = a.project_key
    join lateral (select created_at from artifact_versions v where v.artifact_id = a.id order by v.number desc limit 1) v on true, q
   where e.kind = 'document_block' and a.kind = 'doc' and e.embedding is not null and e.model = $11
     and /* visibility predicate: artifact */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($15::jsonb)->>'kind', 'id', case when ($15::jsonb)->>'kind' = 'person' then user_threads_login(($15::jsonb)->>'id') else ($15::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'artifact' and visible_node.id = a.ref_key), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or a.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or v.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_document as (
  select distinct on (meaning_document_candidates.id) 'document' as kind, 'meaning' as list, issue_key, case when issue_key is null then id else null::uuid end, id, id::text, null::text,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_document_candidates where r >= $10
   order by meaning_document_candidates.id, r desc, window_id
),
meaning_comment_candidates as (
  select e.id as window_id, e.covers, (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end) as comment_id,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
   where e.kind = 'comment_block' and e.embedding is not null and e.model = $11
     and /* visibility predicate: comment */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($15::jsonb)->>'kind', 'id', case when ($15::jsonb)->>'kind' = 'person' then user_threads_login(($15::jsonb)->>'id') else ($15::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'comment' and visible_node.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)::text), true)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_comment as (
  select distinct on (c.id) 'comment' as kind, 'meaning' as list, c.issue_key, c.artifact_id, coalesce(c.artifact_id, (c.anchor->>'artifact_id')::uuid), c.id::text, null::text,
         meaning_comment_candidates.window_id, meaning_comment_candidates.covers,
         i.title, i.status, p.key, a.slug, a.name,
         coalesce(i.updated_at, c.created_at),
         false, meaning_comment_candidates.r
    from meaning_comment_candidates
    join comments c on c.id = meaning_comment_candidates.comment_id
    left join issues i on i.key = c.issue_key
    left join artifacts a on a.id = c.artifact_id
    left join projects p on p.key = a.project_key, q
   where meaning_comment_candidates.r >= $10
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or c.author->>'id' = q.author_filter)
     and (q.updated_since is null or c.created_at >= q.updated_since)
   order by c.id, meaning_comment_candidates.r desc, meaning_comment_candidates.window_id
),
meaning_ask_candidates as (
  select k.issue_key, k.artifact_id, k.block_artifact_id, k.anchor, k.id, case when exists (select 1 from document_blocks d where d.artifact_id = k.block_artifact_id and d.block_id = k.block_id) then k.block_id end as block_id, e.id as window_id, e.covers,
         i.title as issue_title, i.status as issue_status,
         p.key as owner_project, a.slug as owner_slug, a.name as owner_name, coalesce(i.updated_at, k.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join asks k on k.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    left join issues i on i.key = k.issue_key
    left join artifacts a on a.id = coalesce(k.artifact_id, k.block_artifact_id)
    left join projects p on p.key = a.project_key, q
   where e.kind = 'ask_part' and e.embedding is not null and e.model = $11
     and /* visibility predicate: ask */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($15::jsonb)->>'kind', 'id', case when ($15::jsonb)->>'kind' = 'person' then user_threads_login(($15::jsonb)->>'id') else ($15::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'ask' and visible_node.id = k.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or k.author->>'id' = q.author_filter)
     and (q.updated_since is null or k.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_ask as (
  select distinct on (meaning_ask_candidates.id) 'ask' as kind, 'meaning' as list, issue_key, case when issue_key is null then coalesce(artifact_id, block_artifact_id) else null::uuid end,
         coalesce(artifact_id, block_artifact_id, (case when pg_input_is_valid(anchor->>'artifact_id', 'uuid') then (anchor->>'artifact_id')::uuid end)), id::text, block_id,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_ask_candidates where r >= $10
   order by meaning_ask_candidates.id, r desc, window_id
),
meaning_message_candidates as (
  select m.issue_key, m.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, i.updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join messages m on m.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    join issues i on i.key = m.issue_key, q
   where e.kind = 'message_block' and e.embedding is not null and e.model = $11
     and /* visibility predicate: message */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($15::jsonb)->>'kind', 'id', case when ($15::jsonb)->>'kind' = 'person' then user_threads_login(($15::jsonb)->>'id') else ($15::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'message' and visible_node.id = m.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or m.author->>'id' = q.author_filter)
     and (q.updated_since is null or m.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_message as (
  select distinct on (meaning_message_candidates.id) 'message' as kind, 'meaning' as list, issue_key, null::uuid, null::uuid, id::text, null::text,
         window_id, covers,
         issue_title, issue_status, null::text, null::text, null::text, updated_at,
         false, r
    from meaning_message_candidates where r >= $10
   order by meaning_message_candidates.id, r desc, window_id
),
`
	mainAtDF18RelatedMeaningExcludingArmsScoped = `meaning_issue_candidates as (
  select i.key as issue_key, i.key as id, i.title as issue_title, i.status as issue_status, i.updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join issues i on i.key = e.id, q
   where e.kind = 'issue' and e.embedding is not null and e.model = $11 and e.project_key = $2
     and /* visibility predicate: issue */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($15::jsonb)->>'kind', 'id', case when ($15::jsonb)->>'kind' = 'person' then user_threads_login(($15::jsonb)->>'id') else ($15::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'issue' and visible_node.id = i.key), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or i.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or i.updated_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit $7
),
meaning_issue as (
  select 'issue' as kind, 'meaning' as list, issue_key, null::uuid as owner_artifact_id, null::uuid as artifact_id, id, null::text as block_id,
         null::text as window_id, null::text[] as covers,
         issue_title, issue_status, null::text as owner_project, null::text as owner_slug, null::text as owner_name, updated_at,
         false as own, r
    from meaning_issue_candidates where r >= $10
),
meaning_document_candidates as (
  select a.issue_key, a.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, p.key as owner_project, a.slug as owner_slug, a.name as owner_name,
         coalesce(i.updated_at, v.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join artifacts a on a.id = (case when split_part(e.id, '#', 1) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(e.id, '#', 1))::uuid end)
    left join issues i on i.key = a.issue_key
    join projects p on p.key = a.project_key
    join lateral (select created_at from artifact_versions v where v.artifact_id = a.id order by v.number desc limit 1) v on true, q
   where e.kind = 'document_block' and a.kind = 'doc' and e.embedding is not null and e.model = $11 and e.project_key = $2
     and /* visibility predicate: artifact */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($15::jsonb)->>'kind', 'id', case when ($15::jsonb)->>'kind' = 'person' then user_threads_login(($15::jsonb)->>'id') else ($15::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'artifact' and visible_node.id = a.ref_key), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or a.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or v.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_document as (
  select distinct on (meaning_document_candidates.id) 'document' as kind, 'meaning' as list, issue_key, case when issue_key is null then id else null::uuid end, id, id::text, null::text,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_document_candidates where r >= $10
   order by meaning_document_candidates.id, r desc, window_id
),
meaning_comment_candidates as (
  select e.id as window_id, e.covers, (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end) as comment_id,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
   where e.kind = 'comment_block' and e.embedding is not null and e.model = $11 and e.project_key = $2
     and /* visibility predicate: comment */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($15::jsonb)->>'kind', 'id', case when ($15::jsonb)->>'kind' = 'person' then user_threads_login(($15::jsonb)->>'id') else ($15::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'comment' and visible_node.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)::text), true)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_comment as (
  select distinct on (c.id) 'comment' as kind, 'meaning' as list, c.issue_key, c.artifact_id, coalesce(c.artifact_id, (c.anchor->>'artifact_id')::uuid), c.id::text, null::text,
         meaning_comment_candidates.window_id, meaning_comment_candidates.covers,
         i.title, i.status, p.key, a.slug, a.name,
         coalesce(i.updated_at, c.created_at),
         false, meaning_comment_candidates.r
    from meaning_comment_candidates
    join comments c on c.id = meaning_comment_candidates.comment_id
    left join issues i on i.key = c.issue_key
    left join artifacts a on a.id = c.artifact_id
    left join projects p on p.key = a.project_key, q
   where meaning_comment_candidates.r >= $10
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or c.author->>'id' = q.author_filter)
     and (q.updated_since is null or c.created_at >= q.updated_since)
   order by c.id, meaning_comment_candidates.r desc, meaning_comment_candidates.window_id
),
meaning_ask_candidates as (
  select k.issue_key, k.artifact_id, k.block_artifact_id, k.anchor, k.id, case when exists (select 1 from document_blocks d where d.artifact_id = k.block_artifact_id and d.block_id = k.block_id) then k.block_id end as block_id, e.id as window_id, e.covers,
         i.title as issue_title, i.status as issue_status,
         p.key as owner_project, a.slug as owner_slug, a.name as owner_name, coalesce(i.updated_at, k.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join asks k on k.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    left join issues i on i.key = k.issue_key
    left join artifacts a on a.id = coalesce(k.artifact_id, k.block_artifact_id)
    left join projects p on p.key = a.project_key, q
   where e.kind = 'ask_part' and e.embedding is not null and e.model = $11 and e.project_key = $2
     and /* visibility predicate: ask */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($15::jsonb)->>'kind', 'id', case when ($15::jsonb)->>'kind' = 'person' then user_threads_login(($15::jsonb)->>'id') else ($15::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'ask' and visible_node.id = k.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or k.author->>'id' = q.author_filter)
     and (q.updated_since is null or k.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_ask as (
  select distinct on (meaning_ask_candidates.id) 'ask' as kind, 'meaning' as list, issue_key, case when issue_key is null then coalesce(artifact_id, block_artifact_id) else null::uuid end,
         coalesce(artifact_id, block_artifact_id, (case when pg_input_is_valid(anchor->>'artifact_id', 'uuid') then (anchor->>'artifact_id')::uuid end)), id::text, block_id,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_ask_candidates where r >= $10
   order by meaning_ask_candidates.id, r desc, window_id
),
meaning_message_candidates as (
  select m.issue_key, m.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, i.updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join messages m on m.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    join issues i on i.key = m.issue_key, q
   where e.kind = 'message_block' and e.embedding is not null and e.model = $11 and e.project_key = $2
     and /* visibility predicate: message */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($15::jsonb)->>'kind', 'id', case when ($15::jsonb)->>'kind' = 'person' then user_threads_login(($15::jsonb)->>'id') else ($15::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'message' and visible_node.id = m.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or m.author->>'id' = q.author_filter)
     and (q.updated_since is null or m.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_message as (
  select distinct on (meaning_message_candidates.id) 'message' as kind, 'meaning' as list, issue_key, null::uuid, null::uuid, id::text, null::text,
         window_id, covers,
         issue_title, issue_status, null::text, null::text, null::text, updated_at,
         false, r
    from meaning_message_candidates where r >= $10
   order by meaning_message_candidates.id, r desc, window_id
),
`
	mainAtDF18RelatedMeaningExcludingArmsJoined = `meaning_issue_candidates as (
  select i.key as issue_key, i.key as id, i.title as issue_title, i.status as issue_status, i.updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join issues i on i.key = e.id, q
   where e.kind = 'issue' and e.embedding is not null and e.model = $11 and ($2 = '' or i.project_key = $2)
     and /* visibility predicate: issue */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($15::jsonb)->>'kind', 'id', case when ($15::jsonb)->>'kind' = 'person' then user_threads_login(($15::jsonb)->>'id') else ($15::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'issue' and visible_node.id = i.key), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or i.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or i.updated_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit $7
),
meaning_issue as (
  select 'issue' as kind, 'meaning' as list, issue_key, null::uuid as owner_artifact_id, null::uuid as artifact_id, id, null::text as block_id,
         null::text as window_id, null::text[] as covers,
         issue_title, issue_status, null::text as owner_project, null::text as owner_slug, null::text as owner_name, updated_at,
         false as own, r
    from meaning_issue_candidates where r >= $10
),
meaning_document_candidates as (
  select a.issue_key, a.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, p.key as owner_project, a.slug as owner_slug, a.name as owner_name,
         coalesce(i.updated_at, v.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join artifacts a on a.id = (case when split_part(e.id, '#', 1) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(e.id, '#', 1))::uuid end)
    left join issues i on i.key = a.issue_key
    join projects p on p.key = a.project_key
    join lateral (select created_at from artifact_versions v where v.artifact_id = a.id order by v.number desc limit 1) v on true, q
   where e.kind = 'document_block' and a.kind = 'doc' and e.embedding is not null and e.model = $11 and ($2 = '' or p.key = $2)
     and /* visibility predicate: artifact */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($15::jsonb)->>'kind', 'id', case when ($15::jsonb)->>'kind' = 'person' then user_threads_login(($15::jsonb)->>'id') else ($15::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'artifact' and visible_node.id = a.ref_key), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or a.created_by->>'id' = q.author_filter)
     and (q.updated_since is null or v.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_document as (
  select distinct on (meaning_document_candidates.id) 'document' as kind, 'meaning' as list, issue_key, case when issue_key is null then id else null::uuid end, id, id::text, null::text,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_document_candidates where r >= $10
   order by meaning_document_candidates.id, r desc, window_id
),
meaning_comment_candidates as (
  select c.issue_key, c.artifact_id, c.anchor, c.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, p.key as owner_project, a.slug as owner_slug, a.name as owner_name,
         coalesce(i.updated_at, c.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join comments c on c.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    left join issues i on i.key = c.issue_key
    left join artifacts a on a.id = c.artifact_id
    left join projects p on p.key = a.project_key, q
   where e.kind = 'comment_block' and e.embedding is not null and e.model = $11 and ($2 = '' or coalesce(i.project_key, p.key) = $2)
     and /* visibility predicate: comment */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($15::jsonb)->>'kind', 'id', case when ($15::jsonb)->>'kind' = 'person' then user_threads_login(($15::jsonb)->>'id') else ($15::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'comment' and visible_node.id = c.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or c.author->>'id' = q.author_filter)
     and (q.updated_since is null or c.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_comment as (
  select distinct on (meaning_comment_candidates.id) 'comment' as kind, 'meaning' as list, issue_key, artifact_id, coalesce(artifact_id, (anchor->>'artifact_id')::uuid), id::text, null::text,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_comment_candidates where r >= $10
   order by meaning_comment_candidates.id, r desc, window_id
),
meaning_ask_candidates as (
  select k.issue_key, k.artifact_id, k.block_artifact_id, k.anchor, k.id, case when exists (select 1 from document_blocks d where d.artifact_id = k.block_artifact_id and d.block_id = k.block_id) then k.block_id end as block_id, e.id as window_id, e.covers,
         i.title as issue_title, i.status as issue_status,
         p.key as owner_project, a.slug as owner_slug, a.name as owner_name, coalesce(i.updated_at, k.created_at) as updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join asks k on k.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    left join issues i on i.key = k.issue_key
    left join artifacts a on a.id = coalesce(k.artifact_id, k.block_artifact_id)
    left join projects p on p.key = a.project_key, q
   where e.kind = 'ask_part' and e.embedding is not null and e.model = $11 and ($2 = '' or coalesce(i.project_key, p.key) = $2)
     and /* visibility predicate: ask */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($15::jsonb)->>'kind', 'id', case when ($15::jsonb)->>'kind' = 'person' then user_threads_login(($15::jsonb)->>'id') else ($15::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'ask' and visible_node.id = k.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or k.author->>'id' = q.author_filter)
     and (q.updated_since is null or k.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_ask as (
  select distinct on (meaning_ask_candidates.id) 'ask' as kind, 'meaning' as list, issue_key, case when issue_key is null then coalesce(artifact_id, block_artifact_id) else null::uuid end,
         coalesce(artifact_id, block_artifact_id, (case when pg_input_is_valid(anchor->>'artifact_id', 'uuid') then (anchor->>'artifact_id')::uuid end)), id::text, block_id,
         window_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at,
         false, r
    from meaning_ask_candidates where r >= $10
   order by meaning_ask_candidates.id, r desc, window_id
),
meaning_message_candidates as (
  select m.issue_key, m.id, e.id as window_id, e.covers, i.title as issue_title, i.status as issue_status, i.updated_at,
         1 - (e.embedding <=> $9::vector) as r
    from embeddings e
    join messages m on m.id = (case when split_part(split_part(e.id, '#', 1), '/', 2) ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then (split_part(split_part(e.id, '#', 1), '/', 2))::uuid end)
    join issues i on i.key = m.issue_key, q
   where e.kind = 'message_block' and e.embedding is not null and e.model = $11 and ($2 = '' or i.project_key = $2)
     and /* visibility predicate: message */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($15::jsonb)->>'kind', 'id', case when ($15::jsonb)->>'kind' = 'person' then user_threads_login(($15::jsonb)->>'id') else ($15::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = 'message' and visible_node.id = m.id::text), true)
     and (q.status_filter = '' or i.status = q.status_filter)
     and (q.author_filter = '' or m.author->>'id' = q.author_filter)
     and (q.updated_since is null or m.created_at >= q.updated_since)
   order by e.embedding <=> $9::vector
   limit 1000
),
meaning_message as (
  select distinct on (meaning_message_candidates.id) 'message' as kind, 'meaning' as list, issue_key, null::uuid, null::uuid, id::text, null::text,
         window_id, covers,
         issue_title, issue_status, null::text, null::text, null::text, updated_at,
         false, r
    from meaning_message_candidates where r >= $10
   order by meaning_message_candidates.id, r desc, window_id
),
`
)

// mainAtDF18RelatedMeaningExcludingTail is mainAtDF18RelatedMeaningTail with relatedExcludedOwners'
// filter over the owners at $14 in kinds and the acting credential at $15.
const mainAtDF18RelatedMeaningExcludingTail = `kinds as (select * from (` + mainAtDF18RelatedKeywordLegs + `
  union all select * from meaning_issue
  union all select * from meaning_document
  union all select * from meaning_comment
  union all select * from meaning_ask
  union all select * from meaning_message
) arms where not (coalesce(case when owner_artifact_id is null then 'issue:' || issue_key
                     else 'document:' || owner_artifact_id end, '') = any($14::text[])
      or (kind = 'document' and 'document:' || id = any($14::text[]))
      or (kind = 'ask' and 'ask:' || id = any($14::text[]))) and /* visibility predicate: every kind */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($15::jsonb)->>'kind', 'id', case when ($15::jsonb)->>'kind' = 'person' then user_threads_login(($15::jsonb)->>'id') else ($15::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = case arms.kind when 'document' then 'artifact' else arms.kind end and visible_node.id = case case arms.kind when 'document' then 'artifact' else arms.kind end when 'artifact' then (select store_artifact.ref_key from artifacts store_artifact where store_artifact.id::text = arms.id::text) else arms.id::text end), true)
),
ranked as (
  select *, row_number() over (partition by kind, list order by own desc, r desc, updated_at desc, id) as pos
    from kinds
),
legs as (
  select * from ranked where pos <= $7
),
fused as (
  select kind, id, bool_or(own) as own_lead, sum(1.0 / ($8 + pos)) as score
    from legs group by kind, id
),
legs_unique as (
  select distinct on (kind, id) kind, issue_key, owner_artifact_id, artifact_id, id, block_id, covers,
         issue_title, issue_status, owner_project, owner_slug, owner_name, updated_at
    from legs
   order by kind, id, (window_id is null), r desc
),
totals as (
  select (select count(*) from (select distinct kind, id from kinds) u) as total,
         (select count(*) from fused) as reachable
),
page as (
  select l.*, f.score, f.own_lead
    from legs_unique l join fused f using (kind, id)
   order by f.score desc, case when f.own_lead then l.id end, l.updated_at desc, l.id
   limit $3 offset $6
)
select t.total, t.reachable, r.kind, case when r.owner_artifact_id is null then 'issue' else 'document' end,
       r.issue_key, r.issue_title, r.issue_status,
       r.owner_project, r.owner_slug, r.owner_artifact_id::text, r.owner_name,
       ar.slug, ar.name, coalesce(ar.is_primary, false), r.id, r.block_id, hit.block_id, r.score::float8,
       ts_headline('english',
         search_text(case when q.term <> '' and strpos(lower(txt.text), lower(q.term)) > 0
              then substr(txt.text, greatest(1, strpos(lower(txt.text), lower(q.term)) - 1500), 4000)
              else left(txt.text, 4000) end),
         q.tsq, $5) as headline
  from totals t cross join q
  left join (page r left join artifacts ar on ar.id = r.artifact_id) on true
  -- Mirrors the kind arms in kinds above (issue/document/comment/ask/message -> table and text
  -- column); the two must stay in sync. issue reuses r.issue_title, already carried from the
  -- same issues row by kinds, rather than re-reading it. ask reads the text its search vector is
  -- built from (asks_search: the question, then ask_search_suffix's option labels and
  -- descriptions, answer text and selected labels), so its snippet shows what the query matched.
  left join lateral (
    select case r.kind
      when 'issue' then r.issue_title
      when 'document' then (select v.markdown from artifact_versions v where v.artifact_id = r.artifact_id order by v.number desc limit 1) -- re-resolves the latest version kinds already found once; accepted, bounded by the page size
      when 'comment' then (select c.body from comments c where c.id = r.id::uuid)
      when 'ask' then (select k.question || ' ' || ask_search_suffix(k.options, k.answer) from asks k where k.id = r.id::uuid)
      when 'message' then (select m.body from messages m where m.id = r.id::uuid)
    end as text
  ) txt on true
  -- The block of r.covers that matched, if the row carries a window. Each covered block's own
  -- text comes from the table its indexer writes it to: a document's, a comment's and an ask's
  -- block texts are kept per version or revision, the latest being the current text. A message
  -- is never edited and keeps no text table (refs/item_blocks.go's table map), so its block is
  -- ranked on the excerpt its row holds, the block's first refs.BlockExcerptRunes runes.
  left join lateral (
    select covered.block_id
      from unnest(r.covers) with ordinality as covered(block_id, ord)
      cross join lateral (
        select case r.kind
          when 'document' then (select x.text from document_block_texts x
                                 where x.artifact_id = r.artifact_id and x.block_id = covered.block_id
                                 order by x.version desc limit 1)
          when 'comment' then (select x.text from comment_block_texts x
                                 where x.comment_id = r.id::uuid and x.block_id = covered.block_id
                                 order by x.revision desc limit 1)
          when 'ask' then (select x.text from ask_block_texts x
                                 where x.ask_id = r.id::uuid and x.block_id = covered.block_id
                                 order by x.revision desc limit 1)
          when 'message' then (select x.excerpt from message_blocks x
                                 where x.message_id = r.id::uuid and x.block_id = covered.block_id)
        end as text
      ) block
     where /* visibility predicate: every kind */` + mainAtDF18VisibilityHead + `
	end = jsonb_build_object('kind', ($15::jsonb)->>'kind', 'id', case when ($15::jsonb)->>'kind' = 'person' then user_threads_login(($15::jsonb)->>'id') else ($15::jsonb)->>'id' end), false)
		from graph_nodes visible_node where visible_node.kind = case r.kind when 'document' then 'block' else r.kind || '_block' end and visible_node.id = case r.kind when 'document' then ar.ref_key else r.kind || '/' || r.id end || '#' || covered.block_id), true)
     order by ts_rank_cd(to_tsvector('english', search_text(coalesce(block.text, ''))), q.tsq) desc, covered.ord
     limit 1
  ) hit on true
 order by r.score desc, case when r.own_lead then r.id end, r.updated_at desc, r.id
`

// mainAtDF18RelatedMeaningExcludingQuery, mainAtDF18RelatedMeaningScopedExcludingQuery and
// mainAtDF18RelatedMeaningJoinedExcludingQuery are main's relatedMeaningExcludingQuery,
// relatedMeaningScopedExcludingQuery and relatedMeaningJoinedExcludingQuery: the meaning stage
// every write runs, over the fifteen parameters relatedSearch binds it.
const (
	mainAtDF18RelatedMeaningExcludingQuery       = mainAtDF18RelatedMeaningOpener + mainAtDF18RelatedMeaningExcludingArms + mainAtDF18RelatedMeaningExcludingTail
	mainAtDF18RelatedMeaningScopedExcludingQuery = mainAtDF18RelatedMeaningOpener + mainAtDF18RelatedMeaningExcludingArmsScoped + mainAtDF18RelatedMeaningExcludingTail
	mainAtDF18RelatedMeaningJoinedExcludingQuery = mainAtDF18RelatedMeaningOpener + mainAtDF18RelatedMeaningExcludingArmsJoined + mainAtDF18RelatedMeaningExcludingTail
)
