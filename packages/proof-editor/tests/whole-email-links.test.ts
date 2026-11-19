/**
 * The engine links an email address whole, or not at all (INFRA-4224, R4): `remarkWholeEmailLinks`
 * (`src/whole-email-links.ts`), installed in both of the engine's pipelines.
 *
 * An email link that a reader made from a bare address is read with the run of characters RFC
 * 5322 allows in a local part (its `atext` and `.`) in the decoded text just before it, which a
 * character outside that set ends, and which goes on through the text of a link a reader made
 * from bare text, unless that link would read on into the character after it were it written
 * bare, or stands next to an email link the writer wrote, or its text starts `_www.`. At any
 * other node the run ends too; when that node's text ends in a character of the set, markup or
 * that link splits the address, and the link stays as remark-gfm made it. When every
 * character of the run is one GFM links in a local part (`[-.+_A-Za-z0-9]`), the link takes the
 * run, unless the run reaches into a reader's link, when both stand; when one is not, the link
 * goes, and its text joins the text around it, unless a web address GFM links in the joined text
 * would then reach into the address, when the link stays. With no run the link stands, and a
 * link the writer wrote is never changed. Go's reader applies the same rule (`wholeEmailLinks` in
 * `packages/envoy/internal/dispatch/pmdoc/autolink_transform.go`), and these are the rows its
 * tests hold it to; the last are the shapes a write was refused for before (Go's
 * `TestLinkifyStoresEveryRefusedShape`), which both read from one file.
 *
 * Each row is read in a paragraph, at a line's start after a hard break, in a list item, in a
 * typed block and in a list item there, in a quote and in a table cell. Each is read through the
 * headless engine, without `readOnly` and with it, and through the live editor's shared parser,
 * which a text/plain paste reaches (`tests/paste.ts`). Each reads the same tree in all three,
 * except where a row names what the paste reads of its own, and each row asserts its runs in
 * each, so a failure names the pipeline that reads it wrong. A row the rule changes names beside
 * it what the engine read before the rule; a control row read the same before the rule as after
 * it.
 */

import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import type { Node as ProseMirrorNode } from "@milkdown/kit/prose/model";
import remarkGfm from "remark-gfm";
import remarkInlineLinks from "remark-inline-links";
import remarkParse from "remark-parse";
import { unified } from "unified";
import { BLOCK_ID_ATTR, createHeadlessProof } from "../src/lib-headless.js";
import { remarkContainerDirectives } from "../src/lib-remark-directive-plugin.js";
import { remarkWholeEmailLinks } from "../src/whole-email-links.js";
import { serverBlockSchema as blockSchema } from "./block-schema-fixture";
import { withMarksEditor } from "./marks-editor";
import { pastePlainText, withHTMLElementGlobal } from "./paste";

/** One inline run: its text, and the `href` of the link over it, or null. */
interface Run {
  readonly text: string;
  readonly link: string | null;
}

function plain(text: string): Run {
  return { text, link: null };
}

/** A run under the link to the address it holds, which is the whole address the reader sees. */
function address(text: string): Run {
  return { text, link: `mailto:${text}` };
}

/**
 * Every inline node of `doc`, in document order, as its text and the link over it; a node that is
 * no text, as a hard break or inline HTML, as its type's name in brackets.
 */
function runsOf(doc: ProseMirrorNode): Run[] {
  const runs: Run[] = [];
  doc.descendants((node) => {
    if (!node.isInline) return true;
    const link = node.marks.find((mark) => mark.type.name === "link");
    runs.push({
      text: node.isText ? (node.text ?? "") : `[${node.type.name}]`,
      link: link === undefined ? null : String(link.attrs.href),
    });
    return false;
  });
  return runs;
}

type NodeJson = { type: string; attrs?: Record<string, unknown>; content?: NodeJson[] };

/** `doc` as JSON without the block ids each parse mints afresh. */
function tree(doc: ProseMirrorNode): NodeJson {
  const strip = (node: NodeJson): NodeJson => {
    const attrs = { ...node.attrs };
    delete attrs[BLOCK_ID_ATTR];
    return { ...node, attrs, content: node.content?.map(strip) };
  };
  return strip(doc.toJSON() as NodeJson);
}

