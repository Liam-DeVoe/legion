import { createHash } from "node:crypto";

/**
 * The verdicts the tool keeps between runs, each under a key that holds no text: the first 16
 * bytes, in hex, of the sha256 of the item id, the sha256 of the paragraph and the index of the
 * occurrence in that paragraph. The document's version is no part of the key, so a new version
 * that leaves a paragraph as it was (the tool's own write of another paragraph among them) leaves
 * that paragraph's verdicts standing. A verdict's value is one letter, so one entry of the state
 * costs at most 48 bytes as JSON (`"<32 hex>":"t",` is 39).
 */

/** Why the judge left an occurrence: its `allowed` answer, or `judge-refused` where both its
 *  answers failed (an occurrence for a person to change by hand, never an allowed place, which no
 *  later run asks about while its text stands). */
export const VERDICT_CODES = {
  quote: "q",
  "word-as-word": "w",
  "other-team-technical": "t",
  "third-party-name": "p",
  record: "r",
  "judge-refused": "j",
} as const;

export type VerdictName = keyof typeof VERDICT_CODES;
export type VerdictCode = (typeof VERDICT_CODES)[VerdictName];

/** The `allowed` answers the judge may give, in the order its prompt lists them. */
export const ALLOWED_NAMES = [
  "quote",
  "word-as-word",
  "other-team-technical",
  "third-party-name",
  "record",
] as const satisfies readonly VerdictName[];

export type AllowedName = (typeof ALLOWED_NAMES)[number];

const NAME_OF_CODE = new Map<string, VerdictName>(
  Object.entries(VERDICT_CODES).map(([name, code]) => [code, name as VerdictName])
);

/** The name a stored code stands for, or undefined for a letter no verdict uses. */
export function verdictName(code: string): VerdictName | undefined {
  return NAME_OF_CODE.get(code);
}

/** The sha256 of a text, in hex. */
export function sha256Hex(text: string): string {
  return createHash("sha256").update(text, "utf8").digest("hex");
}

/** The key of one occurrence's verdict: its item, its paragraph's text and its place there. */
export function verdictKey(itemId: string, paragraph: string, occurrence: number): string {
  const material = `${itemId}\n${sha256Hex(paragraph)}\n${occurrence}`;
  return createHash("sha256").update(material, "utf8").digest("hex").slice(0, 32);
}

/** The pattern every key matches, which the state's reader holds each key to. */
export const VERDICT_KEY = /^[0-9a-f]{32}$/;
