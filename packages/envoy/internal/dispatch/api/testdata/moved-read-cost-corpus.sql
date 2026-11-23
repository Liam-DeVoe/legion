-- The corpus of c-moved-read-cost, which TestNoMovedReadGetsSlower (moved_read_cost_test.go) writes
-- by SQL over the database of the release before, once that build has migrated it and is stopped.
-- Each row has the shape its writer in that release stores: prioritystore's Create and relate for
-- the priorities and the advances tuples, and workflowstore's putNode for the runs and the plans.
-- These are the shapes of the R32 bench's corpus (dispatch://INFRA-1098/artifact/r32-bench-tar-gz).
-- No value is random.
--
-- Three settings name what the run made through the release before's own API before this file runs:
--   moved_read_cost.scale             1 for the check's corpus; 0.1 a tenth. Every count of issues,
--                                     priorities and tuples is scaled. An issue keeps its runs and
--                                     its children at every scale.
--   moved_read_cost.projects          the keys of the three projects, comma-separated. The first is
--                                     the project the steps read and the plans read address.
--   moved_read_cost.advances_project  the key of the project of the issues that advance priorities.
-- Each of the four projects stands, with its first issue, <key>-1, which that build's issue writer
-- made. Every issue below copies that issue's row and its node, so it has the columns that build
-- writes, and <key>-1 stays open.
--
-- In each of the three projects, issue number n + 1 is the corpus's issue n:
--   * n = 1 has 200 runs, the plan they ran from and 339 other part_of children: 34 comments, 4
--     asks and 2 messages, as production held under INFRA-1098, and 20 artifacts and 279 blocks,
--     as dispatch://INFRA-1253 puts under it;
--   * n = 5, 10, ..., 3,000 are 600 issues with 30 runs each, the plan they ran from and 100 other
--     part_of children: 50 comments, 35 blocks, 10 asks and 5 messages;
--   * the other 2,860 are issues Legion never ran.
-- All 3,461 are finished: status done, closed_at set. Beside them stand 50 priorities, 2,000 open
-- issues of the advances project with 5,000 advances tuples over them, and 1,000,000 tuples of
-- other kinds, split by the tuples_a_day of each `census: sizing tuples-a-write` line that
-- cybermonklabs/legion#3178's body records.

create temp table moved_counts as
select round(50 * s)::int as priorities,
       round(2000 * s)::int as advanced_issues,
       round(4500 * s)::int as asserted,
       round(5000 * s)::int as advances,
       round(3461 * s)::int as finished,
       round(600 * s)::int as ran,
       round(1000000 * s)::bigint as others
from (select current_setting('moved_read_cost.scale')::numeric as s) setting;

create temp table moved_projects as
select p.key, p.place - 1 as slot,
       timestamptz '2026-11-16 22:47:00+00' + (p.place - 1) * interval '5 days' as t0
from unnest(string_to_array(current_setting('moved_read_cost.projects'), ',')) with ordinality as p(key, place);

-- The finished issues of the three projects, each a copy of its project's first issue.
insert into issues (key, project_key, number, title, status, created_by, labels, priority, rank, node_kind, node_address,
                    created_at, updated_at, closed_at)
select p.key || '-' || (n + 1), p.key, n + 1, 'Finished work item ' || n || ' of project ' || p.key, 'done',
       template.created_by, template.labels, template.priority, template.rank || lpad(n::text, 7, '0'), template.node_kind,
       p.key || '-' || (n + 1),
       p.t0 + n * interval '2 minutes', p.t0 + n * interval '2 minutes' + interval '9 hours',
       p.t0 + n * interval '2 minutes' + interval '9 hours'
from moved_projects p
join issues template on template.key = p.key || '-1'
cross join moved_counts c
cross join lateral generate_series(1, c.finished) n;

-- The open issues of the advances project, spread over the statuses as the bench spreads them
-- (one in ten done), three of each four under the fourth as parent.
insert into issues (key, project_key, number, title, status, created_by, labels, priority, rank, node_kind, node_address,
                    parent_key, created_at, updated_at, closed_at)
