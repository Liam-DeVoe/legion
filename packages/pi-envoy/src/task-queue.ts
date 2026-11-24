import { ROLE_TOPIC_PREFIX } from "@legion/contracts";
import { messageFor } from "@legion/envoy-client/errors";
import {
  EnvoyApiError,
  type Interest,
  isBusUnanswered,
  roleHeldBy,
  type SubscribeResult,
} from "@legion/envoy-client/transport";
import type { RoleRegainReason } from "@legion/pi-shared/role-claim-bridge";
import type { HandBackLedger } from "./hand-back-ledger";
import type { RegistrationInput, RegistrationStateDeps } from "./registration-state";
import type { SessionInbound } from "./session-inbound";

/**
 * The queue code of the Envoy extension's two serial queues (`createQueue`, `TaskQueue`): each
 * queue's order, each task's deadline timer, which abandons the task when it fires, and the steps a
 * queued task takes, which write the registration state of `./registration-state`. That module
 * builds and holds the two queues (`createRegistrationState`'s `passes` and `roleMoves`).
 *
 * A queued task never re-checks a fence before a step, and keeps {@link TaskHandle.check} for the
 * two things that are not steps: a catch-all that would otherwise swallow the refusal, and an
 * action outside the handle that a refused task must not take. `run` hands it a
 * {@link TaskHandle}, and every state write, listener request, subscribe and inbound
 * connection it makes after a wait is a method of that handle. The handle refuses the step once
 * the task's deadline has fired, for a task bound to a session (`actFor`) once that session
 * stopped being the open session, and for a role move once the session's stop has begun
 * (`stopping`). The first refusal latches: a session that closes and opens again does not revive
 * a task a step already refused, so a task that caught the refusal cannot go on writing. A
 * refused step writes nothing and ends the task: past the deadline the caller's promise has
 * already rejected with the deadline rejection ({@link taskWasAbandoned}) and the late settlement
 * is swallowed; after the session moved, or once the stop began, `run` resolves `undefined`, so
 * a pass the host left mid-pass decides nothing and the user sees no new warning.
 */

/** A queued task abandoned at its deadline: the queue has moved on, and the task's own
 *  continuation writes nothing when its await finally resolves. Module-private, so
 *  {@link taskWasAbandoned} is the one way to ask: the extension reads the rejection without
 *  naming the class. */
class AbandonedTaskError extends Error {}

/** Whether `error` is a queued task's deadline rejection, which a caller reads as a stall rather
 *  than as a failure of the work the task was doing. */
export function taskWasAbandoned(error: unknown): boolean {
  return error instanceof AbandonedTaskError;
}

/**
 * A step the task's handle refused. Module-private: it never leaves `run`, which turns it into the
 * task's outcome — swallowed on an abandoned task's late settlement, `undefined` once the task's
 * session moved or, for a role move, once the session's stop began.
 */
class StepRefused extends Error {}

/** Whether `error` is a step the task's handle refused (a task abandoned at its deadline, one
 *  whose session moved, or a role move of a session whose stop began): no failure of the work, so
 *  no reason a notice may name. */
export function stepWasRefused(error: unknown): boolean {
  return error instanceof StepRefused;
}

/** The role this process gave up locally while the listener may still name it. */
export interface RoleEnded {
  readonly role: string;
  /** Whether a plugin held notice subjects for it, which the role's end closed. */
  readonly pluginHeld: boolean;
}

/** What a heartbeat's re-assertion of a held role decided. */
export interface RoleReassertion {
  /** False when the hold moved off this role while the listener was read or claimed. */
  readonly decided: boolean;
  /** Set when the Legion regain hook owes a run for this role. */
  readonly regain?: RoleRegainReason;
}

/**
 * The steps a queued task may take. Each refuses before it acts, so no call site re-checks a
 * fence before a step. Refusal throws, which ends the task, and latches: once a step refused, so
 * does every step after it, whatever the session does next. A role move's steps refuse once the
 * session's stop has begun as well (`RegistrationStateDeps.stopping`).
 */
