package eval

import (
	"cmp"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/text"
)

// render.go writes an answer as the person reads it in the command bar (RenderAnswer). The bar's
// conversation draws an answer with chips (the web's MarkdownBody with chips, ItemChip and
// RefLink): each link to an item shows the item, read from Dispatch, in place of the words the
// model gave the link. The judge reads the answer so, and a fact the person reads in a chip is
// stated, and words the chip replaced are not.

// Items is what RenderAnswer reads of a store: an issue's status and title by its key, the name of
// a document or a file by its owner (an issue's key or a project's key) and its slug, and the text
// of an ask, a comment or a message by the id Dispatch stores it under, a message on its issue. A
// Store answers it for a try's store after its run; a Seed for the seed as it stands before any
// task, which the calibration answers of labels.json are read against.
type Items interface {
	chipIssue(key string) (status, title string, found bool)
	documentName(owner, slug string) (string, bool)
	itemText(kind, issue, id string) (string, bool)
}

// RenderAnswer is answer, an answer's markdown, with each link the bar draws as an item written in
// place as the words the bar draws for it, read from items, between square brackets. A link to an
// issue (dispatch://DEMO-12, /issues/DEMO-12) is the issue's chip: its key, its status as the
// chip words it and its title, [DEMO-12 · In progress · Export: CSV and PDF]. A link to an issue's
// spec or to a tab of its page is the issue's title; to a document or a file of an issue or a
// project, its name; to an ask, a comment or a message, the head of its text, cut as the bar cuts
// a link's title (itemHeadline). A link to an item that items does not hold, and a link to a block
// or to an agent's file, which items holds no words of, is its bare reference, such as [DEMO-404],
// so it carries no fact the store does not hold. Every other link - a web address, a picture, a
// reference the bar does not read - and all other text, code included, stays as written.
//
// The links are markdown's inline links, [words](href), and its autolinks, <dispatch://...>; a
// dashboard path names its item as it does on the dashboard's own origin. A web address names no
// item: the bar draws one as a chip only on its own origin, which an answer gives as a path.
func RenderAnswer(answer string, items Items) string {
	var out strings.Builder
	out.Grow(len(answer))
	fence, from := "", 0
	for at := 0; at < len(answer); {
		end := strings.IndexByte(answer[at:], '\n') + 1
		if end == 0 {
			end = len(answer) - at
		}
		line := answer[at : at+end]
		if fence == "" {
			if opened := codeFence(line); opened != "" {
				renderInline(&out, answer[from:at], items)
				fence, from = opened, at
			}
		} else if closesFence(line, fence) {
			out.WriteString(answer[from : at+end])
			fence, from = "", at+end
		}
		at += end
	}
	if fence != "" {
		out.WriteString(answer[from:])
	} else {
		renderInline(&out, answer[from:], items)
	}
	return out.String()
}

// fenceLine is a line that opens or closes a fenced code block: up to three spaces, then three
// backticks or tildes or more.
var fenceLine = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")

// codeFence is the fence line opens a fenced code block with, "" for a line that opens none: a
// backtick fence whose info string holds a backtick is no fence.
func codeFence(line string) string {
	match := fenceLine.FindStringSubmatch(line)
	if match == nil || (match[1][0] == '`' && strings.Contains(line[len(match[0]):], "`")) {
		return ""
	}
	return match[1]
}

// closesFence reports whether line closes the code block fence opened: a fence of the same mark,
// as long or longer, with nothing but spaces after it.
func closesFence(line, fence string) bool {
	match := fenceLine.FindStringSubmatch(line)
	return match != nil && match[1][0] == fence[0] && len(match[1]) >= len(fence) && strings.TrimSpace(line[len(match[0]):]) == ""
}