select a.key || '-' || (n + 1), a.key, n + 1, 'Advance priority work item ' || n || ' across the board',
       (array['triage', 'icebox', 'backlog', 'todo', 'in_progress', 'testing', 'needs_review', 'backlog', 'icebox', 'done'])[n % 10 + 1],
       template.created_by, template.labels, template.priority, template.rank || lpad(n::text, 7, '0'), template.node_kind,
       a.key || '-' || (n + 1),
       case when n % 4 <> 1 then a.key || '-' || (n - (n - 1) % 4 + 1) end,
       timestamptz '2026-11-10 08:00:00+00' + n * interval '7 minutes',
       timestamptz '2026-11-10 08:00:00+00' + n * interval '7 minutes' + interval '2 hours',
       case when n % 10 = 9 then timestamptz '2026-11-10 08:00:00+00' + n * interval '7 minutes' + interval '2 hours' end
from (select current_setting('moved_read_cost.advances_project') as key) a
join issues template on template.key = a.key || '-1'
cross join moved_counts c
cross join lateral generate_series(1, c.advanced_issues) n
order by n;

update projects p set next_number = (select max(number) + 1 from issues i where i.project_key = p.key)
where p.key = any (string_to_array(current_setting('moved_read_cost.projects'), ',') || current_setting('moved_read_cost.advances_project'));

-- Each issue's node, a copy of its project's first issue's node.
insert into graph_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event, project_key, parties)
select template.kind, i.key, template.supertag, template.visibility, template.state, template.attributes, i.created_at,
       template.made_by, template.source_event, template.project_key, template.parties
from issues i
join graph_nodes template on template.kind = 'issue' and template.id = i.project_key || '-1'
where i.number > 1
  and i.project_key = any (string_to_array(current_setting('moved_read_cost.projects'), ',') || current_setting('moved_read_cost.advances_project'));

-- The priorities (prioritystore.Create): a node row whose attributes are what the person wrote, made
-- by that person with no event, and the tuples Create states, in its order. Each priority whose
-- number is a multiple of 25 is deleted: a later state tuple gone, and the row gone.
insert into priority_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
select 'priority', 'priority-' || lpad(p::text, 2, '0'), 'priority', 'project',
       case when p % 25 = 0 then 'gone' else 'live' end,
       jsonb_build_object(
         'title', 'Priority ' || p || ': ship what the people on the work board need first',
         'description', 'Work that moves priority ' || p || ' forward, written by its owner as one paragraph of why it matters now.',
         'owner', 'owner' || (p % 7),
         'rank', 'a' || lpad(p::text, 4, '0')),
       timestamptz '2026-11-12 09:00:00+00' + p * interval '1 hour',
       '{"kind": "person", "id": "owner"}'::jsonb, 0
from moved_counts c cross join lateral generate_series(1, c.priorities) p;

insert into priority_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted)
select 'priority', n.id, stated.kind, 'value', stated.value,
       n.made_at + case when stated.place = 7 then interval '1 day' else interval '0' end,
       n.made_by, 0, true
from priority_nodes n
cross join lateral (values
  (1, 'visibility', 'project'),
  (2, 'state', 'live'),
  (3, 'description', n.attributes->>'description'),
  (4, 'owner', n.attributes->>'owner'),
  (5, 'rank', n.attributes->>'rank'),
  (6, 'title', n.attributes->>'title'),
  (7, 'state', case when n.state = 'gone' then 'gone' end)
) as stated(place, kind, value)
where stated.value is not null
order by n.made_at + case when stated.place = 7 then interval '1 day' else interval '0' end, n.id, stated.place;

-- The advances tuples (prioritystore relate): stated on the issue, its far end the priority. The
-- first `asserted` each assert a pair, two or three different priorities to an issue; the rest each
-- retract one of those pairs.
insert into priority_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted)
select 'issue', current_setting('moved_read_cost.advances_project') || '-' || (pair.issue + 1), 'advances', 'priority',
       'priority-' || lpad(pair.priority::text, 2, '0'),
       timestamptz '2026-11-14 00:00:00+00' + t * interval '1 minute',
       '{"kind": "person", "id": "owner"}'::jsonb, 0, t <= c.asserted
