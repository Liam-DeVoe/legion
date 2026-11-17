import { queryOptions } from "@tanstack/react-query";

import { api, writeRequest } from "../../../api/client";
import type { Result } from "../../../api/paths";
import { useKeyedWrite } from "../../writes/useKeyedWrite";
import { LENS_WORDS } from "../page-words";

/**
 * The one module of the issue page that names the lens read, the lens catalogue and the
 * invocation of a lens (dispatch://INFRA-4065 R2, R4, R6). Both reads sit outside the issue's
 * own keys, so no event refresh of the issue reads them again: each is read once in a load. The
 * app's retries stand.
 */

/** What `POST /api/v1/issues/{key}/lenses` answers: the lens it made. */
export type LensInvoked = Result<"/api/v1/issues/{key}/lenses", "post">;

/** The lens read of the lens at `address`, read at a press of `LENS_WORDS.show_chain` (R2). */
export const lensChainQuery = (address: string, enabled: boolean) =>
  queryOptions({
    enabled,
    gcTime: Number.POSITIVE_INFINITY,
    queryFn: () => api.getLenses(address),
    queryKey: ["lens-read", address],
    refetchOnWindowFocus: false,
    staleTime: Number.POSITIVE_INFINITY,
  });

/** The lens catalogue, in its own order, read when the menu of lens kinds opens (R4). A failed
 *  read is read again at the next opening. */
export const lensKindsQuery = (enabled: boolean) =>
  queryOptions({
    enabled,
    gcTime: Number.POSITIVE_INFINITY,
    queryFn: async () => (await api.getBlockSchema()).lens_kinds ?? [],
    queryKey: ["lens-kinds"],
    refetchOnWindowFocus: false,
    staleTime: Number.POSITIVE_INFINITY,
  });

/**
 * Invokes a lens of kind `kind` on the node at the address `on`, on the issue `issueKey` (R4).
 * Each pick is one write, under a key minted at the pick and in the reader's name
 * (`useKeyedWrite`), and sends `on` and `kind` and nothing more. It answers the lens made,
 * `null` where Dispatch made it and kept no answer, or throws the refusal (`ApiError`). The
 * menu says a refusal itself, so the shell's indicator shows the write only while it is out.
 */
export function useInvokeLens(
  issueKey: string
): (on: string, kind: string) => Promise<LensInvoked | null> {
  const invoke = useKeyedWrite({
    inline: true,
    label: () => LENS_WORDS.menu_title,
    write: ({ kind, on }: { readonly kind: string; readonly on: string }, bounds) =>
      api.send(
        writeRequest("post", "/api/v1/issues/{key}/lenses", { key: issueKey }, { kind, on }),
        bounds
      ),
  });
  return (on, kind) => invoke.mutateAsync({ kind, on });
}
