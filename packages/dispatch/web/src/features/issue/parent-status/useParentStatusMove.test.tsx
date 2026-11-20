import { expect, jest, type Mock, mock, spyOn, test } from "bun:test";
import { NO_ROUTE_ROLLING_RELEASE_MESSAGE } from "@legion/contracts";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { http } from "msw";
import type { ReactNode } from "react";

import { realClock, settle, tick } from "../../../__tests__/fake-clock";
import { keyedAttempt } from "../../../__tests__/keyed-attempt";
import { answerRequests } from "../../../__tests__/network";
import { ApiError } from "../../../api/client";
import type { StatusChangeAnswer, StatusChangeInput } from "../../../api/types";
import { KeymapProvider } from "../../shell/KeymapProvider";
import { refusedNote } from "../../writes/useKeyedWrite";
import { ParentStatusDialog } from "./ParentStatusDialog";
import {
  answerStatusChanges,
  fixtureAnswer,
  fixtureRefusal,
  type StatusChangeAnswerer,
} from "./status-change-fixtures";
import {
  type ParentStatusMove,
  type StatusMove,
  type StatusMoveEnd,
  useParentStatusMove,
} from "./useParentStatusMove";

// The page's state table of the parent-status question (INFRA-4731's plan, "Lists named before
// code"): each row is one state and one event. The route's answers are the ones the real server
// wrote (`status-change-fixtures.ts`): ACME-4 closed with a child in work (`conflict`), its
// `include_children` dry run (`dry-run`), and so on.

/** Which request of the route `input` is: the dry run of the move itself, the dry run of one
 *  option, or a write with one option. */
function requestOf(input: StatusChangeInput): string {
  const option = input.include_children
    ? "include_children"
    : input.unlink_children
      ? "unlink_children"
      : input.include_parent
        ? "include_parent"
        : "no option";
  return `${input.dry_run === true ? "dry run" : "write"} of ${input.status} with ${option}`;
}

/** Answers each request of the route by `requestOf`; a request the table names no answer for
 *  fails the test. */
function answerBy(table: Readonly<Record<string, () => Promise<StatusChangeAnswer>>>) {
  const statusChanges = answerStatusChanges();
  statusChanges.mockImplementation(async (_key, input) => {
    const answer = table[requestOf(input)];
    if (answer === undefined) throw new Error(`no answer for the ${requestOf(input)}`);
    return answer();
  });
  return statusChanges;
}

/** Each request the route was sent, as `requestOf` names it, in order. */
function requests(statusChanges: Mock<StatusChangeAnswerer>): string[] {
  return statusChanges.mock.calls.map(([, input]) => requestOf(input));
}

const never = () => new Promise<StatusChangeAnswer>(() => {});
const refuse = (refusal: ApiError) => () => Promise.reject(refusal);

/** A surface that makes its status moves through the hook, as the issue header does, with the
 *  pending status, the refusal and the question drawn as the header draws them. */
function renderSurface() {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
  client.setQueryData(["whoami"], { event_head: "0", kind: "user", login: "alice" });
  let current: ParentStatusMove | undefined;
  function Surface(): ReactNode {
    const parentStatus = useParentStatusMove();
    current = parentStatus;
    return (
      <>
        <output aria-label="Pending status">{parentStatus.pending?.status ?? ""}</output>
        {parentStatus.refusal === undefined ? null : (
          <p role="alert">{refusedNote("change the status of ACME-4", parentStatus.refusal)}</p>
        )}
        {parentStatus.question === undefined ? null : (
          <ParentStatusDialog question={parentStatus.question} />
        )}
      </>
    );
  }
  // The dialog's Escape is a binding of the page's keymap, which `KeymapProvider` listens for.
  const view = render(
    <QueryClientProvider client={client}>
      <KeymapProvider>
        <Surface />
      </KeymapProvider>
    </QueryClientProvider>
  );
  const hook = (): ParentStatusMove => {
    if (current === undefined) throw new Error("the surface has not rendered");
    return current;
  };
  /** Makes `move` and lets every answer already given reach the page. The move's end is handed
   *  back inside an object: an async function would wait for a promise it returned bare. */
  const start = async (move: StatusMove): Promise<{ ended: Promise<StatusMoveEnd> }> => {
    let ended: Promise<StatusMoveEnd> = Promise.resolve({ kind: "busy" });
    await act(async () => {
      ended = hook().move(move);
      await settle();
    });
    return { ended };
  };
  return { client, hook, start, unmount: view.unmount };
}

