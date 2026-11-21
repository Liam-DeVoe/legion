import { useQuery } from "@tanstack/react-query";

import { whoAmIQuery } from "../api/queries";
import type { EVENT_FRESH } from "../features/refs/reference-target";
/** The freshness of a read that events keep fresh (`EVENT_FRESH`'s two options). */
export type EventFreshness = Partial<typeof EVENT_FRESH>;

/** Whether the signed-in principal is a person (`/auth/whoami`'s `kind: "user"`), whose sign-in
 *  holds the `person` scope that the routes behind these surfaces require: false while the
 *  principal is still loading and for every other principal, so a surface waits for the sign-in
 *  read before it asks and nothing without the scope cycles on a 403. */
export function useIsHuman(options?: EventFreshness): boolean {
  return useQuery({ ...whoAmIQuery(), ...options }).data?.kind === "user";
}
