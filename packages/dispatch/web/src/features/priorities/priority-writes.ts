import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useRef, useState } from "react";
import { api, priorityPatchRequest } from "../../api/client";
import type { CreatePriorityInput, PatchPriorityInput } from "../../api/types";
import { useIsHuman } from "../../hooks/useIsHuman";
import { useKeyedWrite } from "../writes/useKeyedWrite";
import { NOTHING_KEPT } from "./field-arrival";

/** One write of an existing priority: its id, what it changes, and the words the shell's
 *  pending-writes indicator names the write by while it is out. */
export interface PriorityPatch {
  readonly id: string;
  readonly input: PatchPriorityInput;
  readonly label: string;
  /** For a title or a description, the chain of this page's writes of it the write goes on
   *  (`TextChain.id`): what it settles is that chain's, and nothing of a chain that replaced it. */
  readonly chain?: number;
}

/** The attributes a write names the text its edit started from for (`expected`). The owner and
 *  the order take none. */
export type EditedText = "description" | "title";

const EDITED_TEXTS: readonly EditedText[] = ["description", "title"];

/**
 * Where this page's writes of one text of one priority stand, for what the next write of it
 * names as `expected`: the text the server holds when that write's turn comes. Writes to one
 * priority go out in press order (`priority:<id>`), so while one is out the server holds its text
 * by the time the next goes, and once it lands it holds it until somebody else writes.
 */
export interface TextChain {
  /** Which chain it is: a write carries it (`PriorityPatch.chain`), so one still out on a chain
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
 * not in the field: closing the description's disclosure, or a row of the table scrolling away,
 * takes the field with it, and the field drawn again shows them again. Leaving the page lets
 * them go.
 *
 * Whether the reader may write at all is `useIsHuman`: every priority write is classed as the
 * deployment's (`internal/dispatch/api/priorities.go`), which a credential scoped to a project
 * and a token naming no person are both refused, so a page that offered the controls to them
 * would offer a refusal.
 */
export function usePriorityWrites() {
  const queryClient = useQueryClient();
  const canWrite = useIsHuman();
  const chains = useRef(new Map<string, TextChain>());
  const made = useRef(0);
  const [keptTexts, setKeptTexts] = useState<ReadonlyMap<string, readonly string[]>>(
    () => new Map()
  );
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["priorities"] });
  const add = useKeyedWrite({
    label: (input: CreatePriorityInput) => `New priority “${input.title}”`,
    write: (input, bounds) => api.createPriority(input, bounds),
    onSettled: () => void refresh(),
  });
  const patch = useKeyedWrite({
    label: (patch_: PriorityPatch) => patch_.label,
    target: (patch_) => `priority:${patch_.id}`,
    request: (patch_) => priorityPatchRequest(patch_.id, patch_.input),
    // Counted here, on every write, rather than in the press's own callbacks, which TanStack
    // calls for the last press alone.
    onSettled: (_data, error, patch_) => {
      const landed: [string, TextChain][] = [];
      for (const attribute of EDITED_TEXTS) {
        if (patch_.input.expected?.[attribute] == null) continue;
        const key = `${attribute}:${patch_.id}`;
        const chain = chains.current.get(key);
        if (chain === undefined || chain.id !== patch_.chain) continue;
        const next = settledText(chain, error !== null);
        if (next === undefined) chains.current.delete(key);
        else chains.current.set(key, next);
        if (next?.out === 0) landed.push([key, next]);
      }
      const refreshed = refresh();
      if (landed.length === 0) return;
      void refreshed.then(() => {
        for (const [key, chain] of landed) {
          if (chains.current.get(key) === chain) chains.current.delete(key);
        }
      });
    },
  });
  const { mutate } = patch;
  const writeText = useCallback(
    (id: string, attribute: EditedText, text: string, held: string, label: string) => {
      const key = `${attribute}:${id}`;
      const chain = chains.current.get(key);
      const expected = expectedText(chain, held);
      if (text === expected) return;
      made.current += 1;
      const next = pressedText(chain, text, held, made.current);
      chains.current.set(key, next);
      const input: PatchPriorityInput =
        attribute === "title"
          ? { expected: { title: expected }, title: text }
          : { description: text, expected: { description: expected } };
      mutate({ chain: next.id, id, input, label });
    },
    [mutate]
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
  /** The reader's texts kept beside priority `id`'s `attribute`, newest first. */
  const kept = useCallback(
    (id: string, attribute: EditedText) => keptTexts.get(`${attribute}:${id}`) ?? NOTHING_KEPT,
    [keptTexts]
  );
  /** Keeps `texts` beside priority `id`'s `attribute`, in place of what was kept there. */
  const keep = useCallback((id: string, attribute: EditedText, texts: readonly string[]) => {
    setKeptTexts((current) => {
      const key = `${attribute}:${id}`;
      const next = new Map(current);
      if (texts.length === 0) next.delete(key);
      else next.set(key, texts);
      return next;
    });
  }, []);
  return { add, canWrite, describe, keep, kept, patch, rename };
}
