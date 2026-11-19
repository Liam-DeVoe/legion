import { isStrictPrefix, type KeyCombo, parseChord, sameCombo } from "./keymap";
import {
  catalogueEntry,
  KEYMAP_ACTIONS,
  type KeymapActionId,
  type KeySurface,
  keepsOwnKeys,
  keysOfAction,
  type PersonKeys,
} from "./keymap-catalogue";

/**
 * What the check says of a person's keys for one action (R14): refused, with the reason, which
 * names the action that holds the key; or accepted, with a notice for each global action the keys
 * stop on a page. The registry finds a prefix only among the bindings of the pages that are open
 * (`checkPrefixConflicts`, `keymap.ts`) and no duplicate at all, so this check reads the whole
 * catalogue: every action of every scope, whether or not its page is open.
 */
export type KeyVerdict =
  | { readonly kind: "accepted"; readonly notices: readonly string[] }
  | { readonly kind: "refused"; readonly message: string; readonly other?: KeymapActionId };

/** Each part of a page, as a notice names the page. */
const PAGE_NAMES: Record<KeySurface, string> = {
  agents: "the Agents page",
  architecture: "the Architecture page",
  board: "the Board",
  delivery: "the Delivery page",
  document: "a document",
  global: "every page",
  inbox: "the Inbox",
  issue: "the issue page",
  "project-list": "a project's lists",
  "project-page": "a project's pages",
};

/** The surfaces drawn at the same time, whose keys a reader presses on one page: the issue page
 *  with its document, and a project's own keys with each of its tabs. */
const DRAWN_TOGETHER: readonly (readonly [KeySurface, KeySurface])[] = [
  ["issue", "document"],
  ["project-page", "project-list"],
  ["project-page", "board"],
  ["project-page", "architecture"],
];

/** Keys that keep their own actions: a bare digit, an arrow, Enter, Escape, Tab and Space, with
 *  or without Shift (a digit encodes its Shift in the character it types). */
const RESERVED_KEYS: Record<string, true> = {
  ArrowDown: true,
  ArrowLeft: true,
  ArrowRight: true,
  ArrowUp: true,
  Enter: true,
  Escape: true,
  Space: true,
  Tab: true,
};

/** The keys the browser keeps for itself with Ctrl or Cmd: close, new tab, new window, the
 *  address bar, reload and quit, and Tab, which moves between tabs. */
const BROWSER_KEYS: Record<string, true> = {
  l: true,
  n: true,
  q: true,
  r: true,
  t: true,
  tab: true,
  w: true,
};

function isReserved(combo: KeyCombo): boolean {
  if (combo.alt || combo.control || combo.meta || combo.mod) return false;
  return /^[0-9]$/.test(combo.key) || RESERVED_KEYS[combo.key] === true;
}

function isBrowsers(combo: KeyCombo): boolean {
  return (
    (combo.control || combo.meta || combo.mod) && BROWSER_KEYS[combo.key.toLowerCase()] === true
  );
}

function sameChord(a: readonly KeyCombo[], b: readonly KeyCombo[]): boolean {
  return a.length === b.length && a.every((combo, index) => sameCombo(combo, b[index] as KeyCombo));
}

/** Each key's presses as `chordOf` read them first: a check reads every action's keys, and the
 *  same text always gives the same presses. The keys are the catalogue's and the ones a person
 *  records, so the map stays small. */
const chords = new Map<string, readonly KeyCombo[] | null>();

/** The presses of `key`, or null where the text names no key. */
function chordOf(key: string): readonly KeyCombo[] | null {
  const known = chords.get(key);
  if (known !== undefined) return known;
  let chord: readonly KeyCombo[] | null;
  try {
    chord = parseChord(key);
  } catch {
    chord = null;
  }
  chords.set(key, chord);
  return chord;
}

function refused(message: string, other?: KeymapActionId): KeyVerdict {
  return { kind: "refused", message, other };
}

/**
 * Whether `action` may take `keys` in place of the keys it has, with every other action on its
 * keys in `personKeys` or its defaults: the check Settings runs on every key a person gives.
 * Refused: a key that is no key; a key that keeps its own action (`RESERVED_KEYS`, a digit) or
 * that the browser keeps; any key for an action whose keys all keep their own action, since its
 * run reads the key pressed; a key listed twice, or two of the action's own keys of which one is
 * the first press of the other (`g` beside `g i`, while `g i` beside `g a` passes); a key another
 * action of the same surface, or of a surface drawn with it, has; and a key that is the first
 * press of another action's key, or whose first press is another action's key, in any scope. A
 * key a global action has, taken by a page's action, or the other way round, is accepted with
 * the notice that the global action does not work on that page, as the Agents page's `/` and `i`
 * are today. The notice says more than the registry does: where the page's action does not apply
 * (its `when` is false), `resolve` in `keymap.ts` passes the key on to the global action.
 */
export function refusalOf(
  action: KeymapActionId,
  keys: readonly string[],
  personKeys: PersonKeys
): KeyVerdict {
  const entry = catalogueEntry(action);
  if (keys.length > 0 && keepsOwnKeys(entry)) {
    return refused(`${entry.label} keeps its own keys.`);
  }
  const taken: { chord: readonly KeyCombo[]; key: string }[] = [];
  for (const key of keys) {
    const chord = chordOf(key);
    if (chord === null) return refused(`${key} is not a key.`);
    if (chord.some(isReserved)) {
      return refused(
        `${key} is not free: the digits, the arrows, Enter, Escape, Tab and Space keep their own actions.`
      );
    }
    if (chord.some(isBrowsers)) return refused(`${key} is the browser's own key.`);
    for (const earlier of taken) {
      if (sameChord(earlier.chord, chord)) return refused(`${key} is listed twice.`);
      if (isStrictPrefix(earlier.chord, chord)) {
        return refused(`${earlier.key} is the first press of ${key}, both keys of ${entry.label}.`);
      }
      if (isStrictPrefix(chord, earlier.chord)) {
        return refused(`${key} is the first press of ${earlier.key}, both keys of ${entry.label}.`);
      }
    }
    taken.push({ chord, key });
  }

  const notices = new Set<string>();
  for (const other of KEYMAP_ACTIONS) {
    if (other === action) continue;
    const otherEntry = catalogueEntry(other);
    for (const otherKey of keysOfAction(other, personKeys)) {
      const otherChord = chordOf(otherKey);
      if (otherChord === null) continue;
      for (const { chord, key } of taken) {
        if (isStrictPrefix(chord, otherChord)) {
          return refused(`${key} is the first press of ${otherKey}, ${otherEntry.label}.`, other);
        }
        if (isStrictPrefix(otherChord, chord)) {
          return refused(`${otherKey}, ${otherEntry.label}, is the first press of ${key}.`, other);
        }
        if (!sameChord(chord, otherChord)) continue;
        if (
          entry.surface === otherEntry.surface ||
          DRAWN_TOGETHER.some(
            ([one, two]) =>
              (one === entry.surface && two === otherEntry.surface) ||
              (one === otherEntry.surface && two === entry.surface)
          )
        ) {
          return refused(`${key} is already the key of ${otherEntry.label}.`, other);
        }
        if (entry.surface === "global") {
          notices.add(`${entry.label} does not work on ${PAGE_NAMES[otherEntry.surface]}.`);
        } else if (otherEntry.surface === "global") {
          notices.add(`${otherEntry.label} does not work on ${PAGE_NAMES[entry.surface]}.`);
        }
      }
    }
  }
  return { kind: "accepted", notices: [...notices] };
}
