import { useLocation } from "react-router-dom";

import { parseInboxSearch } from "../refs/routes";
import { READING_PANE_QUERY, useMediaQuery } from "../shell/useDialog";

/**
 * Whether the Inbox page fills the viewport, rather than scrolling as one document, for the
 * shell and the page to size themselves by: from the reading column's width, where the list and
 * the column each scroll in themselves, and below it while the address names a row the reader
 * opened (`?item=`), whose reading view scrolls in a box of its own - a decision block is
 * scrolled to its section there, and an approval's Next change steps that box. The address is
 * the reader's: their pick put the row there, and the view's Inbox control or Back takes it
 * off. The Inbox below the column's width clears an `?item=` that names no row it holds.
 */
export function useInboxFillsViewport(): boolean {
  const wide = useMediaQuery(READING_PANE_QUERY);
  const { search } = useLocation();
  return wide || parseInboxSearch(search).item !== undefined;
}
