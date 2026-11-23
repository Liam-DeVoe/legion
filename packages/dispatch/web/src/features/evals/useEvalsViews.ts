import { useQueries } from "@tanstack/react-query";

import {
  EVALS_VIEW_LIMIT,
  type EvalKind,
  type EvalsViewParams,
  evalsViewQuery,
} from "../../api/queries";
import type { EvalListItem, EvalListResponse } from "../../api/types";

/** What every view of the tab shares: everything of a view but its kind and its page. */
export type EvalsScope = Omit<EvalsViewParams, "kind" | "offset">;

/** Where one kind's views stand: `subscribed` until its first page lands, `painted` once it has,
 *  `failed` while any of its pages is refused. */
export type EvalsViewStatus = "subscribed" | "painted" | "failed";

export interface EvalsViewState {
  readonly kind: EvalKind;
  readonly status: EvalsViewStatus;
  /** The first page's answer: its window, compare range, total and freshness. */
  readonly answer: EvalListResponse | undefined;
  /** Every eval of the pages landed, in their order. */
  readonly items: readonly EvalListItem[];
  /** Subscribes again each page that was refused. */
  readonly retry: () => void;
}

/** The offsets past the first page that a kind's first answer says it needs: one view more for
 *  each EVALS_VIEW_LIMIT evals past the first page's, up to its total. */
function laterOffsets(total: number): number[] {
  const offsets: number[] = [];
  for (let offset = EVALS_VIEW_LIMIT; offset < total; offset += EVALS_VIEW_LIMIT) {
    offsets.push(offset);
  }
  return offsets;
}

/**
 * The evals views of `kinds` over `scope`, all subscribed at once (dispatch://INFRA-2918/
 * artifact/plan-md, D3): one view per kind, and, for a kind whose first answer counts more than a
 * page holds, one view more per page at the next offset. Every component that asks for the same
 * view shares its one subscription, through its query key, so two sections that read one kind
 * cost one view; and only the components that read a view render again when its delta lands.
 */
export function useEvalsViews(scope: EvalsScope, kinds: readonly EvalKind[]): EvalsViewState[] {
  const firsts = useQueries({
    queries: kinds.map((kind) => evalsViewQuery({ ...scope, kind, offset: 0 })),
  });
  const later = kinds.flatMap((kind, index) =>
    laterOffsets(firsts[index]?.data?.total ?? 0).map((offset) => ({ kind, offset }))
  );
  const rest = useQueries({
    queries: later.map(({ kind, offset }) => evalsViewQuery({ ...scope, kind, offset })),
  });
  return kinds.map((kind, index) => {
    const first = firsts[index];
    const pages = [first, ...rest.filter((_, at) => later[at]?.kind === kind)].flatMap((page) =>
      page === undefined ? [] : [page]
    );
    const refused = pages.filter((page) => page.isError);
    return {
      kind,
      status: refused.length > 0 ? "failed" : first?.data === undefined ? "subscribed" : "painted",
      answer: first?.data,
      items: pages.flatMap((page) => page.data?.items ?? []),
      retry: () => {
        for (const page of refused) void page.refetch();
      },
    };
  });
}
