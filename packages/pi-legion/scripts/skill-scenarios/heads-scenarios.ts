import { readFileSync } from "node:fs";

/** A scenario's or a case's name: lower-case words joined by single hyphens. */
const NAME = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

/** The heads scenarios' worlds, each line of heads-scenarios.txt one, `<scenario> [<case>]`, in the
 * order a batch runs them: the one catalog that rig.sh, heads-world.ts, seed.ts, score.ts and
 * hidden-proof.ts read. A world with no case is its scenario's own, whose run names none. A line of
 * more than two names, or a name that is not lower-case words and hyphens, is refused here, since
 * rig.sh's `read` would take it otherwise and score.ts puts each name into a pattern. */
const WORLDS: readonly (readonly [scenario: string, which: string])[] = readFileSync(
  new URL("./heads-scenarios.txt", import.meta.url),
  "utf8"
)
  .split("\n")
  .filter((line) => line.trim() !== "")
  .map((line) => {
    const names = line.trim().split(/\s+/);
    if (names.length > 2 || !names.every((name) => NAME.test(name)))
      throw new Error(`heads-scenarios.txt: '${line}' is not <scenario> [<case>]`);
    return [names[0] ?? "", names[1] ?? ""] as const;
  });

/** The scenarios whose runs get the heads fixture and may hide Dispatch's computed state. */
export const HEADS_SCENARIOS = Object.freeze([...new Set(WORLDS.map(([scenario]) => scenario))]);

/** Whether `scenario` gets the heads fixture. */
export function isHeadsScenario(scenario: string): boolean {
  return HEADS_SCENARIOS.includes(scenario);
}

/** The case of each world of `scenario`, in the catalog's order: "" for the scenario's own world.
 * None for a scenario the catalog does not list. */
export function headsCases(scenario: string): readonly string[] {
  return WORLDS.filter(([known]) => known === scenario).map(([, which]) => which);
}
