/**
 * The row of each keyed write of `DispatchClient` (each POST and PATCH): what the method's callers
 * read of its 2xx answer, which `#writtenRow` checks before any caller reads it. Dispatch answers a
 * write's 2xx only after the write committed, so an answer that is not this row is a landing whose
 * answer was lost (`ANSWER_LOST`), never a failure of the call.
 *
 * A row names only members a caller reads, and each in the form the route answers it in the
 * OpenAPI document the server generates (`packages/contracts/schemas/dispatch-openapi.json`):
 * required where the route always answers it, nullable where the route may answer null. A member a
 * caller reads only behind a guard of its own (`??`, `=== undefined`, an explicit refusal) is
 * named as optional: its absence is what that caller already handles, and a row stricter than
 * Dispatch's true answer would turn a good answer into `ANSWER_LOST`. A row keeps every member it
 * does not name, at every depth (`z.looseObject`), and the caller reads the answer as it came.
 * `__tests__/dispatch-write-rows.test.ts` drives every method against the least and the most
 * answer its route declares, and holds every row to that rule.
 *
 * Where `@legion/contracts` declares a member in the form the route answers, the row takes that
 * declaration (`issueFields`, `askFields`, `commentFields`, `messageFields`, `artifactFields`,
 * `versionFields`, `ArchitectureSourceSchema`, `ExternalLinkSchema`, `InboundActorSchema`).
 * Those declarations are the bus payloads' rows, and where a payload's form is not the route's (a
 * list the route answers as `null` when empty, a closed set of words the route answers as any
 * string), the row writes the route's form. No `@legion/contracts` schema holds a whole write's
 * answer: the answer types are generated from the OpenAPI document as TypeScript types alone.
 */
import {
  ArchitectureSourceSchema,
  artifactFields,
  askFields,
  commentFields,
  ExternalLinkSchema,
  InboundActorSchema,
  issueFields,
  messageFields,
  versionFields,
} from "@legion/contracts";
import { z } from "zod";

/** The components of an issue as `architectureGuidance` and `componentsChange` read them. The
 *  route answers `ids` as `null` when empty, where the payload's `IssueComponentsSchema` has a
 *  list, and `mode` as any string. */
const components = z.looseObject({
  mode: z.string(),
  ids: z.array(z.string()).nullish(),
  reason: z.string().nullish(),
});

/** A write's `advice`, as `renderAdvice` reads it (`dispatch-commands/advice.ts`) where no `try`
 *  of the caller's own catches what it throws; the route answers `WriteAdvice` on an issue's
 *  write and `DocumentWriteAdvice` on a project document's, which carries no `issue_status`,
 *  `session_writes_since_human` or `your_open_asks`. `decision_blocks` is read only as
 *  `=== 0`, which an absent count leaves unprinted. Its `suggestions`, `declined_asks` and
 *  `decision_block_checks` are read inside a `try` that prints a line of its own for any shape
 *  (`related-check.ts`), so the row names none of them. */
const advice = z
  .looseObject({
    decision_blocks: z.number().int().optional(),
    issue_status: z.string().optional(),
    legion_managed_status: z.boolean().optional(),
    session_writes_since_human: z.number().int().optional(),
    your_open_asks: z
      .array(z.looseObject({ id: z.string(), question: z.string() }))
      .nullable()
      .optional(),
    unparsed_openers: z
      .looseObject({ count: z.number().int(), examples: z.array(z.string()).nullable() })
      .optional(),
    moved_approval_request: z
      .looseObject({
        ask_id: z.string(),
        issue: z.string().optional(),
        project: z.string().optional(),
        documents: z
          .array(
            z.looseObject({
              artifact_id: z.string(),
              name: z.string(),
              primary: z.boolean(),
              role: z.string().optional(),
              requested_version: z.number().int(),
              version: z.number().int(),
            })
          )
          .nullable(),
      })
      .optional(),
    superseded_approval: z
      .looseObject({
        version: z.number().int(),
        approved_version: z.number().int(),
        approved_by: z.string(),
        design_gates: z.array(z.looseObject({ issue: z.string(), step: z.string() })).optional(),
      })
      .optional(),
  })
  .optional();

