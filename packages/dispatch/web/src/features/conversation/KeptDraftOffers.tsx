import type { ReactNode, RefObject } from "react";
import { flushSync } from "react-dom";

import { focusLost } from "../../components/focus-place";
import {
  dangerText,
  linkHoverText,
  linkText,
  quoteAccentBorder,
  quoteBodyText,
  textMutedOnSurfaceMuted,
} from "../../theme/classes";
import type { ComposerKind } from "./composer-model";
import { GoneReply } from "./KeptReply";
import type { SentDraft } from "./send-request";
import type { GoneReplyDraft, KeptDrafts, OfferedDraft } from "./useKeptDrafts";

/** Each kind as a kept draft names the one it was written as, beside a compose of another
 *  (`KeptDraftOffers`) or in the margin's list of what its compose kept (`MarginKeptDrafts`). */
export const KIND_WORDS: Record<ComposerKind, string> = {
  ask: "an ask",
  comment: "a comment",
  suggestion: "a suggestion",
};

/** One of the reader's presses for a draft offered beside a field. */
interface OfferPress {
  readonly label: string;
  readonly press: () => void;
}

/**
 * A draft the reader wrote that waits outside the field: the line that says why, the draft's text,
 * a suggestion's replacement where it has one, and a warning where its send may have been
 * delivered (`note`), then the reader's presses for it - one that takes it into the field, which
 * a host that could not send it leaves out (`take`), and one that drops it. The margin draws the
 * drafts its compose kept for words of a document the same way (`MarginKeptDrafts`). An offer
 * that holds its field read only gives its words the id the field names as its description
 * (`id`), and after a key typed in that field says, after those words, what the field waits for
 * (`typed`).
 */
export function DraftOffer({
  body,
  drop,
  id,
  lead,
  note,
  replacement = "",
  take,
  typed,
}: {
  readonly body: string;
  readonly drop: OfferPress;
  readonly id?: string;
  readonly lead: ReactNode;
  readonly note?: string;
  readonly replacement?: string;
  readonly take?: OfferPress;
  readonly typed?: string;
}): ReactNode {
  return (
    <div className="space-y-2">
      <div className={`space-y-1 text-sm ${textMutedOnSurfaceMuted}`} id={id} role="status">
        <p>{lead}</p>
        <blockquote
          className={`border-l-2 pl-2 whitespace-pre-wrap ${quoteAccentBorder} ${quoteBodyText}`}
        >
          {body}
        </blockquote>
        {replacement === "" ? null : (
          <>
            <p>Replace with:</p>
            <blockquote
              className={`border-l-2 pl-2 whitespace-pre-wrap ${quoteAccentBorder} ${quoteBodyText}`}
            >
              {replacement}
            </blockquote>
          </>
        )}
        {note === undefined ? null : <p className={dangerText}>{note}</p>}
        {typed === undefined ? null : <p>{typed}</p>}
      </div>
      <div className="flex flex-wrap gap-3">
        {take === undefined ? null : (
          <button
            className={`text-sm font-medium ${linkText} ${linkHoverText}`}
            onClick={take.press}
            type="button"
          >
            {take.label}
          </button>
        )}
        <button
          className={`text-sm font-medium underline ${dangerText}`}
          onClick={drop.press}
          type="button"
        >
          {drop.label}
        </button>
      </div>
    </div>
  );
}

/**
 * Commits `press`, a press of an offer (`DraftOffer`) that takes the offer away and with it the
 * button that held the focus, at once (`flushSync`), so the page holds what the press left; and
 * where the focus went with the button (`focusLost`), hands it to the control `next` names in that
 * page, with no scroll. A text box takes it with the caret at the end of what it holds: a draft
 * taken in is ready to go on. The caret is set first and the focus restores it, so no engine
 * scrolls to show it. A host that moved the focus itself, or took `next`'s control away, keeps
 * that. A composer's offers hand it to its field (`KeptDraftOffers`), and so does the reply the
 * Conversation's docked composer draws for a phone thread (`ConversationTab`); the margin's kept
 * drafts hand it along their list (`MarginKeptDrafts`).
 */
export function handFocusOn(press: () => void, next: () => HTMLElement | null): void {
  flushSync(press);
  const control = next();
  if (control === null || !focusLost()) return;
  if (control instanceof HTMLTextAreaElement) {
    control.setSelectionRange(control.value.length, control.value.length);
  }
  control.focus({ preventScroll: true });
}

/**
 * What a composer offers beside its field that the field does not hold (`useKeptDrafts`): an
 * edit written over a body the comment no longer holds (`changed`), a draft a margin compose
 * kept for other words of its document (`offered`), and each reply it keeps whose comment or
 * message its host no longer lists (`gone`). Each waits with the words that say why, and with the
 * reader's presses for it: one takes it into the field, where the composer can send it, and the
 * other drops it. Either press hands the focus to the field (`field`). Nothing is saved or sent
 * meanwhile. The edit and the offered draft each hold the field read only while they wait, so
 * the one that does gives its words the id the field names as its description (`heldId`), and
 * once a key was typed in the field (`typedOver`) says after them what the field waits for.
 */
