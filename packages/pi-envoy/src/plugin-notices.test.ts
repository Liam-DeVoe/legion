import { expect, test } from "bun:test";
import type { NoticeKind } from "@legion/envoy-client/notice-text";
import { noticeProblems } from "@legion/test-fixtures/notice-fields";
import {
  SESSION_ID_CHANGED_NOTICE,
  sessionIdChangedText,
  UNREAD_MESSAGES_NOTICE,
  unreadMessagesText,
} from "./envoy-notices";

/**
 * `rendering` with the step its kind keeps taken off. The step is the notice's last line
 * (`step: <words>`), the same in every notice of the kind, so the field rule reads only the facts
 * before it.
 */
function withoutStep(declared: NoticeKind<string>, rendering: string): string {
  const step = `\n  step: ${declared.step}`;
  expect(declared.step).toBeTruthy();
  expect(rendering).toEndWith(step);
  return rendering.slice(0, -step.length);
}

// Each notice the Envoy extension builds for its own session, each variant of it rendered twice
// from different values in every field: a sentence the same in every notice of its kind is what
// two renderings would share. Each states the step its kind keeps, last.
test("every plugin notice states its fields", () => {
  const renderings = [
    {
      declared: SESSION_ID_CHANGED_NOTICE,
      variants: [
        [
          sessionIdChangedText("ses_ew_parent", "ses_ew_fork"),
          sessionIdChangedText("019a4c1e-7b2d", "019a4c2f-0e91"),
        ],
      ],
    },
    {
      declared: UNREAD_MESSAGES_NOTICE,
      variants: [[unreadMessagesText(1), unreadMessagesText(3)]],
    },
  ] as const;
  for (const { declared, variants } of renderings) {
    const facts = variants.map(
      ([a, b]) => [withoutStep(declared, a), withoutStep(declared, b)] as const
    );
    expect(noticeProblems(declared, facts)).toEqual([]);
  }
});
