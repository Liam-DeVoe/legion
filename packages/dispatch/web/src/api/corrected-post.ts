import type { QueryClient } from "@tanstack/react-query";

import { type PostDeletion, postDeletion } from "../features/refs/post-marks";

import { correctedPostAddresses } from "./event-refresh";
import { dismissedThreadsQuery, inboxThreadsQuery, type ReferenceTargetsAnswer } from "./queries";
import type {
  Actor,
  AskRead,
  CommentEventPayload,
  CommentRead,
  CommentWithTrees,
  DispatchEvent,
  DisplayTree,
  GraphReferences,
  MessageEventPayload,
  MessageRead,
  MessageWithTrees,
  MyRepliesResponse,
  MyReplyRow,
  ReferenceTarget,
  ReplyBlockWithTrees,
  SearchResponse,
  SearchResult,
} from "./types";

/** What a post's correction writes into each copy of the post: the time of an edit, or the mark
 *  of a delete, with whether the delete emptied the comment's anchor quote, which an insert
 *  suggestion's mark holds as the comment's own words. */
type PostMark =
  | { readonly kind: "edited"; readonly at: string }
  | { readonly kind: "deleted"; readonly deletion: PostDeletion; readonly quoteCleared: boolean };

/** The post a correction event is of, by the kind and the id each copy of it carries. */
interface CorrectedPost {
  readonly kind: "comment" | "message";
  readonly id: string;
  /** What every block of the post's node id starts with: a block's is `<kind>/<id>#<block id>`. */
  readonly blockNodes: string;
  readonly mark: PostMark;
}

/**
 * Writes a post's correction (`comment.edited`, `comment.deleted`, `message.edited`,
 * `message.deleted`) into every copy of the post the cache holds, as the server's own read
 * answers the corrected post. Each of these reads is refreshed by the same event
 * (`eventRefreshTargets`), and its answer replaces this; but a refetch that fails keeps the answer
 * it would have replaced, and a surface drawing that answer shows no error, so without this it
 * would go on printing a deleted post's words, or an edited post's without its mark. The event
 * already holds the deciding fact: the delete's mark (`deleted_at`, `deleted_by`) or the edit's
 * time (`edited_at`).
 *
 * A delete takes every copy of the post's words out and writes its mark beside where they were:
 * - a comment's or a message's row (`body` empty and no `body_tree`, a comment's `suggestion`
 *   null and, where the delete emptied it, its anchor's `quote`), in the issue's comment lists
 *   (`["comments", …]`), each ask thread's replies (`["ask-thread", id]`), a comment's read and its
 *   replies (`["comment", id]`), a message's read and its replies (`["issue-message", …]`) and
 *   the margin's pinned messages (`["issue-messages", key, "margin-pinned", …]`);
 * - the quote a reply in any of those rows holds of a block of the post (`reply_block`): removed,
 *   with no text, as a read of the reply answers it;
 * - each event of the post's own that carries its body, among the margin's pinned events
 *   (`["events", key, "margin-pinned", …]`): no `body`, `body_truncated`, `suggestion` or body
 *   tree, as the server's read of the event log gives it (`attachPostMarks` in
 *   `api/events.go`), which leaves the previews a reply's events froze of the post;
 * - an Inbox thread row (`inboxThreadsQuery`, `dismissedThreadsQuery`) whose root is the post
 *   (`subject` empty) or whose latest reply is (`reply.first_line` empty);
 * - a search hit of the post (`["search", …]`): an empty `snippet` and no `snippet_tree`;
 * - a block read of a block of the post (`["references", "to", …]`): its node with no text, no
 *   tree and no pinned text. The server answers that read 404, which no cached answer holds,
 *   and the node's mark is what a reference to the block reads the deletion from;
 * - each member of a names read naming the post or a block of it (`["reference-targets", …]`,
 *   by `correctedPostAddresses`): the post's named `""`, a block's gone, with no tree.
 *
 * An edit writes its time (`edited_at`) on each of those copies that carries one, and nothing
 * else: an edit event's body can be cut short (`body_truncated`), so the words stay those of the
 * answer until a read gives the new ones.
 *
 * A member a names read answered ambiguous or gone names no post, and is left as it is. An
 * answer holding no copy of the post, or no answer yet, is written nothing: each updater answers
 * `undefined`, where an answer written back unchanged would still count as a new one and clear
 * the read's stale mark. A delete's event carries the delete's time, as every one this server
 * writes does; one that carried none would have no mark to write, and writes nothing.
 */
