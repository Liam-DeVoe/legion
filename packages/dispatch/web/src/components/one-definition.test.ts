import { describe, expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

import { stripComments } from "../__tests__/strip-comments";

/**
 * One definition per control, and one owner for the page address (INFRA-3135).
 *
 * Every search box, panel, menu, column filter, selection bar, table and pager a Dispatch page
 * draws is a component of this directory, named once in `control-names.ts`; and the address
 * those pages read and write is `lib/page-address.ts`'s alone. Both rules hold only for as long
 * as the NEXT page follows them, and a page that writes a box, a `<table>` or a
 * `useSearchParams` of its own compiles, passes its own tests and looks right on the screen.
 * This is the test that refuses it.
 *
 * It carries no allow-list and names no exception, deliberately: a mark it finds is a mark to
 * take out, and a control a page genuinely lacks is a control to add here, where every page
 * gets it. Comments are stripped first (`__tests__/strip-comments.ts`, which leaves a `/*` or a
 * `//` inside a string, a template or a regular expression where it is) so this file and the
 * pages it scans can both describe the marks in prose without being refused for it. Each mark
 * is then matched against the whole file rather than a line at a time, because the formatter
 * breaks a long construct across lines: a long `<table` sits alone at the end of its line, and
 * a `navigate({` has its `search:` on a line below.
 */

const FEATURES_ROOT = join(import.meta.dir, "..", "features");

/** One mark: what it matches, and what the failure says. */
interface Mark {
  /** What was found, and what to draw instead. */
  readonly what: string;
  /** Matched against the whole stripped file, so a `\s` in it spans the formatter's line breaks;
   *  each match is reported at the line it starts on. */
  readonly pattern: RegExp;
}

/** The four marks no page under `features/` may carry, whatever it is. */
const SHARED_MARKS: readonly Mark[] = [
  {
    what: 'writes a search box of its own (`type="search"`) — draw `components/SearchBox.tsx`',
    pattern: /type=(?:\{\s*)?["'`]search["'`]/,
  },
  {
    what: "writes a `<table>` of its own — draw `components/TableShell.tsx` or `components/DataTable.tsx`",
    pattern: /<table(?![\w-])/,
  },
  {
    what: "writes a `data-control` attribute by hand — a page draws the control, which carries its own name from `components/control-names.ts`",
    pattern: /data-control/,
  },
  {
    what: "imports `components/AnchoredPanel` — open a panel through `components/PanelTrigger.tsx`, `components/DisplayMenu.tsx` or `components/MultiSelect.tsx`",
    pattern: /components\/AnchoredPanel"/,
  },
];

/** The feature directories whose whole address is `lib/page-address.ts`'s: the pages this change
 *  moved onto it. A feature outside this list still owns its own route reading. */
const ADDRESS_FEATURES = [
  "agents",
  "artifacts",
  "delivery",
  "inbox",
  "priorities",
  "project",
  "search",
];

/** The marks only those seven carry: a direct read or write of the page address, by the names
 *  the pages used before one module owned it, and the hooks and the redirect the router offers. */
const ADDRESS_MARKS: readonly Mark[] = [
  {
    what: "reads the address directly (`useSearchParams`) — read it through `lib/page-address.ts`",
    pattern: /\buseSearchParams\b/,
  },
  {
    what: "reads the address directly (`useCappedSearchParam`) — `lib/page-address.ts`'s `useCappedAddressParam`",
    pattern: /\buseCappedSearchParam\b/,
  },
  {
    what: "reads the address directly (`useRepeatableSearchParams`) — `lib/page-address.ts`'s `useRepeatableAddressParams`",
    pattern: /\buseRepeatableSearchParams\b/,
  },
  {
    what: "writes the address directly (`useSearchParamsUpdate`) — `lib/page-address.ts`'s `useAddressUpdate`",
    pattern: /\buseSearchParamsUpdate\b/,
  },
  {
    what: "reads the router's location (`useLocation`) — `lib/page-address.ts`'s `usePageAddress` hands back the `pathname` and `search` a route parser takes, and `useNavigationState` what a navigation carried",
    pattern: /\buseLocation\b/,
  },
  {
    what: "builds the address directly (`new URLSearchParams`) — `lib/page-address.ts`'s `copyAddress`",
    pattern: /new URLSearchParams\b/,
  },
  {
    what: "navigates with a query of its own — `lib/page-address.ts`'s `useAddressNavigate`",
    // A path holding `?`, or an object naming `search` among its own keys: the lookbehind takes
    // `search` only where a key can stand, so `state: search` and `hash: x.search` are values
    // and not a query. A key of an object nested in it is not the query either, and a
    // template's `${…}` inside it is skipped whole.
    pattern: /navigate\(\s*(?:["'`][^"'`]*\?|\{(?:[^{}]|\{[^{}]*\})*?(?<=[{,]\s*)search\s*[:,}])/,
  },
  {
    what: "redirects with the router's own `<Navigate>` — `lib/page-address.ts`'s `AddressRedirect`",
    pattern: /<Navigate\b/,
  },
  {
    what: "reads or writes the address through the browser's own location (`window.location`) — `lib/page-address.ts`'s `usePageAddress` to read it, `useAddressUpdate` or `useAddressNavigate` to write it",
    // `self`, `top` and `parent` name this window as `window` does; each is matched only as a
    // name of its own, so an object's own `parent.location` is not one.
    pattern: /(?:\b(?:window|document|globalThis)|(?<![\w$.])(?:self|top|parent))\.location\b/,
  },
  {
    what: "reads the browser's own location through the bare global (`location.search`) — `lib/page-address.ts`'s `usePageAddress`, under a name of its own, since a bare `location` is the browser's",
    pattern: /(?<![\w$.])location\??\.[A-Za-z_$]/,
  },
  {
    what: "takes the browser's own location whole (`const { search } = location`) — `lib/page-address.ts`'s `usePageAddress`, under a name of its own, since a bare `location` is the browser's",
    // The global bound to a name or destructured, which reads the address just as `location.x`
    // does: the `=` is an assignment or a default, never a comparison (`==`, `===`, `!==`).
    pattern: /(?<![=!<>])=\s*location\s*(?![\w$.?(])/,
  },
  {
    what: "writes the address through the browser's own history (`history.pushState` / `history.replaceState`), behind the router's back — `lib/page-address.ts`'s `useAddressUpdate` or `useAddressNavigate`",
    pattern: /\bhistory\.(?:pushState|replaceState)\b/,
  },
];

/** The table library's module specifier, as every one of its statements ends. */
const TABLE_MODULE = '"@tanstack/react-table"';

/**
 * `components/DataTable.tsx` is the web app's one TanStack table, and it re-exports `flexRender`
 * so a page that draws its own rows never reaches the library as a value. A page may still name
 * the library's types - a `ColumnDef`, a `Row`, a `SortingState` - and the project List augments
 * its column `meta` through `declare module`, which has to sit beside the columns it describes.
 *
 * A statement is read whole, from where it starts to the specifier it ends at. It starts at its
 * own `import`, `export` or `declare`, or after the `;` that ends the statement before it,
 * whichever comes later. So a multi-line `import type { … } from "@tanstack/react-table"` is
 * read as the one `import type` it is rather than as the bare `}` on the line the specifier sits
 * on, and an `export { … } from "@tanstack/react-table"` after it is read as the re-export it is
 * rather than as the tail of that `import type`. A re-export of the library's types,
 * `export type { … }`, names types as an `import type` does.
 */
function tableImportRefusals(relativePath: string, source: string): string[] {
  const refused: string[] = [];
  for (
    let at = source.indexOf(TABLE_MODULE);
    at !== -1;
    at = source.indexOf(TABLE_MODULE, at + 1)
  ) {
    const before = source.slice(0, at);
    const keyword = /\b(?:import|export|declare)\b/g;
    let start = before.lastIndexOf(";") + 1;
    for (let match = keyword.exec(before); match !== null; match = keyword.exec(before)) {
      start = Math.max(start, match.index);
    }
    const statement = source.slice(start, at + TABLE_MODULE.length).trimStart();
    const allowed =
      /^(?:import|export)\s+type\b/.test(statement) ||
      /^declare\s+module\s+"@tanstack\/react-table"$/.test(statement);
    if (!allowed) {
      const statementStart = at + TABLE_MODULE.length - statement.length;
      const line = source.slice(0, statementStart).split("\n").length;
      refused.push(
        `${relativePath}:${line} imports \`@tanstack/react-table\` as a value — the one table is \`components/DataTable.tsx\`, which re-exports \`flexRender\``
      );
    }
  }
  return refused;
}

function collectFeatureFiles(dir: string): string[] {
  const files: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const entryPath = join(dir, entry.name);
    if (entry.isDirectory()) {
      files.push(...collectFeatureFiles(entryPath));
      continue;
    }
    const isSource = entry.name.endsWith(".ts") || entry.name.endsWith(".tsx");
    const isUnitTest = entry.name.endsWith(".test.ts") || entry.name.endsWith(".test.tsx");
    if (isSource && !isUnitTest) {
      files.push(entryPath);
    }
  }
  return files;
}

const FEATURE_FILES = collectFeatureFiles(FEATURES_ROOT).map(
  (file) => [join("features", file.slice(FEATURES_ROOT.length + 1)), file] as const
);

/** Every refusal one file earns, each naming the file, the line and what was found there.
 *  `source` is the file as it stands on disk; its comments are stripped here. */
function refusals(relativePath: string, source: string): string[] {
  const code = stripComments(source, relativePath);
  const marks = ADDRESS_FEATURES.some((feature) => relativePath.startsWith(`features/${feature}/`))
    ? [...SHARED_MARKS, ...ADDRESS_MARKS]
    : SHARED_MARKS;
  const found = marks
    .flatMap(({ pattern, what }) =>
      Array.from(code.matchAll(new RegExp(pattern.source, `${pattern.flags}g`)), (match) => ({
        line: code.slice(0, match.index).split("\n").length,
        what,
      }))
    )
    .sort((left, right) => left.line - right.line)
    .map(({ line, what }) => `${relativePath}:${line} ${what}`);
  return [...found, ...tableImportRefusals(relativePath, code)];
}

describe("no page builds a control or an address of its own", () => {
  test("scans every source file under features/", () => {
    // A guard on the walk itself: a collection that collapsed to nothing would let every check
    // below pass while covering no page at all.
    expect(FEATURE_FILES.length).toBeGreaterThan(200);
  });

  test("a page of the seven that redirects with `<Navigate>` is sent to `AddressRedirect`", () => {
    const source = [
      'import { Navigate } from "react-router-dom";',
      "",
      'export const Gone = () => <Navigate replace to="/x" />;',
    ].join("\n");
    expect(refusals("features/project/Gone.tsx", source)).toEqual([
      "features/project/Gone.tsx:3 redirects with the router's own `<Navigate>` — `lib/page-address.ts`'s `AddressRedirect`",
    ]);
    // The redirect the seven draw instead, and a feature outside them, are both left alone.
    expect(refusals("features/project/Gone.tsx", '<AddressRedirect to="/x" />')).toEqual([]);
    expect(refusals("features/architecture/Gone.tsx", source)).toEqual([]);
  });

  // Each form below is one the formatter or an ordinary page writes, and the guard has to read
  // through it: a construct broken across lines, a second spelling of a mark, the browser's
  // bare `location`, a re-export after an `import type`, and a `/*` inside a string.

  test("a `<table` the formatter leaves alone at the end of its line is refused", () => {
    const source = [
      "export const Probe = () => (",
      "  <table",
      '    aria-label="A table whose attributes are long enough to go one to a line"',
      '    className="w-full"',
      "  >",
      "    <tbody />",
      "  </table>",
      ");",
    ].join("\n");
    expect(refusals("features/inbox/Probe.tsx", source)).toEqual([
      "features/inbox/Probe.tsx:2 writes a `<table>` of its own — draw `components/TableShell.tsx` or `components/DataTable.tsx`",
    ]);
  });

  test("a `navigate({` whose `search` is on a later line is refused", () => {
    const source = [
      "export function useOpenBugs(navigate: (to: { pathname: string; search: string }) => void) {",
      "  return () =>",
      "    navigate({",
      '      pathname: "/projects/CORE/issues",',
      '      search: "?label=bug",',
      "    });",
      "}",
    ].join("\n");
    expect(refusals("features/project/open-bugs.ts", source)).toEqual([
      "features/project/open-bugs.ts:3 navigates with a query of its own — `lib/page-address.ts`'s `useAddressNavigate`",
    ]);
    // The key written short is the same query, and a nested object's `search` or a `/search`
    // path is not one.
    expect(refusals("features/project/open-bugs.ts", "navigate({ pathname, search });")).toEqual([
      "features/project/open-bugs.ts:1 navigates with a query of its own — `lib/page-address.ts`'s `useAddressNavigate`",
    ]);
    expect(
      refusals(
        "features/project/open-bugs.ts",
        `navigate({ pathname: \`/search/\${key}\`, state: { search: "x" } });`
      )
    ).toEqual([]);
  });

  test("a `navigate({` whose `search` is a value rather than a key is left alone", () => {
    // `state: search` carries the page's own text into the next page's router state, and
    // `hash: x.search` is a fragment read off an object: neither names a query of its own.
    expect(
      refusals("features/project/open-bugs.ts", "navigate({ pathname, state: search });")
    ).toEqual([]);
    expect(
      refusals("features/project/open-bugs.ts", "navigate({ pathname: p, hash: x.search });")
    ).toEqual([]);
    // The key itself is still refused, however the formatter spaced it.
    expect(
      refusals("features/project/open-bugs.ts", "navigate({\n  pathname,\n  search: query,\n});")
    ).toEqual([
      "features/project/open-bugs.ts:1 navigates with a query of its own — `lib/page-address.ts`'s `useAddressNavigate`",
    ]);
  });

  test("a page of the seven that reads `self`, `top` or `parent` `.location` is refused", () => {
    const what =
      "reads or writes the address through the browser's own location (`window.location`) — `lib/page-address.ts`'s `usePageAddress` to read it, `useAddressUpdate` or `useAddressNavigate` to write it";
    for (const name of ["self", "top", "parent"]) {
      expect(
        refusals("features/inbox/probe-query.ts", `export const q = ${name}.location.search;`)
      ).toEqual([`features/inbox/probe-query.ts:1 ${what}`]);
    }
    // A `parent` of something else is that object's own field, not this window.
    expect(
      refusals("features/inbox/probe-query.ts", "export const q = node.parent.location;")
    ).toEqual([]);
  });

  test("a page of the seven that takes the bare global `location` whole is refused", () => {
    const what =
      "takes the browser's own location whole (`const { search } = location`) — `lib/page-address.ts`'s `usePageAddress`, under a name of its own, since a bare `location` is the browser's";
    expect(
      refusals("features/inbox/probe-query.ts", "const { search } = location;\nexport { search };")
    ).toEqual([`features/inbox/probe-query.ts:1 ${what}`]);
    expect(refusals("features/inbox/probe-query.ts", "export const here = location;")).toEqual([
      `features/inbox/probe-query.ts:1 ${what}`,
    ]);
    // A read through the global is the other mark's, reported once; the page address under a
    // name of its own, a comparison against the global and a feature outside the seven are
    // left alone.
    expect(refusals("features/inbox/probe-query.ts", "export const q = location.search;")).toEqual([
      "features/inbox/probe-query.ts:1 reads the browser's own location through the bare global (`location.search`) — `lib/page-address.ts`'s `usePageAddress`, under a name of its own, since a bare `location` is the browser's",
    ]);
    expect(
      refusals("features/inbox/probe-query.ts", "const { search } = usePageAddress();")
    ).toEqual([]);
    expect(
      refusals("features/architecture/probe-query.ts", "export const here = location;")
    ).toEqual([]);
  });

  test('a search box spelled `type={"search"}` is refused', () => {
    const source = 'export const Probe = () => <input aria-label="Search" type={"search"} />;';
    expect(refusals("features/inbox/Probe.tsx", source)).toEqual([
      'features/inbox/Probe.tsx:1 writes a search box of its own (`type="search"`) — draw `components/SearchBox.tsx`',
    ]);
  });

  test("a page of the seven that reads the bare global `location.search` is refused", () => {
    const source = [
      "export function probeQuery(): string {",
      "  return location.search;",
      "}",
    ].join("\n");
    expect(refusals("features/inbox/probe-query.ts", source)).toEqual([
      "features/inbox/probe-query.ts:2 reads the browser's own location through the bare global (`location.search`) — `lib/page-address.ts`'s `usePageAddress`, under a name of its own, since a bare `location` is the browser's",
    ]);
    // The page address under a name of its own, and a feature outside the seven, are left alone.
    const named = ["const address = usePageAddress();", "export const search = address.search;"];
    expect(refusals("features/inbox/probe-query.ts", named.join("\n"))).toEqual([]);
    expect(refusals("features/architecture/probe-query.ts", source)).toEqual([]);
  });

  test("a value re-export of the table library after an `import type` is refused", () => {
    const source = [
      'import type { ColumnDef } from "@tanstack/react-table";',
      'export { useReactTable } from "@tanstack/react-table";',
    ].join("\n");
    expect(refusals("features/inbox/probe-table.ts", source)).toEqual([
      "features/inbox/probe-table.ts:2 imports `@tanstack/react-table` as a value — the one table is `components/DataTable.tsx`, which re-exports `flexRender`",
    ]);
    // A re-export of the library's types names types, as the `import type` does.
    const types = [
      'import type { ColumnDef } from "@tanstack/react-table";',
      'export type { Row } from "@tanstack/react-table";',
    ].join("\n");
    expect(refusals("features/inbox/probe-table.ts", types)).toEqual([]);
  });

  test('a box and a `<table` after `accept="image/*"` and before a doc comment are refused', () => {
    const source = [
      "export const Upload = () => (",
      "  <form>",
      '    <input accept="image/*" type="file" />',
      '    <input aria-label="Search" type="search" />',
      "    <table>",
      "      <tbody />",
      "    </table>",
      "  </form>",
      ");",
      "",
      "/** A later doc comment, whose close is where the old strip's comment ended. */",
      "export const after = 1;",
    ].join("\n");
    expect(refusals("features/inbox/Upload.tsx", source)).toEqual([
      'features/inbox/Upload.tsx:4 writes a search box of its own (`type="search"`) — draw `components/SearchBox.tsx`',
      "features/inbox/Upload.tsx:5 writes a `<table>` of its own — draw `components/TableShell.tsx` or `components/DataTable.tsx`",
    ]);
  });

  test.each(FEATURE_FILES)("%s draws the shared controls", (relativePath, file) => {
    expect(refusals(relativePath, readFileSync(file, "utf8"))).toEqual([]);
  });
});
