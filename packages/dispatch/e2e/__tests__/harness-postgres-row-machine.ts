/** The machine of the rows of `harness-postgres-removal.test.ts` that run a start's removal of a
 *  container on a clock the row moves (`rowMachine`), so that the start's own bounds pass in a
 *  moment of real time: its `docker`, which answers each call as the row says (`RowAnswer`), and
 *  each call it got (`RowCall`). It starts no process. */

import type { Machine } from "../harness-postgres-container-removal";

/** One call of a row's `docker` (`rowMachine`): its arguments, when it reached the row's clock,
 *  and when and how it ended, in ms of that clock: answered, ended by its `signal`, or ended by
 *  its `timeout`, as `execFile` ends a call. */
export interface RowCall {
  readonly args: string;
  readonly at: number;
  ended?: number;
  how?: "answered" | "aborted" | "timed out";
}

/** How a row's `docker` answers one call (`rowMachine`): `after` ms of the row's clock after the
 *  call reached it, with `stdout` where it exits 0, or with `stderr` where it exits 1; or never, as
 *  Docker never ends a removal that is stuck in the daemon. */
export type RowAnswer =
  | { readonly after: number; readonly stdout?: string; readonly stderr?: string }
  | "never";

/** A machine for a start's removal of a container whose clock the row moves (`Machine`). The clock
 *  starts at 0 and moves only while the removal waits on it, to the next time at which a sleep
 *  ends, an answer comes or a call's `timeout` ends that call, so a row runs the start's own
 *  10-minute bound in a moment of real time, and every time it reads is the removal's own, which
 *  no load stretches. Its `docker` answers each call as `answer` says, and ends it where its
 *  `signal` aborts or its `timeout` passes first, as `execFile` ends one. Answers the machine,
 *  each call so far (`RowCall`), and `drive`, which moves the clock until `done`, the removal,
 *  settles, and throws where the removal waits on nothing that clock moves. */
export function rowMachine(answer: (args: readonly string[]) => RowAnswer) {
  let now = 0;
  // Each time the clock may move to, in the order it was set: the end of a sleep, an answer, or a
  // call's timeout. Two at one time come in that order.
  const due: { readonly at: number; readonly fire: () => void }[] = [];
  const after = (ms: number, fire: () => void): (() => void) => {
    const entry = { at: now + ms, fire };
    due.push(entry);
    return () => {
      const index = due.indexOf(entry);
      if (index !== -1) due.splice(index, 1);
    };
  };
  const calls: RowCall[] = [];
  const machine: Machine = {
    now: () => now,
    sleep: (ms, signal) => {
      const { promise, resolve } = Promise.withResolvers<void>();
      if (signal?.aborted) {
        resolve();
        return promise;
      }
      const cancel = after(ms, resolve);
      signal?.addEventListener(
        "abort",
        () => {
          cancel();
          resolve();
        },
        { once: true }
      );
      return promise;
    },
    docker: (args, { signal, timeout }) => {
      const { promise, resolve, reject } = Promise.withResolvers<{ stdout: string }>();
      const call: RowCall = { args: args.join(" "), at: now };
      calls.push(call);
      const cancels: (() => void)[] = [];
      const end = (how: NonNullable<RowCall["how"]>, settle: () => void): void => {
        if (call.how !== undefined) return;
        call.ended = now;
        call.how = how;
        for (const cancel of cancels) cancel();
        settle();
      };
      // The errors `execFile` throws: an abort's, and a failed command's, with its fields.
      const failed = (stderr: string, fields: object) =>
        Object.assign(new Error(`Command failed: docker ${call.args}\n${stderr}`), {
          stdout: "",
          stderr,
          ...fields,
        });
      const abort = (): void =>
        end("aborted", () =>
          reject(
            Object.assign(new Error("The operation was aborted"), {
              name: "AbortError",
              code: "ABORT_ERR",
            })
          )
        );
      if (signal.aborted) {
        abort();
        return promise;
      }
      signal.addEventListener("abort", abort, { once: true });
      cancels.push(() => signal.removeEventListener("abort", abort));
      if (timeout !== undefined && timeout > 0) {
        cancels.push(
          after(timeout, () =>
            end("timed out", () =>
              reject(failed("", { killed: true, signal: "SIGTERM", code: null }))
            )
          )
        );
      }
      const answered = answer(args);
      if (answered !== "never") {
        cancels.push(
          after(answered.after, () =>
            end("answered", () =>
              answered.stderr === undefined
                ? resolve({ stdout: answered.stdout ?? "" })
                : reject(failed(answered.stderr, { killed: false, code: 1 }))
            )
          )
        );
      }
      return promise;
    },
  };
  async function drive<T>(done: Promise<T>): Promise<T> {
    let settled = false;
    void done.then(
      () => {
        settled = true;
      },
      () => {
        settled = true;
      }
    );
    for (;;) {
      // What the last move made due runs first: each step of a removal after it is a chain of
      // promise callbacks, which all run before the event loop's next turn.
      const turn = Promise.withResolvers<void>();
      setImmediate(turn.resolve);
      await turn.promise;
      if (settled) return done;
      const next = due.reduce<(typeof due)[number] | undefined>(
        (first, entry) => (first === undefined || entry.at < first.at ? entry : first),
        undefined
      );
      if (next === undefined) {
        throw new Error(
          `the removal waits, at ${now} ms of the row's clock, on nothing that clock moves`
        );
      }
      due.splice(due.indexOf(next), 1);
      now = next.at;
      next.fire();
    }
  }
  return { machine, calls: (): readonly RowCall[] => calls, drive };
}