/** ACME-4, which has a child in work, picked for `done`. */
function closeAcme4(patch = mock(() => {})): StatusMove & { patch: typeof patch } {
  return { key: "ACME-4", patch, related: true, status: "done" };
}

const pendingStatus = () => screen.getByRole("status", { name: "Pending status" });
const question = () => screen.getByRole("dialog", { name: "Move ACME-4 to Done?" });
const choice = (name: string) => within(question()).getByRole("button", { name });

test("idle: a status picked for an issue with no parent and no open child sends the PATCH as today, and no dry run", async () => {
  const statusChanges = answerStatusChanges();
  const surface = renderSurface();
  try {
    const move = { ...closeAcme4(), related: false };
    const { ended } = await surface.start(move);
    expect(await ended).toEqual({ kind: "patched" });
    expect(move.patch).toHaveBeenCalledTimes(1);
    expect(statusChanges).not.toHaveBeenCalled();
  } finally {
    surface.unmount();
  }
});

test("idle: a status picked for an issue with a parent or an open child shows it as pending and sends the dry run", async () => {
  const statusChanges = answerBy({ "dry run of done with no option": never });
  const surface = renderSurface();
  try {
    const move = closeAcme4();
    await surface.start(move);
    expect(statusChanges).toHaveBeenCalledWith(
      "ACME-4",
      { dry_run: true, status: "done" },
      expect.objectContaining({ ...keyedAttempt, login: "alice" })
    );
    expect(pendingStatus()).toHaveTextContent("done");
    expect(move.patch).not.toHaveBeenCalled();
  } finally {
    surface.unmount();
  }
});

test("checking: the dry run answers 200, and the PATCH goes as today", async () => {
  const statusChanges = answerBy({
    "dry run of done with no option": async () => fixtureAnswer("dry-run"),
  });
  const surface = renderSurface();
  try {
    const move = closeAcme4();
    const { ended } = await surface.start(move);
    expect(await ended).toEqual({ kind: "patched" });
    expect(move.patch).toHaveBeenCalledTimes(1);
    expect(statusChanges.mock.invocationCallOrder[0]).toBeLessThan(
      move.patch.mock.invocationCallOrder[0] ?? 0
    );
    expect(requests(statusChanges)).toEqual(["dry run of done with no option"]);
    expect(pendingStatus()).toHaveTextContent("");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  } finally {
    surface.unmount();
  }
});

test("checking: the dry run answers 409 PARENT_STATUS_CONFLICT, and the dialog lists the issues and sends each choice's dry run", async () => {
  const statusChanges = answerBy({
    "dry run of done with no option": refuse(fixtureRefusal("conflict")),
    "dry run of done with include_children": never,
    "dry run of done with unlink_children": never,
  });
  const surface = renderSurface();
  try {
    const move = closeAcme4();
    await surface.start(move);
    const listed = within(question()).getByRole("list", { name: "Issues that stop this change" });
    expect(
      within(listed)
        .getAllByRole("listitem")
        .map((item) => item.textContent)
    ).toEqual(["ACME-5 Child in work (child, Todo)"]);
    // A listed child offers the children's two choices, and no parent's.
    expect(choice("Move them with it")).toBeDisabled();
    expect(choice("Unlink them")).toBeDisabled();
    expect(
      within(question()).queryByRole("button", { name: "Move the parents into work too" })
    ).not.toBeInTheDocument();
    expect(requests(statusChanges)).toEqual([
      "dry run of done with no option",
      "dry run of done with include_children",
      "dry run of done with unlink_children",
    ]);
    expect(pendingStatus()).toHaveTextContent("done");
    expect(move.patch).not.toHaveBeenCalled();
  } finally {
    surface.unmount();
  }
});

// The recorded conflicts all name a child, so the parent's case is the recorded conflict with its
// one issue named as the parent instead (the epic of `changed.json`, on the backlog).
test("checking: a 409 that names the parent offers to move the parents into work too", async () => {
  const conflict = fixtureRefusal("conflict");
  const parentConflict = new ApiError(409, {
    changed: [],
    code: conflict.code,
    error: conflict.message,
    issues: [
      { key: "ACME-1", relation: "parent", status: "backlog", title: "Epic on the backlog" },
    ],
  });
  const statusChanges = answerBy({
    "dry run of in_progress with no option": refuse(parentConflict),
    "dry run of in_progress with include_parent": async () => fixtureAnswer("changed"),
  });
  const surface = renderSurface();
  try {
    await surface.start({ key: "ACME-2", patch: () => {}, related: true, status: "in_progress" });
    const dialog = screen.getByRole("dialog", { name: "Move ACME-2 to In progress?" });
    expect(
      within(dialog).getByRole("button", { name: "Move the parents into work too" })
    ).toBeEnabled();
    expect(
      within(dialog).queryByRole("button", { name: "Move them with it" })
    ).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Unlink them" })).not.toBeInTheDocument();
    expect(requests(statusChanges)).toEqual([
      "dry run of in_progress with no option",
      "dry run of in_progress with include_parent",
    ]);
  } finally {
    surface.unmount();
  }
});

