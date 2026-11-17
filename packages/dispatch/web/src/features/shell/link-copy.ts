import { copyText } from "../../lib/clipboard";
import { linkAddress } from "../refs/link-address";
import { useKeymap } from "./keymap";

/** The link the keyboard's focus is on, where it is one `y` copies the address of: a link that is
 *  not editable - a link inside a document the reader edits takes `y` as a letter typed - and
 *  that has an address (`linkAddress`). */
function focusedLink(): Element | null {
  const active = document.activeElement;
  if (
    !(active instanceof HTMLElement) ||
    active.isContentEditable ||
    !active.matches("a[href], [role='link']")
  ) {
    return null;
  }
  return linkAddress(active) === undefined ? null : active;
}

/**
 * `y` while a link has the keyboard's focus copies the link's address (dispatch://INFRA-2160, R23):
 * the `dispatch://` reference of a link to a Dispatch item, the URL of any other link
 * (`linkAddress`). One `global` binding tried before every scope (`KeyBinding.beforeScopes`), so
 * it works on every page and in every dialog, the drawer and the margin's sheet included, and
 * comes before a page's own `y` (the Inbox's copy of the item's reference). `KeymapProvider`
 * registers it. It names no action of the catalogue: the key works only while its link has the
 * focus, which WCAG 2.1.4 accepts with no remap, so Settings lists nothing to change, and the `?`
 * help lists it under Global.
 */
export function useLinkCopy(): void {
  useKeymap("global", [
    {
      beforeScopes: true,
      id: "copy-link-address",
      keys: "y",
      label: "Copy the focused link's address",
      run: () => {
        const link = focusedLink();
        const address = link === null ? undefined : linkAddress(link);
        if (address !== undefined) {
          void copyText(address);
        }
      },
      when: () => focusedLink() !== null,
    },
  ]);
}
