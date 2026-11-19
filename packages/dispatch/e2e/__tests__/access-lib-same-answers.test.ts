/** How `e2e/acceptance/access-lib.sh`'s `same_answers` judges a hidden request beside a missing
 *  one (c-private-unseen), each answer kept by `keep_answer` and masked with its own fill. Two
 *  answers sent byte for byte alike are the same answer: a caller cannot tell them apart, whatever
 *  the masks would make of them. A refusal that names the caller's own token list names PRIV in
 *  both answers, and the hidden answer's mask, which replaces PRIV, would otherwise part them.
 *  Answers sent otherwise are compared masked, so a leak still fails.
 *
 *  Each row sources the library alone in bash, with a work directory of its own and no server, and
 *  reads the count of answers that differed. */

import { expect, test } from "bun:test";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";

const library = fileURLToPath(new URL("../acceptance/access-lib.sh", import.meta.url));
const scratch = temporaryDirectories("access-lib-same-answers");

/** The count of differing answers `same_answers` reaches for one hidden and one missing answer,
 *  each a 403 whose body is given, masked with the fills `key PRIV` and `key ZZNONE`. */
function differed(hidden: string, missing: string): number {
  const work = scratch();
  mkdirSync(join(work, "answers"));
  const script = `
    set -euo pipefail
    work=$1
    run_epoch=$(date +%s)
    source "$2"
    : >"$work/ids-main.tsv"
    printf 'key\\tPRIV\\n' >"$work/fill-hidden"
    printf 'key\\tZZNONE\\n' >"$work/fill-missing"
    status=403
    printf '%s' "$3" >"$work/body"
    keep_answer hidden main "$work/fill-hidden"
    printf '%s' "$4" >"$work/body"
    keep_answer missing main "$work/fill-missing"
    same_answers "the row" hidden missing
    printf '%s' "$differed"
  `;
  const run = Bun.spawnSync(
    ["bash", "-c", script, "same-answers", work, library, hidden, missing],
    {
      stdout: "pipe",
      stderr: "pipe",
    }
  );
  expect(run.stderr.toString()).toBe("");
  expect(run.exitCode).toBe(0);
  return Number(run.stdout.toString());
}

const refusal = (key: string) =>
  `{"code":"PROJECT_SCOPE_DEPLOYMENT","error":"PUT /api/v1/projects/{key}/legion/daemon belongs to the deployment: this request's credential may write project ${key} and nothing else"}`;

test("two answers sent alike are the same, though each one's mask would part them", () => {
  // Both refusals name the caller's token list, PRIV, which the hidden answer's fill names too.
  expect(differed(refusal("PRIV"), refusal("PRIV"))).toBe(0);
});

test("two answers that name each request's own key are the same once each is masked", () => {
  expect(differed(refusal("PRIV"), refusal("ZZNONE"))).toBe(0);
});

test("an answer that names a hidden object a missing one does not is a difference", () => {
  expect(
    differed('{"title":"The issue of PRIV","key":"PRIV"}', '{"code":"NOT_FOUND","key":"ZZNONE"}')
  ).toBe(1);
});
