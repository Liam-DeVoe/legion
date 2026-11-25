import { type ReactNode, useMemo } from "react";
import { Link } from "react-router-dom";
import { EmptyState } from "../../components/EmptyState";
import { PageHeader } from "../../components/Layout";
import { LoadingSkeleton } from "../../components/LoadingSkeleton";
import { dangerText, linkChrome } from "../../theme/classes";
import { useAgents } from "../conversation/useAgents";
import { useDocumentTitle } from "../shell/useDocumentTitle";
import { machineList } from "./machine-model";
import { MachineList } from "./ui/MachineList";

/**
 * Every machine a live session names, with how many sessions are on it and how many of those are
 * in a tmux pane: the second count says which machines have a layout to draw. The list is the
 * agent registry grouped by `host` and nothing else, so it adds no read to the page's one shared
 * poll.
 */
export function MachinesPage(): ReactNode {
  useDocumentTitle("Machines · Dispatch");
  const { agents, current, error, isError, isPending } = useAgents(true, true);
  const machines = useMemo(() => machineList(agents), [agents]);

  if (isPending) return <LoadingSkeleton label="Loading machines" />;
  // A refresh that failed keeps the rows it has; with no rows there is nothing to keep.
  if (isError && agents.length === 0) {
    return <p className={dangerText}>Could not load agents: {error}</p>;
  }

  return (
    <section aria-label="Machines" data-testid="machines-page">
      <PageHeader
        description="Every machine a live session names, and how many of its sessions are in tmux."
        // The way back to the page this one hangs off, above the title as every page under
        // Agents draws it.
        eyebrow={
          <Link className={linkChrome} to="/agents">
            <span aria-hidden>←&nbsp;</span>Agents
          </Link>
        }
        title="Machines"
      />
      {machines.length === 0 ? (
        // Only a current list says that nobody is connected.
        current ? (
          <EmptyState
            label="Machines empty state"
            message="No agent is connected, so no machine is listed."
          />
        ) : null
      ) : (
        <MachineList machines={machines} />
      )}
    </section>
  );
}