from moved_counts c
cross join lateral generate_series(1, c.advances) t
cross join lateral (
  select ((s - 1) % c.advanced_issues) + 1 as issue,
         ((((s - 1) % c.advanced_issues) + ((s - 1) / c.advanced_issues) * ceil(c.priorities / 3.0)::int) % c.priorities) + 1 as priority
  from (select case when t <= c.asserted then t else t - c.asserted end as s) chosen
) pair
order by t;

-- The issues Legion ran: in each project, issue 1 with 200 runs and 339 children, and every fifth
-- issue up to the 600th with 30 runs and 100 children.
create temp table moved_ran as
select p.key as project, p.slot, p.t0, p.key || '-2' as issue, 1 as n, 200 as runs, true as busy
from moved_projects p
union all
select p.key, p.slot, p.t0, p.key || '-' || (5 * k + 1), 5 * k, 30, false
from moved_projects p cross join moved_counts c cross join lateral generate_series(1, c.ran) k;

-- The runs, one row each, with every field the daemon states: generations of eight steps, a retro
-- and a merge; the fourth step of the first two generations failed and returned to step 2.
create temp table moved_runs as
select md5('step/' || r.issue || '/' || k)::uuid::text as id, r.project, r.issue,
       (k - 1) / 10 + 1 as generation,
       case when j < 8 then 'step' when j = 8 then 'retro' else 'merge' end as kind,
       case when j < 8 then j + 1 else 0 end as place,
       (array['planner', 'implementer', 'tester', 'reviewer', 'implementer', 'tester', 'reviewer', 'implementer', 'implementer', 'merger'])[j + 1] as role,
       case when j < 8 then 'code' end as type,
       'session-' || substr(md5('session/' || r.issue || '/' || k), 1, 16) as session,
       r.t0 + r.n * interval '2 minutes' + k * interval '15 minutes' as started_at,
       r.t0 + r.n * interval '2 minutes' + k * interval '15 minutes' + interval '12 minutes' as ended_at,
       case when j = 3 and (k - 1) / 10 + 1 < 3 then 'failed' else 'passed' end as result,
       case when j = 3 and (k - 1) / 10 + 1 < 3 then 2 end as returned_to,
       md5('handoff/' || r.issue || '/' || k)::uuid::text as report,
       case when j = 8 then 1 + (r.n + k) % 5 end as rating
from moved_ran r
cross join lateral generate_series(1, r.runs) k
cross join lateral (select (k - 1) % 10 as j) place;

