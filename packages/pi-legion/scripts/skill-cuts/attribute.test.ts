import { afterAll, expect, test } from "bun:test";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import {
  type Attribution,
  attribute,
  type CountRow,
  copyPrefix,
  type InstallersRow,
  main,
  type PrefixTableRow,
  parseQueryOutput,
  parseRunLog,
  type QueryOutput,
  queryIssues,
  type R4Tables,
  READ_COUNT_COLUMNS,
  rankChanged,
  readCount,
  type SkillCopiesRow,
} from "./attribute";
import { loadConfig, parseConfig } from "./config";
import {
  INSTALLERS_COLUMNS,
  LOADERS_COLUMNS,
  PREFIX_TABLE_COLUMNS,
  parseTsv,
  REPOSITORIES_COLUMNS,
  SKILL_COPIES_COLUMNS,
  tsv,
} from "./copies";

// attribute.ts on F7, count.sql's own output under the store command's flags
// (testdata/F7-count-first.txt and F7-count-second.txt; psql-rows.test.ts's header gives the
// capture commands), and on made-up R4 tables of made-up repositories (acme/skills, acme/tools,
// acme/dotfiles, acme/cc), each in the columns copies.ts writes. Other query outputs are made here
// in F7's shape: a psql table of count.sql rows, read back with parseQueryOutput.
//
// F7-count-first.txt holds, for store_a and the day 2026-11-20:
// - H1 (F1's session): a read of alpha at F1_PROFILE/alpha/SKILL.md, and of its references/x.md;
// - H2 (F4's session): a read of alpha at F3/marketplace/plugins/acme-tools/skills/alpha/SKILL.md,
//   and of beta at F3/config/plugins/cache/acme-market/acme-tools/0.1.0/skills/beta/SKILL.md;
// - H3 (F1's subagent): a read of beta at F1_PROFILE/beta/SKILL.md.
// F7-count-second.txt holds H1B (F1b's session) reading alpha at F1_PROFILE/alpha/SKILL.md.
//
// The made-up world of those rows (`f7World`): alpha has one copy (acme/skills skills/alpha) and
// beta two (acme/skills skills/beta, acme/tools plugins/acme-tools/skills/beta). An installer of
// acme/dotfiles links both acme/skills copies into Oh My Pi's user skills, and acme/tools'
// marketplace ships its beta in the plugin acme-tools. The prefix table maps F1_PROFILE/alpha/ to
// the linked alpha and the plugin cache's beta to acme/tools' beta; F1_PROFILE/beta/ and the
// marketplace's alpha are in no prefix row.

const TESTDATA = path.join(import.meta.dir, "testdata");
const F1_PROFILE =
  "/legion/scratch/tmp/infra4676/t1-runs/f1/home/.omp/profiles/fixture/agent/skills";
const F3 = "/legion/scratch/tmp/infra4676/t1-runs/f3";
const H1 = "83af4b8e0c75a050";
const H2 = "5ebda8a6e0707d07";
const H3 = "0da7427e81182998";
const H1B = "2e8fca5c3950db03";
const H4 = "4444444444444444";

const CONFIG_FILE = path.join(TESTDATA, "F7-skill-cuts.json");
const CONFIG = loadConfig(CONFIG_FILE);
const TWO_STORES = parseConfig({
  ...(JSON.parse(readFileSync(CONFIG_FILE, "utf8")) as object),
  stores: [
    { name: "store_a", context: "devbox" },
    { name: "store_b", context: "legion:ACME" },
  ],
});

const dir = mkdtempSync(path.join(process.env.TMPDIR ?? tmpdir(), "skill-cuts-attribute-"));
afterAll(() => rmSync(dir, { recursive: true, force: true }));

const f7 = (name: string) => readFileSync(path.join(TESTDATA, name), "utf8");
const F7_FIRST = () => parseQueryOutput("store_a", "2026-11-20", f7("F7-count-first.txt"));

/** A psql aligned table of `rows`, one JSON value each, as the store command prints it. */
function psqlTable(rows: readonly object[]): string {
  const lines = rows.map((row) => ` ${JSON.stringify(row)}`);
  return [" total", "-".repeat(40), ...lines, `(${rows.length} rows)`, ""].join("\n");
}

const summaryRow = (store: string) => ({
  row: "summary",
  store,
  mtime_from: 0,
  mtime_to: 0,
  files: 0,
  parts: 0,
  bytes: 0,
  lines_parsed: 0,
  bad_lines: 0,
  shared_call_ids: 0,
});

