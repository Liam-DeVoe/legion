import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useId, useRef, useState } from "react";

import { api, type WriteBounds, type WriteMethod } from "../../api/client";
import { isLiveQuery } from "../../api/query-refresh";
import type { DeliverySessionGroup } from "../../api/types";
import { Button } from "../../components/Button";
import { Input } from "../../components/Field";
import { QueryError } from "../../components/QueryError";
import {
  borderDefault,
  borderStrong,
  dismissButtonText,
  inputClasses,
  surfaceBg,
  textMutedOnSurface,
  textPrimaryOnSurface,
} from "../../theme/classes";
import { AgentName, useAgentText } from "../refs/AgentName";
import { useDialog } from "../shell/useDialog";
import { SendStoppedError, stoppedWriteNote } from "../writes/keyed-write";
import { LandedWithoutAnswer } from "../writes/LandedWithoutAnswer";
import { refusedNote, useKeyedWrite, useRefusalOfSubject } from "../writes/useKeyedWrite";
import { storedSessionTitle } from "./lib/sessionTitles";

/** One change to the groups: what it is, as the editor's sentences name it (`the rename of the
 *  group Crew`), the group it changes, the method of its request and one attempt of it. A delete
 *  is idempotent and goes without the key. A create changes no group yet and names what it makes
 *  (`made`), which the editor says was made when Dispatch lost its answer. */
interface GroupChange {
  readonly group?: DeliverySessionGroup;
  readonly made?: string;
  readonly method: WriteMethod;
  readonly run: (bounds: WriteBounds) => Promise<unknown>;
  readonly what: string;
}

const sectionLabel = `text-xs font-medium ${textMutedOnSurface}`;
const _fieldClass = `h-8 min-w-0 flex-1 rounded border px-2 text-sm ${inputClasses(true)} ${textPrimaryOnSurface}`;

/** One group: its name, renamable, its members, each removable, and the window's selected
 *  sessions to add. While any group write is pending, every mutating button is disabled so a
 *  second click cannot race the one request that decides the membership. */
function GroupCard({
  group,
  storedTitle,
  selected,
  pending,
  write,
}: {
  group: DeliverySessionGroup;
  storedTitle: (session: string) => string | undefined;
  selected: readonly string[];
  pending: boolean;
  write: (change: GroupChange) => void;
}): ReactNode {
  const id = useId();
  const text = useAgentText();
  const [name, setName] = useState(group.name);
  const addable = selected.filter((session) => !(group.sessions ?? []).includes(session));
  return (
    <fieldset
      aria-label={group.name}
      className={`grid gap-2 rounded-lg border p-3 ${borderDefault}`}
    >
      <div className="flex items-center gap-2">
        <label className="sr-only" htmlFor={`${id}-name`}>
          Name
        </label>
        <Input
          className="flex-1"
          id={`${id}-name`}
          onChange={(event) => setName(event.target.value)}
          surface="recessed"
          value={name}
        />
        <Button
          disabled={pending || name.trim() === "" || name.trim() === group.name}
          onClick={() =>
            write({
              group,
              method: "PATCH",
              run: (bounds) =>
                api.updateDeliverySessionGroup(group.id, { name: name.trim() }, bounds),
              what: `the rename of the group ${group.name}`,
            })
          }
        >
          Rename
        </Button>
      </div>
      <ul className="flex flex-wrap gap-1">
        {(group.sessions ?? []).map((session) => (
          <li
            className={`flex h-6 items-center gap-0.5 rounded border pl-2 text-xs ${borderStrong}`}
            key={session}
          >
            <AgentName
              className="max-w-48 truncate"
              sessionId={session}
              storedTitle={storedTitle(session)}
            />
            <button
              aria-label={`Remove ${text.sessionText(session, storedTitle(session))}`}
              className={`flex size-6 items-center justify-center rounded ${dismissButtonText}`}
              disabled={pending}
              onClick={() =>
                write({
                  group,
                  method: "PATCH",
                  run: (bounds) =>
                    api.updateDeliverySessionGroup(group.id, { remove: [session] }, bounds),
                  what: `the removal of ${text.sessionText(session, storedTitle(session))} from ${group.name}`,
                })
              }
              type="button"
            >
              ✕
            </button>
          </li>
        ))}
      </ul>
      <div className="flex gap-2">
        <Button
          disabled={pending || addable.length === 0}
          size="sm"
          onClick={() =>
            write({
              group,
              method: "PATCH",
              run: (bounds) => api.updateDeliverySessionGroup(group.id, { add: addable }, bounds),
              what: `the sessions added to ${group.name}`,
            })
          }
        >
          Add selected
        </Button>
        <Button
          disabled={pending}
          size="sm"
          variant="danger"
          onClick={() =>
            write({
              group,
              method: "DELETE",
              run: (bounds) => api.deleteDeliverySessionGroup(group.id, bounds),
              what: `the deletion of the group ${group.name}`,
            })
          }
        >
          Delete group
        </Button>
      </div>
    </fieldset>
  );
}

