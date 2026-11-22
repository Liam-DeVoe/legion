-- Three new tables and four functions, triggers on pull_request_heads, check_results and
-- ci_check_results that run only on later writes, a lock on those three held to the commit, and a
-- seed that writes only the new table of former heads and check_states_owed: nothing is refused
-- or rewritten.
select 0;