interface Row {
  readonly markdown: string;
  readonly runs: readonly Run[];
  /** The link reference definition the row's link names, written after the row's block. */
  readonly definition?: string;
  /**
   * What the paste reads where it reads a tree of its own for a reason that is no part of this
   * rule: it drops inline HTML the engine keeps. Its links are the engine's still.
   */
  readonly pasted?: readonly Run[];
}

/** The rows whose reading the rule changes. */
const CHANGED_ROWS: readonly Row[] = [
  {
    // Before: `projects/example-project/serviceAccounts/agent-` and a link to
    // `hosts@example-project.iam.gserviceaccount.com`, which names another account.
    markdown:
      "projects/example-project/serviceAccounts/agent-hosts@example-project.iam.gserviceaccount.com",
    runs: [
      plain(
        "projects/example-project/serviceAccounts/agent-hosts@example-project.iam.gserviceaccount.com"
      ),
    ],
  },
  {
    markdown: "projects/p/serviceAccounts/agent-hosts@example.com",
    runs: [plain("projects/p/serviceAccounts/agent-hosts@example.com")],
  },
  {
    // Before: a link to `compute@developer.gserviceaccount.com`.
    markdown: "serviceAccounts/000000000000-compute@developer.gserviceaccount.com",
    runs: [plain("serviceAccounts/000000000000-compute@developer.gserviceaccount.com")],
  },
  {
    // Before, for each of the three: a link to `c@example.com`.
    markdown: "a/b.c@example.com",
    runs: [plain("a/b.c@example.com")],
  },
  { markdown: "a/b+c@example.com", runs: [plain("a/b+c@example.com")] },
  { markdown: "a/b_c@example.com", runs: [plain("a/b_c@example.com")] },
  {
    // Before: links to `c@example.com` and `f@example.com`. Each link that goes joins the text
    // before it and the text after it, so the paragraph is one text again.
    markdown: "a/b.c@example.com, d/e.f@example.com.",
    runs: [plain("a/b.c@example.com, d/e.f@example.com.")],
  },
  {
    // Before: a link to `hosts@example.com`; the transform reads the escape's decoded `@`.
    markdown: "projects/p/serviceAccounts/agent-hosts\\@example.com",
    runs: [plain("projects/p/serviceAccounts/agent-hosts@example.com")],
  },
  {
    // Before: a link to `e-f@example.com`, which the tokenizer starts right after the `%`.
    markdown: "a.b+c_d%e-f@example.com",
    runs: [plain("a.b+c_d%e-f@example.com")],
  },
  {
    // Before: `agent&` and a link to `hosts@example.com`, which the tokenizer starts right after
    // a character reference's `;`; the run holds the decoded `&`.
    markdown: "agent&amp;hosts@example.com",
    runs: [plain("agent&hosts@example.com")],
  },
  {
    // Before, for each of these: the text through the reference, and a link to
    // `hosts@example.com`. Each symbol is a character RFC 5322 allows in a local part and GFM
    // does not link, so the run holds it.
    markdown: "x $agent&#45;hosts@example.com",
    runs: [plain("x $agent-hosts@example.com")],
  },
  { markdown: "x 'agent&#45;hosts@example.com'", runs: [plain("x 'agent-hosts@example.com'")] },
  { markdown: "x =agent&#45;hosts@example.com", runs: [plain("x =agent-hosts@example.com")] },
  { markdown: "x #agent&#45;hosts@example.com", runs: [plain("x #agent-hosts@example.com")] },
  { markdown: "x ?agent&#45;hosts@example.com", runs: [plain("x ?agent-hosts@example.com")] },
  { markdown: "x ^agent&#45;hosts@example.com", runs: [plain("x ^agent-hosts@example.com")] },
  {
    // Before: `agent-` and a link to `hosts@example.com`; the run is all GFM's, so the link
    // takes it.
    markdown: "agent&#45;hosts@example.com",
    runs: [address("agent-hosts@example.com")],
  },
  {
    // Before: `x agent-` and a link to `hosts@example.com`.
    markdown: "x agent&#45;hosts@example.com",
    runs: [plain("x "), address("agent-hosts@example.com")],
  },
  {
    // Before: `xé-` and a link to `hosts@example.com`. `é` ends the run, so the run is `-`,
    // which is what the engine reads for `xé-hosts@example.com`.
    markdown: "xé&#45;hosts@example.com",
    runs: [plain("xé"), address("-hosts@example.com")],
  },
  {
    // Before: `x`, the tag, the path through `agent-` and a link to `hosts@example.com`. The
    // tag's text ends in `>`, which ends the run, so the run is the path's text, `/` and all.
    markdown: "x<br>projects/p/serviceAccounts/agent-hosts@example.com",
    runs: [
      plain("x"),
      plain("[html]"),
      plain("projects/p/serviceAccounts/agent-hosts@example.com"),
    ],
    pasted: [plain("xprojects/p/serviceAccounts/agent-hosts@example.com")],
  },
  {
    // Before: the code, `agent-` and a link to `hosts@example.com`. The code's text ends in `:`,
    // so the run is `agent-`, and the link takes it.
    markdown: "`SA:`agent&#45;hosts@example.com",
    runs: [plain("SA:"), address("agent-hosts@example.com")],
  },
  {
    // Before: the comment's `a`, `gent-` and a link to `hosts@example.com`. The span's closing
    // tag ends in `>`, so the run is `gent-`, as Go reads it too.
    markdown:
      '<span data-proof="comment" data-id="c1" data-by="user:x">a</span>gent&#45;hosts@example.com',
    runs: [plain("a"), address("gent-hosts@example.com")],
  },
  {
    // Before: `alice@example.com`, `/bob-` and a link to `smith@example.com`. A `/` does not
    // continue an email's domain, so the run reaches through `alice@example.com`'s link to its
    // `@` and holds the `/`: the later link goes.
    markdown: "alice@example.com/bob-smith@example.com",
    runs: [address("alice@example.com"), plain("/bob-smith@example.com")],
  },
  {
    // Before: `mail `, `a@b.co`, `&` and a link to `c-d@e.co`.
    markdown: "mail a@b.co&c-d@e.co now",
    runs: [plain("mail "), address("a@b.co"), plain("&c-d@e.co now")],
  },
];

