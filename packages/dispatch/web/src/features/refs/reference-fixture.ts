import type { GraphNode } from "../../__tests__/graph-fixture";
import type { WholeList } from "../../api/client";
import type {
  ArtifactV2,
  AskThread,
  AskV2,
  AskWithThread,
  GraphEdge,
  IssueReadV2,
} from "../../api/types";

/** One document as an issue's own read lists it (`artifacts`) and a project's or a session's
 *  list answers it: what a reference to it shows - its name, kind, latest version and whether it
 *  is the spec - with the fields every row carries beside them. */
export function documentRow(
  fields: Partial<ArtifactV2> & Pick<ArtifactV2, "id" | "name" | "slug">
): ArtifactV2 {
  return {
    created_at: "2026-09-01T00:00:00Z",
    created_by: { id: "alice", kind: "user" },
    issue_key: null,
    kind: "doc",
    latest_version: 1,
    latest_version_at: "2026-09-01T00:00:00Z",
    latest_version_size: null,
    primary: false,
    project: "CORE",
    ref_key: fields.slug,
    session_id: null,
    unsettled: null,
    version_count: 1,
    ...fields,
  };
}

/** An issue's own read with nothing included (`api.getIssueSummary`): what a reference to the
 *  issue, or to one of its documents, shows - its title, its status and its documents - with the
 *  fields every issue read carries beside them. */
export function issueSummary(
  fields: Partial<IssueReadV2> & Pick<IssueReadV2, "key" | "title">
): IssueReadV2 {
  const [project = "", number = "0"] = fields.key.split("-");
  return {
    advances: [],
    advances_revision: 0,
    artifacts: [],
    assignee: null,
    blocked_by: [],
    claim: null,
    closed_at: null,
    components: { ids: [], inherited_from: null, mode: "inherit", reason: null, unknown: [] },
    created_at: "2026-09-01T00:00:00Z",
    created_by: { id: "alice", kind: "user" },
    decisions_artifact_id: "",
    external_links: [],
    labels: [],
    last_seq: 0,
    number: Number(number),
    parent: null,
    plan_artifact_id: "",
    primary_artifact_id: fields.artifacts?.find((document) => document.primary)?.id ?? "",
    priority: null,
    project,
    rank: "U",
    repo: null,
    resolved_repo: null,
    route: null,
    status: "todo",
    updated_at: "2026-09-01T00:00:00Z",
    verification_artifact_id: "",
    ...fields,
  };
}

/** One ask as every v2 list and read answers it: flat, with the fields a row always carries and
 *  whatever the caller names on top. */
export function askRow(fields: Partial<AskV2> & Pick<AskV2, "id">): AskV2 {
  return {
    anchor: null,
    answer: null,
    artifact_id: null,
    asked_of: null,
    asked_of_at: null,
    asked_of_by: null,
    author: { id: "planner", kind: "session" },
    block_id: null,
    created_at: "2026-09-09T00:00:00Z",
    document: null,
    edited_at: null,
    issue: null,
    issue_key: "CORE-1",
    kind: "question",
    last_reply: null,
    multiple: false,
    opened_event_id: 1,
    options: [],
    priority: null,
    question: "Which way round?",
    ref: `dispatch://CORE-1/ask/${fields.id}`,
    state: "open",
    urgency: "med",
    ...fields,
  };
}

/** One ask read with its thread (`api.getAsk`, `include=thread`): the ask flat, its replies,
 *  rewordings, answers, followers, claimant and route under `thread`. */
export function askWithThread(
  fields: Partial<AskV2> & Pick<AskV2, "id">,
  thread: Partial<AskThread> = {}
): AskWithThread {
  return {
    ...askRow(fields),
    thread: {
      answers: [],
      claimant: null,
      edits: [],
      followers: [],
      replies: [],
      route: null,
      route_holder: null,
      route_status: null,
      route_status_reason: null,
      ...thread,
    },
  };
}

/** The graph read of a reference, every page (`api.getReferences`): the node it names and the
 *  edges pointing at it. */
export function referencesRead(
  node: GraphNode,
  edges: GraphEdge[] = []
): WholeList<"/api/v2/references"> {
  return { items: edges, limit: 50, node, offset: 0, total: edges.length };
}

/** The question map of the document one source's text belongs to, every page
 *  (`api.getReferenceQuestions`): one ask row per question block it declares, `document` the
 *  document's id. */
export function questionsRead(items: AskV2[], document = "doc-1"): WholeList<"/api/v2/asks"> {
  const human = items.filter((ask) => ask.waiting_on === "human").length;
  return {
    as_of: "2026-09-01T00:00:00Z",
    count: items.length,
    document,
    items,
    limit: 50,
    offset: 0,
    total: items.length,
    waiting_on_agent: items.length - human,
    waiting_on_human: human,
  };
}
