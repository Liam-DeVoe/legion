---
title: "A false sentence retro may not fix is reported with the price of both routes, because the deferral is observed to leave it standing"
category: legion
tags:
  - retro
  - ready
  - review-rounds
  - stale-prose
  - architecture-pages
date: 2026-11-04
status: active
module: docs/solutions
related_issues:
  - "sjawhar/legion#2636"
  - "INFRA-958"
  - "INFRA-67"
---

# A false sentence retro may not fix is reported with the price of both routes, because the deferral is observed to leave it standing

Extends docs/solutions/legion/a-retro-census-is-run-against-main-and-a-note-only-main-carries-goes-to-the-architect-pr2354.md.

That note covers a learning note only **main** carries, which your branch cannot see. This covers a
sentence your branch **does** carry, in a file retro may not write, and it adds the one number the
architect deciding what to do about it does not otherwise have.

- **Report it, never fix it.** Retro commits nothing and pushes nothing: a push would put a commit
  above the head the task's last worker step passed. At this learning's date, READY compared the
  two heads and refused on any path outside `docs/solutions/`, naming it:
  `packages/daemon/internal/api/ready.go:241-251` filtered the compare to those paths and named
  only them, so the refusal listed the one file you touched and never the learnings beside it.

- **Give both prices, because you are the only one who has read the sentence.** The in-pull-request
  fix costs a full round whatever the size of the edit. The issue moves back to `implementing`,
  every later step of the task runs again, and a second retro follows. The follow-up issue costs
  nothing now, and is observed to leave the sentence standing. Price the defect as well as the fix:
  give the sentence, the code line that falsifies it, and who reads the file — a page under
  `.dispatch/architecture/` is imported into Dispatch and read by agents, not only by whoever edits
  it next.

- **The author of the fix does not matter.** At this learning's date, `unreviewedChange` read paths,
  never authorship (`ready.go:236-251`), so a correction the reviewer itself committed sat above its
  own approval and was refused exactly as yours would be.

## Evidence

In sjawhar/legion#2636 the retro's second reading found `.dispatch/architecture/broker-client.md`
saying `packages/envoy-client/src/broker-credential.ts` "makes one client and one key per process",
where `CLIENTS` is keyed by session (`broker-credential.ts:86`, dropped at `:103-104`) — a page the
same pull request added, carrying the opposite of what the same pull request made true. Six review
rounds passed it. Retro reported it instead of fixing it, and the architect chose the
in-pull-request fix: the reviewer's two prose commits `32a55372` and `c8af1cd7` put the head above
its own approval, READY refused `READY_UNREVIEWED_CHANGE`, and the round that followed was an
implementing round with no code, a tester re-run, a full review from `a5df7090` that found nothing
else, and this second retro — for two sentences.

The deferral's price is the other half, and it is not zero. INFRA-42 filed its own prose residue as
dispatch://INFRA-67 on 2026-10-20, naming the sentence it had declared false; fifteen days later, at
this pull request's head, that sentence is still in the tree verbatim —
`packages/dispatch/web/src/features/issue/useIssueAssignee.ts:22-23`, "moves in or out of the list
at once".

So the architect choosing between the two routes has a round-cost for one and, until now, nothing
for the other. Both routes have a price; name them both and let the architect weigh them.