/** The refusals of a dry run that are not the rule's: each one as the real server wrote it where
 *  a fixture holds it, and the route's own words for a status it does not know. */
const otherRefusals: readonly [string, () => ApiError][] = [
  ["403 BROWSER_SIGN_IN_REQUIRED", () => fixtureRefusal("browser-only")],
  ["409 ISSUE_CHANGED", () => fixtureRefusal("stale")],
  [
    "400 INVALID_STATUS",
    () =>
      new ApiError(400, {
        changed: [],
        code: "INVALID_STATUS",
        error: "status is not in the Legion lifecycle",
      }),
  ],
];

for (const [name, refusal] of otherRefusals) {
  test(`checking: the dry run answers ${name}, so nothing is written, the refusal shows and the status goes back`, async () => {
    const answer = refusal();
    answerBy({ "dry run of done with no option": refuse(answer) });
    const surface = renderSurface();
    try {
      const move = closeAcme4();
      const { ended } = await surface.start(move);
      expect(await ended).toEqual({ kind: "refused", refusal: answer });
      expect(screen.getByRole("alert")).toHaveTextContent(
        `Couldn't change the status of ACME-4 — ${answer.message}`
      );
      expect(pendingStatus()).toHaveTextContent("");
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(move.patch).not.toHaveBeenCalled();
    } finally {
      surface.unmount();
    }
  });
}

// A Dispatch process of an older build answers no route for the route; the client sends it again
// after each of its waits, and then refuses with its own words.
test("checking: the dry run answers no route after the client's retries, so nothing is written and the status goes back", async () => {
  jest.useFakeTimers();
  const attempts: string[] = [];
  answerRequests(
    http.post("/api/v1/issues/:key/status-change", ({ params }) => {
      attempts.push(String(params.key));
      return Response.json(
        {
          code: "NOT_FOUND",
          error: "no route for POST /api/v1/issues/ACME-4/status-change",
        },
        { status: 404 }
      );
    })
  );
  const surface = renderSurface();
  try {
    const move = closeAcme4();
    const { ended } = await surface.start(move);
    // The waits are 1, 2 and 4 seconds, each up to a quarter longer.
    await tick(10_000);
    expect(attempts).toEqual(["ACME-4", "ACME-4", "ACME-4", "ACME-4"]);
    expect(await ended).toMatchObject({ kind: "refused" });
    expect(screen.getByRole("alert")).toHaveTextContent(NO_ROUTE_ROLLING_RELEASE_MESSAGE);
    expect(pendingStatus()).toHaveTextContent("");
    expect(move.patch).not.toHaveBeenCalled();
  } finally {
    realClock();
    surface.unmount();
  }
});

test("checking: a live event changes the issue, and the dry run's answer is still used", async () => {
  const held = Promise.withResolvers<StatusChangeAnswer>();
  const statusChanges = answerBy({
    "dry run of done with no option": () => held.promise,
    "dry run of done with include_children": never,
    "dry run of done with unlink_children": never,
  });
  const surface = renderSurface();
  try {
    const move = closeAcme4();
    await surface.start(move);
    // The issue's event refreshes what the page reads of it: its child is done now.
    await act(async () => {
      surface.client.setQueryData(["issue", "ACME-4"], { children: [], key: "ACME-4" });
      await surface.client.invalidateQueries({ queryKey: ["issue", "ACME-4"] });
    });
    await act(async () => {
      held.reject(fixtureRefusal("conflict"));
      await settle();
    });
    // The move asks as its dry run answered, and checks nothing again; Dispatch judges the write.
    expect(question()).toBeInTheDocument();
    expect(requests(statusChanges)).toEqual([
      "dry run of done with no option",
      "dry run of done with include_children",
      "dry run of done with unlink_children",
    ]);
    expect(move.patch).not.toHaveBeenCalled();
  } finally {
    surface.unmount();
  }
});

