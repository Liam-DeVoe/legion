import { expect, test } from "bun:test";
import { childThreadsEnvironment } from "./child-threads";

test("limits every CPU-sized child pool to one thread", () => {
  expect(childThreadsEnvironment).toEqual({
    TOKIO_WORKER_THREADS: "1",
    RAYON_NUM_THREADS: "1",
  });
});