export function markCorrectedPost(queryClient: QueryClient, event: DispatchEvent): void {
  const post = correctedPost(event);
  if (post === undefined) {
    return;
  }
  const { mark } = post;
  const names = correctedPostAddresses(event);
  if (names !== undefined) {
    queryClient.setQueriesData<ReferenceTargetsAnswer>(
      { queryKey: ["reference-targets"] },
      (answer) => markedNamesRead(answer, names, mark)
    );
  }
  if (post.kind === "comment") {
    const comment = (row: CommentWithTrees) => markedComment(row, post);
    queryClient.setQueriesData<CommentWithTrees[]>({ queryKey: ["comments"] }, (comments) =>
      markEach(comments, comment)
    );
    queryClient.setQueriesData<AskRead>({ queryKey: ["ask-thread"] }, (thread) => {
      const replies = markEach(thread?.replies, comment);
      return thread === undefined || replies === undefined ? undefined : { ...thread, replies };
    });
    queryClient.setQueriesData<CommentRead>({ queryKey: ["comment"] }, (read) => {
      const root = read === undefined ? undefined : comment(read.comment);
      const replies = markEach(read?.replies, comment);
      return read === undefined || (root === undefined && replies === undefined)
        ? undefined
        : { ...read, comment: root ?? read.comment, replies: replies ?? read.replies };
    });
  } else {
    const message = (read: MessageRead) => markedMessageRead(read, post);
    queryClient.setQueriesData<MessageRead>({ queryKey: ["issue-message"] }, (read) =>
      read === undefined ? undefined : message(read)
    );
    queryClient.setQueriesData<MessageRead[]>(
      {
        queryKey: ["issue-messages"],
        predicate: ({ queryKey }) => queryKey[2] === "margin-pinned",
      },
      (reads) => markEach(reads, message)
    );
  }
  queryClient.setQueriesData<DispatchEvent[]>(
    { queryKey: ["events"], predicate: ({ queryKey }) => queryKey[2] === "margin-pinned" },
    (events) => markEach(events, (pinned) => markedEvent(pinned, post))
  );
  queryClient.setQueryData(inboxThreadsQuery().queryKey, (page) =>
    page === undefined ? undefined : markedThreadPage(page, post)
  );
  queryClient.setQueryData(dismissedThreadsQuery().queryKey, (dismissed) => {
    const pages = markEach(dismissed?.pages, (page) => markedThreadPage(page, post));
    return dismissed === undefined || pages === undefined ? undefined : { ...dismissed, pages };
  });
  queryClient.setQueriesData<SearchResponse>({ queryKey: ["search"] }, (answer) => {
    const results = markEach(answer?.results, (hit) =>
      hit.kind === post.kind && hit.id === post.id ? markedHit(hit, mark) : undefined
    );
    return answer === undefined || results === undefined ? undefined : { ...answer, results };
  });
  queryClient.setQueriesData<GraphReferences>({ queryKey: ["references", "to"] }, (read) =>
    read === undefined ? undefined : markedBlockRead(read, post)
  );
}

/** The post `event` corrects and what the correction writes; undefined for every other event, and
 *  for an edit or a delete whose event carries no time to write. */