export interface TaskHandle {
  /**
   * Binds this task to the session it acts for, called once as soon as the task learns it. From
   * then on every step refuses after that session stopped being the open session. Binding twice
   * throws: a task acts for one session.
   */
  readonly actFor: (id: string) => void;
  /**
   * Refuses here, and otherwise does nothing. Two things need it, because neither is a step:
   * code that catches whatever a step threw and would otherwise swallow the refusal, and an
   * action outside the handle (a replay trigger, a throw the caller reports) that a refused task
   * must not take. Before a step, it is redundant: the step refuses itself.
   */
  readonly check: () => void;

  // — registration —
  /** Registers the session at the listener, carrying this instance's client instance id. */
  readonly register: (input: RegistrationInput) => Promise<SubscribeResult>;

  // — the session a pass converges to —
  /** Records that this session's agent subject, registration and Dispatch binding are converged. */
  readonly markEstablished: (id: string) => void;
  /** Forgets every agent session but `host`; answers the ones whose subject the caller must close.
   */
  readonly forgetOtherAgentSessions: (host: string) => readonly string[];
  /** Records that this process may have `id`'s agent subject open. */
  readonly markAgentSessionOpen: (id: string) => void;
  /** Records that `id`'s agent subject, stream watch and registered interests are open in full. */
  readonly markInboundOpen: (id: string) => void;
  /** Records that a pass decided the role for `id`. */
  readonly markRoleSettled: (id: string) => void;

  // — the role —
  /**
   * Ends the role this process holds and hands its hold back at the listener. Answers the role it
   * ended, or `undefined` when it held none. With `tellUser`, says so in the pane: a role end
   * after a move no session event announced is one no plugin heard.
   */
  readonly handBackHeldRole: (tellUser: boolean) => Promise<RoleEnded | undefined>;
  /**
   * Settles the holds session events gave up: one of `wanted`, under a session this process still
   * registers, comes back unconfirmed; every other is handed back, and one the listener refuses is
   * abandoned. Answers whether a hold came back.
   */
  readonly settleEndedHolds: (wanted: string | undefined) => Promise<boolean>;
  /**
   * Claims `role` for this task's session. A hard claim is last-claim-wins. A soft claim naming
   * `previousSessionID` takes the role from that predecessor only when the predecessor has no
   * live registry entry, when its live entry carries this client instance's own instance id, or
   * when either side carries no instance id. A predecessor still live under another client
   * instance keeps the role. A soft claim is also granted when the role is unheld, and when its
   * holder is no longer live. Answers whether the claim landed.
   *
   * A claim the listener granted after this task's deadline fired is left as a pending hand-back,
   * and one granted after the session moved is adopted under the id the listener names.
   */
  readonly claimRole: (
    role: string,
    options?: { readonly soft?: boolean; readonly previousSessionID?: string }
  ) => Promise<boolean>;
  /**
   * Makes sure the listener still resolves this session as `role`'s holder: reads first, and a
   * soft claim goes out only when it does not. The lookup confirms the role only when it names
   * this session *and* this process, since a claim standing under this very session id that
   * another process made is not this one's to renew; a holder naming no process is read as this
   * one's. A refusal naming a different session ends the role here and records it. A refusal
   * naming this session's own id is that other process's standing claim, which the listener
   * grants the moment it stops renewing it: the role stays held here, nothing is recorded or
   * said, and the next heartbeat asks again.
   */
  readonly reassertRole: (role: string, afterOutage: boolean) => Promise<RoleReassertion>;
  /** Says, once, that the notices a plugin opened for `role` stay closed, and forgets that it
   *  opened them. Writes nothing while that plugin's subjects are still open. */
  readonly noteNoticesClosed: (role: string) => void;