/** A count.sql result of `store` for the query `name`, in F7's shape. */
function queryText(
  store: string,
  name: string,
  rows: readonly (Pick<CountRow, "skill" | "copy"> & {
    readonly hashes: readonly string[];
    readonly kind?: CountRow["kind"];
    readonly files?: number;
    readonly unpaired?: number;
    readonly unpaired_hashes?: readonly string[];
  })[]
): string {
  return psqlTable([
    ...rows.map((row) => ({
      row: "count",
      store,
      day: name.slice(0, 10),
      kind: "read",
      files: row.hashes.length,
      unpaired: 0,
      unpaired_hashes: [],
      ...row,
    })),
    summaryRow(store),
  ]);
}

function query(store: string, name: string, rows: Parameters<typeof queryText>[2]): QueryOutput {
  return parseQueryOutput(store, name, queryText(store, name, rows));
}

function copyRow(
  repository: string,
  dir: string,
  name: string,
  more: Partial<SkillCopiesRow> = {}
): SkillCopiesRow {
  return {
    repository,
    path: dir,
    name,
    description_bytes: "20",
    fork: "no",
    archived: "no",
    parent: "",
    installers: "",
    loaded_by: "",
    ...more,
  };
}

type Unit = Pick<
  InstallersRow,
  "unit" | "manifest_repository" | "manifest_path" | "kind" | "action" | "host"
>;
const USER_SKILLS: Unit = {
  unit: "user-skills:omp",
  manifest_repository: "acme/dotfiles",
  manifest_path: "installers/omp.sh",
  kind: "installer-script",
  action: "links",
  host: "omp",
};
/** A Claude Code plugin of acme/cc's marketplace `m`. */
const plugin = (name: string, repository = "acme/cc"): Unit => ({
  unit: `plugin:${name}@m`,
  manifest_repository: repository,
  manifest_path: ".claude-plugin/marketplace.json",
  kind: "claude-marketplace",
  action: "ships",
  host: "claude-code",
});
const ACME_TOOLS: Unit = {
  ...plugin("acme-tools", "acme/tools"),
  unit: "plugin:acme-tools@acme-market",
};

const installs = (unit: Unit, copy: SkillCopiesRow): InstallersRow => ({
  ...unit,
  skill: copy.name,
  copy_repository: copy.repository,
  copy_path: copy.path,
  note: "",
});
const via = (unit: Unit) => `${unit.unit} (${unit.manifest_repository}:${unit.manifest_path})`;
const prefixRow = (prefix: string, copy: SkillCopiesRow, unit: Unit): PrefixTableRow => ({
  prefix,
  repository: copy.repository,
  path: copy.path,
  name: copy.name,
  via: via(unit),
});
const repositoryRow = (repository: string) => ({
  repository,
  branch: "main",
  commit: "0123456789abcdef0123456789abcdef01234567",
  fork: "no",
  archived: "no",
  parent: "",
  tree_entries: "10",
  tree_read: "whole",
  skills: "2",
  error: "",
});

const ALPHA = copyRow("acme/skills", "skills/alpha", "alpha");
const BETA = copyRow("acme/skills", "skills/beta", "beta");
const TOOLS_BETA = copyRow("acme/tools", "plugins/acme-tools/skills/beta", "beta");

function f7World(): R4Tables {
  return {
    copies: [ALPHA, BETA, TOOLS_BETA],
    installers: [
      installs(USER_SKILLS, ALPHA),
      installs(USER_SKILLS, BETA),
      installs(ACME_TOOLS, TOOLS_BETA),
    ],
    prefixes: [
      prefixRow("**/profiles/fixture/agent/skills/alpha/", ALPHA, USER_SKILLS),
      prefixRow("**/plugins/cache/acme-market/acme-tools/*/skills/beta/", TOOLS_BETA, ACME_TOOLS),
    ],
    loaders: [],
    repositories: [repositoryRow("acme/skills"), repositoryRow("acme/tools")],
  };
}

const shownCopy = (copy: SkillCopiesRow) => `${copy.repository} ${copy.path}`;
function countOf(attribution: Attribution, copy: SkillCopiesRow) {
  const found = attribution.copies.find(
    (count) => count.repository === copy.repository && count.path === copy.path
  );
  if (found === undefined) throw new Error(`no count for ${shownCopy(copy)}`);
  return found;
}

test("a mapped prefix goes to its copy", () => {
  const attribution = attribute(CONFIG, [F7_FIRST()], f7World());
  expect(attribution.mappedPrefixes.get(`${F1_PROFILE}/alpha/`)).toEqual(
    new Set([shownCopy(ALPHA)])
  );
  expect(
    attribution.mappedPrefixes.get(
      `${F3}/config/plugins/cache/acme-market/acme-tools/0.1.0/skills/beta/`
    )
  ).toEqual(new Set([shownCopy(TOOLS_BETA)]));
  expect(countOf(attribution, TOOLS_BETA)).toMatchObject({ sessions: 1, referenceSessions: 0 });
  // H1's read and its reference read of references/x.md, both under F1_PROFILE/alpha/.
  expect(countOf(attribution, ALPHA).referenceSessions).toBe(1);
});

