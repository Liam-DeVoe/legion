/**
 * The engine's rule keeps an email link where a web address would reach into its text once the
 * link went (`src/whole-email-links.ts`), and finds each web address with a copy of the URL
 * search of mdast-util-gfm-autolink-literal, which keeps that search private. This holds the copy
 * to the package's own transform, as remark-gfm runs it after a parse: each text is searched by
 * both, and both must find the same web addresses at the same offsets. The texts are the decoded
 * texts the rule's rows join, then texts drawn from a fixed seed out of the pieces a web address
 * starts, runs and ends at.
 */

import { expect, test } from "bun:test";
import type { Root } from "@milkdown/kit/transformer";
import remarkGfm from "remark-gfm";
import { unified } from "unified";
import { webAddressesIn } from "../src/whole-email-links.js";

/** remark-gfm's transforms of the tree a parse builds, mdast-util-gfm-autolink-literal's among them. */
const transforms = (() => {
  const processor = unified().use(remarkGfm);
  processor.freeze();
  return (processor.data("fromMarkdownExtensions") ?? [])
    .flat()
    .flatMap((extension) => extension.transforms ?? []);
})();

/** The offsets of each web address remark-gfm's transform links in a paragraph holding `value`. */
function transformed(value: string): [number, number][] {
  const tree: Root = {
    type: "root",
    children: [{ type: "paragraph", children: [{ type: "text", value }] }],
  };
  for (const transform of transforms) transform(tree);
  const [paragraph] = tree.children;
  if (paragraph?.type !== "paragraph") throw new Error("the transform left no paragraph");
  const found: [number, number][] = [];
  let at = 0;
  for (const node of paragraph.children) {
    const [text] = node.type === "link" ? node.children : [node];
    const length = text?.type === "text" ? text.value.length : 0;
    if (node.type === "link" && !node.url.startsWith("mailto:")) found.push([at, at + length]);
    at += length;
  }
  return found;
}

/** The texts the rule's rows join when a link goes, and the texts the copy's branches part at. */
const ROWS = [
  "q/-www.a.bhosts@example.com",
  "'_www.a.bhosts@example.com",
  "x -_,-/-www.hosts@example.com y",
  "{._www.@example.com",
  "$-_www.hosts@example.com`",
  "}@e.co-=*^www.q.rhosts@example.com-",
  "http://-a.b@c.co",
  "x;http://-a.b@c.co",
  "q/-a.www.b@c.co",
  "q/-a-www.bhosts@example.com d",
  "q/-www.a.bhosts@example.com,-c@d.co",
  "www.a'b@c.test",
  "projects/p/serviceAccounts/agent-hosts@example.com",
  'https://x.test/a)"; b',
  "https://x.test/(a)) b",
  "https://x.test/a&amp;&amp;) b",
  "www.a_b.c.d www.d",
  "www.a.b_c.d",
  "xwww.a.b",
  "HTTPS://X.TEST/Y www.",
  "_www.a_www.a_www.ab",
];

/** What a drawn text is made of. */
const PIECES = [
  "www.",
  "WWW.",
  "www",
  "http://",
  "https://",
  "HtTpS://",
  "a",
  "b1",
  "com",
  "a_b",
  "q.r",
  "_",
  "-",
  ".",
  "(",
  ")",
  "'",
  '"',
  "!",
  "?",
  ",",
  ";",
  ":",
  "&",
  "<",
  ">",
  "]",
  "}",
  "*",
  "~",
  "/",
  "@",
  "x@y.co",
  " ",
  "\n",
  "é",
  "—",
  "😀",
];

/** `count` texts of one to twelve pieces, drawn by a fixed-seed mulberry32. */
function drawnTexts(seed: number, count: number): string[] {
  let state = seed;
  const next = (below: number): number => {
    state = (state + 0x6d2b79f5) | 0;
    let t = Math.imul(state ^ (state >>> 15), 1 | state);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return Math.floor((((t ^ (t >>> 14)) >>> 0) / 4294967296) * below);
  };
  return Array.from({ length: count }, () =>
    Array.from({ length: 1 + next(12) }, () => PIECES[next(PIECES.length)]).join("")
  );
}

const DRAWN = drawnTexts(4224, 5000);

test("remark-gfm's transform links a web address in most rows and a fifth of the drawn texts", () => {
  expect(ROWS.filter((text) => transformed(text).length > 0).length).toBeGreaterThan(15);
  expect(DRAWN.filter((text) => transformed(text).length > 0).length).toBeGreaterThan(1000);
});

for (const [name, texts] of [
  ["the rows", ROWS],
  ["5000 drawn texts", DRAWN],
] as const) {
  test(`the copy finds the web addresses remark-gfm's transform links: ${name}`, () => {
    for (const text of texts) {
      expect(webAddressesIn(text), JSON.stringify(text)).toEqual(transformed(text));
    }
  });
}
