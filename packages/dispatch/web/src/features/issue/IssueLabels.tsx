import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useCallback, useMemo, useState } from "react";

import { projectIssuesQuery } from "../../api/queries";
import type { ExternalLink } from "../../api/types";
import { MultiSelect } from "../../components/MultiSelect";
import {
  secondaryButtonBorder,
  secondaryButtonDisabledText,
  secondaryButtonHoverBorder,
  secondaryButtonText,
  surfaceMutedStrongBg,
  textSecondaryOnSurface,
} from "../../theme/classes";

const maxLabelLength = 40;
const maxLabels = 20;

/** A GitHub issue link as Dispatch records the source of an issue it imported, its group the
 *  repository's `<owner>/<repo>`. */
const githubIssueURL = /^https:\/\/github\.com\/([^/]+\/[^/]+)\/issues\/\d+$/;

function labelKey(label: string): string {
  return label.toLowerCase();
}

function uniqueLabels(labels: readonly string[]): string[] {
  const result: string[] = [];
  const seen = new Set<string>();
  for (const rawLabel of labels) {
    const label = rawLabel.trim();
    if (label === "" || seen.has(labelKey(label))) continue;
    seen.add(labelKey(label));
    result.push(label);
  }
  return result;
}

function includesLabel(labels: readonly string[], label: string): boolean {
  return labels.some((current) => labelKey(current) === labelKey(label));
}

/** The key of the issue's own `repo:` label, when it holds one: the label equal to `repo:` and
 *  the `<owner>/<repo>` of one of its GitHub issue links, which Dispatch wrote when it imported
 *  the issue through the default project. The server keeps that label, whatever its length, when
 *  a replacement sends it back, and counts it toward no limit (R14); every other `repo:` label, a
 *  person's or another issue's, is an ordinary label held to 20 labels and 40 characters. */
function ownRepoLabelKey(
  labels: readonly string[],
  externalLinks: readonly ExternalLink[]
): string | undefined {
  const repoKeys = new Set(
    externalLinks.flatMap((link) => {
      const match = githubIssueURL.exec(link.url);
      return match === null ? [] : [labelKey(`repo:${match[1]}`)];
    })
  );
  return labels.map(labelKey).find((key) => repoKeys.has(key));
}

function labelsMatch(left: readonly string[], right: readonly string[]): boolean {
  const leftKeys = new Set<string>();
  const rightKeys = new Set<string>();
  for (const label of left) leftKeys.add(labelKey(label));
  for (const label of right) rightKeys.add(labelKey(label));
  if (leftKeys.size !== rightKeys.size) return false;
  for (const label of leftKeys) {
    if (!rightKeys.has(label)) return false;
  }
  return true;
}

/** The issue's labels go back as the issue spells them, so its own `repo:` label is sent back
 *  unchanged, which is when the server keeps it. */
function saveLabels(draft: readonly string[], labels: readonly string[]): string[] {
  return [
    ...labels.filter((label) => includesLabel(draft, label)),
    ...draft.filter((label) => !includesLabel(labels, label)),
  ];
}

export interface IssueLabelsProps {
  readonly disabled: boolean;
  /** The issue's external links, which name its own `repo:` label (`ownRepoLabelKey`). */
  readonly externalLinks: readonly ExternalLink[];
  readonly labels: readonly string[];
  readonly onSave: (labels: string[]) => Promise<unknown>;
  readonly project: string;
  readonly saving: boolean;
  readonly variant?: "default" | "rail";
}

/** The issue header's labels: the current chips and the shared `MultiSelect`, which stages a
 *  draft while open and saves the final draft once, when it closes. */
