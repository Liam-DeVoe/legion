import { type DispatchReadingSurface, mentionDrawn } from "@legion/contracts";
import { type ReactNode, useCallback, useEffect, useMemo, useState } from "react";

import type { DisplayTree } from "../../api/types";
import {
  citedReferences,
  type MarkdownHeadline,
  type ReferenceName,
  type ReferenceNaming,
  useMarkdownHeadline,
} from "./markdown-engine";
import { isPartyRoute } from "./party-references";
import { useProjectKeys } from "./project-keys";
import { ReferenceHostIs } from "./reference-host";
import { type KnownReferences, useKnownReferences } from "./reference-names";
import {
  questionReferences,
  questionsQuery,
  useReferenceQuery,
  useReferenceTarget,
} from "./reference-target";
import { RecordIssue } from "./role-holders";
import { type DispatchReferenceRoute, parseNodeReference, shortForm } from "./routes";
import type { TextSource } from "./text-references";

/** No reference read yet, shared so a line that names none keeps one identity across renders. */
const NO_NAMES: ReadonlyMap<string, ReferenceName> = new Map();

/** No reference to read, shared for the same reason. */
const NO_ROUTES: ReadonlyMap<string, DispatchReferenceRoute> = new Map();

/** What a reference reads as in plain words once its target has answered (`ReferenceName`): the
 *  name the painted link reads for it (`RefLink`), which for an issue is its key and then its
 *  title, as a project's issue list writes a key beside one, and for a party's mention what the
 *  painted mention draws (`@legion/contracts`'s `mentionDrawn`): the mention sign and that name,
 *  or the sign and the party's short form where it exists with no name. A target Dispatch
 *  answered does not exist, or exists with no name of its own, has no name, and reads as the
 *  words its author wrote (R8). Undefined while the target has not answered, and for one the
 *  reader may not see, where the projection keeps the words written, as the painted link does
 *  until its title resolves. */
function referenceAnswer(
  route: DispatchReferenceRoute,
  { known, missing, title }: { known: boolean; missing: boolean; title: string | undefined }
): ReferenceName | undefined {
  if (missing) {
    return { mention: undefined, name: undefined };
  }
  if (title === undefined && !known) {
    return undefined;
  }
  return {
    mention: isPartyRoute(route) ? mentionDrawn({ known, title }, shortForm(route)) : undefined,
    name: title === undefined || route.kind !== "issue" ? title : `${route.key} ${title}`,
  };
}

/** How a read tells the projection what one reference resolved to. */
type NameReport = (address: string, name: ReferenceName | undefined) => void;

/** One reference's own read, which draws nothing: it reads the target the painted link reads
 *  (`useReferenceTarget`), so a line of text and the same line as a label never name the same
 *  reference two ways. */
function OwnNameRead({
  address,
  onName,
  route,
}: {
  address: string;
  onName: NameReport;
  route: DispatchReferenceRoute;
}): ReactNode {
  const { known, missing, title } = useReferenceTarget(route, { text: false });
  const answer = referenceAnswer(route, { known, missing, title });
  const name = answer?.name;
  const mention = answer?.mention;
  const answered = answer !== undefined;
  useEffect(() => {
    onName(address, answered ? { mention, name } : undefined);
  }, [address, answered, mention, name, onName]);
  return null;
}

/** A reference's own read, mounted one commit after the reference was first found unread. A
 *  page publishes what it asked for in the layout phase of the commit that draws the references
 *  (`usePublishReferenceNames`), and React runs that commit's passive effects, where a read
 *  starts, before it draws the update the publication makes; the update this read's own effect
 *  makes is drawn after that one. So a reference the page is asking for leaves `unread` before
 *  its read mounts, and no read asks for it a second time (`refetch-budget.e2e.ts`). */
function ReferenceNameRead(props: {
  address: string;
  onName: NameReport;
  route: DispatchReferenceRoute;
}): ReactNode {
  const [settled, setSettled] = useState(false);
  useEffect(() => {
    setSettled(true);
  }, []);
  return settled ? <OwnNameRead {...props} /> : null;
}

/** What the page published answers for one reference (`reference-names.tsx`): its name where the
 *  page read one - an issue's after its key, as a link to an issue reads - and `null` where the
 *  page holds an answer that names nothing, which reads as the words the author wrote (R8): a
 *  reference it read as ambiguous or as gone, and one it has asked for and not been answered for
 *  yet. `undefined` where the page holds nothing for the reference, which is then read on its
 *  own. A party is never asked of the page's batched read, so it is always read on its own. */
