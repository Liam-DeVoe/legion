import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useId, useRef, useState } from "react";

import {
  api,
  apiErrorCodeIn,
  type CreateArtifactReviewInput,
  isCapExceeded,
} from "../../api/client";
import { inboxQuery } from "../../api/queries";
import type { Artifact, ArtifactApproval } from "../../api/types";
import { heldPress } from "../../components/held-press";
import { approvalPillClassName } from "../../components/Pill";
import { submitOnModifiedEnter } from "../../hooks/submitOnModifiedEnter";
import { useSubmitGuard } from "../../hooks/useSubmitGuard";
import {
  backdrop40,
  card,
  dangerText,
  inputClasses,
  primaryButtonBg,
  primaryButtonDisabled,
  primaryButtonEnabledHoverBg,
  secondaryButtonBorder,
  secondaryButtonDisabledText,
  secondaryButtonHoverBorder,
  secondaryButtonText,
  textMutedOnSurface,
  textPrimaryOnSurface,
  textSecondaryOnSurface,
} from "../../theme/classes";
import { AgentName } from "../refs/AgentName";
import { MarkdownBody } from "../refs/MarkdownBody";
import { Timestamp } from "../refs/Timestamp";
import { textSourceOf } from "../refs/text-references";
import { ControlKeyHint, keyshortcutsOf } from "../shell/ShortcutHelp";
import { useDialog } from "../shell/useDialog";
import { useKeyedWrite } from "../writes/useKeyedWrite";

/** The version an approval speaks for: `version` when the review named one, else the document's
 *  latest. */
export function approvalVersion(approval: ArtifactApproval): number {
  return approval.version ?? approval.latest_version;
}

/** One review the reader pressed: what it says, and the version of the document the page read,
 *  which is the version it approves or asks changes of. A Retry sends that same version again. */
interface ReviewPress {
  readonly input: CreateArtifactReviewInput;
  readonly version: number;
}

/** Dispatch's refusal of a review whose version moved after the page read it (`409
 *  REVIEW_VERSION_MOVED`): its words name the version a review would now record. */
const VERSION_MOVED: ReadonlySet<string> = new Set(["REVIEW_VERSION_MOVED"]);

function approvalLabel(approval: ArtifactApproval): string {
  // A plan is approved by its plan review, never by a person, and its chip says so.
  const approved = approval.kind === "plan_review" ? "Approved by plan review" : "Approved";
  const version = approvalVersion(approval);
  switch (approval.state) {
    case "draft":
      return "Draft";
    case "awaiting":
      return `Awaiting approval${approvalScope(approval)}`;
    case "approved":
      return `${approved} v${version}`;
    case "stale":
      return `${approved} v${version} · changed since`;
    case "changes_requested":
      return "Changes requested";
    default:
      // The route table carries the state as a plain string. Dispatch writes one of the five
      // above; one this build does not know reads as itself, as an unknown capability does
      // (`capabilityLabel`), rather than leaving the chip blank.
      return approval.state;
  }
}

/** The documents an open request covers, when it covers more than the one being read. */
function approvalScope(approval: ArtifactApproval): string {
  const documents = approval.documents ?? [];
  if (documents.length < 2) {
    return "";
  }
  const named = documents.map((document) => `${document.name} v${document.version}`);
  return ` of ${named.slice(0, -1).join(", ")} and ${named[named.length - 1]}`;
}

