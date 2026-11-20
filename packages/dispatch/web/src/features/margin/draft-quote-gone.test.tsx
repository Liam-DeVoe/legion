import { expect, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";

import type { ArtifactText } from "../../api/types";
import { KeptDraftOffers } from "../conversation/KeptDraftOffers";
import { MentionComposer } from "../conversation/MentionComposer";
import type { OfferedDraft } from "../conversation/useKeptDrafts";
import { liveTextQueryOptions } from "../doc/ProofDocument";
import { MarginKeptDrafts } from "./CommentsTab";

// Census row 11.9 of dispatch://INFRA-1523, and the architect's ruling of 2026-11-20 17:41Z: a
// reader selects words to comment on inside another comment's pending insert suggestion, so the
// draft's quote is the words that insert put in the document. The insert's author deletes the
// comment, and the delete's reject takes those words out of the document: they are a deleted
// post's words now. The page reads the document's text again after the delete
// (`eventRefreshTargets`), and each surface that draws a draft's quote - the open composer and the
// two lines that lead a draft kept for other words - checks the quote against that text, draws
// none of the words once it no longer holds them, and keeps the draft's own words and presses.

const DOCUMENT = "doc-1";
/** The words the pending insert put in the document, which the reader's selection lies in. */
const QUOTE = "security review";
const BEFORE = ["Ship the release.", "Hold it for the security review."];
/** The document once the delete's reject took the insert's words out. */
const AFTER = ["Ship the release.", "Hold it."];

/** The document's text read (`GET /api/v1/artifacts/{id}/text`) holding `paragraphs`, with the
 *  tree the page reads its words from. */
function textRead(paragraphs: readonly string[]): ArtifactText {
  return {
    markdown: `${paragraphs.join("\n\n")}\n`,
    markdown_tree: {
      content: paragraphs.map((text) => ({ content: [{ text, type: "text" }], type: "paragraph" })),
      type: "doc",
    },
    token: "token-1",
    version: 1,
  };
}

const noop = () => {};

/** A draft kept for `quote`'s words of the document, with `body`. */
function keptDraft(quote: string, body: string): OfferedDraft {
  return {
    anchor: { artifact: DOCUMENT, mark_id: `mark-${body.length}`, quote },
    draft: { body, mentions: [], replacement: "" },
    drop: noop,
    note: undefined,
    owner: { issueKey: "CORE-1", kind: "issue" },
    replyTo: null,
    take: noop,
  };
}

/** The open composer at `quote`'s words, the offer of a draft kept for them beside it, and the
 *  margin's own list of what its compose kept, over a cache holding the document's text `read`,
 *  or none where it is undefined. */
function renderSurfaces(quote: string, read: ArtifactText | undefined) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  if (read !== undefined) client.setQueryData(liveTextQueryOptions(DOCUMENT).queryKey, read);
  const offered = keptDraft(quote, "Is this the review we agreed on?");
  const away = keptDraft(quote, "Name who runs it.");
  const view = render(
    <MemoryRouter future={{ v7_relativeSplatPath: true, v7_startTransition: true }}>
      <QueryClientProvider client={client}>
        <MentionComposer
          anchor={{ artifact: DOCUMENT, mark_id: "mark-open", quote }}
          kind="comment"
          onClose={noop}
          onSent={noop}
          owner={{ issueKey: "CORE-1", kind: "issue" }}
          replyTo={null}
        />
        <div data-testid="offered">
          <KeptDraftOffers
            changed={undefined}
            gone={[]}
            goneTakes="nowhere"
            kind="comment"
            offered={offered}
            onDiscardChanged={noop}
            onDropGone={noop}
            onDropOffered={noop}
            onTakeChanged={noop}
            onTakeGone={noop}
            onTakeOffered={noop}
          />
        </div>
        <div data-testid="kept">
          <MarginKeptDrafts drafts={[away]} hidden={false} onDiscard={noop} />
        </div>
      </QueryClientProvider>
    </MemoryRouter>
  );
  return { client, view };
}

/** How many times `words` stand in the page. */
const timesDrawn = (container: HTMLElement, words: string) =>
  (container.textContent ?? "").split(words).length - 1;

test("a draft quoting a deleted post's inserted words draws none of them once the document's text no longer holds them, and keeps its own words", async () => {
  const user = userEvent.setup();
  const { client, view } = renderSurfaces(QUOTE, textRead(BEFORE));
  try {
    await user.click(screen.getByLabelText("Comment"));
    await user.paste("Which review is this?");
    // While the insert stands, its words are the document's, and each surface quotes them.
    expect(timesDrawn(view.container, QUOTE)).toBe(3);

    // The read after the delete's reject lands.
    act(() => {
      client.setQueryData(liveTextQueryOptions(DOCUMENT).queryKey, textRead(AFTER));
    });

    const composer = screen.getByRole("form", { name: "Comment composer" });
    expect({
      drawn: timesDrawn(view.container, QUOTE),
      composer:
        within(composer).queryByText(
          "The words this draft was for are no longer in the document."
        ) !== null,
      field: within(composer).getByLabelText<HTMLTextAreaElement>("Comment").value,
      offered: screen.getByTestId("offered").textContent,
      kept: screen.getByTestId("kept").textContent,
    }).toEqual({
      drawn: 0,
      composer: true,
      field: "Which review is this?",
      offered:
        "You wrote this draft for other words, which are no longer in the document. It is kept here:Is this the review we agreed on?Use my draftDiscard my draft",
      kept: "You wrote this draft for words that are no longer in the document. It is kept here, and offered in the next compose you open on this document:Name who runs it.Discard my draft",
    });
  } finally {
    view.unmount();
    client.clear();
  }
});

// The control: words the document still holds are drawn, a selection across two blocks among
// them, which the editor names with a line break where the document's words hold a space; and so
// are they while the page holds no text of the document at all.
test("a draft's words the document still holds are drawn, across a block break too, and so are they before the page reads the document", () => {
  const across = "release.\nHold it";
  const held = renderSurfaces(across, textRead(AFTER));
  try {
    expect(timesDrawn(held.view.container, across)).toBe(3);
    expect(held.view.container.textContent).not.toContain("no longer in the document");
  } finally {
    held.view.unmount();
    held.client.clear();
  }
  const unread = renderSurfaces(QUOTE, undefined);
  try {
    expect(timesDrawn(unread.view.container, QUOTE)).toBe(3);
  } finally {
    unread.view.unmount();
    unread.client.clear();
  }
});
