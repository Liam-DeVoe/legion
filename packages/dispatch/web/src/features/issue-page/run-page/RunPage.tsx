import { type ReactNode, useId, useMemo } from "react";
import { Link, useParams } from "react-router-dom";

import type { GraphReferences } from "../../../api/types";
import { pageInkText, pageQuietText } from "../../../theme/classes";
import { MarkdownBody } from "../../refs/MarkdownBody";
import { Timestamp } from "../../refs/Timestamp";
import { NotFoundPage } from "../../shell/NotFoundPage";
import { useDocumentTitle } from "../../shell/useDocumentTitle";
import { DetailsErrorLine } from "../details-error";
import { NoTooltips } from "../NoTooltips";
import { authorText, pageChrome } from "../page-structure";
import {
  ANSWER_LINES,
  CONTROL_LABELS,
  EMPTY_STATE_LINES,
  NODE_REFERENCES_WORDS,
  NODE_TYPE_NAMES,
  RUN_PAGE_WORDS,
  STATE_LINES,
  sentenceCase,
} from "../page-words";
import { ROW_LINK } from "../row-references";
import { type RunPart, type RunWords, runView, useRun } from "./run-page";

/** The words the run's view is drawn in, from the page's one list. */
const WORDS: RunWords = { EMPTY_STATE_LINES, NODE_REFERENCES_WORDS, NODE_TYPE_NAMES, STATE_LINES };

/** The page's heading: the word for what a run is. */
const RUN_WORD = sentenceCase(NODE_REFERENCES_WORDS.kinds.interaction);

/** The look of a section's heading, as Details draws its sections' names. */
const SECTION_HEADING = `text-xs leading-4 font-semibold tracking-[0.08em] uppercase ${pageQuietText}`;

/** A link the page draws inside the run's own words. */
const LINK = `dispatch-inline-link ${ROW_LINK} ${pageInkText}`;

/**
 * A command bar run's own page at `/runs/<id>` (dispatch://INFRA-4064 R3): the asker's question,
 * the answer frame by frame, and what the run changed. The server answers a run only to the
 * person who asked it, so an id that names no run, and a run the reader may not open, draw the
 * app's not-found page and nothing of this one.
 */
export function RunPage(): ReactNode {
  const { id } = useParams();
  const { notFound, query } = useRun(id);
  useDocumentTitle(`${RUN_WORD} · Dispatch`, !notFound);
  if (notFound) {
    return <NotFoundPage />;
  }
  return (
    <div data-issue-page="">
      <NoTooltips>
        {query.isError ? (
          <DetailsErrorLine
            error={query.error}
            message={ANSWER_LINES.run_unreadable}
            onRetry={() => void query.refetch()}
            retrying={query.isFetching}
          />
        ) : null}
        {query.data !== undefined ? (
          <RunAnswer answer={query.data} />
        ) : query.isError ? null : (
          <p className={`text-sm ${pageQuietText}`} {...pageChrome}>
            {ANSWER_LINES.loading_older}
          </p>
        )}
      </NoTooltips>
    </div>
  );
}

/** The run as its read answered it. */
function RunAnswer({ answer }: { answer: GraphReferences }): ReactNode {
  const view = useMemo(() => runView(answer, WORDS), [answer]);
  const questionHeading = useId();
  const changedHeading = useId();
  return (
    <article className={`flex min-w-0 max-w-3xl flex-col gap-6 ${pageInkText}`}>
      <header className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h1 className="text-xl font-semibold" {...pageChrome}>
          {RUN_WORD}
        </h1>
        {view.at === null ? null : (
          <Timestamp at={view.at} className={`text-xs ${pageQuietText}`} />
        )}
      </header>
      <section aria-labelledby={questionHeading} className="flex min-w-0 flex-col gap-1.5">
        <h2 className={SECTION_HEADING} id={questionHeading} {...pageChrome}>
          {RUN_PAGE_WORDS.question}
        </h2>
        {/* The question as the asker typed it: their characters, never read as markdown. */}
        <p className="whitespace-pre-wrap [overflow-wrap:anywhere]" {...authorText}>
          {view.question}
        </p>
      </section>
      <div className="flex min-w-0 flex-col gap-3">
        {view.parts.map((part, index) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: a part is its place in the answer, which its frames' order fixes.
          <RunAnswerPart key={index} part={part} />
        ))}
      </div>
      <section aria-labelledby={changedHeading} className="flex min-w-0 flex-col gap-1.5">
        <h2 className={SECTION_HEADING} id={changedHeading} {...pageChrome}>
          {RUN_PAGE_WORDS.what_changed}
        </h2>
        {view.changed.length === 0 ? (
          <p className={`text-sm ${pageQuietText}`} {...pageChrome}>
            {CONTROL_LABELS.none}
          </p>
        ) : (
          <ul className="flex min-w-0 flex-col gap-0.5">
            {view.changed.map((node) => {
              // A node is named by its first line, beside its kind's word; a node whose read
              // carries no words is named by the word alone, which is then its link.
              const named = node.line !== "";
              const text = named ? node.line : node.kind;
              return (
                <li className="flex min-w-0 items-baseline gap-2 text-sm" key={node.key}>
                  {named ? (
                    <span className={`shrink-0 text-xs ${pageQuietText}`} {...pageChrome}>
                      {node.kind}
                    </span>
                  ) : null}
                  {node.href === undefined ? (
                    <span className="min-w-0 truncate" {...(named ? authorText : pageChrome)}>
                      {text}
                    </span>
                  ) : (
                    <Link
                      className={`min-w-0 truncate ${LINK}`}
                      to={node.href}
                      {...(named ? authorText : pageChrome)}
                    >
                      {text}
                    </Link>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </section>
    </article>
  );
}

/** One part of the answer: the model's text through the one markdown renderer, a thing it cites
 *  as a link named by its title, or a line the page says of the answer. */
function RunAnswerPart({ part }: { part: RunPart }): ReactNode {
  switch (part.kind) {
    case "answer":
      return (
        <div {...authorText}>
          <MarkdownBody markdown={part.markdown} softBreaks="line" />
        </div>
      );
    case "item":
      return (
        <p className="text-sm">
          {part.href.startsWith("/") ? (
            <Link className={LINK} to={part.href} {...authorText}>
              {part.title}
            </Link>
          ) : (
            <a className={LINK} href={part.href} {...authorText}>
              {part.title}
            </a>
          )}
        </p>
      );
    case "line":
      return (
        <p className={`text-sm ${pageQuietText}`} {...pageChrome}>
          {part.text}
        </p>
      );
  }
}
