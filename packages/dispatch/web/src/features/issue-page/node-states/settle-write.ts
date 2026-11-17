import { useSearchParams } from "react-router-dom";

/**
 * What the page reads around a node's life (dispatch://INFRA-4063 R4, R6): the words filter's
 * words, by which the outline opens each line of overtaken nodes that holds a match.
 */

/** The words the words filter holds, as the page's address carries them (`?q=`): the same value
 *  `issue-address.ts`'s `issueAddress` reads into `IssueAddress.filter` from the location's
 *  search, which `slots/body.tsx` narrows the outline's view by (`presets.ts`'s `presetView`,
 *  which trims them). `""` for an address that narrows nothing. */
export function useFilterWords(): string {
  const [search] = useSearchParams();
  return search.get("q") ?? "";
}
