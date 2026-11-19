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
 * (`wholeEmailLinks` in `packages/envoy/internal/dispatch/pmdoc/autolink_transform.go`), so the
 * two still agree. It reads an email link that a reader made from a bare address, and the run of
 * characters that RFC 5322 allows in a local part, its `atext` (``A-Za-z0-9!#$%&'*+-/=?^_`{|}~``)
 * and `.`, in the decoded text just before the link: the run ends at the link's own local part, so
 * with it, it ends at the `@`. A character reference counts as its character, and so does an
 * escape. The walk back stops at the first character outside that set (whitespace, a line break,
 * a letter outside ASCII such as `é`, any other character). A link a reader made from bare text
 * (`bareLinkText`: a web address, a `www.` one or an email) is no markup, so the walk goes on
 * through its text as through text, except where that link, written bare, would read on into the
 * character after it (`readsOnInto`): an email link where that character continues its domain, a
 * web link where the URL search would run on past it. There it ends the run as the writer's markup
 * does, and so does any other node: the run continues into the node only when the node's text
 * ends in a character of the set (`endsInRunCharacter`), and then markup or that link splits the
 * address, as in `*agent*-hosts@example.com`, `www\.a'b@c.test` or
 * `x@y.co&#45;%hosts@example.com`; the run is not one run, and the link stays as remark-gfm made
 * it. Before any other node, as `` `SA:` `` or `<br>`, and at the start of the block, the run
 * starts there. Then, when every character of the run before the link's own text is one GFM links
 * in a local part (`[-.+_A-Za-z0-9]`), the link takes the run: `x agent&#45;hosts@example.com`
 * links `agent-hosts@example.com`; but a run of GFM's characters that reaches into a link a reader
 * made leaves both links as they are, since one link never takes another's text
 * (`a@b.co+c@d.co`). When one is not, the link goes, and its text joins the text around it, so
 * the address reads as written, while a link a reader made that the run reaches into stays: the
 * `/` of `projects/p/serviceAccounts/agent-hosts@example.com` and of
 * `alice@example.com/bob-smith@example.com`, the `%` of `a.b+c_d%e-f@example.com` and the `&` of
 * `agent&amp;hosts@example.com` are local-part characters GFM does not link. Except: where that
 * run holds such a character and a web address that GFM links in the joined text (one starting
 * www., http:// or https://) reaches into the address, the email link the reader made stays, as
 * the base stores it, and no web-address link is made (`webAddressReachesInto`):
 * `q/&#45;www.a.bhosts@example.com` keeps its link to `www.a.bhosts@example.com`, since both
 * readers link the web address in `q/-www.a.bhosts@example.com`, however its dots are spelled. A
 * second exception, because the renderer cannot write that earlier link bare there: where the
 * earlier link the reader made stands next to an email link the writer wrote (`[…](mailto:…)` or
 * `<…@…>`), or its text starts `_www.`, the base's reading stays (`writtenExplicitOnly`): that
 * link ends the run as the writer's markup does. With no run before the link nothing changes:
 * `xé-agent@example.com` keeps `-agent@example.com`, since `é` ends the walk. A link the writer
 * wrote, `[…](mailto:…)`, `[…][label]` or `<…>`, is never changed.
 *
 * Import only, as `./nested-links.ts` is: a unified transformer runs on parse and never on
 * `stringify`, so registering it on the editor's shared remark processor cannot change what the
 * editor writes.
 */

import type { Root } from "@milkdown/kit/transformer";
import { $remark } from "@milkdown/kit/utils";
import { asciiAtext, unicodePunctuation, unicodeWhitespace } from "micromark-util-character";
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

/*
 * A copy of the URL search of mdast-util-gfm-autolink-literal 2.0.1, the version bun.lock pins,
 * from its `lib/index.js`: the URL pattern `transformGfmAutolinkLiterals` searches and its private
 * `findUrl`, `isCorrectDomain`, `splitUrl` and `previous`, which the package does not export.
 * `tests/whole-email-links-url-search.test.ts` holds the copy to that package's own transform, as
 * remark-gfm runs it.
 */

const URL_PATTERN = /(https?:\/\/|www(?=\.))([-.\w]+)([^ \t\r\n]*)/gi;

/** `findUrl`: the text of the link the transform makes of `match`, or false where it makes none. */
function findUrl(protocol: string, domain: string, path: string, match: RegExpExecArray) {
  if (!previous(match)) return false;
  if (/^w/i.test(protocol)) {
    domain = protocol + domain;
    protocol = "";
  }
  if (!isCorrectDomain(domain)) return false;
  const parts = splitUrl(domain + path);
  if (!parts[0]) return false;
  return protocol + parts[0];
}

function isCorrectDomain(domain: string): boolean {
  const parts = domain.split(".");
  const last = parts[parts.length - 1];
  const nextToLast = parts[parts.length - 2];
  return !(
    parts.length < 2 ||
    (last && (/_/.test(last) || !/[a-zA-Z\d]/.test(last))) ||
    (nextToLast && (/_/.test(nextToLast) || !/[a-zA-Z\d]/.test(nextToLast)))
  );
}

function splitUrl(url: string): [string, string | undefined] {
  const trailExec = /[!"&'),.:;<>?\]}]+$/.exec(url);
  if (!trailExec) return [url, undefined];
  url = url.slice(0, trailExec.index);
  let trail = trailExec[0];
  let closingParenIndex = trail.indexOf(")");
  const openingParens = url.split("(").length - 1;
  let closingParens = url.split(")").length - 1;
  while (closingParenIndex !== -1 && openingParens > closingParens) {
    url += trail.slice(0, closingParenIndex + 1);
    trail = trail.slice(closingParenIndex + 1);
    closingParenIndex = trail.indexOf(")");
    closingParens++;
  }
  return [url, trail];
}

/** `previous`, for a URL: the match starts the text, or follows whitespace or punctuation. */
function previous(match: RegExpExecArray): boolean {
  const code = match.input.charCodeAt(match.index - 1);
  return match.index === 0 || unicodeWhitespace(code) || unicodePunctuation(code);
}

/**
 * Each web address the transform links in `value`, as the offsets of its link's text, searched
 * as `mdast-util-find-and-replace` searches the pattern: a match `findUrl` refuses is searched
 * again one character after where it began, and the search goes on after a match it takes.
 */
export function webAddressesIn(value: string): [number, number][] {
  const found: [number, number][] = [];
  const pattern = new RegExp(URL_PATTERN.source, URL_PATTERN.flags);
  for (let match = pattern.exec(value); match !== null; match = pattern.exec(value)) {
    const url = findUrl(match[1], match[2], match[3], match);
    if (url === false) {
      pattern.lastIndex = match.index + 1;
    } else {
      found.push([match.index, match.index + url.length]);
    }
  }
  return found;
}

/**
 * Whether `joined`, the text a link that went would leave, holds a web address the transform
 * links that reaches into `joined.slice(from, to)`, the link's own text, so that the link stays
 * (the rule's exception): a fresh read of that text would link the web address, which no
 * spelling of it stops, since both readers search the decoded text.
 */
function webAddressReachesInto(joined: string, from: number, to: number): boolean {
  return webAddressesIn(joined).some(([start, end]) => start < to && end > from);
}

/**
 * Whether `children[at]`, a link remark-gfm made from bare text whose text is `label`, would read
 * on into the text after it were it written bare, so that it ends an email's run as the writer's
 * markup does: an email link where the character after it continues the email pattern's domain,
 * a `-`, `_`, letter or digit, or a `.` before one; a web link where the transform's URL search
 * (`webAddressesIn`), over its text and the text after it, links no address that starts and ends
 * where the link does. The text after it is the text nodes and the text of the links remark-gfm
 * made, up to any other node.
 */
function readsOnInto(children: MdastNode[], at: number, label: TextNode, source: string): boolean {
  let joined = label.value;
  for (let next = at + 1; next < children.length; next += 1) {
    const node = children[next];
    const value = isText(node) ? node.value : bareLinkText(node, source)?.value;
    if (value === undefined) break;
    joined += value;
  }
  if (children[at].url === `mailto:${label.value}`) {
    return /^(?:[-\w]|\.[-\w])/.test(joined.slice(label.value.length));
  }
  const [first] = webAddressesIn(joined);
  return first === undefined || first[0] !== 0 || first[1] !== label.value.length;
}

/**
 * Whether `children[at]`, a link remark-gfm made from bare text whose text is `label`, is one the
 * renderer cannot write bare, so that it ends an email's run as the writer's markup does, decided
 * from the tree: it stands next to an email link the writer wrote, the node before it, past text
 * made only of a run's characters (`isAtext`), being a link `bareLinkText` does not take whose url
 * is a `mailto:` URL; or its text starts `_` and goes on `www.`, in any case. Beside the writer's
 * email link the renderer writes both explicit or both bare, and either reads back otherwise; and
 * it escapes the `_`, after which both readers read the text from `www.` as a web address.
 */
function writtenExplicitOnly(
  children: MdastNode[],
  at: number,
  label: TextNode,
  source: string
): boolean {
  if (/^_www\./i.test(label.value)) return true;
  for (let before = at - 1; before >= 0; before -= 1) {
    const node = children[before];
    if (!isText(node)) {
      return (
        node.type === "link" &&
        bareLinkText(node, source) === undefined &&
        /^mailto:/i.test(node.url ?? "")
      );
    }
    for (let index = 0; index < node.value.length; index += 1) {
      if (!isAtext(node.value.charCodeAt(index))) return false;
    }
  }
  return false;
}

/**
 * Applies the rule to the children of `parent`, at any depth, in order, so a text that a removed
 * link joined is the text before the next link. The text before a link is the engine's one text
 * node there, so the node before that text, if any, is no text. A node this changes loses its
 * position, as the nodes the transform splits off do: its offsets would no longer be the
 * source's. A parent whose links all stand keeps every node and the array it came in with.
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
    // whether it reaches into a link remark-gfm made that it reads through, and `split` whether
    // markup, or a link remark-gfm made that would read on into it or that the renderer writes
    // explicit only, splits the address.
    const before = children[index - 1];
    let start = 0;
    let length = 0;
    let linked = true;
    let crossed = false;
    let split = false;
    for (let at = index - 1; at >= 0; at -= 1) {
      const node = children[at];
      const label = isText(node) ? undefined : bareLinkText(node, source);
      const through =
        label !== undefined &&
        !readsOnInto(children, at, label, source) &&
        !writtenExplicitOnly(children, at, label, source);
      const value = isText(node) ? node.value : through ? label.value : undefined;
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
      crossed ||= through && end < value.length;
      if (at === index - 1) start = end;
      if (end > 0) break;
    }
    if (length === 0 || split || (linked && crossed)) {
      // No run, one that markup or a link that does not read through splits, or one of GFM's
      // characters that reaches into a link remark-gfm made, which this does not join to the
      // email's: the link stands.
      index += 1;
      continue;
    }
    const after = children[index + 1];
    if (!linked) {
      // The link goes, unless a web address would then reach into its text: its text and the
      // text after it, if any, join the text before it.
      const head = isText(before) ? before.value : "";
      const joined = head + address.value + (isText(after) ? after.value : "");
      if (webAddressReachesInto(joined, head.length, head.length + address.value.length)) {
        index += 1;
        continue;
      }
      if (isText(before)) {
        before.value = joined;
        delete before.position;
        children.splice(index, isText(after) ? 2 : 1);
        continue;
      }
      address.value = joined;
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
