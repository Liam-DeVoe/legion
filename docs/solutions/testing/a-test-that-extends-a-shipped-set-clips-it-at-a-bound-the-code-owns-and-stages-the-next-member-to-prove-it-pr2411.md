---
title: "A test that extends a shipped set clips it at a bound the code owns, and stages the set's next member to prove it"
category: testing
tags:
  - fixtures
  - migrations
  - go
  - rollback
  - operator-docs
date: 2026-10-31
last_updated: 2026-11-17
status: active
module: packages/daemon/internal/store
related_issues:
  - "sjawhar/legion#2411"
---

# A test that extends a shipped set clips it at a bound the code owns, and stages the set's next member to prove it

- A test that reads a property of a set the product keeps growing (embedded migrations, registered handlers, a shipped list) breaks at the set's next real member unless it takes that property from the set.
  The property can be the highest member, the highest member a step leaves standing, or a count, and the test breaks whether it adds crafted members or adds none.
- A test that adds crafted members must not take the whole set.
  Name the property its assertions read, and keep only the part that growth cannot reach.
- Read that bound from the code that defines it, never write the value in the test.
  When no such bound exists, craft the whole set instead, as `craftedSet` in `packages/daemon/internal/store/migrations/embed_test.go` does.
- "The next real member will not break this test" is a claim to prove, not to reason about.
  Add a throwaway member to the real set, watch the test fail before the fix and pass after it, then delete the member and confirm none is left.
- A change that adds a migration searches the tests of every package that calls `migrations.All()`, not only the packages it touches, for a version written as a literal that the new migration moves (`highest`, `newest`, `left standing`).
  A failure in a package the change never ran stays unseen until a whole-suite run.
- When a test checks that a step removed some members by reading the highest member left, any later member hides whether the step ran.
  Count the members the step removes by name too, which must be 0, as docs/solutions/envoy/a-migration-added-after-the-numbered-series-is-named-for-its-creation-instant-and-a-guard-compares-the-applied-set-pr2411.md says for a guard: compare the set of applied versions, never `max(version)`.
- A sentence of a guide that counts or ranks the set is the same assertion, and it goes false at the next member on every branch that adds one.
  Name a migration by its file, and make no claim about how many migrations a release holds or how many stand above it.
  Where a guide must give the version a store reports, say it holds only where no later migration has run.

## Evidence

Three tests in `packages/daemon/internal/store/migrate_timestamped_test.go` appended crafted timestamped migrations to `migrations.All()`, and one started from `store.Migrate` over the whole set.
The reviewer approved them, and READY was armed.
Another tree preparing the daemon's first real timestamped migration (`20261031150000_handoff_records` in that tree, which landed as `20261101144900_handoff_records` in sjawhar/legion#2354) found they would fail: its migration would be the highest version, and it would widen the version column first.
The fix (`d910aa0c`) keeps the migrations up to `migrationid.ClosedAt` of the embedded set, and the widen test starts from that numbered set.
With a throwaway `20261101000000` migration embedded, the three tests failed at `475a0929` and passed at `d910aa0c`.

The migration `20261116234034_phase_reviewed_head` failed `TestTheArchitectUpgradeRunsAgainAfterTheWayBackAndARollForward` in `packages/daemon/internal/admit/architect_upgrade_test.go`.
That test added no member: it pinned `leftStandingVersion` to `20261114031356`, "the highest migration neither way back deletes", and the new migration stood above it.
The change touched that package only for a new parameter in other test files, its author ran only those tests by name, and the failure was found by a run of the whole `admit` suite before any pull request existed.
The fix reads `leftStandingVersion` from `migrations.All()` less `wayBackVersions`, and counts the rows of `wayBackVersions` still in `schema_version`.
With the new migration added to `wayBackVersions` in a copy of the test, the test failed with "1 of the rows the blocks delete stand", so the count can fail.
Three sentences of `docs/kubernetes.md` counted or ranked the set, and each was fixed in a commit of its own.
The migration made false the two that stood on main, "Eleven migrations are recorded above this one at contract 146" and "20261114031356, the highest row neither block deletes".
The change wrote the third itself, "This release's one migration", which any other unreleased migration in the same release makes false.