test("an unmapped read goes to the copy the session's context installs", () => {
  const attribution = attribute(CONFIG, [F7_FIRST()], f7World());
  // store_a's mapped reads come from the linked user skills and the acme-tools plugin, which
  // between them hold one alpha: H2's alpha read from the marketplace goes to it.
  expect(attribution.stores[0]?.units).toEqual([
    { unit: via(ACME_TOOLS), sessions: 1, copies: 1 },
    { unit: via(USER_SKILLS), sessions: 1, copies: 2 },
  ]);
  expect(countOf(attribution, ALPHA)).toMatchObject({ sessions: 2, unattributed: 0 });
  expect(
    attribution.unmapped.find(
      (item) => item.prefix === `${F3}/marketplace/plugins/acme-tools/skills/alpha/`
    )
  ).toEqual({
    prefix: `${F3}/marketplace/plugins/acme-tools/skills/alpha/`,
    skills: ["alpha"],
    sessions: 1,
    referenceSessions: 0,
    outcomes: [
      {
        outcome: "store_a: to acme/skills skills/alpha, the one copy of alpha its context installs",
        sessions: 1,
      },
    ],
  });
});

test("a read that neither the prefix table nor the context decides is unattributed", () => {
  // store_b's only read maps by no prefix row, so its context installs no unit.
  const unmappedOnly = query("store_b", "2026-11-20", [
    { skill: "alpha", copy: "/work/acme/skills/alpha/SKILL.md", hashes: [H4] },
  ]);
  const attribution = attribute(TWO_STORES, [F7_FIRST(), unmappedOnly], f7World());
  // H3's beta under F1_PROFILE/beta/: the context holds two copies of beta.
  expect(
    attribution.unmapped.find((item) => item.prefix === `${F1_PROFILE}/beta/`)?.outcomes
  ).toEqual([
    {
      outcome:
        "store_a: unattributed: its context installs 2 copies of beta (acme/skills skills/beta, acme/tools plugins/acme-tools/skills/beta)",
      sessions: 1,
    },
  ]);
  expect(countOf(attribution, BETA)).toMatchObject({ sessions: 0, unattributed: 1 });
  expect(countOf(attribution, TOOLS_BETA)).toMatchObject({ sessions: 1, unattributed: 1 });
  expect(attribution.stores[1]?.units).toEqual([]);
  expect(
    attribution.unmapped.find((item) => item.prefix === "/work/acme/skills/alpha/")?.outcomes
  ).toEqual([
    { outcome: "store_b: unattributed: its context installs no copy of alpha", sessions: 1 },
  ]);
  expect(countOf(attribution, ALPHA)).toMatchObject({ sessions: 2, unattributed: 1 });
  expect(attribution.unattributedSessions).toBe(2);
});

test.each([
  ["at most the name's unattributed sessions apart is a tie", [], 1, true],
  [
    "one more apart is not",
    [
      {
        skill: "beta",
        copy: `${F3}/config/plugins/cache/acme-market/acme-tools/0.2.0/skills/beta/SKILL.md`,
        hashes: [H4],
      },
    ],
    2,
    false,
  ],
] as const)("two copies of one name %s", (_name, extra, toolsSessions, tie) => {
  const outputs = [F7_FIRST(), query("store_a", "2026-11-21", extra)];
  const attribution = attribute(CONFIG, outputs, f7World());
  // beta: acme/skills' copy 0 sessions, acme/tools' 1 (or 2); H3's read is unattributed.
  expect(countOf(attribution, TOOLS_BETA)).toMatchObject({
    sessions: toolsSessions,
    unattributed: 1,
  });
  expect(countOf(attribution, BETA).ties).toEqual(tie ? [shownCopy(TOOLS_BETA)] : []);
  expect(countOf(attribution, TOOLS_BETA).ties).toEqual(tie ? [shownCopy(BETA)] : []);
  // alpha has one copy, so it ties with none.
  expect(countOf(attribution, ALPHA).ties).toEqual([]);
});