// renderInline writes markdown text that holds no fenced code block, each chip link in it as its
// chip's words: code spans and escaped characters as written, and a picture whole.
func renderInline(out *strings.Builder, markdown string, items Items) {
	for at := 0; at < len(markdown); {
		switch c := markdown[at]; {
		case c == '\\' && at+1 < len(markdown) && asciiPunct(markdown[at+1]):
			out.WriteString(markdown[at : at+2])
			at += 2
		case c == '`':
			end := codeSpanEnd(markdown, at)
			out.WriteString(markdown[at:end])
			at = end
		case c == '!' && strings.HasPrefix(markdown[at+1:], "["):
			end := at + 2
			if linkEnd, _, ok := inlineLink(markdown, at+1); ok {
				end = linkEnd
			}
			out.WriteString(markdown[at:end])
			at = end
		case c == '[':
			end, href, ok := inlineLink(markdown, at)
			if !ok {
				out.WriteByte(c)
				at++
				continue
			}
			writeLink(out, markdown[at:end], href, items)
			at = end
		case c == '<':
			end := strings.IndexByte(markdown[at:], '>')
			if end < 0 || !autolink.MatchString(markdown[at+1:at+end]) {
				out.WriteByte(c)
				at++
				continue
			}
			writeLink(out, markdown[at:at+end+1], markdown[at+1:at+end], items)
			at += end + 1
		default:
			out.WriteByte(c)
			at++
		}
	}
}

// writeLink writes a link, written as written, as its chip's words where href names an item the
// bar draws, and as written otherwise.
func writeLink(out *strings.Builder, written, href string, items Items) {
	named, ok := parseTarget(href)
	if !ok {
		out.WriteString(written)
		return
	}
	out.WriteString("[" + named.words(items) + "]")
}

// autolink is the inside of a markdown autolink: a scheme, a colon and no space or angle bracket.
var autolink = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]{1,31}:[^\x00-\x20<>]*$`)

// codeSpanEnd is where the code span that the run of backticks at start opens ends: after the next
// run of as many backticks, or after the run itself, which is then text, where none follows.
func codeSpanEnd(markdown string, start int) int {
	run := start
	for run < len(markdown) && markdown[run] == '`' {
		run++
	}
	width := run - start
	for at := run; at < len(markdown); {
		if markdown[at] != '`' {
			at++
			continue
		}
		closing := at
		for closing < len(markdown) && markdown[closing] == '`' {
			closing++
		}
		if closing-at == width {
			return closing
		}
		at = closing
	}
	return run
}

// inlineLink reads the inline link whose words open at the '[' at open: its words to their
// matching ']', then '(', the href (in angle brackets or bare, its parentheses balanced), an
// optional title, and ')'. It answers where the link ends and its href, and false where open
// starts no inline link.
func inlineLink(markdown string, open int) (int, string, bool) {
	depth, at := 0, open
	for ; at < len(markdown); at++ {
		switch markdown[at] {
		case '\\':
			at++
		case '`':
			at = codeSpanEnd(markdown, at) - 1
		case '[':
			depth++
		case ']':
			depth--
		}
		if depth == 0 {
			break
		}
	}
	if at+1 >= len(markdown) || markdown[at+1] != '(' {
		return 0, "", false
	}
	at = skipSpace(markdown, at+2)
	var href string
	if at < len(markdown) && markdown[at] == '<' {
		end := strings.IndexAny(markdown[at+1:], ">\n")
		if end < 0 || markdown[at+1+end] != '>' {
			return 0, "", false
		}
		href, at = markdown[at+1:at+1+end], at+end+2
	} else {
		start, parens := at, 0
		for ; at < len(markdown); at++ {
			c := markdown[at]
			if c == '\\' && at+1 < len(markdown) && asciiPunct(markdown[at+1]) {
				at++
				continue
			}
			if c <= ' ' || (c == ')' && parens == 0) {
				break
			}
			switch c {
			case '(':
				parens++
			case ')':
				parens--
			}
		}
		href = markdown[start:at]
	}
	at = skipSpace(markdown, at)
	if at < len(markdown) && strings.IndexByte(`"'(`, markdown[at]) >= 0 {
		closing := markdown[at]
		if closing == '(' {
			closing = ')'
		}
		end := strings.IndexByte(markdown[at+1:], closing)
		if end < 0 {
			return 0, "", false
		}
		at = skipSpace(markdown, at+end+2)
	}
	if at >= len(markdown) || markdown[at] != ')' {
		return 0, "", false
	}
	return at + 1, href, true
}

// skipSpace is the first byte at or after at that is no space, tab or line feed.
func skipSpace(markdown string, at int) int {
	for at < len(markdown) && (markdown[at] == ' ' || markdown[at] == '\t' || markdown[at] == '\n') {
		at++
	}
	return at
}

// pageOrigin stands for the dashboard's origin, which a dashboard path in an answer is read
// against, as the browser reads it against its own.
const pageOrigin = "http://dispatch.invalid"

