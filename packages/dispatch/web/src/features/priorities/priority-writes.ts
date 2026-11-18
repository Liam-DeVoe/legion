import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useRef, useState } from "react";
import { api, priorityPatchRequest } from "../../api/client";
import type { CreatePriorityInput, PatchPriorityInput } from "../../api/types";
import { useIsHuman } from "../../hooks/useIsHuman";
import { useKeyedWrite } from "../writes/useKeyedWrite";
import { type Shape, shape } from "../writes/write-storage";
import { defineWriteKind, ofKind, useWrites } from "../writes/write-store";
import { NOTHING_KEPT } from "./field-arrival";

/** One write of an existing priority's owner or order: its id, what it changes, and the words the
 *  shell's pending-writes indicator names the write by while it is out. A title or a description
 *  is written through `rename` and `describe` instead (`PriorityTextWrite`). */
export interface PriorityPatch {
  readonly id: string;
  readonly input: PatchPriorityInput;
  readonly label: string;
}

/** The attributes a write names the text its edit started from for (`expected`). The owner and
 *  the order take none. */
export type EditedText = "description" | "title";

/**
 * Where this page's writes of one text of one priority stand, for what the next write of it
 * names as `expected`: the text the server holds when that write's turn comes. Writes to one
 * priority go out in press order (`priority:<id>`), so while one is out the server holds its text
 * by the time the next goes, and once it lands it holds it until somebody else writes.
 */
export interface TextChain {
  /** Which chain it is: a write carries it (`TextPress.chain`), so one still out on a chain
   *  that was replaced settles nothing of the chain that replaced it. */
  readonly id: number;
  /** The writes pressed here that have not settled. */
  readonly out: number;
  /** Whether one of them was refused: the server holds none of their texts then. */
  readonly refused: boolean;
  /** The text of the last write pressed. */
  readonly last: string;
  /** What the page's read may show while it catches up with these writes: the text it held at
   *  the first press, and each text written since. A read of any of them is the server's answer
   *  to these writes arriving late, not another person's write. */
  readonly known: readonly string[];
}

/** Whether a write pressed now goes after the writes of `chain`: none of them was refused, and
 *  the page's read shows a text they know, so it has not moved to another person's. Not once
 *  the read shows the last text written with none out, which ends the chain. */
function continues(chain: TextChain | undefined, held: string): chain is TextChain {
  if (chain === undefined || chain.refused || !chain.known.includes(held)) return false;
  return chain.out > 0 || chain.last !== held;
}

/** What a write pressed now names as `expected`, where the page reads `held`: the text of the last
 *  write pressed here while the chain goes on, and else `held`. */
export function expectedText(chain: TextChain | undefined, held: string): string {
  return continues(chain, held) ? chain.last : held;
}

/** The chain once a write of `text` is pressed where the page reads `held`: `chain` gone on, or
 *  a new one, `fresh` its id. */
export function pressedText(
  chain: TextChain | undefined,
  text: string,
  held: string,
  fresh: number
): TextChain {
  if (continues(chain, held)) {
    return { ...chain, known: [...chain.known, text], last: text, out: chain.out + 1 };
  }
  return { id: fresh, known: [held, text], last: text, out: 1, refused: false };
}

/** The chain once one of its writes settled, `refused` or landed; none once a refusal leaves no
 *  write out, so the next write names the text the page reads. */
export function settledText(chain: TextChain, refused: boolean): TextChain | undefined {
  const next = { ...chain, out: Math.max(0, chain.out - 1), refused: chain.refused || refused };
  return next.out === 0 && next.refused ? undefined : next;
}

/** One write of a priority's title or description as the write store holds it, and a reload
 *  keeps it: the priority, the text written - the reader's own - and the text the write names as
 *  the one it goes over (`expected`). */
export interface PriorityTextWrite {
  readonly id: string;
  readonly attribute: EditedText;
  readonly text: string;
  readonly expected: string;
}

const keptTextWrite = shape.record({
  attribute: shape.oneOf("description", "title"),
  expected: shape.string,
  id: shape.string,
  text: shape.string,
}) satisfies Shape<PriorityTextWrite>;