/** The advice of a write whose caller renders it inside `otherAdviceLines`, which prints one line
 *  of its own for advice it cannot show (`issueCommand`, `askCommand`, `docEditCommand`): that
 *  `try` is the caller's guard, so the row names the member and none of its members. */
const caughtAdvice = z.unknown().optional();

/** An ask as `askCommand`, `editAskCommand` and `askOwnerDetails` read it. */
const ask = {
  id: askFields.id,
  issue_key: askFields.issue_key,
  artifact_id: askFields.artifact_id,
};

/** An ask's write: `askCommand` reads the question, the urgency, whom it waits on and the advice
 *  too. The route answers `urgency` as any string, where the payload has the set of urgencies. */
const askWrite = z.looseObject({
  ...ask,
  question: askFields.question,
  urgency: z.string(),
  waiting_on_person: z.string().optional(),
  advice: caughtAdvice,
});

/** A comment's write as `commentCommand` reads it on a document of a project, where the details
 *  name the document rather than an issue. */
const documentComment = {
  id: commentFields.id,
  ask_waiting_on: z.string().optional(),
  advice,
};

/** A suggestion as `suggestCommand` reads it: its id alone. */
const suggestion = z.looseObject({ id: commentFields.id });

/** One architecture source as `architectureSyncLines` reads it; the payload's declaration is the
 *  route's form. */
const {
  repo,
  branch,
  last_error,
  last_commit,
  last_sync_at,
  components: sourceComponents,
} = ArchitectureSourceSchema.unwrap().shape;
const architectureSource = z.looseObject({
  repo,
  branch,
  last_error,
  last_commit,
  last_sync_at,
  components: sourceComponents,
});

/** An upload's stored document and version, as `textWithPictures` reads them for a picture. */
const pictureUpload = {
  artifact: z.looseObject({ slug: artifactFields.slug }),
  version: z.looseObject({ number: versionFields.number }),
};

/** An upload's stored document as `artifactCommand` reads it, the picture's `slug` among its
 *  members. `primary` is read as `primary || name === "spec.md"`, which an absent flag leaves to
 *  the name. The route answers `role` as any string, where the payload has the set of roles. */
const uploadedDocument = {
  id: artifactFields.id,
  slug: artifactFields.slug,
  name: artifactFields.name,
  primary: artifactFields.primary.optional(),
  role: z.string().optional(),
  issue_key: artifactFields.issue_key.optional(),
};

/** The person or session a review or a retraction names, as `actorText` reads it. */
const actor = InboundActorSchema;

/** The rows of the POSTs that store nothing, which the route table says of each
 *  (`packages/envoy/internal/dispatch/api/routes_table.go`): Dispatch's parse and render of a
 *  text (`POST /api/v1/markdown/round-trip`, "The route writes nothing and reads no row") and the
 *  related-item check (`POST /api/v1/projects/{key}/related`, "writing nothing"). */
const STORE_NOTHING = new WeakSet<z.ZodType>();

/** `row`, marked as the row of a route that stores nothing (`rowStores`). */
function storesNothing<Row extends z.ZodType>(row: Row): Row {
  STORE_NOTHING.add(row);
  return row;
}

/** Whether the route whose 2xx `row` reads stores what it is sent. A 2xx of one that stores
 *  nothing and gives no answer made nothing, so it is no landing (`DispatchClient.#writtenRow`). */
export function rowStores(row: z.ZodType): boolean {
  return !STORE_NOTHING.has(row);
}

/**
 * One value a write's call sent that the route's answer names back: the segment of the call's
 * path at index `segment` (the key or id of what the call is about; the path starts at `api`),
 * or the member `member` of its JSON body, and the member of the answer at `at` that holds it (a
 * number in `at` is an item of a list). Each is a key, an id, a commit, a word of a closed set or
 * a number, which Dispatch stores as it was sent, up to the white space around it that it trims.
 * No text is held: Dispatch rewrites a text it stores (it trims it, and folds the line ends of a
 * body), so the answer to the call's own write may hold other text than it sent.
 */
