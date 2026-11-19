import { useCallback, useEffect, useReducer, useRef, useState } from "react";

import type { Issue } from "../../api/types";
import { useSubmitGuard } from "../../hooks/useSubmitGuard";

export type IssueUpdateInput = Partial<Pick<Issue, "parent" | "route" | "status" | "title">>;

export interface IssueUpdateMutation {
  mutate: (input: IssueUpdateInput) => void;
  variables: IssueUpdateInput | undefined;
}

/** What a request to send a field's draft did: sent it, queued it behind the write still out,
 *  found nothing to send, or refused it (a closed issue, or a value Dispatch would not take). */
export type SubmitOutcome = "sent" | "queued" | "unchanged" | "refused";

export interface IssueDrafts {
  discardTitle: () => void;
  discardRoute: () => void;
  discardParent: () => void;
  onIssueSettled: (error: unknown) => void;
  onIssueSuccess: (next: Issue, input: IssueUpdateInput) => void;
  requestParentSubmit: () => boolean;
  requestRouteSubmit: () => boolean;
  requestStatusSubmit: (status: string) => boolean;
  /** `submitTitle`, answering whether the title was sent or queued. */
  requestTitleSubmit: () => boolean;
  retry: () => void;
  /** Asks for the title's draft to be sent, and says what came of it. */
  submitTitle: () => SubmitOutcome;
  parent: string;
  parentDirty: boolean;
  route: string;
  routeDirty: boolean;
  routeIsValid: boolean;
  title: string;
  titleDirty: boolean;
  titleError: string | null;
  /** Whether a send of the title's draft was asked for and has not gone yet. */
  titleQueued: boolean;
  writeParent: (next: string) => void;
  writeRoute: (next: string) => void;
  writeTitle: (next: string) => void;
}

const routePattern = /^(role:[a-z0-9-]+|session:[0-9a-f-]{16,})$/;

/** One field's draft (decision 315). */
interface FieldState {
  /** The issue's value as last read: a prop, or a write's landing. */
  server: string;
  /** The value of this field's write still out, or null. */
  out: string | null;
  /** The reader's text, raw. */
  draft: string;
  /** What the draft would send now, once a send was asked for; else null. */
  queued: string | null;
}

/** What one field's draft is: the value a text sends, which values Dispatch takes, and the write
 *  that sends one. */
interface FieldRules {
  normalize: (text: string) => string;
  valid: (value: string) => boolean;
  wire: (value: string) => IssueUpdateInput;
}

type Field = "title" | "route" | "parent";

type FieldEvent =
  | { type: "write"; text: string }
  | { type: "request"; closed: boolean }
  | { type: "discard" }
  | { type: "sent"; value: string }
  | { type: "server"; value: string }
  | { type: "settled"; landed: boolean };

const fieldRules: Readonly<Record<Field, FieldRules>> = {
  title: {
    normalize: (text) => text.trim(),
    valid: (value) => value !== "",
    wire: (title) => ({ title }),
  },
  route: {
    normalize: (text) => text,
    valid: (value) => value === "" || routePattern.test(value),
    wire: (route) => ({ route }),
  },
  // An empty draft clears the parent: the wire form is null, never "".
  parent: {
    normalize: (text) => text.trim(),
    valid: () => true,
    wire: (parent) => ({ parent: parent === "" ? null : parent }),
  },
};
/** The three fields, in the order a drain sends them. */
const fields: readonly Field[] = ["title", "route", "parent"];

/** Until the write out lands the issue still holds the old value, so a draft is judged against
 *  the value out while there is one: a reader who types the old value back is undoing the write. */
const reference = (state: FieldState): string => state.out ?? state.server;

const changed = (rules: FieldRules, state: FieldState): boolean =>
  rules.normalize(state.draft) !== reference(state);

/** The value the draft would send now: a changed draft Dispatch would take, else null. */
function wouldSend(rules: FieldRules, state: FieldState): string | null {
  const value = rules.normalize(state.draft);
  return value !== reference(state) && rules.valid(value) ? value : null;
}

/** A send asked for follows the draft and its reference; with none asked for, none is made. */
const requeued = (rules: FieldRules, state: FieldState): FieldState => ({
  ...state,
  queued: state.queued === null ? null : wouldSend(rules, state),
});

