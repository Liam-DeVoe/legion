-- 20261122040952_design_gate_document_issue.up.sql — a design gate row records the issue whose
-- document it is bound to, so a carried root's texts name the old root's document a person
-- approves (INFRA-3686).
--
-- A root a hand-over created with its old root's gate (admit's readmit) holds a copy of each of the
-- old root's gate rows, bound to the old root's documents: a person approves the old root's
-- document, and Dispatch sends that document's events by the old root's key. The texts that name
-- a gate step's document as a dispatch:// link, the architect's catch-up (`gates[].document`) and
-- the gate-moved message (`document`), built that link from the row's own issue, so for a carried
-- row they named the carried root's own document, which no person approves, beside the old root's
-- artifact and version.
--
-- design_gates.document_issue is record.DesignGate.DocumentIssue: the issue whose document
-- artifact_id is. A root's own registration writes the root itself (workflow's gateRegistered),
-- the carry writes the old row's own document_issue, or the old row's issue where that row records
-- none (admit's readmit), and an artifact event of a carried row that records none writes the issue
-- the event is of (workflow's applyArtifact). A write that names none keeps the value the row holds
-- while the row stays on the same document (record.Postgres.PutGate).
--
-- The backfill names the old root of a carried row where the store records it: a root a hand-over
-- created whose handed_over_roots row of its generation says the gate came with it, and whose
-- issues.handed_over_from still names the tree it left, which the promotion that opens its own tree
-- clears. Such a root's row is a carried copy where the old root also holds a row on the same
-- artifact, and the backfill names that old root for it; a row the root registered itself names
-- another artifact and is left alone. Every other row takes null: a root's own registration, whose
-- document is its own, and a carried row whose root has been promoted, or whose old root no longer
-- holds the artifact (a re-admission clears it), for which the store records no old root. A row
-- with null reads as its own issue's document, which is what every row read as before this
-- migration (record.DesignGate.DocumentOf), until an artifact event of the carried document names
-- the old root. A carried row of a chain of hand-overs (an old root whose own row was itself a
-- carried copy) is named for the old root it was copied from, since no older record names the
-- root before that.
--
-- What an older daemon binary reads on this schema. Its design_gates reads and writes name the
-- columns it knows, so it reads every row and ignores the column. Its upsert leaves the column as
-- it stands, and its insert leaves it null. So a carry it makes names no old root, and a
-- registration it makes over a carried row of the same step on another document keeps that row's
-- old root, which this release then names as the document's issue after a roll forward;
-- docs/kubernetes.md ("Rolling back the release that carries a person's words in the daemon's
-- texts") gives the statement that clears it before that roll forward. This migration adds no key
-- to an outbox payload.
--
-- There is no down file: migrations here are forward-only.
alter table design_gates add column document_issue text;

update design_gates carried set document_issue = moved.handed_over_from
from issues moved
join handed_over_roots handed on handed.issue = moved.key and handed.generation = moved.generation and handed.gate_carried
where carried.issue = moved.key and moved.handed_over_from <> ''
  and exists (select 1 from design_gates old where old.issue = moved.handed_over_from and old.artifact_id = carried.artifact_id);
