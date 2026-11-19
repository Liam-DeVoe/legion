/**
 * The constants of Dispatch's tool contract that are not a schema: the issue statuses, the ask
 * urgencies, the block units and the two limits a caller is held to, the step and plan writes'
 * codes and forms, and the artifact command's rule wordings, with the small functions over them.
 *
 * Why they are a module of their own, beside `dispatch-tools.ts` rather than inside it. That
 * module builds the tool schemas at its top level, and a bundler may drop none of them: a value
 * is used, so the module is kept and the schemas are kept with it, zod and all. The dashboard
 * reads the statuses, the urgencies, the block units and the two limits and validates nothing,
 * and it was paying for the schema builder on every page load to have them
 * (dispatch://INFRA-1109, R13). A constant a reader needs must not drag a schema builder into a
 * browser, so the constants live here and `dispatch-tools.ts` re-exports them: an import of that
 * module, or of the package index, answers exactly what it answered before. A new constant of
 * the tool contract goes here too, rather than beside the schemas, whether or not a browser
 * reads it.
 */

/** Ask urgency levels the Dispatch server accepts, in ascending order. */
export const ASK_URGENCIES = ["low", "med", "high", "blocking"] as const;

/**
 * Longest one block of a comment, comment edit, message, broadcast or ask part may be, in UTF-16
 * code units as the Dispatch server counts them. Generated into Go as
 * `contracts.DispatchBlockUnits`, which the server enforces and names in its refusal, and which
 * it publishes through `GET /api/v1/limits`, the route `dispatch limits` prints.
 */
export const DISPATCH_BLOCK_UNITS = 32000;

/**
 * Longest body an event carries whole, in UTF-16 code units, and the bound the Dispatch server
 * holds an ask's question and its options to together. Generated into Go as
 * `contracts.DispatchEventBodyMax`, which the server enforces and names in its refusal, and
 * which it publishes through `GET /api/v1/limits`, the route `dispatch limits` prints.
 */
export const DISPATCH_EVENT_BODY_MAX = 32000;

/**
 * Longest `dispatch_search` query (`GET /api/v1/search`'s `q`, trimmed), in UTF-16 units. The
 * query rides in the URL beside `project`, `limit` and `offset`, so this refusal and `project`'s
 * key rule are what keep the tool's URL under the load balancer's limit;
 * `packages/contracts/AGENTS.md` "Search limits" owns that budget. Generated into Go as
 * `contracts.SearchQueryMax`, which the server enforces.
 */
export const SEARCH_QUERY_MAX = 1000;

/** Issue lifecycle statuses the Dispatch server accepts (`model.IssueStatuses`), in lifecycle order. */
export const ISSUE_STATUSES = [
  "triage",
  "icebox",
  "backlog",
  "todo",
  "in_progress",
  "testing",
  "needs_review",
  "retro",
  "done",
] as const;

export function isIssueStatus(value: string): value is IssueStatus {
  return (ISSUE_STATUSES as readonly string[]).includes(value);
}

export type IssueStatus = (typeof ISSUE_STATUSES)[number];

/** A whole project key, as the Dispatch server creates them, generated into Go as
 *  `contracts.ProjectKeyPattern` (and held by the `projects.key` check constraint). */
export const PROJECT_KEY_PATTERN = /^[A-Z][A-Z0-9]{1,9}$/;

export const PATH_KEEPS_BYTES =
  "path (--path <file>), which stores a file whose path does not end in .md or .markdown as its bytes, unchanged";

/**
 * What `dispatch artifact` says of a name the owner already holds. The command refuses it before
 * it uploads anything, because an upload of that name writes the document's next version and
 * replaces its text. `force` is what asks for that version. The refusal is the command's: the
 * API writes the version for every other caller, the Dispatch page's own upload among them.
 */
export const ARTIFACT_NAME_EXISTS_RULE =
  "An upload under the name of a document the owner already holds is refused: it writes that document's next version, over the text it holds. Change a document with dispatch doc-edit. Send the upload again with force (--force) to write the next version.";

/**
 * What `dispatch artifact` says of inline text the store would change. The command renders the
 * text as Dispatch renders a document it stores and compares the two, so a text stored under
 * another spelling (a `*` list stored with `-`, an ordered list renumbered, a CR LF stored as a
 * line feed) is refused before the upload rather than stored changed. The rule holds for both
 * kinds of name: `--path` keeps the bytes of a name that is not Markdown, and a Markdown name is
 * parsed and rendered whichever flag sends it, so its one way out is to send the stored text.
 */
