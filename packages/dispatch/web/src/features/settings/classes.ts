import {
  inputClasses,
  primaryButtonBg,
  primaryButtonHoverBg,
  textPrimaryOnSurface,
  textSecondaryOnCanvas,
} from "../../theme/classes";

/** The recipes every Settings section's form shares, so the sections stay in step. The table
 *  each section draws is `components/TableShell.tsx`, which owns its box, head and rows. */
export const settingsFieldLabel = `grid gap-1 text-sm font-medium ${textSecondaryOnCanvas}`;
export const settingsMonoInput = `rounded-md px-3 py-2 font-mono ${inputClasses(false)} ${textPrimaryOnSurface}`;
/** The trigger of a Settings list of the shared list's single pick, in the place of the form's
 *  `<select>`. Chromium draws a select's text on its font's own line (`line-height: normal`) and
 *  2 px taller than that line, where a button takes the 20 px line the page sets: measured with
 *  14 px text, the select stood 34 px and the trigger 36 px, so each project row of the table
 *  grew by the difference. `leading-[normal]` and 9 px above and below the text draw the trigger
 *  as the select was, 34 px, whatever font draws it. */
export const settingsListTrigger = `rounded-md px-3 py-[9px] text-left leading-[normal] disabled:cursor-not-allowed disabled:opacity-50 ${inputClasses(false)} ${textPrimaryOnSurface}`;
export const settingsSubmitButton = `min-h-11 rounded-md px-4 py-2 font-medium disabled:cursor-not-allowed disabled:opacity-50 md:min-h-9 ${primaryButtonBg} ${primaryButtonHoverBg}`;
/** An `owner/repo` field's `pattern`: one slash between an owner and a name that hold neither a
 *  slash nor whitespace. Its slashes are escaped because browsers compile a `pattern` with the `v`
 *  flag, where an unescaped `/` inside a character class is a syntax error, and a `pattern` that
 *  does not compile checks nothing. */
export const ownerRepoPattern = "[^\\/\\s]+\\/[^\\/\\s]+";

/** The gap between one Settings section and the next: the spec's step between the parts of a
 *  page. The page owns the rhythm - no section sets an outer margin of its own, which is how
 *  three sections came to line up at 40px by coincidence rather than by rule. */
export const settingsSectionGap = "space-y-4";