export function KeptDraftOffers({
  changed,
  field,
  gone,
  goneTakes,
  heldId,
  kind,
  offered,
  onDiscardChanged,
  onDropGone,
  onDropOffered,
  onTakeChanged,
  onTakeGone,
  onTakeOffered,
  typedOver,
}: {
  readonly changed: SentDraft | undefined;
  /** The composer's text box, which each press of an offer here hands the focus to. */
  readonly field: RefObject<HTMLTextAreaElement | null>;
  readonly gone: readonly GoneReplyDraft[];
  /** Where Use it here can put a draft of `gone` (`KeptDrafts.awayTakes`). */
  readonly goneTakes: KeptDrafts["awayTakes"];
  /** The id the offer that holds the field read only gives its words, which the field names as
   *  its description (`DraftOffer.id`): the edit, else the offered draft. */
  readonly heldId: string;
  /** The kind the composer writes now, which an offered draft names its own beside. */
  readonly kind: ComposerKind;
  readonly offered: OfferedDraft | undefined;
  /** Discard my edit: the composer's Discard, which drops the draft the reader wrote there. */
  readonly onDiscardChanged: () => void;
  /** Discard my draft for a draft of `gone`. */
  readonly onDropGone: (away: GoneReplyDraft) => void;
  readonly onDropOffered: () => void;
  readonly onTakeChanged: () => void;
  /** Use it here for a draft of `gone`, drawn while `goneTakes` says `"here"`. */
  readonly onTakeGone: (away: GoneReplyDraft) => void;
  /** Use my draft, which a compose that could not send the draft - a kind its closed issue
   *  refuses - leaves out, so the offer waits until the reader drops it. */
  readonly onTakeOffered: (() => void) | undefined;
  /** Whether the reader typed a key in the field while an offer here held it read only, which
   *  wrote nothing there. */
  readonly typedOver: boolean;
}): ReactNode {
  /** Each press below takes its offer away, and the field takes the focus it leaves
   *  (`handFocusOn`). A host that moved the focus itself, or took the composer away with Discard
   *  my edit, the composer's Discard (`onClose`), keeps that. */
  const intoField = (press: () => void) => () => handFocusOn(press, () => field.current);
  return (
    <>
      {/* An edit written over a body the comment no longer holds: the field shows the comment as
          it reads now, and the edit waits beside it with the words that say why, until the reader
          takes it back into the field or drops it. Nothing is saved meanwhile, and the field is
          read only, which a key typed there has the edit say. */}
      {changed === undefined ? null : (
        <DraftOffer
          body={changed.body}
          drop={{ label: "Discard my edit", press: intoField(onDiscardChanged) }}
          id={heldId}
          lead="This comment changed since you wrote your edit. The field shows the comment as it reads now. Your edit is kept here:"
          take={{ label: "Use my edit", press: intoField(onTakeChanged) }}
          typed={typedOver ? "Use or discard your kept edit first." : undefined}
        />
      )}
      {/* A draft the margin's compose kept for other words of the document: a newer compose there
          carried nothing, so it waits beside this one, naming the words it was written for, and
          the kind it was written as where this compose is another, since what it brings back -
          an ask's options, a suggestion's replacement - shows only under that kind. Only the
          reader's own press puts it in this field, and nothing is sent meanwhile. A draft whose
          send the reader stopped may have been delivered there, and taken here it goes as a new
          message, so the offer says so before that press, as the stopped send's note did. The
          field is read only meanwhile, so a key typed there has the offer say what the field
          waits for, by the presses it draws: a compose its closed issue refuses draws Use my draft
          only once the issue reopens. Only a margin compose offers a draft, and it edits no
          comment, so this offer and the edit never wait together; were they to, the edit holds
          the field's description. */}
      {offered === undefined ? null : (
        <DraftOffer
          body={offered.draft.body}
          drop={{ label: "Discard my draft", press: intoField(onDropOffered) }}
          id={changed === undefined ? heldId : undefined}
          lead={`You wrote this draft${offered.kind === undefined || offered.kind === kind ? "" : ` as ${KIND_WORDS[offered.kind]},`} for other words: “${offered.anchor.quote}”. It is kept here:`}
          note={offered.note}
          replacement={offered.draft.replacement}
          take={
            onTakeOffered === undefined
              ? undefined
              : { label: "Use my draft", press: intoField(onTakeOffered) }
          }
          typed={
            !typedOver || changed !== undefined
              ? undefined
              : onTakeOffered === undefined
                ? "Reopen the issue to use your kept draft, or discard it."
                : "Use or discard your kept draft first."
          }
        />
      )}
      {/* A reply kept for a comment or message the host no longer lists: the Reply its kept line
          would name is gone with it, so the reply itself waits here, with the words that say whose
          comment or message it answered, and Discard my draft. At the owner's own address, Use it
          here puts it in the field as a message that answers none, once nothing of the field's
          own is there to be written over, and the words say when it waits for that. */}
      {gone.map((away) => (
        <DraftOffer
          body={away.draft.body}
          drop={{ label: "Discard my draft", press: intoField(() => onDropGone(away)) }}
          key={away.replyTo.id}
          lead={
            <GoneReply
              actor={away.replyTo.actor}
              parentKind={away.replyTo.parentKind}
              waits={goneTakes === "once empty"}
            />
          }
          replacement={away.draft.replacement}
          take={
            goneTakes === "here"
              ? { label: "Use it here", press: intoField(() => onTakeGone(away)) }
              : undefined
          }
        />
      ))}
    </>
  );
}