// issueTab is a link to a tab of an issue's page, which the bar draws as the issue's title: by
// its reference (dispatch://DEMO-3/plan; the conversation is also dispatch://DEMO-3/log) or by its
// dashboard path (/issues/DEMO-3/plan). The Decisions tab is its document, decisions.md, which
// text.ParseRef reads.
var issueTab = regexp.MustCompile(`^(?:dispatch://|/issues/)([A-Z][A-Z0-9]{1,9}-[1-9][0-9]*)/(verification|plan|steps|activity|children|artifacts|collaborators|conversation|log)$`)

// target is what a link names as the bar reads its href: the item a reference or a dashboard path
// names (text.ParseRef), or, where tab is set, that tab of the page of the issue ref names.
type target struct {
	ref text.Ref
	tab string
}

// parseTarget is what href names, and false for an href that names no item the bar draws: a web
// address, a reference of a kind the bar does not read (a component), or no reference.
func parseTarget(href string) (target, bool) {
	value, serverURL := href, ""
	switch {
	case strings.HasPrefix(href, "dispatch://"):
	case strings.HasPrefix(href, "/") && !strings.HasPrefix(href, "//"):
		value, serverURL = pageOrigin+href, pageOrigin
	default:
		return target{}, false
	}
	page := href
	if !strings.HasPrefix(href, "dispatch://") {
		page, _, _ = strings.Cut(page, "#")
		page, _, _ = strings.Cut(page, "?")
	}
	if match := issueTab.FindStringSubmatch(page); match != nil {
		if match[2] == "log" && !strings.HasPrefix(href, "dispatch://") {
			return target{}, false
		}
		tab := match[2]
		if tab == "log" {
			tab = "conversation"
		}
		return target{ref: text.Ref{Kind: "issue", IssueKey: match[1], ID: match[1]}, tab: tab}, true
	}
	ref, ok := text.ParseRef(value, serverURL)
	if !ok || (ref.Kind != "issue" && ref.Kind != "artifact" && ref.Kind != "ask" && ref.Kind != "comment" &&
		ref.Kind != "message" && ref.Kind != "block") {
		return target{}, false
	}
	return target{ref: ref}, true
}

// words is what the bar draws for the target, read from items, or its bare reference where items
// holds no words of it.
func (t target) words(items Items) string {
	ref := t.ref
	switch {
	case ref.Kind == "issue":
		status, title, found := items.chipIssue(ref.IssueKey)
		switch {
		case !found:
		case t.tab != "":
			return title
		default:
			return ref.IssueKey + " · " + statusText(status) + " · " + title
		}
	case ref.Kind == "artifact" && ref.Session == "" && ref.IssueKey != "" && ref.ID == "spec":
		// The bar titles a link to an issue's spec with the issue's title.
		if _, title, found := items.chipIssue(ref.IssueKey); found {
			return title
		}
	case ref.Kind == "artifact" && ref.Session == "":
		if name, found := items.documentName(ref.IssueKey+ref.Project, ref.ID); found {
			return name
		}
	case ref.Kind == "ask" || ref.Kind == "comment" || ref.Kind == "message":
		if written, found := items.itemText(ref.Kind, ref.IssueKey, ref.ID); found {
			return itemHeadline(written)
		}
	}
	return t.reference()
}

// reference is the target's bare reference, as a task names an item (Seed.Refs): DEMO-12,
// DEMO-3/spec, DEMO-16/artifact/<slug>, OPS/artifact/<slug>, DEMO-3/ask/<id>,
// OPS/artifact/<slug>/comment/<id>, agent/<session>/artifact/<slug>, DEMO-3/plan, and a block's
// as its item's with #b-<block id>.
func (t target) reference() string {
	ref := t.ref
	kind := ref.Kind
	if kind == "block" {
		kind = cmp.Or(ref.Item, "artifact")
	}
	var written string
	switch kind {
	case "issue":
		written = ref.IssueKey
		if t.tab != "" {
			written += "/" + t.tab
		}
	case "artifact":
		switch {
		case ref.Session != "":
			written = "agent/" + ref.Session + "/artifact/" + ref.ID
		case ref.IssueKey != "" && ref.ID == "spec":
			written = ref.IssueKey + "/spec"
		default:
			written = ref.IssueKey + ref.Project + "/artifact/" + ref.ID
		}
	default:
		if ref.Document != "" {
			written = ref.Project + "/artifact/" + ref.Document + "/" + kind + "/" + ref.ID
		} else {
			written = ref.IssueKey + "/" + kind + "/" + ref.ID
		}
	}
	if ref.Kind == "block" {
		written += "#b-" + ref.Block
		if ref.Pin > 0 {
			written += "@v" + strconv.Itoa(ref.Pin)
		}
	}
	return written
}

