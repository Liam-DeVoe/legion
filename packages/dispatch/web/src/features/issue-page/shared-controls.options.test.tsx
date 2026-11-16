import { afterEach, beforeEach, expect, type Mock, spyOn, test } from "bun:test";
import { act, type RenderResult, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";

import { fakeDocumentRuntime } from "../../__tests__/document-runtime";
import { graphNode } from "../../__tests__/graph-fixture";
import { artifact as specDocument } from "../../__tests__/proof-document";
import { renderWithQueries } from "../../__tests__/query-provider";
import { ApiError, api } from "../../api/client";
import type {
  Actor,
  Agent,
  ArchitectureSource,
  ArchitectureTree,
  Artifact,
  ArtifactApproval,
  Ask,
  AskRead,
  ClaimHolderLiveness,
  IssueClaim,
  IssueComponents,
  Priorities,
  PriorityRow,
} from "../../api/types";
import { CopyButton } from "../../components/CopyButton";
import { MultiSelect } from "../../components/MultiSelect";
import { approvalPillClassName, LabelPill, Pill, StatusPill } from "../../components/Pill";
import { PinButton } from "../../components/PinButton";
import { RefusableButton } from "../../components/RefusableButton";
import { pageHeaderControl, pageInkText, pageLabelChip } from "../../theme/classes";
import { ConnectionDot } from "../doc/ConnectionDot";
import { ProofDocument } from "../doc/ProofDocument";
import { DocumentRuntime } from "../doc/runtime";
import { AskCard } from "../inbox/AskCard";
import { ApprovalSteps } from "../issue/ApprovalSteps";
import { AssigneeControl } from "../issue/AssigneeControl";
import { badgeSelectWrapper } from "../issue/badge-select";
import { ClaimChip } from "../issue/ClaimChip";
import { GitHubLink } from "../issue/GitHubLink";
import type { StepEntry } from "../issue/gated-steps";
import { IssueComponentsLine } from "../issue/IssueComponentsLine";
import { IssueLabels } from "../issue/IssueLabels";
import { PriorityControl, PriorityEditor } from "../issue/PriorityControl";
import { RepoControl } from "../issue/RepoControl";
import {
  RouteReachUnknownMarker,
  UnreachableRouteMarker,
  UnresponsiveRouteMarker,
} from "../issue/RouteReach";
import { SubscribedAgents } from "../issue/SubscribedAgents";
import type { IssuePriorityWrite } from "../issue/useIssuePriority";
import { MarginProvider } from "../margin/margin-context";
import { IssuePriorityLine } from "../priorities/IssuePriorityLine";
import { AgentName, AgentUnresponsiveNote, useAgentText } from "../refs/AgentName";
import { Timestamp } from "../refs/Timestamp";
import { NoTooltips } from "./NoTooltips";
import { PROPERTY_LABELS } from "./page-structure";
import { AUTHOR_KIND_WORDS, CONTROL_LABELS } from "./page-words";

/**
 * The options the shared controls take (dispatch://INFRA-3935 R1, R3, R4, R5 and R10): each
 * option's default is `main`'s shape, the options of the new issue page's surface change nothing
 * outside `NoTooltips`, and each shape an option draws is the one R3 to R5 describe. Each case
 * draws the controls under a router and a query client of their own, every read answered by a
 * stub on `api` below.
 */

const SPEC_ID = "artifact-spec";
const DECISIONS_ID = "artifact-decisions";
const VERIFICATION_ID = "artifact-verification";

function doc(id: string, name: string, approval?: ArtifactApproval): Artifact {
  return {
    created_at: "2026-09-09T00:00:00Z",
    created_by: { id: "alice", kind: "user" },
    id,
    issue_key: "CORE-1",
    kind: "doc",
    name,
    primary: id === SPEC_ID,
    project: "CORE",
    ref_key: `CORE-1/${name}`,
    session_id: null,
    slug: name.replace(".", "-"),
    versions: [],
    ...(approval === undefined ? {} : { approval }),
  };
}

function specEntry(approval?: ArtifactApproval): StepEntry {
  return { document: doc(SPEC_ID, "spec.md", approval), label: "Spec", step: "spec" };
}

function decisionsEntry(approval?: ArtifactApproval): StepEntry {
  return {
    document: doc(DECISIONS_ID, "decisions.md", approval),
    label: "Decisions",
    step: "decisions",
  };
}

function verificationEntry(approval?: ArtifactApproval): StepEntry {
  return {
    document: doc(VERIFICATION_ID, "verification.md", approval),
    label: "Verification",
    step: "criteria",
  };
}

// ---------------------------------------------------------------------------------------------
// What the controls read, each answered by a stub on `api`
// ---------------------------------------------------------------------------------------------

const LIVE_SESSION = "session-live-0001";
const STALLED_SESSION = "session-stalled-02";
const GONE_SESSION = "session-gone-00003";
/** A session nothing names: the registry does not list it and no write stamped its title. Its
 *  short id, `main`'s name for it, is `session:01234567…`. */
const NAMELESS_SESSION = "0123456789abcdef";

/** One session as `GET /api/v1/agents` lists it (`AgentName.test.tsx`'s `listed`). */
function listed(sessionId: string, title: string, state: Agent["state"] = "live"): Agent {
  return {
    capabilities: [],
    dir: "/w",
    host: "host",
    last_activity: null,
    last_seen: Date.now() - 3 * 60_000,
    machine_id: "host",
    open_asks: 0,
    roles: [],
    session_id: sessionId,
    state,
    title,
  };
}

/** A priority as the priorities read answers it (`IssuePriorityLine.test.tsx`'s `priority`). */
function priority(id: string, title: string): PriorityRow {
  return {
    description: "",
    id,
    made_at: "2026-11-01T00:00:00Z",
    made_by: { id: "alice", kind: "user" },
    owner: "INFRA controller",
    rank: id,
    state: "live",
    title,
    work: {
      by_status: {},
      claimed: 0,
      closed_last_7_days: 0,
      newest_event_at: null,
      open_asks: 0,
    },
  };
}

const priorities: Priorities = {
  priorities: [
    priority("evals-now", "Evals collecting data now"),
    priority("rocq-lean", "Rocq and Lean speed work restarted"),
  ],
  priority_work_parked: { by_priority: {}, count: 0, issues: [] },
  work_under_no_priority: { by_project: {}, count: 0, issues: [] },
};

/** The project's architecture, holding the one component the components line names
 *  (`IssueComponentsLine.test.tsx`'s `tree`). */
const architecture: ArchitectureTree = {
  components: [
    {
      depends_on: [],
      done: 0,
      external: false,
      id: "dispatch-server",
      issues: [],
      own_done: 0,
      own_total: 0,
      parent: null,
      paths: [],
      prose: "",
      source: "legion/legion",
      title: "Dispatch server",
      total: 0,
    },
  ],
  not_architectural: [],
  retired_links: [],
  sources: [],
  totals: {
    components_without_work: 0,
    issues_done: 0,
    issues_total: 0,
    not_architectural: 0,
    retired_links: 0,
    unassigned: 0,
  },
  unassigned: [],
};

/** The project's one architecture source, so the components line draws its picker
 *  (`IssueComponentsLine.test.tsx`'s `source`). */
const source: ArchitectureSource = {
  branch: "main",
  components: 1,
  created_at: "2026-09-10T00:00:00Z",
  created_by: { id: "alice", kind: "user" },
  enabled: true,
  last_commit: "3be61df5be90",
  last_error: null,
  last_sync_at: "2026-09-17T00:00:00Z",
  project: "CORE",
  repo: "legion/legion",
};

/** The `api` reads the controls make, each answered for the whole file and restored after each
 *  test: a read no stub answers reaches the network, which fails the test (`setup.ts`). */
interface Stubs {
  readonly getArchitecture: Mock<typeof api.getArchitecture>;
  readonly getArtifact: Mock<typeof api.getArtifact>;
  readonly getArtifactBlocks: Mock<typeof api.getArtifactBlocks>;
  readonly getArtifactText: Mock<typeof api.getArtifactText>;
  readonly getDocumentRead: Mock<typeof api.getDocumentRead>;
  readonly getIssueDecisions: Mock<typeof api.getIssueDecisions>;
  readonly getIssueSubscribers: Mock<typeof api.getIssueSubscribers>;
  readonly getReferences: Mock<typeof api.getReferences>;
  readonly githubRest: Mock<typeof api.githubRest>;
  readonly listAgents: Mock<typeof api.listAgents>;
  readonly listIssueAsks: Mock<typeof api.listIssueAsks>;
  readonly listPriorities: Mock<typeof api.listPriorities>;
  readonly listProjectArchitectureSources: Mock<typeof api.listProjectArchitectureSources>;
  readonly listRepoProjects: Mock<typeof api.listRepoProjects>;
  readonly listUsers: Mock<typeof api.listUsers>;
  readonly putDocumentRead: Mock<typeof api.putDocumentRead>;
  readonly whoAmI: Mock<typeof api.whoAmI>;
}

let stubs: Stubs;

beforeEach(() => {
  stubs = {
    getArchitecture: spyOn(api, "getArchitecture").mockResolvedValue(architecture),
    getArtifact: spyOn(api, "getArtifact").mockResolvedValue(specDocument),
    // The spec holds one block, which one ask and one comment reference.
    getArtifactBlocks: spyOn(api, "getArtifactBlocks").mockResolvedValue([
      {
        from: 0,
        id: "block-1",
        references: { asks: 1, comments: 1 },
        to: 17,
        token: "sha256:block-1",
        type: "paragraph",
      },
    ]),
    getArtifactText: spyOn(api, "getArtifactText").mockResolvedValue({
      markdown: "The live document",
      token: "sha256:spec-text",
      version: null,
    }),
    getDocumentRead: spyOn(api, "getDocumentRead").mockImplementation(async (id) => ({
      artifact_id: id,
      read_at: null,
      version: null,
    })),
    getIssueDecisions: spyOn(api, "getIssueDecisions").mockResolvedValue({
      decisions: [],
      document: null,
      open_questions: [],
    }),
    getIssueSubscribers: spyOn(api, "getIssueSubscribers").mockResolvedValue([]),
    getReferences: spyOn(api, "getReferences").mockResolvedValue({
      edges: [],
      node: graphNode({ id: "ask-1", kind: "ask" }),
    }),
    githubRest: spyOn(api, "githubRest").mockImplementation(async (path) => {
      if (path === "repos/owner/repository/issues/7") {
        return new Response(JSON.stringify({ state: "open", title: "Ship it" }), {
          headers: { "Content-Type": "application/json" },
        });
      }
      throw new Error(`no GitHub answer for ${path}`);
    }),
    listAgents: spyOn(api, "listAgents").mockResolvedValue([
      listed(LIVE_SESSION, "Implementer"),
      listed(STALLED_SESSION, "Reviewer", "unresponsive"),
    ]),
    listIssueAsks: spyOn(api, "listIssueAsks").mockResolvedValue([]),
    listPriorities: spyOn(api, "listPriorities").mockResolvedValue(priorities),
    listProjectArchitectureSources: spyOn(api, "listProjectArchitectureSources").mockResolvedValue([
      source,
    ]),
    listRepoProjects: spyOn(api, "listRepoProjects").mockResolvedValue([]),
    listUsers: spyOn(api, "listUsers").mockResolvedValue([
      { login: "alice@example.com" },
      { login: "bob@example.com" },
    ]),
    putDocumentRead: spyOn(api, "putDocumentRead").mockImplementation(async (id, version) => ({
      artifact_id: id,
      read_at: "2026-09-09T00:00:00Z",
      version,
    })),
    whoAmI: spyOn(api, "whoAmI").mockResolvedValue({
      event_head: "0",
      kind: "user",
      login: "alice",
    }),
  };
});

afterEach(() => {
  for (const stub of Object.values(stubs)) stub.mockRestore();
});

/** An open question as the Inbox hands it (`AskCard.test.tsx`'s `answeredAsk`, still open). */
function openAsk(overrides: Partial<Ask> = {}): Ask {
  return {
    anchor: null,
    answer: null,
    artifact_id: null,
    asked_of: null,
    asked_of_at: null,
    asked_of_by: null,
    author: {
      id: LIVE_SESSION,
      kind: "session",
      origin: { session_title: "Implementer", tmux: "legion:3" },
    },
    block_id: null,
    created_at: "2026-10-08T00:00:00Z",
    edited_at: null,
    id: "ask-1",
    issue_key: "CORE-1",
    kind: "question",
    multiple: false,
    opened_event_id: 1,
    options: [{ label: "Ship" }, { label: "Hold" }],
    question: "Which release path?",
    state: "open",
    urgency: "blocking",
    waiting_on: "human",
    ...overrides,
  };
}

/** The ask's read, as `GET /api/v1/asks/{id}` answers it (`AskCard.test.tsx`'s `thread`). */
function threadOf(ask: Ask): AskRead {
  return {
    answers: [],
    ask,
    claimant: null,
    edits: [],
    followers: [],
    replies: [],
    route: null,
    route_holder: null,
    route_status: null,
    route_status_reason: null,
  };
}

/** An open decision in the spec, its own block, which the document's navigator counts. */
function blockAsk(id: string, blockId: string, question: string) {
  return {
    ...openAsk({
      block_artifact: { id: specDocument.id, primary: true, slug: "spec" },
      block_id: blockId,
      id,
      question,
      urgency: "high",
    }),
    last_reply: null,
  };
}

/** The ProofDocument suites' page (`__tests__/proof-document.tsx`'s `renderProofDocument`): the
 *  issue's spec with a fake document runtime and the margin, on its own page. */
function proofDocument(): ReactNode {
  const fake = fakeDocumentRuntime({ text: "The live document" });
  return (
    <DocumentRuntime.Provider value={fake.runtime}>
      <MarginProvider>
        <ProofDocument
          artifact={specDocument}
          highlight={undefined}
          isClosed={false}
          onToolbarChange={() => undefined}
          onVersionChange={() => undefined}
          owner={{ key: "CORE-1", kind: "issue" }}
          showDiff={false}
          surface="page"
          user={{ event_head: "0", kind: "user", login: "alice" }}
          version={undefined}
        />
      </MarginProvider>
    </DocumentRuntime.Provider>
  );
}

// ---------------------------------------------------------------------------------------------
// Drawing a control on either side of `NoTooltips`, and reading what it drew
// ---------------------------------------------------------------------------------------------

/** Which side of `NoTooltips` a control is drawn on: `inside` is the new issue page's surface,
 *  `outside` every page of `main`. */
type Surface = "inside" | "outside";

function draw(node: ReactNode, surface: Surface): RenderResult {
  return renderWithQueries(
    <MemoryRouter>{surface === "inside" ? <NoTooltips>{node}</NoTooltips> : node}</MemoryRouter>
  );
}

/** Takes a drawn control off the page, its container with it. */
function undraw(view: RenderResult): void {
  view.unmount();
  view.container.remove();
}

/** Lets every read a control started answer and every render after it land. */
async function settleReads(): Promise<void> {
  for (let turn = 0; turn < 5; turn += 1) {
    await act(async () => {
      const { promise, resolve } = Promise.withResolvers<void>();
      setTimeout(resolve, 0);
      await promise;
    });
  }
}

/** What `node` draws on `surface` once its reads have landed. React numbers each `useId` from
 *  one counter for every root (`_r_<n>_`), so two mounts of one control differ by that number
 *  alone; it is read as one placeholder. */
async function markupOf(node: ReactNode, surface: Surface): Promise<string> {
  const view = draw(node, surface);
  try {
    await settleReads();
    return view.container.innerHTML.replace(/_r_[0-9a-v]+_/g, "_r_id_");
  } finally {
    undraw(view);
  }
}

function tokens(className: string): string[] {
  return className.split(/\s+/).filter(Boolean);
}

function classOf(element: Element): string[] {
  return tokens(element.getAttribute("class") ?? "");
}

/** The element a control drew inside its `<div data-case>` of a mount, by selector. */
function caseOf(view: RenderResult, name: string): HTMLElement {
  const found = view.container.querySelector<HTMLElement>(`[data-case="${name}"]`);
  if (found === null) throw new Error(`no case ${name}`);
  return found;
}

function parentOf(element: Element): HTMLElement {
  const parent = element.parentElement;
  if (parent === null) throw new Error("the element has no parent");
  return parent;
}

function optionsOf(select: HTMLElement): string[][] {
  return [...(select as HTMLSelectElement).options].map((option) => [option.value, option.text]);
}

/** The header's priority write, which no case presses. */
const idlePriorityWrite: IssuePriorityWrite = {
  failed: false,
  pending: false,
  retry: () => undefined,
  submit: () => undefined,
};

const CLAIMED_AT = "2026-09-24T06:00:00Z";

function sessionClaim(sessionId: string): IssueClaim {
  return { actor: { id: sessionId, kind: "session" }, at: CLAIMED_AT };
}

const minutesFromNow = (minutes: number) => new Date(Date.now() + minutes * 60_000).toISOString();

function explicitComponents(ids: string[]): IssueComponents {
  return { ids, inherited_from: null, mode: "explicit", reason: null, unknown: [] };
}

function componentsIssue(components: IssueComponents, key = "CORE-1") {
  return { components, key, project: "CORE", status: "todo", title: "Ship" };
}

// ---------------------------------------------------------------------------------------------
// The cases
// ---------------------------------------------------------------------------------------------

// dispatch://INFRA-1196 R19: the link to the version an approval request asks about, which the
// panel's old card drew, is Details' approval entry's own: inside `NoTooltips` each entry links
// its document at the version the request names - not the document's latest - or the version a
// review approved, opened in place of the outline. The header's copy holds R13's list and
// nothing else, so it links nothing. R4: `actions`, `reasons` and `testId` default to the
// header's shape of `main`: both actions on the primary document, each reason under the list,
// and `issue-approval-steps`.
test("an approval entry links its document at the version the request asks about", () => {
  const request = {
    approval: { artifact_id: SPEC_ID, name: "spec.md", requested_version: 1, version: 1 },
    id: "ask-1",
    kind: "approval",
    state: "open",
  } as unknown as Ask;
  const entries = [
    specEntry({ latest_version: 2, state: "awaiting", waiting_on: "human" }),
    decisionsEntry({ latest_version: 3, state: "stale", version: 2 }),
    verificationEntry({
      by: { id: "bob", kind: "user" },
      latest_version: 4,
      reason: "The checks name no command.",
      state: "changes_requested",
      version: 4,
    }),
  ];
  const details = renderWithQueries(
    <MemoryRouter>
      <NoTooltips>
        <ApprovalSteps
          entries={entries}
          primaryArtifactId={SPEC_ID}
          versions={{ asks: [request], issueKey: "CORE-1" }}
        />
      </NoTooltips>
    </MemoryRouter>
  );
  try {
    const scope = within(details.container);
    expect(scope.getByRole("link", { name: "spec.md, version 1" }).getAttribute("href")).toBe(
      "/issues/CORE-1/artifacts/spec-md?v=1"
    );
    expect(scope.getByRole("link", { name: "decisions.md, version 2" }).getAttribute("href")).toBe(
      "/issues/CORE-1/artifacts/decisions-md?v=2"
    );
    expect(
      scope.getByRole("link", { name: "verification.md, version 4" }).getAttribute("href")
    ).toBe("/issues/CORE-1/artifacts/verification-md?v=4");
    // Each default: the list's test id is `main`'s, the primary document carries both actions,
    // and the step sent back says why under the list.
    expect(scope.getByRole("list", { name: "Approvals" }).dataset.testid).toBe(
      "issue-approval-steps"
    );
    const [spec] = within(scope.getByRole("list", { name: "Approvals" })).getAllByRole("listitem");
    expect(spec.dataset.approvalStep).toBe("spec");
    expect(within(spec).getByRole("button", { name: "Approve" })).toBeInTheDocument();
    expect(within(spec).getByRole("button", { name: "Request changes" })).toBeInTheDocument();
    expect(
      details.container.querySelector('[data-approval-step-reason="criteria"]')?.textContent
    ).toContain("The checks name no command.");
  } finally {
    details.unmount();
  }

  const header = renderWithQueries(
    <MemoryRouter>
      <NoTooltips>
        <ApprovalSteps
          actions={false}
          entries={entries}
          primaryArtifactId={SPEC_ID}
          reasons={false}
          size="compact"
          testId="issue-header-approval-steps"
        />
      </NoTooltips>
    </MemoryRouter>
  );
  try {
    const scope = within(header.container);
    // The list is drawn, each entry's chip with it: what is missing is missing by its option.
    expect(scope.getByRole("list", { name: "Approvals" }).dataset.testid).toBe(
      "issue-header-approval-steps"
    );
    expect(scope.queryByTestId("issue-approval-steps")).toBeNull();
    expect(scope.getByRole("button", { name: "Awaiting approval" })).toBeInTheDocument();
    expect(
      scope.getByRole("button", { name: `Approved v2 · ${CONTROL_LABELS.approval_latest} v3` })
    ).toBeInTheDocument();
    expect(scope.getByRole("button", { name: "Changes requested" })).toBeInTheDocument();
    expect(scope.queryAllByRole("link")).toHaveLength(0);
    expect(scope.queryByRole("button", { name: /^Approve( v\d+)?$/ })).toBeNull();
    expect(scope.queryByRole("button", { name: "Request changes" })).toBeNull();
    expect(header.container.querySelector("[data-approval-step-reason]")).toBeNull();
  } finally {
    header.unmount();
  }
});

// R1: outside `NoTooltips` the options of the new issue page's surface draw exactly what `main`
// draws. Each pair draws a control without the option and with it; inside `NoTooltips` the same
// pair differs, so a pass outside is no pair that draws nothing either way.
test("outside NoTooltips inRow, author, versions and exact change nothing", async () => {
  const waiting: ClaimHolderLiveness = {
    holder_state: "unresponsive",
    last_seen: minutesFromNow(-10),
    takeable_at: minutesFromNow(20),
  };
  const throughAgent: Actor = { act: { session: LIVE_SESSION }, id: "alice", kind: "user" };
  const versionRequest = {
    approval: { artifact_id: SPEC_ID, name: "spec.md", requested_version: 1, version: 1 },
    id: "ask-1",
    kind: "approval",
    state: "open",
  } as unknown as Ask;
  const steps = () => [specEntry({ latest_version: 2, state: "awaiting", waiting_on: "human" })];
  const pairs: readonly (readonly [string, () => ReactNode, () => ReactNode])[] = [
    [
      "ClaimChip inRow",
      () => <ClaimChip claim={sessionClaim(LIVE_SESSION)} />,
      () => <ClaimChip claim={sessionClaim(LIVE_SESSION)} inRow />,
    ],
    [
      "ClaimChip author",
      () => <ClaimChip claim={sessionClaim(NAMELESS_SESSION)} />,
      () => <ClaimChip author claim={sessionClaim(NAMELESS_SESSION)} />,
    ],
    [
      "ClaimChip inRow and author, waiting to come free",
      () => <ClaimChip claim={sessionClaim(STALLED_SESSION)} liveness={waiting} />,
      () => <ClaimChip author claim={sessionClaim(STALLED_SESSION)} inRow liveness={waiting} />,
    ],
    [
      "RepoControl inRow, no repository",
      () => <RepoControl issueKey="CORE-1" project="CORE" repo={null} resolvedRepo={null} />,
      () => <RepoControl inRow issueKey="CORE-1" project="CORE" repo={null} resolvedRepo={null} />,
    ],
    [
      "RepoControl inRow, an inherited repository",
      () => <RepoControl issueKey="CORE-1" project="CORE" repo={null} resolvedRepo="acme/web" />,
      () => (
        <RepoControl inRow issueKey="CORE-1" project="CORE" repo={null} resolvedRepo="acme/web" />
      ),
    ],
    [
      "IssueComponentsLine inRow",
      () => (
        <IssueComponentsLine issue={componentsIssue(explicitComponents(["dispatch-server"]))} />
      ),
      () => (
        <IssueComponentsLine
          inRow
          issue={componentsIssue(explicitComponents(["dispatch-server"]))}
        />
      ),
    ],
    [
      "IssuePriorityLine inRow, advancing none",
      () => (
        <IssuePriorityLine
          disabled={false}
          issue={{ advances: [], advances_revision: 1, key: "CORE-1" }}
        />
      ),
      () => (
        <IssuePriorityLine
          disabled={false}
          inRow
          issue={{ advances: [], advances_revision: 1, key: "CORE-1" }}
        />
      ),
    ],
    [
      "IssuePriorityLine inRow, advancing one",
      () => (
        <IssuePriorityLine
          disabled={false}
          issue={{
            advances: [{ id: "rocq-lean", inherited: false }],
            advances_revision: 1,
            key: "CORE-1",
          }}
        />
      ),
      () => (
        <IssuePriorityLine
          disabled={false}
          inRow
          issue={{
            advances: [{ id: "rocq-lean", inherited: false }],
            advances_revision: 1,
            key: "CORE-1",
          }}
        />
      ),
    ],
    [
      "AgentName author, a session's actor",
      () => <AgentName actor={{ id: NAMELESS_SESSION, kind: "session" }} />,
      () => <AgentName actor={{ id: NAMELESS_SESSION, kind: "session" }} author />,
    ],
    [
      "AgentName author, a session's id",
      () => <AgentName sessionId={NAMELESS_SESSION} storedTitle={undefined} />,
      () => <AgentName author sessionId={NAMELESS_SESSION} storedTitle={undefined} />,
    ],
    [
      "AgentName author, an act made through an agent",
      () => <AgentName actor={throughAgent} />,
      () => <AgentName actor={throughAgent} author />,
    ],
    [
      "ApprovalSteps versions",
      () => <ApprovalSteps entries={steps()} primaryArtifactId={SPEC_ID} />,
      () => (
        <ApprovalSteps
          entries={steps()}
          primaryArtifactId={SPEC_ID}
          versions={{ asks: [versionRequest], issueKey: "CORE-1" }}
        />
      ),
    ],
    [
      "Timestamp exact",
      () => <Timestamp at={CLAIMED_AT} />,
      () => <Timestamp at={CLAIMED_AT} exact />,
    ],
  ];

  const changedOutside: { name: string; plain: string; given: string }[] = [];
  const sameInside: string[] = [];
  for (const [name, plain, given] of pairs) {
    const plainOutside = await markupOf(plain(), "outside");
    const givenOutside = await markupOf(given(), "outside");
    if (givenOutside !== plainOutside) {
      changedOutside.push({ given: givenOutside, name, plain: plainOutside });
    }
    if ((await markupOf(given(), "inside")) === (await markupOf(plain(), "inside"))) {
      sameInside.push(name);
    }
  }
  expect(changedOutside).toEqual([]);
  expect(sameInside).toEqual([]);
});

// R3: the header variant of the assignee and the priority is the prototype's `.person` and
// `.prio`, drawn over the same native select as the badge, at the header line's 24 px.
test("the header variant draws the person and the priority bars over the same select", async () => {
  const expectHeaderWrapper = (wrapper: HTMLElement) => {
    expect(classOf(wrapper)).toContain("min-h-6");
    expect(classOf(wrapper).filter((token) => token.startsWith("md:min-h-"))).toEqual([]);
  };

  for (const level of [0, 1, 2, 3, null] as const) {
    const header = draw(
      <PriorityEditor
        issueKey="CORE-1"
        priority={level}
        variant="header"
        write={idlePriorityWrite}
      />,
      "inside"
    );
    const badge = draw(
      <PriorityEditor issueKey="CORE-1" priority={level} write={idlePriorityWrite} />,
      "inside"
    );
    try {
      const select = within(header.container).getByRole("combobox", { name: "Priority of CORE-1" });
      const wrapper = parentOf(select);
      expect(wrapper.querySelectorAll("rect")).toHaveLength(3);
      expect(wrapper.querySelectorAll("rect[class~='fill-page-ink']")).toHaveLength(
        level === null ? 0 : 3 - level
      );
      expect(wrapper.querySelector("[aria-hidden='true']")?.textContent).toBe(
        level === null ? PROPERTY_LABELS.priority : `P${level}`
      );
      const badgeSelect = within(badge.container).getByRole("combobox", {
        name: "Priority of CORE-1",
      });
      expect(optionsOf(select)).toEqual(optionsOf(badgeSelect));
      expect((select as HTMLSelectElement).value).toBe((badgeSelect as HTMLSelectElement).value);
      expectHeaderWrapper(wrapper);
    } finally {
      undraw(header);
      undraw(badge);
    }
  }

  const user = userEvent.setup();
  for (const assignee of ["alice@example.com", null]) {
    const header = draw(
      <AssigneeControl assignee={assignee} issueKey="CORE-1" variant="header" />,
      "inside"
    );
    const badge = draw(<AssigneeControl assignee={assignee} issueKey="CORE-1" />, "inside");
    try {
      const select = within(header.container).getByRole("combobox", { name: "Assignee of CORE-1" });
      const badgeSelect = within(badge.container).getByRole("combobox", {
        name: "Assignee of CORE-1",
      });
      // A press reads who has signed in, and both selects then offer each of them.
      await user.click(select);
      await user.click(badgeSelect);
      await waitFor(() => expect(optionsOf(select)).toHaveLength(3));
      await waitFor(() => expect(optionsOf(badgeSelect)).toHaveLength(3));
      expect(optionsOf(select)).toEqual(optionsOf(badgeSelect));
      const wrapper = parentOf(select);
      const shown = wrapper.querySelector("[aria-hidden='true']");
      if (assignee === null) {
        expect(shown?.textContent).toBe(CONTROL_LABELS.unassigned);
        expect(wrapper.querySelector("[data-initial]")).toBeNull();
      } else {
        expect(shown?.textContent).toBe(assignee);
        expect(wrapper.querySelector("[data-initial]")?.getAttribute("data-initial")).toBe("A");
      }
      expectHeaderWrapper(wrapper);
    } finally {
      undraw(header);
      undraw(badge);
    }
  }
});

// R3: at the `compact` size the chip is the prototype's `.pill` and Request changes its `.hbtn`
// (`pageHeaderControl`); Approve has that size, filled with the page's ink and never the
// application's blue. `size` acts on every surface.
test("the compact size draws the chip and both actions at the header's size, with Approve in the ink", () => {
  for (const surface of ["inside", "outside"] as const) {
    const view = draw(
      <ApprovalSteps
        entries={[specEntry({ latest_version: 1, state: "awaiting", waiting_on: "human" })]}
        primaryArtifactId={SPEC_ID}
        reasons={false}
        size="compact"
      />,
      surface
    );
    try {
      const scope = within(view.container);
      const approve = classOf(scope.getByRole("button", { name: "Approve" }));
      expect(approve).toContain("bg-page-ink");
      expect(approve).not.toContain("bg-sky-700");
      expect(scope.getByRole("button", { name: "Request changes" }).getAttribute("class")).toBe(
        pageHeaderControl
      );
      const chip = classOf(scope.getByRole("button", { name: "Awaiting approval" }));
      expect(chip).toContain("min-h-6");
      expect(chip).toContain("leading-[18px]");
    } finally {
      undraw(view);
    }
  }
});

// R4: inside `NoTooltips` a control drawn as the value of a row of Details draws no label of its
// own, since the row draws it, and says Details' words.
test("inRow draws no label and the words of Details", async () => {
  const waiting: ClaimHolderLiveness = {
    holder_state: "unresponsive",
    last_seen: minutesFromNow(-10),
    takeable_at: minutesFromNow(20),
  };
  const free: ClaimHolderLiveness = {
    holder_state: "unresponsive",
    last_seen: minutesFromNow(-90),
    takeable_at: minutesFromNow(-30),
  };
  const view = draw(
    <>
      <div data-case="held">
        <ClaimChip claim={sessionClaim(LIVE_SESSION)} inRow />
      </div>
      <div data-case="waiting">
        <ClaimChip claim={sessionClaim(STALLED_SESSION)} inRow liveness={waiting} />
      </div>
      <div data-case="free">
        <ClaimChip claim={sessionClaim(STALLED_SESSION)} inRow liveness={free} />
      </div>
      <div data-case="no repository">
        <RepoControl inRow issueKey="CORE-1" project="CORE" repo={null} resolvedRepo={null} />
      </div>
      <div data-case="repository">
        <RepoControl
          inRow
          issueKey="CORE-2"
          project="CORE"
          repo="acme/api"
          resolvedRepo="acme/api"
        />
      </div>
      <div data-case="components">
        <IssueComponentsLine
          inRow
          issue={componentsIssue(explicitComponents(["dispatch-server"]), "CORE-3")}
        />
      </div>
      <div data-case="no priority">
        <IssuePriorityLine
          disabled={false}
          inRow
          issue={{ advances: [], advances_revision: 1, key: "CORE-4" }}
        />
      </div>
      <div data-case="priority">
        <IssuePriorityLine
          disabled={false}
          inRow
          issue={{
            advances: [{ id: "rocq-lean", inherited: false }],
            advances_revision: 1,
            key: "CORE-5",
          }}
        />
      </div>
    </>,
    "inside"
  );
  try {
    await settleReads();
    const scope = within(view.container);
    await scope.findByText("Dispatch server");
    await scope.findByRole("link", { name: /Rocq and Lean/ });

    const text = view.container.textContent ?? "";
    for (const label of [
      PROPERTY_LABELS.claim,
      `${PROPERTY_LABELS.repository}:`,
      `${PROPERTY_LABELS.components}:`,
      PROPERTY_LABELS.advances,
    ]) {
      expect(text).not.toContain(label);
    }

    // The claim is plain words in the ink, and no part of it is held whole.
    for (const name of ["held", "waiting", "free"]) {
      const chip = within(caseOf(view, name)).getByTestId("issue-claim");
      expect(classOf(chip)).toEqual(expect.arrayContaining(tokens(pageInkText)));
      expect(classOf(chip)).not.toContain("rounded-full");
      expect(classOf(chip)).not.toContain("whitespace-nowrap");
      const words = [...chip.querySelectorAll("span, time")].filter(
        (element) => element.closest("[data-agent-name]") === null
      );
      expect(words.length).toBeGreaterThan(0);
      expect(words.filter((element) => classOf(element).includes("shrink-0"))).toEqual([]);
    }
    const takeableAt = waiting.takeable_at ?? "";
    const comesFree = within(caseOf(view, "waiting")).getByTestId("issue-claim-takeable");
    expect(comesFree.textContent?.startsWith(`, ${CONTROL_LABELS.takeable} `)).toBe(true);
    const instant = comesFree.querySelector("time");
    expect(instant?.getAttribute("dateTime")).toBe(takeableAt);
    expect(instant?.textContent).toContain(
      new Date(takeableAt).toLocaleDateString(undefined, { dateStyle: "short" })
    );
    expect(instant?.textContent).toContain(
      new Date(takeableAt).toLocaleTimeString(undefined, { timeStyle: "short" })
    );
    expect(within(caseOf(view, "free")).getByTestId("issue-claim-takeable").textContent).toBe(
      `, ${CONTROL_LABELS.takeable_now}`
    );

    expect(caseOf(view, "no repository").textContent).toBe(CONTROL_LABELS.none);
    expect(
      within(caseOf(view, "repository")).getByRole("combobox", { name: "Repository of CORE-2" })
    ).toBeInTheDocument();

    const components = within(caseOf(view, "components")).getByTestId("issue-components");
    expect(within(components).getByText("Dispatch server")).toBeInTheDocument();
    expect(within(components).queryByText("dispatch-server")).toBeNull();

    expect(
      within(caseOf(view, "no priority")).getByRole("button", { name: "Add a priority to CORE-4" })
        .textContent
    ).toBe(CONTROL_LABELS.add_priority);
    expect(view.container.querySelector('[data-testid="issue-advances"]')).toBeNull();
  } finally {
    undraw(view);
  }
});

// R4: `chipClassName` is the chip's whole shape - only what holds a chip on its line is kept -
// and the labels wrap. It acts on every surface; without it the labels keep `main`'s strings.
test("chipClassName replaces the chip's class and lets labels wrap", async () => {
  const given = "rounded-[5px] border px-1.5 leading-5";
  const kept = ["inline-flex", "shrink-0", "items-center", "whitespace-nowrap"];
  const mainChip =
    "inline-flex min-h-11 shrink-0 items-center rounded-full px-3 py-1 text-xs font-medium whitespace-nowrap sm:min-h-7 sm:px-2 bg-slate-200 dark:bg-slate-700 text-slate-700 dark:text-slate-300";
  const mainRail = "relative flex shrink-0 flex-nowrap items-center gap-1";
  const othersOfMain = [...tokens(mainChip), ...tokens(pageLabelChip)].filter(
    (token) => !kept.includes(token)
  );
  const labels = (chipClassName: string | undefined) => (
    <IssueLabels
      chipClassName={chipClassName}
      disabled={false}
      labels={["api", "frontend"]}
      onSave={async () => undefined}
      project="CORE"
      saving={false}
      variant="rail"
    />
  );

  for (const surface of ["outside", "inside"] as const) {
    const view = draw(labels(given), surface);
    try {
      const rail = within(view.container).getByTestId("issue-labels");
      expect(classOf(rail)).toContain("flex-wrap");
      expect(classOf(rail)).not.toContain("flex-nowrap");
      for (const label of ["api", "frontend"]) {
        const chip = within(rail).getByText(label);
        expect(chip.getAttribute("class")).toBe(
          `inline-flex shrink-0 items-center whitespace-nowrap ${given}`
        );
        expect(classOf(chip).filter((token) => othersOfMain.includes(token))).toEqual([]);
      }
    } finally {
      undraw(view);
    }
  }

  const plain = draw(labels(undefined), "outside");
  try {
    const rail = within(plain.container).getByTestId("issue-labels");
    expect(rail.getAttribute("class")).toBe(mainRail);
    for (const label of ["api", "frontend"]) {
      expect(within(rail).getByText(label).getAttribute("class")).toBe(mainChip);
    }
  } finally {
    undraw(plain);
  }
});

/** Each actor `useAgentText().author` names, by what the row holds, and what `label` names it. */
function AuthorProbe({
  actors,
}: {
  actors: readonly (readonly [string, Actor | undefined])[];
}): ReactNode {
  const text = useAgentText();
  return (
    <ul>
      {actors.map(([name, actor]) => (
        <li data-case={`author: ${name}`} key={name}>
          <span data-author="">{text.author(actor)}</span>
          <span data-label="">{actor === undefined ? "" : text.label(actor)}</span>
        </li>
      ))}
    </ul>
  );
}

// R5: inside `NoTooltips`, an author is named by a title - the agent list's, else the one its
// write was stamped with - else the page's word for an agent, and never by the short id that
// tells no two sessions apart; a person's act made through an agent names the agent by what it
// is. Outside, `author` names an actor as `label` does.
test("author names a session by its title and never by its short id", async () => {
  // The page's words for an agent nothing names, for no actor, and for an act made through an
  // agent, as the Verification document reads them.
  expect([
    CONTROL_LABELS.unnamed_author,
    CONTROL_LABELS.unknown_author,
    `, acting through ${AUTHOR_KIND_WORDS.agent}`,
  ]).toEqual(["An agent", "Author unknown", ", acting through an agent"]);
  const throughAgent: Actor = { act: { session: LIVE_SESSION }, id: "alice", kind: "user" };
  const actors: readonly (readonly [string, Actor | undefined])[] = [
    ["listed", { id: LIVE_SESSION, kind: "session" }],
    ["stamped", { id: GONE_SESSION, kind: "session", origin: { session_title: "Old planner" } }],
    ["nameless", { id: NAMELESS_SESSION, kind: "session" }],
    ["through an agent", throughAgent],
    ["no actor", undefined],
  ];
  const node = (
    <>
      <div data-case="actor, listed">
        <AgentName actor={{ id: LIVE_SESSION, kind: "session" }} author />
      </div>
      <div data-case="id, listed">
        <AgentName author sessionId={LIVE_SESSION} storedTitle={undefined} />
      </div>
      <div data-case="actor, stamped">
        <AgentName
          actor={{ id: GONE_SESSION, kind: "session", origin: { session_title: "Old planner" } }}
          author
        />
      </div>
      <div data-case="id, stamped">
        <AgentName author sessionId={GONE_SESSION} storedTitle="Old planner" />
      </div>
      <div data-case="actor, nameless">
        <AgentName actor={{ id: NAMELESS_SESSION, kind: "session" }} author />
      </div>
      <div data-case="id, nameless">
        <AgentName author sessionId={NAMELESS_SESSION} storedTitle={undefined} />
      </div>
      <div data-case="through an agent">
        <AgentName actor={throughAgent} author />
      </div>
      <div data-case="nameless, not an author">
        <AgentName actor={{ id: NAMELESS_SESSION, kind: "session" }} />
      </div>
      <AuthorProbe actors={actors} />
    </>
  );

  const inside = draw(node, "inside");
  try {
    await waitFor(() =>
      expect(inside.container.querySelector(`[data-agent-state="running"]`)).not.toBeNull()
    );
    await settleReads();
    const named = (name: string) => caseOf(inside, name).textContent;
    const authored = (name: string) =>
      caseOf(inside, `author: ${name}`).querySelector("[data-author]")?.textContent;
    expect(named("actor, listed")).toBe("Implementer");
    expect(named("id, listed")).toBe("Implementer");
    expect(named("actor, stamped")).toBe("Old planner");
    expect(named("id, stamped")).toBe("Old planner");
    expect(named("actor, nameless")).toBe(CONTROL_LABELS.unnamed_author);
    expect(named("id, nameless")).toBe(CONTROL_LABELS.unnamed_author);
    expect(named("through an agent")).toBe(`alice, acting through ${AUTHOR_KIND_WORDS.agent}`);
    // Without `author` the same session keeps `main`'s name, its short id.
    expect(named("nameless, not an author")).toBe("session:01234567…");
    for (const name of [
      "actor, listed",
      "id, listed",
      "actor, stamped",
      "id, stamped",
      "actor, nameless",
      "id, nameless",
      "through an agent",
    ]) {
      expect(named(name)).not.toContain("session:");
    }

    expect(authored("listed")).toBe("Implementer");
    expect(authored("stamped")).toBe("Old planner");
    expect(authored("nameless")).toBe(CONTROL_LABELS.unnamed_author);
    expect(authored("through an agent")).toBe(`alice, acting through ${AUTHOR_KIND_WORDS.agent}`);
    expect(authored("no actor")).toBe(CONTROL_LABELS.unknown_author);
  } finally {
    undraw(inside);
  }

  const outside = draw(node, "outside");
  try {
    await waitFor(() =>
      expect(outside.container.querySelector(`[data-agent-state="running"]`)).not.toBeNull()
    );
    await settleReads();
    for (const [name, actor] of actors) {
      const row = caseOf(outside, `author: ${name}`);
      const author = row.querySelector("[data-author]")?.textContent;
      if (actor === undefined) {
        expect(author).toBe("");
      } else {
        expect(author).toBe(row.querySelector("[data-label]")?.textContent ?? "");
        expect(author).not.toBe("");
      }
    }
  } finally {
    undraw(outside);
  }
});

// R4 and R1: the components line reads the architecture to name a component by its title only
// inside `NoTooltips`, in a row of Details; everywhere else an explicit set names its ids, as on
// `main`, and reads it only once the picker opens.
test("outside NoTooltips an explicit component set makes no architecture read", async () => {
  const reads = async (inRow: boolean, surface: Surface) => {
    stubs.getArchitecture.mockClear();
    const view = draw(
      <IssueComponentsLine
        inRow={inRow}
        issue={componentsIssue(explicitComponents(["dispatch-server"]))}
      />,
      surface
    );
    try {
      await settleReads();
      // The line is drawn, so a count of no reads is the line's and not an empty mount's.
      expect(within(view.container).getByTestId("issue-components")).toBeInTheDocument();
      return stubs.getArchitecture.mock.calls.length;
    } finally {
      undraw(view);
    }
  };

  expect(await reads(false, "outside")).toBe(0);
  expect(await reads(true, "outside")).toBe(0);
  expect(await reads(false, "inside")).toBe(0);
  expect(await reads(true, "inside")).toBe(1);
});

// R1: at its defaults each shared control draws `main`'s classes. Each string below is `main`'s
// class, its theme's constants written out from `main`'s `theme/classes.ts`, for every class
// the part builds from a template of its own.
test("the default shapes keep main's class strings", async () => {
  const want: Record<string, string> = {
    "approvalPillClassName approved":
      "inline-flex min-w-0 items-center rounded-full border px-2 py-1 text-center text-xs font-medium [overflow-wrap:anywhere] border-emerald-700 dark:border-emerald-400 text-emerald-700 dark:text-emerald-400",
    "approvalPillClassName awaiting":
      "inline-flex min-w-0 items-center rounded-full border px-2 py-1 text-center text-xs font-medium [overflow-wrap:anywhere] border-amber-700 dark:border-amber-400 text-amber-800 dark:text-amber-300",
    "approvalPillClassName changes_requested":
      "inline-flex min-w-0 items-center rounded-full border px-2 py-1 text-center text-xs font-medium [overflow-wrap:anywhere] border-rose-700 dark:border-rose-400 text-rose-700 dark:text-rose-400",
    "approvalPillClassName draft":
      "inline-flex min-w-0 items-center rounded-full border px-2 py-1 text-center text-xs font-medium [overflow-wrap:anywhere] border-dashed border-slate-300 dark:border-slate-700 text-slate-700 dark:text-slate-300",
    "approvalPillClassName stale":
      "inline-flex min-w-0 items-center rounded-full border px-2 py-1 text-center text-xs font-medium [overflow-wrap:anywhere] border-amber-700 dark:border-amber-400 text-amber-800 dark:text-amber-300",
    badgeSelectWrapper:
      "relative inline-flex min-h-11 shrink-0 items-center rounded-sm md:min-h-8 has-focus-visible:ring-2 has-focus-visible:ring-sky-600 dark:has-focus-visible:ring-sky-400",

    "ApprovalSteps list's host": "flex w-full min-w-0 flex-col gap-2",
    "ApprovalSteps list": "flex w-full min-w-0 flex-wrap items-center gap-x-4 gap-y-2",
    "ApprovalSteps step name": "text-xs font-medium text-slate-500 dark:text-slate-400",
    "ApprovalSteps chip":
      "inline-flex min-w-0 items-center rounded-full border px-2 py-1 text-center text-xs font-medium [overflow-wrap:anywhere] border-amber-700 dark:border-amber-400 text-amber-800 dark:text-amber-300",
    "ApprovalSteps Not requested":
      "inline-flex min-w-0 items-center rounded-full border px-2 py-1 text-center text-xs font-medium [overflow-wrap:anywhere] border-dashed border-slate-300 dark:border-slate-700 text-slate-700 dark:text-slate-300",
    "ApprovalSteps Approve":
      "min-h-11 rounded-lg px-3 py-2 text-sm font-medium md:min-h-8 md:py-1 bg-sky-700 text-white not-refused:hover:bg-sky-800 refused:cursor-not-allowed refused:bg-slate-300 refused:text-slate-700 dark:refused:bg-slate-700 dark:refused:text-slate-300",
    "ApprovalSteps Request changes":
      "min-h-11 rounded-lg border px-3 py-2 text-sm font-medium md:min-h-8 md:py-1 border-slate-300 dark:border-slate-700 text-slate-700 dark:text-slate-300 not-refused:hover:border-sky-500 dark:not-refused:hover:border-sky-400",
    "ApprovalSteps reason":
      "min-w-0 text-xs [overflow-wrap:anywhere] text-slate-700 dark:text-slate-300",

    "AssigneeControl wrapper":
      "relative inline-flex min-h-11 shrink-0 items-center rounded-sm md:min-h-8 has-focus-visible:ring-2 has-focus-visible:ring-sky-600 dark:has-focus-visible:ring-sky-400 ",
    "AssigneeControl badge, assigned":
      "inline-flex items-center rounded-sm px-1.5 py-0.5 text-xs font-semibold bg-sky-100 dark:bg-sky-950 text-sky-800 dark:text-sky-300",
    "AssigneeControl badge, unassigned":
      "inline-flex items-center rounded-sm px-1.5 py-0.5 text-xs font-semibold bg-slate-100 dark:bg-slate-800 text-slate-700 dark:text-slate-300",
    "PriorityControl wrapper":
      "relative inline-flex min-h-11 shrink-0 items-center rounded-sm md:min-h-8 has-focus-visible:ring-2 has-focus-visible:ring-sky-600 dark:has-focus-visible:ring-sky-400 ",
    "PriorityControl badge, P0":
      "inline-flex items-center rounded-sm px-1.5 py-0.5 text-xs font-semibold bg-rose-100 dark:bg-rose-950 text-rose-800 dark:text-rose-300",
    "PriorityControl badge, P1":
      "inline-flex items-center rounded-sm px-1.5 py-0.5 text-xs font-semibold bg-amber-100 dark:bg-amber-950 text-amber-800 dark:text-amber-300",
    "PriorityControl badge, none":
      "inline-flex items-center rounded-sm px-1.5 py-0.5 text-xs font-semibold bg-slate-100 dark:bg-slate-800 text-slate-700 dark:text-slate-300",

    "ClaimChip, held":
      "inline-flex shrink-0 items-center rounded-full px-2 py-1 text-xs font-medium whitespace-nowrap bg-slate-200 dark:bg-slate-700 text-slate-700 dark:text-slate-300 min-w-0 max-w-full gap-1",
    "ClaimChip holder": "min-w-0 [overflow-wrap:anywhere] min-w-0 truncate",
    "ClaimChip dot": "shrink-0",
    "ClaimChip age": "shrink-0",
    "ClaimChip, lapsed":
      "inline-flex shrink-0 items-center rounded-full px-2 py-1 text-xs font-medium whitespace-nowrap bg-slate-100 dark:bg-slate-800 text-slate-600 dark:text-slate-400 min-w-0 max-w-full gap-1",
    "ClaimChip not running": "shrink-0",

    "RepoControl line":
      "flex shrink-0 items-center gap-2 text-sm text-slate-700 dark:text-slate-300",
    "RepoControl wrapper":
      "relative inline-flex min-h-11 shrink-0 items-center rounded-sm md:min-h-7 has-focus-visible:ring-2 has-focus-visible:ring-sky-600 dark:has-focus-visible:ring-sky-400 ",
    "RepoControl badge, its own":
      "inline-flex items-center rounded-sm px-1.5 py-0.5 text-xs font-semibold font-mono bg-sky-100 dark:bg-sky-950 text-sky-800 dark:text-sky-300",
    "RepoControl badge, inherited":
      "inline-flex items-center rounded-sm px-1.5 py-0.5 text-xs font-semibold font-mono bg-slate-100 dark:bg-slate-800 text-slate-700 dark:text-slate-300",

    "IssueComponentsLine line":
      "flex shrink-0 items-center gap-2 text-sm text-slate-700 dark:text-slate-300",
    "IssueComponentsLine chip":
      "inline-flex shrink-0 items-center rounded-full px-2 py-0.5 text-xs font-medium bg-sky-100 dark:bg-sky-950 text-sky-800 dark:text-sky-300",
    "IssueComponentsLine retired chip":
      "inline-flex shrink-0 items-center rounded-full px-2 py-0.5 text-xs font-medium bg-amber-100 dark:bg-amber-950 text-amber-800 dark:text-amber-300",
    "IssueComponentsLine inherited from": "shrink-0 text-xs text-slate-500 dark:text-slate-400",
    "IssueComponentsLine inherited link":
      "dispatch-inline-link underline text-sky-700 dark:text-sky-400 hover:text-sky-900 dark:hover:text-sky-300",
    "IssueComponentsLine not attached": "text-slate-500 dark:text-slate-400",

    "IssueLabels line": "relative mt-2 flex flex-wrap items-center gap-2",
    "IssueLabels chip":
      "inline-flex min-h-11 shrink-0 items-center rounded-full px-3 py-1 text-xs font-medium whitespace-nowrap sm:min-h-7 sm:px-2 bg-slate-200 dark:bg-slate-700 text-slate-700 dark:text-slate-300",
    "IssueLabels trigger":
      "min-h-11 shrink-0 rounded-lg border px-3 py-1 text-xs font-medium whitespace-nowrap sm:min-h-7 sm:px-2 border-slate-300 dark:border-slate-700 text-slate-700 dark:text-slate-300 not-refused:hover:border-sky-500 dark:not-refused:hover:border-sky-400 refused:text-slate-400 dark:refused:text-slate-600",

    "IssuePriorityLine line":
      "flex shrink-0 items-center gap-2 text-sm text-slate-700 dark:text-slate-300",
    "IssuePriorityLine entry": "flex shrink-0 items-center gap-1",
    "IssuePriorityLine link":
      "dispatch-inline-link shrink-0 whitespace-nowrap underline text-sky-700 dark:text-sky-400 hover:text-sky-900 dark:hover:text-sky-300",
    "IssuePriorityLine inherited": "shrink-0 text-xs text-slate-500 dark:text-slate-400",
    "IssuePriorityLine remove":
      "shrink-0 text-slate-700 dark:text-slate-300 hover:text-slate-900 dark:hover:text-slate-100",
    "IssuePriorityLine add":
      "inline-flex min-h-11 shrink-0 items-center rounded-full px-2 py-2 text-sm whitespace-nowrap outline-none bg-slate-200 dark:bg-slate-700 text-slate-700 dark:text-slate-300 text-slate-700 dark:text-slate-300 hover:text-slate-900 dark:hover:text-slate-100",
    "IssuePriorityLine form": "flex shrink-0 items-center gap-2",
    "IssuePriorityLine picker":
      "min-h-11 max-w-56 min-w-0 shrink-0 truncate rounded-lg px-2 py-1 text-sm md:min-h-8 border-slate-300 bg-white focus:border-sky-600 dark:border-slate-700 dark:bg-slate-950 dark:focus:border-sky-400",
    "IssuePriorityLine Add":
      "min-h-11 rounded px-2 py-1 text-sm font-medium disabled:cursor-not-allowed md:min-h-8 border-slate-300 dark:border-slate-700 text-slate-700 dark:text-slate-300 refused:text-slate-400 dark:refused:text-slate-600",
    "IssuePriorityLine Cancel":
      "min-h-11 rounded px-2 py-1 text-sm font-medium md:min-h-8 text-slate-500 dark:text-slate-400 hover:text-slate-700 dark:hover:text-slate-300",

    "AgentName link":
      "rounded outline-none focus-visible:ring-2 focus-visible:ring-sky-600 dark:focus-visible:ring-sky-400 text-sky-700 dark:text-sky-400 hover:text-sky-900 dark:hover:text-sky-300",
    "AgentName inline link":
      "rounded outline-none focus-visible:ring-2 focus-visible:ring-sky-600 dark:focus-visible:ring-sky-400 text-sky-700 dark:text-sky-400 hover:text-sky-900 dark:hover:text-sky-300 dispatch-inline-link underline",
    "AgentName live link":
      "ml-1 px-1 rounded outline-none focus-visible:ring-2 focus-visible:ring-sky-600 dark:focus-visible:ring-sky-400 text-sky-700 dark:text-sky-400 hover:text-sky-900 dark:hover:text-sky-300",
    "AgentName mark, running":
      "mr-1 inline-block h-2 w-2 shrink-0 rounded-full align-middle bg-emerald-500 dark:bg-emerald-400",
    "AgentName mark, unresponsive":
      "mr-1 inline-block h-2 w-2 shrink-0 rounded-full align-middle border-2 border-solid border-amber-500 dark:border-amber-400",
    "AgentName mark, not running":
      "mr-1 inline-block h-2 w-2 shrink-0 rounded-full align-middle border-2 border-solid border-slate-400 dark:border-slate-600",
    "AgentName mark, unknown":
      "mr-1 inline-block h-2 w-2 shrink-0 rounded-full align-middle border border-dashed border-slate-400 dark:border-slate-500",
    AgentUnresponsiveNote: "shrink-0 text-xs text-amber-800 dark:text-amber-300",

    RouteReachUnknownMarker: "text-xs text-slate-500 dark:text-slate-400",
    UnresponsiveRouteMarker:
      "inline-flex min-w-0 max-w-full items-center gap-1 text-xs text-slate-500 dark:text-slate-400",
    UnreachableRouteMarker:
      "inline-flex min-w-0 max-w-full items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-semibold border-amber-200 dark:border-amber-900 bg-amber-50 dark:bg-amber-950 text-amber-950 dark:text-amber-300",

    "GitHubLink link":
      "inline-flex max-w-full items-center gap-2 text-sm underline text-sky-700 dark:text-sky-400 hover:text-sky-900 dark:hover:text-sky-300",
    "GitHubLink state":
      "shrink-0 rounded-full px-2 py-0.5 text-xs bg-slate-100 dark:bg-slate-800 text-slate-700 dark:text-slate-300",

    "SubscribedAgents heading": "text-sm font-semibold text-slate-700 dark:text-slate-300",

    Pill: "inline-flex shrink-0 items-center rounded-full px-2 py-1 text-xs font-medium whitespace-nowrap bg-slate-100 dark:bg-slate-800 text-slate-600 dark:text-slate-400 ",
    "LabelPill, selected":
      "inline-flex shrink-0 items-center rounded-full px-2 py-1 text-xs font-medium whitespace-nowrap bg-slate-200 dark:bg-slate-700 text-slate-700 dark:text-slate-300 ",
    StatusPill:
      "inline-flex shrink-0 items-center rounded-full px-2 py-1 text-xs font-medium whitespace-nowrap bg-slate-100 dark:bg-slate-800 text-slate-800 dark:text-slate-200 gap-1.5 font-semibold tracking-wide uppercase",
    "StatusPill dot": "size-1.5 rounded-full bg-slate-500 dark:bg-slate-400",

    "CopyButton, icon":
      "inline-flex min-h-11 shrink-0 items-center rounded-lg md:min-h-8 min-w-11 justify-center md:min-w-8 text-sky-700 dark:text-sky-400 hover:text-sky-900 dark:hover:text-sky-300 ",
    "CopyButton, labelled":
      "inline-flex min-h-11 shrink-0 items-center rounded-lg md:min-h-8 max-w-full gap-1 px-1 font-mono text-xs font-medium select-text text-sky-700 dark:text-sky-400 hover:text-sky-900 dark:hover:text-sky-300 ",
    "CopyButton copied": "ml-1 text-xs font-medium text-emerald-700 dark:text-emerald-400",
    "CopyButton failed": "ml-1 text-xs font-medium text-rose-700 dark:text-rose-400",

    "PinButton, pinned":
      "flex min-h-11 min-w-11 shrink-0 items-center justify-center rounded-lg disabled:cursor-not-allowed disabled:opacity-50 md:min-h-8 md:min-w-8 bg-sky-700 text-white not-refused:hover:bg-sky-800 ",
    "PinButton, unpinned":
      "flex min-h-11 min-w-11 shrink-0 items-center justify-center rounded-lg disabled:cursor-not-allowed disabled:opacity-50 md:min-h-8 md:min-w-8 border-slate-300 dark:border-slate-700 text-slate-700 dark:text-slate-300 not-refused:hover:border-sky-500 dark:not-refused:hover:border-sky-400 ",
    "PinButton, quiet":
      "flex min-h-11 min-w-11 shrink-0 items-center justify-center rounded-lg disabled:cursor-not-allowed disabled:opacity-50  text-slate-500 dark:text-slate-400 hover:text-slate-700 dark:hover:text-slate-300 ",

    "RefusableButton primary":
      "rounded-lg px-3 py-2 text-sm font-semibold bg-sky-700 text-white not-refused:hover:bg-sky-800 refused:cursor-not-allowed refused:bg-slate-300 refused:text-slate-700 dark:refused:bg-slate-700 dark:refused:text-slate-300 ",
    "RefusableButton secondary-compact":
      "min-h-11 shrink-0 rounded-lg border px-3 py-1 text-xs font-medium whitespace-nowrap sm:min-h-7 sm:px-2 border-slate-300 dark:border-slate-700 text-slate-700 dark:text-slate-300 not-refused:hover:border-sky-500 dark:not-refused:hover:border-sky-400 refused:text-slate-400 dark:refused:text-slate-600 ",

    "ConnectionDot connected": "h-2.5 w-2.5 rounded-full bg-emerald-500 dark:bg-emerald-400",
    "ConnectionDot connecting": "h-2.5 w-2.5 rounded-full bg-amber-500 dark:bg-amber-400",
    "ConnectionDot failed": "h-2.5 w-2.5 rounded-full bg-rose-500 dark:bg-rose-400",
    "ConnectionDot offline": "h-2.5 w-2.5 rounded-full bg-slate-400 dark:bg-slate-500",
    "ConnectionDot notice": "text-xs text-slate-500 dark:text-slate-400",

    "MultiSelect trigger":
      "min-h-11 rounded-lg border px-3 py-2 text-sm font-semibold border-slate-200 dark:border-slate-800 bg-slate-100 dark:bg-slate-800 text-slate-700 dark:text-slate-300",
    "MultiSelect panel":
      "z-50 max-h-72 w-64 max-w-[calc(100vw-1rem)] overflow-auto rounded-lg border shadow-lg border-slate-200 dark:border-slate-800 bg-white dark:bg-slate-900 border-slate-200 dark:border-slate-800",
    "MultiSelect search's host":
      "sticky top-0 border-b p-2 border-slate-200 dark:border-slate-800 bg-white dark:bg-slate-900",
    "MultiSelect search":
      "block min-h-11 w-full rounded-lg px-3 py-2 text-sm outline-none border-slate-300 bg-white focus:border-sky-600 dark:border-slate-700 dark:bg-slate-950 dark:focus:border-sky-400",
    "MultiSelect row, active":
      "flex min-h-11 w-full items-center gap-2 border-b px-3 py-2 text-left text-sm last:border-b-0 border-slate-200 dark:border-slate-800 text-slate-700 dark:text-slate-300 hover:border-sky-400 dark:hover:border-sky-300 bg-sky-50 dark:bg-sky-950 border-sky-500 dark:border-sky-400",
    "MultiSelect row":
      "flex min-h-11 w-full items-center gap-2 border-b px-3 py-2 text-left text-sm last:border-b-0 border-slate-200 dark:border-slate-800 text-slate-700 dark:text-slate-300 hover:border-sky-400 dark:hover:border-sky-300 ",
    "MultiSelect checkbox":
      "grid size-4 shrink-0 place-items-center rounded border text-xs leading-none border-slate-300 dark:border-slate-700",
    "MultiSelect option label": "truncate min-w-0 flex-1",
    "MultiSelect option detail": "shrink-0 text-xs tabular-nums text-slate-500 dark:text-slate-400",
    "MultiSelect empty": "px-3 py-2 text-sm text-slate-500 dark:text-slate-400",
    "MultiSelect refusal":
      "border-t px-3 py-2 text-sm border-slate-200 dark:border-slate-800 text-rose-700 dark:text-rose-400",
    "MultiSelect clear's host":
      "sticky bottom-0 border-t p-2 border-slate-200 dark:border-slate-800 bg-white dark:bg-slate-900",
    "MultiSelect clear":
      "min-h-11 w-full rounded-lg px-3 py-2 text-sm font-medium bg-slate-100 dark:bg-slate-800 text-slate-700 dark:text-slate-300",

    "ProofDocument open decisions":
      "inline-flex h-6 shrink-0 items-center rounded-full px-2 text-xs font-semibold bg-sky-100 dark:bg-sky-950 text-sky-800 dark:text-sky-300",
    "ProofDocument open decision link":
      "block min-w-0 max-w-[90ch] truncate text-sm font-medium underline text-sky-700 dark:text-sky-400 hover:text-sky-900 dark:hover:text-sky-300",
    "ProofDocument Next open decision":
      "ml-auto flex min-h-11 min-w-11 items-center justify-center rounded-full md:min-h-7 md:min-w-7 border-slate-300 dark:border-slate-700 not-refused:hover:border-sky-500 dark:not-refused:hover:border-sky-400 text-slate-700 dark:text-slate-300",
    "ProofDocument block references":
      "pointer-events-auto absolute right-0 min-h-11 min-w-11 rounded-full text-xs font-semibold bg-sky-100 dark:bg-sky-950 text-sky-800 dark:text-sky-300",
    "ProofDocument repair refusal": "text-rose-700 dark:text-rose-400",
    "ProofDocument rebuild refusal": "text-rose-700 dark:text-rose-400",
    "ProofDocument Rebuild":
      "min-h-11 rounded-lg border px-3 py-2 text-sm font-medium border-slate-300 dark:border-slate-700 text-slate-700 dark:text-slate-300 not-refused:hover:border-sky-500 dark:not-refused:hover:border-sky-400",
    "ProofDocument rebuild failure": "text-rose-700 dark:text-rose-400",
    "ProofDocument load failure": "text-rose-700 dark:text-rose-400",

    "AskCard card":
      "relative rounded-xl border-l-4 shadow-sm px-4 pt-5 pb-4 mt-3 border-slate-200 dark:border-slate-800 bg-white dark:bg-slate-900 border-l-rose-500 dark:border-l-rose-400",
    "AskCard urgency notch":
      "absolute -top-2 left-3 rounded px-1.5 text-[10px] leading-4 font-semibold tracking-wide uppercase bg-white dark:bg-slate-900 text-rose-800 dark:text-rose-300",
    "AskCard question":
      "text-[15px] leading-relaxed font-medium text-slate-900 dark:text-slate-100",
    "AskCard asker line":
      "@container mt-1.5 flex flex-wrap items-center gap-x-1.5 text-xs text-slate-500 dark:text-slate-400",
    "AskCard turn": "shrink-0 font-semibold @max-sm:basis-full text-slate-700 dark:text-slate-300",
    "AskCard asker":
      "flex min-w-0 items-center gap-1 text-slate-500 dark:text-slate-400 hover:text-slate-700 dark:hover:text-slate-300",
    "AskCard hand-on line":
      "mt-1.5 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-xs text-slate-500 dark:text-slate-400",
    "AskCard hand-on badge":
      "inline-flex items-center rounded-sm px-1.5 py-0.5 text-xs font-semibold bg-slate-100 dark:bg-slate-800 text-slate-700 dark:text-slate-300",
    "AskCard answer label": "sr-only",
    "AskCard answer box":
      "block w-full rounded-lg px-3 py-2 font-normal outline-none border-slate-300 bg-white focus:border-sky-600 dark:border-slate-700 dark:bg-slate-950 dark:focus:border-sky-400",
    "AskCard Answer":
      "min-h-11 rounded-lg px-3 py-2 text-sm font-semibold bg-sky-700 text-white not-refused:hover:bg-sky-800 refused:cursor-not-allowed refused:bg-slate-300 refused:text-slate-700 dark:refused:bg-slate-700 dark:refused:text-slate-300",
    "AskCard Ask back":
      "min-h-11 rounded-lg border px-3 py-2 text-sm font-medium border-slate-200 dark:border-slate-800 text-slate-700 dark:text-slate-300 hover:border-sky-400 dark:hover:border-sky-300",
    "AskCard Ask back instead":
      "min-h-11 rounded-lg px-3 py-2 text-sm font-semibold bg-sky-700 text-white not-refused:hover:bg-sky-800 refused:cursor-not-allowed refused:bg-slate-300 refused:text-slate-700 dark:refused:bg-slate-700 dark:refused:text-slate-300",
    "AskCard Answer with it anyway":
      "min-h-11 rounded-lg border px-3 py-2 text-sm font-medium border-slate-200 dark:border-slate-800 text-slate-700 dark:text-slate-300 hover:border-sky-400 dark:hover:border-sky-300",
    "AskCard compact card":
      "relative rounded-xl border-l-4 shadow-sm px-3 pt-4 pb-3 mt-3 border-slate-200 dark:border-slate-800 bg-white dark:bg-slate-900 border-l-amber-500 dark:border-l-amber-400",
    "AskCard compact urgency notch":
      "absolute -top-2 left-3 rounded px-1.5 text-[10px] leading-4 font-semibold tracking-wide uppercase bg-white dark:bg-slate-900 text-amber-800 dark:text-amber-300",
    "AskCard approval requested":
      "text-[10px] font-semibold uppercase tracking-wide text-slate-500 dark:text-slate-400",
    "AskCard approval document":
      "mt-1 text-sm text-sky-700 dark:text-sky-400 hover:text-sky-900 dark:hover:text-sky-300",
    "AskCard own words": "min-h-11 text-sm font-medium text-slate-700 dark:text-slate-300",
    "AskCard reason label": "mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300",
    "AskCard submit hint": "text-xs text-slate-500 dark:text-slate-400",
  };

  const read: Record<string, string | null> = {};
  const classAt = (name: string, element: Element | null | undefined) => {
    read[name] = element?.getAttribute("class") ?? null;
  };
  for (const state of ["approved", "awaiting", "changes_requested", "draft", "stale"]) {
    read[`approvalPillClassName ${state}`] = approvalPillClassName(state);
  }
  read.badgeSelectWrapper = badgeSelectWrapper;

  const user = userEvent.setup();
  const view = draw(
    <>
      <div data-case="approval steps">
        <ApprovalSteps
          entries={[
            specEntry({ latest_version: 1, state: "awaiting", waiting_on: "human" }),
            decisionsEntry({
              by: { id: "bob", kind: "user" },
              latest_version: 3,
              reason: "The second decision cites no evidence.",
              state: "changes_requested",
              version: 3,
            }),
            { document: undefined, label: "Verification", step: "criteria" },
          ]}
          primaryArtifactId={SPEC_ID}
        />
      </div>
      <div data-case="assignee">
        <AssigneeControl assignee="alice@example.com" issueKey="CORE-1" />
      </div>
      <div data-case="unassigned">
        <AssigneeControl assignee={null} issueKey="CORE-2" />
      </div>
      <div data-case="P0">
        <PriorityControl issueKey="CORE-1" priority={0} />
      </div>
      <div data-case="P1">
        <PriorityEditor issueKey="CORE-2" priority={1} write={idlePriorityWrite} />
      </div>
      <div data-case="no priority">
        <PriorityControl issueKey="CORE-3" priority={null} />
      </div>
      <div data-case="held">
        <ClaimChip claim={sessionClaim(LIVE_SESSION)} />
      </div>
      <div data-case="lapsed">
        <ClaimChip claim={sessionClaim(GONE_SESSION)} />
      </div>
      <div data-case="repository">
        <RepoControl issueKey="CORE-1" project="CORE" repo="acme/api" resolvedRepo="acme/api" />
      </div>
      <div data-case="inherited repository">
        <RepoControl issueKey="CORE-2" project="CORE" repo={null} resolvedRepo="acme/web" />
      </div>
      <div data-case="components">
        <IssueComponentsLine
          issue={componentsIssue({
            ids: ["dispatch-server"],
            inherited_from: "CORE-9",
            mode: "explicit",
            reason: null,
            unknown: ["old-queue"],
          })}
        />
      </div>
      <div data-case="not attached">
        <IssueComponentsLine
          issue={componentsIssue(
            { ids: [], inherited_from: null, mode: "inherit", reason: null, unknown: [] },
            "CORE-2"
          )}
        />
      </div>
      <div data-case="labels">
        <IssueLabels
          disabled={false}
          labels={["api"]}
          onSave={async () => undefined}
          project="CORE"
          saving={false}
        />
      </div>
      <div data-case="priorities">
        <IssuePriorityLine
          disabled={false}
          issue={{
            advances: [
              { id: "rocq-lean", inherited: false },
              { id: "evals-now", inherited: true },
            ],
            advances_revision: 1,
            key: "CORE-1",
          }}
        />
      </div>
      <div data-case="running">
        <AgentName actor={{ id: LIVE_SESSION, kind: "session" }} />
      </div>
      <div data-case="inline">
        <AgentName actor={{ id: LIVE_SESSION, kind: "session" }} inline />
      </div>
      <div data-case="control">
        <AgentName
          actor={{ id: LIVE_SESSION, kind: "session" }}
          control={(name) => <button type="button">{name}</button>}
          kind="control"
        />
      </div>
      <div data-case="unresponsive">
        <AgentName actor={{ id: STALLED_SESSION, kind: "session" }} />
      </div>
      <div data-case="not running">
        <AgentName actor={{ id: GONE_SESSION, kind: "session" }} />
      </div>
      <div data-case="unresponsive note">
        <AgentUnresponsiveNote lastSeen={Date.now() - 10 * 60_000} state="unresponsive" />
      </div>
      <div data-case="route">
        <RouteReachUnknownMarker testId="route-unknown" />
        <UnresponsiveRouteMarker
          issue={{
            route: "role:reviewer",
            route_holder: STALLED_SESSION,
            route_status: "holder_unresponsive",
            route_status_reason: null,
          }}
        />
        <UnreachableRouteMarker
          issue={{
            route: "role:architect",
            route_holder: null,
            route_status: "no_holder",
            route_status_reason: null,
          }}
        />
      </div>
      <div data-case="github">
        <GitHubLink link={{ url: "https://github.com/owner/repository/issues/7" }} />
      </div>
      <div data-case="subscribers">
        <SubscribedAgents
          onUnsubscribe={() => undefined}
          ownerLabel="CORE-1"
          subscribers={[
            {
              last_seen: Date.now(),
              live: true,
              removable: true,
              session_id: LIVE_SESSION,
              title: "Implementer",
              topics: ["notifications.dispatch.issue.CORE-1.>"],
              via: "notifications.dispatch.issue.CORE-1.>",
            },
          ]}
        />
      </div>
      <div data-case="pills">
        <Pill>label</Pill>
        <LabelPill selected>selected</LabelPill>
        <StatusPill>in progress</StatusPill>
      </div>
    </>,
    "outside"
  );
  const unknown = (() => {
    stubs.listAgents.mockRejectedValueOnce(new Error("Envoy listener unreachable"));
    return draw(<AgentName actor={{ id: LIVE_SESSION, kind: "session" }} />, "outside");
  })();
  try {
    const scope = within(view.container);
    await scope.findByRole("link", { name: /Rocq and Lean/ });
    await scope.findByText("open");
    await waitFor(() =>
      expect(view.container.querySelector('[data-agent-state="unresponsive"]')).not.toBeNull()
    );
    await waitFor(() =>
      expect(unknown.container.querySelector('[data-agent-state="unknown"]')).not.toBeNull()
    );
    await settleReads();

    const steps = within(caseOf(view, "approval steps"));
    const list = steps.getByRole("list", { name: "Approvals" });
    classAt("ApprovalSteps list's host", list.parentElement);
    classAt("ApprovalSteps list", list);
    classAt(
      "ApprovalSteps step name",
      caseOf(view, "approval steps").querySelector('[data-approval-step="spec"] > span')
    );
    classAt("ApprovalSteps chip", steps.getByRole("button", { name: "Awaiting approval" }));
    classAt("ApprovalSteps Not requested", steps.getByText("Not requested"));
    classAt("ApprovalSteps Approve", steps.getByRole("button", { name: "Approve" }));
    classAt(
      "ApprovalSteps Request changes",
      steps.getByRole("button", { name: "Request changes" })
    );
    classAt(
      "ApprovalSteps reason",
      caseOf(view, "approval steps").querySelector('[data-approval-step-reason="decisions"]')
    );

    const assignee = within(caseOf(view, "assignee")).getByRole("combobox");
    classAt("AssigneeControl wrapper", assignee.parentElement);
    classAt("AssigneeControl badge, assigned", assignee.parentElement?.querySelector("span"));
    classAt(
      "AssigneeControl badge, unassigned",
      caseOf(view, "unassigned").querySelector("span[aria-hidden='true']")
    );
    const p0 = within(caseOf(view, "P0")).getByRole("combobox");
    classAt("PriorityControl wrapper", p0.parentElement);
    classAt("PriorityControl badge, P0", p0.parentElement?.querySelector("span"));
    classAt(
      "PriorityControl badge, P1",
      caseOf(view, "P1").querySelector("span[aria-hidden='true']")
    );
    classAt(
      "PriorityControl badge, none",
      caseOf(view, "no priority").querySelector("span[aria-hidden='true']")
    );

    const held = within(caseOf(view, "held")).getByTestId("issue-claim");
    classAt("ClaimChip, held", held);
    classAt("ClaimChip holder", held.querySelector("[data-agent-name]"));
    classAt("ClaimChip dot", held.querySelector(":scope > span[aria-hidden='true']"));
    classAt("ClaimChip age", held.querySelector(":scope > time"));
    classAt("ClaimChip, lapsed", within(caseOf(view, "lapsed")).getByTestId("issue-claim"));
    classAt(
      "ClaimChip not running",
      within(caseOf(view, "lapsed")).getByTestId("issue-claim-lapsed")
    );

    const repository = within(caseOf(view, "repository")).getByRole("combobox", {
      name: "Repository of CORE-1",
    });
    classAt("RepoControl wrapper", repository.parentElement);
    classAt("RepoControl line", repository.parentElement?.parentElement);
    classAt("RepoControl badge, its own", repository.parentElement?.querySelector("span"));
    classAt(
      "RepoControl badge, inherited",
      caseOf(view, "inherited repository").querySelector("span[aria-hidden='true']")
    );

    const components = within(caseOf(view, "components"));
    classAt("IssueComponentsLine line", components.getByTestId("issue-components"));
    classAt("IssueComponentsLine chip", components.getByText("dispatch-server"));
    classAt("IssueComponentsLine retired chip", components.getByText("retired: old-queue"));
    const inheritedLink = components.getByRole("link", { name: "CORE-9" });
    classAt("IssueComponentsLine inherited from", inheritedLink.parentElement);
    classAt("IssueComponentsLine inherited link", inheritedLink);
    classAt(
      "IssueComponentsLine not attached",
      within(caseOf(view, "not attached")).getByText("Not attached")
    );

    const labels = within(caseOf(view, "labels"));
    classAt("IssueLabels line", labels.getByTestId("issue-labels"));
    classAt("IssueLabels chip", labels.getByText("api"));
    classAt("IssueLabels trigger", labels.getByRole("button", { name: "Edit labels" }));

    const advances = within(caseOf(view, "priorities"));
    const priorityLink = advances.getByRole("link", { name: /Rocq and Lean/ });
    classAt("IssuePriorityLine line", advances.getByTestId("issue-advances"));
    classAt("IssuePriorityLine entry", priorityLink.parentElement);
    classAt("IssuePriorityLine link", priorityLink);
    classAt("IssuePriorityLine inherited", advances.getByText("inherited"));
    classAt("IssuePriorityLine remove", advances.getByRole("button", { name: /^Remove / }));
    const add = advances.getByRole("button", { name: "Add a priority to CORE-1" });
    classAt("IssuePriorityLine add", add);
    await user.click(add);
    const picker = await advances.findByRole("combobox", { name: "Priority to advance" });
    classAt("IssuePriorityLine form", picker.closest("form"));
    classAt("IssuePriorityLine picker", picker);
    classAt("IssuePriorityLine Add", advances.getByRole("button", { name: "Add" }));
    classAt("IssuePriorityLine Cancel", advances.getByRole("button", { name: "Cancel" }));

    classAt("AgentName link", caseOf(view, "running").querySelector("a"));
    classAt("AgentName inline link", caseOf(view, "inline").querySelector("a"));
    classAt(
      "AgentName live link",
      within(caseOf(view, "control")).getByRole("link", { name: /^Open live conversation/ })
    );
    classAt("AgentName mark, running", caseOf(view, "running").querySelector("[role='img']"));
    classAt(
      "AgentName mark, unresponsive",
      caseOf(view, "unresponsive").querySelector("[role='img']")
    );
    classAt(
      "AgentName mark, not running",
      caseOf(view, "not running").querySelector("[role='img']")
    );
    classAt("AgentName mark, unknown", unknown.container.querySelector("[role='img']"));
    classAt(
      "AgentUnresponsiveNote",
      within(caseOf(view, "unresponsive note")).getByTestId("agent-unresponsive")
    );

    const route = within(caseOf(view, "route"));
    classAt("RouteReachUnknownMarker", route.getByTestId("route-unknown"));
    classAt("UnresponsiveRouteMarker", route.getByTestId("issue-route-unresponsive"));
    classAt("UnreachableRouteMarker", route.getByTestId("issue-route-unreachable"));

    const github = within(caseOf(view, "github"));
    classAt("GitHubLink link", github.getByRole("link"));
    classAt("GitHubLink state", github.getByText("open"));

    classAt(
      "SubscribedAgents heading",
      within(caseOf(view, "subscribers")).getByRole("heading", { name: "Subscribed agents" })
    );

    const pills = within(caseOf(view, "pills"));
    classAt("Pill", pills.getByText("label"));
    classAt("LabelPill, selected", pills.getByText("selected"));
    const status = pills.getByText("in progress");
    classAt("StatusPill", status);
    classAt("StatusPill dot", status.querySelector("span[aria-hidden='true']"));
  } finally {
    undraw(view);
    undraw(unknown);
  }

  // The components' own controls, each at its defaults: a copy that lands and one that fails,
  // each pin, each look of a refusable button, each connection state and the closed picker.
  const controls = draw(
    <>
      <div data-case="copy, icon">
        <CopyButton value="CORE-1" what="issue key" />
      </div>
      <div data-case="copy, labelled">
        <CopyButton value="session-1" what="session ID">
          session-1
        </CopyButton>
      </div>
      <div data-case="pins">
        <PinButton label="Unpin CORE-1" onClick={() => undefined} pinned />
        <PinButton label="Pin CORE-1" onClick={() => undefined} pinned={false} />
        <PinButton label="Pin this turn" onClick={() => undefined} pinned={false} quiet />
      </div>
      <div data-case="refusable">
        <RefusableButton refusal="Write a reason first.">Send</RefusableButton>
        <RefusableButton look="secondary-compact" refusal={null}>
          Attach
        </RefusableButton>
      </div>
      <div data-case="connection">
        <ConnectionDot connection="connected" pending={undefined} />
        <ConnectionDot connection="connecting" pending={undefined} />
        <ConnectionDot connection="failed" pending={undefined} />
        <ConnectionDot
          connection="offline"
          pending={{ count: 2, readOnly: false, rebuiltAt: undefined, stored: true }}
        />
      </div>
      <div data-case="closed picker">
        <MultiSelect
          emptyMessage="No labels yet."
          label="Labels"
          onChange={() => undefined}
          onOpenChange={() => undefined}
          open={false}
          options={["api"]}
          searchLabel="Search labels"
          selected={[]}
        />
      </div>
    </>,
    "outside"
  );
  const originalClipboard = navigator.clipboard;
  try {
    const scope = within(controls.container);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: async () => undefined },
    });
    await user.click(scope.getByRole("button", { name: "Copy issue key CORE-1" }));
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: async () => Promise.reject(new Error("denied")) },
    });
    await user.click(scope.getByRole("button", { name: "Copy session ID session-1" }));
    await scope.findByText("Copied");
    await scope.findByText(/Copy failed/);
    classAt("CopyButton, icon", scope.getByRole("button", { name: "Copy issue key CORE-1" }));
    classAt(
      "CopyButton, labelled",
      scope.getByRole("button", { name: "Copy session ID session-1" })
    );
    classAt("CopyButton copied", caseOf(controls, "copy, icon").querySelector("[aria-live]"));
    classAt("CopyButton failed", caseOf(controls, "copy, labelled").querySelector("[aria-live]"));

    classAt("PinButton, pinned", scope.getByRole("button", { name: "Unpin CORE-1" }));
    classAt("PinButton, unpinned", scope.getByRole("button", { name: "Pin CORE-1" }));
    classAt("PinButton, quiet", scope.getByRole("button", { name: "Pin this turn" }));

    classAt("RefusableButton primary", scope.getByRole("button", { name: "Send" }));
    classAt("RefusableButton secondary-compact", scope.getByRole("button", { name: "Attach" }));

    const dots = within(caseOf(controls, "connection")).getAllByRole("status");
    for (const [index, state] of ["connected", "connecting", "failed", "offline"].entries()) {
      classAt(`ConnectionDot ${state}`, dots[index]?.querySelector("span[aria-hidden]"));
    }
    classAt("ConnectionDot notice", dots[3]?.querySelectorAll("span[aria-hidden]")[1]);

    classAt(
      "MultiSelect trigger",
      within(caseOf(controls, "closed picker")).getByRole("button", { name: "Labels" })
    );
  } finally {
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: originalClipboard });
    undraw(controls);
  }

  // The picker open, its popover portalled under `document.body`: a pick, a detail, a refusal
  // and Clear, and a second picker with nothing to pick.
  const pickers = draw(
    <>
      <MultiSelect
        emptyMessage="No labels yet."
        error="Labels must be 1–40 characters."
        label="Labels"
        onChange={() => undefined}
        onOpenChange={() => undefined}
        open
        optionDetail={(value) => (value === "api" ? "3" : undefined)}
        options={["api", "docs"]}
        searchLabel="Search labels"
        selected={["docs"]}
      />
      <MultiSelect
        emptyMessage="No topics yet."
        label="Topics"
        onChange={() => undefined}
        onOpenChange={() => undefined}
        open
        options={[]}
        searchLabel="Search topics"
        selected={[]}
      />
    </>,
    "outside"
  );
  try {
    const body = within(pickers.baseElement);
    const listbox = body.getByRole("listbox", { name: "Labels options" });
    classAt("MultiSelect panel", listbox.parentElement);
    const search = body.getByRole("combobox", { name: "Search labels" });
    classAt("MultiSelect search's host", search.parentElement);
    classAt("MultiSelect search", search);
    const [active, row] = within(listbox).getAllByRole("option");
    classAt("MultiSelect row, active", active);
    classAt("MultiSelect row", row);
    classAt("MultiSelect checkbox", active.querySelector("span[aria-hidden='true']"));
    classAt("MultiSelect option label", within(active).getByText("api"));
    classAt("MultiSelect option detail", within(active).getByText("3"));
    classAt("MultiSelect refusal", body.getByText("Labels must be 1–40 characters."));
    const clear = body.getByRole("button", { name: "Clear labels" });
    classAt("MultiSelect clear's host", clear.parentElement);
    classAt("MultiSelect clear", clear);
    classAt("MultiSelect empty", body.getByText("No topics yet."));
  } finally {
    undraw(pickers);
  }

  // The document's own lines and controls: its open decisions and a block's references, then
  // each refusal its admission or its rebuild draws.
  stubs.listIssueAsks.mockResolvedValue([
    blockAsk("ask-b1", "b-1", "Should we ship?"),
    blockAsk("ask-b2", "b-2", "Which region?"),
  ]);
  const decisions = draw(proofDocument(), "outside");
  try {
    const scope = within(decisions.container);
    const nav = await scope.findByRole("navigation", { name: "Open decisions" });
    classAt("ProofDocument open decisions", nav.querySelector("span"));
    classAt("ProofDocument open decision link", within(nav).getByRole("link"));
    classAt(
      "ProofDocument Next open decision",
      within(nav).getByRole("button", { name: "Next open decision" })
    );
    classAt(
      "ProofDocument block references",
      await scope.findByRole("button", { name: "2 references on block" })
    );
  } finally {
    undraw(decisions);
  }
  stubs.listIssueAsks.mockResolvedValue([]);

  stubs.getArtifactText.mockRejectedValueOnce(
    new ApiError(409, {
      code: "DOC_SCHEMA",
      error: "The document holds a node outside its schema.",
    })
  );
  const repair = draw(proofDocument(), "outside");
  try {
    classAt(
      "ProofDocument repair refusal",
      await within(repair.container).findByText("The document holds a node outside its schema.")
    );
  } finally {
    undraw(repair);
  }

  stubs.getArtifactText.mockRejectedValueOnce(
    new ApiError(409, { code: "DOCUMENT_UNLOADABLE", error: "The stored history cannot load." })
  );
  const confirm = spyOn(window, "confirm").mockReturnValue(true);
  const rebuildArtifact = spyOn(api, "rebuildArtifact").mockRejectedValue(
    new ApiError(422, { code: "REBUILD_REFUSED", error: "The history cannot be rebuilt now." })
  );
  const rebuild = draw(proofDocument(), "outside");
  try {
    const scope = within(rebuild.container);
    classAt(
      "ProofDocument rebuild refusal",
      await scope.findByText("The stored history cannot load.")
    );
    const rebuildButton = scope.getByRole("button", { name: "Rebuild from the latest version" });
    classAt("ProofDocument Rebuild", rebuildButton);
    await user.click(rebuildButton);
    classAt(
      "ProofDocument rebuild failure",
      await scope.findByText("The history cannot be rebuilt now.")
    );
  } finally {
    undraw(rebuild);
    confirm.mockRestore();
    rebuildArtifact.mockRestore();
  }

  stubs.getArtifactText.mockRejectedValueOnce(
    new ApiError(404, { code: "PROXY_NOT_FOUND", error: "proxy not found" })
  );
  const unloaded = draw(proofDocument(), "outside");
  try {
    classAt(
      "ProofDocument load failure",
      await within(unloaded.container).findByText("This document could not load: proxy not found")
    );
  } finally {
    undraw(unloaded);
  }

  // The card's own elements: a blocking question in full, its answer box and its actions, and
  // the actions a question-shaped answer offers.
  const question = openAsk();
  const questionCard = draw(
    <AskCard
      ask={question}
      getAskThread={async () => threadOf(question)}
      initialThread={threadOf(question)}
    />,
    "outside"
  );
  try {
    await settleReads();
    const card = within(questionCard.container);
    const article = card.getByRole("article");
    classAt("AskCard card", article);
    classAt("AskCard urgency notch", article.querySelector(":scope > span[aria-hidden='true']"));
    classAt("AskCard question", article.querySelector(".dispatch-markdown")?.parentElement);
    const turn = card.getByTestId("turn-ask-1");
    classAt("AskCard turn", turn);
    classAt("AskCard asker line", turn.parentElement?.parentElement);
    classAt("AskCard asker", card.getByRole("button", { expanded: false, name: /Implementer/ }));
    const handOn = card.getByRole("combobox", { name: "Hand this question to" });
    classAt("AskCard hand-on line", handOn.parentElement?.parentElement);
    classAt(
      "AskCard hand-on badge",
      handOn.parentElement?.querySelector("span[aria-hidden='true']")
    );
    const answerBox = card.getByRole("textbox", { name: "Your answer" });
    classAt("AskCard answer label", answerBox.previousElementSibling);
    classAt("AskCard answer box", answerBox);
    classAt("AskCard Answer", card.getByRole("button", { name: "Answer" }));
    classAt("AskCard Ask back", card.getByRole("button", { name: "Ask back" }));
    await user.type(answerBox, "Which one?");
    await user.click(card.getByRole("button", { name: "Answer" }));
    classAt(
      "AskCard Ask back instead",
      await card.findByRole("button", { name: "Ask back instead" })
    );
    classAt(
      "AskCard Answer with it anyway",
      card.getByRole("button", { name: "Answer with it anyway" })
    );
  } finally {
    undraw(questionCard);
  }

  // A compact approval request: its label, its document, the disclosure of the reader's own
  // words, and the reason Request changes asks for.
  const approval = openAsk({
    approval: {
      artifact_id: "artifact-1",
      documents: null,
      name: "spec.md",
      requested_version: 12,
      version: 12,
    },
    approval_artifact: { name: "spec.md", primary: true, project: "CORE", slug: "spec-md" },
    id: "ask-2",
    kind: "approval",
    options: [{ label: "Approve" }, { label: "Request changes" }],
    question: "Approve spec.md (version 12)?",
    urgency: "high",
  });
  const approvalCard = draw(
    <AskCard
      ask={approval}
      getAskThread={async () => threadOf(approval)}
      initialThread={threadOf(approval)}
      variant="compact"
    />,
    "outside"
  );
  try {
    await settleReads();
    const card = within(approvalCard.container);
    const article = card.getByRole("article");
    classAt("AskCard compact card", article);
    classAt(
      "AskCard compact urgency notch",
      article.querySelector(":scope > span[aria-hidden='true']")
    );
    classAt("AskCard approval requested", card.getByText("Approval requested"));
    classAt("AskCard approval document", card.getByRole("link", { name: /spec\.md/ }).closest("p"));
    classAt(
      "AskCard own words",
      card.getByRole("button", { name: "Add a note or answer in your own words" })
    );
    await user.click(card.getByRole("radio", { name: /Request changes/ }));
    classAt("AskCard reason label", await card.findByText("Reason (required)"));
    classAt("AskCard submit hint", card.getByText(/^Add a reason to send/));
  } finally {
    undraw(approvalCard);
  }

  expect(read).toEqual(want);
});
