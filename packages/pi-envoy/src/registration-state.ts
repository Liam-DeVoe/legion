import { ROLE_TOPIC_PREFIX } from "@legion/contracts";
import {
  type EnvoyClient,
  expandSubscriptionTopics,
  newClientInstanceID,
  type SubscribeInput,
  type SubscribeResult,
} from "@legion/envoy-client/transport";
import { createHandBackLedger } from "./hand-back-ledger";
import type { SessionInbound } from "./session-inbound";
import {
  createQueue,
  type RegistrationRecord,
  type TaskContext,
  type TaskQueue,
} from "./task-queue";

/**
 * The registration and role state the Envoy extension's two serial queues write
 * (`./task-queue`), the queues themselves, and this extension instance's client instance id.
 *
 * The state is private to this module: callers read it through the accessors below and write it
 * only through a named operation or a queued task's handle, so there is no shared variable a
 * later edit can write unguarded. The holds this process gave up and still owes the listener are
 * a module of their own, {@link createHandBackLedger}, which this one holds and nothing else
 * reaches.
 */

/** Whether a topic served the session's role, and how many roles had ended, at one moment. */
export interface RoleSnapshot {
  readonly roleBound: boolean;
  readonly endsBefore: number;
}

/** What a registration carries, less the client instance id this module mints and sends itself. */
export type RegistrationInput = Omit<SubscribeInput, "clientInstanceID">;

/** The warnings this module writes; `logger` from the host satisfies it. */
export interface RegistrationStateLog {
  readonly warn: (message: string, context?: Record<string, unknown>) => void;
}

/** What a queued task's NATS dial may spend: the task's deadline, as a `performance.now()`
 *  instant, and whether the task stopped acting (abandoned, refused, or its session moved), after
 *  which the dial starts no further attempt. */
export interface DialLimit {
  readonly deadlineAt: number;
  readonly stopped: () => boolean;
}

/**
 * Everything the module needs from its host, injected so the package's own unit tests exercise it
 * with plain fakes: no Oh My Pi host type is imported here. The connection is the session's inbound
 * connection, NATS or the listener's relay socket (`SessionInbound`): the module closes one a
 * refused task opened, and hands the host's own to a task.
 */
export interface RegistrationStateDeps {
  /** The listener client, read at each call: it throws while Envoy is off for this session, as
   *  `requireEnvoy()` does, and the queues exist whatever the session resolved. */
  readonly client: () => EnvoyClient;
  /** Whether `id` is the session open here, for this instance and the host alike. */
  readonly sessionOpen: (id: string) => boolean;
  /**
   * Whether this session's stop has begun, read at each step of a role move. The stop hands back
   * every role this process holds and deletes the session's record next, so from then on a role
   * move claims nothing, transfers nothing and tells nothing (R24 of dispatch://INFRA-3589).
   */
  readonly stopping: () => boolean;
  /** The deadline each queued task gets, read afresh when that task starts. */
  readonly deadlineMs: () => number;
  /** Tells the pane what a state write decided. */
  readonly notify: (message: string, level: "warning") => void;
  readonly log: RegistrationStateLog;
  /**
   * Writes the durable transcript record of a claim this process landed: the role, the session
   * it was claimed for, and this process's own client instance, so a later read of that record
   * tells a claim this process made from one another process wrote on the same transcript.
   */
  readonly appendRoleClaimEntry: (claim: {
    readonly role: string;
    readonly sessionID: string;
    readonly clientInstanceID: string;
  }) => void;
  /** Opens the session's inbound connection, storing nothing: this module closes one a refused
   *  task opened. A queued task's dial is given the task's deadline and whether the task was
   *  abandoned, so its redials end in time for the rest of the task; a dial outside a task gets
   *  no limit. */
  readonly dial: (limit?: DialLimit) => Promise<SessionInbound>;
  /**
   * The session's one inbound connection, made with `dial` when it has none and kept by the host.
   * The whole acquisition is the fenced step, not the dial alone: the host's own waits after the
   * dial (closing a connection another caller won the race with, and taking that caller's
   * connection instead) happen inside this call, and the handle checks the task again after it.
   */
  readonly connect: (dial: () => Promise<SessionInbound>) => Promise<SessionInbound>;
  /**
   * Opens `topic`'s subjects over `connect`'s connection; answers whether any subjects were new.
   * A queued task is handed its own fenced `connect`, so a subscribe whose connection outran the
   * task opens nothing: a dial it made is closed and the task ends before any subject opens.
   */
  readonly subscribeTopic: (
    topic: string,
    connect: () => Promise<SessionInbound>
  ) => Promise<boolean>;
  /**
   * Stops following the subjects this module no longer keeps open, the live subscriptions
   * staying with the extension, and takes them out of the listener's registry under
   * `sessionID` — the session the hold was under, empty when the module holds none, which the
   * host reads as its own open session. A role's end closes every notice subject a plugin
   * opened while it was held, and those were registered while the role stood: a notice lane
   * reaches a session from its own consumer alone, so they leave the session's consumer filter
   * only by leaving the registry. Best-effort, and never awaited here.
   */
  readonly closeSubjects: (sessionID: string, subjects: readonly string[]) => void;
}