/** The rows the rule leaves alone: each read this way before it too. */
const CONTROL_ROWS: readonly Row[] = [
  {
    // R4's example: the address alone is linked whole.
    markdown: "Grant act-as on agent-hosts@example-project.iam.gserviceaccount.com.",
    runs: [
      plain("Grant act-as on "),
      address("agent-hosts@example-project.iam.gserviceaccount.com"),
      plain("."),
    ],
  },
  { markdown: "agent-hosts@example.com", runs: [address("agent-hosts@example.com")] },
  {
    markdown: "x agent-hosts@example.com",
    runs: [plain("x "), address("agent-hosts@example.com")],
  },
  {
    // No reader links an address right after a `/`, and no `-`, `.`, `+` or `_` stands in it.
    markdown: "serviceAccounts/agenthosts@example.com",
    runs: [plain("serviceAccounts/agenthosts@example.com")],
  },
  {
    // No run stands before the link: `é` ends the walk.
    markdown: "xé-agent@example.com",
    runs: [plain("xé"), address("-agent@example.com")],
  },
  {
    // The run is GFM's characters and reaches into the email link remark-gfm made before it,
    // which a `+` does not continue: one link never takes another's text, so both stand.
    markdown: "a@b.co+c@d.co",
    runs: [address("a@b.co"), address("+c@d.co")],
  },
  {
    // A link a reader made that, written bare, would read on into the text after it ends the
    // run as markup does, whatever the run holds: a web link, which runs on through the `'`, `~`
    // or `?` after it, and an email link before a `-`, which continues its domain.
    markdown: "www\\.a'b@c.test",
    runs: [{ text: "www.a", link: "http://www.a" }, plain("'"), address("b@c.test")],
  },
  {
    markdown: "q/www.a.b~hosts@example.com",
    runs: [
      plain("q/"),
      { text: "www.a.b~", link: "http://www.a.b~" },
      address("hosts@example.com"),
    ],
  },
  {
    markdown: "x@y.co&#45;%hosts@example.com",
    runs: [address("x@y.co"), plain("-%"), address("hosts@example.com")],
  },
  {
    markdown: "x@y.co&#45;*hosts@example.com",
    runs: [address("x@y.co"), plain("-*"), address("hosts@example.com")],
  },
  {
    markdown: "%www.?hosts@example.com",
    runs: [
      plain("%"),
      { text: "www", link: "http://www" },
      plain(".?"),
      address("hosts@example.com"),
    ],
  },
  {
    // The run holds a character GFM does not link, and once the link went, a web address GFM
    // links would reach into the address (`q/-www.a.bhosts@example.com`, `'_www.a.b…`,
    // `http://-a.b@c.co`): the link remark-gfm made stands.
    markdown: "'_www.a.bhosts@example.com",
    runs: [plain("'"), address("_www.a.bhosts@example.com")],
  },
  {
    markdown: "q/&#45;www.a.bhosts@example.com",
    runs: [plain("q/-"), address("www.a.bhosts@example.com")],
  },
  {
    markdown: "http://&#45;a.b@c.co",
    runs: [plain("http://-"), address("a.b@c.co")],
  },
  {
    // Inline markup splits the address: the text before the run's text ends in a character of
    // the run, so the run is not one run, and remark-gfm's link stands.
    markdown: "*agent*-hosts@example.com",
    runs: [plain("agent"), address("-hosts@example.com")],
  },
  {
    markdown: "`agent`-hosts@example.com",
    runs: [plain("agent"), address("-hosts@example.com")],
  },
  {
    markdown: "*agent*&#45;hosts@example.com",
    runs: [plain("agent"), plain("-"), address("hosts@example.com")],
  },
  {
    markdown: "`agent`&#45;hosts@example.com",
    runs: [plain("agent"), plain("-"), address("hosts@example.com")],
  },
  {
    // A link the writer wrote is never changed: an explicit link, ...
    markdown: "[agent-hosts@example.com](mailto:agent-hosts@example.com)",
    runs: [address("agent-hosts@example.com")],
  },
  {
    // ... as a rendering stored before the rule writes the early link it read, ...
    markdown:
      "agent-[hosts@example-project.iam.gserviceaccount.com](mailto:hosts@example-project.iam.gserviceaccount.com)",
    runs: [plain("agent-"), address("hosts@example-project.iam.gserviceaccount.com")],
  },
  {
    // ... one whose text starts with the `[` its source starts with, ...
    markdown: "agent-[\\[hosts@example.com](mailto:[hosts@example.com)",
    runs: [plain("agent-"), address("[hosts@example.com")],
  },
  // ... an angle-bracket autolink, ...
  { markdown: "<agent-hosts@example.com>", runs: [address("agent-hosts@example.com")] },
  {
    markdown: "agent-<hosts@example.com>",
    runs: [plain("agent-"), address("hosts@example.com")],
  },
  {
    markdown: "a/<hosts@example.com>",
    runs: [plain("a/"), address("hosts@example.com")],
  },
  {
    // ... and a reference-style link, which remark-inline-links makes with no position, as the
    // transform makes its own, while the label's text keeps its own; the live editor's
    // commonmark preset runs remark-inline-links before the rule.
    markdown: "agent-[hosts@example.com][r]",
    definition: "[r]: mailto:hosts@example.com",
    runs: [plain("agent-"), address("hosts@example.com")],
  },
  {
    markdown: "x[a@b.test][r]",
    definition: "[r]: mailto:a@b.test",
    runs: [plain("x"), address("a@b.test")],
  },
];

