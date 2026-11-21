// packages/envoy-client/src/__tests__/relay-registration.test.ts
//
// One registration of a session that holds a relay (`../relay-registration.ts`): the topics it
// carries, the outage told once, and the topics an edge listener refuses for the projects of the
// session's token dropped while every other refusal stays a refusal.

import { describe, expect, test } from "bun:test";
import { inboundRestoredNoticeText, inboundUnavailable } from "../consumer-delivery";
import { NoRelaySubjectError, registerWithRelay } from "../relay-registration";
import { EnvoyApiError } from "../transport";

const AGENT = "notifications.agent.ses_a";
const ISSUE_TOPIC = "notifications.dispatch.issue.DSP-1.>";

describe("one registration", () => {
  const UNCAPTURED = "notifications.team";
  const TEAM = "notifications.team.deploys";

  /** A relay that records what a registration stamped on it. */
  function fakeRelay() {
    const followed: string[][] = [];
    return {
      followed,
      setFollowed: (topics: Iterable<string>) => followed.push([...topics]),
      registration: () => ({ consumer: true, epoch: "epoch-1" }) as const,
    };
  }

  /** What a registration hands its host besides the topics: nothing a row below reads. */
  const quiet = { drop: () => undefined, log: () => undefined } as const;

  /** The listener's refusal of a registration, with this status and body. */
  function refusal(status: number, body: Record<string, unknown>): EnvoyApiError {
    return new EnvoyApiError({
      method: "POST",
      url: "http://listener.example/v1/interests/subscribe",
      status,
      responseBody: JSON.stringify(body),
    });
  }

  test("carries the topics the listener will store, and takes the answer", async () => {
    const relay = fakeRelay();
    const told: string[] = [];
    const answers: string[] = [];
    const sent: { topics: readonly string[]; epoch: string }[] = [];

    const result = await registerWithRelay({
      relay,
      ownAgentSubject: AGENT,
      // The bare base a stored row or a transcript brings back: the listener stores it for no
      // session and warns about it, so it never goes out.
      followed: [TEAM, UNCAPTURED, "notifications.agent.ses_b"],
      outage: inboundUnavailable(
        (why) => told.push(why),
        () => told.push("restored")
      ),
      send: (topics, delivery) => {
        sent.push({ topics, epoch: delivery.epoch });
        return Promise.resolve("registered");
      },
      answered: (answer) => answers.push(answer),
      ...quiet,
    });

    expect(sent).toEqual([{ topics: [AGENT, TEAM], epoch: "epoch-1" }]);
    expect(relay.followed).toEqual([[AGENT, TEAM]]);
    expect([result, answers, told]).toEqual(["registered", ["registered"], []]);
  });

  test("a session holding no relay subject registers nothing and is told once", async () => {
    const told: string[] = [];
    const outage = inboundUnavailable(
      (why) => told.push(why),
      () => told.push("restored")
    );
    const sent: unknown[] = [];
    const register = () =>
      registerWithRelay({
        relay: undefined,
        ownAgentSubject: AGENT,
        followed: [TEAM],
        outage,
        send: (topics) => {
          sent.push(topics);
          return Promise.resolve("registered");
        },
        answered: () => undefined,
        ...quiet,
      });

    await expect(register()).rejects.toThrow("holds no relay subject");
    // The second outage tick says nothing more: the session was told once.
    await expect(register()).rejects.toThrow("holds no relay subject");
    expect(sent).toEqual([]);
    expect(told).toEqual([
      "this session holds no relay subject, so the listener can give it no consumer and it can receive nothing",
    ]);
  });

  test("a session whose relay could not be opened says why in the refusal and in the one outage notice", async () => {
    const told: string[] = [];
    const refusal = await registerWithRelay({
      relay: undefined,
      relayMissing: "Authentication Timeout",
      ownAgentSubject: AGENT,
      followed: [TEAM],
      outage: inboundUnavailable(
        (why) => told.push(why),
        () => told.push("restored")
      ),
      send: () => Promise.resolve("registered"),
      answered: () => undefined,
      ...quiet,
    }).catch((error: unknown) => error);

    const expected =
      "this session holds no relay subject, so the listener can give it no consumer and it can receive nothing, because its relay subject could not be opened: Authentication Timeout";
    if (!(refusal instanceof NoRelaySubjectError))
      throw new Error(`no refusal: ${String(refusal)}`);
    expect(refusal.message).toBe(expected);
    expect(told).toEqual([expected]);
  });

  test("a refused registration tells the session once and takes no answer", async () => {
    const relay = fakeRelay();
    const told: string[] = [];
    const answers: string[] = [];

    await expect(
      registerWithRelay({
        relay,
        ownAgentSubject: AGENT,
        followed: [TEAM],
        outage: inboundUnavailable(
          (why) => told.push(why),
          () => told.push("restored")
        ),
        send: () => Promise.reject(new Error("no_grant")),
        answered: (answer: string) => answers.push(answer),
        ...quiet,
      })
    ).rejects.toThrow("no_grant");

    expect(relay.followed).toEqual([[AGENT, TEAM]]);
    expect([answers, told]).toEqual([[], ["no_grant"]]);
  });

  test("a registration accepted after an outage tells the session once that it receives again", async () => {
    const told: string[] = [];
    const outage = inboundUnavailable(
      (why) => told.push(why),
      () => told.push("restored")
    );
    let refuse = true;
    const register = () =>
      registerWithRelay({
        relay: fakeRelay(),
        ownAgentSubject: AGENT,
        followed: [TEAM],
        outage,
        send: () =>
          refuse ? Promise.reject(new Error("no_grant")) : Promise.resolve("registered"),
        answered: () => undefined,
        ...quiet,
      }).catch(() => undefined);

    await register();
    await register();
    refuse = false;
    await register();
    // A heartbeat that is accepted again says nothing: the session already knows.
    await register();
    expect(told).toEqual(["no_grant", "restored"]);
    // A second outage is told again, and its end too.
    refuse = true;
    await register();
    refuse = false;
    await register();
    expect(told).toEqual(["no_grant", "restored", "no_grant", "restored"]);
    expect(inboundRestoredNoticeText()).toContain("Envoy inbound restored");
  });

  test("any other refusal is a refusal as before: nothing dropped, nothing sent again", async () => {
    // The edge's own refusals that are not a project's: 503 `repositories_unknown` (it could not
    // read which repositories the session's projects hold; every topic is kept and the heartbeat
    // asks again) and 403 `token_not_admitted` (the token's fault, on any route). And a 403 whose
    // reason is not one of the two project refusals though it names topics, and one of those two
    // that names none. None drops a topic or sends again.
    for (const [status, body] of [
      [
        503,
        { error: "the project's repositories could not be read", reason: "repositories_unknown" },
      ],
      [403, { error: "the token is not admitted here", reason: "token_not_admitted" }],
      [403, { error: "bound to another session", reason: "session_not_bound", refused: [TEAM] }],
      [403, { error: "no project", reason: "topic_not_in_project" }],
    ] as const) {
      const told: string[] = [];
      const dropped: (readonly string[])[] = [];
      const sent: (readonly string[])[] = [];
      const refused = refusal(status, body);

      const outcome = await registerWithRelay({
        relay: fakeRelay(),
        ownAgentSubject: AGENT,
        followed: [TEAM],
        outage: inboundUnavailable(
          (why) => told.push(why),
          () => told.push("restored")
        ),
        send: (topics) => {
          sent.push(topics);
          return Promise.reject(refused);
        },
        answered: () => undefined,
        drop: (topics) => dropped.push(topics),
        log: () => undefined,
      }).catch((error: unknown) => error);

      const answer = `${status} ${body.reason}`;
      expect(outcome).toBe(refused);
      expect({ answer, sent, dropped, told }).toEqual({
        answer,
        sent: [[AGENT, TEAM]],
        dropped: [],
        told: [refused.message],
      });
    }
  });

  test("a project refusal that names only the session's own subject drops nothing and is told", async () => {
    // The session's own agent subject is in every registration (`registrationTopics`): a refusal
    // naming it, or a topic the registration did not carry, leaves nothing to drop, so the
    // registration fails as any other refusal does rather than being sent again unchanged.
    const told: string[] = [];
    const dropped: (readonly string[])[] = [];
    const sent: (readonly string[])[] = [];
    const refused = refusal(403, {
      error: "no project",
      reason: "topic_not_in_project",
      refused: [AGENT, "notifications.slack.alerts"],
    });

    const outcome = await registerWithRelay({
      relay: fakeRelay(),
      ownAgentSubject: AGENT,
      followed: [TEAM],
      outage: inboundUnavailable(
        (why) => told.push(why),
        () => told.push("restored")
      ),
      send: (topics) => {
        sent.push(topics);
        return Promise.reject(refused);
      },
      answered: () => undefined,
      drop: (topics) => dropped.push(topics),
      log: () => undefined,
    }).catch((error: unknown) => error);

    expect(outcome).toBe(refused);
    expect([sent, dropped, told]).toEqual([[[AGENT, TEAM]], [], [refused.message]]);
  });

  test("a topic_not_in_project refusal naming a followed topic drops exactly it and registers the rest", async () => {
    // An edge listener answers a topic of no project of the token's (a Slack, team or WhatsApp
    // lane) 403 `topic_not_in_project`, naming it in `refused`: what a real edge listener answers a
    // registration following `notifications.slack.alerts`. It is dropped as the GitHub refusal
    // (`repository_not_in_project`) is, and the rest are registered again at once.
    const SLACK = "notifications.slack.alerts";
    const told: string[] = [];
    const dropped: (readonly string[])[] = [];
    const logs: string[] = [];
    const sent: (readonly string[])[] = [];

    const result = await registerWithRelay({
      relay: fakeRelay(),
      ownAgentSubject: AGENT,
      followed: [ISSUE_TOPIC, SLACK],
      outage: inboundUnavailable(
        (why) => told.push(why),
        () => told.push("restored")
      ),
      send: (topics) => {
        sent.push(topics);
        return topics.includes(SLACK)
          ? Promise.reject(
              refusal(403, {
                error: "no project",
                reason: "topic_not_in_project",
                refused: [SLACK],
              })
            )
          : Promise.resolve("registered");
      },
      answered: () => undefined,
      drop: (topics) => dropped.push(topics),
      log: (line) => logs.push(line),
    });

    expect({ result, sent, dropped, told }).toEqual({
      result: "registered",
      sent: [
        [AGENT, ISSUE_TOPIC, SLACK],
        [AGENT, ISSUE_TOPIC],
      ],
      dropped: [[SLACK]],
      told: [],
    });
    expect(logs).toEqual([
      `the listener refused ${SLACK} for this session's projects (topic_not_in_project); the session no longer follows them and registers the rest`,
    ]);
  });
});
