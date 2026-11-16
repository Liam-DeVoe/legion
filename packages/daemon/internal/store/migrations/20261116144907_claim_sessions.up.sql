-- 20261116144907_claim_sessions.up.sql — every session id a claim has recorded, held to the
-- first claim of its project that recorded it.
--
-- A claim's agent reports its own session id when it registers, and the daemon records that id on
-- the claim and enrolls it with the secrets broker, whose enrollment takes over the grants and
-- pending requests of every other enrollment of the same pod account and session
-- (packages/envoy/internal/broker/enroll's adopt). Every pod of a project enrolls under that
-- project's one account, so a role that reported a sibling's session id, or the id of another
-- claim of its project, live or ended, took that session's secret grants (dispatch://INFRA-840,
-- plan E's t-e-54). The claims row keeps only the session a claim resumes now and the one it last
-- abandoned, so nothing recorded which ids the other claims had recorded before: this table does,
-- one row per project and id, written when a claim first records it (supervise's holdSession,
-- through Store.HoldSession), and never deleted, so a retired, failed or suspended claim keeps every
-- id it recorded. A registration or an enrollment of an id another claim of the project holds is
-- refused.
--
-- A hold is a project's, as a daemon is: the daemon serves one project, and a claim of another
-- project is another daemon's. A role of one daemon that names another daemon's session takes
-- nothing of it at the broker, whose grants follow the daemon that enrolled a pod
-- (exchange.PodLaunchPrincipal), so the daemon need not, and cannot, know another daemon's ids.
--
-- No foreign key to claims: a hold outlives anything that becomes of its claim's row.
--
-- Every id the claims table records already is held for the claim that records it: its session,
-- and the session it last abandoned, which it recorded too. No two claims of a project are meant to
-- record one id, since every launch starts its agent on an id of its own
-- (claudelaunch.NewSessionID for Claude Code, Oh My Pi's own otherwise), so an id two claims record
-- is this hole already used, or a bug, and nothing recorded which claim took it first. Such an id is
-- held for neither: its claim is the empty string, which no claim's token is, so every claim is
-- refused its registration and its enrollment of it. The daemon logs each such id at boot at Error,
-- naming every claim whose row records it, and suspends them (internal/daemon's
-- suspendSessionsHeldForNone), and an operator records the rightful holder by hand:
--   update claim_sessions set claim = '<token>' where project = '<project>' and session = '<id>';
create table claim_sessions (
  project text not null,
  session text not null check (session <> ''),
  claim text not null,
  recorded_at timestamptz not null default now(),
  primary key (project, session));

insert into claim_sessions (project, session, claim)
select project, session, case when count(distinct token) = 1 then min(token) else '' end from (
  select project, session, token from claims where session <> ''
  union all
  select project, abandoned_session ->> 'session', token from claims
    where coalesce(abandoned_session ->> 'session', '') <> '') recorded
group by project, session;