function publishedAnswer(
  address: string,
  route: DispatchReferenceRoute,
  known: KnownReferences
): ReferenceName | null | undefined {
  if (isPartyRoute(route)) return undefined;
  const name = known.names.get(address);
  if (name !== undefined) {
    return { mention: undefined, name: route.kind === "issue" ? `${route.key} ${name}` : name };
  }
  return known.asked.has(address) || known.ambiguous.has(address) || known.gone.has(address)
    ? null
    : undefined;
}

/**
 * What each of `addresses` reads as (`ReferenceName`), by the one rule every plain line and every
 * row of the new issue page names a reference by: the name the page already read for it, where
 * a page published one (`reference-names.tsx`), and otherwise the target's own read
 * (`useReferenceTarget`), mounted in `reads`, which draws nothing and which the caller mounts
 * wherever it draws. A reference the page answers with no name, or has asked for and not been
 * answered for yet, takes no read of its own and is left out, so it reads as the words its author
 * wrote (R8); so is one whose read has not answered. A read's name for an address the caller has
 * since moved off is kept rather than dropped, since the same reference again names the same
 * thing. `issue` is the issue the record holding the addresses is on, by which a role is read
 * (`RecordIssue`).
 */
export function useReferenceNames(
  addresses: readonly string[],
  issue: string | undefined
): { names: ReadonlyMap<string, ReferenceName>; reads: ReactNode } {
  // Keyed on the references themselves, never on the array: a caller answers a fresh one every
  // time it runs, and reading the same references again for it would never settle.
  const addressKey = addresses.join("\n");
  const known = useKnownReferences();
  const { published, unread } = useMemo(() => {
    const named = new Map<string, ReferenceName>();
    const own = new Map<string, DispatchReferenceRoute>();
    for (const address of addressKey === "" ? [] : addressKey.split("\n")) {
      const route = parseNodeReference(address);
      if (route === undefined) continue;
      const answer = publishedAnswer(address, route, known);
      if (answer === undefined) own.set(address, route);
      else if (answer !== null) named.set(address, answer);
    }
    return {
      published: named.size === 0 ? NO_NAMES : named,
      unread: own.size === 0 ? NO_ROUTES : own,
    };
  }, [addressKey, known]);
  const [read, setRead] = useState<ReadonlyMap<string, ReferenceName>>(NO_NAMES);
  const onName = useCallback<NameReport>((address, name) => {
    setRead((current) => {
      const now = current.get(address);
      if (
        now?.name === name?.name &&
        now?.mention === name?.mention &&
        current.has(address) === (name !== undefined)
      ) {
        return current;
      }
      const next = new Map(current);
      if (name === undefined) {
        next.delete(address);
      } else {
        next.set(address, name);
      }
      return next;
    });
  }, []);
  // The page's name stands over a read of one's own: a reference the page names now is read by
  // the page alone, whatever an earlier read of its own answered.
  const names = useMemo(
    () =>
      published.size === 0 ? read : read.size === 0 ? published : new Map([...read, ...published]),
    [published, read]
  );
  const reads = useMemo(
    () => (
      // The host every read sits in: an element that can hold no markup, so a body drawn under
      // one draws its characters rather than a link. Each read takes its role by the issue the
      // record is on, as the painted line's links do (`RecordIssue`).
      <ReferenceHostIs host="plain">
        <RecordIssue.Provider value={issue}>
          {[...unread].map(([address, route]) => (
            <ReferenceNameRead address={address} key={address} onName={onName} route={route} />
          ))}
        </RecordIssue.Provider>
      </ReferenceHostIs>
    ),
    [issue, onName, unread]
  );
  return { names, reads };
}

/** How a caller of `usePlainHeadline` asks for its line. */
export interface PlainHeadlineOptions {
  /** Reads the text's first line alone rather than the whole of it (`useMarkdownHeadline`). */
  readonly firstLine?: boolean;
  /** The issue the record holding the text is on, by which a role in it is read (`RecordIssue`),
   *  for a caller that names no `source`: a line that reads no question map, as a row of the new
   *  issue page reads none (`node-row-words`). A `source` names its own issue. */
  readonly issue?: string | undefined;
  /** The most characters the line holds; the whole text where it is left out. */
  readonly max?: number;
  /** The record the text is, whose document's question map names a `#id` in it, as a rendered
   *  body of the same record names one (`useTextReferences`); a line of no named record reads a
   *  `#id` as written. */
  readonly source?: TextSource;
  /** The row of the reading list this line is drawn on (`DISPATCH_READING_SURFACES`). The
   *  projection reads nothing of it: it names the surface a mount draws, which is how the list's
   *  unit test ties each mount to its row (`surface-lists.test.ts`). */
  readonly surface?: DispatchReadingSurface;
}

