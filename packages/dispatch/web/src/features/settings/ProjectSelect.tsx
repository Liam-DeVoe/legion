import type { ReactNode } from "react";

import type { Project } from "../../api/types";
import { inputClasses, textPrimaryOnSurface } from "../../theme/classes";

/** The "Choose a project" picker both Settings forms use. `held` while its form's write is out:
 *  it keeps the focus and its Tab stop, and a pick made then is dropped (`heldPress`). */
export function ProjectSelect({
  held = false,
  id,
  onChange,
  projects,
  value,
}: {
  held?: boolean;
  id: string;
  onChange: (project: string) => void;
  projects: readonly Project[];
  value: string;
}): ReactNode {
  return (
    <select
      aria-disabled={held || undefined}
      className={`rounded-md px-3 py-2 aria-disabled:opacity-50 ${inputClasses(false)} ${textPrimaryOnSurface}`}
      id={id}
      onChange={(event) => {
        if (!held) onChange(event.target.value);
      }}
      required
      value={value}
    >
      <option disabled value="">
        Choose a project
      </option>
      {projects.map((candidate) => (
        <option key={candidate.key} value={candidate.key}>
          {candidate.key} · {candidate.name}
        </option>
      ))}
    </select>
  );
}
