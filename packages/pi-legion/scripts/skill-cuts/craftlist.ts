/**
 * The craft list (README.md, "The craft list"), and its command `craft-lines`:
 *
 *   bun scripts/skill-cuts/cli.ts craft-lines --list <craft-list.json>
 *
 * The list is written by hand: the properties of a skill that both sets' own guides state, each
 * with the line of each set's guide that states it, at the set's pinned commit. Its shape:
 *
 *   {"schemaVersion": 1,
 *    "sets": {"<set>": {"repo": "<owner/name>", "commit": "<40 hex>", "guide": ["<path>", ...]}},
 *    "properties": [{"id": "C<n>", "property": "<one line>", "checkedBy": "reader"|"behaviour",
 *      "readerLooksFor": "<text>", "lines": [{"set", "path", "line", "lineEnd"?}, ...],
 *      "example"?: {"set", "path", "line"}, "record"?: "https://..."}]}
 *
 * `parseCraftList` refuses a property whose lines do not cite every set of `sets` (a property only
 * one set's guide states is no property of the list), a line of a set the list does not give, an id
 * given twice or not of the form `C<n>`, a `checkedBy` other than `reader` or `behaviour`, a reader
 * property with no `example` (one skill line of theirs that shows it), a behaviour property with no
 * `record` (one link to a record of its testing), and a `lineEnd` before its `line`.
 *
 * `craft-lines` prints, for each property, each cited line (or range) and each example line, with
 * the text of those lines in the set's repository at its commit, read from GitHub through `gh` as
 * sets.ts reads a file (`SetReads.text`, each file once). It exits 1 naming each cited line past the
 * end of its file and each path the commit does not hold, 2 when it refuses its arguments or the
 * list, and 0 otherwise.
 */
import { readFileSync } from "node:fs";
import { messageFor } from "@legion/envoy-client/errors";
import { z } from "zod";
import { textLines } from "./cases";
import { GhRetries, parse, Refused, required, schemaFaults } from "./common";
import type { Gh } from "./copies";
import { SetReads } from "./sets";

const OneLine = z
  .string()
  .refine((text) => text.trim() !== "" && !text.includes("\n"), "is not one non-blank line");
const Path = z.string().min(1);

/** A line, or a range of lines, of a set's file that states a property. */
const CitedLine = z
  .strictObject({
    set: z.string().min(1),
    path: Path,
    line: z.int().positive(),
    lineEnd: z.int().positive().optional(),
  })
  .superRefine((cited, ctx) => {
    if (cited.lineEnd !== undefined && cited.lineEnd < cited.line) {
      ctx.addIssue({
        code: "custom",
        path: ["lineEnd"],
        message: `is before its line, ${cited.line}`,
      });
    }
  });
type CitedLine = z.infer<typeof CitedLine>;

/** A line of a skill of a set that shows a property. */
const ExampleLine = z.strictObject({
  set: z.string().min(1),
  path: Path,
  line: z.int().positive(),
});

const Property = z.strictObject({
  id: z.string().regex(/^C[1-9]\d*$/, "is not C<n>"),
  property: OneLine,
  checkedBy: z.enum(["reader", "behaviour"]),
  readerLooksFor: z.string().min(1),
  lines: z.array(CitedLine).min(1),
  example: ExampleLine.optional(),
  record: z
    .string()
    .regex(/^https:\/\/\S+$/, "is not an https:// link")
    .optional(),
});
export type CraftProperty = z.infer<typeof Property>;

export const CraftList = z
  .strictObject({
    schemaVersion: z.literal(1),
    sets: z.record(
      z.string().min(1),
      z.strictObject({
        repo: z.string().regex(/^[^/\s]+\/[^/\s]+$/, "is not owner/name"),
        commit: z.string().regex(/^[0-9a-f]{40}$/, "is not a commit's 40 hex digits"),
        guide: z.array(Path).min(1),
      })
    ),
    properties: z.array(Property).min(1),
  })
  .superRefine((list, ctx) => {
    const refuse = (at: PropertyKey[], message: string) =>
      ctx.addIssue({ code: "custom", path: at, message });
    const sets = Object.keys(list.sets);
    if (sets.length === 0) refuse(["sets"], "names no set");
    const ids = new Set<string>();
    for (const [index, property] of list.properties.entries()) {
      const at = ["properties", index];
      if (ids.has(property.id)) refuse([...at, "id"], `gives the id ${property.id} twice`);
      ids.add(property.id);
      for (const [n, cited] of property.lines.entries()) {
        if (!Object.hasOwn(list.sets, cited.set))
          refuse([...at, "lines", n, "set"], "is no set of the list");
      }
      const cites = new Set(property.lines.map((cited) => cited.set));
      for (const set of sets) {
        if (!cites.has(set)) {
          refuse(
            [...at, "lines"],
            `cites no line of the set ${set}: a property is on the list only when each set's guide states it`
          );
        }
      }
      if (property.example !== undefined && !Object.hasOwn(list.sets, property.example.set))
        refuse([...at, "example", "set"], "is no set of the list");
      if (property.checkedBy === "reader" && property.example === undefined) {
        refuse(
          [...at, "example"],
          "is missing: a reader property names one skill line of theirs that shows it"
        );
      }
      if (property.checkedBy === "behaviour" && property.record === undefined) {
        refuse(
          [...at, "record"],
          "is missing: a behaviour property links one record of its testing"
        );
      }
    }
  });