/**
 * One line of plain text for a place that can hold no markup - an `<option>`, a `<title>`, an
 * `aria-label` - read by the one reader every rendered body reads (`useMarkdownHeadline`), so
 * what a reader hears of a record and what they see of it are the same words. There is no second
 * stripper: formatting goes and no syntax stays, and every reference - a `dispatch://` address,
 * a dashboard address, a short id, an issue key, and a `#id` where the caller names the text's
 * `source` - reads as the painted line draws it: its target's current name, and the words its
 * author wrote where Dispatch answered that it has none (R8).
 *
 * The projection says which references it read, this names each one (`useReferenceNames`): from
 * what the page already read where a page published it, and otherwise by its own read - and it
 * reads the question map of `source`'s document where the line holds a `#id`, the read a
 * rendered body of the same record makes - and the projection runs again with them: two passes
 * over a line that holds a reference, one over the rest. A name read for a source this one
 * replaced is kept rather than dropped, since the same reference in the next source names the
 * same thing. Where the caller hands over the text's `markdown`, beside its tree or alone, the
 * projection names only what that source cites (`citedReferences`), as a rendered body links only
 * that: the `#q2` an escaped `\#q2` leaves in the words stays those words.
 *
 * The words are `tree`'s, the reading Go made of a stored text, where the caller has one, and
 * the parse of `markdown` for a text Dispatch does not store (`useMarkdownHeadline`): a line
 * read as plain words is read from the same document the surface draws.
 *
 * `text` is `undefined` until the engine has answered, which is one pre-paint commit on a page
 * whose engine is loaded. `reads` draws nothing and the caller mounts it wherever it draws the
 * line.
 */
export function usePlainHeadline(
  tree: DisplayTree | undefined,
  markdown: string | undefined,
  { firstLine = false, issue, max = Number.POSITIVE_INFINITY, source }: PlainHeadlineOptions = {}
): { reads: ReactNode; text: MarkdownHeadline | undefined } {
  // The references the projection last said it read, as one key, which a source the engine has
  // not parsed leaves empty: kept as state, since what names them is read before the projection
  // runs again with their names.
  const [addressKey, setAddressKey] = useState("");
  const addresses = useMemo(() => (addressKey === "" ? [] : addressKey.split("\n")), [addressKey]);
  const { names, reads } = useReferenceNames(addresses, source?.issue ?? issue);
  const [asksQuestions, setAsksQuestions] = useState(false);
  // Asked once the projection says the line holds a `#id`, which is after it mounted
  // (`useReferenceQuery`).
  const questionRead = useReferenceQuery(
    asksQuestions && source !== undefined ? questionsQuery(source) : undefined
  );
  const questions = useMemo(() => questionReferences(questionRead?.data), [questionRead?.data]);
  const cited = useMemo(
    () => (markdown === undefined ? undefined : citedReferences(markdown, questions)),
    [markdown, questions]
  );
  const projects = useProjectKeys();
  const naming = useMemo<ReferenceNaming>(
    () => ({ cited, names, projects, questions }),
    [cited, names, projects, questions]
  );
  const text = useMarkdownHeadline(tree, max, markdown, naming, firstLine);
  const holdsQuestions = text?.questions === true;
  if (holdsQuestions && !asksQuestions) setAsksQuestions(true);
  const projected = (text?.addresses ?? []).join("\n");
  if (projected !== addressKey) setAddressKey(projected);
  return { reads, text };
}

/**
 * `usePlainHeadline` for a caller whose element can hold no children of its own - a checkbox
 * named after the row it marks, a button whose label is its row's subject: the reads sit beside
 * whatever `children` draws rather than inside it.
 */
export function PlainText({
  children,
  firstLine = false,
  markdown,
  max = Number.POSITIVE_INFINITY,
  source,
  surface,
  tree,
}: {
  /** Draws whatever holds the line: the control the label names, the element the title is on.
   *  `undefined` until the engine has answered. */
  children: (text: string | undefined) => ReactNode;
  /** Reads the text's first line alone rather than the whole of it (`useMarkdownHeadline`):
   *  a closed row of the priorities table, which shows the first line of a description and no
   *  more of it, is the one caller that asks. */
  firstLine?: boolean;
  markdown: string | undefined;
  max?: number;
  /** The record the text is (`PlainHeadlineOptions`). */
  source?: TextSource;
  /** The row of the reading list this line is drawn on (`PlainHeadlineOptions`). */
  surface?: DispatchReadingSurface;
  /** The reading Go made of this stored text, whose words the line is; absent for a text
   *  Dispatch does not store, whose `markdown` is parsed instead. */
  tree?: DisplayTree;
}): ReactNode {
  const { reads, text } = usePlainHeadline(tree, markdown, { firstLine, max, source, surface });
  return (
    <>
      {reads}
      {children(text?.text)}
    </>
  );
}
