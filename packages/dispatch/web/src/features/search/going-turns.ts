/** How the bar names the actions of a run. */

/** An action as the bar names it: the catalogue's name without its prefix. */
export function actionName(action: string): string {
  return action.replace(/^dispatch_/, "").replaceAll("_", " ");
}
