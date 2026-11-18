import { expect, test } from "bun:test";
import { writeFileSync } from "node:fs";
import { act, cleanup, type RenderHookResult, renderHook } from "@testing-library/react";

import type { Issue } from "../../api/types";
import {
  type IssueDrafts,
  type IssueUpdateInput,
  type IssueUpdateMutation,
  useIssueDrafts,
} from "./useIssueDrafts";

// The title draft of both issue headers is one hook, and the bugs found in it have each been one
// particular order of typing, saving, escaping and answers from Dispatch. Rather than wait for the
// next order to be found by hand, this file plays every order up to five events long and checks,
// after every event, the four things decision 316 says a reader can rely on. The three orders a
// review found come last, each as its own test, so a fix can be read against them by name.

const startIssue: Issue = {
  advances: [],
  advances_revision: 0,
  assignee: null,
  blocked_by: [],
  claim: null,
  closed_at: null,
  components: { mode: "inherit", ids: [], unknown: [], reason: null, inherited_from: null },
  created_at: "2026-09-09T00:00:00Z",
  created_by: { id: "alice", kind: "user" },
  decisions_artifact_id: "",
  external_links: [],
  key: "CORE-1",
  labels: [],
  last_seq: 250,
  number: 1,
  parent: null,
  plan_artifact_id: "",
  primary_artifact_id: "artifact-1",
  priority: null,
  project: "CORE",
  rank: "U",
  repo: null,
  resolved_repo: null,
  route: null,
  status: "todo",
  title: "Review the spec",
  updated_at: "2026-09-09T00:00:00Z",
  verification_artifact_id: "",
};

interface HookProps {
  issue: Issue;
  updateIssue: IssueUpdateMutation;
}

/** A write the fake received, with the draft the field showed once the step that sent it ended. */
interface Sent {
  input: IssueUpdateInput;
  draft: string;
}

/**
 * One mounted hook, the fake mutation it writes through, and the issue its prop holds. The fake
 * records each write in order and never answers by itself: when a write lands or is refused is the
 * caller's choice, since the orders of those answers are what this file is about.
 */
class Session {
  readonly received: IssueUpdateInput[] = [];
  readonly updateIssue: IssueUpdateMutation = {
    mutate: (input) => {
      this.received.push(input);
      this.updateIssue.variables = input;
    },
    variables: undefined,
  };
  issue = startIssue;
  /** The write the fake last received that has not landed or been refused. */
  out: IssueUpdateInput | null = null;
  private readonly hook: RenderHookResult<IssueDrafts, HookProps>;

  constructor() {
    this.hook = renderHook((props: HookProps) => useIssueDrafts(props.issue, props.updateIssue), {
      initialProps: { issue: this.issue, updateIssue: this.updateIssue },
    });
  }

  get drafts(): IssueDrafts {
    return this.hook.result.current;
  }

  /** Runs one step inside `act` and returns the writes the fake received during it. */
  step(action: () => void): Sent[] {
    const before = this.received.length;
    act(action);
    const sent = this.received.slice(before).map((input) => ({ input, draft: this.drafts.title }));
    const last = sent.at(-1);
    if (last !== undefined) this.out = last.input;
    return sent;
  }

  type(value: string): Sent[] {
    return this.step(() => this.drafts.writeTitle(value));
  }

  /** The end of an edit: a blur, Enter, or a switch's send. */
  end(): Sent[] {
    return this.step(() => this.drafts.requestTitleSubmit());
  }

  escape(): Sent[] {
    return this.step(() => this.drafts.discardTitle());
  }

  /** The write out lands: the issue takes the title it carried, then Dispatch's answer arrives. */
  land(): Sent[] {
    const input = this.takeOut();
    const shown =
      input.title === undefined ? [] : this.showIssue({ ...this.issue, title: input.title });
    const next = this.issue;
    const answered = this.step(() => {
      this.drafts.onIssueSuccess(next, input);
      this.drafts.onIssueSettled(null);
    });
    return [...shown, ...answered];
  }

  refuse(): Sent[] {
    this.takeOut();
    return this.step(() => this.drafts.onIssueSettled(new Error("refused")));
  }