function correctedPost(event: DispatchEvent): CorrectedPost | undefined {
  let kind: CorrectedPost["kind"];
  let mark: PostMark;
  switch (event.type) {
    case "comment.deleted":
    case "message.deleted": {
      const deletion = postDeletion(event.payload);
      if (deletion === null) {
        return undefined;
      }
      kind = event.type === "comment.deleted" ? "comment" : "message";
      // The delete's own payload carries the comment's anchor as the delete left it.
      const quoteCleared = event.type === "comment.deleted" && event.payload.anchor?.quote === "";
      mark = { deletion, kind: "deleted", quoteCleared };
      break;
    }
    case "comment.edited":
    case "message.edited": {
      const at = event.payload.edited_at;
      if (at === undefined || at === null) {
        return undefined;
      }
      kind = event.type === "comment.edited" ? "comment" : "message";
      mark = { at, kind: "edited" };
      break;
    }
    default:
      return undefined;
  }
  const { id } = event.payload;
  return { blockNodes: `${kind}/${id}#`, id, kind, mark };
}

/** `items` with each item `mark` answers for in its place, or undefined where it answers for
 *  none of them (or there are none), so an answer holding no copy of the post is written
 *  nothing. */
function markEach<T>(
  items: readonly T[] | null | undefined,
  mark: (item: T) => T | undefined
): T[] | undefined {
  if (items === null || items === undefined) {
    return undefined;
  }
  const marked = items.map((item) => mark(item));
  return marked.some((item) => item !== undefined)
    ? items.map((item, at) => marked[at] ?? item)
    : undefined;
}

/** The delete's mark as a read writes it where the actor is optional: no `deleted_by` for a
 *  delete that names nobody. */
function deletionMarks(deletion: PostDeletion): { deleted_at: string; deleted_by?: Actor } {
  return deletion.by === null
    ? { deleted_at: deletion.at }
    : { deleted_at: deletion.at, deleted_by: deletion.by };
}

/** `block`, the block of its parent a reply names, as a read of the reply answers it once `post`
 *  is corrected; undefined for no block, or one of another post. A block of a deleted post is
 *  removed with no text, beside the delete's mark; one of an edited post carries the edit's time
 *  (`ReplyBlocks` in `refs/reply_blocks.go`). */
function markedReplyBlock(
  block: ReplyBlockWithTrees | null,
  post: CorrectedPost
): ReplyBlockWithTrees | undefined {
  if (block === null || !block.node.startsWith(post.blockNodes)) {
    return undefined;
  }
  if (post.mark.kind === "edited") {
    return { ...block, edited_at: post.mark.at };
  }
  const { deleted_by: _by, text_tree: _tree, ...kept } = block;
  return { ...kept, ...deletionMarks(post.mark.deletion), removed: true, text: "" };
}

/** `comment` as a read answers it once `post`, a comment, is corrected: the post itself, or a
 *  reply quoting a block of it; undefined for any other comment. */
function markedComment(
  comment: CommentWithTrees,
  post: CorrectedPost
): CommentWithTrees | undefined {
  const block = markedReplyBlock(comment.reply_block, post);
  if (comment.id !== post.id) {
    return block === undefined ? undefined : { ...comment, reply_block: block };
  }
  if (post.mark.kind === "edited") {
    return { ...comment, edited_at: post.mark.at };
  }
  const { deletion, quoteCleared } = post.mark;
  const { body_tree: _tree, ...kept } = comment;
  return {
    ...kept,
    anchor: quoteCleared && kept.anchor !== null ? { ...kept.anchor, quote: "" } : kept.anchor,
    body: "",
    deleted_at: deletion.at,
    deleted_by: deletion.by,
    suggestion: null,
  };
}

/** `message` as a read answers it once `post`, a message, is corrected: the post itself, or a
 *  reply quoting a block of it; undefined for any other message. */
function markedMessage(
  message: MessageWithTrees,
  post: CorrectedPost
): MessageWithTrees | undefined {
  const block = markedReplyBlock(message.reply_block, post);
  if (message.id !== post.id) {
    return block === undefined ? undefined : { ...message, reply_block: block };
  }
  if (post.mark.kind === "edited") {
    return { ...message, edited_at: post.mark.at };
  }
  const { body_tree: _tree, ...kept } = message;
  return {
    ...kept,
    body: "",
    deleted_at: post.mark.deletion.at,
    deleted_by: post.mark.deletion.by,
  };
}

