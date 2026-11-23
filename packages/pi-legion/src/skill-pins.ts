import { expect } from "bun:test";
import { readFileSync } from "node:fs";
import * as path from "node:path";
import { REPO_ROOT } from "@legion/pi-shared/test/skills-guard";

// The pins this package's skill tests hold the texts to, shared by each test file that stages the
// skills (`stageSkills`), so every file reads a text the same way. A pin fails when a later edit
// deletes the sentence it names; it does not show that an agent follows the sentence.

// Whitespace is collapsed before the match, as shipped-agents.test.ts collapses it, so a pin
// survives a rewrap of the file it reads.
export const collapsed = (file: string) => readFileSync(file, "utf8").replace(/\s+/g, " ");

// The pins over the skills of one test file's staged partition: staged() is that file's staged
// root, read when a pin runs, after the file's beforeAll has staged it.
export function skillPins(staged: () => string) {
  // A sentence copied into several files is counted once per file, never over a composed prompt,
  // where a sentence in both roles/<role>.md and go/worker-common.md would appear twice. A file
  // that lost the sentence and a file that gained a second copy both fail, and the files are listed
  // rather than counted, so one that stopped carrying it fails as loudly as one that started.
  // A `skills/…` path is read from the staged partition, as every other skill pin is, so what is
  // counted is the copy the plugin ships.
  function countedOncePerFile(rule: string, relativePaths: string[]) {
    const counts = relativePaths.map((relative) => ({
      file: relative,
      found:
        collapsed(
          relative.startsWith("skills/")
            ? path.join(staged(), relative.slice("skills/".length))
            : path.join(REPO_ROOT, relative)
        ).split(rule).length - 1,
    }));
    expect(counts).toEqual(relativePaths.map((file) => ({ file, found: 1 })));
  }

  // The duties a staged file does not hold, one line each. A pin binds a condition to its duty
  // within one clause (`[^.;]*`), or within one field of a body template (`[^*]*`, which stops at
  // the next `**Field:**`): `collapsed` makes a file one line, so a bare `.*` would match from a
  // word at its top to one at its end. Each file's missing duties are listed together.
  function lacks(relative: string, duties: RegExp[]) {
    const text = collapsed(path.join(staged(), relative));
    return duties.filter((duty) => !duty.test(text)).map((duty) => `${relative} lacks ${duty}`);
  }

  return { countedOncePerFile, lacks };
}
