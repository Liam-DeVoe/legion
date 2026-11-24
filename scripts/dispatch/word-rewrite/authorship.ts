import { z } from "zod";
import {
  type Actor,
  type DispatchReads,
  type Event,
  type Version,
  VersionText,
} from "./dispatch-api.ts";
import type { Author } from "./places.ts";

/**
 * Who can have written an occurrence (the spec's "Who wrote it"). The tool changes an occurrence
 * only when no person made or changed its block and no person's version added it.
 *
 * Each version of a document records its authors. A version whose authors are all sessions
 * wrote nothing a person wrote. Any other version is suspect: a person (`kind: "user"`, an agent
 * acting through a person's token included, which Dispatch records as the person), or authors no
 * program can read as sessions alone (`null`, a `system` actor, a person beside a session). A
 * document with no suspect version costs no read.
 *
 * For each suspect version the tool reads it and the version before it with `?blocks=true`. A
 * past version lists only its typed blocks (`docs.VersionBlocks`): a paragraph has no id in a
 * stored version. So a version touched the occurrence's block when the block is typed and its
 * token moved in that version, or when a paragraph that version added (one it holds that the
 * version before does not) holds the block's text or the sentence of the occurrence. A person's
 * version that changed only another block touched nothing of this one.
 */

/** How a version's authors read. */
export type VersionClass = "session" | "person" | "unknown";

export function versionClass(authors: readonly Actor[] | null): VersionClass {
  if (authors === null || authors.length === 0) return "unknown";
  const kinds = new Set(authors.map((author) => author.kind));
  if (kinds.size === 1 && kinds.has("session")) return "session";
  if (kinds.size === 1 && kinds.has("user")) return "person";
  return "unknown";
}

/** The occurrence's block as the live text holds it: its id (null where no block holds it), its
 *  text, and the sentence of the occurrence inside it. A past version lists typed blocks alone,
 *  so only a typed block's id is found there. */
export interface BlockInText {
  readonly id: string | null;
  readonly text: string;
  readonly sentence: string;
}

const PARAGRAPH_BREAK = /\n[ \t]*\n/;

function paragraphs(markdown: string): Set<string> {
  return new Set(
    markdown
      .split(PARAGRAPH_BREAK)
      .map((paragraph) => paragraph.trim())
      .filter((paragraph) => paragraph !== "")
  );
}

interface ReadVersion {
  readonly paragraphs: Set<string>;
  readonly tokens: Map<string, string>;
}

const EMPTY: ReadVersion = { paragraphs: new Set(), tokens: new Map() };

/** The authorship of one document's blocks, its versions read once each and only when needed. */
export class Authorship {
  readonly #suspects: readonly { readonly number: number; readonly kind: VersionClass }[];
  readonly #read = new Map<number, Promise<ReadVersion>>();

  constructor(
    private readonly artifactId: string,
    versions: readonly Version[] | null,
    private readonly reads: Pick<DispatchReads, "version">
  ) {
    this.#suspects =
      versions === null
        ? [{ number: 0, kind: "unknown" }]
        : versions
            .map((version) => ({ number: version.number, kind: versionClass(version.authors) }))
            .filter((version) => version.kind !== "session")
            .sort((a, b) => a.number - b.number);
  }

  /** Whether any version needs reading at all. */
  get suspect(): boolean {
    return this.#suspects.length > 0;
  }

  #version(number: number): Promise<ReadVersion> {
    if (number < 1) return Promise.resolve(EMPTY);
    let read = this.#read.get(number);
    if (read === undefined) {
      read = this.reads.version(this.artifactId, number).then((answer) => {
        const version = VersionText.parse(answer);
        return {
          paragraphs: paragraphs(version.markdown),
          tokens: new Map((version.blocks ?? []).map((block) => [block.id, block.token])),
        };
      });
      this.#read.set(number, read);
    }
    return read;
  }

  async #touched(number: number, block: BlockInText): Promise<boolean> {
    const [before, after] = await Promise.all([this.#version(number - 1), this.#version(number)]);
    if (block.id !== null) {
      const token = after.tokens.get(block.id);
      if (token !== undefined && token !== before.tokens.get(block.id)) return true;
    }
    const text = block.text.trim();
    const sentence = block.sentence.trim();
    for (const paragraph of after.paragraphs) {
      if (before.paragraphs.has(paragraph)) continue;
      if (paragraph.includes(sentence) || paragraph.includes(text)) return true;
    }
    return false;
  }

  /** Who can have written the block: `person` when a person's version touched it, `unknown` when
   *  only a version of unreadable authors did, else `session`. A document whose versions Dispatch
   *  did not list is `unknown` throughout. */
  async authorOf(block: BlockInText): Promise<Author> {
    let author: Author = "session";
    for (const suspect of this.#suspects) {
      if (suspect.number === 0) return "unknown";
      if (!(await this.#touched(suspect.number, block))) continue;
      if (suspect.kind === "person") return "person";
      author = "unknown";
    }
    return author;
  }
}

const IssuePayload = z.looseObject({
  changed: z.array(z.string()).optional(),
  status: z.string().optional(),
  title: z.string().optional(),
});

/** Whether an issue event moved `field` (its payload's `changed` list names it). */
export function eventMoved(event: Event, field: string): boolean {
  const payload = IssuePayload.safeParse(event.payload);
  return payload.success && (payload.data.changed ?? []).includes(field);
}

/** The status an issue event's payload records, which embeds the whole issue. */
export function eventStatus(event: Event): string | undefined {
  const payload = IssuePayload.safeParse(event.payload);
  return payload.success ? payload.data.status : undefined;
}

/** Who can have written a title: the actor of the last event that set it, or the issue's
 *  creator when no event did. */
export function titleAuthor(events: readonly Event[], createdBy: Actor | null | undefined): Author {
  let setter: Actor | null | undefined = createdBy;
  for (const event of events) if (eventMoved(event, "title")) setter = event.actor;
  return setter === null || setter === undefined ? "unknown" : versionClass([setter]);
}
