/**
 * priorities-seed.ts writes the first priorities from the table a person keeps by hand
 * (dispatch://INFRA-2947/spec, R10). Its one argument is that table's Dispatch document
 * reference, so the script names no deployment, no document and no row of its own.
 *
 * One priority is written for each row whose `Where` cell does not begin with the word `DONE`,
 * in the table's order, with the row's `Priority` cell as the title, the row's own text as the
 * description and the row's `Owner` cell verbatim as the owner. Every issue key written anywhere
 * in the row, a key inside a `dispatch://` reference included, is related to that priority unless
 * its Dispatch status is `done`: `backlog` and `icebox` are related, because priority work that
 * stands in either is what the second fault of that spec reads.
 *
 * Every create names the priority's id (`priorityId`, in `priorities-seed-ids.ts`), made from the
 * title by the rule `POST /api/v1/priorities` makes an id by when a body names none
 * (`priorityIDFromTitle`): the title written as a slug, cut to at most `PRIORITY_ID_MAX`
 * characters at the end of a word. `packages/contracts/fixtures/priority-ids.json` holds the
 * cases of that rule, and both the server's tests and this seed's are held to them.
 *
 * Each priority's description ends in the record below, which is what the check of R10 reads and
 * what makes a second run write no second priority and no second relation. Each of its lines is
 * a paragraph of its own, so an edit of the description in the documents' editor, which joins
 * the lines of one paragraph with spaces, leaves each line whole:
 *
 *     ---
 *
 *     Table row 11 of dispatch://KEY/artifact/some-table-md, read at version 70.
 *
 *     Advances: INFRA-2947, INFRA-2969.
 *
 *     Not advanced: BET-1030 (done), FOO-12 (no issue carries this key).
 *
 * The row number is the idempotency key: a run against a table whose rows moved, or whose row was
 * given a new title under the same `#` cell, finds each row's priority by that number and writes
 * nothing, so the priorities it already wrote keep their order.
 *
 * A row whose number no record of this table names, and whose id a live priority already has
 * that carries no record of this table, is kept by its id: one a person wrote by hand, say. The
 * run writes no second priority, and writes nothing to that priority's title, description or
 * owner, because the person who keeps them is their one writer. It states each relation of the
 * row's keys that the priority does not hold yet, as it does for a row its record keeps, and
 * prints `row N: kept <id> (by its id)`, where a row its record keeps prints `row N: kept <id>`.
 *
 * Dispatch refuses some rows, and a run that met one part way would leave the rows above it
 * written. So before its first write, the run reads the table and the board and refuses the
 * table, naming every row it refuses, where:
 *
 * - a row that is not DONE has a title with no ASCII letter or digit, which gives no id;
 * - a row that is not DONE has a title over `ISSUE_TITLE_MAX` UTF-16 units, which Dispatch
 *   refuses as it refuses such an issue title;
 * - a row that is not DONE gives the id `none` or `any`, which the issue listing reads as a
 *   keyword in `?advances=`, so Dispatch refuses either as an id;
 * - two rows, DONE rows included, give one id after the cut;
 * - a row the run would write a priority for has an id that a gone (deleted) priority holds.
 *   `GET /api/v1/priorities` lists live priorities only, and Dispatch refuses a create of a gone
 *   priority's id (409 PRIORITY_EXISTS), so the run reads
 *   `GET /api/v1/priorities/{id}/history` for each such id, which answers 200 for any id a
 *   priority ever had and 404 for an id that is free;
 * - a row the run would write a priority for has an id that a live priority holds, and that
 *   priority carries another row's record of this table, as it does when that other row was
 *   given a new title after the seed wrote it. The refusal names both rows.
 *
 * Run: bun scripts/priorities-seed.ts dispatch://KEY/artifact/some-table-md
 */
import { parseArgs } from "node:util";
import type { ArtifactText, ArtifactVersionText } from "../packages/contracts/src/dispatch-api";
import { ISSUE_TITLE_MAX } from "../packages/contracts/src/dispatch-api-limits";
import {
  type ActiveDispatchConfig,
  activeDispatchSettings,
} from "../packages/envoy-client/src/dispatch-config";
import { DISPATCH_TOOL_DEADLINE_MS } from "../packages/envoy-client/src/dispatch-http";
import { parseDispatchRef } from "../packages/envoy-client/src/dispatch-owner";
import {
  sessionConfigFile,
  sessionDispatchSettings,
} from "../packages/envoy-client/src/session-settings";
import { priorityId, rowKeys, type TableRow } from "./priorities-seed-ids";

