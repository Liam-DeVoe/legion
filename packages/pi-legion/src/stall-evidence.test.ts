import { afterEach, expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import type { LegionState } from "@legion/contracts/legion-api";
import {
  EnvoyApiError,
  type PublishInput,
  type PublishResult,
} from "@legion/envoy-client/transport";
import type { ClaimCapability } from "@legion/pi-shared/legion-phase-ops";
import type { EnvoyPublish } from "@legion/pi-shared/phase-reminder";
import { DEFAULT_STALL_LIMITS, defaultCallBoundMs } from "@legion/pi-shared/stall-limits";
import {
  createStallWatchdog,
  type StallCall,
  type StallEvidence,
  type StallWatchdog,
  type StallWatchdogTeller,
} from "@legion/pi-shared/stall-watchdog";
// The import above is this file's merge-base check: on the merge base there is no
// `./stall-evidence`, so every test here fails to resolve rather than passing vacuously.
import type { LegionSessionKind } from "./classify";
import {
  boundedText,
  createStallTeller,
  readStallProcesses,
  STALL_DIGEST_LIMIT,
  STALL_READ_ENVIRONMENT,
  STALL_WATCH_ENTRY,
  type StallEvidenceRecord,
  type StallWatchRecord,
  stallArgumentsDigest,
  stallEvidenceRecord,
  stallNoticeTopic,
  stallProcessTree,
} from "./stall-evidence";

const SECOND = 1_000;
const MINUTE = 60 * SECOND;
const START = 1_793_000_000_000;

// ---------------------------------------------------------------------------------------------
// Real processes, read from this pod's own /proc.
// ---------------------------------------------------------------------------------------------

const spawned: Bun.Subprocess[] = [];

afterEach(() => {
  for (const child of spawned.splice(0)) child.kill("SIGKILL");
});

/** One real child of this process, with the environment the case needs, left running until the
 * case ends. `sh -c` is what makes a process of its own: a command Oh My Pi runs in its own shell
 * has none (measured). */
async function child(environment: Record<string, string>): Promise<number> {
  // `exec`, so the process the case reads is the one `kill` ends: a shell that forks `sleep`
  // leaves it running when the shell is killed, reparented to pid 1 (measured).
  const running = Bun.spawn(["sh", "-c", "exec sleep 300"], {
    env: { ...process.env, ...environment },
    stdio: ["ignore", "ignore", "ignore"],
  });
  spawned.push(running);
  // The pid is live the moment `spawn` returns, but the kernel carries the process and its
  // environment a moment later. What is awaited is that reading, not a guessed delay: this case
  // is about a real process tree, so there is no clock to fake.
  await until(async () => {
    const scan = await readStallProcesses();
    if (!scan.read) return false;
    const found = scan.processes.find((candidate) => candidate.pid === running.pid);
    return (
      found !== undefined &&
      found.environment.PI_TOOL_CALL_ID === environment.PI_TOOL_CALL_ID &&
      found.command.includes("sleep 300")
    );
  });
  return running.pid;
}

/** Waits for one reading of the machine to come true, up to ten seconds. */
async function until(ready: () => Promise<boolean>): Promise<void> {
  for (let attempt = 0; attempt < 1_000; attempt += 1) {
    if (await ready()) return;
    await Bun.sleep(10);
  }
  throw new Error("the machine never reached the state the case needs");
}

test("the tree names every process of the agent, read from this pod's own /proc", async () => {
  const pid = await child({
    PI_TOOL_CALL_ID: "toolu_stuck",
    // A credential of the kind an environment routinely holds: the reader keeps two names and
    // this is not one of them, so nothing downstream can write it anywhere.
    LEGION_CLAIM_SECRET: "a-secret-nothing-may-record",
  });

  const scan = await readStallProcesses();
  if (!scan.read) throw new Error(`this pod's /proc could not be read: ${scan.reason}`);
  const agent = scan.processes[0];
  expect(agent?.pid).toBe(process.pid);
  // Measurement 1: the agent's own environment is the one `/proc` will not hand it here, so the
  // reader reads other pids only.
  expect(agent?.environment).toEqual({});

  const found = scan.processes.find((candidate) => candidate.pid === pid);
  expect(found).toBeDefined();
  expect(found?.ppid).toBe(process.pid);
  expect(found?.command).toContain("sleep 300");
  // The identifier, the start time, the state and the processor time, as the kernel reports them.
  expect(found?.startedAt).toBeGreaterThan(Date.now() - 60 * SECOND);
  expect(found?.startedAt).toBeLessThanOrEqual(Date.now());
  expect(found?.state).toMatch(/^[A-Za-z]$/);
  expect(found?.cpuMs).toBeGreaterThanOrEqual(0);
  // The call its environment names, which is exactly the frame's `toolCallId` (measured).
  expect(found?.toolCallId).toBe("toolu_stuck");
  // And nothing else of that environment is kept, whatever else the process inherited.
  expect(
    Object.keys(found?.environment ?? {}).filter(
      (name) => !STALL_READ_ENVIRONMENT.some((wanted) => wanted === name)
    )
  ).toEqual([]);

  // What the evidence carries: the same processes, with no environment and no parent.
  const tree = stallProcessTree(scan);
  if (!tree.read) throw new Error("the tree was read and then reported unread");
  expect(JSON.stringify(tree)).not.toContain("a-secret-nothing-may-record");
  expect(tree.processes.find((candidate) => candidate.pid === pid)).toEqual({
    pid,
    command: found?.command as string,
    startedAt: found?.startedAt as number,
    state: found?.state as string,
    cpuMs: found?.cpuMs as number,
    toolCallId: "toolu_stuck",
  });
});

test("a machine whose /proc cannot be read records that, never an empty tree", async () => {
  const absent = path.join(mkdtempSync(path.join(tmpdir(), "stall-proc-")), "proc");
  try {
    const scan = await readStallProcesses({ procRoot: absent });
    expect(scan.read).toBe(false);
    if (scan.read) throw new Error("a /proc that is not there was read");
    expect(scan.reason).toContain(absent);
    // And the evidence says the same thing, rather than carrying no process at all.
    expect(stallProcessTree(scan)).toEqual({ read: false, reason: scan.reason });
    const record = stallEvidenceRecord(evidenceOf({ tree: stallProcessTree(scan) }), START, {
      session: { kind: "phase-worker", role: "tester", tree: "INFRA-1000", issue: "INFRA-1274" },
      claim: claimOf(),
    });
    expect(record.tree).toEqual({ read: false, reason: scan.reason });
  } finally {
    rmSync(path.dirname(absent), { recursive: true, force: true });
  }
});

// ---------------------------------------------------------------------------------------------
// The record: the tool, the digest, the start, the age, the tree.
// ---------------------------------------------------------------------------------------------

/** One overdue call, with whatever the case needs changed. */
function callOf(over: Partial<StallCall> = {}): StallCall {
  return {
    toolCallId: "toolu_stuck",
    toolName: "bash",
    input: { command: "sleep 100000", pty: true },
    startedAt: START,
    deadlineMs: undefined,
    step: 0,
    steppedAt: undefined,
    told: undefined,
    ...over,
  };
}

function evidenceOf(over: Partial<StallEvidence> = {}): StallEvidence {
  return {
    call: callOf(),
    overdue: {
      rule: { kind: "bound", boundMs: 60 * MINUTE },
      openMs: 65 * MINUTE,
      pastMs: 5 * MINUTE,
    },
    tree: { read: true, processes: [] },
    ...over,
  };
}

function claimOf(over: Partial<ClaimCapability> = {}): ClaimCapability {
  return {
    sessionID: "ses_worker",
    claimToken: "claim-token",
    generation: 3,
    tree: "INFRA-1000",
    issue: "INFRA-1274",
    role: "implementer",
    secret: "claim-secret",
    ...over,
  };
}

test("the record names the tool, the digest, the start, the age and the real tree", async () => {
  const pid = await child({ PI_TOOL_CALL_ID: "toolu_stuck" });
  const tree = stallProcessTree(await readStallProcesses());
  const at = START + 65 * MINUTE;
  const record = stallEvidenceRecord(evidenceOf({ tree }), at, {
    session: { kind: "phase-worker", role: "implementer", tree: "INFRA-1000", issue: "INFRA-1274" },
    claim: claimOf(),
  });

  expect(record.kind).toBe("evidence");
  expect(record.at).toBe(new Date(at).toISOString());
  expect(record.issue).toBe("INFRA-1274");
  expect(record.role).toBe("implementer");
  expect(record.toolCallId).toBe("toolu_stuck");
  expect(record.tool).toBe("bash");
  expect(record.arguments).toBe('command="sleep 100000" pty=true');
  expect(record.startedAt).toBe(new Date(START).toISOString());
  expect(record.openMs).toBe(65 * MINUTE);
  expect(record.allowedMs).toBe(60 * MINUTE);
  expect(record.rule).toEqual({ kind: "bound", boundMs: 60 * MINUTE });

  if (!record.tree.read) throw new Error(`the tree was not read: ${record.tree.reason}`);
  const process = record.tree.processes.find((candidate) => candidate.pid === pid);
  expect(process?.command).toContain("sleep 300");
  expect(process?.startedAt).toMatch(/^\d{4}-\d{2}-\d{2}T/);
  expect(process?.state).toMatch(/^[A-Za-z]$/);
  expect(typeof process?.cpuMs).toBe("number");
  expect(process?.toolCallId).toBe("toolu_stuck");
});

test("the record's allowance is the call's own deadline plus the grace where it promised one", () => {
  const record = stallEvidenceRecord(
    evidenceOf({
      overdue: {
        rule: { kind: "deadline", deadlineMs: 5 * MINUTE, graceMs: 5 * MINUTE },
        openMs: 11 * MINUTE,
        pastMs: MINUTE,
      },
    }),
    START,
    { session: { kind: "controller" }, claim: undefined }
  );
  expect(record.allowedMs).toBe(10 * MINUTE);
  expect(record.session).toBe("controller");
  expect(record.issue).toBeUndefined();
  expect(record.role).toBeUndefined();
});

test("an argument far longer than the bound is cut to it rather than written whole", () => {
  const tail = "end-of-the-very-long-argument";
  const digest = stallArgumentsDigest({ command: `${"x".repeat(5_000)} ${tail}`, timeout: 0 });
  expect(digest.length).toBeLessThanOrEqual(STALL_DIGEST_LIMIT);
  expect(digest).not.toContain(tail);
  expect(digest).toContain("characters in all");
  expect(digest.startsWith('command="xxxx')).toBe(true);

  // The bound is a bound, whatever it is set to, and the marker is counted against it.
  expect(boundedText("x".repeat(100), 40).length).toBe(40);
  expect(boundedText("short", 40)).toBe("short");
  // A command's many lines read as one.
  expect(stallArgumentsDigest({ command: "one\n  two\n" })).toBe('command="one\\n two\\n"');
});

// ---------------------------------------------------------------------------------------------
// The telling: who hears, and how often.
// ---------------------------------------------------------------------------------------------

/** One wait the re-sends asked for: how long, and the end the test gives it, since no clock does.
 * A rig whose waits end at once (`waits: "at-once"`) has ended it already. */
interface ResendWait {
  readonly ms: number;
  readonly end: () => void;
}

interface LogLine {
  readonly message: string;
  readonly context?: Record<string, unknown>;
}

interface TellerRig {
  readonly teller: StallWatchdogTeller;
  readonly entries: StallWatchRecord[];
  readonly published: PublishInput[];
  readonly warnings: LogLine[];
  readonly infos: LogLine[];
  /** What the listener answers once `failures` is spent. */
  publishAnswer: Pick<PublishResult, "kept" | "keptUntil">;
  /** What the next publishes throw, one each, in order, before the listener answers. */
  readonly failures: Error[];
  /** Every wait the re-sends asked for, in order. */
  readonly waits: ResendWait[];
  /** Whether Envoy is on for this session. */
  envoy: boolean;
  /** Whether `pi.appendEntry` takes the record. */
  transcriptWritable: boolean;
  /** The daemon's answer to the record check; a string makes the read throw. */
  state: LegionState | string;
}

function tellerRig(
  options: {
    session?: LegionSessionKind;
    claim?: ClaimCapability | undefined;
    project?: string;
    now?: () => number;
    /** The detector's book of open calls the teller reads before each re-send of an overdue
     * notice; the stuck call alone, open for the whole case, when it is left out. */
    openCalls?: () => readonly StallCall[];
    /** Whether each wait the re-sends ask for lasts until the case ends it (`by-hand`, when it is
     * left out) or ends at once, so the re-sends run to their end inside one `settled`. */
    waits?: "by-hand" | "at-once";
  } = {}
): TellerRig {
  const state: Omit<TellerRig, "teller"> = {
    entries: [],
    published: [],
    warnings: [],
    infos: [],
    publishAnswer: {},
    failures: [],
    waits: [],
    envoy: true,
    transcriptWritable: true,
    state: daemonState("working"),
  };
  const publish: EnvoyPublish = async (input) => {
    state.published.push(input);
    const failure = state.failures.shift();
    if (failure !== undefined) throw failure;
    return state.publishAnswer;
  };
  const teller = createStallTeller({
    now: options.now ?? (() => START),
    session: () =>
      options.session ?? {
        kind: "phase-worker",
        role: "implementer",
        tree: "INFRA-1000",
        issue: "INFRA-1274",
      },
    claim: () => ("claim" in options ? options.claim : claimOf()),
    project: options.project ?? "legion",
    readState: async () => {
      if (typeof state.state === "string") throw new Error(state.state);
      return state.state;
    },
    publisher: () => (state.envoy ? publish : undefined),
    appendEntry: (customType, data) => {
      expect(customType).toBe(STALL_WATCH_ENTRY);
      if (!state.transcriptWritable) throw new Error("the transcript is not writable");
      state.entries.push(data as StallWatchRecord);
    },
    log: {
      debug: () => {},
      info: (message, context) => state.infos.push({ message, ...(context ? { context } : {}) }),
      warn: (message, context) => state.warnings.push({ message, ...(context ? { context } : {}) }),
    },
    recordBoundMs: 200,
    openCalls: options.openCalls ?? (() => [callOf()]),
    sleep: (ms) => {
      if (options.waits === "at-once") {
        state.waits.push({ ms, end: () => {} });
        return Promise.resolve();
      }
      const { promise, resolve } = Promise.withResolvers<void>();
      state.waits.push({ ms, end: resolve });
      return promise;
    },
  });
  return Object.assign(state, { teller });
}

/** The daemon's state with this issue's own architect claim in `state`. */
function daemonState(architect: string | undefined): LegionState {
  return {
    issues: {
      "INFRA-1274": {
        ...(architect === undefined
          ? {}
          : { architect: { session: "ses_architect", state: architect, pluginContract: 57 } }),
      },
    },
  } as unknown as LegionState;
}

/** The listener's answer `status`, thrown as the Envoy client's `publish` throws every answer that
 * is not a 2xx. */
function listenerAnswered(
  status: number,
  body: { readonly error: string; readonly reason?: string; readonly [field: string]: unknown }
): EnvoyApiError {
  return new EnvoyApiError({
    method: "POST",
    url: "http://127.0.0.1:4000/v1/messages/publish",
    status,
    responseBody: JSON.stringify(body),
  });
}

/** What Bun's `fetch` throws when the listener's port refuses the connection. */
const UNREACHABLE = "Unable to connect. Is the computer able to access the url?";
/** The listener's 503 when the secrets broker it checks a token with does not answer. */
const BROKER_DOWN = "the secrets broker cannot be reached; try again in a minute";

/** One turn of the event loop, which waits on no duration: nothing the re-sends do waits on a
 * clock but the rig's own waits, so every promise they chain settles inside it, and they are at
 * their next wait or done when this returns; a rig whose waits end at once runs them to their end
 * inside it, since every microtask runs before a timer's callback. It is a turn rather than an
 * awaited event because a case here asserts that nothing more happened — no second publish, no
 * further wait — and the absence of an event has no event to await; `legion.test.ts` waits the
 * same way for the same reason. */
async function settled(): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>();
  setTimeout(resolve, 0);
  await promise;
}

