import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { z } from "zod";
import type { Ancestry, DaemonStates } from "../word-rewrite/daemons.ts";
import {
  type DispatchReads,
  type DispatchWrites,
  ReadError,
  type ReplaceOp,
  type WriteOutcome,
} from "../word-rewrite/dispatch-api.ts";
import type { Model } from "../word-rewrite/gateway.ts";
import type { Deps, Options } from "../word-rewrite/main.ts";
import type { Clock } from "../word-rewrite/request-log.ts";

/**
 * An in-memory Dispatch behind `DispatchReads` and `DispatchWrites`, a fake model, and a clock that
 * does not wait, for the word rewrite's tests. Every call is recorded, so a test can say which
 * reads a run made and that report mode made no write and no model call.
 */

export const FIXTURES = join(import.meta.dir, "..", "__fixtures__", "word-rewrite");

/** A fixture of `__fixtures__/word-rewrite/`, parsed. */
export function fixture(name: string): unknown {
  return JSON.parse(readFileSync(join(FIXTURES, name), "utf8"));
}

const Inputs = z.object({
  texts: z.record(z.string(), z.string()),
  "check-names": z.array(z.string()),
  rewrites: z.array(z.tuple([z.string(), z.string()])),
});

/** The test inputs that hold the word, kept as a fixture (`inputs.json`). */
export const INPUTS = Inputs.parse(fixture("inputs.json"));

/** One text of `inputs.json`. */
export function input(name: string): string {
  const text = INPUTS.texts[name];
  if (text === undefined) throw new Error(`inputs.json holds no text ${name}`);
  return text;
}

/** `text` with each rule of `inputs.json` applied in order, as the fake model rewrites. */
export function rewriteByRules(text: string): string {
  return INPUTS.rewrites.reduce((kept, [from, to]) => kept.split(from).join(to), text);
}

const SESSION = { kind: "session", id: "5b0f3c2e-8d41-4a7e-9c16-2f7a9e0d4b31" };
const sha = (text: string) => `sha256:${createHash("sha256").update(text).digest("hex")}`;
const bytes = (text: string) => Buffer.byteLength(text, "utf8");

/** The blocks of a text as the fake serves them: each paragraph, and each typed block whole,
 *  with UTF-8 byte ranges. */
export function blocksOf(markdown: string) {
  const blocks: {
    id: string;
    type: string;
    from: number;
    to: number;
    token: string;
    references: { comments: number; asks: number };
    attributes?: { name: string; value: string; kind: string }[];
  }[] = [];
  const lines = markdown.split("\n");
  let byte = 0;
  let index = 0;
  let counter = 0;
  const width = (line: string | undefined) => bytes(line ?? "") + 1;
  while (index < lines.length) {
    const line = lines[index] ?? "";
    if (line === "") {
      byte += 1;
      index += 1;
      continue;
    }
    const typed = /^:::(\w+)\{#([\w-]+)([^}]*)\}$/.exec(line);
    let end = index;
    if (typed) {
      while (end < lines.length && lines[end] !== ":::") end += 1;
    } else {
      while (end + 1 < lines.length && lines[end + 1] !== "") end += 1;
    }
    const text = lines.slice(index, end + 1).join("\n");
    counter += 1;
    blocks.push({
      id: typed?.[2] ?? `p${counter}`,
      type: typed?.[1] ?? "paragraph",
      from: byte,
      to: byte + bytes(text),
      token: sha(text),
      references: { comments: 0, asks: 0 },
      ...(typed
        ? {
            attributes: [...(typed[3] ?? "").matchAll(/(\w+)="([^"]*)"/g)].map((match) => ({
              name: match[1] ?? "",
              value: match[2] ?? "",
              kind: "string",
            })),
          }
        : {}),
    });
    for (let k = index; k <= end; k += 1) byte += width(lines[k]);
    index = end + 1;
  }
  return blocks;
}

export interface FakeDocument {
  id: string;
  project: string;
  issue: string | null;
  name: string;
  markdown: string;
  version: number;
  authors: unknown[][];
  approval: Record<string, unknown>;
}

export interface FakeIssue {
  key: string;
  project: string;
  title: string;
  status: string;
  parent: string | null;
  lastSeq: number;
  createdBy: unknown;
}

interface LogEvent {
  id: number;
  issue_key: string | null;
  artifact_id: string | null;
  project: string;
  seq: number;
  type: string;
  actor: unknown;
  created_at: string;
  payload: unknown;
}

