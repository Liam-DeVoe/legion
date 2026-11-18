import { useMemo } from "react";

import type { Ask, Comment, IssueDetails, MessageRead } from "../../api/types";
import { type ItemLanding, type ItemRoute, useItemLanding } from "../issue/useItemLanding";
import type { IssueRoute } from "../refs/routes";
import {
  type AddressTarget,
  agentsOnlyAncestor,
  agentsOnlyItem,
  type IssueAddress,
  outlineItemRoute,
  pathToTarget,
} from "./issue-address";
import type { TreeNode } from "./issue-tree";

/** The node an address names, read against the tree (`useAddressTarget`). Each field is one
 *  value for as long as the address and the tree say the same; the object holding them is new
 *  on every render. */
export interface AddressedNode {
  /** The id of the node the address names, or nothing: what a view keeps whatever its filter
   *  or its words match (`presets.ts`'s `presetView`). */
  readonly named: ReadonlySet<string>;
  /** The chain down to the node the address names, the issue first, which the tree unfolds and
   *  puts the focus on the last node of; undefined where the address names no node or the
   *  tree holds none it names. */
  readonly reveal: readonly TreeNode[] | undefined;
  /** The node the address names, as the address says it. */
  readonly target: AddressTarget | undefined;
}

/**
 * The node `address` names, resolved against the whole tree `root` - the page's and every
 * filter's - so a view keeps it even where its filter or its words would not have matched it.
 * Every target an address names, `/children` among them, is one a tree unfolds and focuses.
 *
 * The target is read by what it says rather than by the value `issueAddress` built, which is a
 * new one on every render: the tree is walked again only when the address or the tree changes.
 */
export function useAddressTarget(
  root: TreeNode,
  address: Pick<IssueAddress, "target">
): AddressedNode {
  const targetKind = address.target?.kind;
  const targetId = address.target?.id;
  // Which document a block target belongs to, where its address names one: a block id alone
  // names a block of any document of the issue (`pathToTarget`).
  const targetDocument = address.target?.kind === "block" ? address.target.document : undefined;
  const target = useMemo<AddressTarget | undefined>(() => {
    if (targetKind === undefined || targetId === undefined) return undefined;
    if (targetKind === "block") return { document: targetDocument, id: targetId, kind: "block" };
    return { id: targetId, kind: targetKind } as AddressTarget;
  }, [targetDocument, targetId, targetKind]);
  const reveal = useMemo(() => pathToTarget(root, target), [root, target]);
  const named = useMemo(() => {
    const node = reveal?.at(-1);
    return new Set(node === undefined ? [] : [node.id]);
  }, [reveal]);
  return { named, reveal, target };
}

/** Where an address naming an ask, a comment or a message lands while the tree holds no node of
 *  it, once every read the tree is built from has answered (`useAddressFallback`). */
export type AddressFallback =
  /**
   * A turn of a thread only agents wrote in, which the outline leaves out on purpose
   * (`agents-only.ts`): `ancestor` is the chain down to the turn's nearest node the tree holds
   * (`agentsOnlyAncestor`), the issue alone where it holds none, which the outline lands on and
   * says at its row that the thread is the agents' alone.
   *
   * It is a kind of its own, never `missing`: the turn is there, in the panel's history. Opening
   * Details at that history with the turn `id` marked is the panel's to do, and the part that
   * builds it takes this kind over from the outline's landing on the ancestor.
   */
  | {
      readonly ancestor: readonly TreeNode[];
      readonly id: string;
      readonly kind: "agents-only";
    }
  /**
   * The item as the old page's landing reads it (`issue/useItemLanding.ts`), once that read has
   * answered: an item of another issue, or one that stands beside a document (`redirect`); an id
   * written short, which names one item (`rewrite`) or several (`ambiguous`); or none this issue
   * holds (`missing`), or a read that failed (`unavailable`). `route` is the item's route, which
   * the match list and the failure name the item by.
   */
  | {
      readonly kind: "landing";
      readonly landing: Exclude<ItemLanding, { kind: "pending" | "turn" }>;
      readonly route: ItemRoute;
    };

/** The reads of the issue's conversation the tree's turns come from, as the page holds them:
 *  each undefined until it has answered. */
export interface ConversationReads {
  readonly asks: readonly Pick<Ask, "id" | "kind">[] | undefined;
  readonly comments:
    | readonly Pick<Comment, "answers" | "ask_id" | "author" | "id" | "reply_to">[]
    | undefined;
  readonly messages: readonly MessageRead[] | undefined;
}

/**
 * Where `address` lands where it names an ask, a comment or a message of the outline
 * (`outlineItemRoute`) and the tree holds no node of it (`reveal` undefined), once every read the
 * tree is built from has answered (`readsHeld`): before that the node may not have arrived yet.
 * Undefined while the tree holds the node, while the reads are out, for an address naming no
 * such item, and while the old page's landing is still reading the item (`pending`).
 *
 * A turn of a thread only agents wrote in is told from the conversation's reads, as the tree
 * leaves it out (`agentsOnlyItem`, with the asks the tree draws its nodes from: the issue's open
 * asks and the asks read), and lands on its nearest drawn ancestor (`agents-only`). Every other
 * item is read as the old page reads one (`useItemLanding`), and nothing is read for an item the
 * tree holds. Its `turn` - an item of this issue beside no document, whose page is the outline -
 * draws the outline as it stands, which lands on the node once a read draws it.
 */
export function useAddressFallback({
  address,
  issue,
  reads,
  readsHeld,
  reveal,
  root,
  route,
  search,
}: {
  readonly address: Pick<IssueAddress, "documentItem" | "target">;
  readonly issue: IssueDetails;
  readonly reads: ConversationReads;
  readonly readsHeld: boolean;
  readonly reveal: readonly TreeNode[] | undefined;
  /** The whole tree, which the address's node was looked for in. */
  readonly root: TreeNode;
  readonly route: IssueRoute;
  readonly search: string;
}): AddressFallback | undefined {
  const item = outlineItemRoute(route, search, address);
  const unheld = readsHeld && reveal === undefined ? item : undefined;
  const unheldId = unheld?.id;
  const { asks, comments, messages } = reads;
  const openAsks = issue.open_asks;
  const agentsOnly = useMemo(
    () =>
      unheldId === undefined
        ? undefined
        : agentsOnlyItem({ id: unheldId, kind: "item" }, comments, messages, [
            ...(openAsks ?? []),
            ...(asks ?? []),
          ]),
    [asks, comments, messages, openAsks, unheldId]
  );
  const ancestor = useMemo(
    () =>
      agentsOnly === undefined
        ? undefined
        : agentsOnlyAncestor(root, agentsOnly, comments, messages),
    [agentsOnly, comments, messages, root]
  );
  // The issue's own route reads nothing: the landing reads an item only where it falls back.
  const landing = useItemLanding(
    unheld === undefined || agentsOnly !== undefined ? { key: route.key, kind: "issue" } : unheld,
    issue
  );
  if (agentsOnly !== undefined && ancestor !== undefined) {
    return { ancestor, id: agentsOnly, kind: "agents-only" };
  }
  if (unheld === undefined || landing === undefined) return undefined;
  if (landing.kind === "pending" || landing.kind === "turn") return undefined;
  return { kind: "landing", landing, route: unheld };
}
