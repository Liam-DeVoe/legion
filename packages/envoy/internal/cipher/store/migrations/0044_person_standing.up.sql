-- packages/envoy/internal/cipher/store/migrations/0044_person_standing.up.sql
-- The last answer Dispatch gave the broker for each person, whether Dispatch holds
-- them, so an outage of Dispatch releases no held person and a restart or a second process
-- answers alike. Each answer is written unless the row holds a later one, by Dispatch's own clock
-- (the answer's as_of). person is a login as record.CanonicalLogin spells it.
--
-- This file and its follower were numbered 0043 and 0044 until they were merged onto a base whose
-- 0040 is 0040_enrollment_projects, which moved every file after 0039 up by one:
-- 0040_person_subjects to 0042_guards_search_path became 0041 to 0043, and these two 0044 and
-- 0045. A broker database an earlier head of this branch migrated recorded all five under the
-- earlier numbers, and the migrator refuses a version recorded under another file (pgmigrate's
-- identity check), so such a database refuses this head at its next boot, naming version 40, the
-- first of them, which it recorded as 0040_person_subjects. Reset it with its Dispatch database,
-- as 0041_person_subjects says.
--
-- This header was edited in place after the migration identity check arrived, and the line below
-- names the text before the edit, so a database that applied it still boots.
-- replaces: 0044_person_standing.up.sql sha256:6429d66d2bf8eda9301e30a0aa2d12887d0965cd77209a07dc5a1be136495f7f
create table person_standing (
  person text primary key check (person <> '' and person = btrim(person) and person !~ '[A-Z]'),
  held boolean not null,
  answered_at timestamptz not null
);
