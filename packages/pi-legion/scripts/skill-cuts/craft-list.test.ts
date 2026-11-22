import { describe, expect, test } from "bun:test";
import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import type { Gh, GhResult } from "./copies";
import { main, parseCraftList, readerProperties } from "./craftlist";

// craftlist.ts: the craft list's shape and `craft-lines`, on F12 (testdata/CAPTURE.md "F12"): a
// made-up list of two made-up sets, alpha-set (acme/alpha-set) and beta-set
// (acme/beta-set-plugin), at the commits F10 reads them at, with three properties (C1 and C3
// checked by a reader, C2 by a behaviour test); the same list with lines past the end of their
// files and an example at a path the commit lacks; and GitHub's answer for each file cited, as
// the stand-in gh gives it.

const TESTDATA = path.join(import.meta.dir, "testdata");
const fixture = (name: string) => readFileSync(path.join(TESTDATA, name), "utf8");
const CONTENTS: Readonly<Record<string, string>> = JSON.parse(fixture("F12-contents.json"));
const VALID = fixture("F12-craft-list.json");

const scratch = temporaryDirectory("skill-cuts-craft-list");

/** A stand-in gh that answers each `gh api [-H <header>] <route>` from F12's contents, each route
 *  asked kept in `calls`; a route F12 does not hold is answered as GitHub answers a missing one. */
function f12Gh(calls: string[] = []): Gh {
  return async (args): Promise<GhResult> => {
    const route = args.filter((arg, at) => at > 0 && arg !== "-H" && args[at - 1] !== "-H").at(-1);
    calls.push(route ?? "");
    const answer = route === undefined ? undefined : CONTENTS[route];
    if (answer === undefined) return { code: 1, stdout: "", stderr: "gh: Not Found (HTTP 404)\n" };
    return { code: 0, stdout: answer, stderr: "" };
  };
}

/** The valid list, changed by `change`, as text. */
function changed(change: (list: { properties: Record<string, unknown>[] }) => void): string {
  const list = JSON.parse(VALID);
  change(list);
  return JSON.stringify(list);
}

/** Why parseCraftList refuses `text`, or undefined when it parses. */
function refusal(text: string): string | undefined {
  try {
    parseCraftList(text);
    return undefined;
  } catch (error) {
    return error instanceof Error ? error.message : String(error);
  }
}

/** craft-lines' exit status and what it wrote, on the list in `file`. */
async function craftLines(file: string, gh: Gh = f12Gh()) {
  const out: string[] = [];
  const err: string[] = [];
  const code = await main(["--list", file], {
    out: (text) => out.push(text),
    err: (line) => err.push(line),
    gh,
    sleep: async () => {},
  });
  return { code, out: out.join(""), err: err.join("\n") };
}

describe("the craft list's shape", () => {
  test("the valid list parses, and its reader properties are C1 and C3", () => {
    expect(refusal(VALID)).toBeUndefined();
    expect(readerProperties(parseCraftList(VALID)).map((each) => each.id)).toEqual(["C1", "C3"]);
  });

  // Catches: a property on the list that only one set's guide states.
  test("refuses a property with a line of one set only", () => {
    const one = changed((list) => {
      list.properties[0] = {
        ...list.properties[0],
        lines: [{ set: "alpha-set", path: "skills/writing-alpha/SKILL.md", line: 8 }],
      };
    });
    expect(refusal(one)).toContain("properties.0.lines: cites no line of the set beta-set");
    const unknown = changed((list) => {
      list.properties[1] = {
        ...list.properties[1],
        lines: [
          { set: "alpha-set", path: "skills/writing-alpha/SKILL.md", line: 11 },
          { set: "gamma-set", path: "docs/x.md", line: 1 },
        ],
      };
    });
    expect(refusal(unknown)).toContain("properties.1.lines.1.set: is no set of the list");
    expect(refusal(unknown)).toContain("properties.1.lines: cites no line of the set beta-set");
  });

  // Catches: two properties under one id, so that a gap's rule names either.
  test("refuses a duplicate id", () => {
    const twice = changed((list) => {
      list.properties[2] = { ...list.properties[2], id: "C1" };
    });
    expect(refusal(twice)).toContain("properties.2.id: gives the id C1 twice");
  });

  // Catches: a property no reader and no behaviour test checks, or an id a gap cannot name.
  test("checkedBy is reader or behaviour", () => {
    const judged = changed((list) => {
      list.properties[0] = { ...list.properties[0], checkedBy: "judge" };
    });
    expect(refusal(judged)).toContain("properties.0.checkedBy");
    const missing = changed((list) => {
      const { checkedBy: _, ...rest } = list.properties[0] ?? {};
      list.properties[0] = rest;
    });
    expect(refusal(missing)).toContain("properties.0.checkedBy");
  });

  test("an id that is not C<n>, and a lineEnd before its line, are refused", () => {
    for (const id of ["P1", "C0", "C", "c1", "C1a"]) {
      const bad = changed((list) => {
        list.properties[0] = { ...list.properties[0], id };
      });
      expect({ id, refused: refusal(bad)?.includes("properties.0.id: is not C<n>") }).toEqual({
        id,
        refused: true,
      });
    }
    const backwards = changed((list) => {
      const property = list.properties[0] as { lines: Record<string, unknown>[] };
      property.lines[0] = { ...property.lines[0], line: 9, lineEnd: 8 };
    });
    expect(refusal(backwards)).toContain("properties.0.lines.0.lineEnd: is before its line, 9");
  });

  // Catches: a reader property with nothing of theirs that shows it, or a property of testing
  // with no record of the two sets.
  test("a reader property with no example skill line is refused; a behaviour property with no record link is refused", () => {
    const noExample = changed((list) => {
      const { example: _, ...rest } = list.properties[0] ?? {};
      list.properties[0] = rest;
    });
    expect(refusal(noExample)).toContain(
      "properties.0.example: is missing: a reader property names one skill line of theirs that shows it"
    );
    const noRecord = changed((list) => {
      const { record: _, ...rest } = list.properties[1] ?? {};
      list.properties[1] = rest;
    });
    expect(refusal(noRecord)).toContain(
      "properties.1.record: is missing: a behaviour property links one record of its testing"
    );
    const notALink = changed((list) => {
      list.properties[1] = { ...list.properties[1], record: "RELEASE-NOTES.md#L3" };
    });
    expect(refusal(notALink)).toContain("properties.1.record: is not an https:// link");
  });
});

