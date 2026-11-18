/**
 * The engine links an email address whole, or not at all (INFRA-4224, R4): `remarkWholeEmailLinks`
 * (`src/whole-email-links.ts`), installed in both of the engine's pipelines.
 *
 * An email link that a reader made from a bare address, and that starts inside a run of
 * local-part characters (`[-.+_A-Za-z0-9]`) of the text just before it, takes the run, whatever
 * stands before the run, except a `/`. When the character right before the run is `/`, the link
 * goes, and its text joins the text around it. A link the writer wrote is never changed. Go's
 * reader applies the same rule (`packages/envoy/internal/dispatch/pmdoc/linkify.go`), and these
 * are the rows its tests hold it to.
 *
 * Each row is read through the headless engine, without `readOnly` and with it, and through the
 * live editor's shared parser, which a text/plain paste reaches (`tests/paste.ts`). Each reads
 * the same tree in all three, and each row asserts its runs in each, so a failure names the
 * pipeline that reads it wrong. A row the rule changes names beside it what the engine read
 * before the rule; a control row read the same before the rule as after it.
 */

import { expect, test } from "bun:test";
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

/** Every inline node of `doc`, in document order, as its text and the link over it. */
function runsOf(doc: ProseMirrorNode): Run[] {
  const runs: Run[] = [];
  doc.descendants((node) => {
    if (!node.isInline) return true;
    const link = node.marks.find((mark) => mark.type.name === "link");
    runs.push({ text: node.text ?? "", link: link === undefined ? null : String(link.attrs.href) });
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
}

/** The rows whose reading the rule changes. */
const CHANGED_ROWS: readonly Row[] = [
  {
    // Before: `projects/theorem-research/serviceAccounts/agent-` and a link to
    // `hosts@theorem-research.iam.gserviceaccount.com`, which names another account.
    markdown:
      "projects/theorem-research/serviceAccounts/agent-hosts@theorem-research.iam.gserviceaccount.com",
    runs: [
      plain(
        "projects/theorem-research/serviceAccounts/agent-hosts@theorem-research.iam.gserviceaccount.com"
      ),
    ],
  },
  {
    // Before: a link to `compute@developer.gserviceaccount.com`.
    markdown: "serviceAccounts/482450377695-compute@developer.gserviceaccount.com",
    runs: [plain("serviceAccounts/482450377695-compute@developer.gserviceaccount.com")],
  },
  {
    // Before, for each of the three: a link to `c@example.com`.
    markdown: "a/b.c@example.com",
    runs: [plain("a/b.c@example.com")],
  },
  { markdown: "a/b+c@example.com", runs: [plain("a/b+c@example.com")] },
  { markdown: "a/b_c@example.com", runs: [plain("a/b_c@example.com")] },
  {
    // Before: `x agent-` and a link to `hosts@example.com`; the tokenizer starts an address
    // right after a character reference's `;`.
    markdown: "x agent&#45;hosts@example.com",
    runs: [plain("x "), address("agent-hosts@example.com")],
  },
  {
    // Before: `xé-` and a link to `hosts@example.com`. `é` is no local-part character, so the
    // run is `-`, which is what the engine reads for `xé-hosts@example.com`.
    markdown: "xé&#45;hosts@example.com",
    runs: [plain("xé"), address("-hosts@example.com")],
  },
  {
    // Before: `x $agent-` and a link to `hosts@example.com`.
    markdown: "x $agent&#45;hosts@example.com",
    runs: [plain("x $"), address("agent-hosts@example.com")],
  },
  {
    // Before: a link to `hosts@example.com`; the transform reads the escape's decoded `@`.
    markdown: "projects/p/serviceAccounts/agent-hosts\\@example.com",
    runs: [plain("projects/p/serviceAccounts/agent-hosts@example.com")],
  },
  {
    // Before: links to `c@example.com` and `f@example.com`. Each link that goes joins the text
    // before it and the text after it, so the paragraph is one text again.
    markdown: "a/b.c@example.com, d/e.f@example.com.",
    runs: [plain("a/b.c@example.com, d/e.f@example.com.")],
  },
  {
    // The path and the character reference again in a list item, a typed block, a quote and a
    // table cell; before, each read as in its paragraph row above. Each table's column is
    // aligned left, because a paste reads a column with no alignment as left aligned, which is
    // no part of this rule.
    markdown: "- projects/p/serviceAccounts/agent-hosts@example.com",
    runs: [plain("projects/p/serviceAccounts/agent-hosts@example.com")],
  },
  {
    markdown: ":::callout{}\nprojects/p/serviceAccounts/agent-hosts@example.com\n:::",
    runs: [plain("projects/p/serviceAccounts/agent-hosts@example.com")],
  },
  {
    markdown: "> projects/p/serviceAccounts/agent-hosts@example.com",
    runs: [plain("projects/p/serviceAccounts/agent-hosts@example.com")],
  },
  {
    markdown: "| a |\n| :-- |\n| projects/p/serviceAccounts/agent-hosts@example.com |",
    runs: [plain("a"), plain("projects/p/serviceAccounts/agent-hosts@example.com")],
  },
  {
    markdown: "- x agent&#45;hosts@example.com",
    runs: [plain("x "), address("agent-hosts@example.com")],
  },
  {
    markdown: ":::callout{}\nx agent&#45;hosts@example.com\n:::",
    runs: [plain("x "), address("agent-hosts@example.com")],
  },
  {
    markdown: "> x agent&#45;hosts@example.com",
    runs: [plain("x "), address("agent-hosts@example.com")],
  },
  {
    markdown: "| a |\n| :-- |\n| x agent&#45;hosts@example.com |",
    runs: [plain("a"), plain("x "), address("agent-hosts@example.com")],
  },
];

/** The rows the rule leaves alone: each read this way before it too. */
const CONTROL_ROWS: readonly Row[] = [
  {
    // R4's example: the address alone is linked whole.
    markdown: "Grant act-as on agent-hosts@theorem-research.iam.gserviceaccount.com.",
    runs: [
      plain("Grant act-as on "),
      address("agent-hosts@theorem-research.iam.gserviceaccount.com"),
      plain("."),
    ],
  },
  {
    // No reader links an address right after a `/`, and no `-`, `.`, `+` or `_` stands in it.
    markdown: "serviceAccounts/agenthosts@example.com",
    runs: [plain("serviceAccounts/agenthosts@example.com")],
  },
  {
    // The link already starts at its run's start: `é` is no local-part character.
    markdown: "xé-agent@example.com",
    runs: [plain("xé"), address("-agent@example.com")],
  },
  { markdown: "- agent-hosts@example.com", runs: [address("agent-hosts@example.com")] },
  {
    markdown: "- x agent-hosts@example.com",
    runs: [plain("x "), address("agent-hosts@example.com")],
  },
  {
    markdown: ":::callout{}\n- agent-hosts@example.com\n:::",
    runs: [address("agent-hosts@example.com")],
  },
  {
    markdown: ":::callout{}\n- x agent-hosts@example.com\n:::",
    runs: [plain("x "), address("agent-hosts@example.com")],
  },
  {
    // A link the writer wrote is never changed: an explicit link, as a rendering stored before
    // the rule writes the early link it read, ...
    markdown:
      "agent-[hosts@theorem-research.iam.gserviceaccount.com](mailto:hosts@theorem-research.iam.gserviceaccount.com)",
    runs: [plain("agent-"), address("hosts@theorem-research.iam.gserviceaccount.com")],
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
    // transform makes its own, while the label's text keeps its own.
    markdown: "agent-[hosts@example.com][r]\n\n[r]: mailto:hosts@example.com",
    runs: [plain("agent-"), address("hosts@example.com")],
  },
];

/** The three readings of `markdown`: the headless engine's, read-only and not, and the paste's. */
async function readings(markdown: string): Promise<[string, ProseMirrorNode][]> {
  const headless = await createHeadlessProof({ blockSchema });
  const readOnly = await createHeadlessProof({ blockSchema, readOnly: true });
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

/** Asserts `row`'s runs in each pipeline, then that the three read the same tree. */
async function expectRow(row: Row): Promise<void> {
  const [headless, ...others] = await readings(row.markdown);
  for (const [pipeline, doc] of [headless, ...others]) {
    expect(runsOf(doc), pipeline).toEqual([...row.runs]);
  }
  for (const [pipeline, doc] of others) expect(tree(doc), pipeline).toEqual(tree(headless[1]));
}

for (const row of CHANGED_ROWS) {
  test(`changed: ${JSON.stringify(row.markdown)}`, async () => {
    await expectRow(row);
  });
}

for (const row of CONTROL_ROWS) {
  test(`control: ${JSON.stringify(row.markdown)}`, async () => {
    await expectRow(row);
  });
}

/**
 * A document holding email links of every kind, none of them one that starts inside a run of
 * local-part characters: the tokenizer's whole addresses, one whose run starts after a letter
 * outside ASCII and one right after a character reference that ends in no such character, the
 * transform's whole address (an escape stops the tokenizer), a path with no link, the writer's
 * own links, and a URL.
 */
const NO_SUCH_LINK = [
  "# Grant act-as on agent-hosts@theorem-research.iam.gserviceaccount.com.",
  "",
  "xé-agent@example.com, serviceAccounts/agenthosts@example.com and x &#233;agent@example.com.",
  "See agent\\-hosts@example.com.",
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
  expect(mailto).toBe(10);
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
