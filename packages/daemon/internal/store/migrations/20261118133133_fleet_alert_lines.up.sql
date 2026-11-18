-- 20261118133133_fleet_alert_lines.up.sql — which of R6's lines are open, so a daemon that boots
-- again does not write a line the daemon before it wrote for a case that has not ended.
--
-- A line (internal/fleetalert's Writer) is written once per case and claim until that case ends,
-- and the alert policy opens one incident per line. The set of open lines was held in the daemon's
-- memory alone, so every case still holding after a restart wrote its line again and opened an
-- incident per boot. Each row is one open line: the project, the case's word (the line's `case`
-- field), and the claim it names ('' for a case of the whole project). The writer puts the row
-- when it writes the line, deletes it when the caller ends the case, and reads the project's rows
-- at its boot.
--
-- Rolling the image back needs no statement: the release before this one names no such table, so
-- its daemon writes each line again after its boot, as it always has, and a daemon rolled forward
-- again reads the rows back.
--
-- There is no down file: migrations here are forward-only.
create table fleet_alert_lines (
	project text not null,
	case_word text not null,
	claim_token text not null default '',
	opened_at timestamptz not null default now(),
	primary key (project, case_word, claim_token)
);