/** The module's surface: the two queues, the named operations, and the readers of its state. */
export interface RegistrationState {
  /**
   * This extension instance's client instance id, minted here so an ACP host running several
   * top-level sessions in one process gives each its own. Sent on every registration and on the
   * shutdown deregistration, where the listener compares it.
   */
  readonly clientInstanceID: string;
  /** Reconcile passes: one at a time, each converging this process to the host's session. */
  readonly passes: TaskQueue;
  /**
   * Role moves: the model's `envoy_role_set`, a plugin's claim, and each pass's own settle. Each
   * step of one refuses once the session's stop has begun (`RegistrationStateDeps.stopping`); a
   * pass reads the stop itself.
   */
  readonly roleMoves: TaskQueue;

  // — named operations, made on purpose without a live task —
  /**
   * Registers the session at the listener, outside any queue. Every hold this process abandoned
   * under that session becomes a pending hand-back under the same key first, because the
   * registration moves the session's `last_seen` at the listener even when its answer is lost. On
   * the answer, that session is the one registered and the registration clock is now.
   */
  readonly refreshRegistration: (input: RegistrationInput) => Promise<SubscribeResult>;
  /** Whether a registration this process made for `sessionID` was accepted less than `ms` ago. A
   *  registration accepted for another session, or none at all, reads as older than any age. */
  readonly registrationFresherThan: (sessionID: string, ms: number) => boolean;
  /** Deregisters the session at the listener, carrying this instance's client instance id so a
   *  successor instance's registration under the same id is left alone; within `timeoutMs`,
   *  where the caller has only that long (a shutdown's budget). */
  readonly deregister: (sessionID: string, timeoutMs?: number) => Promise<void>;
  /**
   * Hands back at the listener every role hold this process still has there, for a session that
   * is stopping: the role it holds, under the session id the listener names for it, and each hold
   * it gave up and has not handed back yet (`HandBackLedger.handBackAll`). Each release carries
   * this instance's client instance id, so the listener frees only the claims this process made.
   * A release that fails is warned about on `log`, the stop's own writer, and not on the module's
   * `RegistrationStateDeps.log`, which the host's tell gate holds once the stop has begun. It
   * runs outside the queues and changes no state: nothing runs after a stop. A role move still
   * running refuses its next step (`RegistrationStateDeps.stopping`), so this does not wait for
   * one.
   */
  readonly handBackAllHolds: (log: RegistrationStateLog) => Promise<void>;
  /** Forgets the role the listener refused to this session, so a pass may claim it again. */
  readonly clearRefusal: (sessionID: string) => void;
  /**
   * Ends, as a session event lands, the role this process holds when the host session's own
   * records rule it out: a recorded release, another role, or no record while the hold is not
   * under `host` or a session `host` continues (`lineage`). The hold becomes a pending hand-back
   * under the id it was held as.
   */
  readonly endUnwantedRole: (
    host: string,
    recordedRole: string | null | undefined,
    lineage: readonly string[]
  ) => void;
  /** Marks subjects as taken only while this session holds `role`; answers the ones newly marked.
   */
  readonly markRoleBoundSubjects: (subjects: readonly string[], role: string) => readonly string[];
  /** Unmarks subjects whose subscription never opened. */
  readonly unmarkRoleBoundSubjects: (subjects: readonly string[]) => void;
  /** Records a claim the listener granted after its task was abandoned: the hold is real at the
   *  listener, so the next pass hands it back. */
  readonly recordClaimAfterDeadline: (topic: string, sessionID: string) => void;
  /**
   * Records a claim the listener granted after its task's session moved: the new hold is this
   * process's own under the id the listener names, and an earlier role ends and is handed back —
   * a hand-back the listener refused is abandoned, so the pass behind this process's next
   * registration of that hold's session sends it again.
   */
  readonly adoptClaimAfterSessionMoved: (
    topic: string,
    sessionID: string,
    previousTopic: string | undefined,
    previousHoldID: string | undefined
  ) => Promise<void>;

