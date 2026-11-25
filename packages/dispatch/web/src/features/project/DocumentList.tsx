import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { Link } from "react-router-dom";

import { api } from "../../api/client";
import type { Artifact } from "../../api/types";
import { buttonClassName } from "../../components/Button";
import { EmptyState } from "../../components/EmptyState";
import { fieldLabelClassName, Input } from "../../components/Field";
import { LoadingSkeleton } from "../../components/LoadingSkeleton";
import { TruncatedText } from "../../components/TruncatedText";
import {
  borderDefault,
  borderStrong,
  dangerText,
  linkChrome,
  rowHoverBg,
  surfaceMutedBg,
  textMutedOnCanvas,
  textSecondaryOnSurface,
} from "../../theme/classes";
import { formatArtifactBytes, versionsNewestFirst } from "../artifacts/ArtifactHeader";
import {
  ArtifactDropZone,
  ArtifactUploadStatus,
  useArtifactUpload,
} from "../artifacts/ArtifactUpload";
import { CreateDocument } from "../artifacts/CreateDocument";
import { ApprovalChip } from "../doc/ApprovalChip";
import { CopyRefButton } from "../refs/CopyRefButton";
import { buildIssuePath, buildProjectPath, documentRoute } from "../refs/routes";
import { Timestamp } from "../refs/Timestamp";

function documentUpdatedAt(document: Artifact): string {
  return document.versions?.at(-1)?.created_at ?? document.created_at;
}

function DocumentRow({ document }: { document: Artifact }): ReactNode {
  // The latest version's stored size (its markdown's bytes), so a document near the size cap
  // shows in the list without being opened.
  const size = versionsNewestFirst(document.versions)[0]?.size;
  return (
    <li
      aria-label={document.name}
      // From `sm` a four-column grid; on a phone the name takes its own line and the kind, the
      // issue and the time share the line under it; a two-column grid would stack the kind
      // over a blank cell and put the time a line below both.
      className={`flex flex-wrap items-center gap-x-4 gap-y-1 border-b px-2 py-2.5 sm:grid sm:grid-cols-[minmax(0,1fr)_auto_auto_auto] ${borderDefault} ${rowHoverBg}`}
    >
      <div className="min-w-0 basis-full">
        <div className="flex min-w-0 items-center gap-1">
          {document.issue_key === null ? (
            <Link
              className={`min-w-0 truncate text-sm font-medium ${linkChrome}`}
              to={buildProjectPath({
                kind: "document",
                project: document.project,
                slug: document.slug,
              })}
            >
              <TruncatedText title={document.name}>{document.name}</TruncatedText>
            </Link>
          ) : (
            <Link
              className={`min-w-0 truncate text-sm font-medium ${linkChrome}`}
              to={buildIssuePath({
                key: document.issue_key,
                kind: "artifact",
                slug: document.slug,
              })}
            >
              <TruncatedText title={document.name}>{document.name}</TruncatedText>
            </Link>
          )}
          <CopyRefButton route={documentRoute(document)} />
        </div>
        <ApprovalChip artifact={document} />
      </div>
      <span className={`text-xs ${textMutedOnCanvas}`}>
        {document.kind}
        {size === undefined ? null : ` · ${formatArtifactBytes(size)}`}
      </span>
      <span className="text-xs max-sm:empty:hidden">
        {document.issue_key === null ? null : (
          <Link
            className={`inline-flex h-5 items-center rounded px-1.5 font-medium ${surfaceMutedBg} ${linkChrome}`}
            to={buildIssuePath({ key: document.issue_key, kind: "issue" })}
          >
            {document.issue_key}
          </Link>
        )}
      </span>
      <Timestamp at={documentUpdatedAt(document)} className={`text-xs ${textMutedOnCanvas}`} />
    </li>
  );
}

export function DocumentList({ project }: { project: string }): ReactNode {
  // Documents upload the moment a file is picked or dropped; there is no staging step here.
  const upload = useArtifactUpload({ project }, { immediate: true });
  const documents = useQuery({
    queryKey: ["project", project, "artifacts"],
    queryFn: () => api.listProjectArtifacts(project, true),
  });

  if (documents.isPending) {
    return <LoadingSkeleton label="Loading project documents" />;
  }
  if (documents.isError) {
    return <p className={dangerText}>Could not load documents.</p>;
  }

  return (
    <section aria-label="Project documents" className="space-y-6">
      <CreateDocument documents={documents.data ?? []} owner={{ project }} />
      <section className="space-y-3">
        <label className={fieldLabelClassName} htmlFor="artifact-summary">
          Summary (optional)
          <Input
            className="mt-1.5 font-normal"
            id="artifact-summary"
            onChange={(event) => upload.setSummary(event.target.value)}
            value={upload.summary}
          />
        </label>
        <ArtifactDropZone upload={upload}>
          <button
            // One line: the sentence at the left and the picker at the right, wrapping to two
            // on a phone. Laid out explicitly, since below 1280 px every button is a centred
            // inline flex box (the touch rule in styles.css).
            className={`flex w-full flex-wrap items-center justify-between gap-x-4 gap-y-2 rounded-lg border border-dashed px-4 py-3 text-left ${borderStrong} ${surfaceMutedBg}`}
            onClick={upload.openPicker}
            type="button"
          >
            <span className={`text-sm ${textSecondaryOnSurface}`}>
              Drop a file here, or choose one to upload.
            </span>
            <span className={buttonClassName()}>Choose file</span>
          </button>
        </ArtifactDropZone>
        <ArtifactUploadStatus upload={upload} />
      </section>
      {(documents.data ?? []).length === 0 ? (
        <EmptyState label="Project documents empty state" message="No documents yet." />
      ) : (
        <ul className={`border-t ${borderDefault}`}>
          {(documents.data ?? []).map((document) => (
            <DocumentRow document={document} key={document.id} />
          ))}
        </ul>
      )}
    </section>
  );
}
