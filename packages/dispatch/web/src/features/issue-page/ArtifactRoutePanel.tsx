import { Fragment, type ReactNode } from "react";

import type { Artifact, Version } from "../../api/types";
import { dangerText, pageQuietText } from "../../theme/classes";
import { ArtifactDetails } from "../artifacts/ArtifactDetails";
import { ArtifactBlobView, ArtifactHeader, versionsNewestFirst } from "../artifacts/ArtifactHeader";
import { AgentName } from "../refs/AgentName";
import type { DispatchRoute } from "../refs/routes";
import { IssueReferenceClosure } from "./IssueReferenceClosure";
import { shortDay } from "./node-time";
import { authorText, pageChrome } from "./page-structure";
import { EMPTY_STATE_LINES, FILE_WORDS } from "./page-words";

/**
 * The line under an opened document's name (part N's R2, the prototype's `openDoc`): the version
 * it shows, who wrote that version and the day it was made, "version 3 · alice · 5 Nov". The
 * number, the names and the day are the document's own; the word and the dots are the page's.
 */
function VersionByline({ version }: { version: Version }): ReactNode {
  const day = shortDay(version.created_at);
  const authors = version.authors ?? [];
  return (
    <p
      className={`mt-1 text-xs ${pageQuietText}`}
      data-testid="opened-artifact-version"
      {...pageChrome}
    >
      {FILE_WORDS.version} <span {...authorText}>{version.number}</span>
      {authors.length === 0 ? null : (
        <>
          {" · "}
          <span {...authorText}>
            {authors.map((author, index) => (
              <Fragment key={`${author.kind}:${author.id}`}>
                {index === 0 ? null : ", "}
                <AgentName actor={author} author inline />
              </Fragment>
            ))}
          </span>
        </>
      )}
      {day === "" ? null : (
        <>
          {" · "}
          <span {...authorText}>{day}</span>
        </>
      )}
    </p>
  );
}

/**
 * One artifact of an issue, as its own address `/issues/<key>/artifacts/<slug>` opens it: drawn
 * in the issue page's main column in place of the outline (dispatch://INFRA-1196 R3), with its
 * header, its editor or its picture, its versions, their downloads and what references it, and
 * the issue's reference closure, which the Artifacts list it replaces drew at its head.
 *
 * A document's version control is the one beside the views, which reads the document opened
 * here (`VersionControl.tsx`), so its header draws none of its own: it is given no toolbar, and
 * draws Correct title and Remove alone (`ArtifactHeader`'s `documentActions`). A file's header
 * keeps its version picker, since the control beside the views reads documents alone. The panel
 * is named by what it holds - the artifact's name - and never by a tab that is gone.
 *
 * `main`'s `features/artifacts/ArtifactRoutePanel.tsx` is today's page's, and does not change
 * (INFRA-1196 R33): this one is the new page's.
 */
export function ArtifactRoutePanel({
  artifact,
  artifactRoute,
  children,
  isClosed,
  versions,
}: {
  artifact: Artifact | undefined;
  artifactRoute: Extract<DispatchRoute, { kind: "artifact" }>;
  /** The document's editor, drawn under its header; nothing for a file. */
  children: ReactNode;
  isClosed: boolean;
  /** The versions the document's editor read, newer than the issue's own read once a version is
   *  named; the artifact's own where no editor reported any. */
  versions?: readonly Version[];
}): ReactNode {
  // A slug the issue does not hold is said in the page's own line: not the slug, which is an
  // identifier, and not the word the old tab used (part N's R2). The region is named by the
  // same line, so the opened place is still a landmark.
  if (artifact === undefined) {
    return (
      <section
        aria-label={EMPTY_STATE_LINES.document_missing}
        className="space-y-3"
        data-testid="opened-artifact-missing"
      >
        <p className={dangerText} {...pageChrome}>
          {EMPTY_STATE_LINES.document_missing}
        </p>
      </section>
    );
  }
  const isDocument = artifact.kind === "doc";
  // The version the head names: the one the address pins, else the latest.
  const listed = versionsNewestFirst(versions ?? artifact.versions);
  const shown =
    artifactRoute.version === undefined
      ? listed[0]
      : listed.find((one) => one.number === artifactRoute.version);
  return (
    <section aria-label={artifact.name} className="space-y-4" data-testid="opened-artifact">
      <ArtifactHeader
        artifact={artifact}
        byline={shown === undefined ? undefined : <VersionByline version={shown} />}
        documentActions={isDocument}
        highlight
        isClosed={isClosed}
        showVersionPicker={!isDocument}
        version={artifactRoute.version}
      >
        {isDocument ? (
          children
        ) : (
          <ArtifactBlobView artifact={artifact} version={artifactRoute.version} />
        )}
      </ArtifactHeader>
      <ArtifactDetails artifact={artifact} />
      <IssueReferenceClosure issueKey={artifactRoute.key} />
    </section>
  );
}