/** The columns this seed reads, as the table's header writes them; `\#` is how a table escapes
 *  the first one's name. A document holding no table with all four is refused. */
const COLUMNS = ["#", "priority", "owner", "where"] as const;

/** A row the seed passes over: its `Where` cell begins with the word `DONE`. */
const DONE_PATTERN = /^DONE\b/;

/** The record's first line, read back off a priority's description to find the row it was
 *  written from. The version is not part of the key: the same row read at a later version is the
 *  same row. */
const MARKER_PATTERN = /^Table row (\S+) of (dispatch:\/\/\S+?), read at version (?:\d+|none)\.$/m;

/** The two ids the issue listing reads as keywords in `?advances=` (`noAdvancesFilter` and
 *  `anyAdvancesFilter` in `issue_queries.go`), so Dispatch refuses either as a priority's id. */
const KEYWORD_IDS: readonly string[] = ["none", "any"];

/** What this run presents, and who it writes as. */
type DispatchConfig = Pick<ActiveDispatchConfig, "url" | "credential"> & {
  /** The session every write of this run states it was made by (`actor`). Dispatch refuses a
   *  write from a credential that proves no session and names none (`server.go`,
   *  `bearerSessionActor`: 400 ACTOR_KIND), and the shared bearer a pod carries is such a
   *  credential. One that proves a session of its own takes a body naming that same session,
   *  so naming it is right under either. */
  readonly session?: string;
};

/** Which row of which document a priority was written from, as its description records it. */
export interface RowMarker {
  readonly row: string;
  readonly document: string;
}

/** A key the row names that the seed did not relate, with why. */
export interface ExcludedKey {
  readonly key: string;
  /** The key's Dispatch status, or `no issue carries this key`. */
  readonly reason: string;
}

/** What one row of the table left behind. */
export interface SeededRow {
  readonly row: string;
  readonly title: string;
  /** The priority's id, the one this run wrote or the one the row already had. */
  readonly priority: string;
  /** How the run found a priority it did not write: `marker` where a run before this one wrote
   *  it and its record names this row's number, `id` where a priority that carries no record of
   *  this table already has the row's id. Null where this run wrote the priority. */
  readonly kept: "marker" | "id" | null;
  readonly related: readonly string[];
  readonly excluded: readonly ExcludedKey[];
  /** The keys this run wrote a relation for: fewer than `related` where one already stood. */
  readonly relations: readonly string[];
}

/** What one run wrote, which its own output prints and its test reads. */
export interface SeedReport {
  /** The table's document reference, without a version: what each record names. */
  readonly document: string;
  /** The version read, null for a document that carries none. */
  readonly version: number | null;
  /** The `#` cell of every row passed over, whose `Where` cell begins with `DONE`. */
  readonly passed: readonly string[];
  readonly rows: readonly SeededRow[];
}

/** What the seed reads off an issue and writes back: the issue's own `advances` relations and
 *  its status, as every issue read answers them (`model.Issue`, and `IssueDetails` in the
 *  TypeScript contract, which carries both fields). The script names the two fields it uses
 *  rather than the whole issue shape, because its request helper types each answer at the
 *  call. */
interface SeedIssue {
  readonly status: string;
  readonly advances: readonly { readonly id: string; readonly inherited: boolean }[] | null;
  readonly advances_revision: number;
}

/** A priority as `GET` and `POST /api/v1/priorities` answer one (`model.Priority`), of which the
 *  seed reads the id, to relate work to it, and the description, to find the row it holds. */
interface SeedPriority {
  readonly id: string;
  readonly description: string;
}

interface SeedPriorities {
  readonly priorities: readonly SeedPriority[];
}

/** The document a reference names: the owner to read it from, the slug, and the version where the
 *  reference pins one. */
interface DocumentReference {
  /** The reference as a record writes it: the owner and the slug, with no version. */
  readonly document: string;
  readonly owner:
    | { readonly kind: "issue"; readonly issue: string }
    | { readonly kind: "project"; readonly project: string };
  readonly slug: string;
  readonly version?: number;
}

