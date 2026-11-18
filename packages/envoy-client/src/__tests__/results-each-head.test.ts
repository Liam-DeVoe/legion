import { describe, expect, test } from "bun:test";
import type { IssueCheckResults } from "@legion/contracts";
import { formatCheckResults } from "../dispatch-commands/check-results";

// results-each-head.test.ts holds `dispatch results` to R9 to R12 and R18 of the spec of several
// open pull requests: one line for each open head under a `before_merge` check, with its state, its
// roles and the ids of the results that decide it, and the repository of a pull request of another
// repository than the issue's beside its number; one line that counts the merged pull requests; the
// pass and the fail at one head, and what ends that state; and the newest result that decides a
// check compared with a branch. Each answer is in the wire shape of the server, typed as unknown,
// so the file builds against a client that predates these fields and fails there on its own
// assertion.

/** The lines `dispatch results` prints for answer. */
function printed(answer: unknown): string[] {
  return formatCheckResults(answer as IssueCheckResults).split("\n");
}

/** The lines of the check whose head line starts `- <id> (`, up to the next check. */
function checkBlock(lines: readonly string[], id: string): string[] {
  const start = lines.findIndex((line) => line.startsWith(`- ${id} (`));
  if (start < 0) throw new Error(`no head line of ${id} in:\n${lines.join("\n")}`);
  const end = lines.findIndex((line, index) => index > start && line === "");
  return lines.slice(start, end < 0 ? undefined : end);
}

/** A result of a check, with the fields the command prints. */
function result(
  id: string,
  role: string,
  verdict: "pass" | "fail",
  commit: string,
  commitStands: Record<string, unknown>,
  createdAt: string
): Record<string, unknown> {
  return {
    id,
    issue_key: "ACME-1",
    check_issue_key: "ACME-1",
    check_artifact_id: "artifact-1",
    check_block_id: "k",
    check_version: 4,
    check_sha256: "digest",
    check_words_moved: false,
    role,
    verdict,
    commit_sha: commit,
    commit_stands: commitStands,
    surface: "",
    run: "",
    observed: "ok",
    negative_control: "",
    failed: 0,
    skipped: 0,
    excluded: 0,
    screenshots: [],
    session: `session-${role}`,
    actor: { kind: "session", id: `session-${role}` },
    created_at: createdAt,
  };
}

/** One check of ACME-1, with rest over the defaults. */
function check(id: string, when: string, rest: Record<string, unknown>): Record<string, unknown> {
  return {
    block_id: id,
    ref: `dispatch://ACME-1/artifact/verification-md#b-${id}`,
    issue_key: "ACME-1",
    artifact_id: "artifact-1",
    text: `The words of ${id}.`,
    when,
    surface: "",
    run: "",
    negative_control: "A build that does not build.",
    version: 4,
    sha256: "digest",
    results: [],
    compared_with: [],
    ...rest,
  };
}

/** The answer of ACME-1 with checks. */
function answer(...checks: Record<string, unknown>[]): unknown {
  return { issue: "ACME-1", verification_artifact_id: "artifact-1", checks };
}

/** The basis of an open pull request's head. */
const openHead = (number: number, commit: string) => ({
  kind: "pull_request",
  pull_request: number,
  commit,
});

const hex = (digit: string) => digit.repeat(40);

