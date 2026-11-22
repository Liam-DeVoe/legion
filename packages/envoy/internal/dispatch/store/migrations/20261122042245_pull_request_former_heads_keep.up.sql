-- 20261122042245_pull_request_former_heads_keep.up.sql
-- A pull request head moved, closed, merged, keyed anew or deleted keeps or drops the pull
-- request's former heads (pull_request_former_heads_keep, 20261122042244) in the writer's own
-- transaction. There is no insert trigger: a new row has left no head yet. The update trigger
-- fires only where the key, head_sha or state moved: every writer of the row names each column in
-- its `do update set` on each write, a read-through included. Its own migration, so the transaction
-- holds pull_request_heads' SHARE ROW EXCLUSIVE alone, for the catalog write.
create trigger pull_request_former_heads_keep_update after update on pull_request_heads
  for each row when ((old.repo, old.number, old.head_sha, old.state) is distinct from (new.repo, new.number, new.head_sha, new.state))
  execute function pull_request_former_heads_keep();
create trigger pull_request_former_heads_keep_delete after delete on pull_request_heads
  for each row execute function pull_request_former_heads_keep();