test("a file hash seen in two queries of a store counts once, and in two stores twice", () => {
  // H1 resumed: its file is in the day query of 2026-11-20 and again in the next day's.
  const again = query("store_a", "2026-11-21", [
    { skill: "alpha", copy: `${F1_PROFILE}/alpha/SKILL.md`, hashes: [H1] },
  ]);
  expect(countOf(attribute(CONFIG, [F7_FIRST(), again], f7World()), ALPHA).sessions).toBe(2);
  const otherStore = query("store_b", "2026-11-20", [
    {
      skill: "alpha",
      copy: `/home/acme/.omp/profiles/fixture/agent/skills/alpha/SKILL.md`,
      hashes: [H1],
    },
  ]);
  const attribution = attribute(TWO_STORES, [F7_FIRST(), again, otherStore], f7World());
  expect(countOf(attribution, ALPHA).sessions).toBe(3);
  expect(attribution.stores.map((store) => store.readSessions)).toEqual([3, 1]);
  // F7-count-second's session (F1b) is another file, so another session.
  const second = parseQueryOutput("store_a", "2026-11-21", f7("F7-count-second.txt"));
  expect(second.rows.map((row) => row.hashes)).toEqual([[H1B], [H1B]]);
  expect(countOf(attribute(CONFIG, [F7_FIRST(), second], f7World()), ALPHA).sessions).toBe(3);
});

// The hosts of a copy. Oh My Pi installs it when an Oh My Pi session read it, when a store's
// context installs its unit, or when an installers.tsv row of host omp installs it; Claude Code
// when a row of host claude-code does.
const CACHE = "/home/acme/.omp/profiles/default/plugins/cache/plugins";
const DELTA = copyRow("acme/cc", "plugins/cc/skills/delta", "delta");
const EPSILON = copyRow("acme/cc", "plugins/cc2/skills/epsilon", "epsilon");
const ZETA = copyRow("acme/cc", "plugins/cc3/skills/zeta", "zeta");
const ETA = copyRow("acme/cc", "plugins/cc4/skills/eta", "eta");
const THETA = copyRow("acme/cc", "plugins/cc5/skills/theta", "theta");
const IOTA = copyRow("acme/cc", "plugins/cc5/skills/iota", "iota");
const KAPPA = copyRow("acme/skills", "skills/kappa", "kappa");

function hostsWorld(): R4Tables {
  return {
    copies: [DELTA, EPSILON, ZETA, ETA, THETA, IOTA, KAPPA],
    installers: [
      installs(plugin("cc"), DELTA),
      installs(plugin("cc2"), EPSILON),
      installs(plugin("cc3"), ZETA),
      installs(plugin("cc4"), ETA),
      installs(USER_SKILLS, ETA),
      installs(plugin("cc5"), THETA),
      installs(plugin("cc5"), IOTA),
    ],
    prefixes: [
      prefixRow("**/plugins/cache/plugins/m___cc2___*/skills/epsilon/", EPSILON, plugin("cc2")),
      prefixRow("**/plugins/cache/plugins/m___cc3___*/skills/zeta/", ZETA, plugin("cc3")),
      prefixRow("**/plugins/cache/plugins/m___cc5___*/skills/iota/", IOTA, plugin("cc5")),
    ],
    loaders: [],
    repositories: [repositoryRow("acme/cc"), repositoryRow("acme/skills")],
  };
}

test.each([
  [
    "only a Claude Code plugin installs it, and no Oh My Pi session read it",
    DELTA,
    "not countable yet",
  ],
  [
    "only a Claude Code plugin installs it, and an Oh My Pi session read one of its references",
    EPSILON,
    "no Oh My Pi read; Claude Code not counted yet",
  ],
  ["only a Claude Code plugin installs it, and an Oh My Pi session read it", ZETA, "read"],
  [
    "a Claude Code plugin and an Oh My Pi installer install it, and no session read it",
    ETA,
    "no Oh My Pi read; Claude Code not counted yet",
  ],
  [
    "a Claude Code plugin installs it, and Oh My Pi sessions read another skill of that plugin",
    THETA,
    "no Oh My Pi read; Claude Code not counted yet",
  ],
  ["no installer installs it, and no session read it", KAPPA, "no read"],
] as const)("%s: %s", (_name, copy, status) => {
  const reads = query("store_a", "2026-11-20", [
    {
      skill: "epsilon",
      copy: `${CACHE}/m___cc2___1.0.0/skills/epsilon/references/r.md`,
      kind: "reference",
      hashes: [H1],
    },
    { skill: "zeta", copy: `${CACHE}/m___cc3___1.0.0/skills/zeta/SKILL.md`, hashes: [H2] },
    { skill: "iota", copy: `${CACHE}/m___cc5___1.0.0/skills/iota/SKILL.md`, hashes: [H3] },
  ]);
  expect(countOf(attribute(CONFIG, [reads], hostsWorld()), copy).status).toBe(status);
});

