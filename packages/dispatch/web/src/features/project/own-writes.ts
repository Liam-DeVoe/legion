import { useRef } from "react";
import { useWriteSelection } from "../writes/write-store";

/** The target every write of one issue is lined up on (`issue:<KEY>`). */
const ISSUE_TARGET = "issue:";

/** How long an issue stays the reader's own after its last write leaves the store: long enough
 *  for the read that write's landing starts (`kind.landed`, or the store's own refresh) to
 *  answer, so the change it brings is drawn as the reader's. */
const OWN_MS = 30_000;

const issueKeys = (rows: readonly { readonly target: string | undefined }[]): string[] =>
  rows.flatMap(({ target }) =>
    target?.startsWith(ISSUE_TARGET) === true ? [target.slice(ISSUE_TARGET.length)] : []
  );

/**
 * The issues the reader changed on this page: every issue a write of theirs is on now - a bulk
 * action, a status or priority change, Start next, a move on the Board - and every one a write
 * left in the last half minute. A held list draws their changes at once (`useDataTable`'s `own`,
 * `useHeldBoard`), since only what others change waits for the reader. Answered as the same set
 * while it names the same issues.
 */
export function useOwnIssueWrites(): ReadonlySet<string> {
  const writing = useWriteSelection("own-issue-writes", issueKeys);
  const seen = useRef(new Map<string, number>());
  const answer = useRef<{ readonly text: string; readonly keys: ReadonlySet<string> }>({
    text: "",
    keys: new Set(),
  });
  const now = Date.now();
  for (const key of writing) seen.current.set(key, now);
  for (const [key, at] of seen.current) if (now - at >= OWN_MS) seen.current.delete(key);
  const keys = [...seen.current.keys()].sort();
  const text = keys.join("\n");
  if (answer.current.text !== text) answer.current = { text, keys: new Set(keys) };
  return answer.current.keys;
}
