import type { ReactNode } from "react";

import { dangerText, successText } from "../../theme/classes";
import { judgeFigure, type Span, type Target } from "./lib/target";

/** A figure's tone by its own verdict: met green, missed red, and `plain` where it has none. */
export function tone(met: boolean | null, plain = ""): string {
  if (met === null) return plain;
  return met ? successText : dangerText;
}

/** A figure of a delivery page that its eval's target may judge (dispatch://INFRA-2916, R5): the
 *  measures panel's headline figures, counts and fold figures and a Pipeline lead-time card's.
 *  It names the eval, the statistic it shows and whether its value is one day's or the window's
 *  (`data-eval`, `data-statistic`, `data-span`), and is green where its own value meets the
 *  target, red where it misses it, and `plain` where it has no verdict: never the card's badge,
 *  which is the server's verdict of everything the target judges. `write(extra)` writes it with
 *  `extra` decimals past its usual ones, which it takes where its usual ones would print it on the
 *  target itself or past it (`lib/target.ts`'s `judgeFigure`). */
export function JudgedFigure(props: {
  evalSlug: string;
  statistic: string;
  span: Span;
  target: Target;
  value: number | null;
  write: (extra: number) => string;
  plain?: string;
  className?: string;
}): ReactNode {
  const { met, text } = judgeFigure(
    props.target,
    props.statistic,
    props.span,
    props.value,
    props.write
  );
  return (
    <span
      className={`${props.className ?? ""} ${tone(met, props.plain)}`.trim()}
      data-eval={props.evalSlug}
      data-span={props.span}
      data-statistic={props.statistic}
    >
      {text}
    </span>
  );
}
