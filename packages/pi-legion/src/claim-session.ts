import type { LegionTask } from "@legion/contracts/legion-api";
import { messageFor } from "@legion/envoy-client/errors";
import {
  type ClaimCaller,
  claimCaller,
  type LegionDaemonClient,
} from "@legion/pi-shared/daemon-client";
import {
  exportJjSessionAttribution,
  type PaneAttribution,
  paneAttributionOptions,
  sessionIdFromTranscript,
} from "@legion/pi-shared/jj-attribution";
import { bootLegionClaim } from "@legion/pi-shared/legion-claim-core";
import type { ClaimCapability } from "@legion/pi-shared/legion-phase-ops";
import type { SessionContext } from "@legion/pi-shared/pi-types";
import { claimEnvoyRole, onEnvoyRoleRegained } from "@legion/pi-shared/role-claim-bridge";
import { recordBootstrappedSession } from "@legion/pi-shared/subagent-session";
import { type ClaimTaskTake, takeClaimTask } from "@legion/pi-shared/task-take";
import { logger } from "@oh-my-pi/pi-utils";
import pkg from "../package.json";
import { requiredEnvironment, requiredSecret } from "./classify";

export interface ClaimSession {
  /** Boots the claim behind a root architect's or a phase worker's pane (`legion.ts` calls it for
   * every Legion session but the controller). A second call while the first runs waits on it; a
   * call once the claim is registered, from its session or another, boots nothing. */
  readonly bootstrap: (context: SessionContext) => Promise<void>;
  /** The claim `sessionID` registered, or undefined: before its registration, and for any other
   * session. */
  readonly capability: (sessionID: string) => ClaimCapability | undefined;
  /** A run of this pane's session started (`legion.ts` calls it from `agent_start`, never for a
   * `task` subagent's event): the claim's take no longer finds the host idle, and the first run
   * after an accepted take is that task's turn (`@legion/pi-shared/task-take`); the run's tool
   * results may read the takes it holds (`heldNotes`). It does nothing before the claim is
   * registered. */
  readonly runStarted: () => void;
  /** That run ended (`agent_end`): the number of the task whose turn it was, which the turn's
   * outcome report names, or undefined for a run of no task or a session with no claim.
   * `providerError` is whether that report carries the provider's failure; without one the take
   * holds the task finished (`TaskTake.runEnded`). The takes the run's holds reads named, and a
   * read still unanswered, are dropped: each such take is accepted at this turn's end. */
  readonly runEnded: (providerError: boolean) => number | undefined;
  /** The daemon refused with a 4xx the report of a turn of task `number` that carried no provider
   * error (`src/turn-outcome.ts`): the take holds the number finished no longer, since the record
   * still owes it (`TaskTake.reportRefused`). It does nothing before the claim is registered. */
  readonly reportRefused: (number: number) => void;
  /** The number of the claim's own task to note at this tool result (dispatch://INFRA-4130 R1,
   * `taskWaitingNote`): the take's `noteFor`, which gives each number once and records it, so
   * only the host's call at a tool result of the session's own agent may make it. Undefined
   * where the session holds no take. */
  readonly taskNote: () => number | undefined;
  /** Whether the claim's take is pending (R6, `TaskTake.pending`), so that neither the phase
   * reminder nor the stall abort's restart message starts a turn ahead of its task; false where
   * the session holds no take. */
  readonly taskPending: () => boolean;
  /**
   * The host's call at each tool result of the session's own agent (R2): the takes of the
   * issue's other claims that the daemon holds behind this claim's turn, as the latest holds
   * read of this run named them, less those this process noted before (once per role and task).
   *
   * The call also starts that read while a run is going, at the first tool result or at the first
   * one a minute or more after the last read started (`HOLDS_READ_EVERY_MS`): a `claims/task`
   * with the claim's caller, of which only `holds` is kept. The take is handed none of it, since
   * its inputs are the registration, the relay and its own reads. The tool result does not wait
   * for the read, so its answer is noted at the next tool result after it. A read bounded out
   * (`HOLDS_READ_BOUND_MS`) or failed notes nothing, an answer that comes after the run's end is
   * dropped, and no read starts while no run is going. Empty before the claim is registered.
   */
  readonly heldNotes: () => readonly HeldTake[];
}

/** A take of another claim of the issue that the daemon holds behind this claim's turn: that
 * claim's role, and its task's number (`holds` of `POST /legion/v1/claims/task`). */
export type HeldTake = NonNullable<LegionTask["holds"]>[number];

