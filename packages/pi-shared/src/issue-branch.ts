import { LEGION_BRANCH_PATTERN } from "@legion/contracts";

/**
 * The branch of the issue a Legion pane works, read from the pane's environment: the jj bookmark
 * its completion check holds the workspace to, `<branch>@origin` at the remote, and the branch the
 * pane's messages name.
 *
 * `LEGION_BRANCH` is the branch the daemon stores for the issue, which both runtimes set on every
 * worker pane and never on the controller's, and which an operator's pane environment cannot set,
 * as it cannot set `LEGION_ISSUE`: `legion/<KEY>` for an issue recorded before the daemon stored
 * branches, and `legion/` and 16 lower-case hexadecimal characters for one recorded since.
 */
export const LEGION_BRANCH_VARIABLE = "LEGION_BRANCH";

/**
 * The branch of `issue`, the issue the pane whose environment is `env` works (its `LEGION_ISSUE`,
 * which the caller has read): `LEGION_BRANCH`. A value of another shape than an issue branch's
 * (`LEGION_BRANCH_PATTERN`) throws naming the variable, since a value carrying `/` or `#` would
 * name another reference or path; the shape validates the name and no issue is derived from it.
 *
 * Absent or empty, the branch is `legion/<issue>`. That fallback serves a pane of an older daemon,
 * which sets no `LEGION_BRANCH` and named every issue's branch so, and it is removed once every pod
 * runs a daemon that sets the variable.
 */
export function paneIssueBranch(
  env: Readonly<Record<string, string | undefined>>,
  issue: string
): string {
  const branch = env[LEGION_BRANCH_VARIABLE];
  if (branch === undefined || branch === "") return `legion/${issue}`;
  if (!LEGION_BRANCH_PATTERN.test(branch)) {
    throw new Error(
      `${LEGION_BRANCH_VARIABLE} ${JSON.stringify(branch)} is not an issue branch like legion/0123456789abcdef`
    );
  }
  return branch;
}
