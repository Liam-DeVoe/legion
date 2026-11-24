import { daemonAtLeast, describeReading, dispatchBuild, readDaemons } from "./daemons.ts";
import { ReadError, Whoami } from "./dispatch-api.ts";
import { type Context, examine, type Item, readItems } from "./inventory.ts";
import type { Deps } from "./main.ts";
import { builtInMap } from "./name-map.ts";
import { Pace, PaceLog } from "./pace.ts";
import {
  keepRefused,
  keepWaiting,
  type RunState,
  readStateDocument,
  type StateDocument,
  StateTooLarge,
  StateWriteError,
  writeStateDocument,
} from "./state.ts";
import { VERDICT_KEY } from "./verdicts.ts";
import { type WriteContext, writeItem, writerRefusal } from "./write.ts";

/**
 * The settle command (spec version 43): write mode with the tester's answers in place of the
 * model's, for the occurrences whose two answers failed, which the state keeps with the code `j`.
 * Each answer has the shape of a model's answer (`judge.ts`): `{"rewrite": "<the paragraph>"}`,
 * that with `"left": {"<offset>": "<place>"}` for each marked occurrence the new text keeps, or
 * `{"allowed": "<place>"}`, where the place is one by what it is (a quote, the word as a word,
 * another team's technical meaning, a kind of R1). The answers file is one JSON object that gives
 * each answer under the verdict key of a judge-refused occurrence of its paragraph, as a report
 * lists it (`item=<id> key=<key> at=<offset>`).
 *
 * It asks no model. It takes the answers as write mode takes the model's: the same check
 * (`checkAnswer`), the same pace, the same read of the item just before its write, the same
 * verdicts saved, every answer of one item in one write. When an answer fails the check, the pace
 * holds the write back, or Dispatch refuses it, the item is written and saved nothing, and the
 * command prints why, and, for the pace, when it may write. An occurrence that a place a program
 * tells holds needs no settling.
 */

export interface SettleOptions {
  readonly mode: "settle";
  readonly stateDocument: StateDocument;
  readonly answers: string;
  readonly summary: string;
  readonly mergeCommit: string;
  readonly extraMap: string | undefined;
  readonly requestLog: string | undefined;
  readonly stateAddress: ReadonlyMap<string, string>;
  readonly noDaemon: ReadonlySet<string>;
}

/** The answers file: one JSON object, each answer under a verdict key; null where it is not. */
function parseAnswers(text: string): Map<string, unknown> | null {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    return null;
  }
  if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
  const entries = Object.entries(value);
  if (entries.length === 0 || entries.some(([key]) => !VERDICT_KEY.test(key))) return null;
  return new Map(entries);
}

/** A copy of the state that one item's settle changes, kept only when the item is settled. */
function trialOf(state: RunState): RunState {
  return {
    ...state,
    finished: [...state.finished],
    verdicts: new Map(state.verdicts),
    held: [...state.held],
    waiting: [...state.waiting],
    refused: [...state.refused],
    sessions: [...state.sessions],
  };
}

/** One settle; answers the exit status: 0 when every answered item is settled, 1 when an answer
 *  is refused or held back, 2 when the credential may not write, the answers file is not answers
 *  or a read fails. */
