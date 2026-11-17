import { afterEach, beforeEach, expect, jest, spyOn, test } from "bun:test";
import { QueryClient } from "@tanstack/react-query";

import {
  type FakeLiveSockets,
  installFakeLiveSockets,
} from "../../../__tests__/live-socket-harness";
import { liveFeedUp, liveSocketFor } from "../../../api/live-socket";

/**
 * How the tree read asks whether the live feed is up (`liveFeedUp`), which it does on a window
 * focus and on a write the page puts in the cache (`subscribeTreeFreshness`): up only while the
 * client's socket is open and Dispatch has started the event feed's subscription on it
 * (`events_start`) since the page last sent it, so that while Dispatch cannot start the feed with
 * the socket still open - its database not answering - the page reads the feed down and
 * refreshes the tree as it would with no feed at all. Asking opens no socket. Each case drives
 * the page's own live socket over the fake one the test installs, as Dispatch would.
 */

let sockets: FakeLiveSockets;
let client: QueryClient;

beforeEach(() => {
  jest.useFakeTimers();
  sockets = installFakeLiveSockets();
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
});

afterEach(() => {
  client.clear();
  sockets.restore();
  jest.useRealTimers();
});

/** The client's live socket with the event feed subscribed on it, as the shell's event feed holds
 *  it; ends both once `run` is done. */
function withFeed(run: (stop: () => void) => void): void {
  const live = liveSocketFor(client);
  const stop = live.subscribeEvents({
    onEvent: () => undefined,
    onStart: () => undefined,
    since: () => null,
  });
  try {
    run(stop);
  } finally {
    stop();
    live.close();
  }
}

test("the live feed reads down while its socket is not open", () => {
  // Each reconnect waits the middle of its spread.
  const random = spyOn(Math, "random").mockReturnValue(0.5);
  try {
    withFeed(() => {
      expect(liveFeedUp(client), "a socket still connecting").toBe(false);
      sockets.latest().open();
      sockets.latest().drop(1006);
      expect(liveFeedUp(client), "a socket lost").toBe(false);
    });
  } finally {
    random.mockRestore();
  }
});

test("the live feed reads down while Dispatch has ended its subscription on a socket still open", () => {
  // The backoff before the page sends the feed again: 1 s, at the middle of its spread.
  const random = spyOn(Math, "random").mockReturnValue(0.5);
  try {
    withFeed(() => {
      const socket = sockets.latest();
      socket.open();
      const { sub } = socket.subscribed("events");
      socket.receive({ type: "events_start", sub, since: "30", replay_complete: false });
      // Dispatch ends the feed after its start, the page sends it again at once, and Dispatch
      // ends that send too with no start: the page waits out a backoff, the socket open.
      socket.receive({ type: "ended", sub, reason: "behind" });
      socket.receive({ type: "ended", sub, reason: "behind" });
      expect(socket.readyState, "the socket stays open").toBe(1);
      expect(liveFeedUp(client), "the feed ended and not sent again").toBe(false);
      jest.advanceTimersByTime(1_000);
      expect(socket.subscribes(), "the feed sent again after the backoff").toHaveLength(3);
      expect(liveFeedUp(client), "the feed sent again, not started yet").toBe(false);
      // Dispatch refusing that send is the same: the page waits out a longer backoff, 2 s.
      socket.receive({ type: "error", sub, code: "STREAM_UNAVAILABLE", message: "no feed" });
      expect(liveFeedUp(client), "the feed refused").toBe(false);
      jest.advanceTimersByTime(2_000);
      expect(socket.subscribes(), "the feed sent again after the refusal").toHaveLength(4);
      expect(liveFeedUp(client), "the feed sent again, still not started").toBe(false);
      socket.receive({ type: "events_start", sub, since: "30", replay_complete: false });
      expect(liveFeedUp(client), "the feed Dispatch started again").toBe(true);
    });
  } finally {
    random.mockRestore();
  }
});

test("the live feed reads up once Dispatch has started its subscription on the open socket", () => {
  // The reconnect after a lost socket waits the middle of its first attempt's spread: 1 s.
  const random = spyOn(Math, "random").mockReturnValue(0.5);
  try {
    withFeed(() => {
      const socket = sockets.latest();
      socket.open();
      expect(liveFeedUp(client), "the feed sent on the open socket, not started yet").toBe(false);
      const { sub } = socket.subscribed("events");
      socket.receive({ type: "events_start", sub, since: "30", replay_complete: false });
      expect(liveFeedUp(client), "the feed started").toBe(true);
      // An end after a start is sent again at once, which is down until Dispatch starts it.
      socket.receive({ type: "ended", sub, reason: "behind" });
      expect(socket.subscribes(), "the feed sent again at once").toHaveLength(2);
      expect(liveFeedUp(client), "the feed sent again, not started yet").toBe(false);
      socket.receive({ type: "events_start", sub, since: "30", replay_complete: true });
      expect(liveFeedUp(client), "the feed started again").toBe(true);
      // A socket lost after a start: the next one sends the feed again, down until its start.
      socket.drop(1006);
      jest.advanceTimersByTime(1_000);
      const next = sockets.latest();
      expect(next, "the socket opened after the loss").not.toBe(socket);
      next.open();
      expect(liveFeedUp(client), "the feed sent on the next socket, not started yet").toBe(false);
      next.receive({ type: "events_start", sub, since: "30", replay_complete: true });
      expect(liveFeedUp(client), "the feed started on the next socket").toBe(true);
    });
  } finally {
    random.mockRestore();
  }
});

test("asking how the live feed stands opens no socket", () => {
  expect(liveFeedUp(client), "a client with no socket").toBe(false);
  expect(sockets.sockets, "the sockets the page opened").toHaveLength(0);
  // A live socket made for the client, even one with nothing to send, watches its cache.
  expect(client.getQueryCache().hasListeners(), "a live socket made for the client").toBe(false);
  withFeed((stop) => {
    sockets.latest().open();
    stop();
    expect(liveFeedUp(client), "a socket that holds no event feed").toBe(false);
  });
});