export const ARTIFACT_STORED_TEXT_RULE = `content and content-file hold text Dispatch stores as Markdown, and a text the store would write under another spelling is refused. The refusal prints the text Dispatch stores: send that text. A name that does not end in .md or .markdown has a second way out, which keeps the text as it was written: send it as a file with ${PATH_KEEPS_BYTES}.`;

/** A name's extension, or undefined for a name with none: what follows its last dot when that
 *  holds no blank. Digits count, so "rows.jsonl.1" has one, and so does a title such as
 *  "plan v1.2", which takes content once it ends in .md. */
export const NAME_EXTENSION = /\.([^\s.]+)$/u;

export const MARKDOWN_EXTENSION = /^(md|markdown)$/i;

/**
 * Whether `--path` would store this name's text as the bytes it was written as: true for every
 * name that does not end in .md or .markdown, a name with no extension included. Dispatch reads
 * an upload's media type, which a Markdown path alone makes `text/markdown`, and parses and
 * renders only a document (`PATH_KEEPS_BYTES`). A refusal of inline text the store would change
 * names `--path` for such a name, and never for a Markdown one, which `--path` would change the
 * same way.
 */
export function pathKeepsBytesOf(name: string): boolean {
  const extension = NAME_EXTENSION.exec(name.trim())?.[1];
  return extension === undefined || !MARKDOWN_EXTENSION.test(extension);
}

/** The words of a step record, written once for Dispatch's store and the Legion daemon, a Go module
 *  of its own that reads them through `github.com/sjawhar/envoy/steprecord`. A word one side drifts
 *  on is a `400 STEP_INPUT` the daemon's outbox ends the row on, and the record is lost.
 *
 *  The kinds of a run: a step a plan names, at its place from 1, and the retro and the merge,
 *  acts of Legion that no plan names, at the place 0. Generated into Go as
 *  `contracts.StepKindStep`, `contracts.StepKindRetro` and `contracts.StepKindMerge`. */
export const STEP_KINDS = ["step", "retro", "merge"] as const;

/** The results of a run that ended: generated into Go as `contracts.StepResultPassed`,
 *  `contracts.StepResultFailed` and `contracts.StepResultStopped`. A run that still runs has none. */
export const STEP_RESULTS = ["passed", "failed", "stopped"] as const;

/** The codes of the step write's refusals that no retry changes: a value outside its form, a writer
 *  that is not the daemon's session, and an id whose record is a run of another issue. Generated
 *  into Go as `contracts.StepInputCode`, `contracts.StepWriterCode` and
 *  `contracts.StepOtherIssueCode`, which the route answers and the daemon ends a row on. */
export const STEP_INPUT_CODE = "STEP_INPUT";
export const STEP_WRITER_CODE = "STEP_WRITER";
export const STEP_OTHER_ISSUE_CODE = "STEP_OTHER_ISSUE";

/** The codes of the plan write's refusals that no retry changes: a value outside its form, and an
 *  id whose plan is the plan of another issue. A writer that is not the daemon's session is refused
 *  `STEP_WRITER`, as a step write is. Generated into Go as `contracts.PlanInputCode` and
 *  `contracts.PlanOtherIssueCode`, which the route answers and the daemon ends a row on. */
export const PLAN_INPUT_CODE = "PLAN_INPUT";
export const PLAN_OTHER_ISSUE_CODE = "PLAN_OTHER_ISSUE";

/** The form of the agent, the session and the model a step record names: printable ASCII with no
 *  space, from 1 to 128 characters. The session and the model come from the agent's own frames,
 *  and `dispatch steps` joins a record's fields with spaces, so a space or a control character in
 *  one would shift its columns or forge a line. Generated into Go as
 *  `contracts.StepAgentTextPattern`: Dispatch refuses a step write that names text outside it,
 *  and the Legion daemon leaves such text out of the record it writes. */
export const STEP_AGENT_TEXT_PATTERN = /^[!-~]{1,128}$/;

/** STEP_AGENT_TEXT_PATTERN's rule in words, which Dispatch's refusal gives: generated into Go as
 *  `contracts.StepAgentTextRule`. */
export const STEP_AGENT_TEXT_RULE_WORDS = "printable ASCII with no space, from 1 to 128 characters";
