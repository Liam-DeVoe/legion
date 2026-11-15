import { expect, test } from "bun:test";

import type { DispatchEvent } from "../../api/types";
import { activityDescription, dateKey, dayLabel } from "./activity-model";

// The day an event falls on is its local calendar day, so the fixtures are built from local
// components rather than from UTC instants that could land on either side of midnight.
test("a day names Today and Yesterday and spells an older day in full", () => {
  const today = new Date(2026, 8, 10, 12, 0, 0);
  const yesterday = new Date(2026, 8, 9, 12, 0, 0);
  const older = new Date(2025, 0, 3, 12, 0, 0);
  const todayKey = dateKey(today.toISOString());

  expect(
    [today, yesterday, older].map((day) => dayLabel(dateKey(day.toISOString()), todayKey))
  ).toEqual([
    "Today",
    "Yesterday",
    // A day in another year carries that year; the two days above carry none.
    new Intl.DateTimeFormat(undefined, {
      day: "numeric",
      month: "long",
      weekday: "long",
      year: "numeric",
    }).format(older),
  ]);
});

const projectEvent = {
  actor: { id: "alice", kind: "user" },
  created_at: "2026-10-25T00:00:00Z",
  id: 1,
  issue_key: null,
  notify: false,
  project: "CORE",
  seq: 1,
} as const;

test("an architecture import names the repository it came from; a retained event without one reads as before", () => {
  const commit = "3be61df5be90aa11bb22";
  const error = "duplicate component id api";
  expect(
    activityDescription({
      ...projectEvent,
      payload: { branch: "main", commit, components: 3, repo: "acme/infra" },
      type: "architecture.synced",
    })
  ).toEqual(["synced the architecture model from acme/infra at 3be61df5be90"]);
  expect(
    activityDescription({
      ...projectEvent,
      payload: { branch: "main", error, repo: "acme/infra" },
      type: "architecture.sync_failed",
    })
  ).toEqual(["failed to sync the architecture model from acme/infra: duplicate component id api"]);
  expect(
    activityDescription({
      ...projectEvent,
      payload: { commit, components: 3 },
      type: "architecture.synced",
    })
  ).toEqual(["synced the architecture model at 3be61df5be90"]);
  expect(
    activityDescription({ ...projectEvent, payload: { error }, type: "architecture.sync_failed" })
  ).toEqual(["failed to sync the architecture model: duplicate component id api"]);
});

test("a project's default assignee names the person it moved to; a retained clear reads as cleared", () => {
  expect(
    activityDescription({
      ...projectEvent,
      payload: { assignee: "bob", project: "CORE", thread_logins: ["alice", "bob"] },
      type: "settings.project_default_assignee.updated",
    })
  ).toEqual(["set the default assignee of CORE to bob"]);
  // Written before every project named a default: the stored row of a clear, and no
  // `thread_logins`.
  expect(
    activityDescription({
      ...projectEvent,
      payload: { assignee: null, project: "CORE" },
      type: "settings.project_default_assignee.updated",
    })
  ).toEqual(["cleared the default assignee of CORE"]);
});

function commentDelivery(receipt: {
  state: "sent" | "failed";
  duplicate?: boolean;
}): DispatchEvent {
  return {
    actor: { id: "alice", kind: "user" },
    created_at: "2026-10-25T00:00:00Z",
    id: 1,
    issue_key: "CORE-1",
    notify: false,
    payload: {
      attempt: 1,
      comment_id: "comment-1",
      delivery: "btw",
      reply_id: null,
      session_id: null,
      target: "session:planner",
      ...(receipt.state === "failed" ? { error: "no live session planner" } : {}),
      ...receipt,
    },
    seq: 1,
    type: "comment.delivery",
  };
}

test("a mention receipt says whether the mention reached its target", () => {
  expect(activityDescription(commentDelivery({ state: "sent" }))).toEqual([
    "delivered a comment mention to session:planner",
  ]);
  expect(activityDescription(commentDelivery({ state: "failed" }))).toEqual([
    "could not deliver a comment mention to session:planner",
  ]);
  // A repeat the stream already held reached nobody new, which the line says rather than
  // counting it as a second delivery.
  expect(activityDescription(commentDelivery({ duplicate: true, state: "sent" }))).toEqual([
    "already delivered a comment mention to session:planner",
  ]);
});

test("a recorded handoff names its phase and version", () => {
  expect(
    activityDescription({
      actor: { id: "session-1", kind: "session" },
      created_at: "2026-10-26T00:00:00Z",
      id: 2,
      issue_key: "CORE-1",
      notify: true,
      payload: { handoff_id: "handoff-1", phase: "implement", version: 2 },
      seq: 2,
      type: "handoff.recorded",
    })
  ).toEqual(["recorded the implement handoff, version 2"]);
});
