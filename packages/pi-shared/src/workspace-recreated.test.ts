import { expect, test } from "bun:test";
import {
  WORKSPACE_RECREATED_VARIABLE,
  WORKSPACE_RESTORED_VARIABLE,
  workspaceRecreatedNotice,
} from "./workspace-recreated";

/** A new issue's branch as the daemon allocates it, with no key in it. */
const BRANCH = "legion/3f9a1c2b7d4e5f60";
/** A pane whose launcher recreated its workspace, told its issue's branch. */
const recreated = { [WORKSPACE_RECREATED_VARIABLE]: "true", LEGION_BRANCH: BRANCH };

test("a launcher that recreated the workspace earns a notice naming the issue's branch", () => {
  const notice = workspaceRecreatedNotice(recreated, "LEGION-43");
  expect(notice?.content).toContain(`it holds what was pushed to ${BRANCH} (`);
  expect(notice?.content).toContain("anything you had not pushed is gone");
  expect(notice?.content).toContain("dispatch handoff-read --issue LEGION-43 --phase <phase>");
  expect(notice?.id).toMatch(/^[0-9a-f-]{36}$/);
  // A released pod's workspace, its unpushed work put back, names the same branch.
  const restored = { ...recreated, [WORKSPACE_RESTORED_VARIABLE]: "true" };
  expect(workspaceRecreatedNotice(restored, "LEGION-43")?.content).toContain(
    `It holds what was pushed to ${BRANCH} and the tracked work`
  );
});

// The fallback, until every pod runs a daemon that sets `LEGION_BRANCH`.
test("a pane of an older daemon, which names no branch, is told legion/<issue>", () => {
  const notice = workspaceRecreatedNotice({ [WORKSPACE_RECREATED_VARIABLE]: "true" }, "LEGION-43");
  expect(notice?.content).toContain("it holds what was pushed to legion/LEGION-43 (");
});

// The notice is made as the session starts, where a throw would end that start, so a
// LEGION_BRANCH that is no issue branch is named in the notice rather than thrown.
test("a LEGION_BRANCH that is no issue branch is named in the notice, which still goes", () => {
  const environment = { ...recreated, LEGION_BRANCH: "legion/../x" };
  expect(workspaceRecreatedNotice(environment, "LEGION-43")?.content).toContain(
    `it holds what was pushed to your issue's branch, which this pane cannot name (LEGION_BRANCH "legion/../x" is not an issue branch like legion/0123456789abcdef) (`
  );
});

// The id is what a host tells this process's copies of the notice by, so two notices never share
// one.
test("each notice carries an id of its own", () => {
  const first = workspaceRecreatedNotice(recreated, "LEGION-43");
  const second = workspaceRecreatedNotice(recreated, "LEGION-43");
  expect(first?.id).not.toBe(second?.id);
});

test("a launcher that recreated nothing says nothing, and only `true` is recreated", () => {
  expect(workspaceRecreatedNotice({ LEGION_BRANCH: BRANCH }, "LEGION-43")).toBeUndefined();
  for (const value of ["", "false", "TRUE", "1"]) {
    expect(
      workspaceRecreatedNotice({ ...recreated, [WORKSPACE_RECREATED_VARIABLE]: value }, "LEGION-43")
    ).toBeUndefined();
  }
});