/** A message's read, the message and its replies, once `post` is corrected; undefined where it
 *  holds no copy of the post. */
function markedMessageRead(read: MessageRead, post: CorrectedPost): MessageRead | undefined {
  const root = markedMessage(read.message, post);
  const replies = markEach(read.replies, (reply) => markedMessage(reply, post));
  return root === undefined && replies === undefined
    ? undefined
    : { ...read, message: root ?? read.message, replies: replies ?? read.replies };
}

/** The events whose payload is a post of each kind with its body, which the server's read of the
 *  event log marks (`postBodyEventKinds` in `api/events.go`). */
const POST_BODY_EVENTS: Record<
  CorrectedPost["kind"],
  Partial<Record<DispatchEvent["type"], true>>
> = {
  comment: {
    "comment.anchor_refreshed": true,
    "comment.answered": true,
    "comment.created": true,
    "comment.edited": true,
    "comment.reopened": true,
    "comment.resolved": true,
    "suggestion.accepted": true,
    "suggestion.rejected": true,
  },
  message: { "message.answered": true, "message.created": true, "message.edited": true },
};

/** An event whose payload is a comment or a message as it stood when the event was written. */
type PostEvent = Extract<
  DispatchEvent,
  { readonly payload: CommentEventPayload | MessageEventPayload }
>;

/** Whether `event` is one whose payload carries the body of a post of `kind`. */
function carriesPostBody(event: DispatchEvent, kind: CorrectedPost["kind"]): event is PostEvent {
  return POST_BODY_EVENTS[kind][event.type] === true;
}

/** The tree an event carries of its payload's body (`api/event_trees.go`). */
const BODY_TREE = "payload.body";

/** `event` as the server's read of the event log answers it once `post` is corrected, where it is
 *  one of the post's own events carrying its body; undefined for every other event. The payload
 *  keeps its type's own shape, which the event's type no longer says once it is rebuilt, hence
 *  the casts. */
function markedEvent(event: DispatchEvent, post: CorrectedPost): DispatchEvent | undefined {
  if (!carriesPostBody(event, post.kind) || event.payload.id !== post.id) {
    return undefined;
  }
  const { mark } = post;
  if (mark.kind === "edited") {
    return { ...event, payload: { ...event.payload, edited_at: mark.at } } as DispatchEvent;
  }
  // The body, the mark that it was cut short, and a comment's suggestion, whose replacement text
  // is the comment's text too.
  const deleted = { deleted_at: mark.deletion.at, deleted_by: mark.deletion.by };
  const trees = withoutBodyTree(event.trees);
  if ("suggestion" in event.payload) {
    const { body: _body, body_truncated: _cut, suggestion: _suggestion, ...kept } = event.payload;
    return { ...event, payload: { ...kept, ...deleted }, trees } as DispatchEvent;
  }
  const { body: _text, body_truncated: _truncated, ...kept } = event.payload;
  return { ...event, payload: { ...kept, ...deleted }, trees } as DispatchEvent;
}

/** `trees` without the tree of the payload's body. */
function withoutBodyTree(
  trees: Readonly<Record<string, DisplayTree>> | undefined
): Readonly<Record<string, DisplayTree>> | undefined {
  if (trees === undefined || !(BODY_TREE in trees)) {
    return trees;
  }
  const { [BODY_TREE]: _tree, ...kept } = trees;
  return kept;
}

/** One page of Inbox thread rows once `post` is corrected; undefined where no row of it holds a
 *  copy of the post. */
function markedThreadPage(
  page: MyRepliesResponse,
  post: CorrectedPost
): MyRepliesResponse | undefined {
  const rows = markEach(page.rows, (row) => markedThreadRow(row, post));
  return rows === undefined ? undefined : { ...page, rows };
}

