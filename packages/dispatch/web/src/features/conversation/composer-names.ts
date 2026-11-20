import { hashKey, type MutationKey } from "@tanstack/react-query";

import { SendStoppedError } from "../writes/keyed-write";
import type { HeldSend, HeldSends } from "./held-sends";
import { awayDraftOf, type UnsentDrafts } from "./unsent-drafts";

// The two functions the margin reads a composer's drafts with from outside the composer, apart
// from the composer's hook (`useKeptDrafts.ts`): the margin's provider is in the entry chunk
// (`features/margin/margin-context.tsx`), and Rollup never splits a module across chunks, so an
// import of these from the hook's module put the whole hook, and the composer's draft model with
// it, in what every page parses before it draws anything (dispatch://INFRA-1109, R13).

/** The name of the composer that sends under `mutationKey` (`KeptUnder.composerName`): its send's
 *  own name, which is what it keeps the reader's drafts under (`keptNames`). The margin keeps and
 *  reads a compose's drafts under it from outside the composer (`giveWay`, `useMarginSheet`). */
export function composerNameOf(mutationKey: MutationKey): string {
  return hashKey(mutationKey);
}

/**
 * What a margin compose's refusal becomes once a newer compose on its document takes its place,
 * which is the host's move for it and carries nothing. A send the reader stopped may have been
 * delivered, so it is not reduced to a draft: it waits under `aside` unchanged, its mark with it
 * (`HeldSends.setAside`), for the newer compose to offer (`stoppedOfferOf`). Any other refusal
 * goes, its mark with it, and its draft is kept under the compose's name for the words it was
 * written about and in the kind it was written as (`awayDraftOf`), where the compose keeps a draft
 * by address (`keptNames`' `awayName`) and the newer compose offers it (`keptForOtherWords`).
 */
export function giveWay(
  refused: HeldSend & { readonly status: "refused" },
  aside: MutationKey,
  store: HeldSends,
  unsent: UnsentDrafts
): void {
  if (refused.refusal instanceof SendStoppedError) {
    store.setAside(refused.mutationKey, aside);
    return;
  }
  unsent.keepAway(composerNameOf(refused.mutationKey), awayDraftOf(refused));
  store.discard(refused.mutationKey);
}