/** A press of a title or a description: its write, the words the shell's line names it by, and
 *  the chain of this page's writes of that text it goes on (`TextChain.id`): what it settles is
 *  that chain's, and nothing of a chain that replaced it. */
interface TextPress {
  readonly write: PriorityTextWrite;
  readonly label: string;
  readonly chain: number;
}

/** The pages mounted now that show priorities (`usePriorityWrites`), each asked to take a write
 *  of a text a conflict refused: one that shows its priority keeps the reader's text beside that
 *  priority's field, and says so. */
const mountedPages = new Set<(write: PriorityTextWrite) => boolean>();

/**
 * A title or a description written: made from its request alone on its priority's line
 * (`priority:<id>`, `priorityPatchRequest`), and kept across a reload with its record. Dispatch
 * refuses one whose `expected` another person's text has replaced (`409 PRIORITY_CHANGED`), and
 * would refuse that body the same way every time, so no Retry sends it again: a page mounted now
 * that shows its priority takes the refusal (`claims`) and keeps the reader's text beside the
 * field, and where none does, the shell's line offers that text to copy (`draft`).
 */
export const priorityTextWrite = defineWriteKind<PriorityTextWrite>({
  claims: (_client, write) => [...mountedPages].some((takes) => takes(write.detail)),
  draft: (write) => write.detail.text,
  kept: keptTextWrite,
  name: "priority-text",
});

/** A new priority, its record the title it is written with. Dispatch refuses one whose id - the
 *  title's slug - a priority already carries (`409 PRIORITY_EXISTS`), and a Retry would send the
 *  same title, so the same id: the shell's line offers the title to copy instead (`draft`). */
export const newPriorityWrite = defineWriteKind<string>({
  draft: (write) => write.detail,
  kept: shape.string,
  name: "priority-new",
});

/** The reader's texts a page keeps beside one text of one priority, newest first, none twice, and
 *  for each a conflict brought back (`priorityTextWrite.claims`), the text its write went over. */
interface KeptTexts {
  readonly texts: readonly string[];
  readonly over: ReadonlyMap<string, string>;
}

/** The key a priority's text is held under here: its chain's, and its kept texts'. */
function textKey(attribute: EditedText, id: string): string {
  return `${attribute}:${id}`;
}

/** Notes that the page reads `held` where `kept` stands: each text a claimed write went over that
 *  is not `held` the page has read past (`past`, by text key), for good, so a later read of that
 *  text again - another person writing it back - is a text like any other. */
function notePast(
  past: Map<string, Set<string>>,
  key: string,
  kept: KeptTexts | undefined,
  held: string
): void {
  for (const over of kept?.over.values() ?? []) {
    if (over === held) continue;
    const read = past.get(key) ?? new Set<string>();
    read.add(over);
    past.set(key, read);
  }
}

/** Whether the page has yet to read past `over`, the text a claimed write went over. */
function unread(past: Map<string, Set<string>>, key: string, over: string | undefined): boolean {
  return over !== undefined && past.get(key)?.has(over) !== true;
}

const NO_PRIORITIES: readonly string[] = [];

/**
 * The three writes these pages make: a new priority, a change to one, and the move that is a
 * drag of the order. Each goes out under its own key and is retried under it (`useKeyedWrite`),
 * and each refreshes the whole `["priorities"]` family when it settles — the body the pages
 * read and the summary the rail reads sit under that one prefix — so a write a reader made
 * shows as the server holds it rather than as the page guessed. The changes to one priority go
 * out one after another, in the order they were pressed, as the request a reload makes again.
 *
 * A title or a description is written through `rename` and `describe`, which name the text the
 * reader's edit started from (`expected`, `expectedText`): where somebody else wrote the text
 * meanwhile the server refuses the write `409 PRIORITY_CHANGED` and writes nothing, so nobody's
 * text is written over unseen. A write that would change nothing is not sent. A chain of writes
 * that all landed ends once the refresh the last of them started has answered: the page reads
 * what they wrote then, and a text of theirs it reads again later is somebody else's.
 *
 * The reader's own texts a title or a description keeps beside itself while it shows another
 * person's (`ChangedMeanwhile`) are held here, by priority and attribute (`kept`, `keep`), and
 * not in the field: a row of the table scrolling away takes the field with it, and the field
 * drawn again shows them again, and a description that keeps any is drawn though its disclosure
 * or its row is closed. Leaving the page lets them go. A write of a priority in `shown` that
 * Dispatch refuses `409 PRIORITY_CHANGED` is the page's to settle with the reader, whether it was
 * pressed here or a reload took it up, before this page was drawn or since
 * (`priorityTextWrite.claims`, `Writes.claim`): its text is kept beside the field, ahead of any
 * kept there, and nothing stands for it on the shell's line. The page shows that text once it
 * reads another text than the one the write went over, so it never offers the reader's text
 * before it shows the other person's; until then no write over that text is sent, since
 * Dispatch would refuse it the same way.
 *
 * Whether the reader may write at all is `useIsHuman`: every priority write is classed as the
 * deployment's (`internal/dispatch/api/priorities.go`), which a credential scoped to a project
 * and a token naming no person are both refused, so a page that offered the controls to them
 * would offer a refusal.
 */
