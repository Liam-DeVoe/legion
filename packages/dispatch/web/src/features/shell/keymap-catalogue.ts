/**
 * Every action of the keymap, whether or not the page that draws it is open: its scope, its id,
 * its label and its default keys. A component registers an action by naming it here
 * (`useKeymap(scope, [{ action: "inbox.snooze", run }])`, `keymap.ts`), so the `?` help, the
 * command bar and Settings read one label for it, and its keys are the person's where they
 * changed them (`keymap-keys.ts`) and these defaults everywhere else.
 *
 * The keys are the defaults of the spec's table "The keys" (dispatch://INFRA-2160/spec, R13). An
 * action holds its keys here whether or not any component registers it now, so the refusal check
 * (`keymap-refusal.ts`) reads every default key at once, and no person's key takes a key that a
 * page not open would take.
 */

/** A scope an action is listed under; `dialog` is no scope of its own here, since a dialog
 *  registers the actions of the page it shows (the Inbox drawer registers `inbox.*`). */
export type CatalogueScope =
  | "global"
  | "inbox"
  | "project"
  | "architecture"
  | "board"
  | "delivery"
  | "issue"
  | "document"
  | "agents";

/**
 * The part of a page that draws an action, which is what the refusal check reads: two actions of
 * one surface, or of two surfaces drawn at the same time, cannot share a key (`keymap-refusal.ts`).
 * The `project` scope stays pushed under the Board and the Architecture page, but only one tab
 * panel of a project mounts at a time, so the List's own keys (`project-list`) and the Board's
 * never meet; both meet the project page's own (`project-page`).
 */
export type KeySurface =
  | "agents"
  | "architecture"
  | "board"
  | "delivery"
  | "document"
  | "global"
  | "inbox"
  | "issue"
  | "project-list"
  | "project-page";

export interface CatalogueEntry {
  readonly scope: CatalogueScope;
  /** Unique within the scope; the registry describes the binding under it. */
  readonly id: string;
  readonly label: string;
  /** The default keys, in tinykeys syntax (`keymap.ts`), which a person can change. */
  readonly keys: readonly string[];
  /** The keys a person cannot change: the digits, the arrows, Enter, Escape, Tab and Space, and
   *  every key a run reads through `event.key`. They are always bound, after the person's. */
  readonly fixedKeys?: readonly string[];
  /** Whether the command bar offers the action as a row. Always written: a default read from the
   *  number of keys would drop an action from the bar once a person gives it a second key. */
  readonly palette: boolean;
  /** Fires while a control that takes typed text has the focus (`KeyBinding.inEditable`). */
  readonly inEditable?: boolean;
  readonly surface: KeySurface;
}