export type CraftList = z.infer<typeof CraftList>;

/** The craft list the JSON text `text` holds. Throws naming each fault. */
export function parseCraftList(text: string): CraftList {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch (error) {
    throw new Error(`it is not JSON: ${messageFor(error)}`);
  }
  const parsed = CraftList.safeParse(value);
  if (!parsed.success) throw new Error(schemaFaults(parsed.error));
  return parsed.data;
}

/** The properties of `list` that a reader checks on a skill's text, in the list's order. */
export function readerProperties(list: CraftList): CraftProperty[] {
  return list.properties.filter((property) => property.checkedBy === "reader");
}

// `craft-lines`.

const USAGE = "usage: cli.ts craft-lines --list <craft-list.json>";

/** What `main` uses of the world. */
export interface CraftLinesIo {
  out(text: string): void;
  /** Writes one line to standard error. */
  err(line: string): void;
  readonly gh: Gh;
  /** The wait before a gh call is sent again; Bun.sleep unless given. */
  sleep?(ms: number): Promise<void>;
}

/** `path:line`, or `path:line-end` for a range. */
function where(cited: CitedLine): string {
  return `${cited.path}:${cited.line}${cited.lineEnd === undefined ? "" : `-${cited.lineEnd}`}`;
}

/** The command. Returns the exit status: 0 (each cited line printed), 1 (a cited line that cannot
 *  be read, or a GitHub read that failed), 2 (refused). */
export async function main(argv: readonly string[], io: CraftLinesIo): Promise<number> {
  try {
    const values = parse(argv, { list: { type: "string" } }, USAGE);
    const { list: file } = required(values, ["list"], USAGE);
    let text: string;
    try {
      text = readFileSync(file, "utf8");
    } catch (error) {
      throw new Refused(`cannot read the craft list ${file}: ${messageFor(error)}`);
    }
    let list: CraftList;
    try {
      list = parseCraftList(text);
    } catch (error) {
      throw new Refused(`the craft list ${file} is refused: ${messageFor(error)}`);
    }
    const reads = new SetReads(
      io.gh,
      new GhRetries(
        { sleep: (ms) => io.sleep?.(ms) ?? Bun.sleep(ms), err: (line) => io.err(line) },
        "craft-lines"
      )
    );
    // Each file once: many properties cite one guide file.
    const files = new Map<string, Promise<string[] | undefined>>();
    const linesOf = (set: string, path: string) => {
      const { repo, commit } = list.sets[set] ?? { repo: "", commit: "" };
      const key = `${set}\n${path}`;
      let read = files.get(key);
      if (read === undefined) {
        read = reads
          .text(repo, commit, path)
          .then((each) => (each === undefined ? undefined : textLines(each)));
        files.set(key, read);
      }
      return read;
    };
    const faults: string[] = [];
    const out: string[] = Object.entries(list.sets).map(
      ([set, { repo, commit }]) => `${set}: ${repo} at ${commit}\n`
    );
    /** The lines of `cited`, each `    <n>: <text>`, under its heading, and its fault if it has
     *  one. */
    const show = async (id: string, label: string, cited: CitedLine) => {
      const { repo, commit } = list.sets[cited.set] ?? { repo: "", commit: "" };
      const heading = `  ${label}${cited.set} ${where(cited)}\n`;
      const name = `${id} ${label}${cited.set} ${where(cited)}`;
      const lines = await linesOf(cited.set, cited.path);
      if (lines === undefined) {
        return {
          text: `${heading}    (no such file at the commit)\n`,
          fault: `${name}: ${repo} has no such file at ${commit}`,
        };
      }
      const last = cited.lineEnd ?? cited.line;
      if (last > lines.length) {
        return {
          text: `${heading}    (past the end of the file, line ${lines.length})\n`,
          fault: `${name} is past the end of the file, line ${lines.length}, at ${commit}`,
        };
      }
      const shown = lines
        .slice(cited.line - 1, last)
        .map((line, at) => `    ${cited.line + at}: ${line}\n`);
      return { text: heading + shown.join(""), fault: undefined };
    };
    for (const property of list.properties) {
      const example = property.example;
      const shown = await Promise.all([
        ...property.lines.map((cited) => show(property.id, "", cited)),
        ...(example === undefined ? [] : [show(property.id, "example ", example)]),
      ]);
      out.push(
        `\n${property.id} (${property.checkedBy}): ${property.property}\n`,
        ...shown.map((each) => each.text),
        ...(property.record === undefined ? [] : [`  record ${property.record}\n`])
      );
      faults.push(...shown.flatMap((each) => (each.fault === undefined ? [] : [each.fault])));
    }
    io.out(out.join(""));
    if (faults.length === 0) return 0;
    for (const fault of faults) io.err(`craft-lines: ${fault}`);
    io.err(
      `craft-lines: ${faults.length} cited line${faults.length === 1 ? "" : "s"} cannot be read at their commits`
    );
    return 1;
  } catch (error) {
    if (error instanceof Refused) {
      io.err(`craft-lines: ${error.message}`);
      return 2;
    }
    io.err(`craft-lines: ${messageFor(error)}`);
    return 1;
  }
}
