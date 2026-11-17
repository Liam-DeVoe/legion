import type { LensInvoked } from "@legion/contracts";
import { queryOptions, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { useLocation, useNavigate } from "react-router-dom";

import { api, writeRequest } from "../../../api/client";
import { useKeyedWrite } from "../../writes/useKeyedWrite";
import { LENS_WORDS } from "../page-words";
import { issueTreeKey } from "../tree-read/tree-read";

/**
 * The one module of the issue page that names the lens read, the lens catalogue and the
 * invocation of a lens. Both reads sit outside the issue's own keys, so no event refresh of the
 * issue reads them again: each is read once in a load. The app's retries stand.
 */

/** What makes a read held once in a load: an answer never goes stale or is let go, and the
 *  window taking the focus reads nothing again. */
const HELD_FOR_THE_LOAD = {
  gcTime: Number.POSITIVE_INFINITY,
  refetchOnWindowFocus: false,
  staleTime: Number.POSITIVE_INFINITY,
} as const;

/** The lens read of the lens at `address`, read at a press of `LENS_WORDS.show_chain`. */
export const lensChainQuery = (address: string, enabled: boolean) =>
  queryOptions({
    ...HELD_FOR_THE_LOAD,
    enabled,
    queryFn: () => api.getLenses(address),
    queryKey: ["lens-read", address],
  });

/** The lens catalogue, in its own order, read when the menu of lens kinds opens. A failed
 *  read is read again at the next opening. */
export const lensKindsQuery = (enabled: boolean) =>
  queryOptions({
    ...HELD_FOR_THE_LOAD,
    enabled,
    queryFn: async () => (await api.getBlockSchema()).lens_kinds ?? [],
    queryKey: ["lens-kinds"],
  });

/**
 * The id in the tree read of the lens a 201 made: what `?node=` names to land on its row, which
 * `pathToTarget` matches against `TreeNode.readId`. The 201's `id` is the lens's store id, the
 * uuid of its address `dispatch://lens/<uuid>`, and the read gives a lens that id. This form
 * follows the tree read's contract as written, since the server does not serve that read yet;
 * the browser rows check it once it does.
 */
export function lensReadId(invoked: LensInvoked): string {
  return invoked.id;
}

/**
 * Invokes a lens of kind `kind` on the node at the address `on`, on the issue `issueKey`.
 * Each pick is one write, under a key minted at the pick and in the reader's name
 * (`useKeyedWrite`), and sends `on` and `kind` and nothing more. It answers the lens made,
 * `null` where Dispatch made it and kept no answer, or throws the refusal (`ApiError`). The
 * menu says a refusal itself, so the shell's indicator shows the write only while it is out.
 *
 * A landing reads the tree read again, and that read alone, and waits for it; then, where
 * Dispatch answered the lens it made, it replaces the address with that lens's row
 * (`?node=`), keeping the reader's words filter (`?q=`) and dropping every other part of the
 * address, so the page lands on the new row and a reload lands there too. The address is the
 * one that stands when the read answers, not the one at the pick.
 */
export function useInvokeLens(
  issueKey: string
): (on: string, kind: string) => Promise<LensInvoked | null> {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { search } = useLocation();
  const standing = useRef(search);
  useEffect(() => {
    standing.current = search;
  }, [search]);
  const invoke = useKeyedWrite({
    inline: true,
    label: () => LENS_WORDS.menu_title,
    write: ({ kind, on }: { readonly kind: string; readonly on: string }, bounds) =>
      api.send(
        writeRequest("post", "/api/v1/issues/{key}/lenses", { key: issueKey }, { kind, on }),
        bounds
      ),
  });
  return async (on, kind) => {
    const invoked = await invoke.mutateAsync({ kind, on });
    await queryClient.invalidateQueries({ queryKey: issueTreeKey(issueKey), exact: true });
    if (invoked !== null) {
      const filter = new URLSearchParams(standing.current).get("q");
      const landing = new URLSearchParams(filter === null ? {} : { q: filter });
      landing.set("node", lensReadId(invoked));
      navigate({ search: `?${landing}` }, { replace: true });
    }
    return invoked;
  };
}
