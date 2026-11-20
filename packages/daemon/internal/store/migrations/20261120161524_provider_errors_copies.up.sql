-- 20261120161524_provider_errors_copies.up.sql — the copies of the provider-errors notices the
-- Envoy listener stored, so a dead letter of one owes its runs again.
--
-- A provider-errors notice tells the project's controller of a claim's runs of six provider-error
-- turns, and the write that queued it marked those runs told (20261119114140_provider_errors_told).
-- The listener answers its publish 200 once its stream stored the forward to the controller
-- session it read live, which is not that session's word that it read it: a controller whose
-- process died inside the listener's live window is forwarded the notice all the same. The
-- listener copies such a forward to the role's kept lane when another session takes the role, or
-- when its role claim reaper frees the dead session's claim, and hands the copy to the role's next
-- holder; a copy no live holder takes within its hour is dropped, and the listener sends the
-- copy's sender a dead letter naming the copy's event id (packages/envoy/cmd/listener's
-- keptRoleDeadLetter). The daemon publishes the notice as a session of its own and reads that
-- session's agent subject (the daemon's providerErrorsNotice, intake's own durable), so each dead
-- letter of a copy reaches it, with why in its dead_letter field.
--
--   provider_errors_copies   one row for each copy the listener answered a provider-errors
--                            notice's publish with: the event id the listener gave the stored
--                            envelope, which a dead letter names as its source_event_id; the claim
--                            the notice is of; the runs it tells, with their words, as
--                            [{"run": <n>, "reason": <words>}, ...] (record.ToldRuns); the order
--                            the copies were recorded in; and the dead_letter field of the dead
--                            letter the daemon applied for it, null while it has applied none.
--
-- A dead letter is applied once for each copy (record.Store.ProviderErrorsDeadLetter): one whose
-- reason says the copy's forward may have reached the session that held the role then
-- (expired_unconfirmed, delivery_mode_unconfirmed) counts as told; any other owes each run of the
-- copy again, as a row of provider_errors_owed, unless a copy recorded after it tells that run,
-- and admission writes those runs' notice again at its next pass while a controller is registered.
-- A copy whose claim's row goes goes with it.
--
-- Rolling the image back needs no statement: the earlier release names neither this table nor the
-- notice's told field, so it records no copy and applies no dead letter, and the rows here are
-- read by nothing until the image rolls forward again.
create table provider_errors_copies (
  event_id text primary key,
  claim_token text not null references claims (token) on delete cascade,
  runs jsonb not null check (jsonb_typeof(runs) = 'array' and jsonb_array_length(runs) > 0),
  recorded bigserial not null unique,
  dead_letter text
);

create index provider_errors_copies_claim on provider_errors_copies (claim_token, recorded);