export type Held =
  | { readonly segment: number; readonly at: readonly (string | number)[] }
  | { readonly member: string; readonly at: readonly (string | number)[] };

const HELD = new WeakMap<z.ZodType, readonly Held[]>();

/** A copy of `row` whose answer names back the values `held` says (`rowHolds`). It is a copy, so
 *  two methods that read one row each name their own values. */
function holding<Row extends z.ZodType>(row: Row, ...held: Held[]): Row {
  const copy = row.clone() as Row;
  HELD.set(copy, held);
  return copy;
}

/** The values a write's call sent that the answer `row` reads names back (`holding`), none for a
 *  row that declares none. */
export function heldBy(row: z.ZodType): readonly Held[] {
  return HELD.get(row) ?? [];
}

/** What a write call sent: the segments of its path and its JSON body, where it sent one. */
export interface SentWrite {
  readonly path: readonly string[];
  readonly body?: unknown;
}

/**
 * Whether `answer` holds each value `sent` gave that `row`'s answer names back (`heldBy`): a
 * value the call did not set (absent, null or empty) is not read. A 2xx that names no key back
 * and gives the row is the answer of a Dispatch with no keyed layer only where it holds them; one
 * that holds another value is another call's answer (`DispatchClient.#writtenRow`).
 */
export function rowHolds(row: z.ZodType, sent: SentWrite, answer: unknown): boolean {
  return heldBy(row).every((held) => {
    const value = "segment" in held ? sent.path[held.segment] : memberOf(sent.body, held.member);
    if (value === undefined || value === null || value === "") return true;
    const answered = held.at.reduce<unknown>(memberOf, answer);
    return (
      (typeof answered === "string" || typeof answered === "number") &&
      String(answered) === String(value).trim()
    );
  });
}

/** The member `name` of `node`, or the item at `name` where `node` is a list. */
function memberOf(node: unknown, name: string | number): unknown {
  return typeof node === "object" && node !== null
    ? (node as Record<string | number, unknown>)[name]
    : undefined;
}

/** A priority an issue advances and a link it carries, in the payload's forms (`issueFields`,
 *  `ExternalLinkSchema`), whose items refuse a member they do not name: here each keeps it, as
 *  every row does. */
const advance = z.looseObject(issueFields.advances.unwrap().unwrap().element.unwrap().shape);
const externalLink = z.looseObject(ExternalLinkSchema.unwrap().shape);

/** An issue as `issueUpdateCommand` reads the answer of its PATCH (`WRITE_ROWS.updateIssue`). */
const updatedIssue = z.looseObject({
  key: issueFields.key,
  status: issueFields.status,
  title: issueFields.title.optional(),
  priority: issueFields.priority.optional(),
  assignee: issueFields.assignee.optional(),
  route: issueFields.route.optional(),
  parent: issueFields.parent.optional(),
  components: components.optional(),
  repo: issueFields.repo.optional(),
  resolved_repo: issueFields.resolved_repo.optional(),
  labels: issueFields.labels.nullish(),
  blocked_by: issueFields.blocked_by.nullish(),
  advances: z.array(advance).nullable().optional(),
  external_links: z.array(externalLink).nullish(),
  advice,
});

/**
 * Each row of a write whose answer names back what its call sent (`holding`) declares those
 * values: the key or id its path names, and the key, commit, word or number of its body that
 * the answer holds as it was sent. Six rows declare none: the round trip and the related-item
 * check store nothing, and answer a text Dispatch renders and what it found for one; an agent's
 * message names its session only in the deliveries it made, which may be none; no caller reads the
 * answer of a delivery's reply or of a named version; and a document edit's answer names no
 * document.
 */
