-- 20261121121717_account_binding_replacements.up.sql
-- The replacements of an address's Google account made while the account binding was off, kept
-- until the binding holds and catches them up.
--
-- While a build without the account binding is in the fleet (20261120222130_account_binding_fleet),
-- a sign-in that replaces an address's recorded account revokes and owes nothing, whichever build
-- serves it. Once the binding holds, a credential of the replaced account that records it is
-- refused, and so is one that records no account and was made before the replacement
-- (people.sub_recorded_at); but one that records none and was made after it, which the replaced
-- account could still mint while the binding was off (through either build: an earlier build never
-- records the account a mint is bound to), would act as the new holder's, and the replaced
-- account's broker credentials would never be revoked at the broker. So the database keeps each
-- such replacement here, as it keeps sub_recorded_at, whichever build changes people.sub:
--
--   - account_binding_replacements, one row per replacement: the address (people.email), the
--     account replaced, and when (the replacement's people.sub_recorded_at, which the before
--     trigger people_sub_recorded_at stamps from the database's clock, the time a token's
--     created_at is compared with).
--   - people_account_binding_replacement, an AFTER UPDATE OF sub trigger on people, for a row whose
--     sub moves from one account to another (sub is never taken off: people_sub_recorded_at
--     refuses that, and a first recording replaces nothing). It inserts the row and notifies
--     dispatch_broker_revocations, so the revocation loop (api.RunBrokerRevocations) wakes and
--     watches for the binding to start.
--
-- The binding reads the rows in two ways, both only while it holds (store.AccountBindingHolds):
--
--   - At once, in every check of a personal token (api.tokenAccountReplaced): a token recording no
--     account made at or after a kept replacement of its address is refused, as one made before
--     the address's recording is.
--   - In a catch-up under the address's lock (store.CatchUpAccountBinding): every personal token of
--     the address that records no account and was made at or after its earliest kept replacement
--     is revoked, the new holder's own included (they mint again), a broker revocation is recorded
--     for each replaced account that does not hold the address now, and the address's rows are
--     deleted, so a second catch-up finds nothing. A sign-in of a build with the binding runs it
--     before its own writes, and the revocation loop runs it for every address with a row.
--
-- A sign-in that finds the binding holding revokes and owes its own replacement in its own
-- transaction, and deletes the row this trigger wrote for it there (store.PgPeopleStore.SignInSubject).
--
-- The function pins search_path = pg_catalog, public, pg_temp and names every table with its
-- schema, as the binding's guards do (20261119065002).
--
-- Rolled back to a release without the binding, the table and the trigger stay: that release's
-- replacements are kept here, and nothing of it reads them; a later release with the binding
-- catches them up when its binding holds.
--
-- Locks: CREATE TRIGGER takes people SHARE ROW EXCLUSIVE until the commit, so a sign-in waits for
-- an instant; the table is new.
create table public.account_binding_replacements (
  email text not null,
  replaced_subject text not null,
  replaced_at timestamptz not null,
  primary key (email, replaced_at, replaced_subject)
);

create function public.keep_account_binding_replacement() returns trigger language plpgsql
set search_path = pg_catalog, public, pg_temp as $$
begin
  insert into public.account_binding_replacements (email, replaced_subject, replaced_at)
  values (new.email, old.sub, new.sub_recorded_at)
  on conflict do nothing;
  perform pg_catalog.pg_notify('dispatch_broker_revocations', '');
  return null;
end $$;

create trigger people_account_binding_replacement after update of sub on public.people
  for each row when (old.sub is not null and new.sub is distinct from old.sub)
  execute function public.keep_account_binding_replacement();
