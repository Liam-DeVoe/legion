import { Fragment, type ReactNode } from "react";

import type { DispatchEvent, DisplayTree } from "../../api/types";
import { type AgentPart, AgentParts, askResolutionParts } from "../refs/AgentName";
import { askResolutionReason } from "../refs/actor";
import { PlainText } from "../refs/plain-text";

/**
 * A text of the record inside a sentence the dashboard writes - the question in `asked “…”`, the
 * question a reply answers, a resolution's reason - kept as a part until the sentence is drawn,
 * so the one reader reads it (`SentenceParts`) rather than the sentence carrying its source.
 */
export interface ProsePart {
  /** The text as it is stored. */
  readonly prose: string;
  /** The reading Go made of it, where the event carries one (`DispatchEvent.trees`). */
  readonly tree: DisplayTree | undefined;
}

/** One part of an event's sentence: the dashboard's own words, an agent it names, or a text of
 *  the record. */
export type SentencePart = AgentPart | ProsePart;

/** The text an event's payload holds at `path` (`payload.question`, `payload.resolution.reason`)
 *  as a part of the event's sentence, with the tree that rides beside the payload for it. */
export function eventProse(event: DispatchEvent, path: string, prose: string): ProsePart {
  return { prose, tree: event.trees?.[path] };
}

/** An `ask.resolved` event's sentence: who resolved, retracted, declined or withdrew the ask
 *  (`askResolutionParts`), and after it the reason they gave, which is their own stored text. */
export function askResolvedParts(
  event: Extract<DispatchEvent, { type: "ask.resolved" }>
): SentencePart[] {
  const { resolution } = event.payload;
  const reason = askResolutionReason(resolution);
  return reason === undefined
    ? askResolutionParts(resolution)
    : [
        ...askResolutionParts(resolution),
        " - ",
        eventProse(event, "payload.resolution.reason", reason),
      ];
}

/**
 * Draws a sentence's parts in a line of text: its words and the agents it names as `AgentParts`
 * draws them, and each text of the record as its plain-text projection, where the formatting is
 * gone and a reference reads as its target's name. Why a line draws the projection is the
 * reading list's `event-line` (`DISPATCH_READING_SURFACES`).
 */
export function SentenceParts({
  parts,
  text,
}: {
  parts: readonly SentencePart[];
  /** How the host marks up a part that is the dashboard's own words (`AgentParts`). */
  text?: (part: string, index: number) => ReactNode;
}): ReactNode {
  return parts.map((part, index) => (
    // biome-ignore lint/suspicious/noArrayIndexKey: the parts are one fixed sentence, in order.
    <Fragment key={index}>
      {typeof part === "object" && "prose" in part ? (
        <PlainText markdown={part.prose} surface="event-line" tree={part.tree}>
          {/* Until the reader's engine has answered, the text as written, which the reading
              list's `event-line` says is a load state. */}
          {(line) => line ?? part.prose}
        </PlainText>
      ) : (
        <AgentParts
          parts={[part]}
          text={text === undefined ? undefined : (words) => text(words, index)}
        />
      )}
    </Fragment>
  ));
}
