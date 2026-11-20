import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { QueryClient, QueryClientProvider, type QueryKey, useQuery } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";

import { correction, correctionFeed, DELETION, EDITED_AT } from "../__tests__/correction-feed";
import { messageCorrectionFields } from "../__tests__/message-fixture";
import { CORRECTED_POST_FAMILIES, markCorrectedPost, NO_POST_FAMILIES } from "./corrected-post";
import { useEventFeed } from "./event-feed";
import { whoAmIQuery } from "./queries";
import type { CommentWithTrees, DispatchEvent, MessageRead, MessageWithTrees } from "./types";

// R2 and R3 of dispatch://INFRA-1523, and the architect's ruling of 2026-11-20 07:46Z and 07:48Z:
// a correction writes its own facts into every cached family whose answer holds a comment or an
// issue message, or a copy of one's body or marks (`markCorrectedPost`), so a refetch that fails
// shows no old words. The list of those families was made by hand and missed two (test round
// 43), so it is derived here: the families the web app's source keys a query under, read as
// census query C-Q1 reads them (class C of the surface census), are each written
// (`CORRECTED_POST_FAMILIES`) or listed as holding no post with why (`NO_POST_FAMILIES`), and
// each listed family is still keyed somewhere.

/** The web app's source: `web/src`. */
const SOURCE_ROOT = join(import.meta.dir, "..");

/** Every `.ts` and `.tsx` file under `directory` but the tests, as C-Q1's pathspecs take them:
 *  no `*.test.ts`, no `*.test.tsx` and nothing under a `__tests__` directory. */
function sourceFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) return entry.name === "__tests__" ? [] : sourceFiles(path);
    return /\.tsx?$/u.test(entry.name) && !/\.test\.tsx?$/u.test(entry.name) ? [path] : [];
  });
}

/** One source file, by its path from `web/src`, with its text. */
interface Source {
  readonly path: string;
  readonly text: string;
}

const SOURCES: readonly Source[] = sourceFiles(SOURCE_ROOT).map((path) => ({
  path: relative(SOURCE_ROOT, path),
  text: readFileSync(path, "utf8"),
}));

/** C-Q1's own expression, `queryKey: *\[ *"[a-zA-Z_-]+"( *, *"[a-zA-Z_-]+")?`, held at the
 *  `queryKey:` it is tried at. */