export const KEYMAP_CATALOGUE = {
  // Global: `app.tsx`.

  // Fires while the reader is writing, so the key reaches the bar from inside a document's
  // editor too.
  "global.search": {
    scope: "global",
    id: "search",
    label: "Search and actions",
    keys: ["$mod+k"],
    palette: false,
    inEditable: true,
    surface: "global",
  },
  // No row: chosen from the bar it would only reopen the bar. Not `inEditable`, so a slash typed
  // into a document stays a slash, and the Agents page shadows it with its own filter box.
  "global.search-only": {
    scope: "global",
    id: "search-only",
    label: "Search",
    keys: ["/"],
    palette: false,
    surface: "global",
  },
  "global.help": {
    scope: "global",
    id: "help",
    label: "Keyboard shortcuts",
    keys: ["?"],
    palette: true,
    surface: "global",
  },
  "global.create": {
    scope: "global",
    id: "create",
    label: "Create issue",
    keys: ["c"],
    palette: true,
    surface: "global",
  },
  "global.go-inbox": {
    scope: "global",
    id: "go-inbox",
    label: "Go to Inbox",
    keys: ["g i"],
    palette: true,
    surface: "global",
  },
  "global.go-agents": {
    scope: "global",
    id: "go-agents",
    label: "Go to Agents",
    keys: ["g a"],
    palette: true,
    surface: "global",
  },
  "global.go-settings": {
    scope: "global",
    id: "go-settings",
    label: "Go to Settings",
    keys: ["g s"],
    palette: true,
    surface: "global",
  },
  "global.go-documents": {
    scope: "global",
    id: "go-documents",
    label: "Go to Documents",
    keys: ["g d"],
    palette: true,
    surface: "global",
  },
  // No row, for `global.search-only`'s reason.
  "global.go-project": {
    scope: "global",
    id: "go-project",
    label: "Go to project…",
    keys: ["g p"],
    palette: false,
    surface: "global",
  },
  "global.go-priorities": {
    scope: "global",
    id: "go-priorities",
    label: "Go to Priorities",
    keys: [],
    palette: true,
    surface: "global",
  },
  "global.go-delivery": {
    scope: "global",
    id: "go-delivery",
    label: "Go to Delivery",
    keys: [],
    palette: true,
    surface: "global",
  },
  "global.go-pipeline": {
    scope: "global",
    id: "go-pipeline",
    label: "Go to Pipeline",
    keys: [],
    palette: true,
    surface: "global",
  },
  "global.inbox-drawer": {
    scope: "global",
    id: "inbox-drawer",
    label: "Open the inbox",
    keys: ["i"],
    palette: true,
    surface: "global",
  },
  "global.toggle-sidebar": {
    scope: "global",
    id: "toggle-sidebar",
    label: "Toggle sidebar",
    keys: ["Shift+S"],
    palette: true,
    surface: "global",
  },
  "global.toggle-margin": {
    scope: "global",
    id: "toggle-margin",
    label: "Toggle margin",
    keys: ["Shift+M"],
    palette: true,
    surface: "global",
  },

  // The Inbox: `inbox/Inbox.tsx`, on its page and, under the `dialog` scope, in its drawer.

  // Movement is what the arrow keys are for: a palette row that moves the cursor helps nobody,
  // so neither `j`, `k` nor the arrows is a row.
  "inbox.next": {
    scope: "inbox",
    id: "next",
    label: "Next row",
    keys: ["j"],
    palette: false,
    surface: "inbox",
  },
  "inbox.previous": {
    scope: "inbox",
    id: "previous",
    label: "Previous row",
    keys: ["k"],
    palette: false,
    surface: "inbox",
  },
  "inbox.arrows": {
    scope: "inbox",
    id: "arrows",
    label: "Next / previous row from the list",
    keys: [],
    fixedKeys: ["ArrowDown", "ArrowUp"],
    palette: false,
    surface: "inbox",
  },
  // Scrolling the open document is a key pressed with the row in hand, as `j` and `k` are, so
  // none of the four scroll keys is a row either.
  "inbox.scroll-down": {
    scope: "inbox",
    id: "scroll-down",
    label: "Scroll the open document down",
    keys: [],
    fixedKeys: ["Space"],
    palette: false,
    surface: "inbox",
  },
  "inbox.scroll-up": {
    scope: "inbox",
    id: "scroll-up",
    label: "Scroll the open document up",
    keys: [],
    fixedKeys: ["Shift+Space"],
    palette: false,
    surface: "inbox",
  },
  "inbox.scroll-top": {
    scope: "inbox",
    id: "scroll-top",
    label: "Scroll the open document to its top",
    keys: ["g g"],
    palette: false,
    surface: "inbox",
  },
  "inbox.scroll-end": {
    scope: "inbox",
    id: "scroll-end",
    label: "Scroll the open document to its end",
    keys: ["Shift+G"],
    palette: false,
    surface: "inbox",
  },
  "inbox.read-pane": {
    scope: "inbox",
    id: "read-pane",
    label: "Move the focus into the reading pane",
    keys: ["l"],
    palette: true,
    surface: "inbox",
  },
  "inbox.back": {
    scope: "inbox",
    id: "back",
    label: "Back to the row, then clear the focused row",
    keys: [],
    fixedKeys: ["Escape"],
    palette: false,
    inEditable: true,
    surface: "inbox",
  },
  "inbox.answer": {
    scope: "inbox",
    id: "answer",
    label: "Answer the ask in hand",
    keys: [],
    fixedKeys: ["Enter"],
    palette: true,
    surface: "inbox",
  },
  "inbox.option": {
    scope: "inbox",
    id: "option",
    label: "Pick an option of the ask in hand",
    keys: [],
    fixedKeys: ["1", "2", "3", "4", "5", "6", "7", "8", "9", "0"],
    palette: false,
    surface: "inbox",
  },
  "inbox.approve": {
    scope: "inbox",
    id: "approve",
    label: "Approve",
    keys: ["a"],
    palette: true,
    surface: "inbox",
  },
  "inbox.request-changes": {
    scope: "inbox",
    id: "request-changes",
    label: "Request changes",
    keys: ["Shift+R"],
    palette: true,
    surface: "inbox",
  },
  "inbox.reply": {
    scope: "inbox",
    id: "reply",
    label: "Reply in the thread of the item in hand",
    keys: ["r"],
    palette: true,
    surface: "inbox",
  },
  "inbox.snooze": {
    scope: "inbox",
    id: "snooze",
    label: "Snooze the focused ask, or every selected ask",
    keys: ["b", "h"],
    palette: true,
    surface: "inbox",
  },
  "inbox.priority": {
    scope: "inbox",
    id: "priority",
    label: "Open the priority menu",
    keys: ["p"],
    palette: true,
    surface: "inbox",
  },
  "inbox.dismiss": {
    scope: "inbox",
    id: "dismiss",
    label: "Dismiss, or Move to Inbox",
    keys: ["e"],
    palette: true,
    surface: "inbox",
  },
  // Marking walks the list with the row in hand, as `j`/`k` do, so it is no palette row.
  "inbox.select": {
    scope: "inbox",
    id: "select",
    label: "Select or deselect the row",
    keys: ["x"],
    palette: false,
    surface: "inbox",
  },
  "inbox.open": {
    scope: "inbox",
    id: "open",
    label: "Open the row's issue, document or thread",
    keys: ["o"],
    palette: true,
    surface: "inbox",
  },
  "inbox.open-new-tab": {
    scope: "inbox",
    id: "open-new-tab",
    label: "Open in a new tab",
    keys: ["Shift+O"],
    palette: true,
    surface: "inbox",
  },
  "inbox.copy-ref": {
    scope: "inbox",
    id: "copy-ref",
    label: "Copy the row's reference",
    keys: ["y"],
    palette: true,
    surface: "inbox",
  },
  // Not `inEditable`: inside the box itself a `search` input takes typed text (`keymap.ts`'s
  // `TEXT_INPUT_TYPES`), so the registry passes the key through and the reader types an `f`
  // rather than re-focusing what they are already in.
  "inbox.search": {
    scope: "inbox",
    id: "search",
    label: "Search asks",
    keys: ["f"],
    palette: true,
    surface: "inbox",
  },
  "inbox.leave-search": {
    scope: "inbox",
    id: "leave-search",
    label: "Back to the list from the search box",
    keys: [],
    fixedKeys: ["Escape"],
    palette: false,
    inEditable: true,
    surface: "inbox",
  },
  "inbox.clear-selection": {
    scope: "inbox",
    id: "clear-selection",
    label: "Clear the selection",
    keys: [],
    fixedKeys: ["Escape"],
    // The bar's Clear selection, a row of the command bar too (R11), which shows its Escape.
    palette: true,
    surface: "inbox",
  },
  "inbox.filter": {
    scope: "inbox",
    id: "filter",
    label: "Open the filter panel",
    keys: ["Shift+F"],
    palette: true,
    surface: "inbox",
  },
  "inbox.display": {
    scope: "inbox",
    id: "display",
    label: "Open the display menu",
    keys: ["Shift+V"],
    palette: true,
    surface: "inbox",
  },
  // The command bar's actions for the item in hand, with no default key (R11).
  "inbox.assign-to-me": {
    scope: "inbox",
    id: "assign-to-me",
    label: "Assign to me",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.unsnooze": {
    scope: "inbox",
    id: "unsnooze",
    label: "Un-snooze",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.mark-read": {
    scope: "inbox",
    id: "mark-read",
    label: "Mark read",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.earlier-replies": {
    scope: "inbox",
    id: "earlier-replies",
    label: "Show earlier replies",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.asker-handles": {
    scope: "inbox",
    id: "asker-handles",
    label: "Show the asker's handles",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.copy-session-id": {
    scope: "inbox",
    id: "copy-session-id",
    label: "Copy session ID",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.copy-session-title": {
    scope: "inbox",
    id: "copy-session-title",
    label: "Copy session title",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.copy-tmux-target": {
    scope: "inbox",
    id: "copy-tmux-target",
    label: "Copy tmux target",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.hand-on": {
    scope: "inbox",
    id: "hand-on",
    label: "Hand on",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.referenced-by": {
    scope: "inbox",
    id: "referenced-by",
    label: "Referenced by",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.earlier-approval-diff": {
    scope: "inbox",
    id: "earlier-approval-diff",
    label: "Show the earlier approval's diff",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.ask-back": {
    scope: "inbox",
    id: "ask-back",
    label: "Ask back",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.ask-back-instead": {
    scope: "inbox",
    id: "ask-back-instead",
    label: "Ask back instead",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.answer-anyway": {
    scope: "inbox",
    id: "answer-anyway",
    label: "Answer with it anyway",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.cancel": {
    scope: "inbox",
    id: "cancel",
    label: "Cancel",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.decline": {
    scope: "inbox",
    id: "decline",
    label: "Decline",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.recipients": {
    scope: "inbox",
    id: "recipients",
    label: "Recipients",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.change-answer": {
    scope: "inbox",
    id: "change-answer",
    label: "Change answer",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.previous-versions": {
    scope: "inbox",
    id: "previous-versions",
    label: "Show previous versions",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.earlier-answers": {
    scope: "inbox",
    id: "earlier-answers",
    label: "Show earlier answers",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.open-at-section": {
    scope: "inbox",
    id: "open-at-section",
    label: "Open page at the section",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.skip-to-answer": {
    scope: "inbox",
    id: "skip-to-answer",
    label: "Skip to the answer",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.next-change": {
    scope: "inbox",
    id: "next-change",
    label: "Next change",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.show-reviews": {
    scope: "inbox",
    id: "show-reviews",
    label: "Show the reviews",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.select-all": {
    scope: "inbox",
    id: "select-all",
    label: "Select all",
    keys: [],
    palette: true,
    surface: "inbox",
  },
  "inbox.bulk-dismiss": {
    scope: "inbox",
    id: "bulk-dismiss",
    label: "Dismiss, or Move to Inbox, the selected items",
    keys: [],
    palette: true,
    surface: "inbox",
  },

  // Today's issue page: `issue/IssuePage.tsx`, `issue/IssueHeader.tsx` and the Conversation tab.
  "issue.status": {
    scope: "issue",
    id: "status",
    label: "Open the status menu",
    keys: ["s"],
    palette: true,
    surface: "issue",
  },
  "issue.priority": {
    scope: "issue",
    id: "priority",
    label: "Open the priority menu",
    keys: ["p"],
    palette: true,
    surface: "issue",
  },
  "issue.set-priority": {
    scope: "issue",
    id: "set-priority",
    label: "Set priority P0–P3",
    keys: [],
    fixedKeys: ["0", "1", "2", "3"],
    palette: false,
    surface: "issue",
  },
  "issue.labels": {
    scope: "issue",
    id: "labels",
    label: "Edit labels",
    keys: ["l"],
    palette: true,
    surface: "issue",
  },
  "issue.title": {
    scope: "issue",
    id: "title",
    label: "Edit the title",
    keys: ["e"],
    palette: true,
    surface: "issue",
  },
  "issue.pin": {
    scope: "issue",
    id: "pin",
    label: "Pin or unpin the issue",
    keys: ["Shift+P"],
    palette: true,
    surface: "issue",
  },
  "issue.tab-spec": {
    scope: "issue",
    id: "tab-spec",
    label: "Spec tab",
    keys: ["t s"],
    palette: true,
    surface: "issue",
  },
  "issue.tab-verification": {
    scope: "issue",
    id: "tab-verification",
    label: "Verification tab",
    keys: ["t k"],
    palette: true,
    surface: "issue",
  },
  "issue.tab-plan": {
    scope: "issue",
    id: "tab-plan",
    label: "Plan tab",
    keys: ["t p"],
    palette: true,
    surface: "issue",
  },
  "issue.tab-steps": {
    scope: "issue",
    id: "tab-steps",
    label: "Steps tab",
    keys: ["t e"],
    palette: true,
    surface: "issue",
  },
  "issue.tab-conversation": {
    scope: "issue",
    id: "tab-conversation",
    label: "Conversation tab",
    keys: ["t c"],
    palette: true,
    surface: "issue",
  },
  "issue.tab-children": {
    scope: "issue",
    id: "tab-children",
    label: "Children tab",
    keys: ["t h"],
    palette: true,
    surface: "issue",
  },
  "issue.tab-artifacts": {
    scope: "issue",
    id: "tab-artifacts",
    label: "Artifacts tab",
    keys: ["t a"],
    palette: true,
    surface: "issue",
  },
  "issue.tab-activity": {
    scope: "issue",
    id: "tab-activity",
    label: "Activity tab",
    keys: ["t v"],
    palette: true,
    surface: "issue",
  },
  "issue.tab-decisions": {
    scope: "issue",
    id: "tab-decisions",
    label: "Decisions tab",
    keys: ["t d"],
    palette: true,
    surface: "issue",
  },
  "issue.tab-collaborators": {
    scope: "issue",
    id: "tab-collaborators",
    label: "Collaborators tab",
    keys: ["t o"],
    palette: true,
    surface: "issue",
  },
  "issue.previous-tab": {
    scope: "issue",
    id: "previous-tab",
    label: "Previous tab",
    keys: ["["],
    palette: true,
    surface: "issue",
  },
  "issue.next-tab": {
    scope: "issue",
    id: "next-tab",
    label: "Next tab",
    keys: ["]"],
    palette: true,
    surface: "issue",
  },
  "issue.close": {
    scope: "issue",
    id: "close",
    label: "Close issue",
    keys: [],
    palette: true,
    surface: "issue",
  },
  "issue.reopen": {
    scope: "issue",
    id: "reopen",
    label: "Reopen issue",
    keys: [],
    palette: true,
    surface: "issue",
  },
  "issue.set-p0": {
    scope: "issue",
    id: "set-p0",
    label: "Set priority P0",
    keys: [],
    palette: true,
    surface: "issue",
  },
  "issue.set-p1": {
    scope: "issue",
    id: "set-p1",
    label: "Set priority P1",
    keys: [],
    palette: true,
    surface: "issue",
  },
  "issue.set-p2": {
    scope: "issue",
    id: "set-p2",
    label: "Set priority P2",
    keys: [],
    palette: true,
    surface: "issue",
  },
  "issue.set-p3": {
    scope: "issue",
    id: "set-p3",
    label: "Set priority P3",
    keys: [],
    palette: true,
    surface: "issue",
  },
  "issue.approve": {
    scope: "issue",
    id: "approve",
    label: "Approve",
    keys: ["a"],
    palette: true,
    surface: "issue",
  },
  "issue.request-changes": {
    scope: "issue",
    id: "request-changes",
    label: "Request changes",
    keys: ["Shift+R"],
    palette: true,
    surface: "issue",
  },
  "issue.next-turn": {
    scope: "issue",
    id: "next-turn",
    label: "Next turn",
    keys: ["j"],
    palette: false,
    surface: "issue",
  },
  "issue.previous-turn": {
    scope: "issue",
    id: "previous-turn",
    label: "Previous turn",
    keys: ["k"],
    palette: false,
    surface: "issue",
  },
  // Expanding a turn opens no link, so the Conversation tab has no new-tab key.
  "issue.toggle-turn": {
    scope: "issue",
    id: "toggle-turn",
    label: "Expand or collapse the turn's thread",
    keys: ["o"],
    fixedKeys: ["Enter"],
    palette: true,
    surface: "issue",
  },
  "issue.reply-turn": {
    scope: "issue",
    id: "reply-turn",
    label: "Reply in the turn's thread",
    keys: ["r"],
    palette: true,
    surface: "issue",
  },
  "issue.copy-turn-ref": {
    scope: "issue",
    id: "copy-turn-ref",
    label: "Copy the turn's reference",
    keys: ["y"],
    palette: true,
    surface: "issue",
  },
  "issue.copy-turn-address": {
    scope: "issue",
    id: "copy-turn-address",
    label: "Copy the turn's web address",
    keys: ["Shift+Y"],
    palette: true,
    surface: "issue",
  },

  // A document page: `doc/ProofDocument.tsx`.
  "document.comment-on-selection": {
    scope: "document",
    id: "comment-on-selection",
    label: "Comment on selection",
    keys: ["$mod+Alt+m"],
    palette: false,
    inEditable: true,
    surface: "document",
  },
  "document.open-link": {
    scope: "document",
    id: "open-link",
    label: "Open the link at the caret",
    keys: ["Alt+Enter"],
    palette: false,
    inEditable: true,
    surface: "document",
  },
  "document.leave-editor": {
    scope: "document",
    id: "leave-editor",
    label: "Take the focus out of the editor",
    keys: [],
    fixedKeys: ["Escape"],
    palette: false,
    inEditable: true,
    surface: "document",
  },

  // A project: `project/ProjectPage.tsx`, and the row keys every list of the `project` scope
  // shares (`components/table-keys.ts`): the List, the Documents list, the Priorities page and a
  // priority's page. One action each, whichever list draws it (R14).
  "project.toggle-view": {
    scope: "project",
    id: "toggle-view",
    label: "Toggle List / Board",
    keys: ["v"],
    palette: true,
    surface: "project-page",
  },
  "project.next": {
    scope: "project",
    id: "next",
    label: "Next row",
    keys: ["j"],
    palette: false,
    surface: "project-list",
  },
  "project.previous": {
    scope: "project",
    id: "previous",
    label: "Previous row",
    keys: ["k"],
    palette: false,
    surface: "project-list",
  },
  "project.open": {
    scope: "project",
    id: "open",
    label: "Open",
    keys: ["o"],
    palette: true,
    surface: "project-list",
  },
  "project.open-enter": {
    scope: "project",
    id: "open-enter",
    label: "Open the focused row",
    keys: [],
    fixedKeys: ["Enter"],
    palette: false,
    surface: "project-list",
  },
  "project.open-new-tab": {
    scope: "project",
    id: "open-new-tab",
    label: "Open in a new tab",
    keys: ["Shift+O"],
    palette: true,
    surface: "project-list",
  },
  "project.select": {
    scope: "project",
    id: "select",
    label: "Select or deselect the focused row",
    keys: ["x"],
    palette: false,
    surface: "project-list",
  },
  "project.filter": {
    scope: "project",
    id: "filter",
    label: "Open the filter panel",
    keys: ["Shift+F"],
    palette: true,
    surface: "project-list",
  },
  "project.display": {
    scope: "project",
    id: "display",
    label: "Open the display menu",
    keys: ["Shift+V"],
    palette: true,
    surface: "project-list",
  },

  // The Board: `project/IssueBoard.tsx`.
  // Moving the cursor, by card or by column, is no row, for the Inbox's `j` and `k`'s reason.
  "board.next": {
    scope: "board",
    id: "next",
    label: "Next card",
    keys: ["j"],
    palette: false,
    surface: "board",
  },
  "board.previous": {
    scope: "board",
    id: "previous",
    label: "Previous card",
    keys: ["k"],
    palette: false,
    surface: "board",
  },
  "board.column": {
    scope: "board",
    id: "column",
    label: "Next column",
    keys: ["l"],
    palette: false,
    surface: "board",
  },
  "board.previous-column": {
    scope: "board",
    id: "previous-column",
    label: "Previous column",
    keys: ["h"],
    palette: false,
    surface: "board",
  },
  "board.arrows-vertical": {
    scope: "board",
    id: "arrows-vertical",
    label: "Next / previous card while one is focused",
    keys: [],
    fixedKeys: ["ArrowDown", "ArrowUp"],
    palette: false,
    surface: "board",
  },
  "board.arrows-horizontal": {
    scope: "board",
    id: "arrows-horizontal",
    label: "Next / previous column while one is focused",
    keys: [],
    fixedKeys: ["ArrowRight", "ArrowLeft"],
    palette: false,
    surface: "board",
  },
  "board.move-down": {
    scope: "board",
    id: "move-down",
    label: "Move card down",
    keys: ["Shift+J"],
    palette: true,
    surface: "board",
  },
  "board.move-up": {
    scope: "board",
    id: "move-up",
    label: "Move card up",
    keys: ["Shift+K"],
    palette: true,
    surface: "board",
  },
  "board.move-next": {
    scope: "board",
    id: "move-next",
    label: "Move card to the next status",
    keys: ["Shift+L"],
    palette: true,
    surface: "board",
  },
  "board.move-previous": {
    scope: "board",
    id: "move-previous",
    label: "Move card to the previous status",
    keys: ["Shift+H"],
    palette: true,
    surface: "board",
  },
  "board.open": {
    scope: "board",
    id: "open",
    label: "Open issue",
    keys: ["o"],
    palette: true,
    surface: "board",
  },
  "board.open-enter": {
    scope: "board",
    id: "open-enter",
    label: "Open the focused card's issue",
    keys: [],
    fixedKeys: ["Enter"],
    palette: false,
    surface: "board",
  },
  "board.open-new-tab": {
    scope: "board",
    id: "open-new-tab",
    label: "Open in a new tab",
    keys: ["Shift+O"],
    palette: true,
    surface: "board",
  },
  "board.priority": {
    scope: "board",
    id: "priority",
    label: "Open the card's priority menu",
    keys: ["p"],
    palette: true,
    surface: "board",
  },
  "board.copy-ref": {
    scope: "board",
    id: "copy-ref",
    label: "Copy the card's issue reference",
    keys: ["y"],
    palette: true,
    surface: "board",
  },
  "board.copy-key": {
    scope: "board",
    id: "copy-key",
    label: "Copy the card's issue key",
    keys: ["Shift+Y"],
    palette: true,
    surface: "board",
  },
  "board.back": {
    scope: "board",
    id: "back",
    label: "Back to the card, then out",
    keys: [],
    fixedKeys: ["Escape"],
    palette: false,
    inEditable: true,
    surface: "board",
  },
  "board.filter": {
    scope: "board",
    id: "filter",
    label: "Open the filter panel",
    keys: ["Shift+F"],
    palette: true,
    surface: "board",
  },

  // A project's Architecture page: `architecture/ArchitecturePage.tsx`.
  "architecture.next": {
    scope: "architecture",
    id: "next",
    label: "Next component",
    keys: ["j"],
    palette: false,
    surface: "architecture",
  },
  "architecture.previous": {
    scope: "architecture",
    id: "previous",
    label: "Previous component",
    keys: ["k"],
    palette: false,
    surface: "architecture",
  },
  "architecture.arrows": {
    scope: "architecture",
    id: "arrows",
    label: "Next / previous component while one is focused",
    keys: [],
    fixedKeys: ["ArrowDown", "ArrowUp"],
    palette: false,
    surface: "architecture",
  },
  "architecture.open": {
    scope: "architecture",
    id: "open",
    label: "Open the focused component: its children, else its details",
    keys: ["o"],
    fixedKeys: ["Enter"],
    palette: true,
    surface: "architecture",
  },
  "architecture.descend": {
    scope: "architecture",
    id: "descend",
    label: "Into the focused component's children",
    keys: ["l"],
    fixedKeys: ["ArrowRight"],
    palette: false,
    surface: "architecture",
  },
  "architecture.ascend": {
    scope: "architecture",
    id: "ascend",
    label: "Up one level",
    keys: ["h"],
    fixedKeys: ["ArrowLeft"],
    palette: true,
    surface: "architecture",
  },
  "architecture.back": {
    scope: "architecture",
    id: "back",
    label: "Back to the component row, then out",
    keys: [],
    fixedKeys: ["Escape"],
    palette: false,
    inEditable: true,
    surface: "architecture",
  },
  "architecture.open-new-tab": {
    scope: "architecture",
    id: "open-new-tab",
    label: "Open in a new tab",
    keys: ["Shift+O"],
    palette: true,
    surface: "architecture",
  },

  // The Agents page: `agents/keyboard.ts`. Its `/` and `i` take global keys on purpose, which the
  // refusal check accepts with a notice that the global action does not work there.
  "agents.search": {
    scope: "agents",
    id: "search",
    label: "Search agents",
    keys: ["/"],
    palette: true,
    surface: "agents",
  },
  "agents.next": {
    scope: "agents",
    id: "next",
    label: "Next agent",
    keys: ["j"],
    palette: false,
    surface: "agents",
  },
  "agents.previous": {
    scope: "agents",
    id: "previous",
    label: "Previous agent",
    keys: ["k"],
    palette: false,
    surface: "agents",
  },
  "agents.compose": {
    scope: "agents",
    id: "compose",
    label: "Message the focused agent",
    keys: [],
    fixedKeys: ["Enter"],
    palette: true,
    surface: "agents",
  },
  "agents.issue-picker": {
    scope: "agents",
    id: "issue-picker",
    label: "Pick an issue for the message",
    keys: ["i"],
    palette: true,
    surface: "agents",
  },
  "agents.pin": {
    scope: "agents",
    id: "pin",
    label: "Pin or unpin the focused agent",
    keys: ["Shift+P"],
    palette: true,
    surface: "agents",
  },
  "agents.select": {
    scope: "agents",
    id: "select",
    label: "Select or deselect the focused agent",
    keys: ["x"],
    palette: false,
    surface: "agents",
  },
  "agents.back": {
    scope: "agents",
    id: "back",
    label: "Close the issue picker, or go back to the agent row",
    keys: [],
    fixedKeys: ["Escape"],
    palette: false,
    inEditable: true,
    surface: "agents",
  },
  "agents.filter": {
    scope: "agents",
    id: "filter",
    label: "Open the filter panel",
    keys: ["Shift+F"],
    palette: true,
    surface: "agents",
  },

  // The Delivery page's list: `delivery/PRList.tsx`, through `components/table-keys.ts`.
  "delivery.next": {
    scope: "delivery",
    id: "next",
    label: "Next pull request",
    keys: [],
    fixedKeys: ["ArrowDown"],
    palette: false,
    surface: "delivery",
  },
  "delivery.previous": {
    scope: "delivery",
    id: "previous",
    label: "Previous pull request",
    keys: [],
    fixedKeys: ["ArrowUp"],
    palette: false,
    surface: "delivery",
  },
  "delivery.open": {
    scope: "delivery",
    id: "open",
    label: "Open the pull request's details",
    keys: ["o"],
    palette: true,
    surface: "delivery",
  },
  "delivery.open-enter": {
    scope: "delivery",
    id: "open-enter",
    label: "Open the focused pull request",
    keys: [],
    fixedKeys: ["Enter"],
    palette: false,
    surface: "delivery",
  },
  "delivery.filter": {
    scope: "delivery",
    id: "filter",
    label: "Open the filter panel",
    keys: ["Shift+F"],
    palette: true,
    surface: "delivery",
  },
} as const satisfies Record<string, CatalogueEntry>;