export function usePriorityWrites(shown: readonly string[] = NO_PRIORITIES) {
  const queryClient = useQueryClient();
  const writes = useWrites();
  const canWrite = useIsHuman();
  const chains = useRef(new Map<string, TextChain>());
  const made = useRef(0);
  const [keptTexts, setKeptTexts] = useState<ReadonlyMap<string, KeptTexts>>(() => new Map());
  // For each text key, the texts a claimed write went over that the page has read past.
  const readPast = useRef(new Map<string, Set<string>>());
  const showing = useRef(shown);
  showing.current = shown;
  const refresh = useCallback(
    () => queryClient.invalidateQueries({ queryKey: ["priorities"] }),
    [queryClient]
  );
  const add = useKeyedWrite({
    detail: (input: CreatePriorityInput) => input.title,
    kind: newPriorityWrite,
    label: (input) => `New priority “${input.title}”`,
    write: (input, bounds) => api.createPriority(input, bounds),
    onSettled: () => void refresh(),
  });
  const patch = useKeyedWrite({
    label: (patch_: PriorityPatch) => patch_.label,
    target: (patch_) => `priority:${patch_.id}`,
    request: (patch_) => priorityPatchRequest(patch_.id, patch_.input),
    onSettled: () => void refresh(),
  });
  const text = useKeyedWrite({
    detail: (press: TextPress) => press.write,
    kind: priorityTextWrite,
    label: (press) => press.label,
    target: (press) => `priority:${press.write.id}`,
    // The text, and the text it goes over as `expected`.
    request: ({ write }) =>
      priorityPatchRequest(
        write.id,
        write.attribute === "title"
          ? { expected: { title: write.expected }, title: write.text }
          : { description: write.text, expected: { description: write.expected } }
      ),
    // Counted here, on every write, rather than in the press's own callbacks, which TanStack
    // calls for the last press alone.
    onSettled: (_data, error, press) => {
      const key = textKey(press.write.attribute, press.write.id);
      const chain = chains.current.get(key);
      let landed: TextChain | undefined;
      if (chain !== undefined && chain.id === press.chain) {
        const next = settledText(chain, error !== null);
        if (next === undefined) chains.current.delete(key);
        else chains.current.set(key, next);
        if (next?.out === 0) landed = next;
      }
      const refreshed = refresh();
      if (landed === undefined) return;
      void refreshed.then(() => {
        if (chains.current.get(key) === landed) chains.current.delete(key);
      });
    },
  });
  /** Keeps `write`'s text beside its priority's field, ahead of any kept there, as the text a
   *  conflict brought back over `write.expected`, and reads the priorities again, so the page
   *  reads the other person's text where it has not yet. */
  const take = useCallback(
    (write: PriorityTextWrite) => {
      const key = textKey(write.attribute, write.id);
      readPast.current.get(key)?.delete(write.expected);
      setKeptTexts((current) => {
        const before = current.get(key);
        const over = new Map(before?.over);
        over.set(write.text, write.expected);
        const texts = [write.text, ...(before?.texts ?? []).filter((one) => one !== write.text)];
        return new Map(current).set(key, { over, texts });
      });
      void refresh();
    },
    [refresh]
  );
  // The conflicts of the priorities this page shows are its own to settle with the reader, from
  // the moment it is drawn until it goes.
  useEffect(() => {
    const takes = (write: PriorityTextWrite) => {
      if (!showing.current.includes(write.id)) return false;
      take(write);
      return true;
    };
    mountedPages.add(takes);
    return () => {
      mountedPages.delete(takes);
    };
  }, [take]);
  // And those refused before it was drawn - a reload's, taken up before the page was - which
  // stand on the shell's line until a page that shows their priority takes them.
  useEffect(() => {
    for (const row of writes.rows()) {
      const write = ofKind(row, priorityTextWrite);
      if (write === undefined || !shown.includes(write.detail.id)) continue;
      if (writes.claim(write.id)) take(write.detail);
    }
  }, [shown, take, writes]);
  const { mutate } = text;
  const writeText = useCallback(
    (id: string, attribute: EditedText, written: string, held: string, label: string) => {
      const key = textKey(attribute, id);
      const chain = chains.current.get(key);
      const expected = expectedText(chain, held);
      if (written === expected) return;
      const stand = keptTexts.get(key);
      notePast(readPast.current, key, stand, held);
      const stale = [...(stand?.over.values() ?? [])].some(
        (over) => over === expected && unread(readPast.current, key, over)
      );
      if (stale) {
        // Dispatch refused a write over `expected` because another person's text stands there,
        // and the page has not read it yet: this body would be refused the same way. The text
        // stays kept, to be offered once the page reads theirs.
        take({ attribute, expected, id, text: written });
        return;
      }
      made.current += 1;
      const next = pressedText(chain, written, held, made.current);
      chains.current.set(key, next);
      mutate({ chain: next.id, label, write: { attribute, expected, id, text: written } });
    },
    [keptTexts, mutate, take]
  );
  /** Writes `title` as priority `id`'s title, where the page reads `held`. A title with nothing
   *  in it is refused by the server, so none is sent. */
  const rename = useCallback(
    (id: string, title: string, held: string, label: string) => {
      if (title !== "") writeText(id, "title", title, held, label);
    },
    [writeText]
  );
  /** Writes `description` as priority `id`'s description, where the page reads `held`. */
  const describe = useCallback(
    (id: string, description: string, held: string, label: string) =>
      writeText(id, "description", description, held, label),
    [writeText]
  );
  /** The reader's texts kept beside priority `id`'s `attribute`, newest first, where the page
   *  reads `held`: but for a text a conflict brought back while the page still reads the text its
   *  write went over, which waits until the page reads the other person's. */
  const kept = useCallback(
    (id: string, attribute: EditedText, held: string): readonly string[] => {
      const key = textKey(attribute, id);
      const stand = keptTexts.get(key);
      if (stand === undefined) return NOTHING_KEPT;
      notePast(readPast.current, key, stand, held);
      const waits = (one: string) => unread(readPast.current, key, stand.over.get(one));
      if (!stand.texts.some(waits)) return stand.texts;
      const shownTexts = stand.texts.filter((one) => !waits(one));
      return shownTexts.length === 0 ? NOTHING_KEPT : shownTexts;
    },
    [keptTexts]
  );
  /** Keeps `texts` beside priority `id`'s `attribute`, in place of what was kept there, and lets
   *  go of what each text no longer kept went over. A text still waiting for the page to read the
   *  other person's (`kept`) was never shown, so it is no field's to let go, and stays. */
  const keep = useCallback((id: string, attribute: EditedText, texts: readonly string[]) => {
    const key = textKey(attribute, id);
    setKeptTexts((current) => {
      const before = current.get(key);
      const waiting = (before?.texts ?? []).filter(
        (one) => !texts.includes(one) && unread(readPast.current, key, before?.over.get(one))
      );
      const all = [...texts, ...waiting];
      const next = new Map(current);
      if (all.length === 0) next.delete(key);
      else {
        const over = new Map([...(before?.over ?? [])].filter(([one]) => all.includes(one)));
        next.set(key, { over, texts: all });
      }
      return next;
    });
  }, []);
  return { add, canWrite, describe, keep, kept, patch, rename };
}
