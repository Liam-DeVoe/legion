/**
 * Links an email address whole, or not at all, on markdown import.
 *
 * remark-gfm makes a link of a bare email address in two places, and either can start it inside
 * the address the writer wrote, so that the link names another address. Its tokenizer
 * (micromark-extension-gfm-autolink-literal) starts an address after any character that is
 * neither a local-part character nor `/`, and a character reference's `;` is one of those:
 * `x agent&#45;hosts@example.com` links `hosts@example.com`. Its transform
 * (mdast-util-gfm-autolink-literal's `findEmail`), which reads the decoded text the tokenizer
 * left, refuses an address right after a `/`, then searches again one character later and takes
 * the first start after a `-`, `.`, `+` or `_`, each of which it counts as punctuation:
 * `projects/p/serviceAccounts/agent-hosts@example.com` links `hosts@example.com`.
 *
 * The rule here is Dispatch's own, and Go's reader of the same document applies it too
 * (`packages/envoy/internal/dispatch/pmdoc/linkify.go`), so the two still agree. It reads an email
 * link that a reader made from a bare address, and the run of characters that RFC 5322 allows in
 * a local part, its `atext` (``A-Za-z0-9!#$%&'*+-/=?^_`{|}~``) and `.`, in the decoded text just
 * before the link: the run ends at the link's own local part, so with it, it ends at the `@`. A
 * character reference counts as its character, and so does an escape. The walk back stops at the
 * first character outside that set (whitespace, a line break, a letter outside ASCII such as `é`,
 * any other character). A link a reader made from bare text (`bareLinkText`: a web address, a
 * `www.` one or an email) is no markup, so the walk goes on through its text as through text.
 * Any other node ends the walk: the run continues into it only when its text ends in a character
 * of the set (`endsInRunCharacter`), and then the writer split the address with markup, as in
 * `*agent*-hosts@example.com`, the run is not one run, and the link stays as remark-gfm made it.
 * Before any other node, as `` `SA:` `` or `<br>`, and at the start of the block, the run starts
 * there. Then, when every character of the run before the link's own text is one GFM links in a
 * local part (`[-.+_A-Za-z0-9]`), the link takes the run: `x agent&#45;hosts@example.com` links
 * `agent-hosts@example.com`. When one is not, the link goes, and its text joins the text around
 * it, so the address reads as written: the `/` of
 * `projects/p/serviceAccounts/agent-hosts@example.com`, the `%` of `a.b+c_d%e-f@example.com` and
 * the `&` of `agent&amp;hosts@example.com` are local-part characters GFM does not link, and
 * `www\.a'b@c.test` keeps its link to `www.a` and loses `b@c.test`. A run of GFM's characters
 * that reaches into a link a reader made leaves both links as they are, since one link never
 * takes another's text. With no run before the link nothing changes: `xé-agent@example.com`
 * keeps `-agent@example.com`, since `é` ends the walk. A link the writer wrote, `[…](mailto:…)`,
 * `[…][label]` or `<…>`, is never changed.
 *
 * Import only, as `./nested-links.ts` is: a unified transformer runs on parse and never on
 * `stringify`, so registering it on the editor's shared remark processor cannot change what the
 * editor writes.
 */

import type { Root } from "@milkdown/kit/transformer";
import { $remark } from "@milkdown/kit/utils";
import { asciiAtext } from "micromark-util-character";
import { codes } from "micromark-util-symbol";
import type { Transformer } from "unified";
import { markdownSource } from "./typed-block-drawing";

type MdastNode = {
  type: string;
  url?: string;
  value?: string;
  children?: MdastNode[];
  position?: { start?: { offset?: number } };
};

/** A text node, whose `value` is always there. */
type TextNode = MdastNode & { value: string };

/** Whether `code` is a character an address's local part may hold, as remark-gfm counts them. */
function isLocalPart(code: number): boolean {
  return (
    code === 43 || // +
    code === 45 || // -
    code === 46 || // .
    code === 95 || // _
    (code >= 48 && code <= 57) ||
    (code >= 65 && code <= 90) ||
    (code >= 97 && code <= 122)
  );
}

/**
 * Whether `code` is a character RFC 5322 allows in a local part: its `atext`, or `.`. micromark's
 * `asciiAtext` holds every one of them, `.` included, but `!`.
 */
function isAtext(code: number): boolean {
  return code === codes.exclamationMark || asciiAtext(code);
}

function isText(node: MdastNode | undefined): node is TextNode {
  return node?.type === "text" && typeof node.value === "string";
}

/**
 * Whether the text `node` shows ends in a character of an email's run (`isAtext`): a text's, an
 * inline code's or an inline HTML's own value ends in its last character, and a node holding
 * inline children (a link of any kind, emphasis, strong, strikethrough) in its last child's. An
 * image, a break and a footnote reference hold no text.
 */
function endsInRunCharacter(node: MdastNode): boolean {
  if (typeof node.value === "string") {
    return node.value !== "" && isAtext(node.value.charCodeAt(node.value.length - 1));
  }
  const last = node.children?.at(-1);
  return last !== undefined && endsInRunCharacter(last);
}

