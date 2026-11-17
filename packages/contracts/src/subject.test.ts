import { expect, test } from "bun:test";
import { legionControllerNoticeSubject, uncapturedTopicWarning } from "./subject";

test("builds the Go daemon's controller notice topic, byte for byte notify.ControllerTopic", () => {
  expect(legionControllerNoticeSubject("legion")).toBe("notifications.legion.legion.controller");
});

test("the listener's warning for an uncaptured topic says it is registered and waits for a stream subject", () => {
  // The listener's warning is generated from this builder (contracts.UncapturedTopicWarning in
  // packages/envoy/internal/contracts/generated.go), which `bun run check:go` holds to it; this
  // pins the sentence itself. It must not say the topic was left out: the listener keeps it.
  expect(uncapturedTopicWarning("notifications.lxspec.probe", "ses_a")).toBe(
    "notifications.lxspec.probe is registered, but no stream subject captures it yet, so nothing is delivered to ses_a on it until one does; to hear what agents publish, follow a notifications.team.<name> topic"
  );
});
