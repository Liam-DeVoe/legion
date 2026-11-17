import { type PropsWithChildren, type ReactNode, useEffect, useMemo } from "react";

import { ChordIndicator } from "./ChordIndicator";
import { appKeymap, KeymapPersonContext } from "./keymap";
import { usePersonKeys } from "./keymap-keys";
import { useLinkCopy } from "./link-copy";

/**
 * Binds the application registry to `window` keydown, renders the chord indicator, gives every
 * registration below it the keys `login` changed (`keymap-keys.ts`), and registers the one key of
 * no page, the `y` that copies a focused link's address (`link-copy.ts`). With no `login` -
 * nobody signed in, or a unit test - every action takes its defaults.
 */
export function KeymapProvider({
  children,
  login,
}: PropsWithChildren<{ login?: string }>): ReactNode {
  const personKeys = usePersonKeys(login);
  const person = useMemo(() => ({ login, personKeys }), [login, personKeys]);
  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => appKeymap.handleKeyDown(event);
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, []);
  useLinkCopy();

  return (
    <KeymapPersonContext.Provider value={person}>
      {children}
      <ChordIndicator />
    </KeymapPersonContext.Provider>
  );
}