function requestOutcome(
  rules: FieldRules,
  state: FieldState,
  closed: boolean
): Exclude<SubmitOutcome, "sent"> {
  if (closed || !rules.valid(rules.normalize(state.draft))) return "refused";
  return changed(rules, state) ? "queued" : "unchanged";
}

/**
 * One field's draft, as one pure step per event. `reference` is the value out, else the
 * issue's; a draft is `changed` when its text normalized differs from it, and `wouldSend` is
 * that text, if Dispatch would take it.
 *
 * - `write(text)`, the reader's typing: the draft is the text, and a send asked for follows it,
 *   so a draft typed back to the reference asks for nothing.
 * - `request`, the end of an edit, a blur, a switch's send: refused for a closed issue or a
 *   value Dispatch would not take, which asks for nothing; an unchanged draft takes the
 *   reference and asks for nothing; a changed one is queued as its text normalized, which the
 *   hook's drain then sends.
 * - `discard`, Escape or Cancel: the draft takes the reference and asks for nothing. While a
 *   write is out that is the value out, the value the issue is about to hold (decision 316).
 * - `sent(value)`, the guard let the write go: the value is out, and nothing is queued.
 * - `server(value)`, a prop change or a landing's answer: an unchanged draft with nothing out
 *   takes the value. While a value is out the reference stays that value.
 * - `settled(landed)`, this field's write ended: a landed write's draft that is still its value
 *   takes the value it landed with. A refusal keeps the draft, which is then changed against the
 *   issue's value: the reader's unsent choice, which Retry and the end of the next edit send.
 *
 * After `server` and `settled` a send asked for follows the draft against its new reference.
 */
function reduceField(rules: FieldRules, state: FieldState, event: FieldEvent): FieldState {
  switch (event.type) {
    case "write":
      return requeued(rules, { ...state, draft: event.text });
    case "request": {
      const outcome = requestOutcome(rules, state, event.closed);
      if (outcome === "queued") return { ...state, queued: rules.normalize(state.draft) };
      return {
        ...state,
        draft: outcome === "unchanged" ? reference(state) : state.draft,
        queued: null,
      };
    }
    case "discard":
      return { ...state, draft: reference(state), queued: null };
    case "sent":
      return { ...state, out: event.value, queued: null };
    case "server": {
      const follows = state.out === null && !changed(rules, state);
      return requeued(rules, {
        ...state,
        draft: follows ? event.value : state.draft,
        server: event.value,
      });
    }
    case "settled": {
      if (state.out === null) return state;
      const took = event.landed && rules.normalize(state.draft) === state.out;
      return requeued(rules, { ...state, draft: took ? state.server : state.draft, out: null });
    }
  }
}

const fieldAt = (value: string): FieldState => ({
  server: value,
  out: null,
  draft: value,
  queued: null,
});

const asked = (outcome: SubmitOutcome): boolean => outcome === "sent" || outcome === "queued";