/** An action's name: its scope and its id, `inbox.snooze`. */
export type KeymapActionId = keyof typeof KEYMAP_CATALOGUE;

/** Every action, in the catalogue's order. */
export const KEYMAP_ACTIONS = Object.keys(KEYMAP_CATALOGUE) as readonly KeymapActionId[];

/** The keys a person changed, by action, and only those: `[]` is an action they took every key
 *  from, and an action that is not named takes its defaults. */
export type PersonKeys = Readonly<Partial<Record<KeymapActionId, readonly string[]>>>;

/** A person who changed nothing, and everyone while nobody is signed in. */
export const NO_PERSON_KEYS: PersonKeys = Object.freeze({});

/** The entry of `action`, read as the one shape every entry has. */
export function catalogueEntry(action: KeymapActionId): CatalogueEntry {
  return KEYMAP_CATALOGUE[action];
}

/** Whether every key of `entry` keeps its own action: it has no default key a person can change,
 *  and fixed keys its run reads through `event.key`, so it takes no key of a person's. */
export function keepsOwnKeys(entry: CatalogueEntry): boolean {
  return entry.keys.length === 0 && (entry.fixedKeys?.length ?? 0) > 0;
}

/** Whether `name` is an action of the catalogue: a stored map may hold one a later build
 *  renamed or removed. */
export function isKeymapAction(name: string): name is KeymapActionId {
  return Object.hasOwn(KEYMAP_CATALOGUE, name);
}

/** The keys that run `action` for a person: theirs where they changed them, else the defaults,
 *  then the keys nobody changes. */
export function keysOfAction(action: KeymapActionId, personKeys: PersonKeys): readonly string[] {
  const entry = catalogueEntry(action);
  const keys = personKeys[action] ?? entry.keys;
  const fixed = entry.fixedKeys ?? [];
  return fixed.length === 0 ? keys : [...keys, ...fixed];
}
