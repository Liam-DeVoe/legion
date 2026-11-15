import { describe, expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import {
  DISPATCH_READING_SURFACES,
  DISPATCH_WRITING_BOXES,
  type DispatchReadingSurfaceRow,
  type DispatchWritingBoxRow,
} from "@legion/contracts";
import * as ts from "typescript";

/**
 * The two lists say what the framework covers: `DISPATCH_WRITING_BOXES` every box a person
 * writes prose in, `DISPATCH_READING_SURFACES` every surface a reader reads a record on. Both
 * are hand-written, and a hand-written list goes stale the first time someone adds a box or a
 * surface without opening it - which compiles, passes every other test, and leaves the browser
 * rows that walk the lists covering one surface fewer than anyone thinks.
 *
 * This test is what stops that. It reads the dashboard's own source for the six constructs a
 * box or a surface is made of and fails when one of them is in a file no row names, and it
 * reads each row back against the source so a row whose box or surface is gone fails too.
 *
 * It walks `packages/proof-editor/src` beside the dashboard because the document body is one of
 * the three hosts that carry the picker: a box added inside the editor package must not be
 * invisible here.
 */

/** The repository root, from this file: `features/refs` -> `src` -> `web` -> `dispatch` ->
 *  `packages` -> the root. Every path below is written from there, which is how both lists
 *  write the files of a row. */
const REPO_ROOT = join(import.meta.dir, "..", "..", "..", "..", "..", "..");

/** What the walk reads. The editor package is here for the reason the header gives. */
const SCANNED_ROOTS = ["packages/dispatch/web/src", "packages/proof-editor/src"];

/** The shared field's own file. Its one `<textarea>` is the field itself - the thing every
 *  covered box mounts - rather than a box of its own, so the walk reads no box out of it. */
const SHARED_FIELD = "packages/dispatch/web/src/features/conversation/MentionField.tsx";

/** One construct the walk looks for, and the words a failure calls it by. */
interface Construct {
  readonly name: string;
  readonly pattern: RegExp;
}

/** The two editors that host the picker in their own way: a document's body through
 *  `createProofEditor`, an agent's live conversation through assistant-ui's composer input. A box
 *  made of one picks its row beside the mount rather than on it (the document body's picker, the
 *  live conversation's triggers). The pattern skips `createProofEditor`'s own declaration in the
 *  editor package, which is no mount. */
const EDITOR_MOUNT: Construct = {
  name: "an editor mount",
  pattern: /(?<!function )\bcreateProofEditor\(|<ComposerPrimitive\.Input\b/,
};

/** The elements that mount the shared field, bare or through the composer that dresses it. A
 *  mount picks the box's row on its own attributes (`picksOf`). */
const SHARED_FIELD_ELEMENTS: readonly string[] = ["MentionField", "MentionComposer"];

/** What a box is made of: the plain textarea of a box that takes no picker, an editor that
 *  hosts the picker, and a mount of the shared field. */
const WRITING_CONSTRUCTS: readonly Construct[] = [
  { name: "a textarea element", pattern: /<textarea\b/ },
  EDITOR_MOUNT,
  {
    name: "a mount of the shared field",
    pattern: new RegExp(SHARED_FIELD_ELEMENTS.map((element) => `<${element}\\b`).join("|")),
  },
];

/** What a reading surface is made of: a mount of the one renderer, as a body or as a line or
 *  two of one, and a mount of a reference card - the cards under a body that is nothing but
 *  references, and the hover card the shell portals once for the whole app. */
const READING_CONSTRUCTS: readonly Construct[] = [
  {
    name: "a mount of the body or the preview renderer",
    pattern: /<MarkdownBody\b|<MarkdownPreview\b/,
  },
  // `<UnfurlRead` is the mount the renderer itself makes, with the readings it already has.
  { name: "a mount of the reference card", pattern: /<Unfurl|<RefPreviewHost\b/ },
];

/** A reader this change folded into the shared one. Nothing may import it back: a second reader
 *  of a stored text is the drift both lists exist to stop. */
interface DeletedReader {
  /** What it read, for the failure to say. */
  readonly what: string;
  /** The module it lived in, as an importer would write the end of the specifier. */
  readonly module?: string;
  /** The names it exported, each a word no source file writes now. */
  readonly names: readonly string[];
}

const DELETED_READERS: readonly DeletedReader[] = [
  {
    module: "inbox/question-line",
    names: ["questionLine"],
    what: "the second reader of an ask's question line",
  },
  {
    names: ["composerReferences", "ComposerReference"],
    what: "the composer's own reader of a draft's references",
  },
  { module: "refs/ReferencePicker", names: [], what: "the Ctrl+K reference dialog" },
];

// ------------------------------------------------------------------------------------------
// The walk
// ------------------------------------------------------------------------------------------

function collectSourceFiles(root: string): string[] {
  const files: string[] = [];
  const walk = (relative: string): void => {
    for (const entry of readdirSync(join(REPO_ROOT, relative), { withFileTypes: true })) {
      const child = `${relative}/${entry.name}`;
      if (entry.isDirectory()) {
        // The suite's own harnesses stand in for the shell - one of them mounts the hover card
        // as `app.tsx` does - so they are the suite rather than a surface a reader reaches.
        if (entry.name !== "__tests__") {
          walk(child);
        }
        continue;
      }
      const isSource = entry.name.endsWith(".ts") || entry.name.endsWith(".tsx");
      const isTest = entry.name.endsWith(".test.ts") || entry.name.endsWith(".test.tsx");
      if (isSource && !isTest) {
        files.push(child);
      }
    }
  };
  walk(root);
  return files;
}

const SCANNED_FILES = SCANNED_ROOTS.flatMap(collectSourceFiles).sort();

/** What the scanner is reading at one character. */
type Mode = "block" | "code" | "line" | "quoted";

/**
 * One file's lines with every comment of `text` blanked, so prose that names a construct - a
 * `<textarea>` an interface documents, a `createProofEditor()` an explanation mentions - is not
 * read as a mount of it. Blanks rather than drops, so a line number below is the file's own.
 *
 * It reads character by character because a comment's opening characters are also ordinary text
 * inside a string: `path="/issues/:key/*"` in `app.tsx` opens no comment, and a regex that
 * swallowed everything after it to the next comment close would quietly stop this test from
 * seeing a hundred lines of the shell. A string's own characters are left as they are: no
 * construct is written in one, and the import check below reads module specifiers.
 */
function sourceLines(text: string): string[] {
  const chars = [...text];
  let mode: Mode = "code";
  let quote = "";
  let at = 0;
  while (at < chars.length) {
    const char = chars[at] as string;
    const pair = `${char}${chars[at + 1] ?? ""}`;
    if (mode === "code") {
      if (pair === "//" || pair === "/*") {
        mode = pair === "//" ? "line" : "block";
        chars[at] = " ";
        chars[at + 1] = " ";
        at += 2;
        continue;
      }
      if (char === '"' || char === "'" || char === "`") {
        mode = "quoted";
        quote = char;
      }
    } else if (mode === "line") {
      if (char === "\n") {
        mode = "code";
      } else {
        chars[at] = " ";
      }
    } else if (mode === "block") {
      if (pair === "*/") {
        mode = "code";
        chars[at] = " ";
        chars[at + 1] = " ";
        at += 2;
        continue;
      }
      if (char !== "\n") {
        chars[at] = " ";
      }
    } else if (char === "\\") {
      at += 2;
      continue;
    } else if (char === quote || (char === "\n" && quote !== "`")) {
      // Only a template literal holds a newline, so one ends a `'` that was an apostrophe in
      // text rather than the opening of a string.
      mode = "code";
    }
    at += 1;
  }
  return chars.join("").split("\n");
}

/** Each scanned file's own text, read once: the row checks below read a word anywhere in it,
 *  the walk reads it with its comments blanked, and the pick reader parses it (`picksOf`). */
const TEXT_BY_FILE = new Map(
  SCANNED_FILES.map((file) => [file, readFileSync(join(REPO_ROOT, file), "utf8")] as const)
);

const LINES_BY_FILE = new Map(
  SCANNED_FILES.map((file) => [file, sourceLines(TEXT_BY_FILE.get(file) ?? "")] as const)
);

/** Whether `file` holds any of `constructs`. */
function holdsAny(file: string, constructs: readonly Construct[]): boolean {
  return (
    LINES_BY_FILE.get(file)?.some((line) =>
      constructs.some((construct) => construct.pattern.test(line))
    ) === true
  );
}

/** Whether `file` writes `word`, comments included: a row's id is written at its site as a
 *  pointer to the row, and a pointer is a comment. */
function writesWord(file: string, word: string): boolean {
  return TEXT_BY_FILE.get(file)?.includes(word) === true;
}

/** What one file's code picks of the writing list, read from its syntax tree, so a comment, a
 *  string anywhere else - a mutation key, a test id - and an attribute of another element are
 *  no pick. */
interface Picks {
  /** The rows its shared-field mounts pick on their own attributes: `box="<id>"`,
   *  `writing={dispatchWritingBox("<id>", ...)}`, or `writing={NAME}` where NAME is a `const` of
   *  the file initialised with that call. */
  readonly mounts: ReadonlySet<string>;
  /** Every row it calls `dispatchWritingBox("<id>", ...)` with, anywhere: an editor hosts the
   *  picker beside its mount, so a box made of one picks its row there. */
  readonly calls: ReadonlySet<string>;
}

/** The row a `dispatchWritingBox("<id>", ...)` call picks; `undefined` for any other node,
 *  a call whose row is no string literal included. */
function writingBoxCall(node: ts.Node | undefined): string | undefined {
  if (
    node === undefined ||
    !ts.isCallExpression(node) ||
    !ts.isIdentifier(node.expression) ||
    node.expression.text !== "dispatchWritingBox"
  ) {
    return undefined;
  }
  const [box] = node.arguments;
  return box !== undefined && ts.isStringLiteral(box) ? box.text : undefined;
}

/** `source`'s picks, parsed as the TSX it is: the parser ends a string, a comment and a tag
 *  where TypeScript does, escapes included, so a mount's attributes are its own and no more. */
function picksOf(file: string, source: string): Picks {
  const tree = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const calls = new Set<string>();
  const held = new Map<string, string>();
  const mounts: ts.JsxOpeningLikeElement[] = [];
  const visit = (node: ts.Node): void => {
    const call = writingBoxCall(node);
    if (call !== undefined) {
      calls.add(call);
    }
    if (
      ts.isVariableDeclaration(node) &&
      ts.isIdentifier(node.name) &&
      (ts.getCombinedNodeFlags(node) & ts.NodeFlags.Const) !== 0
    ) {
      const box = writingBoxCall(node.initializer);
      if (box !== undefined) {
        held.set(node.name.text, box);
      }
    }
    if (
      (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) &&
      SHARED_FIELD_ELEMENTS.includes(node.tagName.getText(tree))
    ) {
      mounts.push(node);
    }
    ts.forEachChild(node, visit);
  };
  visit(tree);
  // The mounts are read once the whole file has been, since a constant one names may be
  // declared below it.
  const picked = new Set<string>();
  for (const mount of mounts) {
    for (const attribute of mount.attributes.properties) {
      if (!ts.isJsxAttribute(attribute) || attribute.initializer === undefined) {
        continue;
      }
      const name = attribute.name.getText(tree);
      const value = attribute.initializer;
      let box: string | undefined;
      if (name === "box" && ts.isStringLiteral(value)) {
        box = value.text;
      } else if (name === "writing" && ts.isJsxExpression(value)) {
        const { expression } = value;
        box =
          expression !== undefined && ts.isIdentifier(expression)
            ? held.get(expression.text)
            : writingBoxCall(expression);
      }
      if (box !== undefined) {
        picked.add(box);
      }
    }
  }
  return { calls, mounts: picked };
}

/** Each scanned file's picks, read the first time a covered row of it asks. */
const PICKS_BY_FILE = new Map<string, Picks>();

/** Whether `file` picks `box`: on a shared-field mount's own attributes, or, where `editor` is
 *  set, in a `dispatchWritingBox("<box>", ...)` call anywhere, since an editor hosts the picker
 *  beside its mount. A mount picking another row stands in for nothing. */
function picksBox(file: string, box: string, editor: boolean): boolean {
  let picks = PICKS_BY_FILE.get(file);
  if (picks === undefined) {
    picks = picksOf(file, TEXT_BY_FILE.get(file) ?? "");
    PICKS_BY_FILE.set(file, picks);
  }
  return picks.mounts.has(box) || (editor && picks.calls.has(box));
}

/** One place the walk found a construct. */
interface Site {
  readonly at: string;
  readonly construct: string;
  readonly file: string;
}

function sitesOf(constructs: readonly Construct[], skipped: ReadonlySet<string>): Site[] {
  const sites: Site[] = [];
  for (const file of SCANNED_FILES) {
    if (skipped.has(file)) {
      continue;
    }
    const lines = LINES_BY_FILE.get(file) ?? [];
    lines.forEach((line, index) => {
      for (const construct of constructs) {
        if (construct.pattern.test(line)) {
          sites.push({ at: `${file}:${index + 1}`, construct: construct.name, file });
        }
      }
    });
  }
  return sites;
}

const WRITING_SITES = sitesOf(WRITING_CONSTRUCTS, new Set([SHARED_FIELD]));
const READING_SITES = sitesOf(READING_CONSTRUCTS, new Set());

const WRITING_FILES = new Set(DISPATCH_WRITING_BOXES.flatMap((row) => row.files));
const READING_FILES = new Set(DISPATCH_READING_SURFACES.flatMap((row) => row.files));

/** The workflow whose `changes` job decides which jobs a pull request runs. */
const CI_WORKFLOW = ".github/workflows/ci.yaml";

/**
 * The globs of `dispatch_references`, the paths filter of `CI_WORKFLOW`'s `changes` job that
 * starts the reference rows on a pull request. The filters are one YAML string inside the
 * workflow's YAML, and a filter may name a group of them through its anchor, so both are parsed
 * and the list is flattened, as dorny/paths-filter flattens it.
 */
function referenceFilter(): Bun.Glob[] {
  const workflow = Bun.YAML.parse(readFileSync(join(REPO_ROOT, CI_WORKFLOW), "utf8")) as {
    jobs?: { changes?: { steps?: { id?: string; with?: { filters?: string } }[] } };
  };
  const filters = workflow.jobs?.changes?.steps?.find((step) => step.id === "filter")?.with
    ?.filters;
  const groups = Bun.YAML.parse(filters ?? "") as Record<string, unknown> | null;
  return [groups?.dispatch_references]
    .flat(Number.POSITIVE_INFINITY)
    .filter((pattern): pattern is string => typeof pattern === "string")
    .map((pattern) => new Bun.Glob(pattern));
}

const REFERENCE_FILTER = referenceFilter();

/** The file, or the file and the list it is missing from: a failure a person can act on
 *  without opening anything first. */
function listing(file: string, named: ReadonlySet<string>, list: string): string {
  return named.has(file) ? file : `${file} is in no row of ${list}`;
}

/** The row's name, or what is wrong with its reason and which files to look in. A row that is
 *  not covered, or that does not render a record, carries one; a row that does carries none,
 *  so a reason cannot sit beside a box or a surface it does not excuse. */
function reasonComplaint(
  label: string,
  covered: boolean,
  reason: string | undefined,
  files: readonly string[]
): string {
  if (covered) {
    return reason === undefined ? label : `${label} needs no reason and carries one`;
  }
  return (reason ?? "") === ""
    ? `${label} is excluded and says no why: ${files.join(", ")}`
    : label;
}

// ------------------------------------------------------------------------------------------
// The walk is not vacuous
// ------------------------------------------------------------------------------------------

describe("the walk reads the dashboard's source", () => {
  test("scans a non-trivial number of source files", () => {
    expect(SCANNED_FILES.length).toBeGreaterThan(100);
  });

  test("reads both packages", () => {
    for (const root of SCANNED_ROOTS) {
      expect(SCANNED_FILES.filter((file) => file.startsWith(`${root}/`)).length).toBeGreaterThan(5);
    }
  });

  test("finds a non-trivial number of boxes and surfaces", () => {
    // A guard on the patterns themselves: were one to stop matching, every check below would
    // pass over nothing at all.
    expect(WRITING_SITES.length).toBeGreaterThan(15);
    expect(READING_SITES.length).toBeGreaterThan(40);
  });

  test("reads the reference rows' filter in ci.yaml", () => {
    // A guard on the reading of the workflow: were it to find no filter, no covered row's file
    // would be in it, and every covered row below would fail for a reason none of them has.
    expect(REFERENCE_FILTER.length).toBeGreaterThan(20);
  });
});

// ------------------------------------------------------------------------------------------
// A mount's pick is read from its own attributes
// ------------------------------------------------------------------------------------------

describe("a mount's pick is read from its own attributes", () => {
  // A string's escaped quote ends neither the string nor the tag, so the element after a mount
  // is never read as part of it.
  const fixture = (field: string): string =>
    [
      "export const Box = () => (",
      "  <>",
      `    <MentionField label={"Quote \\" here"} ${field}body={draft} onChange={type} />`,
      '    <AskCard box="priority-description" />',
      "  </>",
      ");",
    ].join("\n");

  test("a box on the element after a mount is no pick of it", () => {
    expect([...picksOf("fixture.tsx", fixture("")).mounts]).toEqual([]);
  });

  test("a box on the mount itself is its pick", () => {
    expect([...picksOf("fixture.tsx", fixture('box="ask-answer" ')).mounts]).toEqual([
      "ask-answer",
    ]);
  });
});

// ------------------------------------------------------------------------------------------
// Every box and every surface the dashboard draws is in its list
// ------------------------------------------------------------------------------------------

describe("every box the dashboard draws is in the writing list", () => {
  test.each(
    WRITING_SITES.map((site) => [site.at, site.construct, site.file] as const)
  )("%s is %s", (_at, _construct, file) => {
    expect(listing(file, WRITING_FILES, "DISPATCH_WRITING_BOXES")).toBe(file);
  });
});

describe("every surface the dashboard draws is in the reading list", () => {
  test.each(
    READING_SITES.map((site) => [site.at, site.construct, site.file] as const)
  )("%s is %s", (_at, _construct, file) => {
    expect(listing(file, READING_FILES, "DISPATCH_READING_SURFACES")).toBe(file);
  });
});

// ------------------------------------------------------------------------------------------
// Every row is a box or a surface the dashboard still draws
// ------------------------------------------------------------------------------------------

describe("every row of the writing list is a box the dashboard still draws", () => {
  test.each(
    DISPATCH_WRITING_BOXES.map((row) => [`${row.box} writing ${row.write}`, row] as const)
  )("%s", (label: string, row: DispatchWritingBoxRow) => {
    for (const file of row.files) {
      expect(
        LINES_BY_FILE.has(file) ? file : `${file} (named by ${label}) is not a source file`
      ).toBe(file);
    }
    const drawn = row.files.some((file) => holdsAny(file, WRITING_CONSTRUCTS));
    expect(drawn ? label : `${label} names no file that draws a box: ${row.files.join(", ")}`).toBe(
      label
    );
    const named = row.files.some((file) => writesWord(file, row.box));
    expect(named ? label : `no file of ${label} writes its box id: ${row.files.join(", ")}`).toBe(
      label
    );
    if (row.covered) {
      // A covered box takes the picker: a mount of the shared field in a file of it picks the
      // row on its own attributes, or a file of it mounts an editor that hosts the picker and a
      // file of it picks the row beside it (the document body's editor is mounted in one file
      // and its picker picks the row in another). A comment naming the row, a string that is no
      // pick, and a mount picking another row are none of these.
      const editor = row.files.some((file) => holdsAny(file, [EDITOR_MOUNT]));
      const picked = row.files.some((file) => picksBox(file, row.box, editor));
      expect(
        picked ? label : `no mount in a file of ${label} picks its row: ${row.files.join(", ")}`
      ).toBe(label);
      // A pull request runs the reference rows only where it changes a path ci.yaml's
      // `dispatch_references` filter names, so a covered box's file the filter misses is one a
      // pull request can take the picker out of with none of those rows run.
      for (const file of row.files) {
        expect(
          REFERENCE_FILTER.some((glob) => glob.match(file))
            ? file
            : `${file} (named by ${label}) is in no glob of ${CI_WORKFLOW}'s dispatch_references`
        ).toBe(file);
      }
    }
  });

  test.each(
    DISPATCH_WRITING_BOXES.map((row) => [`${row.box} writing ${row.write}`, row] as const)
  )("%s says why it is not covered", (label: string, row: DispatchWritingBoxRow) => {
    // An excluded row with no reason is the one thing a list like this cannot leave behind:
    // the box has no picker and nothing anywhere says why.
    expect(reasonComplaint(label, row.covered, row.reason, row.files)).toBe(label);
  });
});

describe("every row of the reading list is a surface the dashboard still draws", () => {
  test.each(
    DISPATCH_READING_SURFACES.map((row) => [row.surface, row] as const)
  )("%s", (label: string, row: DispatchReadingSurfaceRow) => {
    for (const file of row.files) {
      expect(
        LINES_BY_FILE.has(file) ? file : `${file} (named by ${label}) is not a source file`
      ).toBe(file);
    }
    if (row.rendered) {
      const drawn = row.files.some((file) => holdsAny(file, READING_CONSTRUCTS));
      expect(
        drawn ? label : `${label} renders, but no file of it mounts the renderer or a card`
      ).toBe(label);
    }
    if (row.reason !== undefined) {
      // An excluded row's tie to the place it excuses: the site names the row rather than
      // carrying a second copy of the reason that can drift from this one.
      const pointed = row.files.some((file) => writesWord(file, row.surface));
      expect(pointed ? label : `no file of ${label} points at it by name`).toBe(label);
    }
  });

  test.each(
    DISPATCH_READING_SURFACES.map((row) => [row.surface, row] as const)
  )("%s says why it is excluded", (label: string, row: DispatchReadingSurfaceRow) => {
    expect(reasonComplaint(label, row.rendered && row.record, row.reason, row.files)).toBe(label);
  });
});

// ------------------------------------------------------------------------------------------
// No second reader came back
// ------------------------------------------------------------------------------------------

describe("no file imports a reader this change deleted", () => {
  test.each(
    DELETED_READERS.map((reader) => [reader.what, reader] as const)
  )("%s is imported nowhere", (_what: string, reader: DeletedReader) => {
    // The patterns are the reader's own, so one is built per deleted reader rather than per
    // line of every file the walk reads.
    const importPattern =
      reader.module === undefined ? undefined : new RegExp(`from "[^"]*${reader.module}"`);
    const namePatterns = reader.names.map((name) => new RegExp(`\\b${name}\\b`));
    const offenders: string[] = [];
    for (const file of SCANNED_FILES) {
      const lines = LINES_BY_FILE.get(file) ?? [];
      lines.forEach((line, index) => {
        const importsModule = importPattern?.test(line) === true;
        const writesName = namePatterns.some((pattern) => pattern.test(line));
        if (importsModule || writesName) {
          offenders.push(`${file}:${index + 1}`);
        }
      });
    }
    expect(offenders).toEqual([]);
  });
});
