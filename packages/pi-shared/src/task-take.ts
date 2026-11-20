import type { LegionTake, LegionTask } from "@legion/contracts/legion-api";
import type { RelayTaskValue } from "@legion/envoy-client/consumer-delivery";
import { messageFor } from "@legion/envoy-client/errors";
import { type ClaimCaller, type LegionDaemonClient, sendUntilAnswered } from "./daemon-client";
import { takeTaskSignals, taskTurnMark } from "./task-values";

/**
 * A worker's take of its claim's task (dispatch://INFRA-2811 R2). The daemon writes the number of
 * the task its record owes a claim into the claim's key of the Legion task bucket, and the Envoy
 * listener relays each write to the session holding the claim's role. The worker holds the newest
 * value it was handed — by the registration's answer, by a read of `claims/task`, or by a relayed
 * task frame — reports each one to the daemon as seen, whether or not its host is idle, and asks
 * the daemon to take it once its host is idle. The daemon answers from its record: the text to
 * run, a hold that refuses it, or a drop. A new revision is the only signal to ask again: the
 * worker asks once per `(epoch, revision)`, and the pairs it asked at are kept in this process's
 * memory alone, so a relaunched process asks again at the value it is handed, which is the
 * daemon's to answer.
 *
 * The host is idle when no run of its session is going, from a run's `agent_start` to its
 * `agent_end`: a compaction between runs does not make it busy, so a take is made during one, and
 * its text, sent while the compaction runs, waits for it (`run`).
 *
 * From the moment it holds a value it will ask about until the task's turn starts, or the take is
 * refused or dropped, the take holds the task-turn mark (`setPending`), which keeps a card that
 * finds the session idle from starting the turn the take goes into
 * (`packages/pi-envoy/src/card-wake.ts`). It holds the mark as well while its host awaits a read
 * that can bring it a value it does not hold yet (`awaitRead`): the read after the claim's ready,
 * whose decision can write one, so no card starts a turn ahead of that task. The first run that
 * starts after an accepted take's text was sent is that task's turn, as the daemon counts it, and
 * its end is reported naming the task's number (`runEnded`); for a worker on the current contract
 * that report alone finishes the task, so from the end of a turn whose report carries no provider
 * error the take holds the number finished, and a newer revision of it is owed nothing. The
 * report's answer settles that run-end mark: a 2xx finished the number in the record, which
 * deletes its value, so the mark may stay; a 4xx refusal applied nothing and the record still
 * owes the number, so the mark is cleared (`reportRefused`) and a newer revision of it is taken
 * and runs once, as R2's "a number the record owes it". The mark is kept apart from the
 * `finished` a reading says, so its clearing never lowers what a reading said. A value naming the
 * marked number is still reported seen, as every value is; only its take waits.
 *
 * Nothing sends a task into a busy worker (R5). A take the daemon accepts while a run is going —
 * the run started after the daemon decided the take and before its answer came back, or while a
 * take sent again after a lost answer waited — has its text held until that run's `agent_end`,
 * and sent then, so it starts a run of its own. The daemon gave the run that was going to the
 * take, as the first to start after its accept; that run's end is reported naming no task, which
 * puts the take back to awaiting its turn on the daemon (the plan's decisions 8 and 9), and the
 * run the held text starts is then the task's turn on both sides, its end the report that names
 * the number. The text is never sent as a follow-up into the running run: Oh My Pi folds a
 * follow-up sent during a run into that run (its agent loop reads the follow-up queue once the
 * run's last step has stopped, `getFollowUpMessages`, and goes on with no `agent_end` or
 * `agent_start` between), so the task would run inside a run whose end names no task.
 *
 * Nothing tells a busy worker that its task waits but the host's note (dispatch://INFRA-4130 R1).
 * At each tool result of the run the host asks `noteFor`, which gives the number of an owed value
 * the worker did not take — one not asked about, a take in flight, or a take the daemon refused at
 * this revision, whatever its hold — once for each task number in this process, and the host adds
 * `taskWaitingNote` (`./task-notes`) to that tool result, which the model reads at its next step.
 * The note carries no task and starts no turn: the take still goes at the run's end.
 * `heldTakeNote` (`./task-notes`) is R2's note, for a take of another claim of the issue that the
 * daemon holds behind this claim's turn, which the host learns of from the `holds` of a
 * `claims/task` read.
 *
 * The take is pending (`pending`, R6) while it holds an owed value it has not asked about at this
 * revision, while a take is in flight, while an accepted take's turn has not started, its text
 * held included, and while a take of the held revision stands refused `in-turn` during the run
 * going now: the daemon writes the value again at that run's end, so the take asks again then. That
 * last state ends at the run's end or at a newer revision, whichever comes first, and a refusal by
 * any other hold is not pending, since the daemon writes the value again only once its hold ends.
 * A held value naming the task whose turn is running is pending neither as one not asked about
 * nor as one refused `in-turn`, and `noteFor` gives it no note either: nothing is taken for it,
 * since that run's end holds the number finished.
 * The host's reminder starts no turn while the take is pending: the task is the next turn. A stall
 * abort's restart message that finds the take pending waits for it (`deferUntilSettled`), and is
 * sent if the take is refused or dropped, since nothing else then starts the worker's work again.
 *
 * A seen report or a take is sent through `sendUntilAnswered` (`./daemon-client`): one the daemon
 * gave no answer to — a refused, reset or closed connection, an unreachable host or network, a
 * connect or read timeout, a DNS failure, no answer within `DAEMON_ANSWER_TIMEOUT_MS`, or a 502,
 * 503 or 504 that names no Legion error code — is sent again, the same request, after a doubling
 * wait with no attempt limit, until the daemon answers it or a newer value replaces it: a take the
 * daemon never received would otherwise be lost, since the worker asks once per revision. Such a
 * re-send is the same ask, not a second one, and the daemon answers a take repeated for the number
 * it accepted with the same text until that task finishes, whether or not its turn has started.
 * Every refusal of these routes names a code, so a coded refusal is an answer and is not sent
 * again.
 *
 * A take its host replaced (`close`) does nothing more: what it was sending stops, an answer that
 * still arrives for it is dropped, a callback held for it (`deferUntilSettled`) is dropped, and its
 * task-turn mark is cleared.
 */