test("a phase worker's session tells the issue's own live architect, with the evidence", async () => {
  const rig = tellerRig();
  const told = await rig.teller.tellOverdue(evidenceOf());

  expect(told).toEqual({ evidenceWritten: true, ownerTold: true });
  // The record goes into the session's transcript.
  const written = rig.entries[0] as StallEvidenceRecord;
  expect(written.kind).toBe("evidence");
  expect(written.tool).toBe("bash");
  expect(written.arguments).toBe('command="sleep 100000" pty=true');
  // And into the message: the same record, as the payload the listener takes.
  expect(rig.published).toHaveLength(1);
  const notice = rig.published[0] as PublishInput;
  expect(notice.topic).toBe("notifications.role.legion-legion-infra-1274-architect");
  expect(notice.message).toContain("The implementer of INFRA-1274 has a `bash` call");
  expect(notice.message).toContain('command="sleep 100000" pty=true');
  expect(JSON.parse(notice.payload as string)).toEqual(written);
  expect(notice.idempotencyKey).toBe("legion-stall:toolu_stuck");
  expect(notice).not.toHaveProperty("keep");
  expect("sourceSessionID" in notice && notice.sourceSessionID).toBe("ses_worker");
});

test("a phase worker falls back to the tree's architect, as the reminder does", async () => {
  for (const architect of ["retired", "failed", undefined]) {
    const rig = tellerRig();
    rig.state = daemonState(architect);
    await rig.teller.tellOverdue(evidenceOf());
    expect(rig.published[0]?.topic).toBe("notifications.role.legion-legion-infra-1000-architect");
  }
  // A daemon that does not answer inside the bound is the same: the tree's architect is told.
  const silent = tellerRig();
  silent.state = "the daemon is unreachable";
  await silent.teller.tellOverdue(evidenceOf());
  expect(silent.published[0]?.topic).toBe("notifications.role.legion-legion-infra-1000-architect");
});