/** A hard break, an inline node with no text. */
const HARD_BREAK = plain("[hardbreak]");

/**
 * A block a row is read in: its markdown around the row's, and the runs it reads before the
 * row's. Each table's column is aligned left, because a paste reads a column with no alignment as
 * left aligned, which is no part of this rule.
 */
interface Context {
  readonly name: string;
  readonly block: (markdown: string) => string;
  readonly runs: readonly Run[];
}

const CONTEXTS: readonly Context[] = [
  { name: "a paragraph", block: (markdown) => markdown, runs: [] },
  {
    name: "at a line's start",
    block: (markdown) => `Lead\\\n${markdown}`,
    runs: [plain("Lead"), HARD_BREAK],
  },
  { name: "a list item", block: (markdown) => `- ${markdown}`, runs: [] },
  { name: "a typed block", block: (markdown) => `:::callout{}\n${markdown}\n:::`, runs: [] },
  {
    name: "a list item in a typed block",
    block: (markdown) => `:::callout{}\n- ${markdown}\n:::`,
    runs: [],
  },
  { name: "a quote", block: (markdown) => `> ${markdown}`, runs: [] },
  {
    name: "a table cell",
    block: (markdown) => `| a |\n| :-- |\n| ${markdown} |`,
    runs: [plain("a")],
  },
];