  // — readers —
  /** The role this session holds, if any. */
  readonly heldRole: () => string | undefined;
  /** Whether `topic` is the role topic this session holds. */
  readonly holdsRoleTopic: (topic: string) => boolean;
  /** The session id the listener names for the role this session holds. */
  readonly holdSessionID: () => string | undefined;
  /** Whether `subject` is taken only while this session holds its role. */
  readonly isRoleBound: (subject: string) => boolean;
  /** Every role-bound subject, in the order they were marked. */
  readonly roleBoundSubjects: () => readonly string[];
  /** Whether `topic` serves the role this session holds, and how many roles had ended, at once. */
  readonly roleSnapshot: (topic: string) => RoleSnapshot;
  /** The role the listener refused to this session's automatic claim, if any. */
  readonly refusalFor: (sessionID: string) => string | undefined;
  /** How many roles have ended, which a role-bound subscribe compares against. */
  readonly roleEndCount: () => number;
  /** Whether the hold came back from a hand-back rather than from a claim the listener granted. */
  readonly holdIsUnconfirmed: () => boolean;
  /** The session the last pass converged to in full. */
  readonly establishedFor: () => string;
  /** The session whose agent subject, stream watch and registered interests a pass opened. */
  readonly inboundOpenSession: () => string;
  /** The session whose role the last pass decided. */
  readonly roleSettledSession: () => string;
}