test("a root architect's own session tells the controller role", async () => {
  const rig = tellerRig({
    session: { kind: "root-architect", tree: "INFRA-1000" },
    claim: claimOf({ issue: "INFRA-1000", role: "architect", sessionID: "ses_root" }),
  });
  await rig.teller.tellOverdue(evidenceOf());
  expect(rig.published).toHaveLength(1);
  expect(rig.published[0]?.topic).toBe("notifications.role.legion-legion-controller");
  expect(rig.published[0]?.message).toContain("The architect of INFRA-1000");
});

// The notice sends no `keep` field, so a listener keeps it for an owner's role that no live
// session holds, and answers 202 kept. That has told no owner yet, and the teller does not send a
// kept notice again, so it reports `ownerTold: false`: for the architect, and for the
// controller, which owns a root architect's own call (dispatch://INFRA-2846, R6).
test("the owner notice is kept", async () => {
  const architect = tellerRig();
  const controller = tellerRig({
    session: { kind: "root-architect", tree: "INFRA-1000" },
    claim: claimOf({ issue: "INFRA-1000", role: "architect", sessionID: "ses_root" }),
  });
  for (const [rig, topic] of [
    [architect, "notifications.role.legion-legion-infra-1274-architect"],
    [controller, "notifications.role.legion-legion-controller"],
  ] as const) {
    rig.publishAnswer = { kept: true, keptUntil: START + 60 * MINUTE };
    expect(await rig.teller.tellOverdue(evidenceOf())).toEqual({
      evidenceWritten: true,
      ownerTold: false,
    });
    expect(rig.published).toHaveLength(1);
    expect(rig.published[0]?.topic).toBe(topic);
    expect(rig.published[0]).not.toHaveProperty("keep");
    expect(rig.warnings).toContainEqual({
      message: "legion: the listener kept the stall notice; no owner has taken it",
      context: expect.objectContaining({ topic }),
    });
  }
});

