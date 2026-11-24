import { act } from "@testing-library/react";

/** One rendering frame. happy-dom runs animation frames as immediates, so this is one turn of the
 *  event loop. A row waits one out before it reads what a component does in a frame of its own: a
 *  margin's hold counts its relayout window in frames, and a settle a tab strip defers to a frame
 *  has run by then. */
export async function nextFrame(): Promise<void> {
  const done = Promise.withResolvers<void>();
  requestAnimationFrame(() => done.resolve());
  await done.promise;
}

/** A frame and then two zero-delay turns of the event loop, inside `act`: past what a component
 *  defers to a frame and a task after a press or a loss, as a component's armed hand-off ends a
 *  frame and a task after the last thing that keeps it live (`focus-place.ts`'s
 *  `afterComponents`). A row waits it out where a control's hold, or the page around it, is to be
 *  read after that. */
export async function afterFrameAndTasks(): Promise<void> {
  await act(async () => {
    await nextFrame();
    for (const _ of [0, 1]) {
      const turn = Promise.withResolvers<void>();
      setTimeout(turn.resolve, 0);
      await turn.promise;
    }
  });
}