test("a skill that an agent definition, a loader list, code or pi-envoy loads is marked loaded", () => {
  const copies = [
    copyRow("acme/skills", "skills/reviewer", "reviewer"),
    copyRow("acme/skills", "skills/tree-tool", "tree-tool"),
    copyRow("acme/skills", "skills/coded", "coded"),
    copyRow("acme/skills", "skills/dispatch-first", "dispatch-first"),
    copyRow("acme/skills", "skills/plain", "plain", {
      installers: "ships package:acme-envoy (acme/skills:packages/acme-envoy/package.json)",
    }),
  ];
  const [reviewer, plain] = [copies[0], copies[4]];
  if (reviewer === undefined || plain === undefined) throw new Error("no copies");
  const world: R4Tables = {
    copies,
    prefixes: [],
    installers: [
      installs(
        {
          unit: "agent:reviewer",
          manifest_repository: "acme/skills",
          manifest_path: "agents/reviewer.md",
          kind: "agent-definition",
          action: "loads",
          host: "omp",
        },
        reviewer
      ),
      {
        unit: "loader:tree/init.py#list",
        manifest_repository: "acme/tree",
        manifest_path: "tree/init.py",
        kind: "python-loader",
        action: "loads",
        host: "any",
        skill: "tree-tool",
        copy_repository: "",
        copy_path: "",
        note: "no copy of this name was read",
      },
      installs(
        {
          unit: "package:acme-envoy",
          manifest_repository: "acme/skills",
          manifest_path: "packages/acme-envoy/package.json",
          kind: "omp-package",
          action: "ships",
          host: "omp",
        },
        plain
      ),
    ],
    loaders: [
      {
        repository: "acme/app",
        path: "src/skills.ts",
        skill: "coded",
        match: "quoted-string,code-load",
        source: "search",
      },
      {
        repository: "acme/app",
        path: "README.md",
        skill: "plain",
        match: "skill-uri",
        source: "search",
      },
    ],
    repositories: [repositoryRow("acme/skills")],
  };
  const attribution = attribute(CONFIG, [], world);
  expect(attribution.copies.map((copy) => [copy.name, copy.loadedBy])).toEqual([
    ["reviewer", ["acme/skills:agents/reviewer.md (agent-definition agent:reviewer)"]],
    ["tree-tool", ["acme/tree:tree/init.py (python-loader loader:tree/init.py#list)"]],
    ["coded", ["acme/app:src/skills.ts (code-load, search)"]],
    [
      "dispatch-first",
      [
        "pi-envoy injects it into each session that has Dispatch (packages/envoy-client/src/dispatch-first.ts)",
      ],
    ],
    ["plain", []],
  ]);
});

test("the prefix table class inventory: each prefix in the totals is mapped or listed unattributed", () => {
  const outputs = [
    F7_FIRST(),
    parseQueryOutput("store_a", "2026-11-21", f7("F7-count-second.txt")),
    query("store_a", "2026-11-22", [
      { skill: "alpha", copy: "skill://alpha/SKILL.md:1-40", hashes: [H4] },
      { skill: "alpha", copy: "skill://alpha/references/y.md", kind: "reference", hashes: [H4] },
      { skill: "beta", copy: "skills/beta/SKILL.md", hashes: [H4] },
      {
        skill: "gamma",
        copy: "https://example.com/acme/skills/main/skills/gamma/SKILL.md",
        hashes: [H4],
      },
      // A call with no result: no read, so in no total.
      {
        skill: "beta",
        copy: "skill://beta",
        files: 0,
        hashes: [],
        unpaired: 1,
        unpaired_hashes: [H3],
      },
    ]),
    query("store_b", "2026-11-20", [
      {
        skill: "beta",
        copy: `${F3}/config/plugins/cache/acme-market/acme-tools/9/skills/beta/SKILL.md`,
        hashes: [H2],
      },
      { skill: "beta", copy: `${F1_PROFILE}/beta/SKILL.md`, hashes: [H2] },
    ]),
  ];
  const attribution = attribute(TWO_STORES, outputs, f7World());
  const listed = new Set(attribution.unmapped.map((item) => item.prefix));
  const inTotals = outputs.flatMap((output) =>
    output.rows.filter((row) => row.hashes.length > 0).map((row) => copyPrefix(row.copy, row.skill))
  );
  expect(
    inTotals.filter((prefix) => !attribution.mappedPrefixes.has(prefix) && !listed.has(prefix))
  ).toEqual([]);
  // And each unmapped prefix's sessions all went somewhere: to a copy or unattributed.
  for (const item of attribution.unmapped) {
    const went = item.outcomes.reduce((sum, outcome) => sum + outcome.sessions, 0);
    expect(went).toBeGreaterThanOrEqual(Math.max(item.sessions, item.referenceSessions));
  }
  expect(listed).toEqual(
    new Set([
      `${F3}/marketplace/plugins/acme-tools/skills/alpha/`,
      `${F1_PROFILE}/beta/`,
      "skill://alpha/",
      "skills/beta/",
      "https://example.com/acme/skills/main/skills/gamma/",
    ])
  );
  expect(attribution.namesWithoutCopy).toEqual([
    { name: "gamma", sessions: 1, referenceSessions: 0 },
  ]);
  expect(attribution.stores[0]?.unpairedSessions).toBe(1);
});