  otherReader(title: string): Sent[] {
    return this.showIssue({ ...this.issue, title });
  }

  /**
   * Another field's write lands carrying another reader's title, as the new header's status pick
   * does. Its answer reaches the hook, but it is not the hook's own write, so nothing settles.
   */
  otherFieldLands(title: string): Sent[] {
    const next = { ...this.issue, title, status: "in_progress" };
    const answered = this.step(() => this.drafts.onIssueSuccess(next, { status: "in_progress" }));
    return [...answered, ...this.showIssue(next)];
  }

  titlesSent(): string[] {
    return this.received.flatMap((input) => (input.title === undefined ? [] : [input.title]));
  }

  private takeOut(): IssueUpdateInput {
    const input = this.out;
    if (input === null) throw new Error("no write is out");
    this.out = null;
    return input;
  }

  private showIssue(next: Issue): Sent[] {
    this.issue = next;
    return this.step(() => this.hook.rerender({ issue: next, updateIssue: this.updateIssue }));
  }
}

const alphabet = [
  "typeNew",
  "typeServer",
  "typeOut",
  "end",
  "escape",
  "land",
  "refuse",
  "otherReader",
  "otherFieldLands",
] as const;
type EventName = (typeof alphabet)[number];
type Invariant = "I1" | "I2" | "I3" | "I4";

interface Finding {
  invariant: Invariant;
  read: string;
}

interface TitleWrite {
  title: string;
  refused: boolean;
}

/**
 * A session and a small model of its reader: what they last chose, what they put away, and what
 * has been sent, which is what the invariants are judged against.
 */
class Reader {
  readonly session = new Session();
  /** Set by an end whose draft differs from what it is compared with; a send or escape clears it. */
  private intent = false;
  /**
   * The reader's own choice the field may show: the text of the last type event, or what an
   * escape put back while a write was out; none after an escape with nothing out.
   */
  lastTyped: string | null = null;
  /** The drafts an escape put away, until the reader types one of them again. */
  private readonly discarded = new Set<string>();
  private lastTitleWrite: TitleWrite | null = null;
  private titleWriteOut: TitleWrite | null = null;
  private typed = 0;
  private others = 0;
  private picks = 0;

  applies(event: EventName): boolean {
    switch (event) {
      case "typeOut":
        return this.session.out?.title !== undefined;
      case "land":
      case "refuse":
        return this.session.out !== null;
      default:
        return true;
    }
  }

  /** Plays one event and returns what the writes it sent broke of I1, I2 and I3. */
  play(event: EventName): { sent: Sent[]; findings: Finding[] } {
    const sent = this.apply(event);
    return { sent, findings: sent.flatMap((write) => this.judge(write)) };
  }

  private apply(event: EventName): Sent[] {
    const { session } = this;
    switch (event) {
      case "typeNew":
        this.typed += 1;
        return this.type(`t${this.typed}`);
      case "typeServer":
        return this.type(session.issue.title);
      case "typeOut":
        return this.type(session.out?.title ?? "");
      case "end": {
        // While a title write is out the draft is compared with the title it carries, since the
        // issue is about to hold that title; otherwise with the issue's own.
        const against = session.out?.title ?? session.issue.title;
        if (session.drafts.title.trim() !== against.trim()) this.intent = true;
        return session.end();
      }
      case "escape": {
        const before = session.drafts.title.trim();
        this.intent = false;
        // An escape while a title write is out puts back the title that write carries: the reader
        // chose it when they ended the edit that sent it, so it is their choice again, and a
        // refusal of that write leaves it in the field as the choice not yet sent.
        this.lastTyped = session.out?.title ?? null;
        const sent = session.escape();
        if (session.drafts.title.trim() !== before) this.discarded.add(before);
        return sent;
      }
      case "land":
        this.titleWriteOut = null;
        return session.land();
      case "refuse":
        if (this.titleWriteOut !== null) this.titleWriteOut.refused = true;
        this.titleWriteOut = null;
        return session.refuse();
      case "otherReader":
        this.others += 1;
        return session.otherReader(`o${this.others}`);
      case "otherFieldLands":
        this.picks += 1;
        return session.otherFieldLands(`p${this.picks}`);
    }
  }