function ReviewsDialog({
  artifactId,
  onClose,
}: {
  artifactId: string;
  onClose: () => void;
}): ReactNode {
  const dialog = useDialog<HTMLDivElement>({ onClose, open: true });
  const reviews = useQuery({
    queryFn: () => api.listArtifactReviews(artifactId),
    queryKey: ["artifact-reviews", artifactId],
  });

  return (
    <>
      <div aria-hidden="true" className={`fixed inset-0 z-40 ${backdrop40}`} onClick={onClose} />
      <div className="pointer-events-none fixed inset-0 z-40 flex items-start justify-center p-4 pt-20">
        <div
          aria-label="Reviews"
          aria-modal="true"
          className={`pointer-events-auto w-full max-w-md space-y-3 rounded-lg border p-4 shadow-xl ${card}`}
          ref={dialog.containerRef}
          role="dialog"
        >
          <div className="flex items-center justify-between gap-3">
            <h2 className={`text-sm font-semibold ${textSecondaryOnSurface}`}>Reviews</h2>
            <button
              className={`text-sm font-medium ${textMutedOnSurface}`}
              onClick={onClose}
              type="button"
            >
              Close
            </button>
          </div>
          {reviews.isPending ? (
            <p className={`text-sm ${textMutedOnSurface}`}>Loading reviews…</p>
          ) : reviews.isError ? (
            <p className={`text-sm ${dangerText}`}>Could not load reviews.</p>
          ) : reviews.data.length === 0 ? (
            <p className={`text-sm ${textMutedOnSurface}`}>No reviews yet.</p>
          ) : (
            <ul className="space-y-3">
              {reviews.data.map((review) => (
                <li key={review.id}>
                  <p className={`text-sm font-medium ${textPrimaryOnSurface}`}>
                    <AgentName actor={review.actor} inline />{" "}
                    {review.state === "approved" ? "approved" : "requested changes on"} v
                    {review.version} · <Timestamp at={review.created_at} />
                  </p>
                  {review.reason === null ? null : (
                    <div className={`mt-1 text-sm ${textSecondaryOnSurface}`}>
                      <MarkdownBody
                        markdown={review.reason}
                        source={textSourceOf("artifact", artifactId)}
                        tree={review.reason_tree}
                      />
                    </div>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </>
  );
}

function RequestChangesDialog({
  error,
  held,
  onClose,
  onSubmit,
}: {
  /** What the server answered the last request with, or null while none was refused. */
  error: Error | null;
  /** The request is out: the reason and both buttons keep the focus and their Tab stops, and
   *  send or close nothing (`heldPress`). */
  held: boolean;
  onClose: () => void;
  onSubmit: (reason: string) => void;
}): ReactNode {
  const [reason, setReason] = useState("");
  const reasonId = useId();
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const dialog = useDialog<HTMLDivElement>({ initialFocusRef: textareaRef, onClose, open: true });
  const trimmed = reason.trim();

  return (
    <>
      <div aria-hidden="true" className={`fixed inset-0 z-40 ${backdrop40}`} onClick={onClose} />
      <div className="pointer-events-none fixed inset-0 z-40 flex items-start justify-center p-4 pt-20">
        <div
          aria-label="Request changes"
          aria-modal="true"
          className={`pointer-events-auto w-full max-w-sm space-y-3 rounded-lg border p-4 shadow-xl ${card}`}
          ref={dialog.containerRef}
          role="dialog"
        >
          <h2 className={`text-sm font-semibold ${textSecondaryOnSurface}`}>Request changes</h2>
          <label
            className={`block text-sm font-medium ${textSecondaryOnSurface}`}
            htmlFor={reasonId}
          >
            Reason
            <textarea
              aria-disabled={held || undefined}
              className={`mt-1 block w-full rounded-lg border px-2 py-1.5 text-sm font-normal outline-none aria-disabled:opacity-50 ${inputClasses(false)}`}
              id={reasonId}
              onChange={(event) => setReason(event.target.value)}
              onKeyDown={(event) =>
                submitOnModifiedEnter(event, {
                  onFormlessSubmit: () => {
                    if (trimmed !== "" && !held) {
                      onSubmit(trimmed);
                    }
                  },
                })
              }
              readOnly={held}
              ref={textareaRef}
              value={reason}
            />
          </label>
          {error === null ? null : (
            <p className={`text-sm ${dangerText}`} role="alert">
              {/* A reason Dispatch refused for its length says so in its own words, which name
                  the block and the bound, and so does a version that moved after the page read
                  it, naming the version a review would now record; any other failure is worth
                  another try. */}
              {isCapExceeded(error) || apiErrorCodeIn(error, VERSION_MOVED)
                ? error.message
                : "Could not request changes. Try again."}
            </p>
          )}
          <div className="flex items-center justify-end gap-2">
            <button
              className={`rounded-lg border px-3 py-1.5 text-sm font-medium ${secondaryButtonBorder} ${secondaryButtonText} ${secondaryButtonHoverBorder} ${secondaryButtonDisabledText}`}
              {...heldPress(held, onClose)}
              type="button"
            >
              Cancel
            </button>
            <button
              className={`rounded-lg px-3 py-1.5 text-sm font-medium ${primaryButtonBg} ${primaryButtonEnabledHoverBg} ${primaryButtonDisabled}`}
              disabled={trimmed === ""}
              {...heldPress(held, () => onSubmit(trimmed))}
              type="button"
            >
              {held ? "Submitting…" : "Request changes"}
            </button>
          </div>
        </div>
      </div>
    </>
  );
}

/** The keys that press the chip's controls where its host binds them (the Inbox's `a` and
 *  Shift+R, or the person's own): Approve, Request changes, and the pill, which opens the
 *  reviews. */
export interface ApprovalShortcutKeys {
  readonly approve: readonly string[];
  readonly requestChanges: readonly string[];
  readonly showReviews: readonly string[];
}

/** The box each action of the reading pane's head stands in (`pane`, and the head's Dismiss): 44
 *  px high, the height of the tallest control the head's place holds (the earlier approval's Show
 *  diff), with the button centred in it. The place lines its controls up at their tops, so a pill
 *  whose label wraps past that height grows the place downward and moves no action. */
export const paneActionBox = "flex min-h-11 shrink-0 items-center";

export interface ApprovalChipProps {
  /** The document, once the page has read it. Only the reading pane's head (`pane`) draws the
   *  chip before that: Approve and Request changes, held until the read returns; for any other
   *  variant an unread document draws nothing. */
  artifact: Artifact | undefined;
  /** `header`: shown on the document header, with an approval-state chip.
   * `pane`: the header's chip and actions in the Inbox's reading-pane head, which draws the
   * place they stand in at a fixed width from its first draw (`ReadingPaneHead`, R9): each
   * action stands in its own box (`paneActionBox`), compact like the controls beside it, Approve
   * keeps the one label it is drawn with before the read (never `Approve v<latest>`; the pill and
   * its title name the versions), and the pill takes only the room the actions leave.
   * `list` (default): shown in a compact list row. */
  variant?: "header" | "list" | "pane";
  /** The issue header exposes review actions only after an approval request exists. */
  showActions?: boolean;
  /** `contents` lets a parent flex group place the state chip and actions independently. */
  layout?: "contents" | "inline";
  /** Called beside the chip's own refreshes once the server has recorded the reader's review, so
   *  a host that selected the approval request learns it was reviewed here. */
  onReviewed?(): void;
  /** The keys that press the pill and the two actions, where the host draws them: named in each
   *  button's `aria-keyshortcuts`, and the first drawn inside it after its label
   *  (`ControlKeyHint`). */
  shortcutKeys?: ApprovalShortcutKeys;
}

/**
 * The document approval indicator: a small badge summarizing `artifact.approval`, clickable to
 * open a popover of every human review (`ArtifactReview`). On the document header only, it also
 * carries the Approve / Request changes actions that write a new review, of the version the page
 * read: a version that moved after the read is refused, and the refusal is shown here.
 *
 * The two actions carry `data-approval-action` (`approve`, `request-changes`), which the keys
 * that press them find them by (the Inbox's `a` and Shift+R), and the badge `data-inbox-action`
 * (`show-reviews`), which the Inbox's command bar presses it by.
 *
 * In the reading pane's head (`pane`) one chip stands for one approval request from the head's
 * first draw: the two actions it draws, held, before the document's read returns are the
 * buttons it acts with once the read has returned, so neither moves, and a reader who reached
 * one with Tab keeps the focus on it.
 */
export function ApprovalChip({
  artifact,
  layout = "inline",
  onReviewed,
  shortcutKeys,
  variant = "list",
  showActions = variant !== "list",
}: ApprovalChipProps): ReactNode {
  const approval = artifact?.approval;
  const [reviewsOpen, setReviewsOpen] = useState(false);
  const [requestChangesOpen, setRequestChangesOpen] = useState(false);
  const queryClient = useQueryClient();
  const submitGuard = useSubmitGuard();
  /** The document a review is written of. A review is pressed only once the read has given the
   *  approval, which comes with the document: before that both actions are held. */
  const reviewed = (): Artifact => {
    if (artifact === undefined) {
      throw new Error("A review is pressed only once the document has been read.");
    }
    return artifact;
  };

  const review = useKeyedWrite({
    label: ({ input }: ReviewPress) =>
      `${input.state === "approved" ? "Approval" : "Change request"} on ${reviewed().name}`,
    onError: (error) => {
      // The document moved after the page read it: the page reads it again, so the next press
      // reviews the version a review would now record, which the reader then has in front of
      // them, and never one they did not read.
      if (apiErrorCodeIn(error, VERSION_MOVED)) {
        void queryClient.invalidateQueries({ queryKey: ["artifact", reviewed().id] });
      }
    },
    onSettled: () => submitGuard.release(),
    write: ({ input, version }, bounds) =>
      api.createArtifactVersionReview(reviewed().id, version, input, bounds),
    onSuccess: () => {
      const target = reviewed();
      setRequestChangesOpen(false);
      onReviewed?.();
      void queryClient.invalidateQueries({ queryKey: ["artifact-reviews", target.id] });
      void queryClient.invalidateQueries({ queryKey: ["artifact", target.id] });
      void queryClient.invalidateQueries({ queryKey: inboxQuery().queryKey });
      if (target.issue_key === null) {
        void queryClient.invalidateQueries({
          queryKey: ["project", target.project, "artifacts"],
        });
        void queryClient.invalidateQueries({
          queryKey: ["artifact-ref", `${target.project}/${target.slug}`],
        });
        return;
      }
      void queryClient.invalidateQueries({ queryKey: ["issue", target.issue_key] });
      void queryClient.invalidateQueries({ queryKey: ["artifacts", target.issue_key] });
      void queryClient.invalidateQueries({ queryKey: ["asks", target.issue_key] });
    },
  });

  const pane = variant === "pane";
  // An unread document draws nothing but in the reading pane's head, which holds its two actions
  // there until the read returns; a document with no approval, or a draft, draws nothing at all.
  if (artifact === undefined ? !pane : approval === undefined || approval.state === "draft") {
    return null;
  }

  /** Held while a review is out, and in the pane's head until the read has given the approval. */
  const held = review.isPending || approval === undefined;
  const approveLabel =
    !pane && approval?.state === "stale" ? `Approve v${approval.latest_version}` : "Approve";
  /** Sends the review of the version the page read: the latest, which Approve names when the
   *  approval it follows is stale, and which the reading pane draws. */
  const send = (input: CreateArtifactReviewInput) => {
    if (approval === undefined) return;
    submitGuard.guard(() => review.mutate({ input, version: approval.latest_version }));
  };
  // An Approve the server refused because the version moved, in its words; the dialog shows its
  // own press's refusal itself.
  const movedRefusal =
    !requestChangesOpen && review.isError && apiErrorCodeIn(review.error, VERSION_MOVED)
      ? review.error.message
      : null;
  // The pane's head draws its actions as compact as the controls beside them, so the two and the
  // pill hold one line of the head's place.
  const actionSize = pane ? "px-2 py-1 text-xs" : "px-3 py-2 text-sm md:py-1";
  // One filled button per surface: approving is the action the header wants, and two outline
  // buttons made a reader choose before reading either.
  const approveButton = (
    <button
      aria-keyshortcuts={keyshortcutsOf(shortcutKeys?.approve)}
      className={`min-h-11 rounded-lg font-medium md:min-h-8 ${actionSize} ${primaryButtonBg} ${primaryButtonEnabledHoverBg} ${primaryButtonDisabled}`}
      data-approval-action="approve"
      {...heldPress(held, () => send({ state: "approved" }))}
      type="button"
    >
      {approveLabel}
      <ControlKeyHint keys={shortcutKeys?.approve} />
    </button>
  );
  const requestChangesButton = (
    <button
      aria-keyshortcuts={keyshortcutsOf(shortcutKeys?.requestChanges)}
      className={`min-h-11 rounded-lg border font-medium md:min-h-8 ${actionSize} ${secondaryButtonBorder} ${secondaryButtonText} ${secondaryButtonHoverBorder} ${secondaryButtonDisabledText}`}
      data-approval-action="request-changes"
      {...heldPress(held, () => setRequestChangesOpen(true))}
      type="button"
    >
      Request changes
      <ControlKeyHint keys={shortcutKeys?.requestChanges} />
    </button>
  );

  const pill =
    approval === undefined ? null : (
      <button
        aria-haspopup="dialog"
        aria-keyshortcuts={keyshortcutsOf(shortcutKeys?.showReviews)}
        className={approvalPillClassName(approval.state)}
        data-inbox-action="show-reviews"
        onClick={() => setReviewsOpen(true)}
        title={
          approval.state === "stale"
            ? `Approved at version ${approval.version}; the latest version is ${approval.latest_version}`
            : undefined
        }
        type="button"
      >
        {approvalLabel(approval)}
        <ControlKeyHint keys={shortcutKeys?.showReviews} />
      </button>
    );

  return (
    <div
      className={layout === "contents" ? "contents" : "inline-flex flex-wrap items-center gap-2"}
    >
      {pane && pill !== null ? (
        // In the pane's head the pill takes the room the two actions leave on their line, from
        // none at all (`basis-0`), so a label of many documents wraps inside it and never takes
        // an action off that line.
        <span className="flex min-w-0 grow basis-0 justify-end self-center">{pill}</span>
      ) : (
        pill
      )}
      {variant !== "list" && showActions ? (
        // In the pane's head each action stands in its own box.
        <>
          {approval?.state === "approved" ? null : pane ? (
            <span className={paneActionBox}>{approveButton}</span>
          ) : (
            approveButton
          )}
          {pane ? (
            <span className={paneActionBox}>{requestChangesButton}</span>
          ) : (
            requestChangesButton
          )}
          {movedRefusal === null ? null : (
            <p className={`basis-full text-sm ${dangerText}`} role="alert">
              {movedRefusal}
            </p>
          )}
        </>
      ) : null}
      {reviewsOpen && artifact !== undefined ? (
        <ReviewsDialog artifactId={artifact.id} onClose={() => setReviewsOpen(false)} />
      ) : null}
      {requestChangesOpen ? (
        <RequestChangesDialog
          error={review.isError ? review.error : null}
          held={review.isPending}
          onClose={() => {
            review.reset();
            setRequestChangesOpen(false);
          }}
          onSubmit={(reason) => send({ reason, state: "changes_requested" })}
        />
      ) : null}
    </div>
  );
}
