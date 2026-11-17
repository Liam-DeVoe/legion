import { primarySpec, roleDocument } from "../../../api/issue-cache";
import type {
  ArtifactBlock,
  BlockAttributeValue,
  DisplayTree,
  IssueDetails,
} from "../../../api/types";
import { compiledBlockSchema } from "../../doc/schema";
import type { TreeAnswer, TreeAnswerNode, TreeFilter } from "./tree-read";

/** A block as the blocks read answers one, with the words the tree read answers beside it, so a
 *  reader of the blocks read reads a block of the tree read with no change. */
interface TreeBlock extends ArtifactBlock {
  readonly text: string;
  readonly text_tree?: DisplayTree;
}

/** The node kinds each role's query takes (dispatch://INFRA-1253 R8): what plays the role on an
 *  issue that has converted, where no block names the document it came from. */
const ROLE_KINDS: Readonly<Record<TreeFilter, readonly string[]>> = {
  plan: ["plan_task"],
  spec: ["criterion", "requirement"],
  verification: ["check"],
};

/** A node the tree read answers as a typed block of a document. */
type PlacedNode = TreeAnswerNode & { readonly document: NonNullable<TreeAnswerNode["document"]> };

/**
 * The id of the document that plays `role` for the issue: the one the issue read names
 * (`primarySpec`, `roleDocument`), which is the server's own answer to which document plays the
 * part, as `useIssueTree` reads it. The issue read's two role fields go once every caller reads
 * the tree read's ids instead (dispatch://INFRA-1253 R8), and while the issue has not converted
 * the tree read answers the same ids (`documents`), so those stand where the issue read names
 * none.
 */
function roleDocumentId(
  answer: TreeAnswer,
  role: TreeFilter,
  issue: IssueDetails
): string | undefined {
  const named = role === "spec" ? primarySpec(issue) : roleDocument(issue, role);
  return named?.id ?? (answer.converted ? undefined : answer.documents?.[role]);
}

/** An attribute's value as the blocks read gives it, the text the block's opening line writes:
 *  a pointer list as its references joined by one space (`parseBlockPointerValue` reads it
 *  back), a number or a flag as its text. A value with no one line of text - none, or a record
 *  such as an actor - is left out, as the opening line leaves it out. */
function attributeText(value: unknown): string | undefined {
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  if (Array.isArray(value)) return value.map(String).join(" ");
  return undefined;
}

/** A node's attributes as the blocks read lists a block's: each with the kind the block schema
 *  declares it (`block[]` is what a reader follows as pointers), in the order the schema
 *  declares them, then any the schema does not declare, which read as text. */
function attributesOf(node: TreeAnswerNode): BlockAttributeValue[] {
  const declared = compiledBlockSchema.types.find(
    (type) => type.name === node.supertag
  )?.attributes;
  const names = new Set([...Object.keys(declared ?? {}), ...Object.keys(node.attributes)]);
  return [...names].flatMap((name) => {
    const value = attributeText(node.attributes[name]);
    return value === undefined ? [] : [{ kind: declared?.[name]?.kind ?? "string", name, value }];
  });
}

/**
 * One node as the blocks read answers a block. The tree read answers no markdown range and no
 * edit token: both ends of the range are the block's place (its `document.position`, else its
 * place in the walk), which keeps the order a reader sorts the blocks by (`from`), and the token
 * is empty, so a block from here is never an edit's precondition. The comments and asks that
 * reference it are the ones the read hangs under it.
 */
function blockOf(
  node: TreeAnswerNode,
  place: number,
  hung: ReadonlyMap<string, { asks: number; comments: number }>
): TreeBlock {
  return {
    attributes: attributesOf(node),
    from: place,
    id: node.id,
    references: hung.get(node.id) ?? { asks: 0, comments: 0 },
    text: node.text,
    text_tree: node.text_tree,
    to: place,
    token: "",
    type: node.supertag,
  };
}

/** How many asks and comments the read hangs under each node, by the node's id. */
function hungUnder(answer: TreeAnswer): Map<string, { asks: number; comments: number }> {
  const counts = new Map<string, { asks: number; comments: number }>();
  for (const node of answer.nodes) {
    if (node.parent === null || (node.supertag !== "ask" && node.supertag !== "comment")) continue;
    const count = counts.get(node.parent) ?? { asks: 0, comments: 0 };
    if (node.supertag === "ask") count.asks += 1;
    else count.comments += 1;
    counts.set(node.parent, count);
  }
  return counts;
}

/**
 * The typed blocks of the document that plays `role` for the issue, from the tree read, in the
 * shape the blocks read of that document (`artifactBlocksQuery`) answered them, so a reader of
 * that read reads these with no change; undefined while there is no answer, as that read's data
 * is before it answers.
 *
 * The tree read answers each typed block of a role document once, with a `document` member
 * naming its document and its place there (dispatch://INFRA-1253 R8), so the role's blocks are
 * the nodes whose `document` names the role's document (`roleDocumentId`), in their place's
 * order. An issue that has converted may answer none that way: its documents are nodes of the
 * issue's own tree, and the issue read may name the role's query rather than a document. Its
 * role's blocks are then the nodes of this issue that role's query takes (`ROLE_KINDS`), in the
 * walk's order, which is the order the query reads them in.
 */
export function blocksOfRole(
  answer: TreeAnswer | undefined,
  role: TreeFilter,
  issue: IssueDetails
): ArtifactBlock[] | undefined {
  if (answer === undefined) return undefined;
  const hung = hungUnder(answer);
  const documentId = roleDocumentId(answer, role, issue);
  const placed = answer.nodes.filter(
    (node): node is PlacedNode =>
      documentId !== undefined && node.document?.artifact_id === documentId
  );
  if (placed.length > 0 || !answer.converted) {
    return placed
      .sort((left, right) => left.document.position - right.document.position)
      .map((node) => blockOf(node, node.document.position, hung));
  }
  const kinds = ROLE_KINDS[role];
  return answer.nodes.flatMap((node, place) =>
    node.issue === undefined && kinds.includes(node.supertag) ? [blockOf(node, place, hung)] : []
  );
}
