import { expect, test } from "bun:test";

import type { SearchOwner, SearchResult } from "../../api/types";
import {
  firstHighlightTerm,
  groupResults,
  HITS_PER_KIND,
  hitIsItsIssue,
  issueHitTitle,
  kindLabel,
  optionId,
  ownerWords,
  stepActive,
} from "./search-model";

/** An issue owner as the one flat owner record carries it: the route table leaves `kind` a plain
 *  string and makes every arm's own field optional, and an issue owner is the one naming `key`. */
type IssueOwner = SearchOwner & { kind: "issue" };

function result(
  id: string,
  owner: IssueOwner,
  kind: SearchResult["kind"] = "document"
): SearchResult {
  return {
    href: `/issues/${owner.key}`,
    id,
    kind,
    owner,
    rank: 1,
    snippet: "result",
  };
}

test("groups hits by their kind, each kind where its first hit ranks, in the server's order", () => {
  const legionTwo: IssueOwner = {
    key: "LEGION-2",
    kind: "issue",
    status: "done",
    title: "First issue",
  };
  const legionThree: IssueOwner = {
    key: "LEGION-3",
    kind: "issue",
    status: "todo",
    title: "Second issue",
  };
  const results = [
    result("document-2", legionTwo),
    result("comment-3", legionThree, "comment"),
    result("comment-2", legionTwo, "comment"),
    result("signal-1", legionTwo, "signal"),
  ];
  expect(groupResults(results)).toEqual([
    { heading: "Documents", kind: "document", results: [results[0]] },
    { heading: "Comments", kind: "comment", results: [results[1], results[2]] },
    // A kind this build has no word for keeps its own.
    { heading: "signal", kind: "signal", results: [results[3]] },
  ]);
});

test("lists at most six hits of a kind, the best ranked", () => {
  const owner: IssueOwner = { key: "LEGION-2", kind: "issue", status: "todo", title: "Issue" };
  const results = Array.from({ length: 8 }, (_, index) => result(`issue-${index}`, owner, "issue"));
  const [issues] = groupResults(results);
  expect(HITS_PER_KIND).toBe(6);
  expect(issues?.results).toEqual(results.slice(0, 6));
});

test("names an issue owner by its key and title, and a project document by its project and name", () => {
  expect(ownerWords({ key: "LEGION-2", kind: "issue", status: "todo", title: "Orrery" })).toEqual({
    key: "LEGION-2",
    name: "Orrery",
  });
  expect(
    ownerWords({
      artifact_id: "document-2",
      kind: "document",
      name: "Navigation design",
      project: "CORE",
      slug: "navigation-design",
    })
  ).toEqual({ key: "CORE", name: "Navigation design" });
});

test("a hit that is the issue itself shows its title once: the marked snippet where it is the whole title", () => {
  const owner: IssueOwner = {
    key: "DEMO-30",
    kind: "issue",
    status: "in_progress",
    title: "API: rate limit headers",
  };
  const issue = result("DEMO-30", owner, "issue");
  expect(hitIsItsIssue(issue)).toBe(true);
  // Its other hits - a document, a comment, an ask, a message - name the issue they belong to.
  for (const kind of ["document", "comment", "ask", "message"]) {
    expect(hitIsItsIssue(result(`${kind}-1`, owner, kind))).toBe(false);
  }
  // The snippet is the whole title: it shows, with its marks.
  expect(issueHitTitle(issue, "API: rate limit headers")).toBeUndefined();
  // The headline started at the first word that matched: the whole title shows instead.
  expect(issueHitTitle(issue, "rate limit headers")).toBe("API: rate limit headers");
});

test("uses the first positive websearch term for document highlighting", () => {
  expect(firstHighlightTerm('"merge queue" -daemon')).toBe("merge");
  expect(firstHighlightTerm("-daemon OR astrolabe")).toBe("astrolabe");
  expect(firstHighlightTerm("-only")).toBeUndefined();
});

test("wraps active result navigation at either end of the result list", () => {
  expect(stepActive(0, -1, 3)).toBe(2);
  expect(stepActive(2, 1, 3)).toBe(0);
  expect(stepActive(0, 1, 0)).toBe(0);
});

test("labels result kinds and creates stable option IDs", () => {
  const document = result("document-2", {
    key: "LEGION-2",
    kind: "issue",
    status: "todo",
    title: "Issue",
  });

  expect(kindLabel("document")).toBe("doc");
  expect(optionId(document)).toBe("search-option-document-document-2");
});
