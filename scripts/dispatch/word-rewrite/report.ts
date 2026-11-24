import { ownerName } from "./dispatch-api.ts";
import type { Examined, Item } from "./inventory.ts";
import { REASONS } from "./places.ts";
import type { RefusedOccurrence } from "./state.ts";
import { VERDICT_CODES, type VerdictCode, verdictName } from "./verdicts.ts";

/**
 * What a run prints: one line per occurrence, with the place that leaves it, the judge's verdict
 * a state holds for it, or `not-judged`; the counts by kind of item and project and by reason;
 * the check blocks of open issues that name an old name of the map; and, from the run, the
 * daemons, Dispatch's build, the pace and the size the state would reach. No line holds a text of
 * an item: keys, ids, slugs, offsets and fixed words alone.
 */

/** What the run knows of an item that holds occurrences no place or verdict leaves. */
export interface Waiting {
  readonly item: Item;
  readonly occurrences: number;
}

/** The counts and lines of one run, added to item by item. */
export class Tally {
  readonly lines: string[] = [];
  readonly checkLines: string[] = [];
  readonly byKind = new Map<string, { occurrences: number; notJudged: number }>();
  readonly byReason = new Map<string, number>();
  readonly byVerdict = new Map<string, number>();
  readonly waiting: Waiting[] = [];
  notJudged = 0;
  /** Occurrences whose two answers failed: for a person to change by hand. */
  judgeRefused = 0;
  items = 0;
  /** The items this run examined. */
  readonly examined = new Set<string>();

  /** Adds one examined item; `verdicts` are the state's. */
  add(examined: Examined, verdicts: ReadonlyMap<string, VerdictCode>): void {
    this.examined.add(examined.item.id);
    const item = examined.item;
    this.items += 1;
    const issue = item.issue?.key ?? "-";
    const document = item.artifact?.slug ?? "-";
    for (const check of examined.checkNames) {
      this.checkLines.push(
        `check-name issue=${issue} document=${document} block=${check.block} old=${check.old}`
      );
    }
    if (examined.occurrences.length === 0) return;
    const kindKey = `${item.kind} ${item.project}`;
    const counts = this.byKind.get(kindKey) ?? { occurrences: 0, notJudged: 0 };
    let waiting = 0;
    for (const occurrence of examined.occurrences) {
      counts.occurrences += 1;
      let place: string;
      const code = occurrence.key === null ? undefined : verdicts.get(occurrence.key);
      if (occurrence.reason !== null) {
        place = `reason=${occurrence.reason}`;
        this.byReason.set(occurrence.reason, (this.byReason.get(occurrence.reason) ?? 0) + 1);
      } else if (code === VERDICT_CODES["judge-refused"]) {
        place = "judge-refused";
        this.judgeRefused += 1;
      } else if (code !== undefined) {
        const name = verdictName(code) ?? code;
        place = `verdict=${name}`;
        this.byVerdict.set(name, (this.byVerdict.get(name) ?? 0) + 1);
      } else {
        place = "not-judged";
        counts.notJudged += 1;
        this.notJudged += 1;
        waiting += 1;
      }
      const at = occurrence.inName ? "name" : String(occurrence.byteStart);
      const name = occurrence.name === null ? "" : ` name=${occurrence.name.old}`;
      this.lines.push(
        `occurrence kind=${item.kind} project=${item.project} issue=${issue} document=${document} byte=${at} ${place}${name}`
      );
    }
    this.byKind.set(kindKey, counts);
    if (waiting > 0) this.waiting.push({ item, occurrences: waiting });
  }

  /** Lists, from the state, each occurrence kept as `judge-refused` whose item this run did not
   *  read: the state names its item and its key, and the run has not seen its text change. */
  addKeptRefused(refused: readonly RefusedOccurrence[]): void {
    for (const occurrence of refused) {
      if (this.examined.has(occurrence.id)) continue;
      this.judgeRefused += 1;
      this.lines.push(
        `occurrence project=${occurrence.project} issue=${occurrence.issue ?? "-"} item=${occurrence.id} key=${occurrence.key} judge-refused from=state`
      );
    }
  }

  /** The counts, in a fixed order: kind and project, reason, verdict, the check names. */
  summary(): string[] {
    const lines = [...this.byKind.entries()]
      .sort(([a], [b]) => a.localeCompare(b))
      .map(
        ([key, counts]) =>
          `count ${key} occurrences=${counts.occurrences} not-judged=${counts.notJudged}`
      );
    for (const reason of REASONS) lines.push(`reason ${reason} ${this.byReason.get(reason) ?? 0}`);
    for (const [name, count] of [...this.byVerdict.entries()].sort()) {
      lines.push(`verdict ${name} ${count}`);
    }
    lines.push(`judge-refused ${this.judgeRefused}`);
    lines.push(`not-judged ${this.notJudged}`);
    lines.push(`items-read ${this.items}`);
    return lines;
  }

  /** The report's section of check blocks that name an old name, counted. */
  checkSection(): string[] {
    return [...this.checkLines, `check-names ${this.checkLines.length}`];
  }

  /** The owners of the items that wait for the judge, each once. */
  owners(): Item["owner"][] {
    const seen = new Map<string, Item["owner"]>();
    for (const { item } of this.waiting) seen.set(ownerName(item.owner), item.owner);
    return [...seen.values()];
  }
}