describe("craft-lines", () => {
  // Catches: a printed line that is not the one cited, a file read once per citation, or an
  // example left unread.
  test("prints each cited line and each example with its text at the set's commit, reading each file once", async () => {
    const file = path.join(scratch, "valid.json");
    writeFileSync(file, VALID);
    const calls: string[] = [];
    const ran = await craftLines(file, f12Gh(calls));
    expect({ code: ran.code, err: ran.err }).toEqual({ code: 0, err: "" });
    expect(ran.out).toContain(
      "alpha-set: acme/alpha-set at 2ce8aa15d05bd57213e6f746f953aafb7ded7a8b\n"
    );
    expect(ran.out).toContain(
      [
        "C1 (reader): The description says when to use the skill, and never sums up its steps.",
        "  alpha-set skills/writing-alpha/SKILL.md:8-9",
        '    8: The description says when to use the skill: start it with "Use when" and name the triggers.',
        "    9: It never sums up the skill's steps.",
        "  beta-set docs/authoring.md:3",
        '    3: A description starts with "Use when" or "Use for" and lists the situations that call for the skill, never its process.',
        "  example alpha-set skills/alpha-skill-one/SKILL.md:3",
        "    3: description: Use when a test fails and its cause is not known.",
      ].join("\n")
    );
    expect(ran.out).toContain(
      [
        "C2 (behaviour): A behaviour test of the skill has an arm that runs with no skill.",
        "  alpha-set skills/writing-alpha/SKILL.md:11",
        "    11: Test a skill with an arm that runs with no skill at all.",
        "  beta-set .agents/skills/beta-skill-work/references/evaluate.md:3",
        "    3: Run each case twice: once with the skill, and once with no skill, the control arm.",
        "  record https://github.com/acme/alpha-set/blob/main/RELEASE-NOTES.md#L3",
      ].join("\n")
    );
    expect(ran.out).toContain(
      "  example beta-set skills/beta-skill-one/SKILL.md:2\n    2: name: beta-skill-one\n"
    );
    expect(calls.toSorted()).toEqual(Object.keys(CONTENTS).toSorted());
  });

  // Catches: a cited line past the end of its file, or a file the commit lacks, passed as cited.
  test("a cited line past the end of its file at the commit fails", async () => {
    const file = path.join(scratch, "past-end.json");
    writeFileSync(file, fixture("F12-craft-list-past-end.json"));
    const ran = await craftLines(file);
    expect(ran.code).toBe(1);
    expect(ran.err.split("\n")).toEqual([
      "craft-lines: C1 beta-set docs/authoring.md:3-7 is past the end of the file, line 5, at 8a776c13a08eea0d6485c26c9bc96ea84d5a6cc6",
      "craft-lines: C2 alpha-set skills/writing-alpha/SKILL.md:30 is past the end of the file, line 12, at 2ce8aa15d05bd57213e6f746f953aafb7ded7a8b",
      "craft-lines: C3 example beta-set skills/beta-skill-nine/SKILL.md:2: acme/beta-set-plugin has no such file at 8a776c13a08eea0d6485c26c9bc96ea84d5a6cc6",
      "craft-lines: 3 cited lines cannot be read at their commits",
    ]);
    // The lines that can be read are printed all the same.
    expect(ran.out).toContain("  example alpha-set skills/alpha-skill-one/SKILL.md:3\n");
  });

  test("a list it cannot read, or that the shape refuses, is refused before any GitHub read", async () => {
    const calls: string[] = [];
    const missing = await craftLines(path.join(scratch, "no-such-list.json"), f12Gh(calls));
    expect(missing.code).toBe(2);
    expect(missing.err).toContain("craft-lines: cannot read the craft list");
    const file = path.join(scratch, "duplicate.json");
    writeFileSync(
      file,
      changed((list) => {
        list.properties[2] = { ...list.properties[2], id: "C1" };
      })
    );
    const refused = await craftLines(file, f12Gh(calls));
    expect(refused.code).toBe(2);
    expect(refused.err).toContain("gives the id C1 twice");
    expect(calls).toEqual([]);
  });
});