/** The Delivery page's session-groups editor, opened from the Parent agent facet: the window's
 *  sessions with their titles, pull request counts and groups, to select and name as a new group,
 *  and every group, to rename, add the selected sessions to, remove members from, or delete. Every
 *  session is an `AgentName`; a member no pull request of the window names has no stored title, so
 *  it reads by its registry title, else its short id. A save reaches the page's live views as
 *  Dispatch's own delta, so it refreshes only the `["delivery"]` reads that are not live (an open
 *  run's drill-down), and a refusal shows as the server wrote it, after the change it refused. A
 *  dialog (`useDialog`), at the right edge as the drill-down is, open while it is mounted. */
export function SessionGroupsEditor({
  onClose,
  groups,
  titles,
  counts,
}: {
  onClose: () => void;
  groups: readonly DeliverySessionGroup[];
  titles: Readonly<Record<string, string>>;
  counts: Readonly<Record<string, number>>;
}): ReactNode {
  const id = useId();
  const queryClient = useQueryClient();
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  const dialog = useDialog<HTMLElement>({ initialFocusRef: closeButtonRef, onClose, open: true });
  const [selected, setSelected] = useState<string[]>([]);
  const [name, setName] = useState("");
  // The editor shows a refusal itself, as the server wrote it.
  const save = useKeyedWrite({
    inline: true,
    label: (change: GroupChange) => `Session groups: ${change.what}`,
    onSuccess: () => {
      setSelected([]);
      void queryClient.invalidateQueries({
        queryKey: ["delivery"],
        predicate: (query) => !isLiveQuery(query),
      });
    },
    method: (change) => change.method,
    write: (change, bounds) => change.run(bounds),
  });
  // One refusal speaks for every group, so it names the change it refused, the group's name with
  // it, and goes once that group is renamed or removed - from another tab, or by another person -
  // rather than stand beside groups it was never about. A create's has no group to lose.
  const refused = useRefusalOfSubject(
    save,
    ({ group: changed }) =>
      changed === undefined ||
      groups.some((group) => group.id === changed.id && group.name === changed.name)
  )
    ? save.variables
    : undefined;

  const text = useAgentText();
  const storedTitle = (session: string): string | undefined => storedSessionTitle(titles, session);
  const groupOf = new Map<string, string>();
  for (const group of groups) {
    for (const session of group.sessions ?? []) groupOf.set(session, group.name);
  }
  const sessions = Object.keys(counts).sort(
    (a, b) =>
      (counts[b] ?? 0) - (counts[a] ?? 0) ||
      text.sessionText(a, storedTitle(a)).localeCompare(text.sessionText(b, storedTitle(b)))
  );
  const toggle = (session: string) =>
    setSelected((current) =>
      current.includes(session) ? current.filter((s) => s !== session) : [...current, session]
    );
  const create = () =>
    save.mutate(
      {
        made: `The group “${name.trim()}”`,
        method: "POST",
        run: (bounds) =>
          api.createDeliverySessionGroup({ name: name.trim(), sessions: selected }, bounds),
        what: `the new group ${name.trim()}`,
      },
      // Made either way - with no answer to give too - so the name goes and a second press
      // makes nothing.
      { onSuccess: () => setName("") }
    );
  const madeUnanswered = save.unanswered?.variables.made;

  return (
    <aside
      aria-labelledby={`${id}-heading`}
      aria-modal="true"
      className={`fixed top-0 right-0 z-30 flex h-full w-[28rem] max-w-full flex-col gap-4 overflow-y-auto border-l p-5 shadow-xl ${borderDefault} ${surfaceBg} ${textPrimaryOnSurface}`}
      ref={dialog.containerRef}
      role="dialog"
    >
      <div className="flex items-start justify-between gap-2">
        <h2 className="text-[15px] leading-8 font-semibold" id={`${id}-heading`}>
          Session groups
        </h2>
        <Button
          aria-label="Close session groups"
          iconOnly
          onClick={onClose}
          ref={closeButtonRef}
          variant="ghost-muted"
        >
          <svg aria-hidden="true" className="size-4" fill="none" viewBox="0 0 16 16">
            <path
              d="m4 4 8 8m0-8-8 8"
              stroke="currentColor"
              strokeLinecap="round"
              strokeWidth="1.5"
            />
          </svg>
        </Button>
      </div>
      <p className={`text-sm ${textMutedOnSurface}`}>
        A group is one Parent agent: every pull request its sessions wrote first counts under the
        group's name. A session can be in one group.
      </p>

      <section className="grid gap-2">
        <h3 className={sectionLabel} id={`${id}-sessions`}>
          Sessions in this window
        </h3>
        <ul aria-labelledby={`${id}-sessions`} className="grid gap-1">
          {sessions.map((session) => {
            const count = counts[session] ?? 0;
            const inGroup = groupOf.get(session);
            return (
              <li className="flex items-center gap-2 text-sm" key={session}>
                {/* The checkbox's label keeps the name; the agent's live link sits beside it. */}
                <AgentName
                  className="flex min-w-0 flex-1 items-center"
                  control={(agentName) => (
                    <label className="flex min-w-0 flex-1 items-center gap-2" title={session}>
                      <input
                        checked={selected.includes(session)}
                        onChange={() => toggle(session)}
                        type="checkbox"
                      />
                      <span className="min-w-0 truncate">{agentName}</span>
                    </label>
                  )}
                  kind="control"
                  sessionId={session}
                  storedTitle={storedTitle(session)}
                />
                <span className={`shrink-0 text-xs ${textMutedOnSurface}`}>
                  {count} PR{count === 1 ? "" : "s"}
                  {inGroup === undefined ? "" : ` · ${inGroup}`}
                </span>
              </li>
            );
          })}
        </ul>
        <div className="flex items-center gap-2">
          <label className="sr-only" htmlFor={`${id}-new-name`}>
            New group name
          </label>
          <Input
            className="flex-1"
            id={`${id}-new-name`}
            onChange={(event) => {
              save.forgetUnanswered();
              setName(event.target.value);
            }}
            placeholder="Group name"
            surface="recessed"
            value={name}
          />
          <Button
            disabled={name.trim() === "" || selected.length === 0 || save.isPending}
            onClick={create}
          >
            Create group
          </Button>
        </div>
        {madeUnanswered === undefined ? null : <LandedWithoutAnswer what={madeUnanswered} />}
      </section>

      {/* A change the reader stopped may have been saved, and every button here is a press of
          its own, under a key of its own: none makes the stopped change again. */}
      {refused === undefined ? null : (
        <QueryError
          message={
            save.error instanceof SendStoppedError
              ? stoppedWriteNote(refused.what, "saved", {
                  anew: "Making it again sends it as a new write.",
                })
              : refusedNote(`save ${refused.what}`, save.error)
          }
        />
      )}

      {groups.length === 0 ? null : (
        <section className="grid gap-2">
          <h3 className={sectionLabel}>Groups</h3>
          {groups.map((group) => (
            <GroupCard
              group={group}
              key={`${group.id}:${group.updated_at}`}
              pending={save.isPending}
              selected={selected}
              storedTitle={storedTitle}
              write={(change) => save.mutate(change)}
            />
          ))}
        </section>
      )}
    </aside>
  );
}