test("asking: a choice's dry run answers, and the choice lists its changes and can be confirmed", async () => {
  answerBy({
    "dry run of done with no option": refuse(fixtureRefusal("conflict")),
    "dry run of done with include_children": async () => fixtureAnswer("dry-run"),
    "dry run of done with unlink_children": never,
  });
  const surface = renderSurface();
  try {
    await surface.start(closeAcme4());
    const changes = within(question()).getByRole("list", {
      name: "What Move them with it changes",
    });
    expect(
      within(changes)
        .getAllByRole("listitem")
        .map((item) => item.textContent)
    ).toEqual([
      "ACME-6: Done, under ACME-5",
      "ACME-5: Done, under ACME-4",
      "ACME-4: Done, no parent",
    ]);
    expect(choice("Move them with it")).toBeEnabled();
  } finally {
    surface.unmount();
  }
});

test("asking: a choice whose dry run is still out cannot be confirmed yet", async () => {
  const statusChanges = answerBy({
    "dry run of done with no option": refuse(fixtureRefusal("conflict")),
    "dry run of done with include_children": async () => fixtureAnswer("dry-run"),
    "dry run of done with unlink_children": never,
  });
  const user = userEvent.setup();
  const surface = renderSurface();
  try {
    await surface.start(closeAcme4());
    const unlink = choice("Unlink them");
    expect(unlink).toBeDisabled();
    expect(within(question()).getByText("Checking what this changes…")).toBeInTheDocument();
    await user.click(unlink);
    expect(requests(statusChanges)).not.toContain("write of done with unlink_children");
    expect(question()).toBeInTheDocument();
  } finally {
    surface.unmount();
  }
});

/** The three ways out of the question that write nothing. */
const closes: readonly [string, (user: UserEvent) => Promise<void>][] = [
  ["Keep as it is", (user) => user.click(choice("Keep as it is"))],
  ["Cancel", (user) => user.click(choice("Cancel"))],
  ["Escape", (user) => user.keyboard("{Escape}")],
];

for (const [name, close] of closes) {
  test(`asking: ${name} closes the question, writes nothing and puts the status back`, async () => {
    const statusChanges = answerBy({
      "dry run of done with no option": refuse(fixtureRefusal("conflict")),
      "dry run of done with include_children": async () => fixtureAnswer("dry-run"),
      "dry run of done with unlink_children": never,
    });
    const user = userEvent.setup();
    const surface = renderSurface();
    try {
      const move = closeAcme4();
      const { ended } = await surface.start(move);
      await close(user);
      expect(await ended).toEqual({ kind: "kept" });
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(pendingStatus()).toHaveTextContent("");
      expect(requests(statusChanges).filter((request) => request.startsWith("write"))).toEqual([]);
      expect(move.patch).not.toHaveBeenCalled();
    } finally {
      surface.unmount();
    }
  });
}

test("asking: a choice confirmed sends the status change with that option", async () => {
  const statusChanges = answerBy({
    "dry run of done with no option": refuse(fixtureRefusal("conflict")),
    "dry run of done with include_children": async () => fixtureAnswer("dry-run"),
    "dry run of done with unlink_children": never,
    "write of done with include_children": never,
  });
  const user = userEvent.setup();
  const surface = renderSurface();
  try {
    const move = closeAcme4();
    await surface.start(move);
    await user.click(choice("Move them with it"));
    await waitFor(() =>
      expect(statusChanges).toHaveBeenLastCalledWith(
        "ACME-4",
        { include_children: true, status: "done" },
        keyedAttempt
      )
    );
    // While it is out, nothing else of the question can be pressed.
    expect(choice("Saving…")).toBeDisabled();
    expect(choice("Cancel")).toBeDisabled();
    expect(move.patch).not.toHaveBeenCalled();
  } finally {
    surface.unmount();
  }
});

test("writing: answered 200, the question closes and each changed issue and the list are read again", async () => {
  const answer = fixtureAnswer("changed");
  answerBy({
    "dry run of done with no option": refuse(fixtureRefusal("conflict")),
    "dry run of done with include_children": async () => fixtureAnswer("dry-run"),
    "dry run of done with unlink_children": never,
    "write of done with include_children": async () => answer,
  });
  const user = userEvent.setup();
  const surface = renderSurface();
  const invalidated = spyOn(surface.client, "invalidateQueries");
  try {
    const { ended } = await surface.start(closeAcme4());
    await user.click(choice("Move them with it"));
    expect(await ended).toEqual({ answer, kind: "changed" });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(pendingStatus()).toHaveTextContent("");
    expect(invalidated.mock.calls.map(([filters]) => filters?.queryKey)).toEqual([
      ["issue", "ACME-1"],
      ["issue", "ACME-3"],
      ["issue", "ACME-2"],
      ["issues"],
      ["issues"],
    ]);
  } finally {
    invalidated.mockRestore();
    surface.unmount();
  }
});