test("the controller's own session publishes nothing and writes its evidence instead", async () => {
  const rig = tellerRig({ session: { kind: "controller" }, claim: undefined });
  const told = await rig.teller.tellOverdue(evidenceOf());

  expect(rig.published).toEqual([]);
  expect(told).toEqual({ evidenceWritten: true, ownerTold: false });
  expect((rig.entries[0] as StallEvidenceRecord).session).toBe("controller");
  expect(rig.warnings.map((line) => line.message)).toContain(
    "legion: a tool call of this session has not returned"
  );
  expect(rig.warnings.map((line) => line.message)).toContain(
    "legion: nobody was told about this session's stuck tool call"
  );
});

test("a session never publishes to the role its own session holds", async () => {
  // A sub-architect is a phase worker whose own claim is its issue's live architect.
  const rig = tellerRig({
    session: { kind: "phase-worker", role: "architect", tree: "INFRA-1000", issue: "INFRA-1300" },
    claim: claimOf({ issue: "INFRA-1300", role: "architect", sessionID: "ses_sub" }),
  });
  rig.state = {
    issues: {
      "INFRA-1300": { architect: { session: "ses_sub", state: "working", pluginContract: 57 } },
    },
  } as unknown as LegionState;
  await rig.teller.tellOverdue(evidenceOf());
  expect(rig.published[0]?.topic).toBe("notifications.role.legion-legion-infra-1000-architect");

  // And the controller's own session, which holds the only role its notice could reach.
  expect(
    stallNoticeTopic({
      session: { kind: "controller" },
      claim: undefined,
      project: "legion",
      liveArchitect: true,
    })
  ).toEqual({
    kind: "none",
    why: "the controller's own session is the owner, so its evidence is its transcript and its log",
  });
});

test("a refused publish and a session with Envoy off leave a line in the log", async () => {
  const refused = tellerRig();
  refused.failures.push(
    listenerAnswered(404, { error: "no session holds the role", reason: "unclaimed" })
  );
  expect(await refused.teller.tellOverdue(evidenceOf())).toEqual({
    evidenceWritten: true,
    ownerTold: false,
  });
  expect(refused.warnings.map((line) => line.message)).toContain(
    "legion: the owner was not told about this session's stuck tool call"
  );

  const off = tellerRig();
  off.envoy = false;
  expect(await off.teller.tellOverdue(evidenceOf())).toEqual({
    evidenceWritten: true,
    ownerTold: false,
  });
  expect(off.published).toEqual([]);
  expect(off.warnings.map((line) => line.message)).toContain(
    "legion: Envoy is off for this session, so its owner was not told"
  );

  // A transcript the host refuses leaves the evidence unwritten, which is what stops the ladder's
  // last step; the owner is still told.
  const unwritable = tellerRig();
  unwritable.transcriptWritable = false;
  expect(await unwritable.teller.tellOverdue(evidenceOf())).toEqual({
    evidenceWritten: false,
    ownerTold: true,
  });

  // A claim whose issue key no role token can be built from is one owner not told, and the
  // evidence is written all the same.
  const unbuildable = tellerRig({ claim: claimOf({ issue: "not-an-issue-key" }) });
  expect(await unbuildable.teller.tellOverdue(evidenceOf())).toEqual({
    evidenceWritten: true,
    ownerTold: false,
  });
  expect(unbuildable.published).toEqual([]);
});

test("each step and each stop is recorded, and a stop is told once", async () => {
  const rig = tellerRig();
  await rig.teller.recordStep({
    call: callOf(),
    step: "end-processes",
    openMs: 65 * MINUTE,
    outcome: "ended 1 process of the call",
  });
  expect(rig.entries[0]).toEqual({
    kind: "step",
    at: new Date(START).toISOString(),
    toolCallId: "toolu_stuck",
    tool: "bash",
    step: "end-processes",
    openMs: 65 * MINUTE,
    outcome: "ended 1 process of the call",
  });

  await rig.teller.tellStopped(callOf(), "the exit cap was reached");
  await rig.teller.tellStopped(callOf(), "the exit cap was reached");
  expect(rig.entries.filter((entry) => entry.kind === "stopped")).toHaveLength(2);
  // The owner hears why the ladder stopped, and hears it once.
  expect(rig.published).toHaveLength(1);
  expect(rig.published[0]?.message).toContain("cannot recover from on its own");
  expect(rig.published[0]?.idempotencyKey).toBe("legion-stall-stopped:toolu_stuck");
});

