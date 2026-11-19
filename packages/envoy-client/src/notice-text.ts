import { encode } from "@toon-format/toon";

/**
 * A notice that a plugin writes into its own session: the facts of one event, in the form that
 * TOON's `encode` gives an object, as a card is rendered (`delivery.ts`). The lines sit under the
 * key that names whose notice it is: `envoy` for Envoy's notices, `legion` for Legion's. The first
 * field names the kind (`notice: <kind>`). Each other field is one that the kind declares, stated
 * once, in the order of the declaration. A field holds a fact of the event: a word, a number, an
 * id or a command. No field holds words that are the same in every notice of its kind, except
 * `step`: the one step its reader takes on every notice of the kind, which no prompt or skill of
 * that reader gives. It is stated last, after the facts, and the test "every plugin notice states
 * its fields" checks it.
 *
 * Each kind is declared once, where its notice is built (`noticeKind`), and `noticeText` writes a
 * notice of it from a value for each field the kind declares.
 */
export interface NoticeKind<Field extends string> {
  /** The key the notice's lines sit under. */
  readonly root: "envoy" | "legion";
  /** The kind, which the first field states (`notice: <kind>`). */
  readonly kind: string;
  /** The fields a notice of the kind states after its kind, in the order it states them. */
  readonly fields: readonly Field[];
  /** The step its reader takes on every notice of the kind, stated last (`step: <words>`). */
  readonly step?: string;
}

/** The value of one field. `undefined` leaves the field out of this notice. */
export type NoticeValue = string | number | boolean | undefined;

/**
 * Declares a notice kind: the one place that names its root, its kind, its fields and its step. A
 * kind declared with a step has one.
 */
export function noticeKind<const Field extends string>(
  root: "envoy" | "legion",
  kind: string,
  fields: readonly Field[]
): NoticeKind<Field>;
export function noticeKind<const Field extends string>(
  root: "envoy" | "legion",
  kind: string,
  fields: readonly Field[],
  step: string
): NoticeKind<Field> & { readonly step: string };
export function noticeKind<const Field extends string>(
  root: "envoy" | "legion",
  kind: string,
  fields: readonly Field[],
  step?: string
): NoticeKind<Field> {
  return step === undefined ? { root, kind, fields } : { root, kind, fields, step };
}

/**
 * The text of one notice of `kind`. `values` names every field the kind declares, so a builder
 * cannot leave a fact out by accident; a field whose value is `undefined` is not stated. The kind's
 * step, if it has one, follows the fields. TOON quotes a value that holds a new line or would read
 * as structure, so a value that a sender controls (a summary, an error) never starts a field of
 * its own.
 */
export function noticeText<Field extends string>(
  kind: NoticeKind<Field>,
  values: { readonly [Name in Field]: NoticeValue }
): string {
  const fields: Record<string, string | number | boolean> = { notice: kind.kind };
  for (const field of kind.fields) {
    const value = values[field];
    if (value !== undefined) fields[field] = value;
  }
  return encode({ [kind.root]: kind.step === undefined ? fields : { ...fields, step: kind.step } });
}
