import type { ReactNode } from "react";
import { Link } from "react-router-dom";

import type { Artifact, Ask } from "../../api/types";
import { approvalPillClassName } from "../../components/Pill";
import { type PageWords, usePageWords } from "../../components/tooltips";
import { linkText, textMutedOnSurface, textSecondaryOnSurface } from "../../theme/classes";
import { ApprovalChip, approvalVersion } from "../doc/ApprovalChip";
import { AgentName } from "../refs/AgentName";
import { buildIssuePath } from "../refs/routes";
import type { StepEntry } from "./gated-steps";

/** What an entry needs to link its document at the version its approval speaks for: the
 *  issue's open asks, whose approval requests name that version, and the issue's key. */
export interface ApprovalVersions {
  readonly asks: readonly Ask[];
  readonly issueKey: string;
}

/**
 * The version of `document` its approval speaks for: while a request is open, the version that
 * request names - read from the request itself (`AskApproval.documents`, else its own `version`
 * where it was opened on this document), since the document's own approval carries no version
 * for a request on it alone - and otherwise the version a review approved or sent back
 * (`approvalVersion`). Undefined for a document nobody has asked to review.
 */
function approvalLinkVersion(document: Artifact, asks: readonly Ask[]): number | undefined {
  const approval = document.approval;
  if (approval === undefined || approval.state === "draft") return undefined;
  if (approval.state !== "awaiting") return approvalVersion(approval);
  const named = asks.flatMap((ask) => {
    const request = ask.kind === "approval" && ask.state === "open" ? ask.approval : undefined;
    if (request === undefined) return [];
    const listed = request.documents?.find((entry) => entry.artifact_id === document.id);
    if (listed !== undefined) return [listed.version];
    return request.artifact_id === document.id ? [request.version] : [];
  })[0];
  return (
    named ??
    approval.documents?.find((entry) => entry.artifact_id === document.id)?.version ??
    approval.latest_version
  );
}

/**
 * The link to the version of `document` an approval speaks for, opened in place of the outline
 * at that version (`?v=`, dispatch://INFRA-1196 R16, R20), as the old panel's card of an
 * approval request linked it (R19): the document's name and the version, `spec.md, version 1`.
 */
function ApprovalVersionLink({
  document,
  issueKey,
  version,
  words,
}: {
  document: Artifact;
  issueKey: string;
  version: number;
  words: PageWords;
}): ReactNode {
  return (
    <Link
      className={`dispatch-inline-link text-xs underline ${linkText}`}
      data-approval-version={document.id}
      to={buildIssuePath({ key: issueKey, kind: "artifact", slug: document.slug, version })}
    >
      <span {...words.authorText}>{`${document.name},`}</span>{" "}
      <span {...words.pageChrome}>{words.CONTROL_LABELS.version.toLowerCase()}</span>{" "}
      <span {...words.authorText}>{version}</span>
    </Link>
  );
}

/** The state of one step: its document's chip, or the dashed pill that says nobody has asked for
 *  this document to be reviewed - a step with no document, a document with no approval, and a
 *  draft all read the same to someone waiting on the gate, and none of the three has a review to
 *  open, so the pill is a span rather than a button. */
function StepState({
  actions,
  entry,
  primaryArtifactId,
  size,
  versions,
}: {
  actions: boolean;
  entry: StepEntry;
  primaryArtifactId: string;
  size: "compact" | "default";
  versions: ApprovalVersions | undefined;
}): ReactNode {
  const page = usePageWords();
  const { document } = entry;
  if (document?.approval === undefined || document.approval.state === "draft") {
    return (
      <span className={approvalPillClassName("draft", size)}>
        {page?.CONTROL_LABELS.not_requested ?? "Not requested"}
      </span>
    );
  }
  // The link to the version an approval speaks for is drawn inside `NoTooltips` alone, in the
  // new issue page's words: Details' copy of the list, where R19 keeps the old card's link.
  const version =
    page === null || versions === undefined
      ? undefined
      : approvalLinkVersion(document, versions.asks);
  return (
    // The chip is reached only for a document with an approval that is not a draft - the only
    // approval `ApprovalChip` draws anything for - so the only thing left to decide here is whether
    // this is the issue's primary document, where the actions have always sat. A gate that
    // names no step for that document reaches none of this: the header's own chip carries it.
    <>
      <ApprovalChip
        artifact={document}
        layout="contents"
        showActions={actions && document.id === primaryArtifactId}
        size={size}
        variant="header"
      />
      {page === null || versions === undefined || version === undefined ? null : (
        <ApprovalVersionLink
          document={document}
          issueKey={versions.issueKey}
          version={version}
          words={page}
        />
      )}
    </>
  );
}