export const WRITE_ROWS = {
  /** `issueCommand` reads `project` on a created issue's check lines (`relatedCheckLines`, which
   *  prints it where no check was made), so the row names it. It reads `components` only inside
   *  `otherAdviceLines`, whose `try` prints a line of its own, so the row names none of it. */
  issue: holding(
    z.looseObject({
      key: issueFields.key,
      title: issueFields.title,
      project: issueFields.project,
      assignee: issueFields.assignee.optional(),
      repo: issueFields.repo.optional(),
      advice: caughtAdvice,
    }),
    { member: "project", at: ["project"] }
  ),
  markdownRoundTrip: storesNothing(z.looseObject({ markdown: z.string() })),
  related: storesNothing(z.looseObject({ outcome: z.string() })),
  /** `issueUpdateCommand` reads the key and the status on every path, and each other member only
   *  where the call wrote it, so those are optional. */
  updateIssue: holding(updatedIssue, { segment: 3, at: ["key"] }),
  /** The same write where the call wrote `components`: `componentsChange` reads them with no guard
   *  of its own, so this row requires them (`DispatchClient.updateIssue` picks it). */
  updateIssueComponents: holding(updatedIssue.extend({ components }), { segment: 3, at: ["key"] }),
  claimIssue: holding(
    z.looseObject({
      status: issueFields.status,
      claim: z.looseObject({ at: z.string() }).nullable(),
      legion_managed_status: z.boolean().optional(),
    }),
    { segment: 3, at: ["key"] }
  ),
  ask: holding(askWrite, { segment: 3, at: ["issue_key"] }),
  artifactAsk: holding(askWrite, { segment: 3, at: ["artifact_id"] }),
  editAsk: holding(z.looseObject({ ...ask, question: askFields.question }), {
    segment: 3,
    at: ["id"],
  }),
  resolveAsk: holding(
    z.looseObject({
      ...ask,
      resolution: z.looseObject({ reason: z.string() }).optional(),
    }),
    { segment: 3, at: ["id"] }
  ),
  createProject: holding(
    z.looseObject({
      key: z.string(),
      name: z.string(),
      created_at: z.string(),
      default_assignee: z.string().optional(),
    }),
    { member: "key", at: ["key"] }
  ),
  /** Dispatch answers a project with no source 404 `SOURCE_NOT_FOUND`, so a 2xx holds one. */
  syncArchitectureSources: holding(z.array(architectureSource), {
    segment: 3,
    at: [0, "project"],
  }),
  syncArchitectureSource: holding(architectureSource, { segment: 3, at: ["project"] }),
  /** `requestApprovalCommand` reads `approval` where the answer holds no ask, which the route
   *  always answers, and its `by` through `?.`. */
  requestApproval: holding(
    z.looseObject({
      version: z.number().int(),
      coverage: z.array(z.string()).nullish(),
      coverage_unread: z.boolean().optional(),
      ask: z
        .looseObject({
          ...ask,
          question: askFields.question,
          waiting_on_person: z.string().optional(),
        })
        .nullable(),
      approval: z.looseObject({ by: z.looseObject({ id: z.string() }).nullish() }),
    }),
    { segment: 3, at: ["artifact_id"] }
  ),
  planApprove: holding(
    z.looseObject({ version: z.number().int() }),
    { segment: 3, at: ["artifact_id"] },
    { member: "version", at: ["version"] }
  ),
  reviewArtifact: holding(
    z.looseObject({
      version: z.number().int(),
      state: z.string(),
      ask_id: z.string().nullable(),
      reason: z.string().nullable(),
      actor,
    }),
    { segment: 3, at: ["artifact_id"] },
    { segment: 5, at: ["version"] }
  ),
  /** The check the call names may be a block reference whose block id the answer holds, so the
   *  check is not held; the issue the result is recorded on, its verdict and its commit are. */
  recordResult: holding(
    z.looseObject({
      line: z.string(),
      result: z.looseObject({
        id: z.string(),
        verdict: z.string(),
        role: z.string(),
        check_block_id: z.string(),
        check_issue_key: z.string(),
        commit_sha: z.string(),
      }),
    }),
    { segment: 3, at: ["result", "issue_key"] },
    { member: "verdict", at: ["result", "verdict"] },
    { member: "commit", at: ["result", "commit_sha"] }
  ),
  retractResult: holding(
    z.looseObject({
      result_id: z.string(),
      reason: z.string(),
      created_at: z.string(),
      actor,
    }),
    { segment: 3, at: ["result_id"] }
  ),
  /** A CI job's result: its project, check, verdict and commit, the commit as the statement's
   *  one subject, the repository at that commit. */
  recordSpecCheckResult: holding(
    z.looseObject({
      id: z.string(),
      seq: z.number().int(),
      attestation: z.looseObject({
        predicate: z.looseObject({ check: z.string(), verdict: z.string() }),
      }),
    }),
    { segment: 3, at: ["attestation", "predicate", "project"] },
    { member: "check", at: ["attestation", "predicate", "check"] },
    { member: "verdict", at: ["attestation", "predicate", "verdict"] },
    { member: "commit", at: ["attestation", "subject", 0, "digest", "gitCommit"] }
  ),
  comment: holding(z.looseObject({ ...documentComment, issue_key: commentFields.issue_key }), {
    segment: 3,
    at: ["issue_key"],
  }),
  artifactComment: holding(z.looseObject(documentComment), { segment: 3, at: ["artifact_id"] }),
  suggest: holding(suggestion, { segment: 3, at: ["issue_key"] }),
  artifactSuggest: holding(suggestion, { segment: 3, at: ["artifact_id"] }),
  resolveComment: holding(
    z.looseObject({
      id: commentFields.id,
      issue_key: commentFields.issue_key,
      artifact_id: commentFields.artifact_id,
    }),
    { segment: 3, at: ["id"] }
  ),
  message: holding(z.looseObject({ id: messageFields.id, advice }), {
    segment: 3,
    at: ["issue_key"],
  }),
  agentMessage: z.looseObject({
    id: messageFields.id,
    deliveries: z
      .array(
        z.looseObject({ state: z.string(), error: z.string().nullish(), delivery: z.string() })
      )
      .nullish(),
  }),
  messageReply: holding(
    z.looseObject({
      id: messageFields.id,
      body: messageFields.body,
      in_reply_to: messageFields.in_reply_to.optional(),
      duplicate: z.boolean().optional(),
    }),
    { segment: 3, at: ["in_reply_to"] }
  ),
  /** `postDeliveryReply` reads nothing of the answer: any object Dispatch answers is the row. */
  deliveryReply: z.looseObject({}),
  acceptMessageDelivery: holding(
    z.looseObject({
      message_id: z.string(),
      body: z.string(),
      delivery: z.string(),
    }),
    { segment: 3, at: ["message_id"] },
    { segment: 5, at: ["attempt"] }
  ),
  docEdit: z.looseObject({
    applied: z.number().int(),
    version: z.looseObject({ number: versionFields.number }).nullable(),
    changed: z.boolean().optional(),
    unchanged_ops: z.array(z.number().int()).nullish(),
    lost_ops: z.array(z.number().int()).nullish(),
    written: z.string().optional(),
    token: z.string().optional(),
    advice: caughtAdvice,
  }),
  /** No caller of `nameArtifactVersion` reads its answer: any object Dispatch answers is the row. */
  nameArtifactVersion: z.looseObject({}),
  writeHandoff: holding(
    z.looseObject({
      id: z.string(),
      issue_key: z.string(),
      phase: z.string(),
      version: z.number().int(),
    }),
    { segment: 3, at: ["issue_key"] },
    { segment: 5, at: ["phase"] }
  ),
  projectArtifact: holding(
    z.looseObject({
      artifact: z.looseObject({ ...uploadedDocument, project: artifactFields.project }),
      version: pictureUpload.version,
      advice,
    }),
    { segment: 3, at: ["artifact", "project"] }
  ),
  agentArtifact: holding(z.looseObject(pictureUpload), {
    segment: 3,
    at: ["artifact", "session_id"],
  }),
  artifact: holding(
    z.looseObject({
      artifact: z.looseObject(uploadedDocument),
      version: pictureUpload.version,
      advice,
    }),
    { segment: 3, at: ["artifact", "issue_key"] }
  ),
} satisfies Record<string, z.ZodType>;