-- Each run's writes, in the order StepWrite.attributes states them: the start write ends with the
-- part_of to the issue, the end write with the reports to the handoff, and a retro's rating is a
-- third write.
create temp table moved_run_tuples as
select run.id, stated.at, stated.place, stated.kind, stated.to_kind, stated.to_id
from moved_runs run
cross join lateral (values
  (run.started_at, 1, 'issue', 'value', run.issue),
  (run.started_at, 2, 'project', 'value', run.project),
  (run.started_at, 3, 'generation', 'value', run.generation::text),
  (run.started_at, 4, 'task', 'value', 'default'),
  (run.started_at, 5, 'kind', 'value', run.kind),
  (run.started_at, 6, 'step', 'value', run.place::text),
  (run.started_at, 7, 'role', 'value', run.role),
  (run.started_at, 8, 'type', 'value', run.type),
  (run.started_at, 9, 'agent', 'value', 'claude-code'),
  (run.started_at, 10, 'session', 'value', run.session),
  (run.started_at, 11, 'model', 'value', 'claude-opus'),
  (run.started_at, 12, 'started_at', 'value', to_char(run.started_at at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')),
  (run.started_at, 13, 'part_of', 'issue', run.issue),
  (run.ended_at, 14, 'result', 'value', run.result),
  (run.ended_at, 15, 'ended_at', 'value', to_char(run.ended_at at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')),
  (run.ended_at, 16, 'returned_to', 'value', run.returned_to::text),
  (run.ended_at, 17, 'report', 'value', run.report),
  (run.ended_at, 18, 'reports', 'handoff', run.report),
  (run.ended_at + interval '1 minute', 19, 'rating', 'value', run.rating::text),
  (run.ended_at + interval '1 minute', 20, 'rating_reason', 'value',
     case when run.rating is not null then 'The plan named the steps the work took, in the order it took them.' end)
) as stated(at, place, kind, to_kind, to_id)
where stated.to_id is not null;

insert into workflow_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
select 'step', run.id, 'step', 'project', 'live',
       jsonb_strip_nulls(jsonb_build_object(
         'issue', run.issue, 'project', run.project, 'generation', run.generation, 'task', 'default',
         'kind', run.kind, 'step', run.place, 'role', run.role, 'type', run.type, 'agent', 'claude-code',
         'session', run.session, 'model', 'claude-opus',
         'started_at', to_char(run.started_at at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
         'result', run.result,
         'ended_at', to_char(run.ended_at at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
         'returned_to', run.returned_to, 'report', run.report, 'rating', run.rating,
         'rating_reason', case when run.rating is not null then 'The plan named the steps the work took, in the order it took them.' end)),
       run.started_at, jsonb_build_object('kind', 'session', 'id', run.session), 0
from moved_runs run
order by run.started_at, run.id;

-- The plan each issue ran from: an owner's document, its newest generation, written once before its
-- first run (PlanWrite.attributes, then the part_of).
create temp table moved_plans as
select md5('plan/' || r.issue)::uuid::text as id, r.project, r.issue,
       md5('artifact/' || r.issue)::uuid::text as artifact_id,
       r.t0 + r.n * interval '2 minutes' + interval '6 minutes' as made_at,
       '[{"id":"default","steps":["planner","implementer","tester","reviewer","implementer","tester","reviewer","implementer"]}]' as tasks
from moved_ran r;

insert into workflow_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
select 'plan', plan.id, 'plan', 'project', 'live',
       jsonb_build_object('issue', plan.issue, 'project', plan.project, 'generation', 3, 'source', 'owner',
                          'artifact_id', plan.artifact_id, 'version', 2, 'tasks', plan.tasks::jsonb),
       plan.made_at, '{"kind": "session", "id": "session-legion-daemon"}'::jsonb, 0
from moved_plans plan
order by plan.made_at;

-- Every tuple of the runs and the plans goes in by its moment, so the runs of the issues interleave
-- in the log as the daemon writes them.
insert into workflow_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted)
select written.from_kind, written.id, written.kind, written.to_kind, written.to_id, written.at, written.who, 0, true
from (
  select 'step' as from_kind, t.id, t.kind, t.to_kind, t.to_id, t.at, t.place,
         jsonb_build_object('kind', 'session', 'id', run.session) as who
  from moved_run_tuples t join moved_runs run on run.id = t.id
  union all
  select 'plan', plan.id, stated.kind, stated.to_kind, stated.to_id, plan.made_at, stated.place,
         '{"kind": "session", "id": "session-legion-daemon"}'::jsonb
  from moved_plans plan
  cross join lateral (values
    (1, 'issue', 'value', plan.issue),
    (2, 'project', 'value', plan.project),
    (3, 'generation', 'value', '3'),
    (4, 'source', 'value', 'owner'),
    (5, 'artifact_id', 'value', plan.artifact_id),
    (6, 'version', 'value', '2'),
    (7, 'tasks', 'value', plan.tasks),
    (8, 'part_of', 'issue', plan.issue)
  ) as stated(place, kind, to_kind, to_id)
) written
order by written.at, written.id, written.place;

-- The other part_of children of each issue Legion ran, in the graph store: a node and its part_of
-- tuple into the issue, each with an event of its own.
create temp table moved_children as
select child.kind, md5(child.kind || '/' || r.issue || '/' || c)::uuid::text as id, r.project, r.issue,
       r.t0 + r.n * interval '2 minutes' + c * interval '4 minutes' as at,
       4000000 + r.slot * 2000000 + r.n * 400 + c as source_event
from moved_ran r
cross join lateral generate_series(1, case when r.busy then 339 else 100 end) c
cross join lateral (
  select case
    when r.busy then case when c <= 34 then 'comment' when c <= 38 then 'ask' when c <= 40 then 'message'
                          when c <= 60 then 'artifact' else 'block' end
    else case when c <= 50 then 'comment' when c <= 85 then 'block' when c <= 95 then 'ask' else 'message' end
  end as kind
) child;

insert into graph_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event, project_key)
select kind, id, case kind when 'block' then 'paragraph' when 'artifact' then 'document' else kind end, 'project', 'live',
       jsonb_build_object('text', 'A ' || kind || ' of ' || issue || ' that the store keeps beside its runs'),
       at, '{"kind": "session", "id": "session-0123456789abcdef"}'::jsonb, source_event, project
from moved_children
order by at, id;

insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted)
select kind, id, 'part_of', 'issue', issue, at, '{"kind": "session", "id": "session-0123456789abcdef"}'::jsonb, source_event, true
from moved_children
order by at, id;

-- The tuples of other kinds: each term of cybermonklabs/legion#3178's 59 takes its share of
-- `others` by its tuples_a_day, each a value tuple of a node of the term's kind, as the driver of
-- c-stores-moved writes them (packages/dispatch/e2e/acceptance/stores-moved.sh, other_tuples).
insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted)
select t.kind, 'other-' || (n / 7), t.kind, 'value', md5(n::text || t.kind), timestamptz '2026-11-01' + n * interval '1 second',
       '{"kind": "session", "id": "moved-read-cost"}'::jsonb, 0, null, true
from (
  select w.kind, round(c.others * w.weight / sum(w.weight) over ())::bigint as share
  from moved_counts c cross join (values
    ('comment', 1.50), ('authored_by', 0.75), ('anchored_to', 0.00), ('owned_by', 0.00), ('author_alias', 0.04),
    ('delivery', 0.14), ('bus_delivery', 0.25), ('fallback_delivery', 0.00), ('block', 2.00), ('block_restored', 0.00),
    ('block_gone', 0.00), ('block_parent_moved', 0.00), ('block_ref', 0.07), ('block_text', 1.00), ('message_delivery', 0.14),
    ('read_mark', 0.39), ('clear', 0.00), ('dismissal', 0.00), ('snooze', 0.04), ('person', 0.14), ('session', 2.50),
    ('role', 0.21), ('page', 0.32), ('message', 1.00), ('message_pointer', 0.32), ('reply', 0.36), ('mention', 0.32),
    ('participation', 0.39), ('person_delivery', 0.36), ('ask_asked_of', 0.00), ('body_block', 4.39), ('body_block_gone', 0.00),
    ('body_block_ref', 0.14), ('delivery_attempt', 4.50), ('issue', 0.86), ('issue_relation', 0.04), ('artifact', 1.39),
    ('document_version', 0.93), ('criterion', 5.00), ('result', 0.00), ('result_screenshot', 0.00), ('result_retraction', 0.00),
    ('component', 0.00), ('component_relation', 0.00), ('ask', 2.29), ('ask_edit', 0.00), ('answer', 0.18), ('ask_resolved', 0.07),
    ('ask_follower', 0.04), ('keep', 0.00), ('decision', 50.00), ('reading', 10.00), ('interaction', 40.00), ('frame', 500.00),
    ('lens', 12.00), ('lens_finding', 40.00), ('lens_kind', 3.14), ('constraint', 5.00), ('risk', 44.00)
  ) as w(kind, weight)
) t
cross join lateral generate_series(1, t.share) n;

drop table moved_counts, moved_projects, moved_ran, moved_runs, moved_run_tuples, moved_plans, moved_children;