// short is the target's short form, which the bar writes for a reference inside the text of a
// link's title (the web's shortForm): DEMO-12, DEMO-3 spec, DEMO-16 <slug>, OPS/<slug>, DEMO-3
// ask, OPS/<slug> comment, DEMO-3 plan, agent/session:<8 characters>…/<slug>, and a block's as its
// item's with " block", and " v<N>" for a pinned one.
func (t target) short() string {
	ref := t.ref
	kind := ref.Kind
	if kind == "block" {
		kind = cmp.Or(ref.Item, "artifact")
	}
	var written string
	switch {
	case kind == "issue" && t.tab != "":
		written = ref.IssueKey + " " + strings.ReplaceAll(t.tab, "conversation", "log")
	case kind == "issue":
		written = ref.IssueKey
	case kind == "artifact" && ref.Session != "":
		return "agent/" + shortSessionID(ref.Session) + "/" + ref.ID
	case kind == "artifact" && ref.Project != "":
		written = ref.Project + "/" + ref.ID
	case kind == "artifact":
		written = ref.IssueKey + " " + ref.ID
	case ref.Document != "":
		written = ref.Project + "/" + ref.Document + " " + kind
	default:
		written = ref.IssueKey + " " + kind
	}
	if ref.Kind == "block" {
		written += " block"
		if ref.Pin > 0 {
			written += " v" + strconv.Itoa(ref.Pin)
		}
	}
	return written
}

// shortSessionID is how the bar writes a session by its id where it has no title (the contracts'
// shortSessionId): session: and its first eight characters, and the Legion daemon's identity
// whole.
func shortSessionID(id string) string {
	if strings.HasPrefix(id, "legion-daemon:") {
		return id
	}
	runes := []rune(id)
	return "session:" + string(runes[:min(len(runes), 8)]) + "…"
}

// statusWords is each status as the issue's chip words it (the web's statusText).
var statusWords = map[string]string{
	"triage": "Triage", "icebox": "Icebox", "backlog": "Backlog", "todo": "Todo", "in_progress": "In progress",
	"testing": "Testing", "needs_review": "Needs review", "retro": "Retro", "done": "Done",
}

// statusText is status as the chip words it: a status of the lifecycle by its words, any other as
// it is.
func statusText(status string) string {
	if words, ok := statusWords[status]; ok {
		return words
	}
	return status
}

// titleHeadlineMax is how many characters of an ask's, a comment's or a message's text the bar
// titles a link to it with (the web's TITLE_HEADLINE_MAX).
const titleHeadlineMax = 60

// itemHeadline is the title the bar gives a link to an ask, a comment or a message, from its text
// (the web's useMarkdownHeadline): the words of the text as Dispatch reads it for display, on one
// line, a reference outside code written as its short form, cut to titleHeadlineMax characters
// at the last space in their second half (the web's textHead), or at the limit where none is
// there, with an ellipsis after a cut.
func itemHeadline(markdown string) string {
	var words strings.Builder
	plainWords(&words, pmdoc.ReadForDisplay(markdown, pmdoc.DisplayLatest), false)
	line := strings.Join(strings.Fields(words.String()), " ")
	runes := []rune(line)
	if len(runes) <= titleHeadlineMax {
		return line
	}
	head := runes[:titleHeadlineMax+1]
	end := titleHeadlineMax
	for at := len(head) - 1; at >= 0; at-- {
		if unicode.IsSpace(head[at]) {
			if at >= titleHeadlineMax/2 {
				end = at
			}
			break
		}
	}
	return strings.TrimRightFunc(string(runes[:end]), unicode.IsSpace) + "…"
}

// plainWords writes the words of a display tree's descendants as the web's plainWords reads them:
// a text as it reads, its references shortened outside code; an inline HTML tag as its characters;
// and every other node a space.
func plainWords(out *strings.Builder, node *pmdoc.Node, code bool) {
	for _, child := range node.Children {
		switch child.Type {
		case "text":
			if code || hasMark(child.Marks, "inlineCode") {
				out.WriteString(child.Text)
			} else {
				out.WriteString(shortenReferences(child.Text))
			}
		case "html":
			value, _ := child.Attrs["value"].(string)
			out.WriteString(value)
		default:
			out.WriteString(" ")
		}
		plainWords(out, child, code || child.Type == "code_block")
	}
}