/** How long after a holds read started the next may start: R2's own bound of a minute. */
const HOLDS_READ_EVERY_MS = 60_000;

/** How long a holds read is waited for: the request is aborted then, and the read notes
 * nothing. */
const HOLDS_READ_BOUND_MS = 3_000;

/**
 * What this pane's jj attribution overlay is written from
 * (`exportJjSessionAttribution`, `@legion/pi-shared/jj-attribution`): the Oh My Pi session id read
 * off the transcript (undefined for a subagent's, which owns no claim), the state directory the
 * daemon names, and the pane's own key, workspace and role (`paneAttributionOptions`, which holds
 * the rule both harnesses read those three variables by).
 */
export function attributionInputs(
  sessionFile: string,
  env: NodeJS.ProcessEnv
): PaneAttribution & {
  readonly sessionId: string | undefined;
  readonly stateDir: string;
} {
  return {
    sessionId: sessionIdFromTranscript(sessionFile),
    stateDir: requiredEnvironment(env, "LEGION_STATE_DIR"),
    ...paneAttributionOptions(env),
  };
}

/**
 * Owns a root architect's or a phase worker's claim: the boot, through the claim boot every
 * harness shares (`bootLegionClaim`, `@legion/pi-shared/legion-claim-core`), the capability it
 * registered, and the take of the claim's task (`takeClaimTask`, `@legion/pi-shared/task-take`).
 * A Legion pane boots as its own Oh My Pi process and holds exactly one claim for its whole
 * lifetime, so both are plain closure state, set before `claims/ready`. The ready carries no
 * capability report: the session's capability rows are measured later, on its own interval.
 *
 * The take holds the claim's value from the registration's answer on, which is also when it sets
 * the task-turn mark, and takes the value the boot reads again once the ready has answered, which
 * holds any value the ready decision wrote, both before the role is claimed; it takes each value
 * the Envoy entry hands over from a task frame the listener relayed, and reads the value again
 * from the daemon whenever the session's relay begins a new epoch and whenever the heartbeat
 * regains the role. From the registration until the read after the ready has answered, the take
 * holds the mark whatever value it holds (`TaskTake.awaitRead`): the session's agent subject is
 * open from its start, and a card addressed to it in that window, which the ready decision's
 * value may not have reached yet, waits like every other. The Envoy name is taken right after
 * that read, so the session is addressable from its ready on, and the lane the listener kept for
 * the role arrives while the mark holds its cards back where a task is owed.
 *
 * The take asks only from a timer that fires once `session_start` has returned: Oh My Pi holds a
 * send made from `session_start` until its startup is over, and runs a send made while its
 * startup drains its sends inside that drain, while RPC mode reads no command at all, so a task's
 * turn started there would keep the daemon's shim from reaching the session for the whole turn.
 * The timer is a macrotask, and the drain's end, up to the return of the host's extension
 * startup, runs in microtasks (`packages/pi-envoy/src/card-wake.ts`, "The startup phase"). An
 * accepted take's text goes to `runTask`.
 *
 * Both notes of dispatch://INFRA-4130 are read here and written by the host at a tool result:
 * the take's own (`taskNote`), and the takes of the issue's other claims that the daemon holds
 * behind this claim's turn (`heldNotes`), which this session reads from the `holds` of its own
 * `claims/task` while a run of it is going. Every claim session reads them, a phase worker's and
 * an architect's alike, since every claim of an issue in a turn holds that issue's phase takes;
 * the controller's claim is not this session's and has no issue.
 *
 * The transcript is recorded as the bootstrapped session before the registration; the jj
 * attribution (`exportJjSessionAttribution`) runs once it answered, before the capability is set.
 * A 4xx registration refusal, and any failure after a registration, exit the process.
 */
