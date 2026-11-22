// A copy of this directory for the tests of its catalogs (heads-scenarios.txt, askgate-cases.txt):
// a catalog edited in the copy reaches the copy's readers and no file of the checkout.
import { cpSync, mkdirSync, readdirSync, readFileSync, symlinkSync } from "node:fs";
import path from "node:path";
import { scriptFunctions } from "../../../../scripts/e2e/lib/script-functions";

/** The checkout this directory is in. */
const ROOT = path.resolve(import.meta.dir, "../../../..");

/** A tree in `into` in which this directory is a copy and every other entry on the way down to it
 * is a link to the checkout's own. Returns the copy, whose top of the tree is four levels up. */
export function copyOfThisDirectory(into: string): string {
  let from = ROOT;
  let to = into;
  for (const step of path.relative(ROOT, import.meta.dir).split(path.sep)) {
    for (const entry of readdirSync(from))
      if (entry !== step) symlinkSync(path.join(from, entry), path.join(to, entry));
    from = path.join(from, step);
    to = path.join(to, step);
    mkdirSync(to);
  }
  cpSync(from, to, { recursive: true });
  return to;
}

/** Every function the copy's rig.sh defines, as scriptFunctions extracts each, whichever helper of
 * it reads a catalog: a test stubs the commands that would start services or agents after them. */
export function rigFunctions(copy: string): string {
  const rig = path.join(copy, "rig.sh");
  const fn = scriptFunctions(rig);
  return [...readFileSync(rig, "utf8").matchAll(/^([A-Za-z_][A-Za-z0-9_]*)\(\) \{/gm)]
    .map((found) => fn(found[1] ?? ""))
    .join("\n");
}
