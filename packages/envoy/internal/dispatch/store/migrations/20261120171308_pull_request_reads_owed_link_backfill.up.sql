-- 20261120171308_pull_request_reads_owed_link_backfill.up.sql
--
-- A link by hand owes a read of the pull request it links where Dispatch holds no head row of it
-- (R5 of the results lookup, delivery.OweLinkedPullRequestReads), but only from the link write on.
-- A link written before this release owes nothing, so a pull request that has no event since, a
-- merged one for instance, never gets its head row, and the checks of the issue that links it
-- read no head. This owes the read once for each such link that is there now, as a link write of
-- this release would have owed it, and the reconcile's next passes read them (failures first, at
-- most 200 a pass) and write each head GitHub answers (Reconcile.readOwedPullRequests).
--
-- Each link of the form https://github.com/<owner>/<name>/pull/<number> is read, in any letter
-- case of its scheme, host and `pull`, the owner and the name held to the rule of GitHub's names
-- that the link write holds them to (delivery.linkedPullRequest, checkPullRequestKey; the
-- patterns of refs.GitHubOwnerPattern and refs.GitHubNamePattern, a name of at most 100
-- characters that is not `.` or `..`) and a number of 1 to 9 digits with no zero before it. A
-- link of the /issues/<number> form is left out: `dispatch issue --external` stores that form for
-- a GitHub issue as well, and a read of each would ask GitHub about every issue ever linked.
-- The read is owed under the repository's canonical name (refs.CanonicalRepoSQL, spelled out
-- below), or under the link's own name where the canonical name is no name GitHub can have, as
-- owedName owes it. A link owes nothing where a head row of its pull request is stored, under
-- the canonical name of the row's repository in any letter case (oweLinkedRead's test), and
-- nothing where the read is owed already: such a row is left as it is. Two links of one pull
-- request in two letter cases owe one read, under the spelling first in byte order.
--
-- Each new row is a link's owe, as recordLinkedOwe writes it: a failure whose linked_revision is
-- its revision, a new value of pull_request_reads_owed_revision_seq, so a head row another writer
-- stores before the read settles hides nothing (delivery.owedReadJoin). Its owed_at is the time of
-- this migration's transaction, which is also the applied_at the runner records for it in
-- schema_migrations in the same transaction: the by-hand way back
-- (20261120171308_pull_request_reads_owed_link_backfill.down.sql) deletes the rows whose owed_at
-- is still that time and whose linked_revision is still their revision, which no later owe of
-- this release leaves so.
--
-- The census answers 0: it inserts rows and rewrites and refuses none. The release before names
-- pull_request_reads_owed in none of its statements, so rolling back past this migration is an
-- image rollback alone; the owed rows wait for the next release that reads them.
insert into pull_request_reads_owed (repo, number, reason, revision, linked_revision)
select owed.repo, owed.number, 'failure', owed.revision, owed.revision
from (
	select linked.repo, linked.number, nextval('pull_request_reads_owed_revision_seq') as revision
	from (
		select distinct on (lower(named.repo), named.number) named.repo, named.number
		from (
			select case
					when canonical.name ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,38}/[A-Za-z0-9._-]{1,100}$'
						and split_part(canonical.name, '/', 2) not in ('.', '..')
					then canonical.name
					else link.given
				end as repo,
				link.number
			from (
				select parsed.part[1] || '/' || parsed.part[2] as given, parsed.part[3]::int as number
				from (
					select regexp_match(l.url,
						'^https://github\.com/([A-Za-z0-9][A-Za-z0-9_-]{0,38})/([A-Za-z0-9._-]{1,100})/pull/([1-9][0-9]{0,8})$',
						'i') as part
					from issue_external_links l
				) parsed
				where parsed.part is not null and parsed.part[2] not in ('.', '..')
			) link
			cross join lateral (
				select coalesce((select max(rename.rename_target) from (
						select renamed.repo as rename_source, renamed.renamed_to as rename_target
						from repo_projects renamed where renamed.renamed_to is not null
					) rename where lower(rename.rename_source) = lower(link.given)), link.given) as name
			) canonical
		) named
		where not exists (
			select 1 from pull_request_heads h
			where h.number = named.number
				and lower(coalesce((select max(rename.rename_target) from (
						select renamed.repo as rename_source, renamed.renamed_to as rename_target
						from repo_projects renamed where renamed.renamed_to is not null
					) rename where lower(rename.rename_source) = lower(h.repo)), h.repo)) = lower(named.repo)
		)
		order by lower(named.repo), named.number, named.repo collate "C"
	) linked
) owed
on conflict (repo, number) do nothing;