  // — listener reads and writes —
  /** The session the listener names as `role`'s holder; `undefined` when it names none or the
   *  read failed. */
  readonly readRoleHolder: (role: string) => Promise<string | undefined>;
  /** The listener's registered interests for a session; `undefined` when the read failed. */
  readonly readInterest: (sessionID: string) => Promise<Interest | undefined>;
  /** Unregisters `topics` as `sessionID`'s interests at the listener. A failure is warned in the
   *  caller's own words and swallowed, as a recovery's own unregister was: the next pass reads
   *  the registry afresh. The warning is `busUnanswered` when the listener's message bus did not
   *  answer (`isBusUnanswered`), since the listener can have written the record before that, and
   *  `failed` for any other failure. */
  readonly unregisterTopics: (
    sessionID: string,
    topics: readonly string[],
    warnings: { readonly failed: string; readonly busUnanswered: string }
  ) => Promise<void>;

  // — the inbound connection —
  /**
   * The session's one inbound connection, dialled if it has none. A connection this task dialled
   * and was refused while the dial was in flight is closed here and never answered, and so is a
   * step the acquisition itself outran: the task ends rather than acting on the connection.
   */
  readonly connect: () => Promise<SessionInbound>;
  /** Opens `topic`'s subjects; answers whether any were new. */
  readonly subscribe: (topic: string) => Promise<boolean>;
}

/** What a task that decided nothing left behind, for the caller that has to say so. */
export interface TaskRefusal {
  /**
   * Whether the listener granted this task's claim after its session had moved, so the role is
   * held here under the session the task acted for. The caller tells the model which of the two
   * happened: a claim that never landed, or one the next pass carries or hands back.
   */
  readonly claimAdopted: boolean;
}

/** One of the module's serial queues. */
export interface TaskQueue {
  /**
   * Runs tasks one at a time, in the order they were queued; a task that fails does not stop the
   * ones after it. Each task gets `deadlineMs()` milliseconds, read when that task starts, after
   * which the caller's promise rejects with the deadline rejection ({@link taskWasAbandoned}),
   * the queue starts the next task at once, and the abandoned task's own settlement is swallowed.
   * A task a refused step ended because its session moved, or a role move a step ended because
   * the session's stop began, resolves `undefined`, and `onRefused` is handed what that task left
   * behind.
   */
  readonly run: <T>(
    task: (handle: TaskHandle) => Promise<T>,
    onRefused?: (refusal: TaskRefusal) => void
  ) => Promise<T | undefined>;
  /** Resolves once every task queued so far has finished. */
  readonly settled: () => Promise<unknown>;
}

/**
 * The registration state a queued task's steps read and write, which `createRegistrationState`
 * owns and hands nothing else: the one role hold, the refusals and notice subjects of the role,
 * and the sessions a pass converged.
 */
export interface RegistrationRecord {
  // The one role this process holds at the listener, and the session id the listener names for
  // it, which a pass that keeps the role for another session supersedes by naming it.
  claimedRoleTopic: string | undefined;
  claimedRoleSessionID: string | undefined;
  // Whether that hold came back from a hand-back a session event left rather than from a claim the
  // listener granted: nothing has checked it at the listener since, so a pass claims it softly,
  // naming it, before trusting it.
  holdUnconfirmed: boolean;
  // Per session, a role the listener refused to that session's automatic claim because another
  // live session holds it: that holder is the newer one, so no pass claims the role for that
  // session again until a session event lands on it or the process restarts. A refusal naming
  // this session's own id is never one of them: that is another process's standing claim under
  // this id, which the listener grants the moment it stops renewing it, so neither `claimRole`
  // nor `reassertRole` records it and the next pass asks again.
  readonly refusedRoles: Map<string, string>;
  // The role a plugin last opened role-bound notices for: a pass that claims that role back after
  // a role end closed them says they stay closed until that plugin claims it again.
  roleNoticesFor: string | undefined;
  // Notice subjects this session takes only while it holds `claimedRoleTopic`. They are never
  // registered with the listener, so a resumed process cannot recover them: the role's claim is
  // their only source, and a role end closes them.
  readonly roleNoticeSubjects: Set<string>;
  // The session whose agent subject, registration and Dispatch binding the last pass converged to.
  establishedSession: string;
  // The session whose role the last pass decided.
  roleSettledFor: string;
  // The sessions whose agent subject this process may have open.
  readonly agentSessions: Set<string>;
  // The session whose agent subject, stream watch and registered interests a pass opened in full.
  inboundOpenFor: string;
}