  private type(value: string): Sent[] {
    this.lastTyped = value;
    this.discarded.delete(value.trim());
    return this.session.type(value);
  }

  private judge({ input, draft }: Sent): Finding[] {
    const { title } = input;
    if (title === undefined) {
      this.titleWriteOut = null;
      return [];
    }
    const findings: Finding[] = [];
    const quoted = JSON.stringify(title);
    // A title the reader never typed, such as the issue's old title an escape put back, is no
    // choice of theirs even when the field shows it as an end sends it.
    const chosen = this.lastTyped?.trim() ?? null;
    const unchosen = [
      ...(this.intent ? [] : ["with no end choosing it"]),
      ...(title === draft.trim() ? [] : [`while the field showed ${JSON.stringify(draft)}`]),
      ...(title === chosen
        ? []
        : [`while the reader's choice was ${chosen === null ? "none" : JSON.stringify(chosen)}`]),
    ];
    if (unchosen.length > 0) {
      findings.push({ invariant: "I1", read: `sent ${quoted} ${unchosen.join(" and ")}` });
    }
    if (this.discarded.has(title)) {
      findings.push({ invariant: "I2", read: `sent ${quoted}, which an escape had put away` });
    }
    const previous = this.lastTitleWrite;
    if (previous !== null && previous.title === title && !previous.refused) {
      findings.push({ invariant: "I3", read: `sent ${quoted} twice in a row` });
    }
    this.intent = false;
    const write = { title, refused: false };
    this.lastTitleWrite = write;
    this.titleWriteOut = write;
    return findings;
  }
}

interface Violation extends Finding {
  sequence: string;
  length: number;
}

/** One line of the trace: a sequence, every title it sent, and the title the field ended on. */
interface TraceLine {
  sequence: string;
  sent: string[];
  title: string;
  violates: Invariant[];
}

/**
 * I4, judged wherever no write is out: the field shows the issue's title or the reader's own
 * choice. A field that shows neither, a stale title the reader never typed, is what the next end
 * would send over the issue's title, so an end that would send it does not excuse it.
 */
function atRest(reader: Reader): Finding[] {
  const { session } = reader;
  const title = session.drafts.title.trim();
  const server = session.issue.title.trim();
  const chosen = reader.lastTyped?.trim() ?? null;
  if (title === server || title === chosen) return [];
  const typed = chosen === null ? "nothing" : JSON.stringify(chosen);
  return [
    {
      invariant: "I4",
      read: `at rest the field shows ${JSON.stringify(title)}, the issue holds ${JSON.stringify(server)}, the reader chose ${typed}`,
    },
  ];
}

const longest = 5;

/**
 * Plays every sequence of one to `longest` events. An event that does not apply where it falls (a
 * landing with nothing out) is never chosen there, since the sequence it would leave is a shorter
 * one already played. A hook cannot be copied, so each sequence is played from a fresh mount; one
 * play of a five-event sequence also stands for each of its prefixes, and each prefix is judged on
 * the play that reaches it first.
 */
