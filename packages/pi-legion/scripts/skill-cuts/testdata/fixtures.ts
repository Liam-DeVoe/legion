/**
 * Builders of made-up fixtures that more than one test file of the runner uses. Each repository,
 * commit and route they give is made up.
 */
import { writeFileSync } from "node:fs";
import { type Manifest, type ManifestKind, parseManifest } from "../copies";

/** A manifest of `repository` at `file` in its tree, parsed as `kind`. */
export const madeManifest = (
  repository: string,
  file: string,
  kind: ManifestKind,
  text: string
): Manifest => ({
  repository,
  path: file,
  kind,
  source: "tree",
  ...parseManifest(kind, file, text),
  error: "",
  rows: 0,
});

/** GitHub's answers, by route, for the made-up acme/skills at `commit` as GitHub lists a
 *  repository of too many entries: its recursive tree cut short, then skills/beta's, and
 *  skills/beta's one level even read alone. */
export function truncatedSkillsTree(commit: string): Record<string, object> {
  const [skills, beta] = ["1".repeat(40), "2".repeat(40)];
  return {
    [`repos/acme/skills/git/trees/${commit}?recursive=1`]: { tree: [], truncated: true },
    [`repos/acme/skills/git/trees/${commit}`]: {
      tree: [{ path: "skills", mode: "040000", type: "tree", sha: skills }],
      truncated: false,
    },
    [`repos/acme/skills/git/trees/${skills}`]: {
      tree: [{ path: "beta", mode: "040000", type: "tree", sha: beta }],
      truncated: false,
    },
    [`repos/acme/skills/git/trees/${beta}?recursive=1`]: { tree: [], truncated: true },
    [`repos/acme/skills/git/trees/${beta}`]: { tree: [], truncated: true },
  };
}

/** Writes at `file` a stand-in gh that appends each call's route (its last argument) to `log`
 *  and answers it with the JSON `answers` holds for it, or as GitHub answers a route it does not
 *  hold (exit 1, `gh: Not Found (HTTP 404)`). */
export function writeGhStandIn(file: string, log: string, answers: Record<string, object>): void {
  writeFileSync(
    file,
    `#!${process.execPath}
import { appendFileSync } from "node:fs";
const answers = ${JSON.stringify(answers)};
const route = process.argv.at(-1) ?? "";
appendFileSync(${JSON.stringify(log)}, route + "\\n");
if (answers[route] === undefined) {
  process.stderr.write("gh: Not Found (HTTP 404)\\n");
  process.exit(1);
}
process.stdout.write(JSON.stringify(answers[route]));
`,
    { mode: 0o755 }
  );
}
