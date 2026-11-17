import type { Actor } from "../../../api/types";
import { type NodeState, nodeStateWord } from "../page-words";
import type { DecisionsEntry, NamedEntry } from "./decisions-parts";

/**
 * How one entry of the Decisions filter stands (dispatch://INFRA-1196 R2, R6): the one reading
 * its row's `data-node-state`, its margin's state word and the panel's facts of the focused node
 * all take, so none of them works it out again.
 */
export interface EntryStanding {
  /** The name of the person who kept the decision, which follows "Kept by"; absent on every
   *  entry that does not stand kept. */
  readonly keeper?: string;
  /** The state's token: an ask's own `state`, and a decision's standing with the reader. */
  readonly token: NodeState;
  /** The page's word for the token (`nodeStateWord`). */
  readonly word: string;
  /** The word, then a space and the keeper where there is one: the state as one line. */
  readonly words: string;
}

/**
 * How `entry` stands, each person named by `nameOf` (`AgentText.author`), so a name is never
 * an id (R9, R22). Undefined where the entry stands in no state: the node an address names,
 * which is drawn for its words, and a decision the model gives no state. A keeper is named only
 * where the decision stands kept, which is the one state whose word leads to a name.
 */
export function entryStanding(
  entry: DecisionsEntry | NamedEntry,
  nameOf: (actor: Actor) => string
): EntryStanding | undefined {
  const token =
    entry.kind === "ask"
      ? entry.node.state
      : entry.kind === "decision"
        ? entry.standing
        : undefined;
  if (token === undefined) return undefined;
  const word = nodeStateWord(token);
  const keeper =
    token === "kept" && entry.kind === "decision" && entry.keptBy !== undefined
      ? nameOf(entry.keptBy)
      : undefined;
  return keeper === undefined
    ? { token, word, words: word }
    : { keeper, token, word, words: `${word} ${keeper}` };
}