export function createRegistrationState(deps: RegistrationStateDeps): RegistrationState {
  const clientInstanceID = newClientInstanceID();

  // The role hold, the refusals and notice subjects of the role, and the sessions a pass
  // converged, which a queued task's handle writes too (`RegistrationRecord`).
  const state: RegistrationRecord = {
    claimedRoleTopic: undefined,
    claimedRoleSessionID: undefined,
    holdUnconfirmed: false,
    refusedRoles: new Map<string, string>(),
    roleNoticesFor: undefined,
    roleNoticeSubjects: new Set<string>(),
    establishedSession: "",
    roleSettledFor: "",
    agentSessions: new Set<string>(),
    inboundOpenFor: "",
  };
  // The holds this process gave up and still owes the listener: both books, their keys, the
  // release itself and the reading of a refused claim against an abandoned hold.
  const holds = createHandBackLedger({
    client: deps.client,
    clientInstanceID,
    warn: (message, context) => deps.log.warn(message, context),
  });
  // Counts role ends, so a role-bound subscription still opening when its role ended can tell.
  let roleEnds = 0;
  // When the listener last accepted a registration, and the session it was accepted for. A
  // registration accepted for an earlier session says nothing about the one running now, so it
  // reads as no registration at all; a session that never had one reads as older than any age.
  let registeredFor = "";
  let registeredAt = -Infinity;

  const heldRole = (): string | undefined =>
    state.claimedRoleTopic?.slice(ROLE_TOPIC_PREFIX.length);
  const isRoleBound = (subject: string): boolean => state.roleNoticeSubjects.has(subject);

  // The session stops holding its role: close the notice subjects it took for it, so a holder
  // another session replaced stops taking the role's wakes, and take them out of the listener's
  // registry with them.
  const endRole = (): void => {
    const held = state.claimedRoleSessionID ?? "";
    const closed = [...state.roleNoticeSubjects];
    state.claimedRoleTopic = undefined;
    state.claimedRoleSessionID = undefined;
    roleEnds += 1;
    state.roleNoticeSubjects.clear();
    if (closed.length > 0) deps.closeSubjects(held, closed);
  };

  const refreshRegistration = (input: RegistrationInput): Promise<SubscribeResult> => {
    // Before the call, because the registration moves the session's `last_seen` at the listener
    // even when its answer is lost: a hold of that session this process abandoned is its own
    // again, and the pass behind this registration sends the release.
    holds.adoptFor(input.sessionID);
    return deps
      .client()
      .subscribe({ ...input, clientInstanceID })
      .then((result) => {
        registeredFor = input.sessionID;
        registeredAt = Date.now();
        return result;
      });
  };

  /**
   * Takes the hold of `topic` under `sessionID`: the role this session held, when the hold moves
   * to another role, ends first and its own hold is handed back at the listener under the id it
   * was held as (a fork's parent, until a pass carried it). `recordClaim` runs between the two,
   * where the claim's durable transcript record belongs: the hold is this process's by then, and
   * the hand-back that follows can fail or outrun the task. A hand-back the listener refused is
   * abandoned rather than thrown: the claim this move carries landed, and the role it superseded
   * is sent again by the pass behind this process's next registration of that hold's session.
   */
  const moveHold = async (
    topic: string,
    sessionID: string,
    previousTopic: string | undefined,
    previousHoldID: string | undefined,
    recordClaim?: () => void
  ): Promise<void> => {
    const movedOff = previousTopic === topic ? undefined : previousTopic;
    if (movedOff !== undefined) endRole();
    state.claimedRoleTopic = topic;
    state.claimedRoleSessionID = sessionID;
    recordClaim?.();
    if (movedOff !== undefined && previousHoldID) {
      await holds.handBackOrAbandon(movedOff, previousHoldID, {
        topic: movedOff,
        sessionID: previousHoldID,
      });
    }
  };

  // What a queued task's steps act on (`./task-queue`).
  const tasks: TaskContext = {
    deps,
    clientInstanceID,
    holds,
    state,
    heldRole,
    endRole,
    moveHold,
    refreshRegistration,
  };

  return {
    clientInstanceID,
    // A pass reads the stop itself, once its registration settled (the extension's
    // `reconcileNow`); a role move refuses each step once the stop began.
    passes: createQueue(tasks, () => false),
    roleMoves: createQueue(tasks, deps.stopping),

    refreshRegistration,
    registrationFresherThan: (sessionID, ms) =>
      registeredFor === sessionID && Date.now() - registeredAt < ms,
    deregister: async (sessionID, timeoutMs) => {
      await deps.client().unregisterSession({
        sessionID,
        clientInstanceID,
        ...(timeoutMs === undefined ? {} : { timeoutMs }),
      });
    },
    handBackAllHolds: (log) =>
      holds.handBackAll(
        state.claimedRoleTopic === undefined || !state.claimedRoleSessionID
          ? undefined
          : { topic: state.claimedRoleTopic, id: state.claimedRoleSessionID },
        (message, context) => log.warn(message, context)
      ),
    clearRefusal: (sessionID) => {
      state.refusedRoles.delete(sessionID);
    },
    endUnwantedRole: (host, recordedRole, lineage) => {
      const held = heldRole();
      const topic = state.claimedRoleTopic;
      const holdID = state.claimedRoleSessionID;
      if (held === undefined || topic === undefined || holdID === undefined) return;
      const carried = holdID === host || lineage.includes(holdID);
      const wanted = recordedRole === undefined ? carried : recordedRole === held;
      if (wanted) return;
      endRole();
      if (holdID !== "") holds.recordEndedHold(topic, holdID);
    },
    markRoleBoundSubjects: (subjects, role) => {
      const marked: string[] = [];
      for (const subject of subjects) {
        if (state.roleNoticeSubjects.has(subject)) continue;
        state.roleNoticeSubjects.add(subject);
        marked.push(subject);
      }
      state.roleNoticesFor = role;
      return marked;
    },
    unmarkRoleBoundSubjects: (subjects) => {
      for (const subject of subjects) state.roleNoticeSubjects.delete(subject);
    },
    recordClaimAfterDeadline: holds.recordEndedHold,
    adoptClaimAfterSessionMoved: moveHold,

    heldRole,
    holdsRoleTopic: (topic) => state.claimedRoleTopic === topic,
    holdSessionID: () => state.claimedRoleSessionID,
    isRoleBound,
    roleBoundSubjects: () => [...state.roleNoticeSubjects],
    roleSnapshot: (topic) => ({
      roleBound: expandSubscriptionTopics([topic]).some(isRoleBound),
      endsBefore: roleEnds,
    }),
    refusalFor: (sessionID) => state.refusedRoles.get(sessionID),
    roleEndCount: () => roleEnds,
    holdIsUnconfirmed: () => state.holdUnconfirmed,
    establishedFor: () => state.establishedSession,
    inboundOpenSession: () => state.inboundOpenFor,
    roleSettledSession: () => state.roleSettledFor,
  };
}