/** `row`'s markdown in `context`, its definition after the block. */
function inContext(row: Row, context: Context): string {
  const block = context.block(row.markdown);
  return row.definition === undefined ? block : `${block}\n\n${row.definition}`;
}

/** The headless engine, without `readOnly` and with it; each parse reads its own markdown. */
const headlessEngines = Promise.all([
  createHeadlessProof({ blockSchema }),
  createHeadlessProof({ blockSchema, readOnly: true }),
]);

/** A pipeline's name and its reading. */
type Reading = [string, ProseMirrorNode];

/** The three readings of `markdown`: the headless engine's, read-only and not, and the paste's. */
async function readings(markdown: string): Promise<[Reading, Reading, Reading]> {
  const [headless, readOnly] = await headlessEngines;
  let pasted: ProseMirrorNode | undefined;
  await withMarksEditor(
    "",
    ({ view }) => {
      withHTMLElementGlobal(() => pastePlainText(view, markdown));
      pasted = view.state.doc;
    },
    { blockSchema }
  );
  if (pasted === undefined) throw new Error("the editor read no paste");
  return [
    ["the headless engine", headless.parseMarkdown(markdown)],
    ["the headless engine, readOnly", readOnly.parseMarkdown(markdown)],
    ["the live editor's parser (a text/plain paste)", pasted],
  ];
}

/**
 * Asserts `row`'s runs in `context` in each pipeline, then that the three read the same tree, or,
 * for a row the paste reads a tree of its own for, that the two headless readings do.
 */
async function expectRow(row: Row, context: Context): Promise<void> {
  const [headless, readOnly, paste] = await readings(inContext(row, context));
  for (const [pipeline, doc] of [headless, readOnly]) {
    expect(runsOf(doc), pipeline).toEqual([...context.runs, ...row.runs]);
  }
  expect(runsOf(paste[1]), paste[0]).toEqual([...context.runs, ...(row.pasted ?? row.runs)]);
  const others = row.pasted === undefined ? [readOnly, paste] : [readOnly];
  for (const [pipeline, doc] of others) expect(tree(doc), pipeline).toEqual(tree(headless[1]));
}

for (const context of CONTEXTS) {
  for (const row of CHANGED_ROWS) {
    test(`changed, ${context.name}: ${JSON.stringify(row.markdown)}`, async () => {
      await expectRow(row, context);
    });
  }
  for (const row of CONTROL_ROWS) {
    test(`control, ${context.name}: ${JSON.stringify(row.markdown)}`, async () => {
      await expectRow(row, context);
    });
  }
}

/**
 * A shape a write was refused for before the rule's last change, with the runs Go reads: the 66
 * the round-5 review of INFRA-4224 listed (its `infra-4224-r5-refused-shapes.jsonl`), 5 more of
 * cause B, and the four a 50,000-shape probe found that the run rule refuses without its second
 * exception. Go's `TestLinkifyStoresEveryRefusedShape` holds Go to the same file. Each is read
 * whole, in the block it is written in, in all three pipelines.
 */
interface RefusedShape {
  readonly markdown: string;
  readonly runs: readonly Run[];
}

const REFUSED_SHAPES = readFileSync(
  join(import.meta.dir, "../../envoy/internal/dispatch/pmdoc/testdata/refused-email-shapes.jsonl"),
  "utf8"
)
  .trim()
  .split("\n")
  .map((line): RefusedShape => JSON.parse(line));

/**
 * What the paste reads of a shape where it reads a tree of its own for a reason that is no part of
 * this rule: it keeps a line's ending as a hard break, and drops a `<br>`. Its links are the
 * engine's still.
 */
