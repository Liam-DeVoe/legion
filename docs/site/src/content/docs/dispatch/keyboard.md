---
title: Keyboard shortcuts
description: Every keyboard shortcut in Dispatch, by page, plus the ? help, the search palette, the menus, and how to change a key.
sidebar:
  order: 5
---

Dispatch can be driven from the keyboard. Press `?` on any page to see the shortcuts that page
offers.

`Ctrl` below means `⌘` on a Mac, and `Alt` means `⌥` (Option). A shortcut written `g i` is a
sequence: press `g`, let go, then press `i` within a second. While you wait, the bottom right
corner shows the keys pressed so far, and under them each key that can come next, with what it
does.

While you type in a text field, or while the focus is in an open menu, list or panel, only
`Ctrl+K`, `Escape`, and in a document `Ctrl+Alt+M` and `Alt+Enter`, work as shortcuts. So single
letters never interrupt your typing.

You can change each key, give an action a second key, or take its keys away. See
[Change the keys](#change-the-keys).

## The help and the palette

![The Keyboard shortcuts help, grouped by page](/legion/media/dispatch/shortcuts.png)

`?` opens **Keyboard shortcuts**: every shortcut the current page offers, grouped by where it
applies. Type in its filter box to narrow the list. A shortcut that would do nothing right now,
such as one that needs a focused row, is greyed out. **Change these keys in Settings**, at its
foot, opens [Change the keys](#change-the-keys).

`Ctrl+K` opens the palette. With an empty box it lists the actions for the page you are on, each
with the keys that also run it. Type to filter those actions and to search all of Dispatch. Use
the arrow keys and `Enter` to pick a row. Some actions have no key, because a single key would be
too easy to press by mistake. Run them from the palette: **Close issue**, **Reopen issue**, and
**Set priority P0** to **P3**.

Other actions have no key either: **Go to Priorities**, **Go to Delivery** and **Go to Pipeline**
on every page, and in the Inbox the actions for the selected row and the ticked rows. These
include **Assign to me**, **Hand on**, **Un-snooze**, **Mark read**, **Decline**, **Ask back**,
**Change answer**, **Skip to the answer**, **Next change**, **Select all**, and
**Dismiss, or Move to Inbox, the selected items**.

You can give each of these actions a key in [Change the keys](#change-the-keys).

`/` opens the palette for search alone, with your last search selected so that typing replaces
it. `g p` opens it as a project list.

In the palette, `Ctrl+Enter` (`⌘+Enter` on a Mac) opens the highlighted row in a new tab: a search
result of any kind (an issue, a document, a comment, an ask or a message), a project or a recent
page. A `Ctrl`+click or a `⌘`+click opens the row that you click in a new tab. The tab you are on
stays as it was, and the palette stays open on the same words. An action row has no page. A row
that makes an issue from your words, or sends them to an agent, has no page either. On these rows,
`Ctrl+Enter` and a `Ctrl`+click do nothing. The palette's foot shows this key with its other keys.

## Global

These work on every page.

| Keys | Action |
| --- | --- |
| `Ctrl+K` | Search and actions |
| `/` | Search only |
| `?` | Keyboard shortcuts |
| `c` | Create issue |
| `g i` | Go to Inbox |
| `i` | Open the Inbox as a drawer over the current page (not offered on the Inbox page itself) |
| `g a` | Go to Agents |
| `g s` | Go to Settings |
| `g d` | Go to the current project's Documents (on a project or issue page) |
| `g p` | Go to project… |
| `Shift+S` | Toggle the sidebar (wide screens) |
| `Shift+M` | Toggle the margin (wide screens, on a page with a document) |
| `y` on a focused link | Copy the link's address: the `dispatch://` reference of a link to a Dispatch item, else its URL |

`y` on a focused link also works in every dialog, the Inbox drawer and the margin's sheet included,
and it comes before the page's own `y`. In a document you edit, `y` types the letter.

## Inbox

From 1280 px the Inbox page is two columns, and these keys act on the **selected** row: the one
the right column is showing. Below that width, and in the Inbox drawer, they act on the
**focused** row.

| Keys | Action |
| --- | --- |
| `j` / `k` | Next / previous row |
| `↓` / `↑` | Next / previous row, while the focus is in the list (below 1280 px, while a row is focused) |
| `Space` / `Shift+Space` | Scroll the right column down / up by most of its height, while the focus is in the list or on nothing. On a focused button, link, checkbox or menu control, `Space` presses that control. |
| `g g` / `Shift+G` | Scroll the right column to its top / end, while the focus is in the list or on nothing |
| `l` | Move the focus into the reading pane, which the arrow keys, Page Up, Page Down, Space, Home and End then scroll (nothing below 1280 px, in the drawer, or with no row selected) |
| `Enter` | Put the cursor in the answer box of the selected ask, from its row, with the focus on nothing, from the right column after `l`, or from an option of its card. A decision block opens its note field first. |
| `1` – `9`, `0` | Pick that option of the selected ask. `0` is the tenth option. The digit after the last option picks **Other**. |
| `a` | Approve the version that the selected approval request shows |
| `Shift+R` | Request changes on the selected approval request: open the box for your reason, with the cursor in it |
| `r` | Put the cursor in the reply box of the selected thread |
| `b` or `h` | Open the snooze menu of the selected ask, or the selection bar's snooze menu while asks are ticked |
| `p` | Open the priority menu of the selected ask's issue |
| `e` | Dismiss the selected thread (**Move to Inbox** in the Dismissed view) |
| `x` | Tick or untick the selected row |
| `o` | Open the row's issue, document or thread |
| `Shift+O` | Open the row's issue, document or thread in a new tab |
| `Shift+F` | Open the filter panel |
| `Shift+V` | Open the display menu |
| `y` | Copy the selected row's reference |
| `f` | Move the focus to the **Search asks** box above the list |
| `Escape` | From the search box: to the selected row, or to the first row when none is selected. From the right column: back to the row you pressed `l` on, else the row it is showing. From a row: off it. With no row focused: drop every tick. |

`Space`, `Shift+Space`, `g g` and `Shift+G` scroll only the right column. The focus and the
selection stay where they are. They do nothing below 1280 px and in the drawer.

In the right column, `a` and `Shift+R` do nothing until the approval request's document has
loaded, and nothing while the focus is in an ask's card there. In the Request changes box,
`Ctrl+Enter` sends the request, and `Escape` closes the box and sends nothing.

`Enter`, the digits, `b`, `h` and `p` are an ask's keys: a thread row has no options, no answer
box, no snooze and no priority, so they do nothing on one. While asks are ticked, `b` and `h` open
the selection bar's snooze menu, also from a thread row. `r` and `e` are a thread row's keys. An
agent-message row has no `dispatch://` reference, so `y` does nothing on one either.

After you snooze, dismiss, approve, request changes on or answer a row, the row after it is
selected, and it takes the keyboard focus. If no row comes after it, the row before it is
selected. If no row is left, the focus goes to the list's empty message, and the Inbox's keys still
work. For ticked rows, the row after the last of them is selected. A dismiss moves on at once. A
snooze, an approval, a request for changes and an answer move on when Dispatch has saved them.

Each control at the head of the right column shows its first key, such as `p` on the priority and
`b` on **Snooze**.

## Menus and lists

The **Snooze**, priority and **Status** controls open a menu. A key opens the same menu as a
click: `b` or `h` for **Snooze** in the Inbox, `p` for the priority, and `s` for the status on the
issue page.

| Keys | Action |
| --- | --- |
| `↓` / `↑`, `Home` / `End` | Move the highlight. Nothing changes yet. |
| `Enter` | Choose the highlighted choice |
| `1` – `4` | In the snooze menu: **Later today**, **Tomorrow**, **Next week**, **Until I clear it** |
| `0` | In the snooze menu of a snoozed ask: **Un-snooze** |
| `0` – `3` | In the priority menu: **P0** – **P3** |
| `Escape`, `Tab` | Close the menu. Nothing changes. |

The snooze menu opens on its first choice, and the priority and status menus on the current
value. A click on a choice chooses it. A click outside the menu closes it, and nothing changes.
When the menu closes, the focus goes back to where it was before the menu opened. If nothing had
the focus, or you opened the menu with a click, the focus goes to the menu's control. The foot of
each menu shows its keys.

The issue page's assignee and repository controls, and an ask's **Asked of** control, open a list
with a search box. On one of these controls, an arrow key, `Home`, `End`, `Page Up` or `Page Down`
opens the list and moves its highlight, and a letter opens the list with that letter in its search
box. Neither changes the value. `Enter` or a click on a choice changes it. `Escape`, `Tab` and a
click outside close the list, and nothing changes.

## Panels

`Shift+F` opens a page's filter panel, and `Shift+V` opens its display menu. The focus goes to the
first control inside. On a panel that is already open, the key moves the focus into it and does
not close it. In the panel, `Tab` moves between the controls. On a field, `Enter` opens its list:
type to narrow the list, use the arrow keys to move, and press `Enter` to turn a value on or off.
The arrow keys move between the choices of a group and select one. `Escape` closes the open list,
then the panel. The focus then goes back to where it was before you pressed the key.

## Project

| Keys | Action |
| --- | --- |
| `v` | Toggle List / Board (on the Issues tab) |

On the List table, `j` and `k` follow the current sorted, filtered page, skip collapsed groups, and scroll virtual rows into view:

| Keys | Action |
| --- | --- |
| `j` / `k` | Next / previous issue |
| `o` | Open issue |
| `Enter` | Open the focused issue (from the row) |
| `Shift+O` | Open the focused issue in a new tab |
| `x` | Select or deselect the focused issue |
| `Shift+F` | Open the filter panel |
| `Shift+V` | Open the display menu |

`j`, `k`, `o`, `Enter` and `Shift+O` work the same way on the tables of the Priorities page and of
a priority's page. On the Priorities page, `Shift+V` opens the display menu. On a priority's page,
`Shift+F` opens the filter panel and `Shift+V` opens the display menu.

On the project's Documents tab, the list of documents takes the same row keys:

| Keys | Action |
| --- | --- |
| `j` / `k` | Next / previous document |
| `o` | Open the focused document |
| `Enter` | Open the focused document (from the row) |
| `Shift+O` | Open the focused document in a new tab |

## Board

| Keys | Action |
| --- | --- |
| `j` / `k` | Next / previous card |
| `l` / `h` | Next / previous column |
| `↓` / `↑` | Next / previous card, while one is focused |
| `→` / `←` | Next / previous column, while one is focused |
| `Shift+J` / `Shift+K` | Move card down / up |
| `Shift+L` / `Shift+H` | Move card to the next / previous status |
| `o` | Open issue |
| `Enter` | Open the focused card's issue (from the card) |
| `Shift+O` | Open the focused card's issue in a new tab |
| `p` | Open the card's priority menu |
| `y` | Copy the card's issue reference |
| `Shift+Y` | Copy the card's issue key |
| `Escape` | Back to the card, then off it |
| `Shift+F` | Open the filter panel |

## Architecture

| Keys | Action |
| --- | --- |
| `j` / `k` | Next / previous component |
| `↓` / `↑` | Next / previous component, while one is focused |
| `Enter` or `o` | Open the focused component: its children, else its details |
| `l` or `→` | Into the focused component's children |
| `h` or `←` | Up one level |
| `Escape` | Back to the component row, then off it |
| `Shift+O` | Open the focused component's own link in a new tab |

## Delivery

On the Delivery page's list:

| Keys | Action |
| --- | --- |
| `↓` / `↑` | Next / previous pull request, while a row is focused |
| `o` or `Enter` | Open the focused pull request's details |
| `Shift+F` | Open the filter panel, in the List and in the Timeline |

## Issue

| Keys | Action |
| --- | --- |
| `s` | Open the status menu |
| `p` | Open the priority menu |
| `0` – `3` | Set priority P0–P3 |
| `l` | Edit labels |
| `e` | Edit the title |
| `Shift+P` | Pin or unpin the issue |
| `a` | Approve the spec, while its approval request waits on you |
| `Shift+R` | Request changes on the spec, while its approval request waits on you |
| `t s` | Spec tab |
| `t k` | Verification tab |
| `t p` | Plan tab |
| `t e` | Steps tab |
| `t c` | Conversation tab |
| `t h` | Children tab |
| `t a` | Artifacts tab |
| `t v` | Activity tab |
| `t d` | Decisions tab |
| `t o` | Collaborators tab |
| `]` / `[` | Next / previous tab. After the last tab `]` goes to the first, and before the first `[` goes to the last. |

`0` – `3` and `Shift+P` do nothing while focus is in an ask's card or on a checkbox or option,
where the key belongs to that control. `a` and `Shift+R` also do nothing on a turn of the
Conversation tab, in an ask's card, or in a text field.

On a keyboard layout where `[` and `]` need AltGr or Option (`⌥`), German and French among them,
the key press can carry `Alt`. Then `[` and `]` may not work. Give the two actions other keys in
[Change the keys](#change-the-keys).

### The Conversation tab

These work while the Conversation tab shows. A turn is one comment, message, ask or handoff, or
one reply of a thread you expanded.

| Keys | Action |
| --- | --- |
| `j` / `k` | Next / previous turn |
| `Enter` or `o` | Expand or collapse the focused turn's thread, or open or close its other fold, such as its earlier replies or a handoff's fields |
| `r` | Put the cursor in the reply box of the focused turn's thread |
| `y` | Copy the focused turn's `dispatch://` reference |
| `Shift+Y` | Copy the focused turn's web address |

The focus stays on the turn when `Enter` or `o` expands its thread. An open ask waits on its
answer and a handoff takes no reply, so `r` does nothing on one. A handoff has no reference, so
`y` and `Shift+Y` do nothing on one.

## Document

These work in an issue's documents and in a project's documents.

| Keys | Action |
| --- | --- |
| `Ctrl+Alt+M` | Comment on the selected text |
| `Alt+Enter` | Open the link, or the issue key or reference, at the cursor (in a document you edit) |
| `Escape` | Move the focus out of the document you edit, so the page's keys work again |

`Ctrl+Alt+M` is the shortcut Google Docs uses. It does what the selection bar's **Comment** does:
it opens a comment on the text you selected and puts the cursor in it. It works while the cursor is
in the document and text is selected. With nothing selected, in a comment you are writing, or on a
document you cannot edit, it does nothing.

Each new comment puts the cursor in the comment box, also when a comment box is open already, after
you hid the margin and showed it again, and below 1280 px, where the margin is a sheet. `Escape` in
an empty comment box, or a comment you send, puts the cursor back in the document at the text you
commented on. Below 1280 px they also close the sheet.

A plain click on a link opens it, also in a document you edit. `Alt+Enter` opens the link at the
cursor as a click does, and changes no text. In a document you cannot edit, `Tab` reaches each
link, the titles Dispatch draws for issue keys and references included, and `Enter` opens the
focused one.

After `Escape`, the next `Tab` goes back into the document, and the cursor is where you left it.

## Agents

| Keys | Action |
| --- | --- |
| `/` | Move the focus to the **Search agents** box. While the page shows that box, `/` does not open search. |
| `j` / `k` | Next / previous agent |
| `Enter` | Message the focused agent |
| `i` | Pick an issue for the message |
| `Shift+P` | Pin or unpin the focused agent |
| `x` | Select or deselect the focused agent |
| `Escape` | Close the issue picker, or go back to the agent row |
| `Shift+F` | Open the filter panel |

## Dialogs

| Keys | Action |
| --- | --- |
| `Escape` | Close the dialog |
| `Ctrl+K` | Close the search palette |

While a dialog is open, only its own keys work. `y` on a focused link works there too.

## Writing

These keys belong to the message boxes rather than to a page, so `?` does not list them. The live
view's message box is the exception to the `Enter` row only: there `Enter` sends, as at the agent's
terminal, `Shift+Enter` starts a new line, and `Ctrl+Enter` sends as everywhere.

| Keys | Action |
| --- | --- |
| `Ctrl+Enter` | Send |
| `Enter` | New line |
| `Enter` in the live view's message box | Send |
| `Shift+Enter` in the live view's message box | New line |
| After `/` in the live view's Send or Aside box | Arrow keys choose a command; `Tab` or `Enter` picks it; `Escape` closes the list |
| `Ctrl+K` | In a new comment on an issue, open the reference picker to insert a `dispatch://` link |

## Change the keys

**Settings → Keyboard shortcuts** lists every action by the part of Dispatch it belongs to, with
its keys, also for pages you do not have open. Type in **Filter actions** to narrow the list. Sign
in to change a key.

- To change a key, choose it and press the new key.
- **Add key** gives the action one more key.
- **×** beside a key removes that key.
- **Reset** puts one action back on its default keys, and **Reset all** puts every action back.

The new key is the next key you press, with or without `Ctrl`, `Alt` or `Shift`. Press a second key
within one second to make a sequence, such as `g r`. `Escape`, `Tab` or a click elsewhere stops
without a change.

Dispatch refuses a key, and names the action that has it, when:

- another action has it on the same page, or on a part shown at the same time. The issue page is
  shown with its documents, and a project's page with its List, Board or Architecture tab.
- it is the first key of another action's sequence, or another action's key is its first key.

Dispatch also refuses the digits, the arrow keys, `Enter`, `Escape`, `Tab` and `Space`, alone or
with `Shift`, which keep their own actions. It also refuses `Ctrl` with `L`, `N`, `Q`, `R`, `T`,
`W` or `Tab`, which the browser keeps. An action whose keys all keep their own actions shows
**Fixed**, and you cannot change it.

A global key that you give to a page's action runs that action on its page whenever the action can
run. Where it cannot, the key runs the global action. The list shows a note under each of the two
actions. The Agents page's `/` and `i` are two such keys already: `/` opens search while the page
shows no **Search agents** box, and `i` opens the Inbox drawer while no agent row that can pick an
issue has the focus.

Dispatch keeps your keys in this browser, for your sign-in. Every open Dispatch tab uses them at
once, and so do the `?` help, the palette, and the keys the controls show.