// hasMark reports whether marks hold one of the type named.
func hasMark(marks []pmdoc.Mark, name string) bool {
	for _, mark := range marks {
		if mark.Type == name {
			return true
		}
	}
	return false
}

// referenceSpan is a reference-shaped span of text, as the web's referenceSpans and the server's
// text package find one: a dispatch:// reference or a web address, up to a space, an angle or
// square bracket, a quote, a backtick or a pipe.
var referenceSpan = regexp.MustCompile("(?:dispatch://|https?://(?:\\[[0-9A-Fa-f:.]+\\])?)[^\\t\\n\\f\\r \\p{Z}<>\"'`\\[\\]|]+")

// shortenReferences is text with each dispatch:// reference in it that names an item written as
// its short form, the punctuation that trails it kept.
func shortenReferences(written string) string {
	return referenceSpan.ReplaceAllStringFunc(written, func(span string) string {
		if !strings.HasPrefix(span, "dispatch://") {
			return span
		}
		reference := trimReference(span)
		named, ok := parseTarget(reference)
		if !ok {
			return span
		}
		return named.short() + span[len(reference):]
	})
}

// trimReference drops what trails a reference in prose, as the server's text package does:
// sentence punctuation, the marks that close emphasis around it, and a closing parenthesis that
// opens nowhere in it.
func trimReference(raw string) string {
	opened, closed := strings.Count(raw, "("), strings.Count(raw, ")")
	end := len(raw)
	for ; end > 0; end-- {
		switch raw[end-1] {
		case '.', ',', ';', ':', '!', '?', '*', '_', '~':
		case ')':
			if opened >= closed {
				return raw[:end]
			}
			closed--
		default:
			return raw[:end]
		}
	}
	return ""
}

// chipIssue is the issue's status and title in the store.
func (s *Store) chipIssue(key string) (string, string, bool) {
	issue, found := s.Issues[key]
	return issue.Status, issue.Title, found
}

// documentName is the name of the document or file of owner, an issue's key or a project's key,
// with slug, in the store.
func (s *Store) documentName(owner, slug string) (string, bool) {
	for _, d := range s.Documents {
		if d.Slug == slug && (d.Issue == owner || (d.Issue == "" && d.Project == owner)) {
			return d.Name, true
		}
	}
	return "", false
}

// itemText is the question of the ask, or the text of the comment or of the message on issue,
// stored under id.
func (s *Store) itemText(kind, issue, id string) (string, bool) {
	switch kind {
	case "ask":
		ask, found := s.Asks[id]
		return ask.Question, found
	case "comment":
		comment, found := s.Comments[id]
		return comment.Body, found
	case "message":
		message, found := s.Messages[id]
		return message.Body, found && message.Issue == issue
	}
	return "", false
}

// chipIssue is the seeded issue's status and title.
func (s *Seed) chipIssue(key string) (string, string, bool) {
	issue, found := s.Issue(key)
	return issue.Status, issue.Title, found
}

// documentName is the name of the seeded document or file of owner with slug.
func (s *Seed) documentName(owner, slug string) (string, bool) {
	for _, a := range s.Artifacts {
		if a.Owner == owner && a.Slug == slug {
			return a.Name, true
		}
	}
	return "", false
}

// itemText is the question of the seeded ask, or the text of the seeded comment or of the message
// on issue, that Dispatch stores under id (ItemID), the comments WriteSeed writes for the seed's
// links of kind relates included.
func (s *Seed) itemText(kind, issue, id string) (string, bool) {
	switch kind {
	case "ask":
		for _, a := range s.Asks {
			if ItemID("ask", a.ID) == id {
				return a.Question, true
			}
		}
	case "comment":
		for _, c := range s.Comments {
			if ItemID("comment", c.ID) == id {
				return c.Body, true
			}
		}
		for _, link := range s.Links {
			if c := relatesComment(link); link.Kind == LinkRelates && ItemID("comment", c.ID) == id {
				return c.Body, true
			}
		}
	case "message":
		for _, m := range s.Messages {
			if ItemID("message", m.ID) == id && m.Issue == issue {
				return m.Body, true
			}
		}
	}
	return "", false
}
