/**
 * The number of Tokio worker threads a process a test starts gives `@oh-my-pi/pi-natives`, where
 * the caller states none: one. The processes these tests start run a handful of native calls, which
 * one worker serves.
 */
export const nativesWorkerThreads = "1";

/**
 * The environment for a process a test starts that loads `@oh-my-pi/pi-natives`: a Bun process
 * whose modules import `@oh-my-pi/pi-utils` or `@oh-my-pi/pi-tui` (every extension of pi-envoy,
 * pi-legion and pi-shared does), or the `omp` binary, which embeds the addon. It is `environment`
 * with `TOKIO_WORKER_THREADS` set to {@link nativesWorkerThreads}, unless `environment` already
 * holds a value for it, which it keeps.
 *
 * The addon's Tokio runtime starts a worker for each CPU the machine has unless
 * `TOKIO_WORKER_THREADS` says how many. On a shared Linux machine of 288 CPUs, on 2026-11-19, a
 * Bun process that imported `@oh-my-pi/pi-utils` held 288 `tokio-rt-worker` threads (1 with the
 * variable set to 1, none without the import), and so did `omp config get`. Under a limit of 512
 * tasks, a second such process could not start its threads: the row that loads pi-envoy's entry
 * in a child, beside its own test process, saw the child exit 1 having printed nothing.
 *
 * `environment` is not changed; the returned object is a new one.
 */
export function nativesEnvironment(
  environment: Record<string, string | undefined>
): Record<string, string | undefined> {
  return {
    ...environment,
    TOKIO_WORKER_THREADS: environment["TOKIO_WORKER_THREADS"] ?? nativesWorkerThreads,
  };
}
