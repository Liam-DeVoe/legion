-- 20261123114449_pod_resume_one_open_per_claim.up.sql — one open resume per claim on a database
-- that ran the claim of each resume in an earlier shape.
--
-- 20261121183336 names on each resume the claim its wake brought back and holds one open resume
-- per claim (pod_resumes_one_open_per_claim, in place of 20261105183200's one open resume per
-- role). It took that shape before any release carried it, and a database can have run either of
-- its two earlier shapes: one that named each resume's claim and kept the one open resume per
-- role, and one beside a second migration, 20261122074350, which then made the open resume one per
-- claim and which no release carries. A database that ran either has recorded 20261121183336, so it
-- never runs that migration's present shape. On the first the one open resume per role still
-- stands, and it refuses the wake of one claim while another claim of its role holds an open
-- resume: a role or agent message for it, and a pause's owed launch of it, queue nothing, which is
-- the defect 20261121183336 closes. On the second the indexes are right and schema_version holds a
-- version no file names.
--
-- This brings either to 20261121183336's present shape, and changes nothing on a database that ran
-- that shape: it closes every open resume of a claim but the newest, at the time of the newer, as
-- 20261105183200 closed the open resumes of a role, so the index of one open resume per claim refuses
-- no row it finds; then it drops the one open resume per role where it stands, creates the one open
-- resume per claim where it does not, and deletes 20261122074350's version row. What the close costs
-- is the wake of each older open resume of a claim, which a later wake of its claim takes again; the
-- one open resume per role allowed one open resume of each role, so only a claim that took two roles
-- while a resume of the first stood open can have two.
--
-- The release before this one has none of this release's migrations, and the way back of
-- 20261121183336 drops the index of one open resume per claim and puts the one open resume per role
-- back, so this migration's way back undoes nothing of the schema.
--
-- There is no down file. This directory keeps none — migrations here are forward-only and no
-- .down.sql file exists — so the way back is the one transaction that closes this header, its last
-- paragraph (migrations.WayBack), run with every daemon on that database stopped and before the
-- release before starts, through psql '<postgres_dsn>' -X -v ON_ERROR_STOP=1. A rollback past it
-- runs this block before 20261121183336's own. The delete of this migration's row is what makes the
-- version the store reports that release's own again, and is owed to the roll-forward, which then
-- runs this file again, and it changes nothing then.
--
-- begin;
-- delete from schema_version where version = 20261123114449;
-- commit;
update pod_resumes older set closed_at = newer.at
  from pod_resumes newer
  where older.first_turn_at is null and older.closed_at is null and newer.first_turn_at is null and newer.closed_at is null
    and older.project = newer.project and older.issue = newer.issue and older.claim = newer.claim and older.id < newer.id;
drop index if exists pod_resumes_one_open_per_role;
create unique index if not exists pod_resumes_one_open_per_claim on pod_resumes (project, issue, claim)
  where first_turn_at is null and closed_at is null;
delete from schema_version where version = 20261122074350;
