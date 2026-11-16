import { afterEach, beforeEach, expect, type Mock, spyOn, test } from "bun:test";
import { act, type RenderResult, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";

import { fakeDocumentRuntime } from "../../__tests__/document-runtime";
import { graphNode } from "../../__tests__/graph-fixture";
import { artifact as specDocument } from "../../__tests__/proof-document";
import { renderWithQueries } from "../../__tests__/query-provider";
import { ApiError, api } from "../../api/client";
import type {
  Agent,
  ArchitectureTree,
  Artifact,
  ArtifactApproval,
  ArtifactReview,
  Ask,
  AskRead,
  Priorities,
  PriorityRow,
  Subscriber,
} from "../../api/types";
import { CopyButton } from "../../components/CopyButton";
import { MultiSelect } from "../../components/MultiSelect";
import { LabelPill, Pill, StatusPill } from "../../components/Pill";
import { PinButton } from "../../components/PinButton";
import { RefusableButton } from "../../components/RefusableButton";
import {
  calloutWarningBg,
  calloutWarningBorder,
  calloutWarningText,
  linkText,
  PAGE_COLORS,
  pageInkText,
  pageStateText,
  pageWaitingChip,
  textMutedOnSurface,
} from "../../theme/classes";
import { ApprovalChip } from "../doc/ApprovalChip";
import { ConnectionDot } from "../doc/ConnectionDot";
import { ProofDocument } from "../doc/ProofDocument";
import { DocumentRuntime } from "../doc/runtime";
import { AskCard } from "../inbox/AskCard";
import { ApprovalSteps } from "../issue/ApprovalSteps";
import { AssigneeControl } from "../issue/AssigneeControl";
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
import { AgentName, AgentUnresponsiveNote } from "../refs/AgentName";
import { Timestamp } from "../refs/Timestamp";
import { NoTooltips } from "./NoTooltips";

/**
 * The shared controls on the new issue page's surface (dispatch://INFRA-3935 R2 and R6): inside
 * `NoTooltips` each file of R9 sets no `title`, draws what a `title` held, says the page's words
 * with their marks, and draws every colour in the page's palette; outside it, each draws what
 * `main` draws. Each case mounts the controls through `draw`, from the fixtures below, which the
 * controls' own test files on `main` build the same way.
 */

// ---------------------------------------------------------------------------------------------
// Drawing a control on either side of `NoTooltips`
// ---------------------------------------------------------------------------------------------

/** Which side of `NoTooltips` a mount is drawn on: `inside` is the new issue page's surface,
 *  `outside` every page of `main`. */
type Surface = "inside" | "outside";

/** Draws `node` the way a page does: under a router and a query client of its own, inside
 *  `NoTooltips` or outside it. */
function draw(node: ReactNode, surface: Surface): RenderResult {
  return renderWithQueries(
    <MemoryRouter>{surface === "inside" ? <NoTooltips>{node}</NoTooltips> : node}</MemoryRouter>
  );
}

/** One control of R9, or a few of one file, in the state a case reads it in. */
interface Mount {
  /** The file of R9 that draws it, and how it is drawn. */
  readonly name: string;
  /** What to draw: called once for each draw, so each draw holds fresh fixtures. */
  readonly node: () => ReactNode;
  /** Brings the drawn mount to the state it is read in: its reads answered, a popover open. */
  readonly settle?: (view: RenderResult, user: UserEvent) => Promise<void>;
  /** The roots of what a file outside R9 draws inside the mount, read by selector and by role
   *  (R2's "no title" binds each R9 file's own elements alone, so `AskCard`'s children of other
   *  files, the editor `ProofDocument` hosts and a Markdown body are out). Each root and what it
   *  holds is skipped; an element of a file of R9 that a child of another file holds, such as
   *  `CopyRefButton`'s `CopyButton`, is read. */
  readonly outside?: (root: HTMLElement) => readonly Element[];
  /** Whether the mount draws no colour at all (a `Timestamp` alone takes its host's). */
  readonly colourless?: boolean;
}

/** A drawn mount, settled, with the elements its files of R9 draw. */
interface Drawn {
  readonly elements: readonly Element[];
  readonly view: RenderResult;
}

/** Draws `mount` on `surface`, settles it, and reads every element its files of R9 draw, those a
 *  portal puts under `document.body` (an open `MultiSelect`'s popover) among them. */
async function drawMount(mount: Mount, surface: Surface): Promise<Drawn> {
  answerDefaults();
  const user = userEvent.setup();
  const view = draw(mount.node(), surface);
  await (mount.settle ?? settleReads)(view, user);
  const skipped = mount.outside?.(view.baseElement) ?? [];
  const elements = [...view.baseElement.querySelectorAll("*")].filter(
    (element) => !skipped.some((root) => root.contains(element))
  );
  return { elements, view };
}

/** Takes a drawn mount off the page, its container with it, so the next mount's read of
 *  `document.body` holds that mount alone. */
function undraw(view: RenderResult): void {
  view.unmount();
  view.container.remove();
}

// ---------------------------------------------------------------------------------------------
// Reading a class for its colours
// ---------------------------------------------------------------------------------------------

/** `theme/no-raw-colors.test.ts`'s `COLOR_FAMILIES` and `RAW_COLOR_PATTERN`, copied as they
 *  stand there: they are constants of that test file, which exports nothing. The pattern is the
 *  palette guard's own reading of a raw Tailwind colour. */
const COLOR_FAMILIES = [
  "slate",
  "sky",
  "rose",
  "amber",
  "emerald",
  "red",
  "orange",
  "yellow",
  "lime",
  "green",
  "teal",
  "cyan",
  "blue",
  "indigo",
  "violet",
  "purple",
  "fuchsia",
  "pink",
  "gray",
  "zinc",
  "neutral",
  "stone",
] as const;
const RAW_COLOR_PATTERN = new RegExp(
  `\\b(?:text|bg|border|ring|placeholder|divide|outline|caret|accent|decoration|from|via|to)-(?:(?:${COLOR_FAMILIES.join("|")})-\\d{2,3}|white|black|transparent)\\b`
);

/** The variants of a focus indicator: departure 4 of the plan (decision 58 of
 *  dispatch://INFRA-1196/artifact/part-decisions-md) keeps `focusVisibleRing` and
 *  `hasFocusVisibleRing`, and `main`'s focus rule on a field, inside `NoTooltips`, since the
 *  page's palette holds no focus colour and the application has one focus indicator. */
const FOCUS_VARIANTS: readonly string[] = ["focus", "focus-visible", "has-focus-visible"];

/** A token as its variants and the utility after them. A colon inside brackets or parentheses
 *  is an arbitrary value's (`[overflow-wrap:anywhere]`, `content-[attr(data-initial)]`), not a
 *  variant's. */
function splitToken(token: string): { readonly utility: string; readonly variants: string[] } {
  const variants: string[] = [];
  let depth = 0;
  let start = 0;
  for (let index = 0; index < token.length; index += 1) {
    const character = token[index];
    if (character === "[" || character === "(") depth += 1;
    else if (character === "]" || character === ")") depth -= 1;
    else if (character === ":" && depth === 0) {
      variants.push(token.slice(start, index));
      start = index + 1;
    }
  }
  return { utility: token.slice(start), variants };
}

/** Every utility that takes a colour after its property, and what follows the property. */
const COLOUR_PROPERTY =
  /^-?(?:text|bg|border(?:-[xytrblse])?|outline|ring(?:-offset)?|inset-ring|shadow|inset-shadow|drop-shadow|fill|stroke|decoration|divide|placeholder|caret|accent|from|via|to)-(.+)$/;
const SHADE = new RegExp(`^(?:${COLOR_FAMILIES.join("|")})-\\d{2,3}$`);

/**
 * The colour a utility names, or `undefined` for a utility that names none.
 *
 * The same property prefix carries colours and sizes alike (`text-sky-700` and `text-xs`,
 * `border-page-line` and `border-2`, `ring-sky-600` and `ring-2`), so a utility names a colour
 * by what follows its property, an opacity (`/40`) dropped: a shade of a palette family
 * (`sky-700`), one of the keyword colours `white`, `black` and `transparent`, an arbitrary colour
 * (`[#fff]`, `[rgb(…)]`, `[color:…]`, `[var(--color-…)]`), or a token of the page's palette by its
 * own name (`page-ink`, `icon-rail`), which `PAGE_COLORS` must then hold, so a misspelt page
 * token is a colour and fails. Anything else after the same prefixes names no colour: a size
 * (`text-xs`, `border-2`, `ring-2`, `border-l-4`), a style (`border-dashed`, `outline-none`,
 * `text-left`, `shadow-lg`), an arbitrary value that is no colour (`text-[10px]`), and `current`
 * and `inherit`, which paint the element's own text colour rather than name one; that text
 * colour is a token of its own, read here too.
 */
function colourOf(utility: string): string | undefined {
  const named = COLOUR_PROPERTY.exec(utility)?.[1];
  if (named === undefined) return undefined;
  const name = named.replace(/\/(?:\d+|\[[^\]]*\])$/, "");
  if (SHADE.test(name) || name === "white" || name === "black" || name === "transparent") {
    return name;
  }
  if (/^\[(?:#|rgba?\(|hsla?\(|oklch\(|oklab\(|color:|var\(--color-)/.test(name)) return name;
  return /^(?:page|icon-rail)(?:-[a-z]+)*$/.test(name) ? name : undefined;
}

/** What a mount's elements draw that is no colour of the page: each token the palette guard
 *  reads as raw, and each colour `PAGE_COLORS` does not name, focus indicators skipped. */
function offPageColours(
  name: string,
  elements: readonly Element[]
): { readonly colours: number; readonly offenders: string[] } {
  const offenders: string[] = [];
  let colours = 0;
  for (const element of elements) {
    const testId = element.getAttribute("data-testid");
    const where = `${name}: <${element.tagName.toLowerCase()}${testId === null ? "" : ` data-testid="${testId}"`}>`;
    for (const token of (element.getAttribute("class") ?? "").split(/\s+/).filter(Boolean)) {
      const { utility, variants } = splitToken(token);
      if (variants.some((variant) => FOCUS_VARIANTS.includes(variant))) continue;
      if (RAW_COLOR_PATTERN.test(utility)) {
        offenders.push(`${where} ${token}: a raw colour`);
        continue;
      }
      const colour = colourOf(utility);
      if (colour === undefined) continue;
      colours += 1;
      if (!Object.hasOwn(PAGE_COLORS, colour)) offenders.push(`${where} ${token}: no page colour`);
    }
  }
  return { colours, offenders };
}

/** The tokens of a class string, for reading whether an element carries each. */
function tokens(className: string): string[] {
  return className.split(/\s+/).filter(Boolean);
}

function classOf(element: Element): string[] {
  return tokens(element.getAttribute("class") ?? "");
}

// ---------------------------------------------------------------------------------------------
// What the controls read, each answered by a stub on `api`
// ---------------------------------------------------------------------------------------------

const LIVE_SESSION = "session-live-0001";
const STALLED_SESSION = "session-stalled-02";
const GONE_SESSION = "session-gone-00003";

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

/** The project's architecture, holding the one component the components line names. */
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

/** The reviews the approval chip's dialog lists (`ApprovalChip.test.tsx`'s). */
const reviews: ArtifactReview[] = [
  {
    actor: { id: "alice", kind: "user" },
    artifact_id: "artifact-notes",
    ask_id: null,
    content_sha256: null,
    content_sha256_source: null,
    created_at: "2026-09-10T00:00:00Z",
    id: "review-1",
    kind: "person",
    reason: null,
    state: "approved",
    version: 2,
  },
  {
    actor: { id: "bob", kind: "user" },
    artifact_id: "artifact-notes",
    ask_id: null,
    content_sha256: null,
    content_sha256_source: null,
    created_at: "2026-09-11T00:00:00Z",
    id: "review-2",
    kind: "person",
    reason: "Please tighten the intro.",
    state: "changes_requested",
    version: 3,
  },
];

/** A GitHub read's answer, as `api.githubRest` hands it (`IssuePage.test.tsx`'s). */
function githubAnswer(body: unknown): Response {
  return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
}

/** The `api` methods the mounts read, each answered for the whole file and restored after each
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
  readonly listArtifactReviews: Mock<typeof api.listArtifactReviews>;
  readonly listIssueAsks: Mock<typeof api.listIssueAsks>;
  readonly listIssues: Mock<typeof api.listIssues>;
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
    getArchitecture: spyOn(api, "getArchitecture"),
    getArtifact: spyOn(api, "getArtifact"),
    getArtifactBlocks: spyOn(api, "getArtifactBlocks"),
    getArtifactText: spyOn(api, "getArtifactText"),
    getDocumentRead: spyOn(api, "getDocumentRead"),
    getIssueDecisions: spyOn(api, "getIssueDecisions"),
    getIssueSubscribers: spyOn(api, "getIssueSubscribers"),
    getReferences: spyOn(api, "getReferences"),
    githubRest: spyOn(api, "githubRest"),
    listAgents: spyOn(api, "listAgents"),
    listArtifactReviews: spyOn(api, "listArtifactReviews"),
    listIssueAsks: spyOn(api, "listIssueAsks"),
    listIssues: spyOn(api, "listIssues"),
    listPriorities: spyOn(api, "listPriorities"),
    listProjectArchitectureSources: spyOn(api, "listProjectArchitectureSources"),
    listRepoProjects: spyOn(api, "listRepoProjects"),
    listUsers: spyOn(api, "listUsers"),
    putDocumentRead: spyOn(api, "putDocumentRead"),
    whoAmI: spyOn(api, "whoAmI"),
  };
  answerDefaults();
});

afterEach(() => {
  for (const stub of Object.values(stubs)) stub.mockRestore();
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: originalClipboard });
});

const originalClipboard = navigator.clipboard;

/** Each stub's answer for a mount that asks nothing else of it; a mount that needs another
 *  answer sets it in its own `node` or `settle`, and the next mount starts from these again. */
function answerDefaults(): void {
  stubs.getArchitecture.mockResolvedValue(architecture);
  stubs.getArtifact.mockImplementation(async (id) =>
    id === specDocument.id ? specDocument : notes
  );
  stubs.getArtifactBlocks.mockResolvedValue([
    {
      from: 0,
      id: "block-1",
      references: { asks: 1, comments: 1 },
      to: 17,
      token: "sha256:block-1",
      type: "paragraph",
    },
  ]);
  stubs.getArtifactText.mockResolvedValue({
    markdown: "The live document",
    token: "sha256:spec-text",
    version: null,
  });
  stubs.getDocumentRead.mockImplementation(async (id) => ({
    artifact_id: id,
    read_at: null,
    version: null,
  }));
  stubs.getIssueDecisions.mockResolvedValue({ decisions: [], document: null, open_questions: [] });
  stubs.getIssueSubscribers.mockResolvedValue([]);
  stubs.getReferences.mockResolvedValue({
    edges: [],
    node: graphNode({ id: "ask-1", kind: "ask" }),
  });
  stubs.githubRest.mockImplementation(async (path) => {
    if (path === "repos/owner/repository/pulls/7") {
      return githubAnswer({
        head: { sha: "abcdef" },
        merged: false,
        state: "open",
        title: "Ship it",
      });
    }
    if (path.startsWith("repos/owner/repository/commits/abcdef/check-runs")) {
      return githubAnswer({
        check_runs: [
          { check_suite: { id: 1 }, conclusion: "success", id: 1, name: "ci", status: "completed" },
        ],
      });
    }
    if (path.startsWith("repos/owner/repository/actions/runs")) {
      return githubAnswer({ workflow_runs: [{ check_suite_id: 1, event: "pull_request", id: 1 }] });
    }
    throw new Error(`no GitHub answer for ${path}`);
  });
  stubs.listAgents.mockResolvedValue([
    listed(LIVE_SESSION, "Implementer"),
    listed(STALLED_SESSION, "Reviewer", "unresponsive"),
  ]);
  stubs.listArtifactReviews.mockResolvedValue(reviews);
  stubs.listIssueAsks.mockResolvedValue([]);
  stubs.listIssues.mockResolvedValue([]);
  stubs.listPriorities.mockResolvedValue(priorities);
  stubs.listProjectArchitectureSources.mockResolvedValue([]);
  stubs.listRepoProjects.mockResolvedValue([]);
  stubs.listUsers.mockResolvedValue([]);
  stubs.putDocumentRead.mockImplementation(async (id, version) => ({
    artifact_id: id,
    read_at: "2026-09-09T00:00:00Z",
    version,
  }));
  stubs.whoAmI.mockResolvedValue({ event_head: "0", kind: "user", login: "alice" });
}

/** Lets every read a mount started answer and every render after it land. */
async function settleReads(): Promise<void> {
  for (let turn = 0; turn < 5; turn += 1) {
    await act(async () => {
      const { promise, resolve } = Promise.withResolvers<void>();
      setTimeout(resolve, 0);
      await promise;
    });
  }
}

// ---------------------------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------------------------

const SPEC_ID = "artifact-spec";
const DECISIONS_ID = "artifact-decisions";

/** A document of the issue (`shared-controls.options.test.tsx`'s `doc`). */
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

/** The document the approval chips speak for (`ApprovalChip.test.tsx`'s `artifact`). */
const notes = doc("artifact-notes", "notes.md");

function notesWith(approval: ArtifactApproval): Artifact {
  return { ...notes, approval };
}

/** The open request the version link reads its version from. */
const specRequest = {
  approval: { artifact_id: SPEC_ID, name: "spec.md", requested_version: 1, version: 1 },
  id: "ask-spec",
  kind: "approval",
  state: "open",
} as unknown as Ask;

/** The gate's steps as Details draws them: the spec waits on a request for its version 1, the
 *  decisions were sent back with a reason, and the verification has no document yet. */
function stepEntries(): StepEntry[] {
  return [
    {
      document: doc(SPEC_ID, "spec.md", {
        latest_version: 2,
        state: "awaiting",
        waiting_on: "human",
      }),
      label: "Spec",
      step: "spec",
    },
    {
      document: doc(DECISIONS_ID, "decisions.md", {
        by: { id: "bob", kind: "user" },
        latest_version: 3,
        reason: "The second decision cites no evidence.",
        state: "changes_requested",
        version: 3,
      }),
      label: "Decisions",
      step: "decisions",
    },
    { document: undefined, label: "Verification", step: "criteria" },
  ];
}

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

/** An open approval request (`AskCard.test.tsx`'s `approvalAsk`). */
function openApprovalAsk(): Ask {
  return openAsk({
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

/** Two open decisions in the spec, each its own block, which the page's navigator counts. */
function blockAsk(id: string, blockId: string, question: string): Ask {
  return openAsk({
    author: { id: LIVE_SESSION, kind: "session", origin: { session_title: "Architect" } },
    block_artifact: { id: specDocument.id, primary: true, slug: "spec" },
    block_id: blockId,
    id,
    options: [{ label: "Ship" }, { label: "Hold" }],
    question,
    urgency: "high",
  });
}

/** The header's priority write, which no row of this file presses. */
const idlePriorityWrite: IssuePriorityWrite = {
  failed: false,
  pending: false,
  retry: () => undefined,
  submit: () => undefined,
};

function subscriber(sessionId: string, title: string, removable: boolean): Subscriber {
  return {
    last_seen: Date.now(),
    live: true,
    removable,
    session_id: sessionId,
    title,
    topics: [removable ? "notifications.dispatch.issue.CORE-1.>" : "notifications.dispatch.>"],
    via: removable ? "notifications.dispatch.issue.CORE-1.>" : "notifications.dispatch.>",
  };
}

const minutesFromNow = (minutes: number) => new Date(Date.now() + minutes * 60_000).toISOString();

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

/** Opens a popover or a dialog by the control a reader presses. */
function pressing(name: string | RegExp) {
  return async (view: RenderResult, user: UserEvent) => {
    await settleReads();
    await user.click(await within(view.container).findByRole("button", { name }));
    await settleReads();
  };
}

// ---------------------------------------------------------------------------------------------
// Every code file of R9, and `MultiSelect`, in the states that draw its colours
// ---------------------------------------------------------------------------------------------

/** The roots of what `AskCard`'s children of other files draw (R2's "no title" leaves them out,
 *  since the new page draws no `AskCard`): each option row (`AskChoiceRow`, the label inside the
 *  options' fieldset), the decline control (`DeclineControl`, around its button), the
 *  recipients (`AskRecipients`), the replies (`AskThread`) and the question's Markdown body. */
function askCardChildren(root: HTMLElement): Element[] {
  const declines = within(root)
    .queryAllByRole("button", { name: "Decline to answer" })
    .flatMap((button) => (button.parentElement === null ? [] : [button.parentElement]));
  return [
    ...root.querySelectorAll(
      "form fieldset > label, section[aria-label='Recipients'], section[aria-label='Replies'], .dispatch-markdown"
    ),
    ...declines,
  ];
}

/** The editor `ProofDocument` hosts, which the editor fills and the decision blocks portal into:
 *  the element its `root` ref holds, the first under the document's article. */
function documentEditor(root: HTMLElement): Element[] {
  return [...root.querySelectorAll("article[aria-label='Document'] > div:first-child")];
}

/** A Markdown body (`MarkdownBody`), which draws a reviewer's reason. */
function markdownBodies(root: HTMLElement): Element[] {
  return [...root.querySelectorAll(".dispatch-markdown")];
}

const MOUNTS: readonly Mount[] = [
  {
    name: "CopyButton, copied and refused",
    node: () => (
      <>
        <CopyButton value="session-1" what="session ID">
          ID
        </CopyButton>
        <CopyButton value="CORE-1" what="issue key" />
      </>
    ),
    settle: async (view, user) => {
      Object.defineProperty(navigator, "clipboard", {
        configurable: true,
        value: { writeText: async () => undefined },
      });
      await user.click(
        within(view.container).getByRole("button", { name: "Copy session ID session-1" })
      );
      Object.defineProperty(navigator, "clipboard", {
        configurable: true,
        value: { writeText: async () => Promise.reject(new Error("denied")) },
      });
      await user.click(
        within(view.container).getByRole("button", { name: "Copy issue key CORE-1" })
      );
      await within(view.container).findByText("Copied");
      await within(view.container).findByText(/Copy failed/);
    },
  },
  {
    name: "MultiSelect, open with a pick, details and a refusal",
    node: () => (
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
    ),
  },
  {
    name: "MultiSelect, open with nothing to pick",
    node: () => (
      <MultiSelect
        emptyMessage="No labels yet."
        label="Labels"
        onChange={() => undefined}
        onOpenChange={() => undefined}
        open
        options={[]}
        searchLabel="Search labels"
        selected={[]}
      />
    ),
  },
  {
    name: "Pill, LabelPill and StatusPill",
    node: () => (
      <>
        <Pill>3</Pill>
        <LabelPill>api</LabelPill>
        <LabelPill selected>2 unread replies</LabelPill>
        <StatusPill>In progress</StatusPill>
      </>
    ),
  },
  {
    name: "PinButton, pinned, unpinned and quiet",
    node: () => (
      <>
        <PinButton label="Unpin CORE-1" onClick={() => undefined} pinned />
        <PinButton label="Pin CORE-1" onClick={() => undefined} pinned={false} />
        <PinButton label="Pin this turn" onClick={() => undefined} pinned={false} quiet />
      </>
    ),
  },
  {
    name: "RefusableButton, primary refused and compact secondary",
    node: () => (
      <>
        <RefusableButton refusal="Write a reason first.">Send</RefusableButton>
        <RefusableButton look="secondary-compact" refusal={null}>
          Attach
        </RefusableButton>
      </>
    ),
  },
  {
    name: "ApprovalChip, each state, and the Request changes dialog",
    node: () => (
      <>
        <ApprovalChip
          artifact={notesWith({ latest_version: 3, state: "awaiting" })}
          variant="header"
        />
        <ApprovalChip
          artifact={notesWith({ latest_version: 5, state: "stale", version: 3 })}
          size="compact"
          variant="header"
        />
        <ApprovalChip artifact={notesWith({ latest_version: 3, state: "approved", version: 3 })} />
      </>
    ),
    settle: async (view, user) => {
      const [requestChanges] = within(view.container).getAllByRole("button", {
        name: "Request changes",
      });
      if (requestChanges === undefined) throw new Error("no Request changes");
      await user.click(requestChanges);
      await within(view.container).findByRole("dialog", { name: "Request changes" });
    },
  },
  {
    name: "ApprovalChip, the Reviews dialog",
    node: () => (
      <ApprovalChip
        artifact={notesWith({ latest_version: 3, state: "changes_requested", version: 3 })}
      />
    ),
    outside: markdownBodies,
    settle: async (view, user) => {
      await user.click(within(view.container).getByRole("button", { name: "Changes requested" }));
      const dialog = await within(view.container).findByRole("dialog", { name: "Reviews" });
      await within(dialog).findByText("Please tighten the intro.");
      await settleReads();
    },
  },
  {
    name: "ConnectionDot, each state and a pending notice",
    node: () => (
      <>
        <ConnectionDot connection="connected" pending={undefined} />
        <ConnectionDot connection="connecting" pending={undefined} />
        <ConnectionDot connection="failed" pending={undefined} />
        <ConnectionDot
          connection="offline"
          pending={{ count: 2, readOnly: false, rebuiltAt: undefined, stored: true }}
        />
      </>
    ),
  },
  {
    name: "ProofDocument, two open decisions and a block's references",
    node: () => {
      stubs.listIssueAsks.mockResolvedValue([
        { ...blockAsk("ask-b1", "b-1", "Should we ship?"), last_reply: null },
        { ...blockAsk("ask-b2", "b-2", "Which region?"), last_reply: null },
      ]);
      return proofDocument();
    },
    outside: documentEditor,
    settle: async (view) => {
      await within(view.container).findByRole("navigation", { name: "Open decisions" });
      await within(view.container).findByRole("button", { name: "2 references on block" });
    },
  },
  {
    name: "ProofDocument, a history that cannot load",
    node: () => {
      stubs.getArtifactText.mockRejectedValue(
        new ApiError(409, { code: "DOCUMENT_UNLOADABLE", error: "The stored history cannot load." })
      );
      return proofDocument();
    },
    outside: documentEditor,
    settle: async (view) => {
      await within(view.container).findByRole("button", {
        name: "Rebuild from the latest version",
      });
    },
  },
  {
    name: "AskCard, an open blocking question with its session's handles open",
    node: () => {
      const ask = openAsk();
      return (
        <AskCard ask={ask} getAskThread={async () => threadOf(ask)} initialThread={threadOf(ask)} />
      );
    },
    outside: askCardChildren,
    settle: async (view, user) => {
      await settleReads();
      await user.click(
        within(view.container).getByRole("button", { expanded: false, name: /Implementer/ })
      );
      await within(view.container).findByRole("button", { name: /Copy session ID/ });
    },
  },
  {
    name: "AskCard, an open approval request, compact",
    node: () => {
      const ask = openApprovalAsk();
      return (
        <AskCard
          ask={ask}
          getAskThread={async () => threadOf(ask)}
          initialThread={threadOf(ask)}
          variant="compact"
        />
      );
    },
    outside: askCardChildren,
    settle: async () => {
      await settleReads();
    },
  },
  {
    name: "IssuePriorityLine, its own priority and an inherited one",
    node: () => (
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
    ),
    settle: async (view) => {
      await within(view.container).findByRole("link", { name: /Rocq and Lean/ });
    },
  },
  {
    name: "IssuePriorityLine, adding a priority",
    node: () => (
      <IssuePriorityLine
        disabled={false}
        issue={{ advances: [], advances_revision: 1, key: "CORE-1" }}
      />
    ),
    settle: async (view, user) => {
      await user.click(
        within(view.container).getByRole("button", { name: "Add a priority to CORE-1" })
      );
      await within(view.container).findByRole("combobox", { name: "Priority to advance" });
      await settleReads();
    },
  },
  {
    name: "IssuePriorityLine, in a row of Details",
    node: () => (
      <>
        <IssuePriorityLine
          disabled={false}
          inRow
          issue={{
            advances: [{ id: "rocq-lean", inherited: false }],
            advances_revision: 1,
            key: "CORE-1",
          }}
        />
        <IssuePriorityLine
          disabled={false}
          inRow
          issue={{ advances: [], advances_revision: 1, key: "CORE-2" }}
        />
      </>
    ),
    settle: async () => {
      await settleReads();
    },
  },
  {
    name: "AgentName, each state the registry reads and each shape",
    node: () => (
      <>
        <AgentName actor={{ id: LIVE_SESSION, kind: "session" }} />
        <AgentName actor={{ id: STALLED_SESSION, kind: "session" }} inline />
        <AgentName sessionId={GONE_SESSION} storedTitle="Old planner" />
        <AgentName
          actor={{ id: LIVE_SESSION, kind: "session" }}
          control={(name) => <button type="button">{name}</button>}
          kind="control"
        />
        <AgentName actor={{ id: "alice", kind: "user" }} />
        <AgentUnresponsiveNote lastSeen={Date.now() - 10 * 60_000} state="unresponsive" />
      </>
    ),
    settle: async (view) => {
      await waitFor(() =>
        expect(view.container.querySelector(`[data-agent-state="unresponsive"]`)).not.toBeNull()
      );
    },
  },
  {
    name: "AgentName and ClaimChip, with no registry read",
    node: () => {
      stubs.listAgents.mockRejectedValue(new Error("Envoy listener unreachable"));
      return (
        <>
          <AgentName actor={{ id: LIVE_SESSION, kind: "session" }} />
          <ClaimChip
            claim={{ actor: { id: LIVE_SESSION, kind: "session" }, at: "2026-09-24T06:00:00Z" }}
          />
        </>
      );
    },
    settle: async (view) => {
      await waitFor(() =>
        expect(view.container.querySelector(`[data-agent-state="unknown"]`)).not.toBeNull()
      );
      await settleReads();
    },
  },
  {
    name: "Timestamp, exact",
    node: () => <Timestamp at="2026-09-24T06:00:00Z" exact />,
    colourless: true,
  },
  {
    name: "ApprovalSteps, Details' size with versions and reasons",
    node: () => (
      <ApprovalSteps
        entries={stepEntries()}
        primaryArtifactId={SPEC_ID}
        versions={{ asks: [specRequest], issueKey: "CORE-1" }}
      />
    ),
    settle: async () => {
      await settleReads();
    },
  },
  {
    name: "ApprovalSteps, the header's compact size",
    node: () => (
      <ApprovalSteps
        entries={stepEntries()}
        primaryArtifactId={SPEC_ID}
        reasons={false}
        size="compact"
      />
    ),
  },
  {
    name: "AssigneeControl, both variants, assigned and not",
    node: () => (
      <>
        <AssigneeControl assignee="alice@example.com" issueKey="CORE-1" />
        <AssigneeControl assignee={null} issueKey="CORE-2" />
        <AssigneeControl assignee="alice@example.com" issueKey="CORE-3" variant="header" />
        <AssigneeControl assignee={null} issueKey="CORE-4" variant="header" />
      </>
    ),
  },
  {
    name: "PriorityControl and PriorityEditor, both variants, set and not",
    node: () => (
      <>
        <PriorityControl issueKey="CORE-1" priority={0} />
        <PriorityControl issueKey="CORE-2" priority={null} />
        <PriorityEditor issueKey="CORE-3" priority={1} variant="header" write={idlePriorityWrite} />
        <PriorityEditor
          issueKey="CORE-4"
          priority={null}
          variant="header"
          write={idlePriorityWrite}
        />
      </>
    ),
  },
  {
    name: "ClaimChip, held, lapsed, unresponsive and in a row of Details",
    node: () => (
      <>
        <ClaimChip
          claim={{ actor: { id: LIVE_SESSION, kind: "session" }, at: "2026-09-24T06:00:00Z" }}
        />
        <ClaimChip
          claim={{ actor: { id: GONE_SESSION, kind: "session" }, at: "2026-09-24T06:00:00Z" }}
        />
        <ClaimChip
          claim={{ actor: { id: STALLED_SESSION, kind: "session" }, at: "2026-09-24T06:00:00Z" }}
          liveness={{
            holder_state: "unresponsive",
            last_seen: minutesFromNow(-10),
            takeable_at: minutesFromNow(20),
          }}
        />
        <ClaimChip
          claim={{ actor: { id: STALLED_SESSION, kind: "session" }, at: "2026-09-24T06:00:00Z" }}
          liveness={{
            holder_state: "unresponsive",
            last_seen: minutesFromNow(-90),
            takeable_at: minutesFromNow(-30),
          }}
        />
        <ClaimChip
          claim={{ actor: { id: LIVE_SESSION, kind: "session" }, at: "2026-09-24T06:00:00Z" }}
          inRow
        />
      </>
    ),
    settle: async (view) => {
      await within(view.container).findByTestId("issue-claim-lapsed");
      await settleReads();
    },
  },
  {
    name: "GitHubLink, a pull request, a plain link and an unsafe one",
    node: () => (
      <>
        <GitHubLink link={{ url: "https://github.com/owner/repository/pull/7" }} />
        <GitHubLink link={{ url: "https://example.com/notes" }} />
        <GitHubLink link={{ url: "javascript:alert(1)" }} />
      </>
    ),
    settle: async (view) => {
      await within(view.container).findByText(/checks/);
    },
  },
  {
    name: "IssueComponentsLine, an explicit set, inherited, none and not attached",
    node: () => (
      <>
        <IssueComponentsLine
          issue={{
            components: {
              ids: ["dispatch-server"],
              inherited_from: "CORE-9",
              mode: "explicit",
              reason: null,
              unknown: ["old-queue"],
            },
            key: "CORE-1",
            project: "CORE",
            status: "todo",
            title: "Ship",
          }}
        />
        <IssueComponentsLine
          issue={{
            components: {
              ids: [],
              inherited_from: null,
              mode: "none",
              reason: "A process change",
              unknown: [],
            },
            key: "CORE-2",
            project: "CORE",
            status: "todo",
            title: "Ship",
          }}
        />
        <IssueComponentsLine
          issue={{
            components: {
              ids: [],
              inherited_from: null,
              mode: "inherit",
              reason: null,
              unknown: [],
            },
            key: "CORE-3",
            project: "CORE",
            status: "todo",
            title: "Ship",
          }}
        />
        <IssueComponentsLine
          inRow
          issue={{
            components: {
              ids: ["dispatch-server"],
              inherited_from: null,
              mode: "explicit",
              reason: null,
              unknown: [],
            },
            key: "CORE-4",
            project: "CORE",
            status: "todo",
            title: "Ship",
          }}
        />
      </>
    ),
    settle: async () => {
      await settleReads();
    },
  },
  {
    name: "IssueLabels, its chips and its picker open",
    node: () => (
      <IssueLabels
        disabled={false}
        labels={["api", "frontend"]}
        onSave={async () => undefined}
        project="CORE"
        saving={false}
      />
    ),
    settle: pressing("Edit labels"),
  },
  {
    name: "RepoControl, its own repository, an inherited one and none in a row of Details",
    node: () => (
      <>
        <RepoControl issueKey="CORE-1" project="CORE" repo="acme/api" resolvedRepo="acme/api" />
        <RepoControl issueKey="CORE-2" project="CORE" repo={null} resolvedRepo="acme/web" />
        <RepoControl inRow issueKey="CORE-3" project="CORE" repo={null} resolvedRepo={null} />
      </>
    ),
    settle: async () => {
      await settleReads();
    },
  },
  {
    name: "RouteReach, each marker",
    node: () => routeMarkers(),
  },
  {
    name: "SubscribedAgents, one removable and one subscribed more widely",
    node: () => (
      <SubscribedAgents
        onUnsubscribe={() => undefined}
        ownerLabel="CORE-1"
        subscribers={[
          subscriber(LIVE_SESSION, "Implementer", true),
          subscriber(STALLED_SESSION, "Reviewer", false),
        ]}
      />
    ),
    settle: async () => {
      await settleReads();
    },
  },
];

/** Every marker `RouteReach.tsx` draws, each by its test id. */
function routeMarkers(): ReactNode {
  return (
    <>
      <RouteReachUnknownMarker testId="route-unknown" />
      <UnreachableRouteMarker
        issue={{
          route: "role:architect",
          route_holder: null,
          route_status: "no_holder",
          route_status_reason: null,
        }}
        showRoute
        testId="route-nobody"
      />
      <UnreachableRouteMarker
        issue={{
          route: `session:${GONE_SESSION}`,
          route_holder: null,
          route_status: "no_holder",
          route_status_reason: null,
        }}
        showRoute
        testId="route-not-running"
      />
      <UnresponsiveRouteMarker
        issue={{
          route: "role:reviewer",
          route_holder: STALLED_SESSION,
          route_status: "holder_unresponsive",
          route_status_reason: null,
        }}
        showRoute
      />
    </>
  );
}

// ---------------------------------------------------------------------------------------------
// The cases
// ---------------------------------------------------------------------------------------------

// R6 of dispatch://INFRA-3935 (decision 56 of dispatch://INFRA-1196/artifact/part-decisions-md):
// inside `NoTooltips` every colour a file of R9 draws is one of B's page colours, the focus
// indicator excepted (departure 4, decision 58).
test("inside NoTooltips every colour is one of B's page colours", async () => {
  const offenders: string[] = [];
  const colourless: string[] = [];
  for (const mount of MOUNTS) {
    const { elements, view } = await drawMount(mount, "inside");
    try {
      const read = offPageColours(mount.name, elements);
      offenders.push(...read.offenders);
      if (read.colours === 0 && mount.colourless !== true) colourless.push(mount.name);
    } finally {
      undraw(view);
    }
  }
  expect(offenders).toEqual([]);
  // A mount that drew nothing would pass the walk above with nothing read.
  expect(colourless).toEqual([]);
});

// R6 names the version link: it is drawn inside `NoTooltips` alone, so it is the page's link, in
// the ink with its underline, and never `main`'s link blue.
test("inside NoTooltips ApprovalSteps' version link draws in B's page colours", async () => {
  const node = () => (
    <ApprovalSteps
      entries={stepEntries()}
      primaryArtifactId={SPEC_ID}
      versions={{ asks: [specRequest], issueKey: "CORE-1" }}
    />
  );
  const inside = await drawMount({ name: "ApprovalSteps", node }, "inside");
  try {
    const link = within(inside.view.container).getByRole("link", { name: "spec.md, version 1" });
    const drawn = classOf(link);
    expect(drawn).toEqual(expect.arrayContaining([...tokens(pageInkText), "underline"]));
    expect(drawn.filter((token) => tokens(linkText).includes(token))).toEqual([]);
  } finally {
    undraw(inside.view);
  }

  const outside = await drawMount({ name: "ApprovalSteps", node }, "outside");
  try {
    expect(
      within(outside.view.container).queryByRole("link", { name: "spec.md, version 1" })
    ).toBeNull();
  } finally {
    undraw(outside.view);
  }
});

// R6 names RouteReach's markers: inside `NoTooltips` each is the page's warning, its waiting
// orange, and none keeps `main`'s callout or muted grey; outside, each keeps `main`'s.
test("inside NoTooltips RouteReach's markers draw in B's page colours", async () => {
  const markers = (view: RenderResult) => {
    const scope = within(view.container);
    return {
      nobody: scope.getByTestId("route-nobody"),
      notRunning: scope.getByTestId("route-not-running"),
      unknown: scope.getByTestId("route-unknown"),
      unresponsive: scope.getByTestId("issue-route-unresponsive"),
    };
  };
  const mainsTokens = tokens(
    `${calloutWarningBg} ${calloutWarningBorder} ${calloutWarningText} ${textMutedOnSurface}`
  );

  const inside = await drawMount({ name: "RouteReach", node: routeMarkers }, "inside");
  try {
    const drawn = markers(inside.view);
    for (const chip of [drawn.nobody, drawn.notRunning]) {
      expect(classOf(chip)).toEqual(expect.arrayContaining(tokens(pageWaitingChip)));
    }
    for (const words of [drawn.unknown, drawn.unresponsive]) {
      expect(classOf(words)).toEqual(expect.arrayContaining(tokens(pageStateText.waiting)));
    }
    for (const marker of Object.values(drawn)) {
      expect(classOf(marker).filter((token) => mainsTokens.includes(token))).toEqual([]);
    }
  } finally {
    undraw(inside.view);
  }

  const outside = await drawMount({ name: "RouteReach", node: routeMarkers }, "outside");
  try {
    const drawn = markers(outside.view);
    for (const chip of [drawn.nobody, drawn.notRunning]) {
      expect(classOf(chip)).toEqual(
        expect.arrayContaining(
          tokens(`${calloutWarningBorder} ${calloutWarningBg} ${calloutWarningText}`)
        )
      );
    }
    for (const words of [drawn.unknown, drawn.unresponsive]) {
      expect(classOf(words)).toEqual(expect.arrayContaining(tokens(textMutedOnSurface)));
    }
    for (const marker of Object.values(drawn)) {
      expect(classOf(marker).filter((token) => token.includes("page-"))).toEqual([]);
    }
  } finally {
    undraw(outside.view);
  }
});
