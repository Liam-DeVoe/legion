import { createContext } from "react";

import type { AskHandOnWrites } from "./useAskHandOn";

/**
 * The hand-on writes of the Inbox around a card (`useAskHandOnWrites`), as a context rather than
 * a prop.
 *
 * The hand-on's optimistic `waiting_on_person` takes the row out of the Inbox's own list the
 * moment the write starts (`isMine`, `inAssigneeFilter`), and the card that pressed can go before
 * the server answers: the reading column's card goes whenever the reader moves the selection. So
 * the Inbox holds every card's hand-on itself, by ask, and keeps the ask's row where the reader
 * saw it while that write is out (`heldRow`). The write and its refusal outlive the card: the
 * card drawn for the ask again, and the ask's short row beside the column, show that one write,
 * with its Retry. A refused or stopped hand-on holds no row: the rollback has put the person back,
 * and the next read of the Inbox places the row.
 *
 * It is a context because the card the reader pressed need not be one the Inbox rendered itself.
 * A question written as a decision block is drawn in the reading column by the document page's
 * own components - `ReadingPane` -> `DocumentPane` (which lives in `PaneDocument.tsx`) ->
 * `ProofDocument` -> `AskBlockCard` - which carry no prop of the Inbox's, and threading one
 * through them would leave the next host free to omit it exactly as that path did. `AskHandOn`
 * reads the writes from here instead, so every card under an Inbox has them and no host can be
 * written without them. The reader's answer and decline reach the Inbox's writes the same way
 * (`outstanding-answers.ts`), and so does every row's snooze (`useAskSnoozeWrites`). `AssignToMe`
 * still reports upward through a prop, because its hosts are the Inbox's own: the row it renders,
 * and the reading column's head (`ReadingPaneHead`), whose `AssignToMe` reports no further than
 * the head, which draws it while that write is live.
 *
 * Reaching every card is also why the Inbox holds only rows it drew. Many cards under it speak
 * for an ask it draws no row for: the card of a Reply row's thread, whose ask can be one the
 * reader's filters hide, and every ask block of a document open in the column beside the
 * selected one. The Inbox keeps a row for a hand-on out only where the row is among those it
 * last drew (`Inbox.tsx`, `heldRow`), so none of those can pull a hidden question into the list.
 *
 * Each Inbox provides its own - the page's and the drawer's are separate. With no provider a card
 * makes its own write and holds nothing (`useAskHandOn`), which is what a card drawn outside a
 * list does: the issue page's Conversation, a document's margin, its ask block, the Answers page.
 */
export const AskHandOnWritesContext = createContext<AskHandOnWrites | undefined>(undefined);