test("writing: answered 409 PARENT_STATUS_CONFLICT with what changed, the question names it and the new conflict, and checks each choice again", async () => {
  const statusChanges = answerBy({
    "dry run of done with no option": refuse(fixtureRefusal("conflict")),
    "dry run of done with include_children": async () => fixtureAnswer("dry-run"),
    "dry run of done with unlink_children": never,
    "write of done with include_children": refuse(fixtureRefusal("conflict-midway")),
  });
  const user = userEvent.setup();
  const surface = renderSurface();
  try {
    await surface.start(closeAcme4());
    await user.click(choice("Move them with it"));
    const made = await within(question()).findByRole("list", { name: "Changes already made" });
    expect(
      within(made)
        .getAllByRole("listitem")
        .map((item) => item.textContent)
    ).toEqual(["ACME-8: Done, under ACME-7"]);
    const listed = within(question()).getByRole("list", { name: "Issues that stop this change" });
    expect(
      within(listed)
        .getAllByRole("listitem")
        .map((item) => item.textContent)
    ).toEqual(["ACME-10 Filed between two steps (child, Triage)"]);
    await waitFor(() => expect(choice("Move them with it")).toBeEnabled());
    expect(requests(statusChanges)).toEqual([
      "dry run of done with no option",
      "dry run of done with include_children",
      "dry run of done with unlink_children",
      "write of done with include_children",
      "dry run of done with include_children",
      "dry run of done with unlink_children",
    ]);
  } finally {
    surface.unmount();
  }
});

test("writing: answered another refusal, the question shows it, writes nothing more, and Cancel closes it", async () => {
  const refusal = fixtureRefusal("browser-only");
  const statusChanges = answerBy({
    "dry run of done with no option": refuse(fixtureRefusal("conflict")),
    "dry run of done with include_children": async () => fixtureAnswer("dry-run"),
    "dry run of done with unlink_children": never,
    "write of done with include_children": refuse(refusal),
  });
  const user = userEvent.setup();
  const surface = renderSurface();
  try {
    const { ended } = await surface.start(closeAcme4());
    await user.click(choice("Move them with it"));
    expect(await within(question()).findByRole("alert")).toHaveTextContent(refusal.message);
    expect(choice("Move them with it")).toBeDisabled();
    await user.click(choice("Move them with it"));
    expect(requests(statusChanges).filter((request) => request.startsWith("write"))).toEqual([
      "write of done with include_children",
    ]);
    await user.click(choice("Cancel"));
    expect(await ended).toEqual({ kind: "kept" });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  } finally {
    surface.unmount();
  }
});

test("idle: a PATCH answered 409 PARENT_STATUS_CONFLICT opens the question as a 409 dry run does", async () => {
  const statusChanges = answerBy({
    "dry run of done with include_children": async () => fixtureAnswer("dry-run"),
    "dry run of done with unlink_children": never,
  });
  const surface = renderSurface();
  try {
    await act(async () => {
      void surface.hook().ask({ key: "ACME-4", status: "done" }, fixtureRefusal("conflict"));
      await settle();
    });
    expect(within(question()).getByText("ACME-5")).toBeInTheDocument();
    expect(choice("Move them with it")).toBeEnabled();
    expect(pendingStatus()).toHaveTextContent("done");
    expect(requests(statusChanges)).toEqual([
      "dry run of done with include_children",
      "dry run of done with unlink_children",
    ]);
  } finally {
    surface.unmount();
  }
});

test("a second move while one is checking does nothing", async () => {
  const statusChanges = answerBy({ "dry run of done with no option": never });
  const surface = renderSurface();
  try {
    await surface.start(closeAcme4());
    const second = closeAcme4();
    expect(await (await surface.start(second)).ended).toEqual({ kind: "busy" });
    expect(requests(statusChanges)).toEqual(["dry run of done with no option"]);
    expect(second.patch).not.toHaveBeenCalled();
  } finally {
    surface.unmount();
  }
});