/** What the queues and a task's handle act on: the registration state and its operations. */
export interface TaskContext {
  readonly deps: RegistrationStateDeps;
  /** The client instance id every claim of this extension instance carries. */
  readonly clientInstanceID: string;
  /** The holds this process gave up and still owes the listener. */
  readonly holds: HandBackLedger;
  readonly state: RegistrationRecord;
  /** The role this session holds, if any. */
  readonly heldRole: () => string | undefined;
  /** Ends the role this session holds and closes the notice subjects it took for it. */
  readonly endRole: () => void;
  /** Takes the hold of `topic` under `sessionID` (`createRegistrationState`'s `moveHold`). */
  readonly moveHold: (
    topic: string,
    sessionID: string,
    previousTopic: string | undefined,
    previousHoldID: string | undefined,
    recordClaim?: () => void
  ) => Promise<void>;
  /** Registers the session at the listener (`RegistrationState.refreshRegistration`). */
  readonly refreshRegistration: (input: RegistrationInput) => Promise<SubscribeResult>;
}

/** A task's handle and what the task left behind, which the queue reads when a step refused it.
 */
interface RunningTask {
  readonly handle: TaskHandle;
  readonly refusal: TaskRefusal;
}

const createHandle = (
  context: TaskContext,
  live: () => boolean,
  deadlineAt: number,
  stopped: () => boolean
): RunningTask => {
  const { deps, clientInstanceID, holds, state, heldRole, endRole, moveHold, refreshRegistration } =
    context;
  let bound: string | undefined;
  // Whether this task may still write: no step of it has been refused yet, its deadline has not
  // fired, the session's stop has not begun where the task is a role move (`stopped`) and, once
  // bound, its session is still the open one. The one question every step below asks, asked
  // here alone.
  //
  // The first refusal latches. A session can close and open again — a switch away and back —
  // while a task waits on I/O, and a task that caught its refusal would otherwise pass the next
  // step and go on writing for a pass that replaced it.
  let refused = false;
  const refusal = { claimAdopted: false };
  const acting = (): boolean => {
    if (refused) return false;
    if (!stopped() && live() && (bound === undefined || deps.sessionOpen(bound))) return true;
    refused = true;
    return false;
  };
  const refuse = (message: string): never => {
    refused = true;
    throw new StepRefused(message);
  };
  const guard = (): void => {
    if (acting()) return;
    refuse(
      stopped()
        ? "the role move refused a step: this session's stop has begun"
        : live()
          ? `the task acting for session ${bound} refused a step: that session is no longer the open one`
          : "the task refused a step: it was abandoned at its deadline"
    );
  };
  // The stop read after a wait whose outcome a role move acts on whatever else became of it: a
  // hand-back that landed, a claim the listener answered. Once the session's stop has begun,
  // the move ends here, and nothing it would do next is done.
  const refuseOnceStopping = (message: string): void => {
    if (stopped()) refuse(message);
  };
  const actingFor = (): string => {
    if (bound === undefined) {
      throw new Error("this queued task has not named the session it acts for (actFor)");
    }
    return bound;
  };

  const handBackHeldRole = async (tellUser: boolean): Promise<RoleEnded | undefined> => {
    guard();
    const role = heldRole();
    const holdID = state.claimedRoleSessionID;
    const topic = state.claimedRoleTopic;
    if (role === undefined || topic === undefined) return undefined;
    const pluginHeld = state.roleNoticeSubjects.size > 0;
    endRole();
    if (holdID !== undefined && holdID !== "") {
      await holds.handBackOrAbandon(topic, holdID, { role, sessionID: holdID });
    }
    // The end has landed, so the pane hears about it whatever became of this task, unless the
    // session's stop began meanwhile: a stopping session is told nothing.
    refuseOnceStopping(`the role move ended role ${role} as the session's stop began`);
    if (tellUser) {
      const host = bound ?? "";
      deps.log.warn("envoy: role ended after a move with no session event", {
        role,
        sessionID: host,
      });
      deps.notify(
        pluginHeld
          ? `envoy: role ${role} ended: the host moved to session ${host} with no session event, and that session's records do not name it; the plugin that opened its notices claims it again on its next session switch or restart`
          : `envoy: role ${role} ended: the host moved to session ${host} with no session event, and that session's records do not name it; if this session should hold it, run envoy_role_set`,
        "warning"
      );
    }
    return { role, pluginHeld };
  };

  const settleEndedHolds = async (wanted: string | undefined): Promise<boolean> => {
    guard();
    const restored = await holds.settleEnded(acting, (hold) => {
      // A hold is adopted only for the session this process still registers: one under a
      // session it has left may be another process's now, and a claim naming it would take
      // that live process's role.
      if (
        wanted === undefined ||
        hold.topic !== ROLE_TOPIC_PREFIX + wanted ||
        !deps.sessionOpen(hold.id)
      ) {
        return false;
      }
      state.claimedRoleTopic = hold.topic;
      state.claimedRoleSessionID = hold.id;
      state.holdUnconfirmed = true;
      return true;
    });
    // Refused during the last hand-back, or at the break inside it: the restore this task made
    // is the replacing one's to decide.
    guard();
    return restored;
  };

  const claimRole = async (
    role: string,
    options: { readonly soft?: boolean; readonly previousSessionID?: string } = {}
  ): Promise<boolean> => {
    const id = actingFor();
    guard();
    const topic = ROLE_TOPIC_PREFIX + role;
    const previousTopic = state.claimedRoleTopic;
    const previousHoldID = state.claimedRoleSessionID;
    const result = await deps.client().setRole({
      sessionID: id,
      role,
      soft: options.soft,
      previousSessionID: options.previousSessionID,
      // The process making the claim: a role already held under this very session id is the
      // listener's to decide on, and it decides on this id — granted where the standing claim
      // names this same process, refused where another process still renews it.
      clientInstanceID,
    });
    // The session's stop began while the claim was in flight. The stop has handed back what
    // this process held and deletes the session's record next, so nothing of the claim is kept:
    // no hold, no transcript record, no word. A claim the listener granted all the same waits
    // out the live window, as one the stop's release did not reach does.
    refuseOnceStopping(
      `the role move that claimed ${role} for session ${id} ended: the session's stop began while the claim was in flight`
    );
    if (!live()) {
      // The listener may have granted the claim while this task was abandoned. Nothing of it goes
      // in the transcript — the task that replaced this one owns the role from here — but the
      // hold is real at the listener, so leave it pending for the next pass to hand back.
      if (result.claimed) holds.recordEndedHold(topic, id);
      refuse(
        `the role move that claimed ${role} for session ${id} was abandoned at its deadline while the claim was in flight`
      );
    }
    if (result.claimed) {
      state.refusedRoles.delete(id);
      state.holdUnconfirmed = false;
      // The listener names this claim's session for the role now, so no hold of it this process
      // gave up is left to hand back or wait on (one under this same id would end the new hold).
      holds.forgetHoldsOf(topic);
    }
    if (!deps.sessionOpen(id)) {
      // The session moved while the claim was in flight. A hold that landed is kept as this
      // process's own, under the id the listener names, and the pass the move queues converges
      // it. Handing it back here would lose a role a fork should carry. The caller is told
      // which of the two happened through the refusal's `claimAdopted`.
      if (result.claimed) {
        await moveHold(topic, id, previousTopic, previousHoldID);
        refusal.claimAdopted = true;
      }
      refuse(`the session moved on while its claim of role ${role} was in flight`);
    }
    if (!result.claimed) {
      endRole();
      // A refusal naming this task's own session id is the standing claim of another process
      // under that id, which the listener grants the moment that process stops renewing it.
      // Nothing says another session took the role, so no refusal is latched and nothing is
      // said — the rule `reassertRole` keeps for the same state. A latch would make
      // `desiredRole` want no role until a session event landed, and the line would be written
      // out again on every heartbeat for as long as that other process lived; the next pass
      // claims again instead.
      if (result.holder === id) return false;
      // Only a soft claim is refused. A holder that is this process's own abandoned hold is
      // waited out, since the listener reaps it; any other is the newer holder.
      const waitingOnReap = await holds.observeAbandonedHold(topic, role, result.holder, live);
      // Read across a wait too: once the session's stop has begun, nothing is recorded or said.
      refuseOnceStopping(
        `the role move that claimed ${role} for session ${id} ended: the session's stop began before the refusal was recorded`
      );
      // The hold was read across a wait: this task may have been refused since, and the claim
      // that replaced it may already have cleared the refusal this one is about to record.
      // Nothing is written then, and the line says so.
      const notRecorded = !live()
        ? "the move was abandoned at its deadline before the refusal was recorded"
        : deps.sessionOpen(id)
          ? undefined
          : "the session moved on before the refusal was recorded";
      if (notRecorded === undefined && !waitingOnReap) state.refusedRoles.set(id, role);
      deps.log.warn(
        notRecorded !== undefined
          ? `envoy: role held by another live session; ${notRecorded}`
          : waitingOnReap
            ? "envoy: role still held by a session this process left; claiming again once the listener reaps it"
            : "envoy: role held by another live session; not reclaimed",
        { role, sessionID: id, holder: result.holder }
      );
      return false;
    }
    // The transcript is the one thing `omp --resume` guarantees, so it is the durable record of
    // the claim: the listener releases a role claim one session TTL after heartbeats stop, and
    // this process's memory dies with it. The record names the session claimed for and this
    // process, so a reader tells it from a record another process wrote on the same transcript.
    await moveHold(topic, id, previousTopic, previousHoldID, () =>
      deps.appendRoleClaimEntry({ role, sessionID: id, clientInstanceID })
    );
    // The stop began while the role this claim moved off was handed back. The stop handed back
    // the new hold with every other, so the move reports no role the session no longer holds.
    refuseOnceStopping(
      `the role move that claimed ${role} for session ${id} ended: the session's stop began as the claim landed`
    );
    return true;
  };

  const reassertRole = async (role: string, afterOutage: boolean): Promise<RoleReassertion> => {
    const id = actingFor();
    guard();
    const topic = ROLE_TOPIC_PREFIX + role;
    const client = deps.client();
    // Whether this process still holds the role the re-assertion was decided for: a session
    // event can end it while the listener is read or claimed, and the task that ends it owns
    // the role from there.
    const stillHolds = (): boolean => state.claimedRoleTopic === topic;
    const holding = await client.getRole(role).then(
      (info) => info,
      (error: unknown) => {
        // 404 is the listener's answer for "no live holder", not a failure.
        if (error instanceof EnvoyApiError && error.details.status === 404) return undefined;
        throw error;
      }
    );
    guard();
    if (!stillHolds()) return { decided: false };
    // This session *and* this process (`roleHeldBy`, the one rule both clients read): a claim
    // standing under this very session id that another process made is not this one's to
    // renew, while a holder naming no process at all is read as this one's.
    if (roleHeldBy(holding, id, clientInstanceID)) {
      return afterOutage ? { decided: true, regain: "reregistered" } : { decided: true };
    }
    const result = await client.setRole({ sessionID: id, role, soft: true, clientInstanceID });
    guard();
    if (!stillHolds()) return { decided: false };
    if (!result.claimed) {
      if (result.holder === id) {
        // The standing claim of another process under this session's id, which the listener
        // grants the moment that process stops renewing it. Nothing says another session took
        // the role, so the role stays held here and the next heartbeat asks again: no
        // `endRole`, and no refusal latched either — a latch would make `desiredRole` want no
        // role on the next pass and hand this one back, undoing what this branch keeps. The
        // pane is told nothing, because nothing changed for it, and the log is left alone too:
        // the state is normal, self-resolving, and would otherwise be written out again on
        // every heartbeat for as long as the other process lives. `claimRole` keeps the same
        // rule for the same state.
        return { decided: true };
      }
      endRole();
      state.refusedRoles.set(id, role);
      deps.log.warn("envoy: role re-assertion refused; held by another live session", {
        role,
        sessionID: id,
        holder: result.holder,
      });
      deps.notify(
        `envoy: role ${role} is now held by session ${result.holder}; this session no longer holds it`,
        "warning"
      );
      return { decided: true };
    }
    deps.log.warn("envoy: role re-asserted after the listener lost the claim", {
      role,
      sessionID: id,
      previousHolder: holding?.holder ?? null,
    });
    return { decided: true, regain: "reclaimed" };
  };

  // The one dial a queued task makes, its own: a connection opened for a task that was refused
  // while the dial was in flight is closed here rather than left dangling, and the task ends.
  // Private: a task asks for the session's connection, not for a dial.
  const dial = async (): Promise<SessionInbound> => {
    guard();
    const dialled = await deps.dial({ deadlineAt, stopped: () => !acting() });
    if (!acting()) {
      await dialled.close().catch(() => {});
      refuse(
        live()
          ? `the task acting for session ${bound} opened its ${dialled.name} for a session that is no longer the open one; the connection it opened was closed`
          : `the task that opened the ${dialled.name} was abandoned at its deadline while it was being opened; the connection it opened was closed`
      );
    }
    return dialled;
  };

  // The whole acquisition of the session's connection is the step, not the dial inside it: the
  // host waits again after a dial another caller's connection beat, closing this one and
  // answering with theirs, and a deadline or a session move during that wait would otherwise go
  // unseen — the task would open subjects and replace the stream control for a session it no
  // longer decides for. A connection this task did not open is left alone: it is the session's.
  const connect = async (): Promise<SessionInbound> => {
    guard();
    const connected = await deps.connect(dial);
    if (!acting()) {
      refuse(
        live()
          ? `the task acting for session ${bound} took its ${connected.name} for a session that is no longer the open one`
          : `the task that took the ${connected.name} was abandoned at its deadline while the connection was made`
      );
    }
    return connected;
  };

  const handle: TaskHandle = {
    actFor: (id) => {
      if (bound !== undefined) {
        throw new Error(
          `this queued task already acts for session ${bound} and cannot act for ${id}`
        );
      }
      bound = id;
    },
    check: guard,
    register: (input) => {
      guard();
      // Timed whatever becomes of this task: the heartbeat's stall test reads one clock, and a
      // registration the listener accepted moved the session's `last_seen` whoever asked for it.
      return refreshRegistration(input);
    },
    markEstablished: (id) => {
      guard();
      state.establishedSession = id;
    },
    forgetOtherAgentSessions: (host) => {
      guard();
      const closed: string[] = [];
      for (const other of state.agentSessions) {
        if (other === host) continue;
        closed.push(other);
        state.agentSessions.delete(other);
        if (state.inboundOpenFor === other) state.inboundOpenFor = "";
      }
      return closed;
    },
    markAgentSessionOpen: (id) => {
      guard();
      state.agentSessions.add(id);
    },
    markInboundOpen: (id) => {
      guard();
      state.inboundOpenFor = id;
    },
    markRoleSettled: (id) => {
      guard();
      state.roleSettledFor = id;
    },
    handBackHeldRole,
    settleEndedHolds,
    claimRole,
    reassertRole,
    noteNoticesClosed: (role) => {
      guard();
      if (state.roleNoticesFor !== role || state.roleNoticeSubjects.size > 0) return;
      state.roleNoticesFor = undefined;
      deps.notify(
        `envoy: role ${role} is held again, but the notices a plugin opened for it stay closed until that plugin claims it again on its next session switch or restart`,
        "warning"
      );
    },
    readRoleHolder: async (role) => {
      guard();
      return deps
        .client()
        .getRole(role)
        .then(
          (info) => info.holder,
          () => undefined
        );
    },
    readInterest: async (sessionID) => {
      guard();
      return deps
        .client()
        .getInterest(sessionID)
        .catch(() => undefined);
    },
    unregisterTopics: async (sessionID, topics, warnings) => {
      guard();
      await deps
        .client()
        .unsubscribe({ sessionID, topics: [...topics] })
        .catch((error: unknown) =>
          deps.log.warn(isBusUnanswered(error) ? warnings.busUnanswered : warnings.failed, {
            sessionID,
            topics,
            error: messageFor(error),
          })
        );
    },
    connect,
    subscribe: async (topic) => {
      guard();
      // `connect` is this task's own step, so a subscribe whose connection outran the task
      // opens no subject: the acquisition refuses and this call throws out of the adapter.
      const opened = await deps.subscribeTopic(topic, connect);
      guard();
      return opened;
    },
  };

  return { handle, refusal };
};