/** The daemon's hold for a take refused because the claim is in a turn (`supervise.HoldInTurn`,
 * `packages/daemon/internal/supervise/take.go`): it writes the value again at that turn's end. */
const HOLD_IN_TURN = "in-turn";

/** A claim's task value: the task's number, and the bucket's epoch and revision of the write that
 * put it there. */
export type TaskValue = NonNullable<LegionTask["task"]>;

export interface TaskTakeLog {
  readonly info: (message: string, fields?: Record<string, unknown>) => void;
  readonly warn: (message: string, fields?: Record<string, unknown>) => void;
}

export interface TaskTakeDeps {
  readonly daemon: Pick<LegionDaemonClient, "taskSeen" | "take">;
  readonly caller: ClaimCaller;
  /** Runs an accepted take's text as the turn's user message, in the form that starts a turn on
   * an idle host and waits behind a setup or a compaction that runs. */
  readonly run: (text: string) => void;
  /** Sets the task-turn mark for the claim's session, or clears it. */
  readonly setPending: (pending: boolean) => void;
  readonly log: TaskTakeLog;
  /**
   * Resolves `true` once `ms` have passed, or `false` as soon as `signal` aborts: the wait between
   * a request's attempts, and each attempt's bound. A timer by default; a test passes its own
   * clock.
   */
  readonly wait?: (ms: number, signal: AbortSignal) => Promise<boolean>;
}

