/** How the bar names the actions of a run and the items they act on. */

/** An action as the bar names it: the catalogue's name without its prefix. */
export function actionName(action: string): string {
  return action.replace(/^dispatch_/, "").replaceAll("_", " ");
}

/** The item an invocation's arguments name: the value of `argument`, the argument Dispatch says
 *  names it (`GoingInvocation.target_argument`), and `""` where it names none. */
export function targetOf(args: Readonly<Record<string, unknown>>, argument: string): string {
  const value = argument === "" ? undefined : args[argument];
  return typeof value === "string" ? value : "";
}
