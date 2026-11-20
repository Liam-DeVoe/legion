import { afterEach, beforeEach, expect, spyOn } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, waitFor } from "@testing-library/react";
import { http, type RequestHandler } from "msw";
import { MemoryRouter, useLocation } from "react-router-dom";

import { api, type WholeList } from "../api/client";
import type { ArtifactText, ArtifactV2, AskV2, IssueDecisionsV2, MarkV2 } from "../api/types";
import type { ComposerAnchor } from "../features/conversation/composer-model";
import { type DocumentToolbar, ProofDocument } from "../features/doc/ProofDocument";
import { DocumentRuntime } from "../features/doc/runtime";
import { MarginProvider, useMargin } from "../features/margin/margin-context";
import type { MarkPlacement } from "../features/margin/useMarginItems";
import { RefPreviewHost } from "../features/refs/RefPreview";
import { type FakeDocumentRuntime, fakeDocumentRuntime } from "./document-runtime";

/** The ProofDocument suites' shared page: an issue's spec, rendered with a fake document runtime,
 * the margin, and the reference preview host, as the issue page renders it. */

export const artifact: ArtifactV2 = {
  created_at: "2026-09-09T00:00:00Z",
  created_by: { id: "alice", kind: "user" },
  id: "artifact-1",
  issue_key: "CORE-1",
  project: "CORE",
  kind: "doc",
  latest_version: null,
  latest_version_at: null,
  latest_version_size: null,
  name: "spec.md",
  primary: true,
  ref_key: "CORE-1/spec",
  session_id: null,
  slug: "spec",
  unsettled: null,
  version_count: 0,
};

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
}

/**
 * The issue's decisions and the viewer's read mark on a document, answered with the API's empty
 * shape so the page renders its loaded-empty state rather than its error state. A test that
 * asserts one of these spies the `api` method that fetches it and never reaches these handlers.
 *
 * Built afresh on each call rather than held in a module constant: an msw `RequestHandler`
 * carries the state of its own run, and `answerRequests` registers the instance it is handed.
 */
export function decisionsAndReadMarkHandlers(): RequestHandler[] {
  return [
    http.get("/api/v2/issues/:key/decisions", () =>
      Response.json({
        document: null,
        items: [],
        limit: 50,
        offset: 0,
        open_questions: [],
        total: 0,
      } satisfies IssueDecisionsV2)
    ),
    // A document the viewer has never read: its mark names no version.
    http.get("/api/v2/me/marks/document/:id", ({ params }) =>
      Response.json({ id: String(params.id), kind: "document" } satisfies MarkV2)
    ),
    // Opening a document the viewer has never read records the version they are looking at: the
    // page's own write of the mark above.
    http.patch("/api/v2/me/marks/document/:id", async ({ params, request }) => {
      const { read_version } = (await request.json()) as { read_version: number };
      return Response.json({
        id: String(params.id),
        kind: "document",
        read_at: "2026-09-09T00:00:00Z",
        read_version,
      } satisfies MarkV2);
    }),
  ];
}

/**
 * `GET /api/v2/artifacts/:id` answered from `recordOf`, with the API's own answer for an artifact
 * nobody has where that finds none. A read that asks for the document's blocks
 * (`include=blocks`) is answered with none. Each file keeps its own lookup - this module's one
 * artifact, or the set a stubbed issue carries - and the not-found answer stays in one place.
 */
export function artifactRecordHandler(
  recordOf: (id: string) => ArtifactV2 | undefined
): RequestHandler {
  return http.get("/api/v2/artifacts/:id", ({ params, request }) => {
    const record = recordOf(String(params.id));
    if (record === undefined) {
      return Response.json(
        { code: "ARTIFACT_NOT_FOUND", error: `no artifact ${String(params.id)}` },
        { status: 404 }
      );
    }
    const include = new URL(request.url).searchParams
      .getAll("include")
      .flatMap((value) => value.split(","));
    return Response.json(include.includes("blocks") ? { ...record, blocks: [] } : record);
  });
}

/**
 * The reads the document surface makes of its own record, blocks, versions and read mark, and of
 * the issue's asks and decisions: the record handler and the read-mark and decision handlers
 * above, with the versions and the asks beside them. An id this module's artifact does not carry
 * is answered as the API answers an artifact nobody has, and a document's versions as a document
 * that holds none.
 *
 * `asks` is read on each request, so a file that reassigns the rows it seeds between tests is
 * answered the rows it holds then. A caller that answers one of these routes itself passes its
 * own handler before the spread of these: `answerRequests` prepends, so of one call's handlers
 * the first given a path wins it.
 */
export function documentSurfaceHandlers({
  asks = () => [],
}: {
  asks?: () => AskV2[];
} = {}): RequestHandler[] {
  return [
    artifactRecordHandler((id) => (id === artifact.id ? artifact : undefined)),
    http.get("/api/v2/artifacts/:id/versions", () =>
      Response.json({
        items: [],
        limit: 50,
        offset: 0,
        total: 0,
      } satisfies WholeList<"/api/v2/artifacts/{id}/versions">)
    ),
    http.get("/api/v2/issues/:key/asks", () => {
      const rows = asks();
      return Response.json({ items: rows, limit: 50, offset: 0, total: rows.length });
    }),
    ...decisionsAndReadMarkHandlers(),
  ];
}

let defaultText = "";