test.each([
  ["skill://alpha", "alpha", "skill://alpha/"],
  ["skill://alpha/references/x.md:raw", "alpha", "skill://alpha/"],
  ["/opt/acme/skills/alpha/SKILL.md", "alpha", "/opt/acme/skills/alpha/"],
  ["/opt/acme/skills/alpha/SKILL.md:1-40", "alpha", "/opt/acme/skills/alpha/"],
  ["/opt/acme/skills/alpha/references/deep/x.md", "alpha", "/opt/acme/skills/alpha/"],
  ["/opt/alpha/skills/alpha/SKILL.md", "alpha", "/opt/alpha/skills/alpha/"],
  ["skills/alpha/SKILL.md", "alpha", "skills/alpha/"],
  ["/opt/acme/skills/alpha-dir/SKILL.md", "alpha", "/opt/acme/skills/alpha-dir/"],
  ["/opt/acme/skills/alpha-dir/references/x.md", "alpha", "/opt/acme/skills/alpha-dir/"],
])("the copy prefix of %s (skill %s) is %s", (copyPath, skill, prefix) => {
  expect(copyPrefix(copyPath, skill)).toBe(prefix);
});

/** Writes `files` (path below `root` → text) and returns `root`. */
function tree(root: string, files: Readonly<Record<string, string>>): string {
  for (const [file, text] of Object.entries(files)) {
    mkdirSync(path.dirname(path.join(root, file)), { recursive: true });
    writeFileSync(path.join(root, file), text);
  }
  return root;
}

test("a failed query in the run log is listed, with a split day's missing hours and a day not run", () => {
  const config = parseConfig({
    ...(JSON.parse(readFileSync(CONFIG_FILE, "utf8")) as object),
    windowEnd: "2026-11-23T00:00:00Z",
  });
  const hours = Object.fromEntries(
    Array.from({ length: 24 }, (_, hour) => String(hour).padStart(2, "0"))
      .filter((hour) => hour !== "05")
      .map((hour) => [`store_a/2026-11-20T${hour}.txt`, queryText("store_a", "2026-11-20", [])])
  );
  const at = (minute: number) => `2026-11-22T03:${String(minute).padStart(2, "0")}:00Z`;
  const log = [
    "START 2026-11-22T03:00:00Z runner=/scratch/runner",
    `${at(1)}\tstore_a\t2026-11-20\trc=3\tseconds=900.0\tpsql:<stdin>:1: ERROR:  canceling statement due to statement timeout`,
    ...Array.from({ length: 24 }, (_, hour) =>
      hour === 5
        ? `${at(2)}\tstore_a\t2026-11-20T05\trc=3\tseconds=900.0\tpsql:<stdin>:1: ERROR:  canceling statement due to statement timeout`
        : `${at(2)}\tstore_a\t2026-11-20T${String(hour).padStart(2, "0")}\trc=0\tseconds=1.0\t`
    ),
    "FAILED store_a 2026-11-20 hour 5",
    `${at(3)}\tstore_a\t2026-11-21\trc=2\tseconds=1.0\tpsql: error: connection to server failed`,
    "FAILED store_a 2026-11-21 rc=2",
    "",
  ].join("\n");
  const countDir = tree(path.join(dir, "issues-count"), {
    ...hours,
    "store_a/2026-11-21.err": "psql: error: connection to server failed\n",
    "store_a/2026-11-22.tmp": "",
    "run.log": log,
  });
  const count = readCount(countDir, ["store_a"]);
  expect(count.outputs).toHaveLength(23);
  expect(queryIssues(config, count)).toEqual([
    {
      store: "store_a",
      name: "2026-11-20",
      what: "split",
      detail:
        "timed out, so it was split into hours: 23 of 24 hour queries have output; hours 05 have none",
    },
    {
      store: "store_a",
      name: "2026-11-20T05",
      what: "failed",
      detail: "rc=3: psql:<stdin>:1: ERROR:  canceling statement due to statement timeout",
    },
    {
      store: "store_a",
      name: "2026-11-21",
      what: "failed",
      detail: "rc=2: psql: error: connection to server failed",
    },
    {
      store: "store_a",
      name: "2026-11-22",
      what: "no output",
      detail: "the run log has no line for it: not run yet",
    },
  ]);
  expect(parseRunLog(log).ended).toBeUndefined();
});

