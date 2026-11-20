import { expect, setSystemTime, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";

import { ApiError, api } from "../../api/client";
import type { ArtifactText } from "../../api/types";
import type { ComposerAnchor } from "../conversation/composer-model";
import { heldSends } from "../conversation/held-sends";
import { KeptDraftOffers } from "../conversation/KeptDraftOffers";
import { MentionComposer } from "../conversation/MentionComposer";
import { unsentDraftsOf } from "../conversation/unsent-drafts";
import { giveWay, type OfferedDraft } from "../conversation/useKeptDrafts";
import { liveTextQueryOptions } from "../doc/live-text-query";
import { MarginKeptDrafts } from "./CommentsTab";
import { marginComposeKeptKey, marginComposeSendKey } from "./margin-context";

// Census row 11.9 of dispatch://INFRA-1523, and the architect's ruling of 2026-11-20 17:41Z: a
// reader selects words to comment on inside another comment's pending insert suggestion, so the
// draft's quote is the words that insert put in the document. The insert's author deletes the
// comment, and the delete's reject takes those words out of the document: they are a deleted
// post's words now. The page reads the document's text again after the delete
// (`eventRefreshTargets`), and each surface that draws a draft's quote - the open composer and the
// two lines that lead a draft kept for other words - checks the quote against that text, draws
// none of the words once it no longer holds them, and keeps the draft's own words and presses.
//
// No keystroke and no collaborator's update reads the text again, so the page's read can be older
// than words the reader has just typed and selected. Only a read newer than the time the draft's
// compose took the words says they left (`useQuoteInDocument`): the open composer's mount, the
// time a kept draft keeps from its compose (`AwayDraft.anchoredAt`), and for a draft an older
// release kept with no time, before every read.

const DOCUMENT = "doc-1";
/** The words the pending insert put in the document, which the reader's selection lies in. */
const QUOTE = "security review";
const BEFORE = ["Ship the release.", "Hold it for the security review."];
/** The document once the delete's reject took the insert's words out. */
const AFTER = ["Ship the release.", "Hold it."];
const OWNER = { issueKey: "CORE-1", kind: "issue" } as const;

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

/** `read` landing in `client`'s cache as the page's read of the document's text, at `at`, or as
 *  the clock reads now where that is not given. */
function land(client: QueryClient, read: ArtifactText, at?: number): void {
  client.setQueryData(liveTextQueryOptions(DOCUMENT).queryKey, read, { updatedAt: at });
}

const noop = () => {};

/** A draft kept for `quote`'s words of the document under the mark `markId`, with `body`, by a
 *  compose that took those words at `anchoredAt`, or by an older release, which kept no time. */
function keptDraft(markId: string, quote: string, body: string, anchoredAt?: number): OfferedDraft {
  return {
    anchor: { artifact: DOCUMENT, mark_id: markId, quote },
    anchoredAt,
    draft: { body, mentions: [], replacement: "" },
    drop: noop,
    note: undefined,
    owner: OWNER,
    replyTo: null,
    take: noop,
  };
}

/** The open composer at `quote`'s words, the offer of a draft kept for them beside it, and the
 *  margin's own list of what its compose kept, over a cache holding the document's text `read`,
 *  or none where it is undefined. `offeredAt` and `awayAt` are when the composes that kept the two
 *  drafts took the words, and `readAt` is when `read` landed. */
function renderSurfaces(
  quote: string,
  read: ArtifactText | undefined,
  { awayAt, offeredAt, readAt }: { awayAt?: number; offeredAt?: number; readAt?: number } = {}
) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  if (read !== undefined) land(client, read, readAt);
  const offered = keptDraft("mark-offered", quote, "Is this the review we agreed on?", offeredAt);
  const away = keptDraft("mark-away", quote, "Name who runs it.", awayAt);
  const view = render(
    <MemoryRouter future={{ v7_relativeSplatPath: true, v7_startTransition: true }}>
      <QueryClientProvider client={client}>
        <MentionComposer
          anchor={{ artifact: DOCUMENT, mark_id: "mark-open", quote }}
          kind="comment"
          onClose={noop}
          onSent={noop}
          owner={OWNER}
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
  // The page's read and the composer's mount at one moment, and the read after the delete a
  // second later, on a clock this row moves itself.
  const start = Date.now();
  setSystemTime(start);
  const { client, view } = renderSurfaces(QUOTE, textRead(BEFORE));
  try {
    await user.click(screen.getByLabelText("Comment"));
    await user.paste("Which review is this?");
    // While the insert stands, its words are the document's, and each surface quotes them.
    expect(timesDrawn(view.container, QUOTE)).toBe(3);

    // The read after the delete's reject lands.
    setSystemTime(start + 1_000);
    act(() => land(client, textRead(AFTER)));

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
    setSystemTime();
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

// The reader, or a collaborator over Yjs, typed the words a minute after the page last read the
// document, and the reader selected them: that read lacks them, and nothing has read the text
// since. It is older than each compose that took the words - the open one, and the one that kept
// a draft for them a second ago - so no surface takes it to say the words left: they are on
// screen.
test("words typed after the page last read the document are drawn though that read lacks them", () => {
  const now = Date.now();
  const { client, view } = renderSurfaces(QUOTE, textRead(AFTER), {
    awayAt: now - 1_000,
    offeredAt: now - 1_000,
    readAt: now - 60_000,
  });
  try {
    expect({
      drawn: timesDrawn(view.container, QUOTE),
      gone: view.container.textContent?.includes("no longer in the document"),
    }).toEqual({ drawn: 3, gone: false });
  } finally {
    view.unmount();
    client.clear();
  }
});

/** The margin's compose at `anchor`, as the margin draws one: under the document's compose send
 *  name, and keyed by its mark, so a compose at other words is a composer of its own. */
function composeAt(client: QueryClient, anchor: ComposerAnchor) {
  return (
    <MemoryRouter future={{ v7_relativeSplatPath: true, v7_startTransition: true }}>
      <QueryClientProvider client={client}>
        <MentionComposer
          anchor={anchor}
          key={anchor.mark_id}
          kind="comment"
          mutationKey={marginComposeSendKey(DOCUMENT)}
          onClose={noop}
          onSent={noop}
          owner={OWNER}
          replyTo={null}
        />
      </QueryClientProvider>
    </MemoryRouter>
  );
}

// The delete's reject takes the words out while the compose at them is open, and the read after
// it lands, newer than the compose's taking them: the composer says they are gone. The reader then
// selects other words, and the newer compose offers the draft the first one kept as it went. That
// draft keeps the time its compose took the words, which the read is newer than, not the time it
// was kept, which is after the read: the offer draws none of the deleted post's words either.
test("after the read that follows a delete's reject, the composer at the words and the draft it keeps as a newer compose opens draw none of them", async () => {
  const user = userEvent.setup();
  const start = Date.now();
  setSystemTime(start);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  land(client, textRead(BEFORE), start - 60_000);
  const view = render(
    composeAt(client, { artifact: DOCUMENT, mark_id: "mark-insert", quote: QUOTE })
  );
  try {
    await user.click(screen.getByLabelText("Comment"));
    await user.paste("Which review is this?");
    expect(timesDrawn(view.container, QUOTE)).toBe(1);

    setSystemTime(start + 1_000);
    act(() => land(client, textRead(AFTER)));
    const gone = timesDrawn(view.container, QUOTE);

    setSystemTime(start + 2_000);
    view.rerender(
      composeAt(client, { artifact: DOCUMENT, mark_id: "mark-release", quote: "Ship the release." })
    );
    expect({
      gone,
      drawn: timesDrawn(view.container, QUOTE),
      offered: screen.getByText("Which review is this?").closest('[role="status"]')?.textContent,
    }).toEqual({
      gone: 0,
      drawn: 0,
      offered:
        "You wrote this draft for other words, which are no longer in the document. It is kept here:Which review is this?",
    });
  } finally {
    setSystemTime();
    view.unmount();
    client.clear();
  }
});

// A reload after the delete: the page mounts with no read of the document, the composer at the
// words, as a send held over the reload draws one, and the drafts the tab kept for them - one with
// the time its compose took the words, before the reload, and one an older release kept with no
// time, which counts as taken before every read. The page's first read lands after all of them,
// and lacks the words: none is drawn.
test("after a reload that follows the delete, a draft kept for the words draws none of them, with its compose's time or with none", () => {
  const start = Date.now();
  setSystemTime(start);
  const { client, view } = renderSurfaces(QUOTE, undefined, { offeredAt: start - 60_000 });
  try {
    setSystemTime(start + 1_000);
    act(() => land(client, textRead(AFTER)));
    expect({
      drawn: timesDrawn(view.container, QUOTE),
      offered: screen.getByTestId("offered").textContent,
      kept: screen.getByTestId("kept").textContent,
    }).toEqual({
      drawn: 0,
      offered:
        "You wrote this draft for other words, which are no longer in the document. It is kept here:Is this the review we agreed on?Use my draftDiscard my draft",
      kept: "You wrote this draft for words that are no longer in the document. It is kept here, and offered in the next compose you open on this document:Name who runs it.Discard my draft",
    });
  } finally {
    setSystemTime();
    view.unmount();
    client.clear();
  }
});

/** Dispatch's refusal of a send whose mark left the document with the words it marked. */
const ANCHOR_MISSING = new ApiError(409, {
  code: "ANCHOR_MISSING",
  error: "anchor mark is not in the document",
});

// The reader presses Send after the composer already says its words are gone, and Dispatch
// refuses the send: its mark went with the words. A newer compose takes its place, and the refused
// send gives way (`giveWay`): its draft is kept from the send, with the time its composer took the
// words, which the send keeps (`ComposerSend.anchoredAt`), not the time Send was pressed, which is
// after the read that lacks them. The newer compose offers the draft, and draws none of the
// deleted post's words.
test("a send pressed after the composer says its words are gone, which Dispatch refuses, keeps a draft that draws none of them", async () => {
  const user = userEvent.setup();
  const start = Date.now();
  setSystemTime(start);
  const createComment = spyOn(api, "createComment").mockRejectedValue(ANCHOR_MISSING);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const store = heldSends(client);
  const sendKey = marginComposeSendKey(DOCUMENT);
  land(client, textRead(BEFORE), start - 60_000);
  let view = render(
    composeAt(client, { artifact: DOCUMENT, mark_id: "mark-insert", quote: QUOTE })
  );
  try {
    await user.click(screen.getByLabelText("Comment"));
    await user.paste("Which review is this?");
    setSystemTime(start + 1_000);
    act(() => land(client, textRead(AFTER)));
    const saidGone =
      screen.queryByText("The words this draft was for are no longer in the document.") !== null;

    setSystemTime(start + 2_000);
    await user.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(store.get(sendKey)?.status).toBe("refused"));
    view.unmount();
    const refused = store.get(sendKey);
    if (refused?.status !== "refused") throw new Error("Dispatch refused no send");
    act(() => giveWay(refused, marginComposeKeptKey(DOCUMENT), store, unsentDraftsOf(client)));

    view = render(
      composeAt(client, { artifact: DOCUMENT, mark_id: "mark-release", quote: "Ship the release." })
    );
    expect({
      saidGone,
      drawn: timesDrawn(view.container, QUOTE),
      offered: screen.getByText("Which review is this?").closest('[role="status"]')?.textContent,
    }).toEqual({
      saidGone: true,
      drawn: 0,
      offered:
        "You wrote this draft for other words, which are no longer in the document. It is kept here:Which review is this?",
    });
  } finally {
    setSystemTime();
    createComment.mockRestore();
    view.unmount();
    client.clear();
  }
});

// The architect's ruling of 2026-11-20 20:03Z on dispatch://INFRA-1523 (round 42's ruling 4): a
// composer that mounts again after the delete's reject may draw the deleted post's words only while
// the page's next read of the document is out, and never once that read has failed. The reader
// leaves the document and comes back: the composer at the words mounts again over the cache, which
// holds the read that followed the reject and lacks them, and the page's next read fails. In
// chromium at 8259e87d the composer drew the inserted words again, 8 s after the page came back
// with every read of the document's text answered 503 (test round at 8259e87d).
test("a composer that mounts again after the delete, while the page's next read of the document fails, draws none of the deleted post's words", async () => {
  const user = userEvent.setup();
  const start = Date.now();
  setSystemTime(start);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  land(client, textRead(BEFORE), start - 60_000);
  const anchor = { artifact: DOCUMENT, mark_id: "mark-insert", quote: QUOTE };
  const first = render(composeAt(client, anchor));
  try {
    await user.click(screen.getByLabelText("Comment"));
    await user.paste("Which review is this?");
    setSystemTime(start + 1_000);
    act(() => land(client, textRead(AFTER)));
    // The positive control: before it mounts again, the composer says the words are gone.
    expect(timesDrawn(first.container, QUOTE)).toBe(0);
  } finally {
    first.unmount();
  }

  setSystemTime(start + 5_000);
  const again = render(composeAt(client, anchor));
  try {
    // The next read of the document fails: the cache keeps the read that lacks the words.
    expect(client.getQueryState(liveTextQueryOptions(DOCUMENT).queryKey)?.dataUpdatedAt).toBe(
      start + 1_000
    );
    expect(timesDrawn(again.container, QUOTE)).toBe(0);
  } finally {
    setSystemTime();
    again.unmount();
    client.clear();
  }
});