export interface TaskTake {
  /** The daemon's reading of the claim's value: the registration's answer, or a `claims/task`
   * read right after the claim's ready, at a new epoch of the relay, or at a regain of the role. */
  readonly read: (reading: LegionTask) => void;
  /** A value a task frame relayed; one for another claim is not this take's. */
  readonly relayed: (value: RelayTaskValue) => void;
  /** The claim's ready has answered: from now on a take is made once the host is idle. Before
   * it, the daemon refuses a take as not ready. */
  readonly ready: () => void;
  /**
   * The host awaits a read of the claim's value that can bring one this take does not hold yet:
   * the read after the claim's ready, whose decision can write a value (the relaunch record's
   * task) the registration did not carry (the plan's decision 15). From this call until the
   * returned function runs, which the host calls once that read has answered — its reading handed
   * to `read`, or its failure logged —, the take holds the task-turn mark whatever value it holds,
   * so a card that finds the host idle in that window, the session's own direct message included,
   * starts no turn ahead of the task's. From then on the mark follows the take's own rule again:
   * it stays while the take holds a value it will ask about, and is cleared where it holds none.
   * The returned function does nothing a second time.
   */
  readonly awaitRead: () => () => void;
  /** A run of the claim's session started (its `agent_start`). */
  readonly runStarted: () => void;
  /**
   * That run ended (its `agent_end`): the number of the task whose turn it was, which the turn's
   * report names, or undefined for a run of no task. `providerError` is whether that report
   * carries the provider's failure (`reportsProviderError` in `packages/pi-legion/src/turn-outcome.ts`).
   * The daemon finishes a current claim's task on a report naming its number with no provider
   * error, so from such a run's end the take holds that number finished, beside a reading's
   * `finished`, until the daemon refuses that report (`reportRefused`): a newer revision of the
   * same number, which the daemon may write before the report lands (a daemon start writes every
   * owed value again) and would answer with the same text while the task is unfinished, is
   * reported seen, owed nothing and runs nothing a second time. A report with a provider error
   * finishes nothing: the daemon takes the task back, and the take takes that number again at
   * the revision the daemon writes after its wait.
   */
  readonly runEnded: (providerError: boolean) => number | undefined;
  /**
   * The daemon refused with a 4xx the report of a turn of task `number` that `runEnded` held
   * finished, one whose report carried no provider error: that report applied nothing and the
   * record still owes the number, so the take holds it finished no longer, and asks about a newer
   * revision of it, one it holds already or one the daemon writes later, which then runs once. A
   * number a reading said finished stays finished, and a mark that has moved on to a later number
   * since stays. A report the daemon answered with a 2xx finished the number in the record, which
   * deletes its value, so nothing is told of it.
   */
  readonly reportRefused: (number: number) => void;
  /**
   * Whether the take is pending, so that no reminder turn starts ahead of it (R6): it holds an
   * owed value it has not asked about at this revision, a take is in flight, an accepted take's
   * turn has not started (its text held included), or a take of the held revision was refused
   * `in-turn` during the run going now, which the daemon answers by writing the value again at
   * that run's end. The last ends at the run's end or at a newer revision, whichever comes first;
   * an `in-turn` refusal that arrives while the host is idle came after that run's end, and sets
   * nothing. A held value naming the task whose turn is running is pending neither as one not
   * asked about nor as one refused `in-turn`, and `noteFor` gives it no note either: nothing is
   * taken for it, since that run's end holds the number finished (`runEnded`). False once the
   * take is closed.
   */
  readonly pending: () => boolean;
  /**
   * Holds `callback` for the pending take (a stall abort's restart message): where `pending` is
   * true, the callback is kept and the answer is true, and it is called once, at the end of the
   * first settle that finds no run going and nothing pending — the take refused by a hold, the
   * value dropped or deleted — since nothing else then starts the worker's work again. A run that
   * starts first drops it, since the worker is working again (the take's own text, or another
   * turn), and so does `close`. A second call replaces the first. A callback that throws is
   * logged as a warning. Where `pending` is false, the answer is false and nothing is kept.
   */
  readonly deferUntilSettled: (callback: () => void) => boolean;
  /**
   * The number to note at this tool result of the run (R1, `taskWaitingNote`): that of an owed
   * value held while a run is going that is neither accepted nor the task running — one not asked
   * about, a take in flight, or a take refused at this revision, whatever its hold. Each number is
   * given once in this process, and a call that gives it records it, so only the host's call at a
   * tool result may make it. Undefined in every other state, and once the take is closed.
   */
  readonly noteFor: () => number | undefined;
  /** The take is replaced by another of the same host: every request it is sending stops, an
   * answer that still arrives for one changes nothing, a callback held for it is dropped, its
   * task-turn mark is cleared, and every call after this one does nothing. */
  readonly close: () => void;
}