test("an output that is not a whole count.sql result is listed, and one of another store refused", () => {
  const countDir = tree(path.join(dir, "unreadable-count"), {
    "store_a/2026-11-20.txt": f7("F7-count-first.txt"),
    "store_a/2026-11-21.txt": psqlTable([]),
    "run.log": [
      "2026-11-22T03:00:00Z\tstore_a\t2026-11-20\trc=0\tseconds=1.0\t",
      "2026-11-22T03:01:00Z\tstore_a\t2026-11-21\trc=0\tseconds=1.0\t",
      "END 2026-11-21T23:00:00Z",
    ].join("\n"),
  });
  const count = readCount(countDir, ["store_a"]);
  expect(queryIssues(CONFIG, count)).toEqual([
    {
      store: "store_a",
      name: "2026-11-21",
      what: "unreadable",
      detail: "its output is not a whole result: no summary row: the query did not end",
    },
  ]);
  expect(() => parseQueryOutput("store_b", "2026-11-20", f7("F7-count-first.txt"))).toThrow(
    'row 1 is of the store "store_a"'
  );
});

test("a cut's ranking is its changed skills by the sessions of its repository's copy, a tie by name", () => {
  const outputs = [
    F7_FIRST(),
    parseQueryOutput("store_a", "2026-11-21", f7("F7-count-second.txt")),
  ];
  const attribution = attribute(CONFIG, outputs, f7World());
  expect(
    rankChanged(attribution, "skills-1", "acme/skills", [
      "skills/beta",
      "skills/gone/",
      "skills/alpha",
      "",
    ])
  ).toEqual({
    cut: "skills-1",
    repository: "acme/skills",
    rows: [
      { skill: "alpha", sessions: 3 },
      { skill: "beta", sessions: 0 },
      { skill: "gone", sessions: 0 },
    ],
    missing: ["skills/gone"],
  });
});

/** R4's output directory for `world`, in copies.ts's columns. */
function r4Dir(root: string, world: R4Tables): string {
  const rows = <C extends readonly string[]>(
    columns: C,
    items: readonly Record<C[number], string>[]
  ) =>
    tsv(
      columns,
      items.map((item) => columns.map((column: C[number]) => item[column]))
    );
  return tree(root, {
    "skill-copies.tsv": rows(SKILL_COPIES_COLUMNS, world.copies),
    "prefix-table.tsv": rows(PREFIX_TABLE_COLUMNS, world.prefixes),
    "installers.tsv": rows(INSTALLERS_COLUMNS, world.installers),
    "loaders.tsv": rows(LOADERS_COLUMNS, world.loaders),
    "repositories.tsv": rows(REPOSITORIES_COLUMNS, world.repositories),
  });
}

function run(argv: readonly string[]) {
  const out: string[] = [];
  const err: string[] = [];
  const code = main(argv, { out: (text) => out.push(text), err: (text) => err.push(text) });
  return { code, out: out.join(""), err: err.join("\n") };
}