// ---------------------------------------------------------------------------------------------
// The re-sends: a publish no rule decides is sent again until the listener stores it, after the
// wait the listener names where it names one, while the notice is still true, for four minutes at
// most.
// ---------------------------------------------------------------------------------------------

test("a publish no rule decides is sent again under its one key until the listener stores it", async () => {
  const rig = tellerRig();
  rig.failures.push(
    listenerAnswered(504, {
      error: "no forward the stream stored for the holder",
      reason: "delivery_failed",
    }),
    new DOMException("The operation timed out.", "TimeoutError")
  );
  // The ladder has its answer from the first attempt, with nobody told yet, and waits for none of
  // what follows.
  expect(await rig.teller.tellOverdue(evidenceOf())).toEqual({
    evidenceWritten: true,
    ownerTold: false,
  });
  expect(rig.published).toHaveLength(1);
  await settled();
  expect(rig.waits.map((wait) => wait.ms)).toEqual([1_000]);

  rig.waits[0]?.end();
  await settled();
  expect(rig.published).toHaveLength(2);
  expect(rig.waits.map((wait) => wait.ms)).toEqual([1_000, 2_000]);

  rig.waits[1]?.end();
  await settled();
  // The third attempt is stored, and the re-sends end with it: no further wait is asked for.
  expect(rig.published).toHaveLength(3);
  expect(rig.waits).toHaveLength(2);
  // One notice, three times: the same topic, message, payload and idempotency key.
  expect(rig.published[1]).toEqual(rig.published[0] as PublishInput);
  expect(rig.published[2]).toEqual(rig.published[0] as PublishInput);
  expect(rig.published[0]?.idempotencyKey).toBe("legion-stall:toolu_stuck");
  // Each failed attempt is a warning with its number and its error; the attempt that told the
  // owner says that it told it late.
  const failed = rig.warnings.filter(
    (line) => line.message === "legion: the stall notice was not stored, so it is sent again"
  );
  expect(failed.map((line) => line.context?.attempt)).toEqual([1, 2]);
  expect(failed[0]?.context?.error).toContain("no forward the stream stored for the holder");
  expect(failed[1]?.context?.error).toContain("timed out");
  expect(rig.infos).toEqual([
    {
      message: "legion: the owner was told late about this session's stuck tool call",
      context: expect.objectContaining({
        attempt: 3,
        toolCallId: "toolu_stuck",
        topic: "notifications.role.legion-legion-infra-1274-architect",
      }),
    },
  ]);
});

test("a 4xx refusal is final, and only a failure no rule decides is sent again", async () => {
  // A 4xx is the listener's own word on this notice, and the same notice under the same key would
  // get the same word: every 4xx but 408 and the refusal for this session's send rate.
  for (const body of [
    { status: 400, error: "payload must be a JSON string" },
    { status: 403, error: "this session's capability does not reach that topic" },
    { status: 404, error: "no session holds the role", reason: "unclaimed" },
    { status: 413, error: "the message is larger than the listener takes" },
    // The stream's own refusal of the message itself (`streamRefusedPrefix`,
    // `packages/envoy/cmd/listener/api.go`): the bus answered, and answers the same however often
    // the notice is sent, so the listener's 409 carries no `Retry-After`.
    {
      status: 409,
      error:
        "envoy: the message bus stream refused the message, and refuses it however often it is " +
        "sent: nats: API error: code=400 err_code=10054 description=message size exceeds maximum allowed",
    },
    // A 429 that is not the refusal for this session's send rate: the listener's 429 for too many
    // access tokens checked from one address (`refuseBrokerToken`,
    // `packages/envoy/cmd/listener/apiauth.go`) carries no `sender_rate_limited` reason.
    { status: 429, error: "too many access tokens checked from this address" },
  ]) {
    const rig = tellerRig();
    rig.failures.push(listenerAnswered(body.status, body));
    expect(await rig.teller.tellOverdue(evidenceOf())).toEqual({
      evidenceWritten: true,
      ownerTold: false,
    });
    await settled();
    expect(rig.published).toHaveLength(1);
    expect(rig.waits).toEqual([]);
    expect(rig.warnings).toContainEqual({
      message: "legion: the owner was not told about this session's stuck tool call",
      context: expect.objectContaining({ attempt: 1, error: expect.stringContaining(body.error) }),
    });
  }

  // Everything else is sent again: a 5xx, 408, the one 4xx answer that says "later" rather than
  // "no", a timeout, a connection that failed — and an error whose text names a 404, because the
  // status is read from the client's error type, never from an error's words. The refusal for
  // this session's send rate is sent again too, after the wait it names (below).
  for (const failure of [
    listenerAnswered(500, { error: "internal error" }),
    listenerAnswered(503, { error: BROKER_DOWN }),
    listenerAnswered(504, { error: "no forward the stream stored", reason: "delivery_failed" }),
    listenerAnswered(408, { error: "request timeout" }),
    new DOMException("The operation timed out.", "TimeoutError"),
    new Error(UNREACHABLE),
    new Error('listener returned 404 Not Found: {"reason":"unclaimed"}'),
  ]) {
    const rig = tellerRig();
    rig.failures.push(failure);
    await rig.teller.tellOverdue(evidenceOf());
    await settled();
    expect(rig.waits.map((wait) => wait.ms)).toEqual([1_000]);
    rig.waits[0]?.end();
    await settled();
    expect(rig.published).toHaveLength(2);
    expect(rig.waits).toHaveLength(1);
  }
});