const REFUSED_SHAPES_PASTED: Readonly<Record<string, readonly Run[]>> = {
  "- *}\n&www.q.r~~x@y.cohosts@example.com\n": [
    plain("*}"),
    HARD_BREAK,
    plain("&"),
    { text: "www.q.r~~", link: "http://www.q.r~~" },
    address("x@y.cohosts"),
    plain("@example.com"),
  ],
  ':::callout{#c1 kind="note" title="T"}\n\\-<br>www.\\}?hosts@example.com}\n:::\n': [
    plain("-"),
    { text: "www", link: "http://www" },
    plain(".}?"),
    address("hosts@example.com"),
    plain("}"),
  ],
};

for (const shape of REFUSED_SHAPES) {
  test(`refused before, stored now: ${JSON.stringify(shape.markdown)}`, async () => {
    const [headless, readOnly, paste] = await readings(shape.markdown);
    for (const [pipeline, doc] of [headless, readOnly]) {
      expect(runsOf(doc), pipeline).toEqual([...shape.runs]);
    }
    const pasted = REFUSED_SHAPES_PASTED[shape.markdown] ?? shape.runs;
    expect(runsOf(paste[1]), paste[0]).toEqual([...pasted]);
    expect(tree(readOnly[1]), readOnly[0]).toEqual(tree(headless[1]));
  });
}

/**
 * A document holding email links of every kind, none of them one the rule changes: the
 * tokenizer's whole addresses, one whose run starts after a letter outside ASCII and one right
 * after a character reference that ends in no character of a run, the transform's whole address
 * (an escape stops the tokenizer), one whose run markup splits and one whose run of GFM's
 * characters reaches into the email link before it, a path with no link, the writer's own links,
 * and a URL.
 */
const NO_SUCH_LINK = [
  "# Grant act-as on agent-hosts@example-project.iam.gserviceaccount.com.",
  "",
  "xé-agent@example.com, serviceAccounts/agenthosts@example.com and x &#233;agent@example.com.",
  "See agent\\-hosts@example.com.",
  "*agent*&#45;hosts@example.com and a@b.co+c@d.co.",
  "",
  "agent-[hosts@example.com](mailto:hosts@example.com), agent-<hosts@example.com>,",
  "agent-[hosts@example.com][r] and https://example.com/a-b@c.",
  "",
  "- *agent-hosts@example.com* `a/b.c@example.com`",
  "  > x agent-hosts@example.com",
  "",
  ":::callout{}",
  "| a |",
  "| --- |",
  "| agent-hosts@example.com |",
  ":::",
  "",
  "[r]: mailto:hosts@example.com",
].join("\n");

/** As much of an mdast node as these checks read. */
interface TreeNode {
  type: string;
  url?: string;
  children?: TreeNode[];
}

/** Every node of `root`, each with the children array it holds. */
function nodesOf(root: TreeNode): [TreeNode, TreeNode[] | undefined][] {
  const nodes: [TreeNode, TreeNode[] | undefined][] = [];
  const walk = (node: TreeNode): void => {
    nodes.push([node, node.children]);
    for (const child of node.children ?? []) walk(child);
  };
  walk(root);
  return nodes;
}

test("the transformer touches no node of a document that holds no such link", () => {
  const parser = unified()
    .use(remarkParse)
    .use(remarkGfm)
    .use(remarkContainerDirectives)
    .use(remarkInlineLinks);
  const parsed = parser.runSync(parser.parse(NO_SUCH_LINK), NO_SUCH_LINK) as TreeNode;
  const mailto = nodesOf(parsed).filter(([node]) => node.url?.startsWith("mailto:")).length;
  // The tokenizer's links, the transform's and the writer's are each there to be left alone.
  expect(mailto).toBe(13);
  const before = structuredClone(parsed);
  const nodes = nodesOf(parsed);

  unified().use(remarkWholeEmailLinks).runSync(parsed, NO_SUCH_LINK);

  expect(parsed).toEqual(before);
  const after = nodesOf(parsed);
  expect(after.length).toBe(nodes.length);
  nodes.forEach(([node, children], index) => {
    expect(after[index][0]).toBe(node);
    expect(after[index][1]).toBe(children);
  });
});
