-- 20261122120917_claim_session_agent.up.sql — the agent whose session a claim holds.
--
-- claims.session_agent is supervise.Claim.SessionAgent: the agent whose session the claim holds,
-- in the identity a step's start names it by (plan.Agent.ID): `key:<key>` for the agent of a key,
-- `step:<place>:<task>` for the agent of a step with no key. One name can go to two agents of an
-- issue in turn — a later version of a plan can give the name a stopped task's agent ran under to
-- a new agent — so the claim records which of them its session is. Each start of a step names its
-- step's agent (record.SuperviseRequest.Agent). The decision that brings the claim back leaves the
-- session behind where the two differ, so the new agent starts in a session of its own, and
-- records the start's agent once the claim is admitted. A start that gives its task to the process
-- a claim still runs records that start's agent where the claim records none. Besides this file,
-- nothing else writes it.
--
-- Every row the table already holds takes the default, the empty string, which records no agent:
-- the next start of a step's agent that reaches such a claim takes the session it holds, as the
-- release before took it, and records that agent, whether the start brings the claim back or
-- gives its task to the process the claim still runs.
--
-- But a claim whose name the issue's plan rows give a key records that key, `key:<key>`: the key
-- of the latest plan_steps row of the issue that holds the name, in the order
-- record.Postgres.PlanStepsOf reads the rows (generation, task, place), where that row names a key.
-- A row of the owner, or one that holds no name, is left out, as record.NameAgents takes no name
-- from it, and NameAgents keeps the name for that key. A claim's name is the last part of its
-- token after the issue's part (claim.Token.Cut). NameAgents keeps a key's name from the rows and
-- from the claims that record a key's session (record.KeySessionsOf), and a write of a plan
-- deletes rows: a version replaces the rows of each task it gives
-- (record.Postgres.PutPlan), a later version removes a task with its rows (DeleteTasks), and the
-- first step of a new owner's run deletes the plans an earlier owner gave (workflow's
-- passOverEarlierOwners). Left recording no agent, the claim would lose the name once the last row
-- of the key was gone, and the next version would give it to a new agent, whose start would resume
-- the key's conversation on that claim. Recorded, the claim keeps the name for the key whatever
-- rows a later write deletes, and a start of another agent that reaches it starts a session of its
-- own. A start the release before queued names no agent, and where its claim is not made yet, it
-- makes the claim when it runs, recording no agent; so each queued supervise start that names no
-- agent and whose claim's name the rows give a key names that key, and the claim it makes or
-- reaches records the key. A name whose last row of the key the release before had already deleted
-- is one no record says was a key's, and its claim records no agent.
--
-- The fill skips a name whose latest row names no key, though an earlier row of it names one: its
-- claim records no agent, and a queued start of it names none. NameAgents gives no step with no key
-- a name a key holds, so such a row is one the release before wrote: its naming checked only the
-- names of the version it named, so it gave the name to an agent with no key after the key, and
-- that agent ran on the claim after the key's agent, and may run on it now. Had the fill recorded
-- the key, that agent's next start of its own step, which names it (`step:<place>:<task>`), would
-- find another agent's session on the claim and start the agent again mid-step in a new session;
-- with the name skipped, that start takes the session the claim holds and records the agent. What
-- the skip leaves: the rows show which agent a version named, not which ran, so where that agent
-- with no key had not started at the upgrade, its first start takes the session the claim holds,
-- the key's, as the release before did. And NameAgents still holds the name for the key while the
-- key's earlier row stands, since a later row of the name with no key does not end a key's hold,
-- so a later version names that agent with no key `<role>-2`, in a session of its own.
--
-- The release before this one names each column it reads and writes (the claim store's
-- claimSelect and upsertClaim), so it neither reads nor writes this one, and every row it inserts
-- takes the default.
--
-- That release does not know the key `agent` of a supervise row, which this release writes on the
-- start of each step. It decodes an outbox payload strictly (record.DecodeOutboxPayload,
-- DisallowUnknownFields), so a start still queued with the key would fail its decode at every
-- attempt, and fail every read of its role's runs (record.Store.RoleRuns decodes the queued
-- supervise rows of each run it reads). The update takes the key off each such row and leaves
-- every other key, and that release then starts the claim as it did, resuming the session it
-- holds. The update is owed to that release, not to the roll-forward.
--
-- The fill writes the claims and queued starts of every project the database holds: a migration is
-- one static file that each database runs once (store.Store.apply), and nothing in it names the
-- project of the daemon that runs it, so it cannot keep to one project, and no later run would fill
-- another project's rows. So until each other project's pod restarts on this release, its daemon of
-- the release before fails that decode on each start of its project the fill gave the key `agent`,
-- and on each read of that start's role's runs, and the start is tried again on its backoff, so
-- nothing is lost.
--
-- There is no down file. This directory keeps none — migrations here are forward-only and no
-- .down.sql file exists — so the way back is the one transaction that closes this header, its last
-- paragraph (migrations.WayBack), run with this release's daemon stopped and before the release
-- before starts, through psql '<postgres_dsn>' -X -v ON_ERROR_STOP=1. The drop and the delete of
-- this migration's row go together: the delete is what makes the version the store reports that
-- release's own again, and both are owed to the roll-forward, which then runs this file again and
-- adds the column at its default, and records on each claim and queued start the key the rows then
-- give its name. What the drop costs is the record of each claim's agent: after the roll-forward
-- each other claim records none, and the next start of a step's agent takes the session the claim
-- holds, so a claim whose session was another agent's when the rollback came is resumed for the
-- next agent once, as the release before resumed it. What the update costs is that same record on
-- each start it strips: the release before resumes the claim's session for it.
--
-- begin;
-- update outbox set payload = payload - 'agent' where kind = 'supervise' and payload ? 'agent';
-- alter table claims drop column if exists session_agent;
-- delete from schema_version where version = 20261122120917;
-- commit;
alter table claims add column session_agent text not null default '';
with holders as (
  select issue, agent, agent_key from (
    select distinct on (issue, agent) issue, agent, agent_key
    from plan_steps
    where agent_key <> 'owner' and agent <> ''
    order by issue, agent, generation desc, task desc, position desc
  ) latest
  where agent_key <> ''
), recorded as (
  update claims c set session_agent = 'key:' || h.agent_key
  from holders h
  where c.issue = h.issue and substring(c.token from '^legion-[^-]+-[^-]+-[^-]+-(.+)$') = h.agent
)
update outbox o set payload = o.payload || jsonb_build_object('agent', 'key:' || h.agent_key)
from holders h
where o.kind = 'supervise' and o.payload->>'op' = 'start' and not (o.payload ? 'agent')
  and o.issue = h.issue
  and substring(o.payload->>'claim' from '^legion-[^-]+-[^-]+-[^-]+-(.+)$') = h.agent;