test("a second telling of a call whose notice is being sent again starts no second run", async () => {
  const rig = tellerRig();
  rig.failures.push(
    ...Array.from({ length: 8 }, () => listenerAnswered(503, { error: BROKER_DOWN }))
  );
  await rig.teller.tellOverdue(evidenceOf());
  await settled();
  expect(rig.waits).toHaveLength(1);

  // Told again while the first re-send waits: the first answer, and nothing published, written or
  // waited for on its account.
  expect(await rig.teller.tellOverdue(evidenceOf())).toEqual({
    evidenceWritten: true,
    ownerTold: false,
  });
  await settled();
  expect(rig.published).toHaveLength(1);
  expect(rig.waits).toHaveLength(1);
  expect(rig.entries.filter((entry) => entry.kind === "evidence")).toHaveLength(1);

  // The one run goes on, each wait twice the last up to a minute, until the listener stores it.
  for (let wait = 0; wait < 8; wait += 1) {
    rig.waits[wait]?.end();
    await settled();
  }
  expect(rig.waits.map((wait) => wait.ms)).toEqual([
    1_000, 2_000, 4_000, 8_000, 16_000, 32_000, 60_000, 60_000,
  ]);
  expect(rig.published).toHaveLength(9);
  expect(rig.published.map((notice) => notice.idempotencyKey)).toEqual(
    Array.from({ length: 9 }, () => "legion-stall:toolu_stuck")
  );
  expect(rig.infos).toHaveLength(1);

  // And a telling after the late one publishes nothing either.
  await rig.teller.tellOverdue(evidenceOf());
  await settled();
  expect(rig.published).toHaveLength(9);
  expect(rig.waits).toHaveLength(8);
});

test("a stop's notice is sent again the same way, and a stop already told starts no second run", async () => {
  const rig = tellerRig();
  rig.failures.push(new Error(UNREACHABLE), new Error(UNREACHABLE));
  await rig.teller.tellStopped(callOf(), "the exit cap was reached");
  await settled();
  expect(rig.published).toHaveLength(1);
  expect(rig.waits.map((wait) => wait.ms)).toEqual([1_000]);

  // The same stop again, while the first one's re-send waits: written again, told no second time.
  await rig.teller.tellStopped(callOf(), "the exit cap was reached");
  await settled();
  expect(rig.entries.filter((entry) => entry.kind === "stopped")).toHaveLength(2);
  expect(rig.published).toHaveLength(1);
  expect(rig.waits).toHaveLength(1);

  rig.waits[0]?.end();
  await settled();
  expect(rig.waits.map((wait) => wait.ms)).toEqual([1_000, 2_000]);
  rig.waits[1]?.end();
  await settled();
  expect(rig.published).toHaveLength(3);
  expect(rig.waits).toHaveLength(2);
  expect(rig.published[2]).toEqual(rig.published[0] as PublishInput);
  expect(rig.published[0]?.idempotencyKey).toBe("legion-stall-stopped:toolu_stuck");
  expect(rig.infos).toEqual([
    {
      message: "legion: the owner was told late about this session's stuck tool call",
      context: expect.objectContaining({ attempt: 3, reason: "the exit cap was reached" }),
    },
  ]);
});

test("an overdue notice the ladder's stop notice supersedes is sent no more", async () => {
  const rig = tellerRig();
  rig.failures.push(listenerAnswered(503, { error: BROKER_DOWN }));
  await rig.teller.tellOverdue(evidenceOf());
  await settled();
  expect(rig.waits.map((wait) => wait.ms)).toEqual([1_000]);

  // The ladder stops short for the same call while the overdue notice's re-send waits, and the
  // stop's own notice is stored.
  await rig.teller.tellStopped(callOf(), "the exit cap was reached");
  expect(rig.published.map((notice) => notice.idempotencyKey)).toEqual([
    "legion-stall:toolu_stuck",
    "legion-stall-stopped:toolu_stuck",
  ]);

  // The wait's end sends the overdue notice no more and asks for no further wait: the owner has
  // the stop, which is the newer news about the same call.
  rig.waits[0]?.end();
  await settled();
  expect(rig.published).toHaveLength(2);
  expect(rig.waits).toHaveLength(1);
  expect(rig.infos).toEqual([
    {
      message:
        "legion: the stall notice is not sent again, because the ladder's stop notice supersedes it",
      context: expect.objectContaining({ key: "legion-stall:toolu_stuck", attempts: 1 }),
    },
  ]);
});

test("a stop's notice whose call has ended is still sent again until it is stored or the cap ends it", async () => {
  // The call ends while the stop's notice is being sent again, as the ladder's own abort usually
  // ends it: that the ladder stopped short for the call is still true, so the re-sends go on.
  let open: StallCall[] = [callOf()];
  const stored = tellerRig({ openCalls: () => open });
  stored.failures.push(new Error(UNREACHABLE), new Error(UNREACHABLE));
  await stored.teller.tellStopped(callOf(), "the exit cap was reached");
  await settled();
  stored.waits[0]?.end();
  await settled();
  expect(stored.published).toHaveLength(2);
  expect(stored.waits).toHaveLength(2);

  open = [];
  stored.waits[1]?.end();
  await settled();
  expect(stored.published).toHaveLength(3);
  expect(stored.waits).toHaveLength(2);
  expect(stored.infos).toEqual([
    {
      message: "legion: the owner was told late about this session's stuck tool call",
      context: expect.objectContaining({ attempt: 3, reason: "the exit cap was reached" }),
    },
  ]);

  // A call already ended when the ladder stopped short: the stop's notice is sent again until the
  // cap ends its re-sends, and nothing else ends them.
  let clock = START;
  const capped = tellerRig({ now: () => clock, openCalls: () => [] });
  capped.failures.push(
    ...Array.from({ length: 20 }, () => listenerAnswered(503, { error: BROKER_DOWN }))
  );
  await capped.teller.tellStopped(callOf(), "the exit cap was reached");
  await settled();
  for (let wait = 0; wait < 20; wait += 1) {
    const next = capped.waits[wait];
    if (next === undefined) break;
    clock += next.ms;
    next.end();
    await settled();
  }
  expect(capped.published).toHaveLength(9);
  expect(capped.infos).toEqual([]);
  expect(capped.warnings).toContainEqual({
    message: "legion: the stall notice is not sent again, because its re-sends reached their cap",
    context: expect.objectContaining({ key: "legion-stall-stopped:toolu_stuck", attempts: 9 }),
  });
});

