import { type ReactNode, useState } from "react";
import type { BarActionProps } from "../../components/BarAction";
import { textGlyph } from "../../components/bar-look";
import {
  borderDefault,
  focusVisibleRing,
  surfaceBg,
  surfaceMutedHoverBg,
  textPrimaryOnCanvas,
} from "../../theme/classes";
import { usePriorityWrites } from "./priority-writes";

/** **New priority**'s glyph, drawn before its words where the control bar draws it. */
const PLUS_GLYPH = textGlyph("+");

/**
 * The one-line form a priority is added with: its title, **Add priority** and **Cancel**. Its
 * opener owns the write (`onAdd`), which outlives the form: the form closes as the write goes
 * out, and a closed form is no longer drawn. The form keeps a line of its own wherever it stands.
 */
function NewPriorityForm({
  onAdd,
  onClose,
}: {
  onAdd: (title: string) => void;
  onClose: () => void;
}): ReactNode {
  const [title, setTitle] = useState("");
  return (
    <form
      aria-label="New priority"
      className="flex basis-full flex-wrap items-center gap-2"
      onSubmit={(event) => {
        event.preventDefault();
        const named = title.trim();
        if (named === "") return;
        onAdd(named);
        onClose();
      }}
    >
      <input
        aria-label="Title of the new priority"
        className={`min-w-64 flex-1 rounded-lg border px-3 py-1.5 text-sm ${borderDefault} ${surfaceBg} ${textPrimaryOnCanvas}`}
        onChange={(event) => setTitle(event.target.value)}
        placeholder="What the new priority is"
        value={title}
      />
      <button
        className={`min-h-11 rounded-lg border px-3 text-sm disabled:opacity-40 ${borderDefault} ${surfaceMutedHoverBg} ${focusVisibleRing}`}
        disabled={title.trim() === ""}
        type="submit"
      >
        Add priority
      </button>
      <button
        className={`min-h-11 rounded-lg border px-3 text-sm ${borderDefault} ${surfaceMutedHoverBg}`}
        onClick={onClose}
        type="button"
      >
        Cancel
      </button>
    </form>
  );
}

/**
 * **New priority** in the priorities table's control bar: the action the bar draws (`TableToolbar`,
 * a quiet button of its line, and an item of **More** below 640 px of the bar's width), and the
 * form it opens, which the page draws under the bar, outside it. Only a reader who may write a
 * priority (`usePriorityWrites`) is offered the action; every other reader is offered none, and
 * the form is never drawn for them.
 */
export function useNewPriority(): { action?: BarActionProps; form: ReactNode } {
  const { add, canWrite } = usePriorityWrites();
  const [adding, setAdding] = useState(false);
  if (!canWrite) return { form: null };
  return {
    action: {
      icon: PLUS_GLYPH,
      label: "New priority",
      onPress: () => setAdding((was) => !was),
    },
    form: adding ? (
      <NewPriorityForm onAdd={(title) => add.mutate({ title })} onClose={() => setAdding(false)} />
    ) : null,
  };
}

/**
 * The way the first priority is added, on the page alone where no priority is written yet, which
 * is the one place a fresh deployment can write its first: a button that opens the one-line form
 * beside it, on a line of its own. It stands in no control bar. Drawn only for a reader who may
 * write a priority (`usePriorityWrites`); every other reader is shown nothing here.
 */
export function NewPriority(): ReactNode {
  const { action, form } = useNewPriority();
  if (action === undefined) return null;
  return (
    <>
      <button
        className={`rounded-lg border px-3 py-1.5 text-sm ${borderDefault} ${surfaceMutedHoverBg}`}
        onClick={action.onPress}
        type="button"
      >
        New priority
      </button>
      {form}
    </>
  );
}
