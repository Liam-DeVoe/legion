import { buttonClassName } from "../../components/Button";
import { inputClassName } from "../../components/Field";
import { card, textSecondaryOnCanvas } from "../../theme/classes";

/**
 * The recipes a Settings form or table outside this folder still takes by name
 * (`features/delivery/DeliverySettingsForm.tsx`, `features/credentials/RevocableList.tsx`), each
 * now the design system's own shape (`components/DESIGN.md`). The sections in this folder use
 * the primitives themselves: `Table`, `Field`, `Input`, `Select`, `Button`.
 */
/** `relative` is load-bearing: a table's `sr-only` header cell is absolutely positioned, and
 *  without a positioned ancestor inside the scroll container its containing block is the page,
 *  so it sits at the scrolled table's own x and widens the document past the viewport (the
 *  Settings page measured 417px at 390px). The table itself already scrolls here. */
export const settingsTableWrapper = `relative overflow-x-auto rounded-lg border ${card}`;
/** A label that wraps its control: the field label's type, 6 px above the control. */
export const settingsFieldLabel = `grid content-start gap-1.5 text-xs font-medium ${textSecondaryOnCanvas}`;
export const settingsMonoInput = inputClassName({ mono: true });
/** A Settings form's submit. The page holds several small forms and no one act that is the
 *  page's own, so each form's submit is the secondary button and none is the primary. */
export const settingsSubmitButton = buttonClassName({ variant: "secondary" });
/** An `owner/repo` field's `pattern`: one slash between an owner and a name that hold neither a
 *  slash nor whitespace. Its slashes are escaped because browsers compile a `pattern` with the `v`
 *  flag, where an unescaped `/` inside a character class is a syntax error, and a `pattern` that
 *  does not compile checks nothing. */
export const ownerRepoPattern = "[^\\/\\s]+\\/[^\\/\\s]+";

/** What a repository box says about a value it refuses before it sends it: one that is not
 *  `owner/name` by the box's own pattern. */
export const NOT_OWNER_NAME = "Write the repository as owner/name.";

/** `pattern` as a test of a whole value, as a field's `pattern` attribute applies it: the check a
 *  repository box makes before it sends, so a typo shows at the box and not as the server's 400 a
 *  round trip later. */
export function wholeValue(pattern: string): RegExp {
  return new RegExp(`^(?:${pattern})$`);
}

/** The gap between one Settings section and the next: 32 px, the design system's step between
 *  the sections of a page. The page owns the rhythm - no section sets an outer margin of its
 *  own, which is how three sections came to line up at 40px by coincidence rather than by rule. */
export const settingsSectionGap = "space-y-6";
/** Each Settings section is one card: its heading, its table and the form that adds to that
 *  table share an edge, so a section reads as one unit on the canvas rather than loose rows. The
 *  heading inside steps up to 16 px so it anchors the card over its description. */
export const settingsSectionCard = `rounded-lg border ${card} p-4 sm:p-6 [&_h2]:text-base`;