export function createClaimSession(deps: {
  readonly daemon: () => LegionDaemonClient;
  readonly persistedTranscript: (
    context: SessionContext
  ) => Promise<{ readonly sessionFile: string; readonly agentId: string }>;
  /** Sends an accepted take's text as the turn's user message. */
  readonly runTask: (text: string) => void;
  readonly exitProcess: (code: number) => never;
  /** The clock the holds reads are a minute apart by (`heldNotes`). */
  readonly now: () => number;
}): ClaimSession {
  let capability: ClaimCapability | undefined;
  let booting: Promise<void> | undefined;
  let taking: ClaimTaskTake | undefined;
  // Ends the take's hold on its mark, kept until the read after the ready has answered
  // (`afterReadyRead`).
  let readAnswered: (() => void) | undefined;
  // The holds read (`heldNotes`): the daemon and the caller it reads with, set at the
  // registration; the run going now, counted so that an answer of an ended run is dropped; when
  // the last read started; the takes this run's latest answer named; and the role and task of
  // each take this process has noted.
  let holdsFrom: { readonly daemon: LegionDaemonClient; readonly caller: ClaimCaller } | undefined;
  let run: number | undefined;
  let runs = 0;
  let lastHoldsRead: number | undefined;
  let held: readonly HeldTake[] = [];
  const noted = new Set<string>();

  const boot = async (
    context: SessionContext,
    sessionID: string,
    bootToken: string,
    daemon: LegionDaemonClient
  ): Promise<void> => {
    const { sessionFile, agentId } = await deps.persistedTranscript(context);
    recordBootstrappedSession(sessionFile);
    const claimBoot = await bootLegionClaim({
      daemon,
      registration: {
        bootToken,
        sessionId: sessionID,
        ompSessionFile: sessionFile,
        agentId,
        pluginContract: pkg.legion.daemonApiVersion,
      },
      afterRegister: async (claim) => {
        const caller = claimCaller(claim, sessionID);
        taking = takeClaimTask({
          daemon,
          caller,
          run: deps.runTask,
          log: logger,
        });
        holdsFrom = { daemon, caller };
        taking.take.read(claim);
        readAnswered = taking.take.awaitRead();
        const { sessionId, stateDir, ...pane } = attributionInputs(sessionFile, process.env);
        if (sessionId) await exportJjSessionAttribution(sessionId, stateDir, pane);
      },
      setCapability: (registered) => {
        capability = registered;
      },
      // The claim's role topic is where the daemon sends every notice for an architect (the
      // notice executor, packages/daemon/internal/daemon/outbox.go), and where the listener
      // relays the claim's task value: no issue topic carries one, so no claim subscribes to one.
      claimRole: (role) => claimEnvoyRole(sessionID, role, context),
      taskValue: (reading) => taking?.take.read(reading),
      afterReadyRead: () => readAnswered?.(),
      onRoleRegained: onEnvoyRoleRegained,
      log: (line) => console.error(`[legion] ${line}`),
      stopping: "exiting so the daemon launches it again",
      stop: () => deps.exitProcess(1),
    });
    if (claimBoot === undefined) return;
    const take = taking?.take;
    setImmediate(() => take?.ready());
  };

  const bootstrap = async (context: SessionContext): Promise<void> => {
    const sessionID = context.sessionManager.getSessionId();
    if (capability !== undefined && capability.sessionID !== sessionID) return;
    if (booting !== undefined) return booting;

    const bootToken = requiredSecret(process.env, "LEGION_BOOT_TOKEN");
    booting = boot(context, sessionID, bootToken, deps.daemon());
    try {
      await booting;
    } catch (error) {
      booting = undefined;
      throw error;
    }
  };

  return {
    bootstrap,
    capability: (sessionID) => (capability?.sessionID === sessionID ? capability : undefined),
    runStarted: () => {
      if (taking === undefined) return;
      runs += 1;
      run = runs;
      taking.take.runStarted();
    },
    runEnded: (providerError) => {
      run = undefined;
      held = [];
      return taking?.take.runEnded(providerError);
    },
    reportRefused: (number) => taking?.take.reportRefused(number),
    taskNote: () => taking?.take.noteFor(),
    taskPending: () => taking?.take.pending() ?? false,
    heldNotes: () => {
      const from = holdsFrom;
      if (from === undefined || run === undefined) return [];
      const due = held.filter((take) => !noted.has(`${take.role} ${take.task}`));
      for (const take of due) noted.add(`${take.role} ${take.task}`);
      const now = deps.now();
      if (lastHoldsRead === undefined || now - lastHoldsRead >= HOLDS_READ_EVERY_MS) {
        lastHoldsRead = now;
        const during = run;
        void from.daemon
          .task(from.caller, { signal: AbortSignal.timeout(HOLDS_READ_BOUND_MS) })
          .then(
            (reading) => {
              // The run it was read in has ended, and each take it names is accepted at that end.
              if (run !== during) return;
              held = reading.holds ?? [];
            },
            (error: unknown) => {
              logger.debug(
                `legion: a claims/task read of the takes held behind this turn failed or had no answer within ${HOLDS_READ_BOUND_MS} ms; it notes nothing`,
                { error: messageFor(error) }
              );
            }
          );
      }
      return due;
    },
  };
}