/**
 * One serial queue over `context`. `stopped`: whether each task of this queue refuses its steps
 * once the session's stop began.
 */
export const createQueue = (context: TaskContext, stopped: () => boolean): TaskQueue => {
  let tail: Promise<unknown> = Promise.resolve();
  return {
    run<T>(
      task: (handle: TaskHandle) => Promise<T>,
      onRefused?: (refusal: TaskRefusal) => void
    ): Promise<T | undefined> {
      // `tail` never rejects (it is always `next.catch(…)`), so a failed task never stops the
      // next.
      const next = tail.then(
        () =>
          new Promise<T | undefined>((resolve, reject) => {
            let abandoned = false;
            const taskDeadlineMs = context.deps.deadlineMs();
            const deadlineAt = performance.now() + taskDeadlineMs;
            const timer = setTimeout(() => {
              abandoned = true;
              reject(
                new AbandonedTaskError(
                  `a queued task did not settle within ${taskDeadlineMs}ms and was abandoned; the queue moved on`
                )
              );
            }, taskDeadlineMs);
            const running = createHandle(context, () => !abandoned, deadlineAt, stopped);
            // The task runs inside this promise so a synchronous throw is its rejection too.
            void (async () => task(running.handle))().then(
              (value) => {
                // Clear first: a healthy task must not leave a pending timer holding the process
                // open for a whole deadline.
                clearTimeout(timer);
                if (!abandoned) resolve(value);
              },
              (error: unknown) => {
                clearTimeout(timer);
                // An abandoned task's settlement is swallowed: the caller's promise rejected at
                // the deadline, and an unhandled rejection is fatal to the host.
                if (abandoned) return;
                // The task's session moved, or the stop began under a role move: it decided
                // nothing, and the pass or move the move queued decides, or nothing does after
                // the stop. That is not a failure to report, but the caller is told what the task
                // left behind, which is not the same as nothing in every case.
                if (error instanceof StepRefused) {
                  onRefused?.(running.refusal);
                  resolve(undefined);
                } else reject(error);
              }
            );
          })
      );
      tail = next.catch(() => undefined);
      return next;
    },
    settled: (): Promise<unknown> => tail,
  };
};