test("a notice the listener keeps failing is sent again for four minutes at most, then given up with one warning", async () => {
  const capMessage =
    "legion: the stall notice is not sent again, because its re-sends reached their cap";
  for (const notice of [
    {
      key: "legion-stall:toolu_stuck",
      tell: (teller: StallWatchdogTeller) => teller.tellOverdue(evidenceOf()),
    },
    {
      key: "legion-stall-stopped:toolu_stuck",
      tell: (teller: StallWatchdogTeller) =>
        teller.tellStopped(callOf(), "the exit cap was reached"),
    },
  ]) {
    let clock = START;
    const rig = tellerRig({ now: () => clock });
    rig.failures.push(
      ...Array.from({ length: 20 }, () => listenerAnswered(503, { error: BROKER_DOWN }))
    );
    await notice.tell(rig.teller);
    await settled();
    // Each wait passes on the teller's clock before the rig ends it. Twenty waits are far past the
    // cap, so a loop the cap does not end runs until the failures are spent and a late attempt is
    // stored, rather than waiting on a wait nobody ends.
    for (let wait = 0; wait < 20; wait += 1) {
      const next = rig.waits[wait];
      if (next === undefined) break;
      clock += next.ms;
      next.end();
      await settled();
    }
    // The next wait, a minute, would start the tenth attempt 243 seconds after the first, past the
    // ladder's two step gaps: the ninth failure is the last.
    expect(rig.waits.map((wait) => wait.ms)).toEqual([
      1_000, 2_000, 4_000, 8_000, 16_000, 32_000, 60_000, 60_000,
    ]);
    expect(rig.published).toHaveLength(9);
    expect(rig.warnings.filter((line) => line.message === capMessage)).toEqual([
      {
        message: capMessage,
        context: expect.objectContaining({
          key: notice.key,
          attempts: 9,
          sentForMs: 183_000,
          capMs: 2 * DEFAULT_STALL_LIMITS.stepMs,
          error: expect.stringContaining(BROKER_DOWN),
        }),
      },
    ]);
    // The ninth failure is that warning alone, never also a promise of a tenth attempt.
    expect(
      rig.warnings
        .filter(
          (line) => line.message === "legion: the stall notice was not stored, so it is sent again"
        )
        .map((line) => line.context?.attempt)
    ).toEqual([1, 2, 3, 4, 5, 6, 7, 8]);
    expect(rig.infos).toEqual([]);

    await settled();
    expect(rig.published).toHaveLength(9);
    expect(rig.waits).toHaveLength(8);
  }
});

test("a re-send that tells the owner after a failure no rule decides corrects the teller's record", async () => {
  const rig = tellerRig();
  rig.failures.push(listenerAnswered(503, { error: BROKER_DOWN }));
  expect(await rig.teller.tellOverdue(evidenceOf())).toEqual({
    evidenceWritten: true,
    ownerTold: false,
  });
  await settled();
  expect(rig.waits.map((wait) => wait.ms)).toEqual([1_000]);

  rig.waits[0]?.end();
  await settled();
  expect(rig.published).toHaveLength(2);
  // The re-send was stored, so the owner was told late, and a later telling of the same call
  // answers that the owner was told, with nothing published again.
  expect(await rig.teller.tellOverdue(evidenceOf())).toEqual({
    evidenceWritten: true,
    ownerTold: true,
  });
  expect(rig.published).toHaveLength(2);
  expect(rig.infos).toEqual([
    {
      message: "legion: the owner was told late about this session's stuck tool call",
      context: expect.objectContaining({ attempt: 2, toolCallId: "toolu_stuck" }),
    },
  ]);
});

/** The 429 the listener answers a publish past this session's send limit with (an operator's
 * values of 4 messages a minute and 2 at once), as the transport throws it. */
function senderRateLimited(retryAfterSeconds: number): EnvoyApiError {
  return listenerAnswered(429, {
    error:
      "Your sending is rate limited. This message was not sent. This listener lets one session " +
      `send 4 messages a minute and 2 at once. You can send it again after ${retryAfterSeconds} seconds.`,
    reason: "sender_rate_limited",
    retry_after_seconds: retryAfterSeconds,
    messages_per_minute: 4,
    burst: 2,
  });
}

const GIVEN_UP = "it was given up after 4 sends";

test("a stall notice rate limited by the listener is sent again under its key after the wait", async () => {
  const rig = tellerRig({ waits: "at-once" });
  rig.failures.push(senderRateLimited(2));
  // The first answer comes back with the first publish's outcome: the wait holds no step.
  expect(await rig.teller.tellOverdue(evidenceOf())).toEqual({
    evidenceWritten: true,
    ownerTold: false,
  });
  await settled();

  expect(rig.waits.map((wait) => wait.ms)).toEqual([2 * SECOND]);
  expect(rig.published).toHaveLength(2);
  expect(rig.published[1]?.idempotencyKey).toBe("legion-stall:toolu_stuck");
  expect(rig.published[1]).toEqual(rig.published[0] as PublishInput);
  // The resend was answered and not kept, so the owner was told, and the teller's record says so.
  expect(await rig.teller.tellOverdue(evidenceOf())).toEqual({
    evidenceWritten: true,
    ownerTold: true,
  });
  expect(rig.published).toHaveLength(2);
  expect(rig.warnings).toContainEqual({
    message: expect.stringContaining("the wait the listener named"),
    context: expect.objectContaining({ retryAfterSeconds: 2, sends: 1 }),
  });
  expect(rig.warnings).toContainEqual({
    message: expect.stringContaining("its owner was told"),
    context: expect.objectContaining({ sends: 2 }),
  });
});