describe("dispatch results: the state at each head", () => {
  // Three open pull requests, the implementer's pass at one head, the tester's fail at another,
  // and both roles' passes at the third.
  test("prints one line for each open head", () => {
    const [h1, h2, h3] = [hex("1"), hex("2"), hex("3")];
    const ids = {
      implementerAtH1: "aaaaaaaa-0000-4000-8000-000000000001",
      testerFailAtH2: "aaaaaaaa-0000-4000-8000-000000000002",
      implementerAtH3: "aaaaaaaa-0000-4000-8000-000000000003",
      testerAtH3: "aaaaaaaa-0000-4000-8000-000000000004",
    };
    const at = (commit: string) => ({ answer: "at_basis", basis: openHead(0, commit) });
    const lines = printed(
      answer(
        check("c-fix-at-cause", "before_merge", {
          compared_with: [openHead(4253, h1), openHead(4260, h2), openHead(4295, h3)],
          results: [
            result(ids.testerAtH3, "tester", "pass", h3, at(h3), "2026-11-17T12:04:00Z"),
            result(ids.implementerAtH3, "implementer", "pass", h3, at(h3), "2026-11-17T12:03:00Z"),
            result(ids.testerFailAtH2, "tester", "fail", h2, at(h2), "2026-11-17T12:02:00Z"),
            result(ids.implementerAtH1, "implementer", "pass", h1, at(h1), "2026-11-17T12:01:00Z"),
          ],
          pass_stands: {
            answer: "no",
            subjects: [
              {
                basis: openHead(4253, h1),
                answer: "yes",
                roles: ["implementer"],
                results: [ids.implementerAtH1],
              },
              {
                basis: openHead(4260, h2),
                answer: "no",
                reason: "fail",
                results: [ids.testerFailAtH2],
              },
              {
                basis: openHead(4295, h3),
                answer: "yes",
                roles: ["tester", "implementer"],
                results: [ids.testerAtH3, ids.implementerAtH3],
              },
            ],
          },
        })
      )
    );
    expect(checkBlock(lines, "c-fix-at-cause").slice(0, 4)).toEqual([
      "- c-fix-at-cause (before_merge). Compared with the heads of 3 open pull requests. A pass stands: no, as 2 of 3 heads read yes.",
      `  - #4253 at ${h1}: yes, from implementer. Pass ${ids.implementerAtH1} decides it.`,
      `  - #4260 at ${h2}: no, as fail ${ids.testerFailAtH2} counts there.`,
      `  - #4295 at ${h3}: yes, from tester, implementer. Passes ${ids.testerAtH3}, ${ids.implementerAtH3} decide it.`,
    ]);
  });

  // A check whose head holds a pass and a fail: the line of that head is the spec's Design, byte
  // for byte.
  test("names the pass, the fail and what ends the state", () => {
    const head = "9b3deb0e2587ccecd06a32858627387a20660e22";
    const pass = "287ea9e8-36a9-4426-bfe2-44d5ddee49e5";
    const fail = "192a6f78-9de0-48e6-a990-4bd78f44059f";
    const at = { answer: "at_basis", basis: openHead(3181, head) };
    const lines = printed(
      answer(
        check("c4", "before_merge", {
          compared_with: [openHead(3181, head)],
          results: [
            result(fail, "tester", "fail", head, at, "2026-11-15T00:12:12.107374Z"),
            result(pass, "tester", "pass", head, at, "2026-11-14T23:43:55.993653Z"),
          ],
          pass_stands: {
            answer: "unknown",
            reason: "pass_and_fail",
            subjects: [
              {
                basis: openHead(3181, head),
                answer: "unknown",
                reason: "pass_and_fail",
                results: [pass, fail],
              },
            ],
          },
        })
      )
    );
    expect(checkBlock(lines, "c4")[1]).toBe(
      "  - #3181 at 9b3deb0e2587ccecd06a32858627387a20660e22: not known, as pass 287ea9e8-36a9-4426-bfe2-44d5ddee49e5 and fail 192a6f78-9de0-48e6-a990-4bd78f44059f count at this commit. A later pass does not end this. A retraction of the wrong one ends it, and so does a change of the check's words. A new head of the pull request gets a state of its own."
    );
  });

  test("counts the merged pull requests in one line", () => {
    const [h1, h2] = [hex("a"), hex("b")];
    const pass = "bbbbbbbb-0000-4000-8000-000000000001";
    const merged = [11, 12, 13, 14, 15, 16].map((number) => ({
      ...openHead(number, hex(`${number - 10}`)),
      merged: true,
    }));
    const lines = printed(
      answer(
        check("k1", "before_merge", {
          compared_with: [openHead(21, h1), openHead(22, h2), ...merged],
          results: [
            result(
              pass,
              "tester",
              "pass",
              h1,
              { answer: "at_basis", basis: openHead(21, h1) },
              "2026-11-17T12:00:00Z"
            ),
          ],
          pass_stands: {
            answer: "no",
            subjects: [
              { basis: openHead(21, h1), answer: "yes", roles: ["tester"], results: [pass] },
              { basis: openHead(22, h2), answer: "no", reason: "no_result" },
            ],
          },
        })
      )
    );
    const block = checkBlock(lines, "k1");
    expect(block.slice(0, 4)).toEqual([
      "- k1 (before_merge). Compared with the heads of 2 open pull requests. A pass stands: no, as 1 of 2 heads read yes.",
      `  - #21 at ${h1}: yes, from tester. Pass ${pass} decides it.`,
      `  - #22 at ${h2}: no, as it holds no result.`,
      "  The 6 merged pull requests decide nothing while one is open.",
    ]);
    for (const number of [11, 12, 13, 14, 15, 16]) {
      expect(block.filter((line) => line.includes(`#${number}`))).toEqual([]);
    }
  });

  // An after_merge check under the newest-result rule: its head line is the spec's Design, byte
  // for byte.
  test("names the newest result of a check compared with a branch", () => {
    const commit = "1d0adea3bdada61b2c393bfd3bff5888093cbe8d";
    const newest = "f1c12f1a-0383-4788-9a5e-5772dc06126d";
    const onMain = { answer: "at_basis", basis: { kind: "branch", branch: "main" } };
    const lines = printed(
      answer(
        check("c9", "after_merge", {
          compared_with: [{ kind: "branch", branch: "main" }],
          results: [
            result(newest, "tester", "fail", commit, onMain, "2026-11-17T16:34:28.365626Z"),
            result(
              "cf3c4306-3776-455e-bc82-c5990dd954bd",
              "tester",
              "fail",
              commit,
              onMain,
              "2026-11-16T22:34:08.441352Z"
            ),
            result(
              "dddddddd-0000-4000-8000-000000000001",
              "tester",
              "pass",
              commit,
              onMain,
              "2026-11-16T20:00:00Z"
            ),
          ],
          pass_stands: { answer: "no", results: [newest] },
        })
      )
    );
    expect(checkBlock(lines, "c9")[0]).toBe(
      "- c9 (after_merge). Compared with branch main. A pass stands: no, as the newest result is fail f1c12f1a-0383-4788-9a5e-5772dc06126d at 1d0adea3bdada61b2c393bfd3bff5888093cbe8d."
    );
  });

  test("says not known for a result that Dispatch could not place", () => {
    const commit = "1d0adea3bdada61b2c393bfd3bff5888093cbe8d";
    const newest = "f1c12f1a-0383-4788-9a5e-5772dc06126d";
    const lines = printed(
      answer(
        check("c9", "after_merge", {
          compared_with: [{ kind: "branch", branch: "main" }],
          results: [
            result(
              newest,
              "tester",
              "fail",
              commit,
              { answer: "unknown", reason: "read_failed" },
              "2026-11-17T16:34:28.365626Z"
            ),
          ],
          pass_stands: { answer: "unknown", results: [newest] },
        })
      )
    );
    expect(checkBlock(lines, "c9")[0]).toBe(
      "- c9 (after_merge). Compared with branch main. A pass stands: not known, as Dispatch could not place fail f1c12f1a-0383-4788-9a5e-5772dc06126d at 1d0adea3bdada61b2c393bfd3bff5888093cbe8d on a branch."
    );
  });

  // A check CI proves, read at two open heads: CI's run passed at #702 and is absent at #703, the
  // open pull request of the highest number, whose read is the check's own `ci`. The head line says
  // that read derives no result, and no CI job's result: the result derived at #702 is no result of
  // the job, and #702's own line says it decides that head.
  test("names the head CI's run passed at, under a check whose newest head has no run", () => {
    const [h702, h703] = [hex("2"), hex("3")];
    const ciAt = (head: string, number: number) => ({
      answer: "at_basis",
      basis: openHead(number, head),
    });
    const absent = {
      run: "docs",
      state: "no_result",
      case: "absent",
      detail: `No CI run named docs is on ${h703}, the head of example/widgets#703.`,
      line: "- Check c1 (before_merge): CI run docs has no result (absent)",
    };
    const passed = {
      run: "docs",
      state: "pass",
      case: "success",
      detail: `CI run docs concluded success at ${h702}, the head of example/widgets#702.`,
      line: `- Check c1 (before_merge): pass at ${h702}, from CI run docs. The words of c1.`,
    };
    const lines = printed(
      answer(
        check("c1", "before_merge", {
          ci_check: "docs",
          ci: absent,
          compared_with: [openHead(703, h703), openHead(702, h702)],
          results: [
            result("ci-run:9001", "ci", "pass", h702, ciAt(h702, 702), "2026-11-18T04:53:16Z"),
          ],
          pass_stands: {
            answer: "no",
            subjects: [
              { basis: openHead(703, h703), answer: "no", reason: "no_result", ci: absent },
              {
                basis: openHead(702, h702),
                answer: "yes",
                roles: ["ci"],
                results: ["ci-run:9001"],
                ci: passed,
              },
            ],
          },
        })
      )
    );
    expect(checkBlock(lines, "c1").slice(0, 6)).toEqual([
      `- c1 (before_merge): no result: No CI run named docs is on ${h703}, the head of example/widgets#703. The words of c1.`,
      "  A pass stands: no, as 1 of 2 heads read yes.",
      `  - #703 at ${h703}: no, as it holds no result.`,
      "    Pull request line: - Check c1 (before_merge): CI run docs has no result (absent)",
      `  - #702 at ${h702}: yes, from ci. Pass ci-run:9001 decides it.`,
      `    Pull request line: - Check c1 (before_merge): pass at ${h702}, from CI run docs. The words of c1.`,
    ]);
  });

  // R18: ACME-1's repository is acme/app. #70 is its own open pull request, and #9 is an open pull
  // request of acme/infra that ACME-1 links, whose basis names its repository. Each head has a line
  // of its own; the line of #9 names acme/infra#9, and the line of #70 names #70.
  test("names the repository of a pull request in another repository", () => {
    const [h70, h9] = [hex("7"), hex("9")];
    const pass = "eeeeeeee-0000-4000-8000-000000000001";
    const infraHead = { ...openHead(9, h9), repo: "acme/infra" };
    const lines = printed(
      answer(
        check("k1", "before_merge", {
          compared_with: [openHead(70, h70), infraHead],
          results: [
            result(
              pass,
              "tester",
              "pass",
              h9,
              { answer: "at_basis", basis: infraHead },
              "2026-11-18T00:00:00Z"
            ),
          ],
          pass_stands: {
            answer: "no",
            subjects: [
              { basis: openHead(70, h70), answer: "no", reason: "no_result" },
              { basis: infraHead, answer: "yes", roles: ["tester"], results: [pass] },
            ],
          },
        })
      )
    );
    expect(checkBlock(lines, "k1").slice(0, 3)).toEqual([
      "- k1 (before_merge). Compared with the heads of 2 open pull requests. A pass stands: no, as 1 of 2 heads read yes.",
      `  - #70 at ${h70}: no, as it holds no result.`,
      `  - acme/infra#9 at ${h9}: yes, from tester. Pass ${pass} decides it.`,
    ]);
  });
});