test("the command writes the count, its report and each cut's ranking under --out", () => {
  const root = path.join(dir, "command");
  const countDir = tree(path.join(root, "count"), {
    "store_a/2026-11-20.txt": f7("F7-count-first.txt"),
    "store_a/2026-11-20.err": "",
    "store_a/2026-11-21.txt": f7("F7-count-second.txt"),
    "store_a/2026-11-21.err": "",
    "run.log": [
      "START 2026-11-21T20:00:00Z runner=/scratch/runner",
      "2026-11-21T20:00:01Z\tstore_a\t2026-11-20\trc=0\tseconds=1.0\t",
      "2026-11-21T20:00:02Z\tstore_a\t2026-11-21\trc=0\tseconds=1.0\t",
      "END 2026-11-21T23:00:00Z",
      "",
    ].join("\n"),
  });
  const copiesDir = r4Dir(path.join(root, "copies"), f7World());
  const files = tree(root, {
    "spans.jsonl": `${JSON.stringify({ store: "store_a", earliest_mtime_ms: Date.parse("2026-11-20T02:00:00Z"), files: 3, measured_at: "2026-11-21T23:30:00Z" })}\n`,
    "changed.txt": "skills/alpha\nskills/beta\nskills/gone\n",
  });
  const out = path.join(root, "out");
  const result = run([
    "--config",
    CONFIG_FILE,
    "--count",
    countDir,
    "--copies",
    copiesDir,
    "--spans",
    path.join(files, "spans.jsonl"),
    "--out",
    out,
    "--changed",
    `skills-1=${path.join(files, "changed.txt")}`,
  ]);
  expect(result.err).toBe("");
  expect(result.code).toBe(0);
  expect(result.out).toContain(
    "2 query outputs, 3 copies (2 read), 4 read sessions (1 unattributed), 2 unmapped prefixes"
  );

  const tsvText = readFileSync(path.join(out, "skill-read-count.tsv"), "utf8");
  expect(tsvText.split("\n")[0]).toBe(READ_COUNT_COLUMNS.join("\t"));
  expect(parseTsv(tsvText)).toEqual([
    {
      repository: "acme/skills",
      path: "skills/alpha",
      name: "alpha",
      sessions: "3",
      reference_sessions: "2",
      unattributed_sessions: "0",
      tie: "",
      loaded: "no",
      status: "read",
    },
    {
      repository: "acme/skills",
      path: "skills/beta",
      name: "beta",
      sessions: "0",
      reference_sessions: "0",
      unattributed_sessions: "1",
      tie: "acme/tools plugins/acme-tools/skills/beta",
      loaded: "no",
      status: "no read",
    },
    {
      repository: "acme/tools",
      path: "plugins/acme-tools/skills/beta",
      name: "beta",
      sessions: "1",
      reference_sessions: "0",
      unattributed_sessions: "1",
      tie: "acme/skills skills/beta",
      loaded: "no",
      status: "read",
    },
  ]);
  expect(JSON.parse(readFileSync(path.join(out, "skills-1", "ranking.json"), "utf8"))).toEqual([
    { skill: "alpha", sessions: 3 },
    { skill: "beta", sessions: 0 },
    { skill: "gone", sessions: 0 },
  ]);

  const report = readFileSync(path.join(out, "skill-read-count.md"), "utf8");
  for (const line of [
    "End time 2026-11-22T00:00:00Z. The window is [2026-11-20T00:00:00Z, 2026-11-22T00:00:00Z), 2 days.",
    "| store_a | devbox | 2026-11-20T02:00:00Z | 3 | 2026-11-20 | 4 | 2 | 4 | 205671 | 0 | 0 |",
    "- store_a starts after the window's start: its records span 2026-11-20T02:00:00Z to the end time, 1.9 of the window's 2 days.",
    '- The transcript store is not read: the cluster store that a pod reads holds no Claude Code transcript until the Claude Code transcripts are moved into it, so this count holds no Claude Code session. "Claude Code" below lists the skills that leaves out.',
    "None: each query of the window has its output, and the run log lists no failure.",
    "- store_a (devbox):",
    `  - ${via(USER_SKILLS)}: 2 sessions read from it; it holds 2 copies`,
    `| \`${F3}/marketplace/plugins/acme-tools/skills/alpha/\` | alpha | 1 | 0 | store_a: to acme/skills skills/alpha, the one copy of alpha its context installs (1 session) |`,
    `| \`${F1_PROFILE}/beta/\` | beta | 1 | 0 | store_a: unattributed: its context installs 2 copies of beta (acme/skills skills/beta, acme/tools plugins/acme-tools/skills/beta) (1 session) |`,
    "- beta: acme/skills skills/beta (0 sessions) ties with acme/tools plugins/acme-tools/skills/beta; the name has 1 unattributed session.",
    "- beta (skills/beta): no read; 1 session read beta unattributed",
    "| acme/skills | 0123456789abcdef0123456789abcdef01234567 | 2 | 9 | 40 | 49 |",
    "- skills-1 (acme/skills): 3 changed skills, in `skills-1/ranking.json`; first: alpha (3), beta (0), gone (0). R4 read no copy at `skills/gone`: ranked with 0 sessions.",
  ]) {
    expect(report).toContain(line);
  }
});

test("the command refuses missing flags, an --out inside an input, an unknown cut, and R4 tables of other columns", () => {
  const root = path.join(dir, "refusals");
  const countDir = tree(path.join(root, "count"), { "run.log": "" });
  const copiesDir = r4Dir(path.join(root, "copies"), f7World());
  const spans = path.join(tree(root, { "spans.jsonl": "" }), "spans.jsonl");
  const flags = (out: string, ...more: string[]) => [
    "--config",
    CONFIG_FILE,
    "--count",
    countDir,
    "--copies",
    copiesDir,
    "--spans",
    spans,
    "--out",
    out,
    ...more,
  ];
  expect(run(["--config", CONFIG_FILE]).code).toBe(2);
  const insideCount = run(flags(path.join(countDir, "out")));
  expect(insideCount.code).toBe(2);
  expect(insideCount.err).toContain("is inside --count");
  expect(existsSync(path.join(countDir, "out"))).toBe(false);
  const unknownCut = run(flags(path.join(root, "out"), "--changed", "no-such-cut=/dev/null"));
  expect(unknownCut.code).toBe(2);
  expect(unknownCut.err).toContain('the cut "no-such-cut"');
  writeFileSync(path.join(copiesDir, "loaders.tsv"), "repository\tpath\tskill\n");
  const wrongColumns = run(flags(path.join(root, "out")));
  expect(wrongColumns.code).toBe(1);
  expect(wrongColumns.err).toContain(
    "loaders.tsv: its columns are repository, path, skill, not copies.ts's"
  );
});
