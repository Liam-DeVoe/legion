import { beforeAll, expect, test } from "bun:test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { main } from "./fixturescan";

// The fixture scan (testdata/CAPTURE.md, "The scan") over made-up files: a key file of a random
// fixture secret, a terms file of one made-up name, and each case's files in a directory of its
// own.

const scratch = temporaryDirectory("skill-cuts-fixturescan", { real: true });
const keyFile = path.join(scratch, "key");
const termsFile = path.join(scratch, "terms");
beforeAll(() => {
  writeFileSync(keyFile, fixtureSecret("key"), { mode: 0o600 });
  writeFileSync(termsFile, "made-up-private-name\n");
});

/** The scan of a directory `name` holding `files` (path to text): its exit status and each line it
 *  printed. */
function scan(name: string, files: Record<string, string>): { code: number; out: string[] } {
  const dir = path.join(scratch, name);
  for (const [file, text] of Object.entries(files)) {
    mkdirSync(path.dirname(path.join(dir, file)), { recursive: true });
    writeFileSync(path.join(dir, file), text);
  }
  const out: string[] = [];
  const code = main(["--key-file", keyFile, "--terms", termsFile, dir], {
    out: (text) => out.push(text),
    err: (text) => out.push(text),
  });
  return { code, out };
}

// Catches: a hit that prints more of the key or token it found than its first 6 characters.
test("a key or token shape is printed as its kind and at most its first 6 characters", () => {
  const token = `sk-${fixtureSecret("tok")}`;
  const { code, out } = scan("shape", { "a.json": JSON.stringify({ note: `use ${token} here` }) });
  expect(code).toBe(1);
  expect(out).toContain(`a.json: sk- key: ${token.slice(0, 6)}…`);
  expect(out.join("\n")).not.toContain(token.slice(0, 7));
});

// Catches: the decoded content of a GitHub contents answer left out of the random-run check.
test("a random-looking run in the decoded content of a GitHub contents answer is a hit", () => {
  const run = "Ab1".repeat(6);
  const content = Buffer.from(`# Notes\n\nThe build id is ${run}.\n`).toString("base64");
  const { code, out } = scan("contents", {
    "contents.json": JSON.stringify({ name: "notes.md", encoding: "base64", content }),
  });
  expect(code).toBe(1);
  expect(out).toContain(
    `contents.json: a random-looking run of ${run.length} characters in "(content)": ${run.slice(0, 6)}…`
  );
});

// Catches: an allow-list entry for a bot's no-reply address that the email shape never matches,
// so no `[bot]` address is checked at all.
test("a bot's no-reply address is checked: the allowed one passes and another is a hit", () => {
  const { code, out } = scan("bots", {
    "allowed.txt": "Co-authored-by: acme-agent[bot]@users.noreply.github.com\n",
    "other.txt": "Co-authored-by: other-app[bot]@users.noreply.github.com\n",
  });
  expect(code).toBe(1);
  expect(out.filter((line) => line.includes("email address"))).toEqual([
    "other.txt: email address: other-…",
  ]);
});
