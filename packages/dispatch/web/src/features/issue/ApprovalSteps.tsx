import type { ReactNode } from "react";

import { approvalPillClassName } from "../../components/Pill";
import { textMutedOnSurface, textSecondaryOnSurface } from "../../theme/classes";
import { ApprovalChip, approvalVersion } from "../doc/ApprovalChip";
import { AgentName } from "../refs/AgentName";
import type { StepEntry } from "./gated-steps";
import { CONTROL_LABELS } from "./page-words";

/** The state of one step: its document's chip, or the dashed pill that says nobody has asked for
 *  this document to be reviewed - a step with no document, a document with no approval, and a
 *  draft all read the same to someone waiting on the gate, and none of the three has a review to
 *  open, so the pill is a span rather than a button. */
function StepState({
  actions,
  entry,
  primaryArtifactId,
  size,
}: {
  actions: boolean;
  entry: StepEntry;
  primaryArtifactId: string;
  size: "compact" | "default";
}): ReactNode {
  const { document } = entry;
  if (document?.approval === undefined || document.approval.state === "draft") {
    return (
      <span className={approvalPillClassName("draft", size)}>{CONTROL_LABELS.not_requested}</span>
    );
  }
  return (
    // The chip is reached only for a document with an approval that is not a draft - the only
    // approval `ApprovalChip` draws anything for - so the only thing left to decide here is whether
    // this is the issue's primary document, where the actions have always sat. A gate that
    // names no step for that document reaches none of this: the header's own chip carries it.
    <ApprovalChip
      artifact={document}
      layout="contents"
      showActions={actions && document.id === primaryArtifactId}
      size={size}
      variant="header"
    />
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
 * The design gate's steps of a root issue: one entry per gated step, in the order the
 * deployment publishes them, each naming the document that step reviews and where that document
 * stands. The header draws them in its properties line with Approve and Request changes while an
 * approval request waits on the reader; Details draws them again with the reasons a reviewer
 * gave and those two controls otherwise (dispatch://INFRA-1196 R13, R31). The chip is drawn
 * beside this list when no step here reviews the primary document, so that document's state and
 * its two controls never leave the page, whatever the gate names.
 *
 * The caller decides which steps there are (`gatedStepEntries`); this draws them.
 */
export function ApprovalSteps({
  actions,
  entries,
  primaryArtifactId,
  reasons,
  size = "default",
  testId = "issue-approval-steps",
}: {
  /** Whether the primary document's entry carries Approve and Request changes here. */
  actions: boolean;
  entries: StepEntry[];
  primaryArtifactId: string;
  /** Whether each step a reviewer sent back says why, under the list. */
  reasons: boolean;
  /** `compact` draws each chip and its actions at the header's own size (`ApprovalChip`). */
  size?: "compact" | "default";
  /** The list's test id: the header's is the one every check reads the gate by, and Details'
   *  copy carries its own, so the two are never one ambiguous name. */
  testId?: string;
}): ReactNode {
  return (
    // The list wraps within itself: three steps, their chips and the two actions never fit a
    // phone's line, and a row that cannot wrap scrolls the page sideways.
    <div className={reasons ? "flex w-full min-w-0 flex-col gap-2" : "contents"}>
      <ul
        aria-label="Approvals"
        className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1"
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
            />
          </li>
        ))}
      </ul>
      {reasons ? entries.map((entry) => <StepReason entry={entry} key={entry.step} />) : null}
    </div>
  );
}