/**
 * An Inbox thread row as `GET /api/v1/me/replies` answers it once `post` is corrected: a row whose
 * root is the post carries the edit's time, or the delete's mark with no subject, and one whose
 * latest reply is the post carries that reply's delete mark with no first line (`readMyReplyRows`
 * in `api/replies.go`). An ask's and a comment's thread take comment replies, a message's
 * message replies. Undefined for any other row, and for a reply's edit, which a row does not
 * mark.
 */
function markedThreadRow(row: MyReplyRow, post: CorrectedPost): MyReplyRow | undefined {
  const replyKind = row.kind === "message" || row.kind === "agent" ? "message" : "comment";
  const root = row.kind === post.kind && row.thread_id === post.id;
  const reply = replyKind === post.kind && row.reply.id === post.id;
  if (post.mark.kind === "edited") {
    return root ? { ...row, edited_at: post.mark.at } : undefined;
  }
  if (!root && !reply) {
    return undefined;
  }
  const marks = deletionMarks(post.mark.deletion);
  const { deleted_by: _rootBy, ...rowKept } = row;
  const { deleted_by: _replyBy, ...replyKept } = row.reply;
  return {
    ...(root ? { ...rowKept, ...marks, subject: "" } : row),
    reply: reply ? { ...replyKept, ...marks, first_line: "" } : row.reply,
  };
}

/** A search hit of the post once it is corrected: the edit's time, or the delete's mark with no
 *  snippet, as the server answers a hit of a deleted post (`stampSearchPostMarks` in
 *  `api/search.go`). */
function markedHit(hit: SearchResult, mark: PostMark): SearchResult {
  if (mark.kind === "edited") {
    return { ...hit, edited_at: mark.at };
  }
  const { deleted_by: _by, snippet_tree: _tree, ...kept } = hit;
  return { ...kept, ...deletionMarks(mark.deletion), snippet: "" };
}

/** A block read (`blockQuery`) of a block of the post once it is corrected: its node carries the
 *  edit's time, or the delete's mark with no text, no tree and no pinned text. Undefined for a
 *  read of anything else. */
function markedBlockRead(read: GraphReferences, post: CorrectedPost): GraphReferences | undefined {
  const { node } = read;
  if (node.kind !== "block" || !node.id.startsWith(post.blockNodes)) {
    return undefined;
  }
  if (post.mark.kind === "edited") {
    return { ...read, node: { ...node, edited_at: post.mark.at } };
  }
  const { deleted_by: _by, pin, text, text_tree: _tree, ...kept } = node;
  const marked = {
    ...kept,
    ...deletionMarks(post.mark.deletion),
    ...(text === undefined ? {} : { text: "" }),
  };
  if (pin === undefined) {
    return { ...read, node: marked };
  }
  const { text_tree: _pinTree, ...pinKept } = pin;
  return { ...read, node: { ...marked, pin: { ...pinKept, text: "" } } };
}

/** A names read once the post is corrected: each member naming the post or a block of it
 *  (`names`) as GET /api/v1/reference-targets answers it; undefined where no member does. */
function markedNamesRead(
  answer: ReferenceTargetsAnswer | undefined,
  names: (address: string) => boolean,
  mark: PostMark
): ReferenceTargetsAnswer | undefined {
  const targets = markEach(answer?.targets, (target) =>
    target.ambiguous === true || target.gone || !names(target.ref)
      ? undefined
      : mark.kind === "edited"
        ? { ...target, edited_at: mark.at }
        : deletedTarget(target, mark.deletion)
  );
  return answer === undefined || targets === undefined ? undefined : { ...answer, targets };
}

/** `target`, a member naming a post or a block of it, as the reference-targets read answers it
 *  once the post is deleted: the post's member named `""`, the body the delete cleared, and a
 *  block's gone, since the delete takes every block out of the post, with no words and no tree;
 *  either carries the delete's mark, and the time of an edit before it as it stood. */
function deletedTarget(target: ReferenceTarget, deleted: PostDeletion): ReferenceTarget {
  const { deleted_by: _by, text_tree: _tree, ...kept } = target;
  return {
    ...kept,
    ...deletionMarks(deleted),
    gone: target.kind === "block",
    name: "",
  };
}