/** `ref` as the document to read, or a refusal naming the grammar. */
export function parseDocumentReference(ref: string): DocumentReference {
  const parsed = parseDispatchRef(ref);
  if (parsed === null || (parsed.kind !== "artifact" && parsed.kind !== "spec")) {
    throw new Error(
      `${JSON.stringify(ref)} is no document reference: name the table as ` +
        "dispatch://KEY/artifact/SLUG, dispatch://PROJECT/artifact/SLUG or dispatch://KEY/spec"
    );
  }
  const holder = parsed.owner.kind === "issue" ? parsed.owner.issue : parsed.owner.project;
  const slug = parsed.kind === "spec" ? "spec" : parsed.id;
  return {
    document:
      parsed.kind === "spec"
        ? `dispatch://${holder}/spec`
        : `dispatch://${holder}/artifact/${slug}`,
    owner: parsed.owner,
    slug,
    ...(parsed.version === undefined ? {} : { version: parsed.version }),
  };
}

/** `line`'s cells where it is a table row, else null. A `\|` is a pipe in a cell rather than the
 *  edge of one, and the empty strings outside the row's own leading and trailing pipes are not
 *  cells. */
function tableCells(line: string): string[] | null {
  const text = line.trim();
  if (!text.startsWith("|")) return null;
  const cells: string[] = [];
  let cell = "";
  for (let at = 0; at < text.length; at += 1) {
    if (text[at] === "\\" && text[at + 1] === "|") {
      cell += "|";
      at += 1;
      continue;
    }
    if (text[at] === "|") {
      cells.push(cell);
      cell = "";
      continue;
    }
    cell += text[at];
  }
  cells.push(cell);
  cells.shift();
  if (cells.at(-1)?.trim() === "") cells.pop();
  return cells.map((part) => part.trim());
}

/** Where each column the seed reads stands in `header`, or null where one is missing. A header
 *  cell is matched with its escape dropped, so the `\#` a table writes is the `#` column. */
function columnPlaces(header: readonly string[]): Record<string, number> | null {
  const places: Record<string, number> = {};
  for (const column of COLUMNS) {
    const at = header.findIndex((cell) => cell.replace(/^\\/, "").toLowerCase() === column);
    if (at === -1) return null;
    places[column] = at;
  }
  return places;
}

/**
 * The rows of the first table in `markdown` carrying all four columns, in the table's order.
 * Refuses a document that holds no such table, a row with an empty `#` cell and two rows with
 * the same one, because the row number is what a second run reads to write nothing twice, and a
 * row with an empty `Priority` cell. The refusals that read a row's id are `placeRows`'s.
 */
export function parseTable(markdown: string): TableRow[] {
  const lines = markdown.split("\n");
  for (let at = 0; at + 1 < lines.length; at += 1) {
    const header = tableCells(lines[at] ?? "");
    const under = tableCells(lines[at + 1] ?? "");
    // A table's header stands over a row of `---` cells and nothing else.
    if (header === null || under === null) continue;
    if (under.length === 0 || !under.every((cell) => /^:?-+:?$/.test(cell))) continue;
    const places = columnPlaces(header);
    if (places === null) continue;
    const rows: TableRow[] = [];
    const numbers = new Set<string>();
    for (let row = at + 2; row < lines.length; row += 1) {
      const cells = tableCells(lines[row] ?? "");
      if (cells === null) break;
      const cell = (column: string): string => cells[places[column] ?? 0] ?? "";
      const number = cell("#");
      if (number === "") throw new Error(`table row ${rows.length + 1} has an empty # cell`);
      if (/\s/.test(number)) {
        throw new Error(
          `the # cell ${JSON.stringify(number)} holds whitespace: a row number is one word, ` +
            "because each priority's record names it"
        );
      }
      if (numbers.has(number)) throw new Error(`two rows carry the # cell ${number}`);
      numbers.add(number);
      const title = cell("priority");
      // A priority with no title is one Dispatch refuses, so the row is refused here, where
      // nothing has been written yet, rather than part way through a run.
      if (title === "") throw new Error(`row ${number} has an empty Priority cell`);
      rows.push({ number, title, owner: cell("owner"), where: cell("where") });
    }
    return rows;
  }
  throw new Error("this document holds no table with the columns #, Priority, Owner and Where");
}