export class FakeDispatch {
  readonly projects: string[] = [];
  readonly issues = new Map<string, FakeIssue>();
  readonly documents = new Map<string, FakeDocument>();
  readonly log: LogEvent[] = [];
  readonly calls: string[] = [];
  readonly writeCalls: string[] = [];
  readonly daemonSettings = new Map<string, string>();
  whoami: unknown = {
    kind: "agent",
    owner: null,
    project: null,
    scopes: [],
    service: null,
    session: null,
  };
  health: unknown = {
    ok: true,
    commit: "1111111111111111111111111111111111111111",
    schema_version: 1,
  };
  /** Projects whose issue list fails, as a read that ends a run. */
  readonly failing = new Set<string>();
  /** Runs just before a write is applied, as another writer in between. */
  beforeWrite: ((kind: "edit" | "retitle", id: string) => void) | undefined;
  #nextId = 1;
  #nextEvent = 1;

  project(key: string): this {
    if (!this.projects.includes(key)) this.projects.push(key);
    return this;
  }

  #event(
    type: string,
    issue: string | null,
    artifact: string | null,
    project: string,
    payload: unknown
  ) {
    const seq = issue === null ? 0 : (this.issues.get(issue)?.lastSeq ?? 0) + 1;
    const owner = issue === null ? undefined : this.issues.get(issue);
    if (owner !== undefined) owner.lastSeq = seq;
    // An event has one owner (`events_one_owner`): an issue document's event is its issue's, with
    // no top-level artifact id, and names the document in its payload alone.
    this.log.push({
      id: this.#nextEvent++,
      issue_key: issue,
      artifact_id: issue === null ? artifact : null,
      project,
      seq,
      type,
      actor: SESSION,
      created_at: "2026-11-24T10:00:00Z",
      payload: artifact === null ? payload : { artifact_id: artifact, ...Object(payload) },
    });
  }

  issue(project: string, title: string, options: Partial<FakeIssue> = {}): FakeIssue {
    this.project(project);
    const number =
      [...this.issues.values()].filter((issue) => issue.project === project).length + 1;
    const issue: FakeIssue = {
      key: `${project}-${number}`,
      project,
      title,
      status: "todo",
      parent: null,
      lastSeq: 0,
      createdBy: SESSION,
      ...options,
    };
    this.issues.set(issue.key, issue);
    this.#event("issue.created", issue.key, null, project, {
      title,
      status: issue.status,
      changed: ["title", "status"],
    });
    return issue;
  }

  document(
    owner: { issue: string } | { project: string },
    name: string,
    markdown: string,
    options: Partial<FakeDocument> = {}
  ): FakeDocument {
    const issue = "issue" in owner ? owner.issue : null;
    const project =
      "issue" in owner ? (this.issues.get(owner.issue)?.project ?? "") : owner.project;
    this.project(project);
    const document: FakeDocument = {
      id: `00000000-0000-4000-8000-${String(this.#nextId++).padStart(12, "0")}`,
      project,
      issue,
      name,
      markdown,
      version: 1,
      authors: [[SESSION]],
      approval: { state: "draft", latest_version: 1 },
      ...options,
    };
    this.documents.set(document.id, document);
    this.#event("artifact.created", issue, document.id, project, { name, version: 1 });
    return document;
  }

  /** Changes a document as another session does, moving its version and token. */
  editAsOther(id: string, find: string, replacement: string): void {
    const document = this.documents.get(id);
    if (document === undefined) throw new Error(`no document ${id}`);
    document.markdown = document.markdown.replace(find, replacement);
    document.version += 1;
    document.authors.push([{ kind: "session", id: "another" }]);
    this.#event("artifact.version", document.issue, id, document.project, {
      version: document.version,
    });
  }

  close(key: string): void {
    const issue = this.issues.get(key);
    if (issue === undefined) throw new Error(`no issue ${key}`);
    issue.status = "done";
    this.#event("issue.updated", key, null, issue.project, {
      status: "done",
      changed: ["status", "closed_at"],
    });
  }

  /** Appends an event to an issue that changes nothing of its title, as a comment does. */
  touch(key: string): void {
    const issue = this.issues.get(key);
    if (issue === undefined) throw new Error(`no issue ${key}`);
    this.#event("comment.created", key, null, issue.project, { body: "a comment" });
  }

  #artifact(document: FakeDocument) {
    const slug = document.name.replace(/\./g, "-");
    return {
      id: document.id,
      issue_key: document.issue,
      project: document.project,
      session_id: null,
      ref_key: `${document.issue ?? document.project}/${slug}`,
      slug,
      name: document.name,
      kind: "doc",
      // Dispatch makes an issue's spec its primary document at the issue's creation.
      primary: document.issue !== null && document.name === "spec.md",
      created_by: SESSION,
      created_at: "2026-11-24T10:00:00Z",
      versions: document.authors.map((authors, index) => ({
        number: index + 1,
        named: true,
        summary: null,
        authors,
        created_at: "2026-11-24T10:00:00Z",
      })),
      approval: document.approval,
    };
  }

  #row(issue: FakeIssue) {
    return {
      key: issue.key,
      title: issue.title,
      status: issue.status,
      closed_at: issue.status === "done" ? "2026-11-24T11:00:00Z" : null,
      last_seq: issue.lastSeq,
      parent: issue.parent,
      created_by: issue.createdBy,
    };
  }

  reads(): DispatchReads {
    const record = (call: string) => this.calls.push(call);
    /** The answer `answer` gives, once the call is recorded. */
    const recorded =
      (call: string, answer: () => unknown): (() => Promise<unknown>) =>
      async () => {
        record(call);
        return answer();
      };
    const documentsOf = (issue: string | null, project: string) =>
      [...this.documents.values()].filter(
        (document) => document.issue === issue && document.project === project
      );
    return {
      healthz: recorded("healthz", () => this.health),
      whoami: recorded("whoami", () => this.whoami),
      projects: recorded("projects", () => this.projects.map((key) => ({ key }))),
      issues: async (project, includeClosed) => {
        record(`issues ${project}`);
        if (this.failing.has(project)) throw new ReadError("GET /api/v1/issues", "transport");
        return [...this.issues.values()]
          .filter(
            (issue) => issue.project === project && (includeClosed || issue.status !== "done")
          )
          .map((issue) => this.#row(issue));
      },
      children: async (parent) => {
        record(`children ${parent}`);
        return [...this.issues.values()]
          .filter((issue) => issue.parent === parent)
          .map((issue) => this.#row(issue));
      },
      issue: async (key) => {
        record(`issue ${key}`);
        const issue = this.issues.get(key);
        if (issue === undefined) throw new Error(`no issue ${key}`);
        return {
          ...this.#row(issue),
          project: issue.project,
          artifacts: documentsOf(key, issue.project).map((document) => this.#artifact(document)),
        };
      },
      issueArtifacts: async (key) => {
        record(`issueArtifacts ${key}`);
        const issue = this.issues.get(key);
        return documentsOf(key, issue?.project ?? "").map((document) => this.#artifact(document));
      },
      projectDocuments: async (project) => {
        record(`projectDocuments ${project}`);
        return documentsOf(null, project).map((document) => this.#artifact(document));
      },
      text: async (id) => {
        record(`text ${id}`);
        const document = this.documents.get(id);
        if (document === undefined) throw new Error(`no document ${id}`);
        return {
          markdown: document.markdown,
          token: sha(document.markdown),
          version: document.version,
          blocks: blocksOf(document.markdown),
        };
      },
      version: async (id, number) => {
        record(`version ${id} ${number}`);
        throw new Error("the fake keeps no past versions");
      },
      issueEvents: async (key, after) => {
        record(`issueEvents ${key}`);
        return this.log.filter((event) => event.issue_key === key && event.seq > after);
      },
      daemonSetting: async (project) => {
        record(`daemonSetting ${project}`);
        const url = this.daemonSettings.get(project);
        return url === undefined ? null : { project, state_url: url };
      },
      eventLog: async (after, limit) => {
        record(`eventLog ${after} ${limit}`);
        return this.log.filter((event) => event.id > after).slice(0, limit);
      },
      stateText: async (project, slug) => {
        record(`stateText ${project} ${slug}`);
        const document = [...this.documents.values()].find(
          (candidate) =>
            candidate.project === project &&
            candidate.issue === null &&
            candidate.name.replace(/\./g, "-") === slug
        );
        if (document === undefined) return null;
        return {
          markdown: document.markdown,
          token: sha(document.markdown),
          version: document.version,
        };
      },
    };
  }

  writes(): DispatchWrites {
    const refuse = (status: number, code: string): WriteOutcome => ({ ok: false, status, code });
    return {
      edit: async (id: string, ops: readonly ReplaceOp[], token: string, summary: string) => {
        this.writeCalls.push(`edit ${id} ops=${ops.length} summary=${summary}`);
        this.beforeWrite?.("edit", id);
        const document = this.documents.get(id);
        if (document === undefined) return refuse(404, "ARTIFACT_NOT_FOUND");
        const issue = document.issue === null ? undefined : this.issues.get(document.issue);
        if (issue?.status === "done") return refuse(409, "ISSUE_CLOSED");
        if (sha(document.markdown) !== token) return refuse(409, "PRECONDITION_FAILED");
        let markdown = document.markdown;
        for (const op of ops) {
          const parts = markdown.split(op.find);
          const at = op.occurrence ?? 0;
          if (parts.length - 1 <= at) return refuse(400, "INVALID_OP");
          markdown = `${parts.slice(0, at + 1).join(op.find)}${op.with}${parts.slice(at + 1).join(op.find)}`;
        }
        document.markdown = markdown;
        document.version += 1;
        document.authors.push([SESSION]);
        this.#event("artifact.version", document.issue, id, document.project, {
          version: document.version,
          summary,
        });
        return { ok: true, answer: fixture("edit-ok.json") };
      },
      retitle: async (key: string, title: string, expectedSequence: number) => {
        this.writeCalls.push(`retitle ${key} ${expectedSequence}`);
        this.beforeWrite?.("retitle", key);
        const issue = this.issues.get(key);
        if (issue === undefined) return refuse(404, "ISSUE_NOT_FOUND");
        if (issue.status === "done") return refuse(409, "ISSUE_CLOSED");
        if (issue.lastSeq !== expectedSequence) return refuse(409, "ISSUE_CHANGED");
        issue.title = title;
        this.#event("issue.updated", key, null, issue.project, { title, changed: ["title"] });
        return { ok: true, answer: fixture("title-ok.json") };
      },
      uploadState: async (project: string, name: string, content: string, token: string | null) => {
        this.writeCalls.push(`uploadState ${project} ${name}`);
        const existing = [...this.documents.values()].find(
          (document) =>
            document.project === project && document.issue === null && document.name === name
        );
        if (existing === undefined) {
          if (token !== null) return refuse(409, "PRECONDITION_FAILED");
          this.document({ project }, name, content);
          return { ok: true, answer: {} };
        }
        if (token !== sha(existing.markdown)) return refuse(409, "PRECONDITION_FAILED");
        existing.markdown = content;
        existing.version += 1;
        existing.authors.push([SESSION]);
        this.#event("artifact.version", null, existing.id, project, { version: existing.version });
        return { ok: true, answer: {} };
      },
    };
  }
}

/** A model that answers by a rule, and counts its calls. */
export class FakeModel implements Model {
  readonly prompts: string[] = [];

  constructor(private readonly answer: (user: string) => string | null) {}

  async ask(prompt: { readonly system: string; readonly user: string }): Promise<string | null> {
    this.prompts.push(prompt.user);
    return this.answer(prompt.user);
  }
}

/** The paragraph between the prompt's two rule lines. */
export function promptParagraph(user: string): string {
  return user.split("\n-----\n")[1] ?? "";
}

/** A model that rewrites each status use of the word by the rules of `inputs.json`. */
export function rewritingModel(): FakeModel {
  return new FakeModel((user) =>
    JSON.stringify({ rewrite: rewriteByRules(promptParagraph(user)) })
  );
}

/** A clock that moves only when a test or a wait moves it. */
export class FakeClock implements Clock {
  constructor(public time = Date.parse("2026-11-24T12:00:00Z")) {}

  now(): number {
    return this.time;
  }

  async sleep(ms: number): Promise<void> {
    this.time += ms;
  }
}

export const NO_DAEMONS: DaemonStates = {
  async read() {
    throw new Error("no daemon answers");
  },
};

export const NO_ANCESTRY: Ancestry = { holds: async () => false };

/** The options of a run, with the flags a test does not name at their defaults. */
export function options(overrides: Partial<Options> = {}): Options {
  return {
    mode: "report",
    tree: "TREE-1",
    mergeCommit: "abcdef1234567",
    projects: [],
    includeClosed: false,
    stateDocument: undefined,
    stateFile: undefined,
    extraMap: undefined,
    requestLog: undefined,
    stateAddress: new Map(),
    noDaemon: new Set(),
    summary: undefined,
    model: "fake",
    concurrency: 2,
    maxWrites: undefined,
    pauseBeforeWrite: undefined,
    stopAt: undefined,
    stopAfterMs: undefined,
    whole: false,
    ...overrides,
  };
}

/** The dependencies of a run over a fake Dispatch, with every printed line kept. */
export function deps(
  dispatch: FakeDispatch,
  overrides: Partial<Deps> = {}
): Deps & { readonly lines: string[] } {
  const lines: string[] = [];
  return {
    reads: dispatch.reads(),
    writes: dispatch.writes(),
    model: undefined,
    daemonStates: NO_DAEMONS,
    ancestry: NO_ANCESTRY,
    clock: new FakeClock(),
    contract: 220,
    extraEntries: [],
    out: (line) => lines.push(line),
    requests: () => dispatch.calls.length,
    lines,
    ...overrides,
  };
}
