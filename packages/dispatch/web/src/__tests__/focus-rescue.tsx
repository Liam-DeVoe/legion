import { QueryClientContext } from "@tanstack/react-query";
import { act } from "@testing-library/react";
import { type ReactNode, useContext, useRef } from "react";

import { useFocusRescue } from "../components/focus-place";
import { writesOf } from "../features/writes/write-store";

/** The shell's main content holding `children`, with the page's focus rescue mounted on it as
 *  `app.tsx` mounts it: handed the writes of the client around it, where there is one. */
export function RescuedMain({ children }: { children: ReactNode }): ReactNode {
  const main = useRef<HTMLElement>(null);
  const client = useContext(QueryClientContext);
  useFocusRescue(main, client === undefined ? undefined : writesOf(client));
  return <main ref={main}>{children}</main>;
}

/** Lets the page's focus rescue answer a loss: its check runs a frame and then a task after it,
 *  so a frame and two zero-delay turns of the event loop here come after that check. */
export async function afterRescue(): Promise<void> {
  await act(async () => {
    const drawn = Promise.withResolvers<void>();
    requestAnimationFrame(() => drawn.resolve());
    await drawn.promise;
    for (const _ of [0, 1]) {
      const turn = Promise.withResolvers<void>();
      setTimeout(turn.resolve, 0);
      await turn.promise;
    }
  });
}
