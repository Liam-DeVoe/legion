import type { ReactNode } from "react";

import { dangerText, secondaryButtonCompact, textSecondaryOnSurface } from "../../theme/classes";
import { keptRecords, keptWrites } from "./shown-writes";
import { useNotTakenUp, useWriteSelection, useWrites } from "./write-store";

/**
 * What a lapsed session keeps, on the sign-in page: the person's writes by name, under the login
 * whose sign-in takes them up again (`Writes.signedIn`), so they see what waits for them; one
 * Dispatch refused offers Discard, since only they can drop it. Nothing while the page keeps none,
 * as after a sign-out. The writes the reload kept that the store has not taken up yet are listed
 * too, from their own records (`Writes.notTakenUp`), so the list needs no kind of write: until
 * the store takes one up it shows by its name alone, and from then on as the store's own row.
 */
export function KeptWrites(): ReactNode {
  const writes = useWrites();
  const rows = useWriteSelection("kept-while-signed-out", keptWrites);
  const records = keptRecords(useNotTakenUp());
  const shown = [...rows, ...records].sort((a, b) => a.pressedAt - b.pressedAt);
  if (shown.length === 0) return null;
  const logins = [...new Set(shown.map((write) => write.login))];
  return (
    <section aria-label="Kept changes" className="mt-6 space-y-3 text-sm">
      {logins.map((login) => (
        <div key={login}>
          <p className={textSecondaryOnSurface}>Kept until you sign in again as {login}:</p>
          <ul className="mt-2 space-y-1">
            {shown
              .filter((write) => write.login === login)
              .map((write) => (
                <li
                  className="flex min-h-8 flex-wrap items-center gap-x-2 gap-y-1"
                  data-kept-write={write.key}
                  key={write.key}
                >
                  {"id" in write && write.status === "refused" ? (
                    <>
                      <span className={`min-w-0 flex-1 ${dangerText}`}>
                        {write.label} — Not saved
                      </span>
                      <button
                        className={secondaryButtonCompact}
                        onClick={() => writes.discard(write.id)}
                        type="button"
                      >
                        Discard
                      </button>
                    </>
                  ) : (
                    <span className="min-w-0 flex-1">{write.label}</span>
                  )}
                </li>
              ))}
          </ul>
        </div>
      ))}
    </section>
  );
}
