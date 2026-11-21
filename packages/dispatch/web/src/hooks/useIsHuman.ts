import { useQuery } from "@tanstack/react-query";

import { whoAmIQuery } from "../api/queries";
/** Options a surface gives the sign-in read over its own, such as how long its answer is fresh. */
export type WhoAmIReadOptions = Partial<ReturnType<typeof whoAmIQuery>>;

/** Whether the signed-in principal is a person (`/auth/whoami`'s `kind: "user"`), whose sign-in
 *  holds the `person` scope that the routes behind these surfaces require: false while the
 *  principal is still loading and for every other principal, so a surface waits for the sign-in
 *  read before it asks and nothing without the scope cycles on a 403. */
export function useIsHuman(options?: WhoAmIReadOptions): boolean {
  return useQuery({ ...whoAmIQuery(), ...options }).data?.kind === "user";
}
