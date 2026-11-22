-- packages/envoy/internal/cipher/store/migrations/0046_person_standing_member_kind.up.sql
-- The member kind Dispatch's person answer named (member_kind), kept with that answer under the
-- same answered_at rule, so while Dispatch cannot be asked the broker serves a person whose last
-- kept kind is full and refuses everyone else (projectaccess.Client.MemberKind), at a restart and
-- at a second broker process as at the first. Null for an answer that named no kind, and for every
-- row kept before this column, which the person's next answer from Dispatch fills. A nullable
-- column added with no default writes the catalog alone: no existing row is read or changed.
alter table person_standing add column member_kind text;