function explore(): Violation[] {
  const violations: Violation[] = [];
  const trace = new Map<string, TraceLine>();
  // The choice at each depth of the sequence being played: the events that applied there, and
  // which one was taken. A depth past the last one changed is played for the first time.
  const path: { options: EventName[]; index: number }[] = [];
  let firstNew = 0;
  for (;;) {
    const reader = new Reader();
    const { session } = reader;
    const events: EventName[] = [];
    try {
      for (let depth = 0; depth < longest; depth += 1) {
        if (depth === path.length) {
          path.push({ options: alphabet.filter((event) => reader.applies(event)), index: 0 });
        }
        const choice = path[depth];
        const event = choice.options[choice.index];
        events.push(event);
        const sequence = events.join(" ");
        const { findings } = reader.play(event);
        if (depth < firstNew) continue;
        const line: TraceLine = {
          sequence,
          sent: session.titlesSent(),
          title: session.drafts.title,
          violates: [],
        };
        trace.set(sequence, line);
        const atRestFindings = session.out === null ? atRest(reader) : [];
        for (const finding of [...findings, ...atRestFindings]) {
          violations.push({ ...finding, sequence, length: events.length });
          line.violates.push(finding.invariant);
        }
      }
    } finally {
      cleanup();
    }
    // The next sequence takes the next event at the deepest choice that has one left.
    let last = path.at(-1);
    while (last !== undefined && last.index + 1 >= last.options.length) {
      path.pop();
      last = path.at(-1);
    }
    if (last === undefined) break;
    last.index += 1;
    firstNew = path.length - 1;
  }

  // Off by default: a trace of every sequence, one JSON line each, so two heads of the hook can be
  // compared sequence by sequence.
  const tracePath = process.env.DRAFTS_TRACE;
  if (tracePath) {
    const lines = [...trace.values()].map((line) => `${JSON.stringify(line)}\n`);
    writeFileSync(tracePath, lines.join(""));
  }
  violations.sort((a, b) => a.length - b.length);
  return violations;
}

// The four tests below share one exploration: it is the slow part, and it judges all four at once.
let explored: Violation[] | undefined;
function exploration(): Violation[] {
  explored ??= explore();
  return explored;
}

const invariants: [Invariant, string][] = [
  ["I1", "nothing is sent the reader did not last choose"],
  ["I2", "a discarded value is never sent later"],
  ["I3", "no value is sent twice in a row"],
  ["I4", "at rest the field shows the server's value or the reader's unsent choice"],
];

for (const [invariant, statement] of invariants) {
  test(`every sequence of one to five events keeps ${invariant}: ${statement}`, () => {
    const broken = exploration().filter((v) => v.invariant === invariant);
    expect({
      sequences: new Set(broken.map((v) => v.sequence)).size,
      shortest: broken.slice(0, 5).map((v) => `${v.sequence}: ${v.invariant} ${v.read}`),
    }).toEqual({ sequences: 0, shortest: [] });
  });
}

// The three orders below were found in review, each a way the hook sent or showed a title the
// reader had not chosen.

test("an escape while a saved title is out, then an end with nothing typed, sends no second title", () => {
  const session = new Session();
  session.type("a");
  session.end();
  session.escape();
  session.end();
  session.land();
  expect(session.titlesSent()).toEqual(["a"]);
});

test("a draft typed back to the title out after a queued end keeps that title and sends no other", () => {
  const session = new Session();
  session.type("a");
  session.end();
  session.type("ab");
  session.end();
  session.type("a");
  session.land();
  expect({ title: session.drafts.title, sent: session.titlesSent() }).toEqual({
    title: "a",
    sent: ["a"],
  });
});

test("the issue's title typed back, queued and refused gives way to another reader's title and a route save sends only the route", () => {
  const session = new Session();
  session.type("a");
  session.end();
  session.type(startIssue.title);
  session.end();
  session.refuse();
  session.otherReader("Plan the review");
  const title = session.drafts.title;
  const before = session.received.length;
  session.step(() => session.drafts.writeRoute("role:a"));
  session.step(() => session.drafts.requestRouteSubmit());
  expect({ title, routeSave: session.received.slice(before) }).toEqual({
    title: "Plan the review",
    routeSave: [{ route: "role:a" }],
  });
});

// A write that lands with no answer is passed the issue as it was (`next ?? issue` in both
// headers), which still holds the old title until the page reads the issue again. The field the
// write carried takes the value it carried, so the draft stays the title it sent and reads as not
// changed, rather than going back to the old title until that read lands.
test("a title write that lands with no answer keeps the title it sent", () => {
  const session = new Session();
  session.type("a");
  session.end();
  session.step(() => {
    session.drafts.onIssueSuccess(session.issue, { title: "a" });
    session.drafts.onIssueSettled(null);
  });
  expect({
    title: session.drafts.title,
    changed: session.drafts.titleDirty,
    sent: session.titlesSent(),
  }).toEqual({ title: "a", changed: false, sent: ["a"] });
});
