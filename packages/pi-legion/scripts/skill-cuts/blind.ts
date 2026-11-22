/**
 * The judge sees no skill name (the plan's D3): in a run with an arm of ours, before the judge
 * reads a case's criterion, its line, its history and a step (judge.ts), the case's skill name
 * (theirs) and ours become THE_SKILL. So `skill://<name>` becomes `skill://the-skill`, a path
 * part `/<name>/` becomes `/the-skill/`, and the name as a whole word becomes `the-skill`: three
 * forms of one rule, a name that no letter, digit, `_` or `-` touches on either side. A longer
 * name that holds the name as a part of a word (`alphabet`, `alpha-extra` for `alpha`) is not
 * changed. The same names are given for every arm of a run, so the judge cannot tell the arm of
 * their skill from the arm of ours. A run with `--new` or with no skill compared gives none, and
 * its judge reads the case's text as it is.
 */

/** What each skill name becomes before the judge reads it. */
export const THE_SKILL = "the-skill";

/** `text` with each of `names`, where no letter, digit, `_` or `-` touches it, as THE_SKILL. */
export function blindSkillNames(text: string, names: readonly string[]): string {
  const words = [...new Set(names.filter((name) => name !== ""))]
    .sort((a, b) => b.length - a.length)
    .map((name) => name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
  if (words.length === 0) return text;
  return text.replace(new RegExp(`(?<![\\w-])(?:${words.join("|")})(?![\\w-])`, "g"), THE_SKILL);
}
