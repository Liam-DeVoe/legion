import { type ReactNode, useState } from "react";
import { Button } from "../../components/Button";
import { Select } from "../../components/Field";
import { dangerText, successText } from "../../theme/classes";
import { ConnectionDot } from "../doc/ConnectionDot";
import type { DocumentToolbar } from "../doc/ProofDocument";
import { CopyRefButton } from "../refs/CopyRefButton";
import type { DispatchReferenceRoute } from "../refs/routes";

/** Compact primary-Spec controls, one row under the issue's tab bar: the version actions and
 *  the connection dot that belongs to them at the row's start, the two copies at its end.
 *  `reference` is the document as shown — the spec, or a historical version of it. */
export function SpecToolbar({
  isClosed,
  onShowDiffChange,
  onVersionChange,
  reference,
  showDiff,
  toolbar,
  version,
}: {
  isClosed: boolean;
  onShowDiffChange(next: boolean): void;
  onVersionChange(version: number | null): void;
  reference: DispatchReferenceRoute;
  showDiff: boolean;
  toolbar: DocumentToolbar;
  version: number | undefined;
}): ReactNode {
  const [copyFeedback, setCopyFeedback] = useState<"copied" | "failed" | undefined>(undefined);
  return (
    <div className="flex w-full min-w-0 flex-wrap items-center gap-2">
      {/* The control names itself rather than carrying a separate label element: every option
          says "version", so the closed select reads as a version picker and the strip still
          stays one compact row above the document. */}
      <Select
        aria-label="Version"
        className="max-w-48 flex-1 sm:flex-none"
        size="sm"
        onChange={(event) =>
          onVersionChange(event.target.value === "" ? null : Number(event.target.value))
        }
        value={version ?? ""}
      >
        <option value="">Current version</option>
        {toolbar.versions.map((item) => (
          <option key={item.number} value={item.number}>
            Version {item.number}
            {/* A version's summary, as its writer gave it: the reading list's
                `spec-version-summary`. */}
            {item.named && item.summary !== null ? ` — ${item.summary}` : ""}
          </option>
        ))}
      </Select>
      <Button
        aria-label="Name version"
        size="sm"
        disabled={isClosed || toolbar.isNamingVersion}
        onClick={toolbar.requestNamedVersion}
      >
        Name
      </Button>
      {version === undefined ? null : (
        <Button size="sm" onClick={() => onShowDiffChange(!showDiff)}>
          {showDiff ? "Show version" : "Diff vs current"}
        </Button>
      )}
      <ConnectionDot connection={toolbar.connection} pending={toolbar.pending} />
      {/* The copies sit at the row's end, apart from the version actions. The answer to a copy
          comes before its button, so the button keeps its place at the row's edge. */}
      <span className="ml-auto flex min-w-0 shrink-0 items-center gap-2">
        <CopyRefButton route={reference} />
        {version === undefined ? (
          <>
            {copyFeedback === undefined ? null : (
              <span
                className={`text-xs font-medium ${copyFeedback === "copied" ? successText : dangerText}`}
                role="status"
              >
                {copyFeedback === "copied" ? "Copied" : "Copy failed - select the text"}
              </span>
            )}
            <Button
              aria-label="Copy link to block"
              size="sm"
              onClick={() => {
                void toolbar.copyBlockLink().then(
                  (copied) => setCopyFeedback(copied ? "copied" : "failed"),
                  () => setCopyFeedback("failed")
                );
              }}
            >
              Copy link
            </Button>
          </>
        ) : null}
      </span>
    </div>
  );
}
