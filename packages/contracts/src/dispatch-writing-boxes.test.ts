import { expect, test } from "bun:test";
import { DISPATCH_WRITING_BOXES, DISPATCH_WRITING_HINT_DELIVERED } from "./dispatch-writing-boxes";

// R4: a box whose write tells nobody it names says what a pick shows as - a link, or, in a
// suggestion's replacement, the characters written until someone accepts it - and that it tells
// nobody, and says what does tell. What does is the write's own doing - a reply goes to its
// thread, an ask to the assignee's Inbox, and for an edit only a new comment tells anyone - so
// each such write has words of its own, the same in every box that makes it, and none of them is
// the line of a box that delivers. One sentence shared by every such write says nothing of what
// does tell.
test("each write that tells nobody it names has words of its own, the same in every box", () => {
  const quiet = DISPATCH_WRITING_BOXES.filter((row) => row.covered && !row.delivers);
  const hintsOf = new Map<string, Set<string>>();
  const writesOf = new Map<string, Set<string>>();
  for (const row of quiet) {
    hintsOf.set(row.write, (hintsOf.get(row.write) ?? new Set()).add(row.hint));
    writesOf.set(row.hint, (writesOf.get(row.hint) ?? new Set()).add(row.write));
  }

  expect(
    [...hintsOf].filter(([, hints]) => hints.size > 1),
    "a write whose boxes say different words"
  ).toEqual([]);
  expect(
    [...writesOf].filter(([, writes]) => writes.size > 1),
    "words several writes share"
  ).toEqual([]);
  expect(
    quiet.filter((row) => row.hint === DISPATCH_WRITING_HINT_DELIVERED).map((row) => row.box),
    "a box that tells nobody saying it does"
  ).toEqual([]);
});
