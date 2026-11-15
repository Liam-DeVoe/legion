import type { ReactNode } from "react";
import { Link } from "react-router-dom";

import { PinButton } from "../../components/PinButton";
import {
  badgePrimary,
  focusVisibleRing,
  linkHoverText,
  linkText,
  pageLineBorder,
  pageMutedText,
  pagePaperBg,
  textMutedOnSurface,
} from "../../theme/classes";
import { DETAILS_ATTRIBUTE, useMarginDrawer } from "../margin/margin-slot";
import { CopyRefButton } from "../refs/CopyRefButton";
import { buildIssuePath } from "../refs/routes";
import { useShellNavigation } from "../shell/shell-navigation";
import { type IssueAction, IssueActionsMenu } from "./IssueActionsMenu";
import { authorText, pageChrome } from "./page-structure";
import { CONTROL_LABELS } from "./page-words";

/** The prototype's header control (`.hbtn`): a small bordered button in the muted ink. */
const HEADER_CONTROL = `inline-flex min-h-11 min-w-11 items-center justify-center gap-1.5 rounded-[7px] border px-2.5 text-xs leading-5 font-medium md:min-h-0 md:min-w-0 md:py-px ${pageLineBorder} ${pagePaperBg} ${pageMutedText} ${focusVisibleRing}`;

/**
 * Row one of the identity block: where the issue sits, what it is called, and what a reader does
 * to the issue itself. The parent is a breadcrumb rather than a field, because a reader reads it
 * to go there; the control that sets it is in the actions menu beside the pin.
 *
 * At its right end, Details opens the panel of the issue's properties over the right of the page
 * (R14); below the phone's width the row starts with the `☰` that opens the navigation, where
 * the shell draws no rail (R14, the 390 px picture).
 */
export function IssueIdentityRow({
  actions,
  issueKey,
  onPin,
  parent,
  pinSaving,
  pinned,
}: {
  actions: readonly IssueAction[];
  issueKey: string;
  onPin: () => void;
  /** The issue this one sits under, drawn as the way back up to it. */
  parent: string | null;
  pinSaving: boolean;
  pinned: boolean;
}): ReactNode {
  const navigation = useShellNavigation();
  const details = useMarginDrawer();
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-1">
      {navigation === undefined ? null : (
        <button
          aria-expanded={navigation.open}
          aria-haspopup="dialog"
          aria-label="Open navigation"
          className={HEADER_CONTROL}
          onClick={navigation.onOpen}
          type="button"
        >
          <span aria-hidden="true">☰</span>
        </button>
      )}
      {parent === null ? null : (
        <span className={`flex shrink-0 items-center gap-1 text-sm ${textMutedOnSurface}`}>
          <Link
            className={`dispatch-inline-link shrink-0 underline ${linkText} ${linkHoverText}`}
            to={buildIssuePath({ key: parent, kind: "issue" })}
            {...authorText}
          >
            {parent}
          </Link>
          <span aria-hidden="true" {...pageChrome}>
            ›
          </span>
        </span>
      )}
      <p
        className={`rounded-md px-2.5 py-1.5 font-mono text-sm font-semibold ${badgePrimary.bg} ${badgePrimary.text}`}
        {...authorText}
      >
        {issueKey}
      </p>
      <CopyRefButton
        primary={{ label: `issue key ${issueKey}`, value: issueKey }}
        route={{ key: issueKey, kind: "issue" }}
      />
      <PinButton
        disabled={pinSaving}
        label={pinned ? "Unpin issue" : "Pin issue"}
        onClick={onPin}
        pinned={pinned}
      />
      <span className="ml-auto flex items-center gap-1">
        {details === undefined ? null : (
          // A press leaves the focus where it is, so Details opens on the node the reader stood
          // on: a button takes no focus from a click in every browser, and the outline reads
          // the focus moving to nothing as the reader leaving it (`IssueTree.tsx`).
          <button
            aria-expanded={details.open}
            className={HEADER_CONTROL}
            data-testid="issue-details-control"
            {...{ [DETAILS_ATTRIBUTE]: "" }}
            onClick={() => details.setOpen(!details.open)}
            onMouseDown={(event) => event.preventDefault()}
            type="button"
            {...pageChrome}
          >
            {CONTROL_LABELS.details}
          </button>
        )}
        <IssueActionsMenu actions={actions} />
      </span>
    </div>
  );
}
