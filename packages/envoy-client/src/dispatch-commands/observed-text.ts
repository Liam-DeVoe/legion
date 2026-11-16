import { SPEC_CHECK_OBSERVED_CUT_NOTE, SPEC_CHECK_OBSERVED_MAX } from "@legion/contracts";

/** The UTF-16 length of the first `count` characters (code points) of `text`, or undefined when
 *  `text` holds no more than `count` characters. */
function lengthOfFirst(text: string, count: number): number | undefined {
  let at = 0;
  for (let seen = 0; seen < count && at < text.length; seen += 1) {
    at += (text.codePointAt(at) ?? 0) > 0xffff ? 2 : 1;
  }
  return at < text.length ? at : undefined;
}

/** What a result of a spec check keeps of `observed`, as Dispatch keeps it (`cutObserved`,
 *  spec_results.go): each NUL, which Dispatch cannot store, as U+FFFD, then the text whole when it
 *  is SPEC_CHECK_OBSERVED_MAX characters or fewer, else its first characters and
 *  SPEC_CHECK_OBSERVED_CUT_NOTE, that many in all. Each byte of a file that is not UTF-8 is
 *  already U+FFFD, as the command decodes the files it reads. A test's log can be larger than any
 *  request Dispatch takes, so `dispatch result --project` sends only what Dispatch keeps, and the
 *  result is written. */
export function observedAsKept(observed: string): string {
  const text = observed.replaceAll("\u0000", "\uFFFD");
  if (lengthOfFirst(text, SPEC_CHECK_OBSERVED_MAX) === undefined) return text;
  const kept = SPEC_CHECK_OBSERVED_MAX - [...SPEC_CHECK_OBSERVED_CUT_NOTE].length;
  return `${text.slice(0, lengthOfFirst(text, kept))}${SPEC_CHECK_OBSERVED_CUT_NOTE}`;
}