/** Each test of the calling file starts with the server's text read answering the text its render
 * seeded. A test's own `spyOn(api, "getArtifactText")` returns this same mock and replaces what it
 * answers. */
export function answerTextReadsWithTheSeededText(): void {
  let restore: (() => void) | undefined;
  beforeEach(() => {
    const getArtifactText = spyOn(api, "getArtifactText").mockImplementation(async () => ({
      ...artifact,
      markdown: defaultText,
      token: "sha256:spec-text",
      unsettled: true,
    }));
    restore = () => getArtifactText.mockRestore();
  });
  afterEach(() => restore?.());
}

function CurrentRoute() {
  const location = useLocation();
  return <div data-testid="current-route">{location.pathname}</div>;
}

export function renderProofDocument({
  document = artifact,
  fake = fakeDocumentRuntime({ text: "The live document" }),
  highlightTerm,
  isClosed = false,
  onAskAnswered,
  onUnavailable,
  queryClient = createQueryClient(),
  showDiff = false,
  surface,
  version,
}: {
  document?: ArtifactV2;
  fake?: FakeDocumentRuntime;
  highlightTerm?: string;
  isClosed?: boolean;
  onAskAnswered?(askId: string): void;
  onUnavailable?(reason: string): void;
  queryClient?: QueryClient;
  showDiff?: boolean;
  surface?: "page" | "pane";
  version?: number;
} = {}) {
  const margin = {
    current: undefined as
      | {
          blockFilterId: string | undefined;
          focusBlock(blockId: string): void;
          documentBridge:
            | {
                focusMark(markId: string): void;
                setActiveMarks(markIds: string[]): void;
                whenSent(timeoutMs: number): Promise<void>;
              }
            | undefined;
          focusRequest: { markId: string; seq: number } | undefined;
          hoveredMarkId: string | undefined;
          markPlacements: ReadonlyMap<string, MarkPlacement>;
          composes: ReadonlyMap<
            string,
            {
              open: { anchor: ComposerAnchor; kind: "ask" | "comment" | "suggestion" } | undefined;
              seq: number;
            }
          >;
          cancelCompose(artifact: string, seq: number): void;
        }
      | undefined,
  };
  const toolbar: { current: DocumentToolbar | undefined } = { current: undefined };
  /** Every value the document reported upward, so a suite can assert it reported none at all. */
  const toolbarChanges: (DocumentToolbar | undefined)[] = [];
  const onToolbarChange = (next: DocumentToolbar | undefined) => {
    toolbarChanges.push(next);
    toolbar.current = next;
  };
  const onVersionChange = (_version: number | null) => {};
  const MarginProbe = () => {
    margin.current = useMargin();
    return null;
  };
  const renderDocument = (
    next: { highlightTerm?: string; isClosed?: boolean; showDiff?: boolean; version?: number } = {}
  ) => (
    <MemoryRouter>
      <QueryClientProvider client={queryClient}>
        <DocumentRuntime.Provider value={fake.runtime}>
          <MarginProvider>
            <CurrentRoute />
            <MarginProbe />
            <ProofDocument
              artifact={document}
              highlight={undefined}
              highlightTerm={next.highlightTerm ?? highlightTerm}
              isClosed={next.isClosed ?? isClosed}
              onAskAnswered={onAskAnswered}
              onToolbarChange={onToolbarChange}
              onUnavailable={onUnavailable}
              owner={{ key: "CORE-1", kind: "issue" }}
              onVersionChange={onVersionChange}
              showDiff={next.showDiff ?? showDiff}
              surface={surface}
              user={{ event_head: "0", kind: "user", login: "alice" }}
              version={next.version ?? version}
            />
            <RefPreviewHost />
          </MarginProvider>
        </DocumentRuntime.Provider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  const cachedText = queryClient.getQueryData<Partial<ArtifactText>>([
    "artifact",
    document.id,
    "text",
  ]);
  defaultText = cachedText?.markdown ?? fake.text;
  const view = render(renderDocument());
  return {
    ...fake,
    margin,
    rerender(next: {
      highlightTerm?: string;
      isClosed?: boolean;
      showDiff?: boolean;
      version?: number;
    }) {
      view.rerender(renderDocument(next));
    },
    // The server's sync can only follow the connection, which exists once the lazy transport
    // has resolved.
    async sync() {
      await waitFor(() => expect(fake.connections).toHaveLength(1));
      await act(async () => {
        fake.sync();
      });
    },
    toolbar,
    toolbarChanges,
    view,
  };
}

// `reportLoadFailure`'s `setConnection`/`setLoadError` land through a passive effect
// (`onToolbarChange`, ProofDocument.tsx) that React flushes on its own schedule, one tick behind
// the DOM commit that shows the alert. Under load, a bare `findByRole` can observe the alert
// once it commits without that effect having run yet, so `toolbar.current` still reads
// "connecting" ("expected failed, received connecting" -- a real flake on this box, not a wall-
// clock timeout). Mirroring the component's own `<promise>.then(...).catch(reportLoadFailure)`
// chain shape here, inside `act`, drives React to the same state the component reaches and
// flushes the resulting effect before either assertion below runs -- no retry, no widened
// timeout, no relaxed assertion.
export async function flushLoadFailure(rejection: Promise<unknown>): Promise<void> {
  await act(async () => {
    await rejection.then(() => undefined).catch(() => undefined);
  });
}
