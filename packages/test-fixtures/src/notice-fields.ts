// Reading the notices a plugin writes into its own session (`noticeText` of
// `@legion/envoy-client/notice-text`). It lives in this package because the notices are built in
// envoy-client, pi-shared, pi-envoy and pi-legion, each of which tests its own by the same rule:
// each notice states the fields of its kind, and no field holds words that are the same in every
// notice of its kind.

/** A notice kind as its package declares it: the key its lines sit under, its kind, its fields. */
export interface DeclaredNotice {
  readonly root: string;
  readonly kind: string;
  readonly fields: readonly string[];
}

/** The longest run of characters two renderings of one field may share. */
export const SHARED_RUN_LIMIT = 40;

/** A `key: value` line under a notice's root, as TOON writes a field (a list as `key[n]: …`). */
const FIELD_LINE = /^ {2}([A-Za-z_][A-Za-z0-9_]*)(?:\[\d+\])?: (.*)$/;

/** The longest run of characters that both `a` and `b` hold. */
function sharedRun(a: string, b: string): string {
  let best = "";
  let previous = new Array<number>(b.length + 1).fill(0);
  for (let i = 1; i <= a.length; i += 1) {
    const current = new Array<number>(b.length + 1).fill(0);
    for (let j = 1; j <= b.length; j += 1) {
      if (a[i - 1] !== b[j - 1]) continue;
      const run = (previous[j - 1] ?? 0) + 1;
      current[j] = run;
      if (run > best.length) best = a.slice(i - run, i);
    }
    previous = current;
  }
  return best;
}

/**
 * The fields of a notice's text by key, each value as the text writes it (TOON quotes a value
 * that holds a new line or would read as structure). Throws on a line below the root that is no
 * `key: value` line.
 */
export function noticeFields(text: string): Record<string, string> {
  const fields: Record<string, string> = {};
  for (const line of text.split("\n").slice(1)) {
    const match = FIELD_LINE.exec(line);
    const key = match?.[1];
    const value = match?.[2];
    if (key === undefined || value === undefined) {
      throw new Error(`${JSON.stringify(line)} is no key: value line of a notice`);
    }
    fields[key] = value;
  }
  return fields;
}

/**
 * What two renderings of one notice of `declared`, made from different values in every field,
 * break of the rule that a notice states the fields of its kind. Each rendering opens with its
 * root line, and every line after it is a `key: value` line whose key the kind declares (or
 * `notice`), with no key twice and one line naming the kind (`notice: <kind>`). Every declared
 * field is stated by at least one of the two, the two never hold the same value of a field, and
 * no field of the two shares a run of more than `SHARED_RUN_LIMIT` characters, which is what a
 * sentence the same in every notice of the kind would share. Empty when they break none of it.
 */
export function noticeProblems(
  declared: DeclaredNotice,
  renderings: readonly [string, string]
): string[] {
  const problems: string[] = [];
  const named = new Set(["notice", ...declared.fields]);
  const parse = (text: string, which: string): Map<string, string> => {
    const [root, ...lines] = text.split("\n");
    if (root !== `${declared.root}:`) {
      problems.push(`${which}: its first line is ${JSON.stringify(root)}, not "${declared.root}:"`);
    }
    const values = new Map<string, string>();
    for (const line of lines) {
      const match = FIELD_LINE.exec(line);
      const key = match?.[1];
      const value = match?.[2];
      if (key === undefined || value === undefined) {
        problems.push(`${which}: ${JSON.stringify(line)} is no key: value line`);
        continue;
      }
      if (!named.has(key))
        problems.push(`${which}: ${key} is no field that ${declared.kind} declares`);
      if (values.has(key)) problems.push(`${which}: ${key} comes twice`);
      values.set(key, value);
    }
    if (values.get("notice") !== declared.kind) {
      problems.push(`${which}: no line names its kind (notice: ${declared.kind})`);
    }
    return values;
  };
  const first = parse(renderings[0], `${declared.kind}, first rendering`);
  const second = parse(renderings[1], `${declared.kind}, second rendering`);
  for (const field of declared.fields) {
    if (!first.has(field) && !second.has(field)) {
      problems.push(`${declared.kind}: neither rendering states ${field}`);
    }
  }
  for (const [key, a] of first) {
    const b = second.get(key);
    if (key === "notice" || b === undefined) continue;
    if (a === b) problems.push(`${declared.kind}: both renderings hold the same ${key}: ${a}`);
    const run = sharedRun(a, b);
    if (run.length > SHARED_RUN_LIMIT) {
      problems.push(
        `${declared.kind}: the two renderings of ${key} share a run of ${run.length} characters: ${JSON.stringify(run)}`
      );
    }
  }
  return problems;
}