/** One request being sent until it is answered: the pair it names, and what replaces it. */
interface Sending {
  readonly key: string;
  readonly stop: AbortController;
}

const keyOf = (value: { readonly epoch: string; readonly revision: number }): string =>
  `${value.epoch} ${value.revision}`;

export function createTaskTake(deps: TaskTakeDeps): TaskTake {
  const { caller, log } = deps;
  // The newest value this process was handed; the number of the last task the daemon's readings
  // said the claim finished; and the run-end mark, the number of the last task whose turn ended
  // here with no provider error, which that turn's report finishes on the daemon, kept apart from
  // the readings' number so that a refused report clears the mark alone (`reportRefused`). A value
  // naming a number at or below either is owed nothing.
  let held: TaskValue | undefined;
  let finished = 0;
  let finishing = 0;
  // The pairs this process reported seen and asked at, in its memory alone.
  const seen = new Set<string>();
  const asked = new Set<string>();
  let armed = false;
  let busy = false;
  // The number of the take the daemon accepted whose turn has not started, and of the task whose
  // turn is running; and the text of an accepted take held until the run going when it was
  // accepted has ended.
  let accepted: number | undefined;
  let running: number | undefined;
  let heldText: string | undefined;
  // The pair whose take the daemon refused `in-turn` during the run going now, which it writes
  // again at that run's end; and the task numbers this process has noted to its host (`noteFor`).
  let refusedInTurn: string | undefined;
  const noted = new Set<number>();
  let seeing: Sending | undefined;
  let taking: Sending | undefined;
  let marked = false;
  // The callback held for the pending take (`deferUntilSettled`).
  let deferred: (() => void) | undefined;
  // The reads the host awaits (`awaitRead`) that have not answered yet.
  let reading = 0;
  let closed = false;

  const owed = (value: TaskValue | undefined): value is TaskValue =>
    value !== undefined && value.number > Math.max(finished, finishing);

  const request = (value: TaskValue) => ({
    ...caller,
    number: value.number,
    epoch: value.epoch,
    revision: value.revision,
  });

  const reportSeen = (value: TaskValue): void => {
    const key = keyOf(value);
    if (seen.has(key)) return;
    seen.add(key);
    const sending: Sending = { key, stop: new AbortController() };
    seeing = sending;
    sendUntilAnswered(
      "claims/task-seen",
      (signal) => deps.daemon.taskSeen(request(value), { signal }),
      { stop: sending.stop.signal, warn: log.warn, wait: deps.wait }
    ).then(
      () => {
        if (seeing === sending) seeing = undefined;
      },
      (error: unknown) => {
        if (seeing !== sending) return;
        seeing = undefined;
        log.warn("legion: the daemon refused a task value's seen report", {
          task: value.number,
          revision: value.revision,
          error: messageFor(error),
        });
      }
    );
  };

  const answered = (value: TaskValue, answer: LegionTake): void => {
    log.info("legion: the daemon answered a take", {
      task: value.number,
      revision: value.revision,
      outcome: answer.outcome,
      ...(answer.outcome === "refused" ? { hold: answer.hold } : {}),
    });
    switch (answer.outcome) {
      case "accepted":
        // The daemon answers a take of the number it accepted with the same text until that task
        // finishes, whether or not its turn has started: a take sent again at a newer revision
        // of the same number, before the task's turn, during it, or after its end was reported
        // with no provider error (`runEnded`) and until that report is refused, runs nothing more.
        if (!owed(value) || accepted === value.number || running === value.number) return;
        accepted = value.number;
        // Nothing goes into a busy worker: a text accepted while a run is going waits for that
        // run's end and starts a run of its own then (`runEnded`), and that run is the task's
        // turn. Sent now, it goes to an idle host (`run`).
        if (busy) heldText = answer.text;
        else deps.run(answer.text);
        return;
      case "dropped":
        if (held?.number === value.number) held = undefined;
        return;
      case "refused":
        // A take the run going now overtook: the daemon writes the value again at that run's
        // end, so the take is pending until then (`pending`). One the host meets idle came after
        // that run's end, which the daemon's write follows, so it sets nothing.
        if (answer.hold === HOLD_IN_TURN && busy) refusedInTurn = keyOf(value);
        return;
    }
  };

  const ask = (value: TaskValue, key: string): void => {
    const sending: Sending = { key, stop: new AbortController() };
    taking = sending;
    sendUntilAnswered("claims/take", (signal) => deps.daemon.take(request(value), { signal }), {
      stop: sending.stop.signal,
      warn: log.warn,
      wait: deps.wait,
    }).then(
      (answer) => {
        if (taking !== sending) return;
        taking = undefined;
        answered(value, answer);
        settle();
      },
      (error: unknown) => {
        if (taking !== sending) return;
        taking = undefined;
        log.warn(
          "legion: the daemon refused a take; it is asked again at the value's next revision",
          {
            task: value.number,
            revision: value.revision,
            error: messageFor(error),
          }
        );
        settle();
      }
    );
  };

  /** The take holds an owed value it has not asked about at this revision, a take is in flight, or
   * an accepted take's turn has not started: the task's turn is still to come. */
  const turnToCome = (): boolean =>
    (owed(held) && !asked.has(keyOf(held))) || taking !== undefined || accepted !== undefined;

  const pending = (): boolean => {
    if (closed) return false;
    if (taking !== undefined || accepted !== undefined) return true;
    // A value naming the task whose turn is running is owed no take: that run's end holds the
    // number finished, so nothing is taken for it, and `noteFor` gives it no note either.
    if (!owed(held) || held.number === running) return false;
    // A refusal `in-turn` holds only the revision it answered: a newer one ends it.
    const key = keyOf(held);
    return !asked.has(key) || key === refusedInTurn;
  };

  /** Asks about the held value where it may, keeps the task-turn mark to what is pending, and
   * calls the callback held for the take (`deferUntilSettled`) once nothing is pending while no
   * run is going. */
  const settle = (): void => {
    const key = held === undefined ? undefined : keyOf(held);
    if (armed && !busy && owed(held) && key !== undefined && !asked.has(key)) {
      asked.add(key);
      ask(held, key);
    }
    const mark = reading > 0 || turnToCome();
    if (mark !== marked) {
      marked = mark;
      deps.setPending(mark);
    }
    if (deferred === undefined || busy || pending()) return;
    const callback = deferred;
    deferred = undefined;
    // A settle runs inside the host's own event handlers and the daemon's answers: a callback
    // that throws is logged, and changes neither the take's state nor its caller's answer.
    try {
      callback();
    } catch (error) {
      log.warn("legion: what waited for the take to settle failed", { error: messageFor(error) });
    }
  };

  /** A request for a pair other than `key`'s is replaced: it is sent no more. */
  const replace = (key: string | undefined): void => {
    if (seeing !== undefined && seeing.key !== key) {
      seeing.stop.abort();
      seeing = undefined;
    }
    if (taking !== undefined && taking.key !== key) {
      taking.stop.abort();
      taking = undefined;
    }
  };

  const hold = (value: TaskValue): void => {
    if (held !== undefined && held.epoch === value.epoch && value.revision <= held.revision) {
      return;
    }
    held = value;
    replace(keyOf(value));
    reportSeen(value);
  };

  return {
    read: (reading) => {
      if (closed) return;
      finished = Math.max(finished, reading.finished);
      if (reading.task !== null) hold(reading.task);
      settle();
    },
    relayed: (value) => {
      if (closed || value.claim !== caller.claimToken) return;
      const at = { epoch: value.epoch, revision: value.revision };
      if (value.op === "put") {
        hold({ number: value.task, ...at });
      } else if (
        held !== undefined &&
        (held.epoch !== value.epoch || held.revision < value.revision)
      ) {
        // The claim has no current task from this revision on: the record dropped or finished it.
        held = undefined;
        replace(undefined);
      }
      settle();
    },
    ready: () => {
      if (closed) return;
      armed = true;
      settle();
    },
    awaitRead: () => {
      if (closed) return () => undefined;
      reading += 1;
      settle();
      let answered = false;
      return () => {
        if (answered) return;
        answered = true;
        reading -= 1;
        if (!closed) settle();
      };
    },
    runStarted: () => {
      if (closed) return;
      busy = true;
      // The worker is working again: a callback held for the take is not called.
      deferred = undefined;
      // The first run that starts after an accepted take's text was sent is the task's turn. A
      // text still held waits for the end of a run that started before it was accepted, so a run
      // that starts before that end is not its turn.
      if (accepted !== undefined && heldText === undefined) {
        running = accepted;
        accepted = undefined;
      }
      settle();
    },
    runEnded: (providerError) => {
      if (closed) return undefined;
      busy = false;
      refusedInTurn = undefined;
      const ended = running;
      running = undefined;
      // That run's report names the task with no provider error, which finishes it on the
      // daemon: the number is owed nothing from here on, before the report has even landed, and
      // until the daemon refuses it (`reportRefused`).
      if (ended !== undefined && !providerError) finishing = Math.max(finishing, ended);
      // A text accepted during the run that just ended: that run was not the task's turn, so its
      // end names no task (`ended` is undefined), and the text starts the next run, the task's.
      if (heldText !== undefined) {
        const text = heldText;
        heldText = undefined;
        deps.run(text);
      }
      settle();
      return ended;
    },
    reportRefused: (number) => {
      if (closed || finishing !== number) return;
      finishing = 0;
      settle();
    },
    pending,
    deferUntilSettled: (callback) => {
      if (!pending()) return false;
      deferred = callback;
      return true;
    },
    noteFor: () => {
      if (closed || !busy || !owed(held)) return undefined;
      const { number } = held;
      if (number === accepted || number === running || noted.has(number)) return undefined;
      noted.add(number);
      return number;
    },
    close: () => {
      if (closed) return;
      closed = true;
      deferred = undefined;
      replace(undefined);
      if (!marked) return;
      marked = false;
      deps.setPending(false);
    },
  };
}

