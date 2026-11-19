// Parses a corpus with both engines of the headless proof and prints one sha256 per input and
// mode of the document's JSON: run once at each lock and compare the two outputs.
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { Glob } from "bun";
import { createHeadlessProof } from "/legion/scratch/tmp/piece-d/packages/proof-editor/src/lib-headless.ts";
import { serverBlockSchema } from "/legion/scratch/tmp/piece-d/packages/proof-editor/tests/block-schema-fixture.ts";

const root = "/legion/scratch/tmp/piece-d";
const inputs: [string, string][] = [];
const nest = (depth: number) =>
  Array.from({ length: depth }, (_, level) => `${"  ".repeat(level)}- a`).join("\n") + "\n";
for (const [name, body] of [
  ["plain list", "- x\n".repeat(8000)],
  ["quote lazy", `> x\n${"x\n".repeat(15998)}`],
  ["item lazy", `- x\n${"x\n".repeat(15998)}`],
  ["brackets", "[".repeat(15000) + "]".repeat(15000)],
  ["nest49", nest(49) + "\n".repeat(30000)],
  ["nest100", nest(100) + "\n".repeat(20000)],
  ["marker chain", `${"- ".repeat(15999)}x`],
  ["hard breaks", "x  \ny \n".repeat(4500)],
  ["links and autolinks", "see https://example.com/a and www.example.org, mail ada@example.com, [ref][r], [inline](http://x.y)\n\n[r]: http://r.example\n".repeat(300)],
] as const) inputs.push([name, body]);
for (const pattern of ["docs/**/*.md", "packages/*/AGENTS.md", "AGENTS.md", "packages/proof-editor/**/*.md"]) {
  for (const file of new Glob(pattern).scanSync({ cwd: root })) {
    if (file.includes("node_modules")) continue;
    inputs.push([file, readFileSync(`${root}/${file}`, "utf8")]);
  }
}
inputs.sort(([a], [b]) => (a < b ? -1 : 1));
const engines = [
  ["read-only", await createHeadlessProof({ blockSchema: serverBlockSchema, readOnly: true })],
  ["default", await createHeadlessProof({ blockSchema: serverBlockSchema })],
  ["plain", await createHeadlessProof()],
] as const;
let id = 0;
for (const [name, body] of inputs) {
  for (const [mode, proof] of engines) {
    for (const softBreaks of ["space", "line"] as const) {
      let out: string;
      try {
        out = JSON.stringify(proof.parseMarkdown(body, softBreaks).toJSON(), (key, value) =>
          key === "blockId" ? undefined : value
        );
      } catch (error) {
        out = `threw: ${error instanceof Error ? error.message : String(error)}`;
      }
      console.log(`${createHash("sha256").update(out).digest("hex").slice(0, 16)}\t${mode}\t${softBreaks}\t${name}`);
      id++;
    }
  }
}
console.error(`${id} documents`);