const LITERAL_KEY = /queryKey: *\[ *"([a-zA-Z_-]+)"(?: *, *"([a-zA-Z_-]+)")?/uy;

/** A family as `CORRECTED_POST_FAMILIES` names it: the key's first string, and its second where
 *  the key writes one, joined by a space. */
function familyName(first: string, second: string | undefined): string {
  return second === undefined ? first : `${first} ${second}`;
}

/** Where `text[from]`, a quote or a backtick, ends: the index past its closing mark. */
function quotedEnd(text: string, from: number): number {
  const quote = text[from];
  let at = from + 1;
  let braces = 0;
  while (at < text.length) {
    const char = text[at];
    if (char === "\\") {
      at += 2;
      continue;
    }
    if (quote === "`" && char === "$" && text[at + 1] === "{") braces += 1;
    else if (quote === "`" && char === "}" && braces > 0) braces -= 1;
    else if (char === quote && braces === 0) return at + 1;
    at += 1;
  }
  return at;
}

/** The expression that starts at `from`: up to the first `,` or `;` at its own depth, or the
 *  bracket that closes the one it stands in, with its comments left out. */
function expressionAt(text: string, from: number): string {
  let depth = 0;
  let at = from;
  let expression = "";
  while (at < text.length) {
    const char = text[at] ?? "";
    if (char === '"' || char === "'" || char === "`") {
      const end = quotedEnd(text, at);
      expression += text.slice(at, end);
      at = end;
      continue;
    }
    if (char === "/" && text[at + 1] === "/") {
      const end = text.indexOf("\n", at);
      at = end < 0 ? text.length : end;
      continue;
    }
    if (char === "/" && text[at + 1] === "*") {
      const end = text.indexOf("*/", at);
      at = end < 0 ? text.length : end + 2;
      continue;
    }
    if ("([{".includes(char)) depth += 1;
    else if (")]}".includes(char)) {
      if (depth === 0) break;
      depth -= 1;
    } else if ((char === "," || char === ";") && depth === 0) break;
    expression += char;
    at += 1;
  }
  return expression.trim();
}

/** Where the bracket at `open` in `expression` is closed: the index of its closing mark, or -1. */
function closingAt(expression: string, open: number): number {
  let depth = 0;
  for (let at = open; at < expression.length; at += 1) {
    const char = expression[at] ?? "";
    if (char === '"' || char === "'" || char === "`") {
      at = quotedEnd(expression, at) - 1;
      continue;
    }
    if ("([{".includes(char)) depth += 1;
    else if (")]}".includes(char)) {
      depth -= 1;
      if (depth === 0) return at;
    }
  }
  return -1;
}

/** The two branches of `expression` where it is a conditional at its own depth
 *  (`test ? when : otherwise`); undefined for any other expression. */
function conditionalBranches(expression: string): readonly [string, string] | undefined {
  let depth = 0;
  let question = -1;
  let pending = 0;
  for (let at = 0; at < expression.length; at += 1) {
    const char = expression[at] ?? "";
    if (char === '"' || char === "'" || char === "`") {
      at = quotedEnd(expression, at) - 1;
      continue;
    }
    if ("([{".includes(char)) depth += 1;
    else if (")]}".includes(char)) depth -= 1;
    if (depth !== 0) continue;
    // `?.` and `??` are no conditional.
    if (char === "?" && expression[at + 1] !== "." && expression[at + 1] !== "?") {
      if (expression[at - 1] === "?") continue;
      if (question < 0) question = at;
      else pending += 1;
    } else if (char === ":" && question >= 0) {
      if (pending > 0) pending -= 1;
      else return [expression.slice(question + 1, at).trim(), expression.slice(at + 1).trim()];
    }
  }
  return undefined;
}

/** `make`'s answer, worked out the first time it is asked for and kept. */
function once<T>(make: () => T): () => T {
  let made: { readonly value: T } | undefined;
  return () => {
    if (made === undefined) made = { value: make() };
    return made.value;
  };
}

/** A name a file binds with `const` or `let`, or declares as a function, where it binds it. */
interface Binding {
  readonly path: string;
  readonly at: number;
  readonly exported: boolean;
  /** The bound expression, or each `return` of a function's body, read from the source the first
   *  time a key is resolved through the binding. */
  readonly expressions: () => readonly string[];
}

/** Every `const`, `let` and `function` of the source, by the name it binds. */
const BINDINGS: ReadonlyMap<string, readonly Binding[]> = (() => {
  const bindings = new Map<string, Binding[]>();
  const bind = (name: string, binding: Binding) => {
    const named = bindings.get(name);
    if (named === undefined) bindings.set(name, [binding]);
    else named.push(binding);
  };
  for (const { path, text } of SOURCES) {
    for (const found of text.matchAll(
      /(export\s+)?(?:const|let)\s+([a-zA-Z_$][\w$]*)\s*(?::[^=;]+?)?=(?![=>])\s*/gu
    )) {
      const from = (found.index ?? 0) + found[0].length;
      bind(found[2] ?? "", {
        at: found.index ?? 0,
        exported: found[1] !== undefined,
        expressions: once(() => [expressionAt(text, from)]),
        path,
      });
    }
    for (const found of text.matchAll(/(export\s+)?function\s+([a-zA-Z_$][\w$]*)\s*[<(]/gu)) {
      const start = text.indexOf("{", text.indexOf(")", found.index ?? 0));
      bind(found[2] ?? "", {
        at: found.index ?? 0,
        exported: found[1] !== undefined,
        expressions: once(() => {
          const body = start < 0 ? "" : `{${expressionAt(text, start + 1)}}`;
          return [...body.matchAll(/\breturn\s+/gu)].map((one) =>
            expressionAt(body, (one.index ?? 0) + one[0].length)
          );
        }),
        path,
      });
    }
  }
  return bindings;
})();

/** The bindings `name` reads at `at` in `path`: the file's own last one before it, else the file's
 *  first, else every exported one, as an import names it. */
function bindingsOf(name: string, path: string, at: number): readonly Binding[] {
  const all = BINDINGS.get(name) ?? [];
  const own = all.filter((binding) => binding.path === path);
  const before = own.filter((binding) => binding.at < at).at(-1);
  if (before !== undefined) return [before];
  if (own[0] !== undefined) return [own[0]];
  return all.filter((binding) => binding.exported);
}

/** What a computed key resolves to: the families it can name, or a key another site keys a query
 *  under (`someQuery(…).queryKey`), which the test counts there, or a type, where the colon
 *  annotates a parameter or a field and keys nothing. */
type Resolved = readonly string[] | "named elsewhere" | "a type";

/** The families `expression`, a key as written at `at` in `path`, names; undefined where no rule
 *  here reads it. */
function keyFamilies(
  expression: string,
  path: string,
  at: number,
  seen: ReadonlySet<string> = new Set()
): Resolved | undefined {
  const bare = expression.replace(/\s+(?:as\s+const|satisfies\s[\s\S]+)$/u, "").trim();
  const branches = conditionalBranches(bare);
  if (branches !== undefined) {
    const each = branches.map((branch) => keyFamilies(branch, path, at, seen));
    if (each.some((one) => one === undefined)) return undefined;
    return each.flatMap((one) => (Array.isArray(one) ? one : []));
  }
  if (bare.startsWith("(") && closingAt(bare, 0) === bare.length - 1) {
    return keyFamilies(bare.slice(1, -1), path, at, seen);
  }
  const arrow = /^(?:\([^)]*\)|[a-zA-Z_$][\w$]*)\s*(?::[^=]+?)?=>\s*/u.exec(bare);
  if (arrow !== null) {
    const body = bare.slice(arrow[0].length);
    if (!body.startsWith("{")) return keyFamilies(body, path, at, seen);
    const returns = [...body.matchAll(/\breturn\s+/gu)].map((one) =>
      keyFamilies(expressionAt(body, (one.index ?? 0) + one[0].length), path, at, seen)
    );
    if (returns.length === 0 || returns.some((one) => one === undefined)) return undefined;
    return returns.flatMap((one) => (Array.isArray(one) ? one : []));
  }
  if (bare.startsWith("[")) {
    const head = /^\[\s*"([a-zA-Z_-]+)"(?:\s*,\s*"([a-zA-Z_-]+)")?/u.exec(bare);
    if (head !== null) return [familyName(head[1] ?? "", head[2])];
    const spread = /^\[\s*\.\.\./u.exec(bare);
    return spread === null
      ? undefined
      : keyFamilies(expressionAt(bare, spread[0].length), path, at, seen);
  }
  if (/\.queryKey$/u.test(bare)) return "named elsewhere";
  const name = /^[a-zA-Z_$][\w$]*$/u.exec(bare)?.[0];
  const called = /^([a-zA-Z_$][\w$]*)\s*\(/u.exec(bare);
  const bound =
    name ??
    (called !== null && closingAt(bare, called[0].length - 1) === bare.length - 1
      ? called[1]
      : undefined);
  if (bound === undefined) return undefined;
  if (name !== undefined && /^[A-Z]/u.test(name)) return "a type";
  const bindings = bindingsOf(bound, path, at);
  if (bindings.length === 0) return undefined;
  const families: string[] = [];
  for (const binding of bindings) {
    const key = `${binding.path}:${binding.at}`;
    if (seen.has(key)) return undefined;
    for (const bindingExpression of binding.expressions()) {
      const resolved = keyFamilies(
        bindingExpression,
        binding.path,
        binding.at,
        new Set([...seen, key])
      );
      if (resolved === undefined) return undefined;
      if (Array.isArray(resolved)) families.push(...resolved);
    }
  }
  return families;
}

/** One place the source keys a query, by its file and line. */
interface KeySite {
  readonly where: string;
  readonly families: readonly string[];
}

/** Each `queryKey:` of the source: C-Q1's literal families, and each computed key's (C-Q1's
 *  second command, `queryKey: *[^\[ ]`, and the keys neither of its commands reads: a literal
 *  spread from another key, `queryKey: [...myRepliesKey, "count"]`, or one whose array opens on
 *  the next line). A computed key in a comment is no key. */
function keySites(): { readonly sites: KeySite[]; readonly unread: string[] } {
  const sites: KeySite[] = [];
  const unread: string[] = [];
  for (const { path, text } of SOURCES) {
    for (const found of text.matchAll(/queryKey:/gu)) {
      const at = found.index ?? 0;
      const line = text.slice(0, at).split("\n").length;
      const where = `${path}:${line}`;
      LITERAL_KEY.lastIndex = at;
      const literal = LITERAL_KEY.exec(text);
      if (literal !== null) {
        sites.push({ families: [familyName(literal[1] ?? "", literal[2])], where });
        continue;
      }
      const lineStart = text.lastIndexOf("\n", at) + 1;
      if (/\/\/|^\s*\*/u.test(text.slice(lineStart, at))) continue;
      const expression = expressionAt(text, at + found[0].length);
      const resolved = keyFamilies(expression, path, at);
      if (resolved === undefined) unread.push(`${where}: queryKey: ${expression}`);
      else if (Array.isArray(resolved)) sites.push({ families: resolved, where });
    }
  }
  return { sites, unread };
}

const { sites: KEY_SITES, unread: UNREAD_KEYS } = keySites();

/** Every family the source keys a query under, with the first place it does. */
const SOURCE_FAMILIES: ReadonlyMap<string, string> = new Map(
  KEY_SITES.flatMap(({ families, where }) =>
    families.map((family) => [family, where] as const)
  ).reverse()
);

test("every computed query key resolves to the families it names", () => {
  expect(
    UNREAD_KEYS.map(
      (site) => `${site}: write the key as a literal array, or as a name this test resolves`
    )
  ).toEqual([]);
  // The positive control: the reader finds C-Q1's families and the computed ones C-Q1 misses.
  expect(SOURCE_FAMILIES.has("issue-messages")).toBe(true);
  expect(SOURCE_FAMILIES.has("me replies")).toBe(true);
});

test("every query family the source keys is written by a correction or listed as holding no post", () => {
  const unlisted = [...SOURCE_FAMILIES]
    .filter(
      ([family]) =>
        !Object.hasOwn(CORRECTED_POST_FAMILIES, family) && !Object.hasOwn(NO_POST_FAMILIES, family)
    )
    .map(
      ([family, where]) =>
        `query family "${family}" (${where}) is in neither list: write it in CORRECTED_POST_FAMILIES, or list it in NO_POST_FAMILIES with why it holds no post`
    );
  expect(unlisted).toEqual([]);
});

test("every family either list names is still keyed in the source, and in one list alone", () => {
  const listed = [...Object.keys(CORRECTED_POST_FAMILIES), ...Object.keys(NO_POST_FAMILIES)];
  expect(
    listed
      .filter((family) => !SOURCE_FAMILIES.has(family))
      .map((family) => `listed family "${family}" no longer appears in the source`)
  ).toEqual([]);
  expect(
    Object.keys(CORRECTED_POST_FAMILIES).filter((family) => Object.hasOwn(NO_POST_FAMILIES, family))
  ).toEqual([]);
  for (const [family, reason] of Object.entries(NO_POST_FAMILIES)) {
    expect(reason.trim().length, family).toBeGreaterThan(0);
  }
});

// Each written family has a row that proves the write: here below, or one of these tests, which
// already drive the family's correction through a refetch that fails.
const WRITTEN_ELSEWHERE: Readonly<
  Record<string, { readonly file: string; readonly test: string }>
> = {
  "ask-thread": {
    file: "features/inbox/AskThread.test.tsx",
    test: "a thread whose refetch fails after a reply's edit and delete reads the reply as edited, then deleted",
  },
  // The document read by its slug holds no post; it lists the document's versions, whose name a
  // comment's delete renames (`renamed_version`).
  "artifact-ref": {
    file: "api/corrected-post-versions.test.ts",
    test: "a version a comment's delete renamed takes its new name in a project document's read by its slug, whose refetch fails",
  },
  comment: {
    file: "features/refs/reference-target.test.ts",
    test: "a post's reference whose refetch fails after its edit and delete reads as edited, then deleted",
  },
  comments: {
    file: "features/margin/PinnedTab.test.tsx",
    test: "a pinned comment's and message's cards whose refetch fails read as edited, then deleted",
  },
  // The issue's details hold no post; they list each document's versions, whose name a comment's
  // delete renames (`renamed_version`).
  issue: {
    file: "api/corrected-post-versions.test.ts",
    test: "a version a comment's delete renamed takes its new name in the issue's details, whose refetch fails",
  },
  "issue-message": {
    file: "features/refs/reference-target.test.ts",
    test: "a post's reference whose refetch fails after its edit and delete reads as edited, then deleted",
  },
  "issue-messages": {
    file: "api/corrected-post-messages.test.ts",
    test: "a message's delete writes its mark into the issue's message list, as into the pinned copy",
  },
  "me replies": {
    file: "features/inbox/useInboxThreadRows.test.tsx",
    test: "a thread row whose refetch fails after its post's edit and delete reads as edited, then deleted",
  },
  "reference-targets": {
    file: "api/correction-post-addresses.test.ts",
    test: "a delete writes its mark into each kept member naming the post, and no other",
  },
  search: {
    file: "features/search/SearchPalette.test.tsx",
    test: "a post's hit whose refetch fails after its edit and delete carries the edited mark, then the deleted line",
  },
};

const POST = "5e6f7a8b-9c0d-4e1f-8a2b-3c4d5e6f7a8b";
const OLD_WORDS = "Hold the deploy until Friday.";

/** The post as a comment, or a message, as a read answers it before its correction. */
function commentOf(id = POST, more: Partial<CommentWithTrees> = {}): CommentWithTrees {
  return {
    anchor: null,
    author: { id: "alice", kind: "user" },
    body: OLD_WORDS,
    created_at: "2026-11-19T00:00:00Z",
    deleted_at: null,
    deleted_by: null,
    edited_at: null,
    id,
    issue_key: "CORE-1",
    reply_block: null,
    reply_to: null,
    suggestion: null,
    ...more,
  } as CommentWithTrees;
}

function messageOf(): MessageWithTrees {
  return {
    author: { id: "alice", kind: "user" },
    body: OLD_WORDS,
    broadcast_id: null,
    created_at: "2026-11-19T00:00:00Z",
    deliveries: [],
    id: POST,
    in_reply_to: null,
    issue_key: "CORE-1",
    reply_block: null,
    target: "session:019a1c01-0d7b-7a2a-8e93-000000000a01",
    ...messageCorrectionFields(),
  };
}

/** What a row reads of the post's copy an answer holds. */
interface CopyMarks {
  readonly deleted_at?: string | null;
  readonly edited_at?: string | null;
}

/** One written family's cached answer holding a copy of the post, and where in it the copy is. */
interface WriteRow {
  readonly family: string;
  readonly kind: "comment" | "message";
  readonly key: QueryKey;
  readonly answer: unknown;
  readonly copies: (answer: unknown) => readonly CopyMarks[];
}

const at = (value: unknown, ...path: (string | number)[]): unknown =>
  path.reduce<unknown>(
    (held, step) =>
      typeof held === "object" && held !== null ? Reflect.get(held, step) : undefined,
    value
  );
const marksAt = (value: unknown, ...path: (string | number)[]): CopyMarks =>
  (at(value, ...path) ?? {}) as CopyMarks;

const commentEvent: DispatchEvent = {
  actor: { id: "alice", kind: "user" },
  artifact_id: null,
  created_at: "2026-11-19T00:00:00Z",
  id: 7,
  issue_key: "CORE-1",
  notify: true,
  payload: { ...commentOf(), body_truncated: false },
  project: "CORE",
  seq: 7,
  trees: {},
  type: "comment.created",
} as unknown as DispatchEvent;

const ROWS: readonly WriteRow[] = [
  {
    answer: { ask: { id: "ask-1" }, replies: [commentOf()] },
    copies: (answer) => [marksAt(answer, "replies", 0)],
    family: "ask",
    key: ["ask", "ask-1"],
    kind: "comment",
  },
  {
    answer: [{ id: "ask-1", thread: { replies: [commentOf()] } }],
    copies: (answer) => [marksAt(answer, 0, "thread", "replies", 0)],
    family: "asks",
    key: ["asks", "CORE-1", "thread"],
    kind: "comment",
  },
  {
    answer: [{ id: "ask-1", thread: { replies: [commentOf()] } }],
    copies: (answer) => [marksAt(answer, 0, "thread", "replies", 0)],
    family: "inbox",
    key: ["inbox"],
    kind: "comment",
  },
  {
    answer: [commentOf()],
    copies: (answer) => [marksAt(answer, 0)],
    family: "artifact",
    key: ["artifact", "doc-1", "comments"],
    kind: "comment",
  },
  {
    answer: [{ id: "ask-2", thread: { replies: [commentOf()] } }],
    copies: (answer) => [marksAt(answer, 0, "thread", "replies", 0)],
    family: "artifact",
    key: ["artifact", "doc-1", "asks"],
    kind: "comment",
  },
  {
    answer: { pageParams: [0], pages: [[commentEvent]] },
    copies: (answer) => [marksAt(answer, "pages", 0, 0, "payload")],
    family: "events",
    key: ["events", "CORE-1"],
    kind: "comment",
  },
  {
    answer: [{ message: messageOf(), replies: [] }],
    copies: (answer) => [marksAt(answer, 0, "message")],
    family: "agents",
    key: ["agents", "019a1c01-0d7b-7a2a-8e93-000000000a01", "messages"],
    kind: "message",
  },
  {
    answer: {
      pageParams: [0],
      pages: [
        {
          limit: 20,
          offset: 0,
          rows: [{ ask_id: "ask-1", kind: "reply", reply: { body: OLD_WORDS, id: POST } }],
          total: 1,
        },
      ],
    },
    copies: (answer) => [marksAt(answer, "pages", 0, "rows", 0, "reply")],
    family: "me answers",
    key: ["me", "answers"],
    kind: "comment",
  },
  // The Referenced by panel's list (`ReferencedBy.tsx`), which it draws beside "Referenced by
  // unavailable" when its read fails: an edge from the comment, and one from a block of it.
  {
    answer: {
      edges: [
        {
          created_at: "2026-11-19T00:00:00Z",
          direction: "in",
          excerpt: { text: OLD_WORDS },
          kind: "mentions",
          node: { id: POST, kind: "comment" },
          source_seq: 7,
        },
        {
          created_at: "2026-11-19T00:00:00Z",
          direction: "in",
          excerpt: { block_id: "b2", text: OLD_WORDS },
          kind: "mentions",
          node: {
            block_id: "b2",
            id: `comment/${POST}#b2`,
            kind: "block",
            parent: { id: POST, kind: "comment" },
          },
          source_seq: 8,
        },
      ],
      node: { id: "CORE-1", kind: "issue" },
    },
    copies: (answer) => [marksAt(answer, "edges", 0, "node"), marksAt(answer, "edges", 1, "node")],
    family: "references to",
    key: ["references", "to", "dispatch://CORE-1"],
    kind: "comment",
  },
];

test("every family a correction writes has a row that proves the write", () => {
  const here = new Set(ROWS.map((row) => row.family));
  expect(
    Object.keys(CORRECTED_POST_FAMILIES).filter(
      (family) => !here.has(family) && !Object.hasOwn(WRITTEN_ELSEWHERE, family)
    )
  ).toEqual([]);
  for (const [family, { file, test: name }] of Object.entries(WRITTEN_ELSEWHERE)) {
    expect(readFileSync(join(SOURCE_ROOT, file), "utf8"), family).toContain(name);
  }
});

for (const row of ROWS) {
  test(`a delete writes its mark into ${row.family} (${JSON.stringify(row.key)}), and keeps none of the post's words`, () => {
    const client = new QueryClient();
    client.setQueryData(row.key, row.answer);
    expect(JSON.stringify(client.getQueryData(row.key))).toContain(OLD_WORDS);
    markCorrectedPost(client, correction(row.kind, POST, false));
    const answer = client.getQueryData(row.key);
    expect(row.copies(answer).map((copy) => copy.deleted_at)).toEqual(
      row.copies(row.answer).map(() => DELETION.deleted_at)
    );
    expect(JSON.stringify(answer)).not.toContain(OLD_WORDS);
  });

  test(`an edit writes its time into ${row.family} (${JSON.stringify(row.key)})`, () => {
    const client = new QueryClient();
    client.setQueryData(row.key, row.answer);
    markCorrectedPost(client, correction(row.kind, POST, true));
    const answer = client.getQueryData(row.key);
    expect(row.copies(answer).map((copy) => copy.edited_at)).toEqual(
      row.copies(row.answer).map(() => EDITED_AT)
    );
    expect(JSON.stringify(answer)).toContain(OLD_WORDS);
  });
}

// The architect's ruling of 2026-11-20 07:52Z: an answer composed before a correction cannot take
// the correction's mark out. A read already out when the delete arrives is not cancelled
// (`query-refresh.ts`), so its answer lands after the feed wrote the delete's mark; the feed then
// writes the delete's facts into it again, and the refetch the event owes the read, which fails
// here, leaves the mark where it is.
test("an answer a read out at a delete lands with the post's words keeps the delete's mark, and no body, when the refetch after it fails", async () => {
  const list = ["issue-messages", "CORE-1"];
  const read: MessageRead = { message: messageOf(), replies: [] };
  const held = Promise.withResolvers<MessageRead[]>();
  let reads = 0;
  const fetchList = (): Promise<MessageRead[]> => {
    reads += 1;
    if (reads === 1) return Promise.resolve([read]);
    if (reads === 2) return held.promise;
    return Promise.reject(new Error("the read failed"));
  };
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(whoAmIQuery().queryKey, { event_head: "0", kind: "user", login: "alice" });
  const feed = correctionFeed(client);
  const hook = renderHook(
    () => {
      useEventFeed();
      return useQuery({ queryFn: fetchList, queryKey: list });
    },
    {
      wrapper: ({ children }: { children: ReactNode }) =>
        createElement(QueryClientProvider, { client }, children),
    }
  );
  const message = () => client.getQueryData<MessageRead[]>(list)?.[0]?.message;

  try {
    await waitFor(() => expect(hook.result.current.data?.[0]?.message.body).toBe(OLD_WORDS));
    feed.start();
    void hook.result.current.refetch();
    await waitFor(() => expect(reads).toBe(2));
    // The answer the held read composed before the delete lands once the feed has written the
    // delete's mark.
    const landAfterTheMark = client.getQueryCache().subscribe(() => {
      if (message()?.deleted_at !== DELETION.deleted_at) return;
      landAfterTheMark();
      held.resolve([read]);
    });
    await feed.deliver(correction("message", POST, false), [list]);

    expect(reads).toBe(3);
    expect({ body: message()?.body, deleted_at: message()?.deleted_at }).toEqual({
      body: "",
      deleted_at: DELETION.deleted_at,
    });
  } finally {
    hook.unmount();
    feed.restore();
    client.clear();
  }
});

// The bound: a correction is kept for the reads out at its arrival and no longer. Once the one
// read out has landed, an answer the cache is handed is left as it is handed.
test("a correction is kept only while a read out at its arrival is out", async () => {
  const list = ["issue-messages", "CORE-1"];
  const read: MessageRead = { message: messageOf(), replies: [] };
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(list, [read]);
  const held = Promise.withResolvers<MessageRead[]>();
  const out = client.fetchQuery({ queryFn: () => held.promise, queryKey: list });
  expect(client.getQueryState(list)?.fetchStatus).toBe("fetching");
  markCorrectedPost(client, correction("message", POST, false));
  held.resolve([read]);
  await out;
  expect(client.getQueryData<MessageRead[]>(list)?.[0]?.message.body).toBe("");

  client.setQueryData(list, [read]);
  expect(client.getQueryData<MessageRead[]>(list)?.[0]?.message.body).toBe(OLD_WORDS);
  client.clear();
});

// A kept edit is written again into each answer that lands while a read out at its arrival is
// out, and an answer composed after a later edit, whose event has not arrived yet, carries that
// edit's time: the kept edit's older time never moves it back. Its delete's facts would apply
// whatever the answer said.
test("an answer that lands carrying a later edit than a kept one keeps its own edit's time", async () => {
  const list = ["issue-messages", "CORE-1"];
  const later = "2026-11-20T00:45:00.5Z";
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(list, [{ message: messageOf(), replies: [] }]);
  const held = Promise.withResolvers<MessageRead[]>();
  const out = client.fetchQuery({ queryFn: () => held.promise, queryKey: list });
  markCorrectedPost(client, correction("message", POST, true));
  expect(client.getQueryData<MessageRead[]>(list)?.[0]?.message.edited_at).toBe(EDITED_AT);
  held.resolve([{ message: { ...messageOf(), edited_at: later }, replies: [] }]);
  await out;
  expect(client.getQueryData<MessageRead[]>(list)?.[0]?.message.edited_at).toBe(later);
  client.clear();
});

// A delete empties the anchor quote of the post's own cached events where it emptied the
// comment's (`markedEvent`), as the server's read of the log does (`attachPostMarks`): an insert
// suggestion's quote is the text it put in the document, the comment's own words, which the delete
// clears (`clearDeletedPostBody`), and the delete's own event carries the anchor with that quote
// empty. A replace suggestion's quote is the document's own text, which its delete leaves.
test("a delete empties a cached event's anchor quote where its own event's is empty, and keeps a replace suggestion's", () => {
  const anchor = {
    artifact_id: "doc-1",
    block_id: null,
    mark_id: "mark-1",
    orphaned: false,
    version: 1,
  };
  const pinned = ["events", "CORE-1", "margin-pinned", [POST]];
  const quoteAfter = (cached: string, deleted: string) => {
    const client = new QueryClient();
    client.setQueryData(pinned, [
      {
        ...commentEvent,
        payload: { ...commentEvent.payload, anchor: { ...anchor, quote: cached } },
      },
    ]);
    markCorrectedPost(
      client,
      correction("comment", POST, false, { anchor: { ...anchor, quote: deleted } })
    );
    const payload = at(client.getQueryData(pinned), 0, "payload");
    return { deleted_at: at(payload, "deleted_at"), quote: at(payload, "anchor", "quote") };
  };
  expect({
    insert: quoteAfter("Friday at noon", ""),
    replace: quoteAfter("the old wording", "the old wording"),
  }).toEqual({
    insert: { deleted_at: DELETION.deleted_at, quote: "" },
    replace: { deleted_at: DELETION.deleted_at, quote: "the old wording" },
  });
});