export function IssueLabels({
  disabled,
  externalLinks,
  labels,
  onSave,
  project,
  saving,
  variant = "default",
}: IssueLabelsProps): ReactNode {
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState<string[]>([]);
  const [inputError, setInputError] = useState<string | null>(null);
  // The same list the project's issues table and IssueBoard load for this project, so opening
  // the popover on a page that already has it fetches nothing.
  const issues = useQuery({ ...projectIssuesQuery(project, []), enabled: open });
  const ownLabelKey = ownRepoLabelKey(labels, externalLinks);
  // Every label over 40 characters but the issue's own `repo:` label, another issue's long
  // `repo:` label among them, is left out of the options: the server would refuse it.
  const options = useMemo(
    () =>
      uniqueLabels([
        ...draft,
        ...labels,
        ...(issues.data ?? []).flatMap((issue) => issue.labels ?? []),
      ])
        .filter((label) => label.length <= maxLabelLength || labelKey(label) === ownLabelKey)
        .sort((left, right) => {
          const selectedOrder =
            Number(includesLabel(draft, right)) - Number(includesLabel(draft, left));
          return selectedOrder === 0 ? left.localeCompare(right) : selectedOrder;
        }),
    [draft, issues.data, labels, ownLabelKey]
  );
  const displayLabels = open ? draft : labels;

  const setOpenState = useCallback(
    (next: boolean) => {
      if (next) {
        if (disabled || saving) return;
        setDraft([...labels]);
        setInputError(null);
        setOpen(true);
        return;
      }
      if (!open) return;
      const nextLabels = saveLabels(draft, labels);
      setOpen(false);
      setInputError(null);
      if (!labelsMatch(nextLabels, labels)) {
        void onSave(nextLabels).catch(() => undefined);
      }
    },
    [disabled, draft, labels, onSave, open, saving]
  );
  const changeDraft = (next: string[]) => {
    if (next.filter((label) => labelKey(label) !== ownLabelKey).length > maxLabels) {
      setInputError(`An issue can have at most ${maxLabels} labels.`);
      return;
    }
    setDraft(next);
    setInputError(null);
  };
  const createLabel = (value: string) => {
    const label = value.trim();
    if (label.length === 0 || label.length > maxLabelLength) {
      setInputError(`Labels must be 1–${maxLabelLength} characters.`);
      return;
    }
    if (includesLabel(draft, label)) {
      setInputError(null);
      return;
    }
    changeDraft([...draft, label]);
  };

  return (
    <>
      <div
        className={
          variant === "rail"
            ? "relative flex shrink-0 flex-nowrap items-center gap-1"
            : "relative mt-2 flex flex-wrap items-center gap-2"
        }
        data-testid="issue-labels"
      >
        {displayLabels.map((label) => (
          <span
            className={`inline-flex min-h-11 shrink-0 items-center rounded-full px-3 py-1 text-xs font-medium whitespace-nowrap sm:min-h-7 sm:px-2 ${surfaceMutedStrongBg} ${textSecondaryOnSurface}`}
            key={label}
          >
            {label}
          </span>
        ))}
        <MultiSelect
          // Not disabled while it saves what the close committed: a disabled button cannot hold
          // focus, so Escape's return to the trigger would be undone by the save's own render.
          // `setOpenState` is what refuses to reopen until the save has landed, and `aria-disabled`
          // is how the reader is told it is busy without focus being taken away.
          ariaDisabled={saving}
          disabled={disabled}
          emptyMessage="No labels yet."
          error={inputError}
          label="Labels"
          onChange={changeDraft}
          onCreate={createLabel}
          onOpenChange={setOpenState}
          onQueryChange={() => setInputError(null)}
          open={open}
          options={options}
          searchLabel="Search or create label"
          selected={draft}
          triggerAriaLabel="Edit labels"
          triggerClassName={`min-h-11 shrink-0 rounded-lg border px-3 py-1 text-xs font-medium whitespace-nowrap sm:min-h-7 sm:px-2 ${secondaryButtonBorder} ${secondaryButtonText} ${secondaryButtonHoverBorder} ${secondaryButtonDisabledText}`}
        >
          {saving ? "Saving labels…" : displayLabels.length === 0 ? "Labels" : "+"}
        </MultiSelect>
      </div>
      {/* A refused save shows in the shell's pending-writes indicator, with every other write's
          (`PendingWritesIndicator`), and the popover's draft is gone with its close. */}
    </>
  );
}