/** The record this seed writes at the end of a priority's description. Each of its lines is a
 *  paragraph of its own: the priority's page edits the description in the documents' editor,
 *  which reads the lines of one paragraph as one line joined by spaces, and the marker is read
 *  back as a whole line (`MARKER_PATTERN`). */
export function priorityDescription(
  row: TableRow,
  document: string,
  version: number | null,
  related: readonly string[],
  excluded: readonly ExcludedKey[]
): string {
  const advances = related.length === 0 ? "none" : related.join(", ");
  const held = excluded.map((key) => `${key.key} (${key.reason})`);
  const record = [
    `Table row ${row.number} of ${document}, read at version ${version ?? "none"}.`,
    `Advances: ${advances}.`,
    `Not advanced: ${held.length === 0 ? "none" : held.join(", ")}.`,
  ];
  return [row.title, row.where, "---", ...record].filter((part) => part !== "").join("\n\n");
}

/** The row a priority was written from, as its description records it, or undefined for a
 *  priority this seed did not write. */
export function rowMarker(description: string): RowMarker | undefined {
  const marker = MARKER_PATTERN.exec(description);
  if (marker === null) return undefined;
  const [, row, document] = marker;
  if (row === undefined || document === undefined) return undefined;
  return { row, document };
}

/**
 * A request to Dispatch, presenting the bearer or the access token this machine traded its own
 * identity for, with the proof that goes with it. The priority list and write
 * (`model.Priority`), a priority's history (`GET /api/v1/priorities/{id}/history`) and a
 * document's text by its owner and slug have no DispatchClient method of their own, so every
 * route the seed reads or writes goes through this one helper rather than the client for the
 * issue read and patch and a second layer for the rest.
 */
async function request(
  config: DispatchConfig,
  method: string,
  path: string,
  body?: unknown
): Promise<Response> {
  const url = `${config.url}${path}`;
  // Who the write is by, stated in the body: see `DispatchConfig.session`. A read states
  // nothing, and neither does a run with no session to name.
  const stated =
    method === "GET" || config.session === undefined
      ? body
      : {
          ...((body ?? {}) as Record<string, unknown>),
          actor: { id: config.session, kind: "session" },
        };
  // One deadline for the credential and the request, the bound DispatchClient puts on each of
  // its own: a broker credential waiting on a pod's missing registration stops at it.
  const deadline = AbortSignal.timeout(DISPATCH_TOOL_DEADLINE_MS);
  return fetch(url, {
    method,
    headers: {
      ...(await config.credential.headers(method, url, undefined, deadline)),
      ...(stated === undefined ? {} : { "Content-Type": "application/json" }),
    },
    ...(stated === undefined ? {} : { body: JSON.stringify(stated) }),
    signal: deadline,
  });
}

async function send<T>(
  config: DispatchConfig,
  method: string,
  path: string,
  body?: unknown
): Promise<T> {
  const response = await request(config, method, path, body);
  if (!response.ok) {
    throw new Error(`${method} ${path}: ${response.status} ${await response.text()}`);
  }
  return (await response.json()) as T;
}

/** The answer of `GET path`, or undefined where Dispatch answers 404; any other failure throws. */
async function readOptional<T>(config: DispatchConfig, path: string): Promise<T | undefined> {
  const response = await request(config, "GET", path);
  if (response.status === 404) {
    await response.text();
    return undefined;
  }
  if (!response.ok) {
    throw new Error(`GET ${path}: ${response.status} ${await response.text()}`);
  }
  return (await response.json()) as T;
}

/** The issue `key`, or undefined where no issue carries that key. */
async function readIssue(config: DispatchConfig, key: string): Promise<SeedIssue | undefined> {
  return readOptional<SeedIssue>(config, `/api/v1/issues/${encodeURIComponent(key)}`);
}

/**
 * Every issue the run reads, read once however many rows name it, and kept current as relations
 * are written: a second relation on one issue carries the revision the first write answered.
 */
class Issues {
  readonly #config: DispatchConfig;
  readonly #read = new Map<string, SeedIssue | undefined>();

