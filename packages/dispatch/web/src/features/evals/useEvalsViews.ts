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

/** The offsets past the first page that a first answer of `total` evals says a read needs: one
 *  page more for each EVALS_VIEW_LIMIT evals past the first page's, up to its total. */
export function laterOffsets(total: number): number[] {
  const offsets: number[] = [];
  for (let offset = EVALS_VIEW_LIMIT; offset < total; offset += EVALS_VIEW_LIMIT) {
    offsets.push(offset);
  }
  return offsets;
}

/** What a reader keeps of each page of a view: the page as answered, or the part of it the reader
 *  draws. One function for the reader's life (a module's, or memoized), since each new function
 *  reads every page again. */
export type EvalsViewSelect = (answer: EvalListResponse) => EvalListResponse;

/**
 * The evals views of `kinds` over `scope`, all subscribed at once (dispatch://INFRA-2918/
 * artifact/plan-md, D3): one view per kind, and, for a kind whose first answer counts more than a
 * page holds, one view more per page at the next offset. Every component that asks for the same
 * view shares its one subscription, through its query key, so two sections that read one kind
 * cost one view; and only the components that read a view render again when its delta lands.
 * `select` keeps the part of each page a reader draws (the Logged section's logged evals, the
 * panel's one eval), so a delta that moves nothing of that part renders that reader not at all;
 * a page's `total` stays the kind's.
 */
export function useEvalsViews(
  scope: EvalsScope,
  kinds: readonly EvalKind[],
  select?: EvalsViewSelect
): EvalsViewState[] {
  const firsts = useQueries({
    queries: kinds.map((kind) => ({ ...evalsViewQuery({ ...scope, kind, offset: 0 }), select })),
  });
  const later = kinds.flatMap((kind, index) =>
    laterOffsets(firsts[index]?.data?.total ?? 0).map((offset) => ({ kind, offset }))
  );
  const rest = useQueries({
    queries: later.map(({ kind, offset }) => ({
      ...evalsViewQuery({ ...scope, kind, offset }),
      select,
    })),
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