/** Why a step's reviewer sent its document back, as one wrapping line: the chip says only
 *  `Changes requested`, and the reason is the whole of what the writer must act on. It is given
 *  no width, no clamp and no tooltip, so a phone reads all of it. */
function StepReason({ entry }: { entry: StepEntry }): ReactNode {
  const approval = entry.document?.approval;
  if (approval === undefined || approval.state !== "changes_requested") {
    return null;
  }
  const version = approvalVersion(approval);
  const reason = approval.reason ?? null;
  return (
    <p
      className={`min-w-0 text-xs [overflow-wrap:anywhere] ${textSecondaryOnSurface}`}
      data-approval-step-reason={entry.step}
    >
      {`${entry.label}: `}
      {approval.by === undefined ? (
        `Changes requested on v${version}`
      ) : (
        <>
          <AgentName actor={approval.by} inline />
          {` requested changes on v${version}`}
        </>
      )}
      {reason === null ? null : ` · ${reason}`}
    </p>
  );
}

/**
 * The design gate's steps in a root issue's header: one line per gated step, in the order the
 * deployment publishes them, each naming the document that step reviews and where that document
 * stands. It takes the place of the header's one chip, which spoke for the primary document
 * alone and left a reader guessing which of three documents the gate was waiting on. The chip
 * is drawn beside this list when no step here reviews the primary document, so that document's
 * state and its two controls never leave the header, whatever the gate names.
 *
 * The new issue page draws the list twice (dispatch://INFRA-1196 R13, R31): its header at the
 * `compact` size with no reasons, and Details with the reasons and, inside `NoTooltips`, each
 * document's link at the version its approval speaks for (`versions`). Each option's default is
 * this header's shape.
 *
 * The caller decides which steps there are (`gatedStepEntries`); this draws them.
 */
export function ApprovalSteps({
  actions = true,
  entries,
  primaryArtifactId,
  reasons = true,
  size = "default",
  testId = "issue-approval-steps",
  versions,
}: {
  /** Whether the primary document's entry carries Approve and Request changes. */
  actions?: boolean;
  entries: StepEntry[];
  primaryArtifactId: string;
  /** Whether each step a reviewer sent back says why, under the list. */
  reasons?: boolean;
  /** `compact` draws each chip and its actions at the new issue page header's size
   *  (`ApprovalChip`), in a list that wraps with 4 px between its lines and takes no full
   *  width. */
  size?: "compact" | "default";
  /** The list's test id: the header's is the one every check reads the gate by, and a second
   *  copy on a page carries its own, so the two are never one ambiguous name. */
  testId?: string;
  /** Given, inside `NoTooltips`, every entry links its document at the version its approval
   *  speaks for. */
  versions?: ApprovalVersions;
}): ReactNode {
  return (
    // The list takes the header row's full width and wraps within itself: three steps, their
    // chips and the two actions never fit a phone's line, and a row that cannot wrap scrolls
    // the page sideways. With no reasons under it, the list is its host's own flex item.
    <div className={reasons ? "flex w-full min-w-0 flex-col gap-2" : "contents"}>
      <ul
        aria-label="Approvals"
        className={
          size === "compact"
            ? "flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1"
            : "flex w-full min-w-0 flex-wrap items-center gap-x-4 gap-y-2"
        }
        data-testid={testId}
      >
        {entries.map((entry) => (
          <li
            className="flex min-w-0 flex-wrap items-center gap-2"
            data-approval-step={entry.step}
            key={entry.step}
          >
            <span className={`text-xs font-medium ${textMutedOnSurface}`}>{entry.label}</span>
            <StepState
              actions={actions}
              entry={entry}
              primaryArtifactId={primaryArtifactId}
              size={size}
              versions={versions}
            />
          </li>
        ))}
      </ul>
      {reasons ? entries.map((entry) => <StepReason entry={entry} key={entry.step} />) : null}
    </div>
  );
}