/** A claim's take as a host runs it (`takeClaimTask`): the take, and its read of the claim's value
 * from the daemon. */
export interface ClaimTaskTake {
  readonly take: TaskTake;
  /** Reads the claim's value from the daemon (`claims/task`) and hands it to the take. A read that
   * fails is logged and asks nothing again, since the daemon's next write of the value reaches
   * the session as well. Never rejects. */
  readonly read: () => Promise<void>;
}

/**
 * The take of the claim `caller` names, as an Oh My Pi host runs it, the one way pi-legion's claim
 * session and its daemon-launched controller build it: the take (`createTaskTake`), whose
 * task-turn mark names the caller's session (`taskTurnMark`); its read of the claim's value; and
 * the one taker of the task signals the Envoy entry hands over (`takeTaskSignals`), which hands
 * the take every value relayed for that session and reads the value again at each new epoch of
 * the session's relay. A later call replaces this one's taker: a host that replaces its take
 * closes the one it replaced (`TaskTake.close`).
 */
export function takeClaimTask(deps: {
  readonly daemon: Pick<LegionDaemonClient, "task" | "taskSeen" | "take">;
  readonly caller: ClaimCaller;
  readonly run: (text: string) => void;
  readonly log: TaskTakeLog;
}): ClaimTaskTake {
  const { daemon, caller, log } = deps;
  const take = createTaskTake({
    daemon,
    caller,
    run: deps.run,
    setPending: taskTurnMark(caller.sessionId),
    log,
  });
  const read = async (): Promise<void> => {
    try {
      take.read(await daemon.task(caller));
    } catch (error) {
      log.warn(
        "legion: a claims/task read failed; the claim's value reaches this session with the daemon's next write of it",
        { error: messageFor(error) }
      );
    }
  };
  takeTaskSignals((signal) => {
    if (signal.session !== caller.sessionId) return;
    if (signal.kind === "value") take.relayed(signal.value);
    else void read();
  });
  return { take, read };
}