export async function runSettle(
  options: SettleOptions,
  answersText: string,
  deps: Deps
): Promise<number> {
  const { reads, out } = deps;
  try {
    const whoami = Whoami.parse(await reads.whoami());
    const refusal = writerRefusal(whoami, deps.session);
    if (refusal !== null || deps.session === undefined || deps.writes === undefined) {
      out(`refused: ${refusal ?? "settle runs with writes"}`);
      return 2;
    }
    const session = deps.session;
    const answers = parseAnswers(answersText);
    if (answers === null) {
      out("refused: the answers file is not one JSON object of answers under verdict keys");
      return 2;
    }
    let { state, token } = await readStateDocument(reads, options.stateDocument);
    let sessionSaved = state.sessions.includes(session);
    if (!sessionSaved) state.sessions.push(session);
    const io = { reads, writes: deps.writes };

    // Each answer's item, from the state's judge-refused occurrences.
    let code = 0;
    const items = new Map<string, { project: string; issue: string | null; id: string }>();
    for (const key of answers.keys()) {
      const entry = state.refused.find((candidate) => candidate.key === key);
      if (entry === undefined) {
        out(`refused: key ${key}: the state keeps no judge-refused occurrence under it`);
        code = 1;
        continue;
      }
      items.set(entry.id, entry);
    }
    const read = await readItems(reads, [...items.values()]);

    // The places of each item's project, read as a run reads them.
    const build = await dispatchBuild(await reads.healthz(), options.mergeCommit, deps.ancestry);
    out(`dispatch commit=${build.commit ?? "null"} holds-merge=${build.holdsMerge ? "yes" : "no"}`);
    const projects = [...new Set(read.map((item) => item.project))];
    const daemons = await readDaemons(projects, reads, deps.daemonStates, options);
    for (const [project, reading] of daemons) out(`daemon ${project} ${describeReading(reading)}`);
    const reachOf = (project: string) => ({
      daemonAtLeast: (contract: number) => daemonAtLeast(daemons.get(project), contract),
      dispatchHoldsMerge: build.holdsMerge,
    });
    const entries = [...builtInMap(deps.contract), ...deps.extraEntries];
    const scope = whoami.kind === "agent" ? whoami.project : null;
    const context: Context = { reads, entries, reachOf, scope, tree: new Set() };
    const pace = new Pace(deps.clock);
    const shared = {
      reads,
      writes: deps.writes,
      answerer: { kind: "tester", answers, why: (line: string) => out(`refused: ${line}`) },
      pace,
      paceLog: new PaceLog(reads, pace, new Set(state.sessions), deps.clock),
      // The session's id is saved before the first write, with nothing an answer changed.
      beforeWrite: async () => {
        if (sessionSaved) return;
        sessionSaved = true;
        token = await writeStateDocument(io, options.stateDocument, state, token);
      },
      clock: deps.clock,
      entries,
      reachOf,
      summary: options.summary,
      remaining: Number.POSITIVE_INFINITY,
      pauseFile: undefined,
      statusLines: new Map(),
      retried: new Set<string>(),
      calls: { first: 0, second: 0, again: 0, againOccurrences: 0 },
    } satisfies Omit<WriteContext, "state">;

    let settledAny = false;
    for (const [id, entry] of items) {
      const where = `item=${id}`;
      const item: Item | undefined = read.find((candidate) => candidate.id === id);
      if (item === undefined) {
        out(`refused: ${where}: the item is gone`);
        code = 1;
        continue;
      }
      const examined = await examine(context, item);
      // Each answered key of the item must name an occurrence that no place a program tells
      // holds, and one paragraph takes one answer.
      const problems: string[] = [];
      const paragraphs = new Set<number>();
      for (const key of answers.keys()) {
        if (!state.refused.some((one) => one.id === entry.id && one.key === key)) continue;
        const occurrence = examined.occurrences.find((candidate) => candidate.key === key);
        if (occurrence === undefined || occurrence.paragraph === null) {
          problems.push(`key ${key}: no occurrence holds it now: its text changed`);
        } else if (occurrence.reason !== null) {
          problems.push(
            `key ${key}: it is in ${occurrence.reason}, a place a program tells, which needs no settling`
          );
        } else if (paragraphs.has(occurrence.paragraph.start)) {
          problems.push(`key ${key}: its paragraph has an answer already: one answer a paragraph`);
        } else {
          paragraphs.add(occurrence.paragraph.start);
        }
      }
      if (problems.length > 0) {
        for (const problem of problems) out(`refused: ${where} ${problem}`);
        code = 1;
        continue;
      }
      const trial = trialOf(state);
      keepWaiting(trial, examined);
      keepRefused(trial, examined);
      const result = await writeItem({ ...shared, state: trial }, examined);
      if (result === "written" || result === "judged-left") {
        state = trial;
        settledAny = true;
        out(`settled ${where} result=${result}`);
        continue;
      }
      code = 1;
      if (result === "held") {
        const at = pace.allowedAt({ owner: item.issue?.key ?? item.id, project: item.project });
        out(`held: ${where}: the pace lets it be written at ${new Date(at).toISOString()}`);
      } else {
        out(`refused: ${where} result=${result}`);
      }
    }
    // What no item settled is not saved: a settle the check, the pace or Dispatch refused saves
    // nothing.
    if (settledAny) {
      await writeStateDocument(io, options.stateDocument, state, token);
    }
    return code;
  } catch (error) {
    if (error instanceof ReadError) {
      out(`unavailable: ${error.route} ${error.category}`);
      return 2;
    }
    if (error instanceof StateTooLarge || error instanceof StateWriteError) {
      out(error.message);
      return 1;
    }
    throw error;
  }
}