export function useIssueDrafts(issue: Issue, updateIssue: IssueUpdateMutation): IssueDrafts {
  // Read and written synchronously: a request and the drain it starts run in one task, and so do
  // a landing and its settle.
  const states = useRef<Record<Field, FieldState>>({
    title: fieldAt(issue.title),
    route: fieldAt(issue.route ?? ""),
    parent: fieldAt(issue.parent ?? ""),
  });
  const [, drawAgain] = useReducer((count: number) => count + 1, 0);
  const [titleError, setTitleError] = useState<string | null>(null);
  const updateIssueGuard = useSubmitGuard();

  const apply = useCallback((field: Field, event: FieldEvent) => {
    const state = states.current[field];
    const next = reduceField(fieldRules[field], state, event);
    if (
      next.server === state.server &&
      next.out === state.out &&
      next.draft === state.draft &&
      next.queued === state.queued
    ) {
      return;
    }
    states.current[field] = next;
    drawAgain();
  }, []);

  // A value is marked out inside the guard, as its write goes: a write the guard holds back is
  // not out, and its value stays queued. Answers the field it sent, or null.
  function drainPendingSubmits(): Field | null {
    for (const field of fields) {
      const value = states.current[field].queued;
      if (value === null) continue;
      const went = updateIssueGuard.guard(() => {
        apply(field, { type: "sent", value });
        updateIssue.mutate(fieldRules[field].wire(value));
      });
      return went ? field : null;
    }
    return null;
  }

  function submit(field: Field): SubmitOutcome {
    const closed = issue.closed_at !== null;
    const outcome = requestOutcome(fieldRules[field], states.current[field], closed);
    apply(field, { type: "request", closed });
    if (outcome !== "queued") return outcome;
    return drainPendingSubmits() === field ? "sent" : "queued";
  }

  function submitTitle(): SubmitOutcome {
    const outcome = submit("title");
    setTitleError(outcome === "refused" ? "Title is required." : null);
    return outcome;
  }

  const requestTitleSubmit = () => asked(submitTitle());
  const requestRouteSubmit = () => asked(submit("route"));
  const requestParentSubmit = () => asked(submit("parent"));

  // The field a write carried takes the value it carried: a write that landed with no answer is
  // passed the issue as it was, and a later read brings any value Dispatch normalized. The other
  // fields take the answer's: another field's write can land while this field's write waits
  // behind it in the issue's line (a title saved after a status pick), and while a value is out
  // a draft stays judged against it. An Escape or a Cancel while a write is out puts back the
  // value out (decision 316), the value the issue is about to hold, so the landing finds that
  // draft still the value out, and `settled` gives it the value the write landed with.
  const onIssueSuccess = (next: Issue, input: IssueUpdateInput) => {
    for (const field of fields) {
      const carried = input[field];
      const value = (carried === undefined ? next[field] : carried) ?? "";
      apply(field, { type: "server", value });
    }
  };
  // One write of these fields is out at a time (the guard), so one field at most has a value out,
  // and `settled` leaves the others as they are.
  const onIssueSettled = (error: unknown) => {
    for (const field of fields) apply(field, { type: "settled", landed: error === null });
    updateIssueGuard.release();
    if (error === null) {
      drainPendingSubmits();
    }
  };
  const retry = () => {
    const { variables } = updateIssue;
    if (variables?.title !== undefined) {
      requestTitleSubmit();
      drainPendingSubmits();
      return;
    }
    if (variables?.route !== undefined) {
      requestRouteSubmit();
      drainPendingSubmits();
      return;
    }
    if (variables?.parent !== undefined) {
      requestParentSubmit();
      drainPendingSubmits();
      return;
    }
    updateIssueGuard.retryLast(updateIssue);
  };
  const discardTitle = () => {
    apply("title", { type: "discard" });
    setTitleError(null);
  };
  const discardRoute = () => apply("route", { type: "discard" });
  const discardParent = () => apply("parent", { type: "discard" });
  const writeTitle = (next: string) => {
    apply("title", { type: "write", text: next });
    setTitleError(null);
  };
  const writeRoute = (next: string) => apply("route", { type: "write", text: next });
  const writeParent = (next: string) => apply("parent", { type: "write", text: next });
  const requestStatusSubmit = (status: string): boolean =>
    updateIssueGuard.guard(() => updateIssue.mutate({ status }));

  useEffect(() => {
    apply("title", { type: "server", value: issue.title });
  }, [apply, issue.title]);
  useEffect(() => {
    apply("route", { type: "server", value: issue.route ?? "" });
  }, [apply, issue.route]);
  useEffect(() => {
    apply("parent", { type: "server", value: issue.parent ?? "" });
  }, [apply, issue.parent]);

  const { title, route, parent } = states.current;
  return {
    discardTitle,
    discardRoute,
    discardParent,
    onIssueSettled,
    onIssueSuccess,
    requestParentSubmit,
    requestRouteSubmit,
    requestTitleSubmit,
    retry,
    submitTitle,
    parent: parent.draft,
    parentDirty: changed(fieldRules.parent, parent),
    route: route.draft,
    routeDirty: changed(fieldRules.route, route),
    routeIsValid: fieldRules.route.valid(route.draft),
    title: title.draft,
    requestStatusSubmit,
    titleDirty: changed(fieldRules.title, title),
    titleError,
    titleQueued: title.queued !== null,
    writeParent,
    writeRoute,
    writeTitle,
  };
}
