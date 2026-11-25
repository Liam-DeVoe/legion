import { badgeClassName } from "../../components/Badge";

/** The row a session is listed in (subscribed agents, an ask's followers): the session's badge,
 *  then the control that removes it, a ghost `Button` of the compact size. */
export const sessionChipClassName = "flex max-w-full items-center gap-1 text-xs";

/** The badge that names the session, in the badge family's shape (`DESIGN.md`). */
export const sessionBadgeClassName = `${badgeClassName("neutral")} gap-1.5`;