/**
 * The text of `node` when `node` is a link remark-gfm made from bare text, an email or a web
 * address, else undefined. Such a link holds one text, and its url is that text, or that text
 * after the scheme the reader adds: a `www.` link's `http://`, an email's `mailto:`. The
 * transform's link and its text carry no position: `findUrl` and `findEmail`
 * (`mdast-util-gfm-autolink-literal/lib/index.js:197`) build both, and
 * `mdast-util-find-and-replace/lib/index.js:164-193` splices them in as they are. The tokenizer's
 * link starts in the source at its address's first character, which is never a `[` or a `<`. A
 * link the writer wrote starts at its `[` or its `<`, even when its text starts with a `[` too,
 * or, when remark-inline-links made it from a reference, carries no position while its text, the
 * label's, does.
 */
function bareLinkText(node: MdastNode, source: string): TextNode | undefined {
  if (node.type !== "link" || node.children?.length !== 1) return undefined;
  const text = node.children[0];
  if (!isText(text) || text.value === "") return undefined;
  const url = node.url;
  if (url !== text.value && url !== `http://${text.value}` && url !== `mailto:${text.value}`) {
    return undefined;
  }
  if (node.position === undefined) return text.position === undefined ? text : undefined;
  const offset = node.position.start?.offset;
  if (offset === undefined) return undefined;
  const first = source.charCodeAt(offset);
  if (first === 91 /* [ */ || first === 60 /* < */) return undefined;
  return source[offset] === text.value[0] ? text : undefined;
}

/** The text of `node` when `node` is an email link remark-gfm made from a bare address. */
function bareAddress(node: MdastNode, source: string): TextNode | undefined {
  const text = bareLinkText(node, source);
  return text !== undefined && node.url === `mailto:${text.value}` ? text : undefined;
}

/**
 * Applies the rule to the children of `parent`, at any depth, in order, so a text that a removed
 * link joined is the text before the next link. The text before a link is the engine's one text
 * node there, so the node before that text, if any, is no text. The run reads a link remark-gfm
 * made from bare text as the text it shows: it is no markup the writer wrote. A node this changes
 * loses its position, as the nodes the transform splits off do: its offsets would no longer be
 * the source's. A parent whose links all stand keeps every node and the array it came in with.
 */
function wholeEmailLinks(parent: MdastNode, source: string): void {
  const children = parent.children;
  if (children === undefined) return;
  let index = 0;
  while (index < children.length) {
    const child = children[index];
    const address = bareAddress(child, source);
    if (address === undefined) {
      wholeEmailLinks(child, source);
      index += 1;
      continue;
    }
    // The run, back from the link: `length` characters, of which `linked` says whether GFM links
    // each in a local part, starting at `start` in the text right before the link. `crossed` is
    // whether it reaches into a link remark-gfm made, and `split` whether markup splits it.
    const before = children[index - 1];
    let start = 0;
    let length = 0;
    let linked = true;
    let crossed = false;
    let split = false;
    for (let at = index - 1; at >= 0; at -= 1) {
      const node = children[at];
      const label = isText(node) ? undefined : bareLinkText(node, source);
      const value = isText(node) ? node.value : label?.value;
      if (value === undefined) {
        split = endsInRunCharacter(node);
        break;
      }
      let end = value.length;
      while (end > 0 && isAtext(value.charCodeAt(end - 1))) {
        end -= 1;
        linked &&= isLocalPart(value.charCodeAt(end));
      }
      length += value.length - end;
      crossed ||= label !== undefined && end < value.length;
      if (at === index - 1) start = end;
      if (end > 0) break;
    }
    if (length === 0 || split || (linked && crossed)) {
      // No run, one that markup splits, or one of GFM's characters that reaches into a link
      // remark-gfm made, which this does not join to the email's: the link stands.
      index += 1;
      continue;
    }
    const after = children[index + 1];
    if (!linked) {
      // The link goes: its text and the text after it, if any, join the text before it.
      const tail = isText(after) ? after.value : "";
      if (isText(before)) {
        before.value += address.value + tail;
        delete before.position;
        children.splice(index, isText(after) ? 2 : 1);
        continue;
      }
      address.value += tail;
      delete address.position;
      children.splice(index, isText(after) ? 2 : 1, address);
      index += 1;
      continue;
    }
    // The link takes the run, which lies in the text before it, and that text keeps what stands
    // before the run.
    if (!isText(before)) throw new Error("a run of the text before a link lies in no text");
    address.value = before.value.slice(start) + address.value;
    child.url = `mailto:${address.value}`;
    delete child.position;
    delete address.position;
    if (start === 0) {
      children.splice(index - 1, 1);
      continue;
    }
    before.value = before.value.slice(0, start);
    delete before.position;
    index += 1;
  }
}

/** remark transformer - both of the engine's parse seams register it; `stringify` never runs it. */
export function remarkWholeEmailLinks() {
  return (tree: MdastNode, file: { toString(): string }) => {
    wholeEmailLinks(tree, markdownSource(file));
  };
}

export const libraryRemarkWholeEmailLinksPlugin = $remark(
  "remarkWholeEmailLinks",
  () => remarkWholeEmailLinks as () => Transformer<Root, Root>
);
