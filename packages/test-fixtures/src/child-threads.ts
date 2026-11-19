/**
 * What every child process a test starts carries in its environment beside its own: one thread for
 * each pool that otherwise starts one thread per CPU. Oh My Pi's natives run a Tokio runtime and a
 * rayon global pool, and `jj` a rayon pool; each sizes itself by the CPUs unless
 * `TOKIO_WORKER_THREADS` or `RAYON_NUM_THREADS` names a number. On a 288-CPU machine one omp held
 * 1,177 threads with neither set and about 316 with Tokio's alone, so a test run under a limit of
 * 512 tasks failed when omp, or a `jj` beside it, started its pool: "Failed to start HTTP Client
 * thread: EAGAIN", or a panic "The global thread pool has not been initialized … Resource
 * temporarily unavailable". With both set, an omp holds 25 to 34 threads.
 *
 * A test spreads it into the environment of each child it starts: the harness's omp
 * (`@legion/pi-shared/test/omp-harness`), `versionControlEnvironment`'s jj and git, and any other
 * child that loads such a pool. No test names the variables itself.
 */
export const childThreadsEnvironment = {
  TOKIO_WORKER_THREADS: "1",
  RAYON_NUM_THREADS: "1",
} as const;
