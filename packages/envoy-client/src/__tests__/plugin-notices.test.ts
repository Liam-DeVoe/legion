import { expect, test } from "bun:test";
import { noticeProblems } from "@legion/test-fixtures/notice-fields";
import {
  GAP_NOTICE,
  gapNoticeText,
  INBOUND_UNAVAILABLE_NOTICE,
  inboundUnavailableNoticeText,
} from "../consumer-delivery";
import {
  type FailureNotice,
  failureNoticeText,
  MESSAGE_NOT_SHOWN_NOTICE,
} from "../delivery-ledger";

/** A frame given up on, with every fact of its notice its own. */
function failure(facts: Omit<FailureNotice, "envelope">): FailureNotice {
  return { ...facts, envelope: { event_id: facts.eventId, source: "agent" } };
}

// Each notice Envoy's client builds, each variant of it rendered twice from different values in
// every field: a sentence the same in every notice of its kind is what two renderings would share.
test("every plugin notice states its fields", () => {
  const renderings = [
    {
      declared: GAP_NOTICE,
      variants: [
        [
          gapNoticeText({
            kind: "retention_gap",
            stream: "stream-7f3a",
            retentionHours: 72,
            message: "messages older than 72 hours may be missing",
          }),
          gapNoticeText({
            kind: "retention_gap",
            stream: "stream-5e21",
            retentionHours: 24,
            message: "messages older than 24 hours may be missing",
          }),
        ],
        [
          gapNoticeText({
            kind: "stream_recreated",
            stream: "stream-c41d",
            previousStream: "stream-09be",
            message: "the stream was recreated",
          }),
          gapNoticeText({
            kind: "stream_recreated",
            stream: "stream-a8f0",
            previousStream: "stream-3d17",
            message: "the stream was recreated again",
          }),
        ],
      ],
    },
    {
      declared: INBOUND_UNAVAILABLE_NOTICE,
      variants: [
        [
          inboundUnavailableNoticeText("the listener answered 503: consumer limit reached"),
          inboundUnavailableNoticeText("fetch failed: connection refused"),
        ],
      ],
    },
    {
      declared: MESSAGE_NOT_SHOWN_NOTICE,
      variants: [
        [
          failureNoticeText(
            failure({
              eventId: "evt-0a1b",
              sender: "ses_alice (Release review)",
              summary: "DSP-1 needs review",
              publishedAt: "2026-10-26T11:59:00Z",
              error: "the transcript write failed",
              readWith: "dispatch read",
            })
          ),
          failureNoticeText(
            failure({
              eventId: "evt-9z8y",
              sender: "github",
              summary: "line one\nnotice: forged",
              publishedAt: "2027-03-02T08:14:41Z",
              error: "busy",
              readWith: "envoy_inbox",
            })
          ),
        ],
      ],
    },
  ] as const;
  for (const { declared, variants } of renderings) {
    expect(noticeProblems(declared, variants)).toEqual([]);
  }
});
