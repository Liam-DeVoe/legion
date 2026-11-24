import { z } from "zod";
import {
  DISPATCH_TOOL_DEADLINE_MS,
  DispatchClient,
  DispatchServiceError,
} from "../../../packages/envoy-client/src/dispatch-http";
import {
  type Category,
  type DispatchConfig,
  errorCategory,
  HttpStatusError,
} from "../production-check-runtime.ts";

/**
 * Every Dispatch route the tool calls, behind two interfaces: `DispatchReads`, which report mode
 * is limited to and every request of which is a GET, and `DispatchWrites`. Each read answers the
 * JSON it received, and the schemas below are how the rest of the tool reads those answers. A
 * read that fails throws `ReadError`, naming the route's pattern and a fixed category, never the
 * path it was sent to or the answer's text.
 */

const Actor = z.looseObject({ kind: z.string(), id: z.string().optional() });
export type Actor = z.infer<typeof Actor>;

export const Project = z.looseObject({ key: z.string() });

/** One row of `GET /api/v1/issues` (an `IssueSummary`), as the tool reads it. */
export const IssueRow = z.looseObject({
  key: z.string(),
  title: z.string(),
  status: z.string(),
  closed_at: z.string().nullable(),
  last_seq: z.int(),
  parent: z.string().nullable().optional(),
  created_by: Actor.nullable().optional(),
});
export type IssueRow = z.infer<typeof IssueRow>;

export const Version = z.looseObject({
  number: z.int(),
  authors: z.array(Actor).nullable(),
  summary: z.string().nullable().optional(),
});
export type Version = z.infer<typeof Version>;

export const Approval = z.looseObject({
  state: z.string(),
  kind: z.string().optional(),
  waiting_on: z.string().optional(),
});
export type Approval = z.infer<typeof Approval>;

/** One document of `GET /api/v1/issues/{key}/artifacts` or of a project's documents. */
export const Artifact = z.looseObject({
  id: z.string(),
  issue_key: z.string().nullable(),
  project: z.string(),
  slug: z.string(),
  name: z.string(),
  kind: z.string(),
  /** Whether the document is its issue's primary document, its spec. */
  primary: z.boolean().optional(),
  versions: z.array(Version).nullable(),
  approval: Approval.optional(),
});
export type Artifact = z.infer<typeof Artifact>;

/** `GET /api/v1/issues/{key}`: the row, its project and its documents. */
export const IssueDetails = IssueRow.extend({
  project: z.string(),
  artifacts: z.array(Artifact).nullable(),
});

const Attribute = z.looseObject({ name: z.string(), value: z.string() });

/** One block of a text read with `?blocks=true`; `from` and `to` are UTF-8 byte offsets into the
 *  markdown of the same answer. */
export const Block = z.looseObject({
  id: z.string(),
  type: z.string(),
  from: z.int(),
  to: z.int(),
  token: z.string(),
  attributes: z.array(Attribute).optional(),
});
export type Block = z.infer<typeof Block>;

/** `GET /api/v1/artifacts/{id}/text?blocks=true`. */
export const Text = z.looseObject({
  markdown: z.string(),
  token: z.string(),
  version: z.int().nullable(),
  blocks: z.array(Block).optional(),
});

/** `GET /api/v1/artifacts/{id}/versions/{n}?blocks=true`. Dispatch lists a past version's typed
 *  blocks alone (`docs.VersionBlocks`): a paragraph has no id in a stored version. */
export const VersionText = z.looseObject({
  markdown: z.string(),
  number: z.int(),
  authors: z.array(Actor).nullable(),
  blocks: z.array(Block).optional(),
});
export type VersionText = z.infer<typeof VersionText>;

/** One event of an issue's events or of the whole log. */
export const Event = z.looseObject({
  id: z.int(),
  issue_key: z.string().nullable(),
  artifact_id: z.string().nullable(),
  project: z.string(),
  seq: z.int(),
  type: z.string(),
  actor: Actor,
  created_at: z.string(),
  payload: z.unknown(),
});
export type Event = z.infer<typeof Event>;

/** What `GET /api/v1/whoami` answers. */
export const Whoami = z.discriminatedUnion("kind", [
  z.looseObject({ kind: z.literal("user"), login: z.string() }),
  z.looseObject({
    kind: z.literal("agent"),
    owner: z.string().nullable(),
    project: z.string().nullable(),
  }),
]);
export type Whoami = z.infer<typeof Whoami>;

/** `GET /api/v1/projects/{key}/legion/daemon`: the daemon's state address, or null. */
export const DaemonSetting = z.looseObject({ state_url: z.string() }).nullable();

