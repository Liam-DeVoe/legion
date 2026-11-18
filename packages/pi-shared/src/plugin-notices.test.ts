import { expect, test } from "bun:test";
import { noticeProblems } from "@legion/test-fixtures/notice-fields";
import { restartMessage, type StallCall, TURN_ENDED_NOTICE } from "./stall-watchdog";
import {
  WORKSPACE_RECREATED_NOTICE,
  WORKSPACE_RECREATED_VARIABLE,
  WORKSPACE_RESTORED_VARIABLE,
  workspaceRecreatedNotice,
} from "./workspace-recreated";

/** An open call of `toolName` with the id `toolCallId`, as the watch records it. */
function call(toolName: string, toolCallId: string): StallCall {
  return {
    toolCallId,
    toolName,
    input: {},
    startedAt: 0,
    deadlineMs: undefined,
    step: 1,
    steppedAt: undefined,
    told: undefined,
  };
}

// Each notice this package builds for Legion's plugins, rendered twice from different values in
// every field: a sentence the same in every notice of its kind is what the two would share.
test("every plugin notice states its fields", () => {
  const workspace = (issue: string, restored: string) =>
    workspaceRecreatedNotice(
      { [WORKSPACE_RECREATED_VARIABLE]: "true", [WORKSPACE_RESTORED_VARIABLE]: restored },
      issue
    )?.content ?? "";
  const renderings = [
    {
      declared: WORKSPACE_RECREATED_NOTICE,
      texts: [workspace("REPO-43", "true"), workspace("CORE-7", "false")],
    },
    {
      declared: TURN_ENDED_NOTICE,
      texts: [
        restartMessage(call("eval", "toolu_01Aa"), {
          rule: { kind: "deadline", deadlineMs: 300_000, graceMs: 60_000 },
          openMs: 412_400,
          pastMs: 52_400,
        }),
        restartMessage(call("bash", "toolu_02Bb"), {
          rule: { kind: "bound", boundMs: 900_000 },
          openMs: 1_000_200,
          pastMs: 100_200,
        }),
      ],
    },
  ] as const;
  for (const { declared, texts } of renderings) {
    expect(noticeProblems(declared, texts)).toEqual([]);
  }
});
