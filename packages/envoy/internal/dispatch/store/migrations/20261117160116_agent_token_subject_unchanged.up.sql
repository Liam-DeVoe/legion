-- A personal agent token records, when it is minted, the Google account (`sub`) of the credential
-- that minted it (agent_tokens.subject, migration 20261117064356), and Dispatch holds the token to
-- that account from then on: it acts, and its approves name that account, only while the address's
-- people.sub records the same one (api.personalTokenActor, api.approverStanding, the mint's
-- re-check). A token that records none is held to its creation time (agent_tokens.created_at)
-- instead, against the moment the address's account was recorded (people.sub_recorded_at). That
-- holds only if neither ever changes, so this trigger refuses every update that would change
-- either, null to a value included, whatever code or migration sends it, with SQLSTATE 23514
-- (check_violation). It fires only on an update that names one of the two in its SET list
-- (`before update of`), so a write that names neither (a token's revoke, its last use, its
-- scopes) costs nothing, and one that restates them passes. The function's name is Dispatch's
-- own: the broker's migrations may share this schema (CI runs both sets on one database), and
-- broker migration 0038 has a function of its own for the same refusal
-- (TestTheBrokerAndDispatchSchemasShareOneDatabase).
create function refuse_agent_token_account_change() returns trigger language plpgsql as $$
begin
  raise exception 'the Google account % records is written once, when it is made, and is never changed', tg_table_name
    using errcode = 'check_violation';
end
$$;

create trigger agent_tokens_subject_unchanged before update of subject, created_at on agent_tokens for each row
  when (old.subject is distinct from new.subject or old.created_at is distinct from new.created_at)
  execute function refuse_agent_token_account_change();