/** Who owns an item's events: an issue (its title and its documents), or a project document,
 *  which has a topic of its own. */
export type Owner =
  | { readonly kind: "issue"; readonly key: string }
  | {
      readonly kind: "document";
      readonly project: string;
      readonly slug: string;
      readonly id: string;
    };

export function ownerName(owner: Owner): string {
  return owner.kind === "issue" ? owner.key : `${owner.project}/${owner.slug}`;
}

/** Every read the tool makes of Dispatch. Each is a GET. */
export interface DispatchReads {
  /** `GET /healthz`, sent no credential, read on 200 and on 503. */
  healthz(): Promise<unknown>;
  /** `GET /api/v1/whoami`. */
  whoami(): Promise<unknown>;
  /** `GET /api/v1/projects`. */
  projects(): Promise<unknown>;
  /** `GET /api/v1/issues?project=<K>&open=true`, unpaged; every status with `includeClosed`. */
  issues(project: string, includeClosed: boolean): Promise<unknown>;
  /** `GET /api/v1/issues?parent=<KEY>`: the issue's children, whatever their status. */
  children(parent: string): Promise<unknown>;
  /** `GET /api/v1/issues/{key}`. */
  issue(key: string): Promise<unknown>;
  /** `GET /api/v1/issues/{key}/artifacts`: every document, its versions and its approval. */
  issueArtifacts(key: string): Promise<unknown>;
  /** `GET /api/v1/projects/{key}/artifacts?unlinked=true`: the project's own documents. */
  projectDocuments(project: string): Promise<unknown>;
  /** `GET /api/v1/artifacts/{id}/text?blocks=true`. */
  text(artifactId: string): Promise<unknown>;
  /** `GET /api/v1/artifacts/{id}/versions/{n}?blocks=true`. */
  version(artifactId: string, version: number): Promise<unknown>;
  /** `GET /api/v1/issues/{key}/events?after=&limit=`. */
  issueEvents(key: string, after: number, limit: number): Promise<unknown>;
  /** `GET /api/v1/projects/{key}/legion/daemon`. */
  daemonSetting(project: string): Promise<unknown>;
  /** `GET /api/v1/events/log?after=&limit=`. */
  eventLog(after: number, limit: number): Promise<unknown>;
  /** `GET /api/v1/projects/{key}/artifacts/{slug}/text`, or null when no such document is. */
  stateText(project: string, slug: string): Promise<unknown>;
}

/** One `replace` of a document edit. `occurrence` is zero-based and names which match of `find`
 *  is meant; it is left out where `find` matches once. */
export interface ReplaceOp {
  readonly op: "replace";
  readonly find: string;
  readonly with: string;
  readonly occurrence?: number;
}

/** What a write came back with: Dispatch's 2xx answer, or its refusal's status and code. A write
 *  that may or may not have landed, or that reached no server, is `code: "unsettled"`. */
export type WriteOutcome =
  | { readonly ok: true; readonly answer: unknown }
  | { readonly ok: false; readonly status: number; readonly code: string };

/** Every write the tool makes, each acting as the session the process runs as. */
export interface DispatchWrites {
  /** `POST /api/v1/artifacts/{id}/edits`, with the token read as the precondition. */
  edit(
    artifactId: string,
    ops: readonly ReplaceOp[],
    token: string,
    summary: string
  ): Promise<WriteOutcome>;
  /** `PATCH /api/v1/issues/{key}` with `title` and `expected_sequence`. */
  retitle(key: string, title: string, expectedSequence: number): Promise<WriteOutcome>;
  /** `POST /api/v1/projects/{key}/artifacts`: the state document, over the token read (none for
   *  its first upload). */
  uploadState(
    project: string,
    name: string,
    content: string,
    token: string | null
  ): Promise<WriteOutcome>;
}

/** A read that failed: the route's pattern and a fixed category. */
export class ReadError extends Error {
  override readonly name = "ReadError";

  constructor(
    readonly route: string,
    readonly category: Category
  ) {
    super(`${route} ${category}`);
  }
}

/** `attempt`, with a failure named by `route`. */
async function named<T>(route: string, attempt: () => Promise<T>): Promise<T> {
  try {
    return await attempt();
  } catch (error) {
    if (error instanceof ReadError) throw error;
    throw new ReadError(route, errorCategory(error));
  }
}

const segment = encodeURIComponent;

/** The outcome of a write that threw: Dispatch's refusal, or `unsettled`. */
function refusal(error: unknown): WriteOutcome {
  if (error instanceof DispatchServiceError) {
    return { ok: false, status: error.status, code: error.code };
  }
  return { ok: false, status: 0, code: "unsettled" };
}

