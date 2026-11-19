-- 20261119114140_provider_errors_told.up.sql — the notice the project's controller is owed of a
-- claim's run of provider-error turns, and the mark that it was told.
--
-- After six turns of one claim in a row end in a provider error, the project's controller is told
-- once, with the issue, the role and the error. The daemon used to tell it at exactly the sixth
-- turn, and that notice queued nothing while no controller was registered, which is the default
-- (`controller: operator`), and nothing when its write failed: the run went on or ended, and
-- nothing told the controller later. A run that reached six is owed its notice whatever comes
-- after it, so the owed notice is a part of the claim's own row:
--
--   provider_error_run_id        the number of the run of provider-error turns the claim stands
--                                in, or stood in last. It moves to a new number as each run begins,
--                                and it is 0 before the claim's first run;
--   provider_errors_owed_run     the number of the newest run that reached six turns, and 0 when
--                                no run has;
--   provider_errors_owed_reason  what that run's notice tells the controller: the run, the model
--                                and the HTTP status. The claim's machine writes it and the owed
--                                run at each turn of the run from the sixth on (supervise's
--                                Claim.ProviderErrorsOwed);
--   provider_errors_told_run     the number of the run whose notice admission wrote last, in the
--                                transaction that wrote the notice (admit's tellProviderErrors).
--
-- A claim owes a notice while provider_errors_owed_run stands above provider_errors_told_run. No
-- turn, reset or restart of the claim changes that: only the write of the notice moves the told
-- mark, and the machine's claim write names no told mark. Admission writes every owed notice while
-- a controller is registered: at the claim's own offer, at the controller's registration, and at
-- the admission pass of every fact, tick and boot (admit's TellOwedProviderErrors). That pass reads
-- the owed rows of its project, which are almost always none, and the partial index serves it.
--
-- The machine moves the run's number as the run's first turn ends in a provider error (supervise's
-- providerErrored). An earlier release names none of these columns, so its claim write leaves each
-- of them as it stands, and a run that release begins keeps the number of the run before it: after
-- a rollback and a roll forward, that run would read as a run already told, and its notice would
-- never be written. claims_provider_error_run_begun moves the number for such a write, whichever
-- release makes it: a write that begins a run and leaves the number as the row had it. A write
-- begins a run when its run climbs from 0, or stands below the row's, which a write whose reset the
-- store did not take leaves. This release's writes move the number themselves, so the trigger does
-- nothing to them.
--
-- Every claim that is in a run at this migration gets the number 1, so the run it stands in is
-- numbered above the told mark's 0. A claim whose run already stands at six or more is owed run 1's
-- notice at once: the earlier release told such a run at exactly its sixth turn, and only while a
-- controller was registered, which this database does not record, and the run's next turn can be
-- a clean one, after which no turn of the run is left to owe it. The reason names the run and the
-- ladder's bound but no model and no HTTP status, which the earlier release kept nowhere. So such
-- a run can be told once more after the upgrade, but no run goes untold.
--
-- Rolling the image back needs no statement. The earlier release names none of the four columns and
-- reads none of them. The trigger changes only provider_error_run_id, which that release does not
-- read, and it refuses no write.
alter table claims
  add column provider_error_run_id bigint not null default 0,
  add column provider_errors_owed_run bigint not null default 0,
  add column provider_errors_owed_reason text not null default '',
  add column provider_errors_told_run bigint not null default 0;

update claims set provider_error_run_id = 1 where provider_error_run > 0;

update claims set provider_errors_owed_run = 1,
  provider_errors_owed_reason = provider_error_run || ' turns in a row ended in a provider error; the daemon keeps the task and sends it again every 10m0s at most'
  where provider_error_run >= 6;

create index claims_provider_errors_owed on claims (project)
  where provider_errors_owed_run > provider_errors_told_run;

create function claims_provider_error_run_begun() returns trigger language plpgsql as $$
begin
  new.provider_error_run_id := old.provider_error_run_id + 1;
  return new;
end $$;

create trigger claims_provider_error_run_begun before update on claims
  for each row
  when (new.provider_error_run > 0
    and (old.provider_error_run = 0 or new.provider_error_run < old.provider_error_run)
    and new.provider_error_run_id = old.provider_error_run_id)
  execute function claims_provider_error_run_begun();