test("a stall notice rate limited 4 times is sent 4 times and then given up", async () => {
  const rig = tellerRig({ waits: "at-once" });
  // Each refusal names its own wait, and each wait is that refusal's.
  rig.failures.push(...[2, 3, 5, 7].map(senderRateLimited));
  await rig.teller.tellOverdue(evidenceOf());
  await settled();

  expect(rig.published).toHaveLength(4);
  for (const input of rig.published) expect(input).toEqual(rig.published[0] as PublishInput);
  expect(rig.waits.map((wait) => wait.ms)).toEqual([2 * SECOND, 3 * SECOND, 5 * SECOND]);
  expect(rig.warnings.filter((line) => line.message.includes(GIVEN_UP))).toHaveLength(1);
  expect(rig.warnings.at(-1)?.message).toContain(GIVEN_UP);
  expect(await rig.teller.tellOverdue(evidenceOf())).toEqual({
    evidenceWritten: true,
    ownerTold: false,
  });
});

test("a 429 rate limited for another reason is not sent again", async () => {
  for (const refusal of [
    // A refusal whose reason is not the sender's rate starts no resend, whatever wait it names.
    listenerAnswered(429, {
      error: "too many requests",
      reason: "rate_limited",
      retry_after_seconds: 2,
    }),
    // The listener's 429 for too many access tokens checked from one address, as it writes it.
    listenerAnswered(429, { error: "too many access tokens checked from this address" }),
  ]) {
    const rig = tellerRig({ waits: "at-once" });
    rig.failures.push(refusal);
    expect(await rig.teller.tellOverdue(evidenceOf())).toEqual({
      evidenceWritten: true,
      ownerTold: false,
    });
    await settled();

    expect(rig.published).toHaveLength(1);
    expect(rig.waits).toEqual([]);
    expect(rig.warnings.map((line) => line.message)).toContain(
      "legion: the owner was not told about this session's stuck tool call"
    );
  }
});

// ---------------------------------------------------------------------------------------------
// Driven by the detector itself: one message per stuck call, and none for a call that ends.
// ---------------------------------------------------------------------------------------------

function watchdogWith(teller: StallWatchdogTeller, clock: () => number): StallWatchdog {
  return createStallWatchdog({
    now: clock,
    limits: { ...DEFAULT_STALL_LIMITS, callBoundMs: defaultCallBoundMs("tester") },
    isIdle: () => false,
    processes: { tree: () => ({ read: true, processes: [] }), endCall: () => 0 },
    teller,
    aborter: { abortTurn: () => {}, sendMessage: () => {} },
    exits: { read: () => 0, write: () => true },
    exitProcess: () => {},
    log: { debug: () => {}, warn: () => {} },
  });
}

test("a second sweep of the same still-open call sends no second message", async () => {
  let clock = START;
  const rig = tellerRig({ now: () => clock });
  const watchdog = watchdogWith(rig.teller, () => clock);
  watchdog.callRequested({ toolName: "eval", toolCallId: "toolu_stuck", input: { code: "1" } });
  watchdog.executionStarted({ toolName: "eval", toolCallId: "toolu_stuck", input: { code: "1" } });

  clock += 61 * MINUTE;
  await watchdog.sweep();
  expect(rig.published).toHaveLength(1);

  // The call is still open, and the ladder carries it further without telling anyone again.
  clock += 3 * MINUTE;
  await watchdog.sweep();
  clock += 3 * MINUTE;
  await watchdog.sweep();
  expect(rig.published).toHaveLength(1);
  expect(rig.entries.filter((entry) => entry.kind === "evidence")).toHaveLength(1);
  expect(rig.entries.filter((entry) => entry.kind === "step").length).toBeGreaterThan(1);

  // And a teller told twice about one call repeats nothing, whatever reaches it.
  const again = await rig.teller.tellOverdue(evidenceOf({ call: callOf({ toolName: "eval" }) }));
  expect(again).toEqual({ evidenceWritten: true, ownerTold: true });
  expect(rig.published).toHaveLength(1);
});

test("a call that ends before the bound sends nothing at all", async () => {
  let clock = START;
  const rig = tellerRig({ now: () => clock });
  const watchdog = watchdogWith(rig.teller, () => clock);
  watchdog.callRequested({ toolName: "eval", toolCallId: "toolu_quick", input: {} });
  watchdog.executionStarted({ toolName: "eval", toolCallId: "toolu_quick", input: {} });
  clock += 59 * MINUTE;
  await watchdog.sweep();
  watchdog.executionEnded({ toolCallId: "toolu_quick" });
  clock += 10 * MINUTE;
  await watchdog.sweep();

  expect(rig.published).toEqual([]);
  expect(rig.entries).toEqual([]);
});

test("an overdue notice whose call returns while it is sent again is sent no more", async () => {
  let clock = START;
  let watchdog: StallWatchdog | undefined;
  // The teller reads the detector's own book of open calls, as the entry wires it
  // (`extensions/legion.ts`, `armStallWatch`).
  const rig = tellerRig({ now: () => clock, openCalls: () => watchdog?.openCalls() ?? [] });
  watchdog = watchdogWith(rig.teller, () => clock);
  rig.failures.push(
    ...Array.from({ length: 4 }, () => listenerAnswered(503, { error: BROKER_DOWN }))
  );
  watchdog.callRequested({ toolName: "eval", toolCallId: "toolu_stuck", input: { code: "1" } });
  watchdog.executionStarted({ toolName: "eval", toolCallId: "toolu_stuck", input: { code: "1" } });
  clock += 61 * MINUTE;
  await watchdog.sweep();
  await settled();
  expect(rig.published).toHaveLength(1);
  expect(rig.waits.map((wait) => wait.ms)).toEqual([1_000]);

  // While the call is open, a re-send goes out.
  rig.waits[0]?.end();
  await settled();
  expect(rig.published).toHaveLength(2);
  expect(rig.waits).toHaveLength(2);

  // The call returns while the second wait runs: the wait's end sends nothing and asks for no
  // further wait, and the log says why, once.
  watchdog.executionEnded({ toolCallId: "toolu_stuck" });
  rig.waits[1]?.end();
  await settled();
  expect(rig.published).toHaveLength(2);
  expect(rig.waits).toHaveLength(2);
  expect(rig.infos).toEqual([
    {
      message: "legion: the stall notice is not sent again, because its tool call has ended",
      context: expect.objectContaining({
        toolCallId: "toolu_stuck",
        key: "legion-stall:toolu_stuck",
        attempts: 2,
      }),
    },
  ]);
});