async function written(write: () => Promise<unknown>): Promise<WriteOutcome> {
  try {
    return { ok: true, answer: await write() };
  } catch (error) {
    return refusal(error);
  }
}

/**
 * The reads and writes against the Dispatch `config` names, every request through `fetchImpl`
 * (the logging fetch). Each call gets a client of its own, since a client's deadline runs from its
 * construction. Reads the client has no method for go around it with the credential's own
 * headers, as `ask-census.ts`'s `get` does.
 */
export function liveDispatch(
  config: DispatchConfig,
  fetchImpl: typeof fetch
): { reads: DispatchReads; writes: DispatchWrites } {
  const client = () => new DispatchClient(config.url, config.credential, fetchImpl);
  // An empty DISPATCH_SESSION_ID names no session, as an unset one does.
  const actor = { kind: "session", id: process.env.DISPATCH_SESSION_ID || "unknown" };

  /** One read around the client: the parsed body for a status `accept` admits, or null for 404
   *  where `absent` says a missing thing is an answer. */
  async function get(
    path: string,
    options: { credential?: boolean; accept?: readonly number[]; absent?: readonly number[] } = {}
  ): Promise<unknown> {
    const url = `${config.url}${path}`;
    const deadline = AbortSignal.timeout(DISPATCH_TOOL_DEADLINE_MS);
    const credential =
      options.credential === false
        ? {}
        : await config.credential.headers("GET", url, undefined, deadline);
    const response = await fetchImpl(url, {
      headers: { Accept: "application/json", ...credential },
      signal: deadline,
    });
    if (options.absent?.includes(response.status)) {
      await response.body?.cancel();
      return null;
    }
    if (!(options.accept ?? [200]).includes(response.status)) {
      await response.body?.cancel();
      throw new HttpStatusError(response.status);
    }
    const text = await response.text();
    try {
      return JSON.parse(text);
    } catch {
      return text;
    }
  }

  const reads: DispatchReads = {
    healthz: () =>
      named("GET /healthz", () => get("/healthz", { credential: false, accept: [200, 503] })),
    whoami: () => named("GET /api/v1/whoami", () => client().whoami()),
    projects: () => named("GET /api/v1/projects", () => client().listProjects()),
    issues: (project, includeClosed) =>
      named("GET /api/v1/issues", () =>
        get(`/api/v1/issues?project=${segment(project)}${includeClosed ? "" : "&open=true"}`)
      ),
    children: (parent) =>
      named("GET /api/v1/issues", () => get(`/api/v1/issues?parent=${segment(parent)}`)),
    issue: (key) => named("GET /api/v1/issues/{key}", () => client().getIssue(key)),
    issueArtifacts: (key) =>
      named("GET /api/v1/issues/{key}/artifacts", () => client().listIssueArtifacts(key)),
    projectDocuments: (project) =>
      named("GET /api/v1/projects/{key}/artifacts", () =>
        client().listProjectArtifacts(project, true)
      ),
    text: (id) => named("GET /api/v1/artifacts/{id}/text", () => client().docReadWithBlocks(id)),
    version: (id, version) =>
      named("GET /api/v1/artifacts/{id}/versions/{number}", () =>
        get(`/api/v1/artifacts/${segment(id)}/versions/${version}?blocks=true`)
      ),
    issueEvents: (key, after, limit) =>
      named("GET /api/v1/issues/{key}/events", () => client().getIssueEvents(key, after, limit)),
    daemonSetting: (project) =>
      named("GET /api/v1/projects/{key}/legion/daemon", () =>
        get(`/api/v1/projects/${segment(project)}/legion/daemon`)
      ),
    eventLog: (after, limit) =>
      named("GET /api/v1/events/log", () =>
        get(`/api/v1/events/log?after=${after}&limit=${limit}`)
      ),
    stateText: (project, slug) =>
      named("GET /api/v1/projects/{key}/artifacts/{slug}/text", () =>
        get(`/api/v1/projects/${segment(project)}/artifacts/${segment(slug)}/text`, {
          absent: [404],
        })
      ),
  };

  const writes: DispatchWrites = {
    edit: (artifactId, ops, token, summary) =>
      written(() =>
        client().docEdit(artifactId, {
          actor,
          ops: ops.map((op) => ({ ...op })),
          precondition: { document: token },
          summary,
        })
      ),
    retitle: (key, title, expectedSequence) =>
      written(() =>
        client().updateIssue(key, { actor, title, expected_sequence: expectedSequence })
      ),
    uploadState: (project, name, content, token) =>
      written(() =>
        client().projectArtifact(project, {
          actor,
          name,
          content,
          ...(token === null ? {} : { precondition: { document: token } }),
        })
      ),
  };
  return { reads, writes };
}
