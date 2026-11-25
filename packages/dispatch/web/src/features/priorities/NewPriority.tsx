import { ISSUE_TITLE_MAX } from "@legion/contracts";
import { type ReactNode, useState } from "react";
import { Button } from "../../components/Button";
import { Input } from "../../components/Field";
import { toolbarControlHeight } from "../../theme/classes";
import { usePriorityWrites } from "./priority-writes";

/**
 * The way a priority is added: a button that opens a one-line form for its title. It stands in
 * the priorities table's toolbar, and on the page alone where no priority is written yet, which
 * is the one place a fresh deployment can write its first. Drawn only for a reader who may write
 * a priority (`usePriorityWrites`); every other reader is shown nothing here. The form takes a
 * line of its own in the flex row it is drawn in.
 */
export function NewPriority(): ReactNode {
  const { add, canWrite } = usePriorityWrites();
  const [adding, setAdding] = useState(false);
  const [title, setTitle] = useState("");
  if (!canWrite) return null;
  return (
    <>
      <Button className={toolbarControlHeight} onClick={() => setAdding((was) => !was)}>
        New priority
      </Button>
      {adding ? (
        <form
          aria-label="New priority"
          className="flex basis-full flex-wrap items-center gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            const named = title.trim();
            if (named === "") return;
            add.mutate({ title: named });
            setTitle("");
            setAdding(false);
          }}
        >
          <Input
            aria-label="Title of the new priority"
            className={`w-full flex-none sm:w-auto sm:min-w-64 sm:flex-1 ${toolbarControlHeight}`}
            maxLength={ISSUE_TITLE_MAX}
            onChange={(event) => setTitle(event.target.value)}
            placeholder="What the new priority is"
            value={title}
          />
          <Button className={toolbarControlHeight} disabled={title.trim() === ""} type="submit">
            Add priority
          </Button>
          <Button
            onClick={() => {
              setAdding(false);
              setTitle("");
            }}
            className={toolbarControlHeight}
            variant="ghost"
          >
            Cancel
          </Button>
        </form>
      ) : null}
    </>
  );
}
