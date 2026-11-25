import type { ReactNode } from "react";

import { badgeClassName } from "../../components/Badge";

/** The met / missed / no-data pill a delivery card wears against its target: the measures panel's
 *  KPI cards and the Pipeline page's two lead-time cards. `met` is null when there is nothing to
 *  judge; each surface names the met and missed states its own way, as its prototype page does,
 *  and `data-target-met` carries the verdict for tests and the e2e rows. */
export function TargetBadge({
  met,
  metLabel,
  missedLabel,
}: {
  met: boolean | null;
  metLabel: string;
  missedLabel: string;
}): ReactNode {
  const [label, tone] =
    met === null
      ? ["no data", "outline" as const]
      : met
        ? [metLabel, "success" as const]
        : [missedLabel, "danger" as const];
  return (
    <span className={badgeClassName(tone)} data-target-met={met === null ? "none" : String(met)}>
      {label}
    </span>
  );
}