  constructor(config: DispatchConfig) {
    this.#config = config;
  }

  async issue(key: string): Promise<SeedIssue | undefined> {
    if (!this.#read.has(key)) this.#read.set(key, await readIssue(this.#config, key));
    return this.#read.get(key);
  }

  /** Reads every key of `keys` that is not read yet, at once rather than one after another, so
   *  the rows that name them read them from this cache. */
  async readAll(keys: Iterable<string>): Promise<void> {
    const wanted = [...new Set(keys)].filter((key) => !this.#read.has(key));
    const read = await Promise.all(wanted.map((key) => readIssue(this.#config, key)));
    for (const [place, key] of wanted.entries()) this.#read.set(key, read[place]);
  }

  /**
   * States that `key` advances `priority`, where it does not already. Answers whether this wrote
   * a relation. The write carries the revision the read it answers returned, so a set changed
   * since that read is refused rather than written over.
   */
  async relate(key: string, priority: string): Promise<boolean> {
    const issue = await this.issue(key);
    if (issue === undefined) throw new Error(`no issue carries the key ${key}`);
    // The issue's own relations: an inherited one is no relation of its own, so no write of
    // this issue states or retracts it.
    const own = (issue.advances ?? []).filter((entry) => !entry.inherited).map((entry) => entry.id);
    if (own.includes(priority)) return false;
    const written = await send<SeedIssue>(
      this.#config,
      "PATCH",
      `/api/v1/issues/${encodeURIComponent(key)}`,
      { advances: [...own, priority], advances_revision: issue.advances_revision }
    );
    this.#read.set(key, written);
    return true;
  }
}

/** The table's text and the version it was read at, read in one request: the document's own text
 *  route, by the owner and slug the reference names, which the check of R10 reads it through as
 *  well. A reference pinning a version reads that version. */
async function readTable(
  config: DispatchConfig,
  reference: DocumentReference
): Promise<{ markdown: string; version: number | null }> {
  const owner = reference.owner;
  const holder =
    owner.kind === "issue"
      ? `issues/${encodeURIComponent(owner.issue)}`
      : `projects/${encodeURIComponent(owner.project)}`;
  const document = `/api/v1/${holder}/artifacts/${encodeURIComponent(reference.slug)}`;
  if (reference.version !== undefined) {
    const path = `${document}/versions/${reference.version}`;
    const pinned = await send<ArtifactVersionText>(config, "GET", path);
    return { markdown: pinned.markdown, version: pinned.number };
  }
  const text = await send<ArtifactText>(config, "GET", `${document}/text`);
  return { markdown: text.markdown, version: text.version };
}

/** Whether a priority of the id `id` was ever written, a gone one included: its history answers
 *  200 for any id a priority row carries, a deleted priority's too, and 404 for an id none
 *  carries (`priorityHistory` in `priorities.go`). */
async function priorityEverWritten(config: DispatchConfig, id: string): Promise<boolean> {
  const path = `/api/v1/priorities/${encodeURIComponent(id)}/history`;
  return (await readOptional<unknown>(config, path)) !== undefined;
}

/** A row the run writes, as the board stands before its first write: the priority it keeps,
 *  or, where `kept` is null, the id it writes a priority under. */
interface PlacedRow {
  readonly row: TableRow;
  readonly priority: string;
  readonly kept: SeededRow["kept"];
}

/**
 * Where each row that is not DONE stands before the run writes anything: kept by the record of
 * its row number, kept by its id, or written under its id. Refuses the table, naming every row
 * Dispatch would refuse part way through a run, as the header says, so a refused table leaves
 * nothing written.
 */
async function placeRows(
  config: DispatchConfig,
  document: string,
  rows: readonly TableRow[],
  board: readonly SeedPriority[]
): Promise<PlacedRow[]> {
  /** Each row number a record of this table names, with the priority that carries it. */
  const byRow = new Map<string, string>();
  /** Each live priority's id, with the row number its record of this table names, if any. */
  const recordOf = new Map<string, string | undefined>();
  for (const priority of board) {
    const marker = rowMarker(priority.description);
    const row = marker?.document === document ? marker.row : undefined;
    if (row !== undefined) byRow.set(row, priority.id);
    recordOf.set(priority.id, row);
  }
  const refused: string[] = [];
  /** Each id a row gives, and the first row to give it. */
  const firstOf = new Map<string, TableRow>();
  const placed: PlacedRow[] = [];
  for (const row of rows) {
    const before = refused.length;
    const id = priorityId(row.title);
    const first = id === "" ? undefined : firstOf.get(id);
    // Every row, a DONE one included, as the row a later run writes once its Where cell moves.
    if (first !== undefined) {
      refused.push(
        `rows ${first.number} and ${row.number} give the one id ${id} after the cut: ` +
          `${JSON.stringify(first.title)} and ${JSON.stringify(row.title)} differ only where ` +
          "an id does not, or past where it is cut, so one of the two has to be renamed"
      );
    } else if (id !== "") {
      firstOf.set(id, row);
    }
    if (DONE_PATTERN.test(row.where)) continue;
    if (id === "") {
      refused.push(
        `row ${row.number}: the title ${JSON.stringify(row.title)} holds no ASCII letter or ` +
          "digit, so it gives no id"
      );
    }
    if (row.title.length > ISSUE_TITLE_MAX) {
      refused.push(
        `row ${row.number}: the title is ${row.title.length} characters, and Dispatch refuses a ` +
          `priority title of more than ${ISSUE_TITLE_MAX}, as it refuses such an issue title`
      );
    }
    if (KEYWORD_IDS.includes(id)) {
      refused.push(
        `row ${row.number}: the title ${JSON.stringify(row.title)} gives the id ${id}, which ` +
          `Dispatch refuses, because the issue listing reads advances=${id} as a keyword`
      );
    }
    if (refused.length > before) continue;
    // The row number first: a row given a new title under its `#` cell keeps its priority.
    const marked = byRow.get(row.number);
    if (marked !== undefined) {
      placed.push({ row, priority: marked, kept: "marker" });
      continue;
    }
    if (!recordOf.has(id)) {
      placed.push({ row, priority: id, kept: null });
      continue;
    }
    const other = recordOf.get(id);
    if (other === undefined) {
      placed.push({ row, priority: id, kept: "id" });
      continue;
    }
    refused.push(
      `rows ${row.number} and ${other}: the id ${id} that row ${row.number} gives is held by a ` +
        `live priority that carries the record of row ${other} of this table, as it does when ` +
        `row ${other} was given a new title after the seed wrote it, so Dispatch would refuse ` +
        `the create of row ${row.number} (409 PRIORITY_EXISTS) and row ${row.number} has to be ` +
        "renamed"
    );
  }
  // The board lists live priorities only, and Dispatch keeps a deleted priority's id, so each id
  // the run would write a priority under is read in its history: 200 is a gone priority's.
  const created = placed.filter((one) => one.kept === null);
  const written = await Promise.all(
    created.map((one) => priorityEverWritten(config, one.priority))
  );
  for (const [at, one] of created.entries()) {
    if (!written[at]) continue;
    refused.push(
      `row ${one.row.number}: the id ${one.priority} is a gone priority's, which Dispatch keeps ` +
        "and refuses a create of (409 PRIORITY_EXISTS), so the row has to be renamed"
    );
  }
  if (refused.length > 0) {
    throw new Error(
      `the seed wrote nothing, because Dispatch would refuse these rows of ${document} part ` +
        `way through a run:\n${refused.join("\n")}`
    );
  }
  return placed;
}

/**
 * Writes the priorities of `reference`'s table, and the relations from the work each row names.
 * A row whose priority a run before this one wrote keeps that priority, its order and its text,
 * as a row whose id a priority that carries no record of this table already has keeps that one,
 * and this run states only the relations that are missing from it.
 */
export async function seedPriorities(
  config: DispatchConfig,
  reference: string
): Promise<SeedReport> {
  const document = parseDocumentReference(reference);
  // The table and the priorities already written are read at once: neither names the other.
  const [table, board] = await Promise.all([
    readTable(config, document),
    send<SeedPriorities>(config, "GET", "/api/v1/priorities"),
  ]);
  const rows = parseTable(table.markdown);
  const placed = await placeRows(config, document.document, rows, board.priorities);
  const issues = new Issues(config);
  // Every key the rows this run writes name, read at once; a DONE row is passed over, so no key
  // only it names is read at all.
  await issues.readAll(placed.flatMap(({ row }) => rowKeys(row)));
  const seeded: SeededRow[] = [];
  for (const { row, priority: id, kept } of placed) {
    const related: string[] = [];
    const excluded: ExcludedKey[] = [];
    for (const key of rowKeys(row)) {
      const issue = await issues.issue(key);
      if (issue === undefined) excluded.push({ key, reason: "no issue carries this key" });
      else if (issue.status === "done") excluded.push({ key, reason: issue.status });
      else related.push(key);
    }
    const priority =
      kept !== null
        ? id
        : (
            await send<SeedPriority>(config, "POST", "/api/v1/priorities", {
              id,
              title: row.title,
              description: priorityDescription(
                row,
                document.document,
                table.version,
                related,
                excluded
              ),
              owner: row.owner,
            })
          ).id;
    const relations: string[] = [];
    for (const key of related) if (await issues.relate(key, priority)) relations.push(key);
    seeded.push({
      row: row.number,
      title: row.title,
      priority,
      kept,
      related,
      excluded,
      relations,
    });
  }
  const passed = rows.filter((row) => DONE_PATTERN.test(row.where)).map((row) => row.number);
  return { document: document.document, version: table.version, passed, rows: seeded };
}

function parseArguments(argv: readonly string[]): string {
  const { positionals } = parseArgs({ args: [...argv], allowPositionals: true, strict: true });
  const [reference] = positionals;
  if (positionals.length !== 1 || reference === undefined) {
    throw new Error(
      "usage: bun scripts/priorities-seed.ts <dispatch://KEY/artifact/SLUG>: one argument, the " +
        "priorities table's document reference"
    );
  }
  return reference;
}

function printReport(report: SeedReport): void {
  console.log(`${report.document}, read at version ${report.version ?? "none"}`);
  for (const row of report.rows) {
    const found = row.kept === null ? "wrote" : "kept";
    const held = row.excluded.map((key) => `${key.key} (${key.reason})`);
    console.log(
      `row ${row.row}: ${found} ${row.priority}${row.kept === "id" ? " (by its id)" : ""}; ` +
        `related ${row.relations.length === 0 ? "none" : row.relations.join(", ")} of ` +
        `${row.related.length === 0 ? "none" : row.related.join(", ")}; ` +
        `not related ${held.length === 0 ? "none" : held.join(", ")}`
    );
  }
  const wrote = report.rows.filter((row) => row.kept === null).length;
  const byId = report.rows.filter((row) => row.kept === "id").length;
  console.log(
    `${report.rows.length} rows, ${wrote} priorities written, ${byId} kept by their id, ` +
      `${report.rows.reduce((count, row) => count + row.relations.length, 0)} relations written, ` +
      `${report.passed.length} rows passed over (DONE): ${report.passed.join(", ") || "none"}`
  );
}

async function run(reference: string): Promise<void> {
  const { dispatch: resolution, warnings } = sessionDispatchSettings(
    sessionConfigFile("command", process.env),
    process.env,
    { cwd: process.cwd() }
  );
  // A checkout's OpenCode-era file, which is not read, is said on every run while it is there.
  for (const warning of warnings) console.error(`dispatch: ${warning}`);
  // The one reading of a resolution, as every other caller of it reads one: a file, URL or
  // token that prevents Dispatch throws `dispatch config: <reason>` here, and a machine that
  // names no Dispatch at all answers null.
  const config = activeDispatchSettings(resolution);
  if (config === null) {
    throw new Error(
      "Dispatch is not configured: name dispatch.url and dispatch.token in the session's " +
        "envoy.json, or set DISPATCH_URL and DISPATCH_TOKEN"
    );
  }
  const session = process.env.DISPATCH_SESSION_ID?.trim();
  if (session === undefined || session === "") {
    throw new Error(
      "DISPATCH_SESSION_ID names no session: every write this seed makes states who made it, " +
        "and the shared bearer a pod carries proves no session of its own"
    );
  }
  printReport(await seedPriorities({ ...config, session }, reference));
}

if (import.meta.main) await run(parseArguments(Bun.argv.slice(2)));
