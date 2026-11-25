import type { ReactNode } from "react";

import type { Project } from "../../api/types";
import { Select } from "../../components/Field";

/** The "Choose a project" picker of the Settings page's architecture sources form and its Legion
 *  daemons form; `disabled` is omitted from the DOM when not given. */
export function ProjectSelect({
  disabled,
  id,
  onChange,
  projects,
  value,
}: {
  disabled?: boolean;
  id: string;
  onChange: (project: string) => void;
  projects: readonly Project[];
  value: string;
}): ReactNode {
  return (
    <Select
      disabled={disabled}
      id={id}
      onChange={(event) => onChange(event.target.value)}
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
    </Select>
  );
}
