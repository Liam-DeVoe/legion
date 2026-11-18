import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useRef } from "react";
import { api, priorityPatchRequest } from "../../api/client";
import type { CreatePriorityInput, PatchPriorityInput } from "../../api/types";
import { useIsHuman } from "../../hooks/useIsHuman";
import { useKeyedWrite } from "../writes/useKeyedWrite";

/** One write of an existing priority: its id, what it changes, and the words the shell's
 *  pending-writes indicator names the write by while it is out. */
export interface PriorityPatch {
  readonly id: string;
  readonly input: PatchPriorityInput;
  readonly label: string;
}

/** The attributes a write names the text its edit started from for (`expected`). The owner and
 *  the order take none. */
type EditedText = "description" | "title";

const EDITED_TEXTS: readonly EditedText[] = ["description", "title"];

/**
 * Where this page's writes of one text of one priority stand, for what the next write of it
 * names as `expected`: the text the server holds when that write's turn comes. Writes to one
 * priority go out in press order (`priority:<id>`), so while one is out the server holds its text
 * by the time the next goes, and once it lands it holds it until somebody else writes.
 */
export interface TextChain {
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

/** Whether a write pressed now goes after the writes of `chain`: one of them is still out, or the
 *  last landed and the page's read has not moved to a text they did not write. Not once a refused
 *  one has settled with none out, and not once the read holds the last text written, which ends
 *  the chain. */
function continues(chain: TextChain | undefined, held: string): chain is TextChain {
  if (chain === undefined) return false;
  if (chain.out > 0) return true;
  return !chain.refused && chain.last !== held && chain.known.includes(held);
}

/** What a write pressed now names as `expected`, where the page reads `held`: the text of the last
 *  write pressed here while the chain goes on, and else `held`. */
export function expectedText(chain: TextChain | undefined, held: string): string {
  return continues(chain, held) ? chain.last : held;
}

/** The chain once a write of `text` is pressed where the page reads `held`. */
export function pressedText(chain: TextChain | undefined, text: string, held: string): TextChain {
  if (continues(chain, held)) {
    return { ...chain, known: [...chain.known, text], last: text, out: chain.out + 1 };
  }
  return { known: [held, text], last: text, out: 1, refused: false };
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
 * text is written over unseen. A write that would change nothing is not sent.
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
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["priorities"] });
  };
  const add = useKeyedWrite({
    label: (input: CreatePriorityInput) => `New priority “${input.title}”`,
    write: (input, bounds) => api.createPriority(input, bounds),
    onSettled: refresh,
  });
  const patch = useKeyedWrite({
    label: (patch_: PriorityPatch) => patch_.label,
    target: (patch_) => `priority:${patch_.id}`,
    request: (patch_) => priorityPatchRequest(patch_.id, patch_.input),
    // Counted here, on every write, rather than in the press's own callbacks, which TanStack
    // calls for the last press alone.
    onSettled: (_data, error, patch_) => {
      for (const attribute of EDITED_TEXTS) {
        if (patch_.input.expected?.[attribute] == null) continue;
        const key = `${attribute}:${patch_.id}`;
        const chain = chains.current.get(key);
        if (chain === undefined) continue;
        const next = settledText(chain, error !== null);
        if (next === undefined) chains.current.delete(key);
        else chains.current.set(key, next);
      }
      refresh();
    },
  });
  const { mutate } = patch;
  const writeText = useCallback(
    (id: string, attribute: EditedText, text: string, held: string, label: string) => {
      const key = `${attribute}:${id}`;
      const chain = chains.current.get(key);
      const expected = expectedText(chain, held);
      if (text === expected) return;
      chains.current.set(key, pressedText(chain, text, held));
      const input: PatchPriorityInput =
        attribute === "title"
          ? { expected: { title: expected }, title: text }
          : { description: text, expected: { description: expected } };
      mutate({ id, input, label });
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
  return { add, canWrite, describe, patch, rename };
}
