import { envoyNoticeText } from "./delivery-ledger";
import { messageFor } from "./errors";

/**
 * What a session is told when the listener can give it no consumer, worded once for every host:
 * its registration was refused, by one of the reasons `ErrorWireSchema.reason` lists
 * (`transport.ts`), or it failed, so nothing reaches it until a later registration is accepted.
 * `why` is the listener's own message, which carries its machine-readable reason. Each host
 * shows it where it shows a gap notice and says once per outage.
 */
export function inboundUnavailableNoticeText(why: string): string {
  return envoyNoticeText({
    notice: "Envoy inbound unavailable",
    error: why,
    detail:
      "The listener gave this session no durable consumer, which is the only path an Envoy message reaches it by, so it receives none. Everything else works, and the session registers again at every heartbeat; what the stream stored meanwhile arrives once a registration is accepted.",
  });
}

/**
 * What a session that was told its inbound is unavailable is told once a registration is
 * accepted again, worded once for every host: without it the outage notice is the last word the
 * session has, and a session that recovered a few seconds later reads itself as deaf for its life.
 */
export function inboundRestoredNoticeText(): string {
  return envoyNoticeText({
    notice: "Envoy inbound restored",
    detail:
      "The listener accepted this session's registration and gave it its durable consumer, so Envoy messages reach it again, the ones the stream stored during the outage first.",
  });
}

/** One session's outage report (`inboundUnavailable`). */
export interface InboundUnavailable {
  /**
   * A registration was refused or failed: tells the session, through the host's `tell`, unless
   * this outage already told it.
   */
  refused(error: unknown): void;
  /** A registration was accepted: ends the outage, telling the session so through the host's
   *  `restored` when it was told of the outage, and the next outage tells the session again. */
  accepted(): void;
  /**
   * The host bound a session and read that session's own record of what it was last told of its
   * inbound: `told` says the record ends with the outage notice and no end after it. From
   * here the report is the bound session's. A told record is an outage an earlier process, or
   * this one before it moved away, told the session and did not end; it is taken up, so a refusal
   * tells nothing new and the next accepted registration tells the session it receives again. A
   * record that is not told leaves the session untold: its next refusal tells it that it receives
   * nothing, and the end of an outage another session was told is never told to it. The session
   * the host left keeps its own record, which tells its end when a process binds it again.
   */
  recordRead(told: boolean): void;
}

/**
 * Says once per outage that the session receives nothing, and that it receives again once a
 * registration is accepted. The listener refuses a registration it can give no consumer, and the
 * host registers again at every heartbeat, so a host that told the session each time would tell
 * it every tick for as long as the outage lasts; one that never told it again would leave a second
 * outage silent; and one that never said the outage ended would leave the session believing it
 * deaf after it recovered. `tell` takes the listener's own message (`messageFor`), which each host
 * words with `inboundUnavailableNoticeText`, and `restored` is worded with
 * `inboundRestoredNoticeText`; each host shows both where it shows a gap notice.
 */
export function inboundUnavailable(
  tell: (why: string) => void,
  restored: () => void
): InboundUnavailable {
  // Whether the session bound now was told an outage and not its end, by this process or, as its
  // record read at the bind says (`recordRead`), by an earlier one.
  let told = false;
  return {
    refused(error) {
      if (told) return;
      told = true;
      tell(messageFor(error));
    },
    accepted() {
      if (!told) return;
      told = false;
      restored();
    },
    recordRead(recordTold) {
      told = recordTold;
    },
  };
}
