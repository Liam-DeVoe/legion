# Ask card demo

A live demo of Rewrite, Define and Quote reply on the ask card: the Dispatch web app, run by
Vite, with a stubbed back end in the browser. It is a prototype for the owner's approval, not
part of the browser suite, and not for merge.

Every word of the asks, the documents, the comments, the messages, the rewrites and the
definitions is invented. The fixtures are in the repository, and a note in the bottom right
corner of every page says that the back end is stubbed and the text invented.

## What it shows

- The card's split button: Answer, a thin rule, and a chevron that opens one menu with Rewrite
  (`r`), Ask back (`b`), Hand this question to… (`a`), Decline to answer (`n`) and Define
  selected words (`d`). An approval request's menu holds Ask back, Hand this question to and
  Define.
- One badge for a rewrite: **Rewriting…**, **Rewritten** (press it, or `w`, for the original
  wording in place; it then reads **Original**), and **Rewrite failed** (press it, or `r`, to
  try again; why it failed is the badge's tooltip and nowhere else). The ask Dispatch's check
  rewrote before the reader saw it wears the same badge.
- A rewritten decision block in a document: the block's header carries the badge, and the badge
  or `w` shows the agent's question and options in the block in place of the document's text,
  which still holds the rewrite.
- The same badge on a comment the reviewer rewrote, beside its author and time on the issue's
  Conversation tab (the badge, or `w` with the comment in focus, shows the agent's wording), and
  beside the title of an issue it rewrote as its agent opened it (the badge shows the agent's
  title and spec in place).
- The chevron beside the asking agent's name opens a popover: the session's handles to copy, and
  under **Reaches N** every session a person's answer and replies reach, each with its Unfollow
  or Unsubscribe.
- A question handed from one person to another names, beside whom it waits on, who handed it on
  and when.
- Define: select words in an ask's question or options and press `d` (or the menu item). A small
  popover beside the words says what they mean, from the issue's record, with a link to where
  the documents define them; the asking agent's own words join it under the agent's name.
- Define in a document: the selection bar of a document's read view (the approval document in
  the Inbox's reading pane) and of its edit view (an open issue's spec) offers Define. In the read
  view `d` defines the selection. In the edit view, where letters type, the key is **Ctrl+Alt+Y**,
  **Cmd+Alt+Y** on a Mac: neither the browser, macOS nor the app uses it (Cmd+Alt+D shows and
  hides the Dock, and Cmd+Alt+I, J, C, U, K and E open the browsers' developer tools).
- Quote reply: select words of an ask, a comment, a reply or a message that stands beside a box
  that answers it, and a dark selection bar over the words offers **Quote reply** `q` and, over
  an open ask's words, **Define** `d`, each with its key beside its name. `q` is free in every
  scope the bar shows in (Ask card, Issue, Inbox, Document, Agents and Global); `r`, GitHub's
  key for it, is the card's Rewrite. Quote reply opens the reply box of that same thread, or
  takes it where it is open, and the quote goes into it:
  - an open ask's question, its options and the replies in its thread: the ask's answer box (a
    compact card opens it from behind **Add a note**);
  - an answered ask's record and the replies under it: the reply box of its thread (on an issue's
    Conversation tab, **Write a reply** opens);
  - a comment thread, on an issue's Conversation tab, in a document's margin or read in the
    Inbox: the thread's reply box;
  - a message of an issue's Conversation: the composer at the top, replying to that message; a
    message thread read in the Inbox: that thread's reply box;
  - a message on the Agents page, and an agent's thread read in the Inbox: the row's own
    composer, replying to that message.
- What the quote holds: the Markdown that draws the selected words, read from the elements the
  page draws them with, as GitHub's Quote reply reads the selected HTML. Bold, italics,
  strikethrough, code spans, code blocks, links, pictures, headings, quotes, list items and table
  rows keep their marks; every other character is escaped where Markdown would read it as
  formatting, so `__tests__` and `*.ts` post as they read. Each block the page draws (a
  paragraph, an option's label, a list item, a code block) is its own quoted block: each of its
  lines after `> `, a bare `>` between two blocks, then a blank line, and the cursor waits on the
  empty line under it. Text the box already held stays, and the quote goes after it and a blank
  line; the box grows to show the quote. GitHub's own library, `@github/quote-selection`, is not
  used: its latest release ships no code and the one before leaves text unescaped
  (`web/src/features/threads/selection-markdown.ts`).
- A quote's lines are not the reader's words. Every check of what a box holds reads the lines
  that do not start with `>`: a box holding a quote alone holds no answer, no reason, no question
  back, no reply and no message, and an answer is taken for a question (**Ask back instead**) by
  the reader's own lines, never by a question the quote ends in. What is sent is the whole box,
  quote and all.
- Where the bar does not show: over a thread that takes no reply now (a decided suggestion's, a
  resolved ask, an agent that takes no messages), while a send of that thread is on its way, and
  over any text inside a document, a decision block's included: a document's own selection bar
  is the one drawn there. On a touch screen the bar docks at the foot of the screen, clear of the
  browser's own selection menu, as the document's does.
- The shortcut help (`?`) lists the card's keys under **Ask card**, `q` among them, and the `q`
  for comments, replies and messages under the page's own section (**Issue**, **Inbox**,
  **Document** or **Agents**).

The fixtures to try it on (project PANE, signed in as `alice`):

- PANE-5 "R3 flip audit: go with B per the train?": Rewrite goes through; Define on "flip
  audit", "the train" or "refused lease" answers from the record and then from the agent.
- PANE-3 "Which service moves first?": the first Rewrite fails, as one that changed an option's
  label would; the second goes through.
- PANE-2: the ask Dispatch's check rewrote before it reached the Inbox. Select from "The R3
  audit decides" to the end of its recommendation and press `q`: the two paragraphs go into its
  answer box as a quote, the recommendation's bold kept.
- PANE-2's Conversation tab (`/issues/PANE-2/conversation`): a question alice answered, with the
  lease-tool implementer's reply under the answer. Quote reply on the reply's list opens
  **Write a reply** and quotes its two items and the bold line there.
- PANE-3's Conversation tab (`/issues/PANE-3/conversation`): a message from the rollout
  architect, whose words Quote reply answers from the composer at the top.
- The Agents page (`/agents`): open the lease-mirror reviewer's row for a question alice sent it
  and its reply, whose words Quote reply answers from the row's composer.
- PANE-1: an approval request; its document reads in the Inbox's reading pane, and its card is on
  the issue's Conversation tab.
- PANE-6: a decision block in the issue's spec that the check rewrote (`/issues/PANE-6/spec`).
- PANE-4 "Can the hand-back batch hold 500 leases…": dana handed it on to bruno, so it shows in
  the Inbox under Assignee: everyone (`/?assignee=everyone`).
- PANE-5's Conversation tab: a comment of the lease-mirror implementer the reviewer rewrote;
  Expand thread shows its reply box, where Quote reply puts the comment's words.
- PANE-7: an issue the lease-tool implementer opened, whose title and spec the reviewer
  rewrote (`/issues/PANE-7`).

## Files

- `web/vite.demo.config.ts`: Dispatch's Vite config with the stub put into the page before the
  app's entry (`web/src/demo/install.ts`), so no app file imports demo code. The dev server
  proxies nothing.
- `web/src/demo/`: the stub.
  - `install.ts` replaces `fetch` and `WebSocket`, and records any request no route answers in
    `window.__dispatchDemoUnanswered`.
  - `clock.ts` fixes the page's clock at the fixtures' moment (`DEMO_CLOCK`), so every age reads
    the same whenever the demo is opened.
  - `fixtures.ts` holds the project, the issues, the asks, the rewrites and the definitions;
    `document-fixtures.ts` holds each issue's documents.
  - `server.ts` answers the Inbox, the ask routes, Rewrite (`POST /api/v1/asks/{id}/rewrite`)
    and Define (`POST /api/v1/definitions`, `GET /api/v1/definitions/{id}`); `documents.ts`
    answers an issue page and its documents; `http.ts` holds the routes' shapes.
  - `live.ts` is `/ws/live` (the event feed) and hands a document's socket to `document-room.ts`,
    which builds each document from its markdown and acknowledges every edit.
  - `banner.ts` is the corner note; `file-router.ts` is the single file's router.
- `e2e/ask-card-demo/shots.ts` is the screenshot run; `single-file.ts` builds the single file.

Writes change the stub's state, which the tab keeps in sessionStorage across a reload; a new tab
starts from the fixtures again.

## Run the demo

```sh
bun install
cd packages/dispatch/web
bunx vite --config vite.demo.config.ts --port 5191 --host 127.0.0.1
```

Open `http://127.0.0.1:5191/`. `/?item=<ask id>` opens one question in the reading pane, with the
ids in `web/src/demo/fixtures.ts`.

## Take the pictures

With the demo running:

```sh
cd packages/dispatch
bun e2e/ask-card-demo/shots.ts --base http://127.0.0.1:5191 --out "$TMPDIR/ask-card-shots" \
  --chromium <path to Chromium> [--only <name part>,…]
```

It writes one PNG per picture, `ask-card-NN-<what>.png`, at 1440x900 and the phone ones at
390x844, and `report-design.json`. The run fails if a picture did not reach its state in time,
the stub left a request unanswered, the page threw, or a check failed.

The app's font is Inter. A machine without it draws the pictures in a fallback font; to draw them
as the app is designed, put Inter's TTF files in a directory and point Chromium's fontconfig at it
with `FONTCONFIG_FILE` (a `fonts.conf` that includes `/etc/fonts/fonts.conf` and adds that
directory).

## Build the single file

```sh
cd packages/dispatch
bun e2e/ask-card-demo/single-file.ts --out "$TMPDIR/ask-card-demo.html"
bun e2e/ask-card-demo/shots.ts --file --base "file://$TMPDIR/ask-card-demo.html" \
  --out "$TMPDIR/ask-card-shots" --chromium <path to Chromium>
```

The first command makes one Vite build (`DISPATCH_DEMO_SINGLE_FILE=1`: one script with every
asset inlined) and writes the script, the stylesheet and the icon into the page, which then opens
from the disk in Chrome with no network. No service worker runs at a `file:` address, so the stub
replaces `fetch` and `WebSocket` in the page, and the router reads the address's fragment
(`#/issues/PANE-5/spec`).

The second command opens the file with the network off and checks that it opens on the Inbox,
that the menu lists its five actions, that a rewrite and Define answer, that an issue's spec
opens by its fragment address, and that Quote reply puts an ask's selected words in its answer
box as Markdown.

## What the stub does differently from the server the design describes

- A rewrite answers its POST when it ends, about 2.4 seconds later, with the ask as the rewrite
  left it. The server runs it behind the write and writes an `ask.edited` event the page reads.
- The rewritten wording and each definition are the fixtures' own, not a model's.
- The asking agent's answer to a definition joins it 3.5 seconds after the request; the server
  sends the running session a side question and adds its answer when it comes.
- The `rewrite` field on an ask, a comment and an issue (running, done or failed, who asked, the
  original wording) is the prototype's own; no Dispatch contract holds it yet.
- The rewritten comment and the rewritten issue come already rewritten, as the check's rewrite
  of the decision block does; the server's check reads the text after the write and edits it.
- Document edits are acknowledged and kept for the tab only, and every past version of a
  document reads as its current text.
- The rewritten decision block comes already rewritten: its spec holds the rewrite as its live
  text. The server's rewrite of a block writes that new version of the document itself.
- An Unsubscribe on the Reaches list lasts until the page reloads, and an Unfollow for the tab;
  the server cuts the route and tells the session.
- A reply sent from the Conversation's composer to a message (`POST
  /api/v1/issues/{key}/messages`) and a message sent to a session from the Agents page (`POST
  /api/v1/agents/{session}/messages`) are answered and kept nowhere, as a comment that answers
  no ask is; the server stores each, so the thread shows it. A reply under an ask is kept for
  the tab.
