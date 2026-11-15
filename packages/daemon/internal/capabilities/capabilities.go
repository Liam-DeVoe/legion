// Package capabilities is the one list of what a Legion worker can do (LEGION-578: every worker is
// a full agent). Each capability is checked at one site — in the image by `legion probe-image`
// (CheckImage), by each claimed session at an idle tick of the 5 s interval it already runs, which
// sends the row on a `claims/ready` of its own and the daemon renders (LEGION-663, INFRA-927), by
// the daemon from the deployment's configuration, or withheld by a cited issue — and the probe prints
// the whole table, one line per row, so an operator reads a single declared list in the probe
// pod's log rather than inferring what was checked from which probes happened to run.
package capabilities

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// Name is a capability as the probe prints it and legion.yaml names it.
type Name string

const (
	Subagents            Name = "subagents"
	EvalJS               Name = "eval-js"
	EvalPython           Name = "eval-python"
	Browser              Name = "browser"
	LSP                  Name = "lsp"
	CodeGraph            Name = "codegraph"
	WebSearch            Name = "web-search"
	Skills               Name = "skills"
	MCP                  Name = "mcp"
	RepositoryExtensions Name = "repository-extensions"
	DispatchEnvoyTools   Name = "dispatch-envoy-tools"
	GitHub               Name = "github"
	Secrets              Name = "secrets"
	ModelFallback        Name = "model-fallback"
	Toolchain            Name = "toolchain"
	ClaudeCode           Name = "claude-code"
	ResourceLimits       Name = "resource-limits"
	PoolCapacity         Name = "pool-capacity"
	Network              Name = "network"
	OperatorSetup        Name = "operator-setup"
	ProductionIdentities Name = "production-identities"
)

// Site is where a capability is checked.
type Site string

const (
	// SiteImage is checked inside the worker image by `legion probe-image` (CheckImage): the
	// image either carries what the capability needs or it does not.
	SiteImage Site = "image"
	// SiteLive is measured by a claimed session while it runs and is idle, and reported on
	// claims/ready (claim.ReadyRequest.Capabilities). The daemon renders the row from the live
	// sessions' reports, and a row no live session has reported reads unchecked
	// (Deployment.Sessions; LEGION-663, INFRA-927).
	SiteLive Site = "live"
	// SiteDeployment is decided by the deployment's legion.yaml, which the daemon reports: no
	// image or pod can show it.
	SiteDeployment Site = "deployment"
	// SiteWithheld is withheld by the cited issue: a worker never gets it, by design.
	SiteWithheld Site = "withheld"
)

// Capability is one row of the table.
type Capability struct {
	Name Name
	Site Site
	// Summary is one line: what a worker does with it.
	Summary string
	// Citation is the dispatch://LEGION-<n> a withheld row cites; for a live row, the issue whose
	// check proves it (never ""); "" otherwise.
	Citation string
}

// Table is every capability, in the order the probe prints them: the declared list, printed
// whole. Image rows are checked by CheckImage; the others are rendered as what they are.
var Table = []Capability{
	{Name: Subagents, Site: SiteLive, Summary: "dispatches task subagents, each on the model its role configures", Citation: "dispatch://LEGION-663"},
	{Name: EvalJS, Site: SiteImage, Summary: "evaluates JavaScript in Oh My Pi's own runtime"},
	{Name: EvalPython, Site: SiteImage, Summary: "evaluates Python through the interpreter `omp setup python` manages"},
	{Name: Browser, Site: SiteImage, Summary: "drives a headless Chromium through the browser tools"},
	{Name: LSP, Site: SiteImage, Summary: "reads diagnostics and symbols from the Go, TypeScript and Python language servers"},
	{Name: CodeGraph, Site: SiteImage, Summary: "queries the CodeGraph index for affected tests, impact and callers"},
	{Name: WebSearch, Site: SiteLive, Summary: "searches the web through the web-search tool", Citation: "dispatch://LEGION-663"},
	{Name: Skills, Site: SiteImage, Summary: "loads the skills Legion's prompts name"},
	{Name: MCP, Site: SiteLive, Summary: "reaches the MCP servers the deployment configures", Citation: "dispatch://LEGION-663"},
	{Name: RepositoryExtensions, Site: SiteLive, Summary: "loads the Oh My Pi extensions the repository it works carries", Citation: "dispatch://LEGION-629"},
	{Name: DispatchEnvoyTools, Site: SiteLive, Summary: "reaches Dispatch through the `dispatch` command and Envoy through the pi-envoy tools", Citation: "dispatch://LEGION-663"},
	{Name: GitHub, Site: SiteLive, Summary: "reads and writes GitHub through its plain `gh` and `git` on its role's App token file", Citation: "dispatch://LEGION-631"},
	{Name: Secrets, Site: SiteDeployment, Summary: "reads the secrets the deployment grants its pod generation through the agent-secrets broker"},
	{Name: ModelFallback, Site: SiteDeployment, Summary: "falls back to another model when its own is unavailable (retry.modelFallback)"},
	{Name: Toolchain, Site: SiteImage, Summary: "builds and runs with the generic toolchain: go, gcc, curl, wget, python3, node, bun, uv, kubectl, jq, ssh-keygen, flock, psql, redis-cli, sqlite3, nc and dig"},
	{Name: ClaudeCode, Site: SiteImage, Summary: "runs a phase worker on Claude Code: the claude-envoy plugin that worker loads, which the image carries, and the pin a pod installs Claude Code from at launch and verifies it against"},
	{Name: ResourceLimits, Site: SiteDeployment, Summary: "runs within the CPU and memory the deployment's pod resources give it"},
	{Name: PoolCapacity, Site: SiteDeployment, Summary: "starts in a Legion pool with room for the image probe's reservation at boot"},
	{Name: Network, Site: SiteWithheld, Summary: "reaches beyond the pod's own network: the pod is the boundary, except a workload a role starts where its deployment's cluster admits it, carrying the pod label that deployment's declaration names for a workload asking to be reachable, and the Service fronting that workload", Citation: "dispatch://LEGION-5"},
	{Name: OperatorSetup, Site: SiteWithheld, Summary: "installs or configures its own dependencies in the pod: Legion owns its dependencies", Citation: "dispatch://LEGION-200"},
	{Name: ProductionIdentities, Site: SiteWithheld, Summary: "acts under a production identity of its own", Citation: "dispatch://LEGION-551, dispatch://LEGION-205"},
}

// renderer renders one row of Table at each site but SiteWithheld, which render renders itself:
// every withheld row's state is the same shape wherever Table is walked, so it lives in one place.
type renderer struct {
	image      func(row Capability) State
	live       func(row Capability) State
	deployment func(row Capability) State
}

// render renders every row of Table, in its order, dispatching each to r's function for its site;
// a withheld row renders itself: State{Name, StatusWithheld, the citation and the summary}. CheckImage
// and Deployment.Report are its two callers, each with the renderer that gives a row's state as
// that surface knows it. A ctx that ends ends the walk — the rows rendered so far, and an error
// wrapping ctx's — since an image row's rendering runs commands under it; Report's renderers run
// none and pass a context that never ends.
func render(ctx context.Context, r renderer) ([]State, error) {
	states := make([]State, 0, len(Table))
	for _, row := range Table {
		var state State
		switch row.Site {
		case SiteImage:
			state = r.image(row)
		case SiteLive:
			state = r.live(row)
		case SiteDeployment:
			state = r.deployment(row)
		case SiteWithheld:
			state = State{Name: row.Name, Status: StatusWithheld, Detail: withheldDetail(row)}
		}
		if err := ctx.Err(); err != nil {
			return states, fmt.Errorf("capability check: %w", err)
		}
		states = append(states, state)
	}
	return states, nil
}

// Decidable is the SiteDeployment names in Table order: what a legion.yaml decided line may name,
// and what the daemon measures from its deployment (Deployment.Report).
func Decidable() []Name { return namesAtSite(SiteDeployment) }

// Live is the SiteLive names in Table order: the rows each claimed session measures at an idle
// tick of the 5 s interval it already runs and sends on a `claims/ready` of its own (INFRA-927),
// which Normalize fills out and Deployment.Report renders.
func Live() []Name { return namesAtSite(SiteLive) }

// namesAtSite is the names of Table's rows checked at site, in Table order.
func namesAtSite(site Site) []Name {
	var names []Name
	for _, row := range Table {
		if row.Site == site {
			names = append(names, row.Name)
		}
	}
	return names
}

// Row is one live row's measurement as a session reported it, normalised (Normalize). Its JSON is
// the daemon log's `rows` attribute on a report (one object per row, as the wire spells them); the
// store and the state each keep a shape of their own. A row built from what a session sent or what
// the store kept is built by NewRow.
type Row struct {
	Name Name `json:"name"`
	// OK is whether the session's check of the row passed.
	OK bool `json:"ok"`
	// Detail is the fact the check found, passed or not, cleaned (cleanDetail): it reaches the daemon
	// log, the store, the unauthenticated state route and the controller's daily report with nothing
	// delimiting it. A row that is not OK always has one — "no detail reported" when the session
	// sent none — since the state's `open[].detail` is a non-empty string; an OK row's may be empty,
	// a passing check with nothing to add.
	Detail string `json:"detail"`
}

// NewRow is the row a detail from outside the daemon makes, a session's ready or the store: the
// detail cleaned (cleanDetail), and "no detail reported" for a failing row left with none. Every
// such row is built here, so a detail reaches the log, the store and the state cleaned whichever
// way it came in, and a stored report a daemon kept before it cleaned something is cleaned when a
// later one reads it.
func NewRow(name Name, ok bool, detail string) Row {
	detail = cleanDetail(detail)
	if !ok && detail == "" {
		detail = "no detail reported"
	}
	return Row{Name: name, OK: ok, Detail: detail}
}

// Report is one session's normalised report of the live rows, as the daemon keeps and persists it:
// which claim and generation reported, from which process, when the session measured and when the
// daemon took the report, how long the measuring took, and the rows.
type Report struct {
	Claim      claim.Token
	Generation uint64
	// Locator is the process that reported; its Incarnation fences a stale report, since a claim
	// relaunched since the report is another process whose own ready reports anew.
	Locator runtime.Locator
	// MeasuredAt is when the session measured (claim.CapabilityReport.MeasuredAt); ReportedAt is
	// when the daemon took its ready.
	MeasuredAt time.Time
	ReportedAt time.Time
	// ElapsedMs is how long the session's measuring took.
	ElapsedMs int
	// Rows is in Table order, every live row present (Normalize).
	Rows []Row
}

// Session is one live session's report with its claim's role and issue: what Deployment.Sessions
// carries.
type Session struct {
	Role   claim.Role
	Issue  string
	Report Report
}

// MaxReportDetail is the longest detail a kept row carries, in bytes: a session's check quotes
// what it found, and a chatty failure is cut rather than kept whole in the store and the state.
const MaxReportDetail = 1024

// Normalize turns a wire report's rows into the rows the daemon keeps: a name Table has no live
// row for is dropped (returned in dropped, cleaned as a detail is, since the log quotes it, in wire
// order), each kept row is built by NewRow, a live row the wire lacks is appended as not OK with
// the detail "not reported by this session", and the kept rows are in Table order; a name sent
// twice keeps the first.
func Normalize(rows []claim.CapabilityRow) (kept []Row, dropped []string) {
	live := Live()
	byName := make(map[Name]Row, len(live))
	for _, row := range rows {
		name := Name(row.Name)
		if !slices.Contains(live, name) {
			dropped = append(dropped, cleanDetail(row.Name))
			continue
		}
		if _, seen := byName[name]; seen {
			continue
		}
		byName[name] = NewRow(name, row.OK, row.Detail)
	}
	kept = make([]Row, 0, len(live))
	for _, name := range live {
		row, ok := byName[name]
		if !ok {
			row = Row{Name: name, Detail: "not reported by this session"}
		}
		kept = append(kept, row)
	}
	return kept, dropped
}

// cleanDetail is a reported detail as one printed line with no credential in it, cut at
// MaxReportDetail on a rune boundary. A detail quotes upstream errors and names the worked
// repository chooses (a server in `.mcp.json`, a file under `.omp/`): neither may split a log line,
// put a line of its own into the controller's instructions, or show a credential on the state.
//
// Every space rune (unicode.IsSpace: a newline, a tab, a next line, a Unicode space) is a space,
// U+009B, the C1 form of a control sequence's introducer, is held as c1Introducer, and every other
// control rune (an escape, another C1 control, and either marker itself) is held. A value class
// runs across either, and across a held escape with the `[`, parameters and final byte that
// complete a control sequence after it, so neither a control rune nor a control sequence inside a
// credential's value leaves part of it unredacted; what follows c1Introducer counts as the
// credential's own characters, as it may be. One inside a rule's label or prefix (`gh`, `Bearer`,
// `?token=`) still keeps that rule from matching. Outside a credential, a sequence's final byte may
// close it or may be a credential's first letter. So when anything was held, the rules run on four
// readings in turn (heldReadings), each pass after the last:
//  1. Each control rune held on its own, a `[` and what follows it plain text: a credential right
//     after an unterminated `ESC [` (`ESC [ghp_…`, `ESC [?token=…`).
//  2. Each introducer (held `[`, or c1Introducer), its parameters and intermediates as opener, what
//     follows kept as text: a credential glued to a parameter (`ESC [1ghp_…`, `ESC [0Bearer …`).
//  3. Each opener and the final byte after it as held: a credential a sequence's final letter is
//     glued to (`ESC [1mghp_…`, `ESC [Kghp_…`), and grep's colour around a match.
//  4. Each held rune a space: a JWT right after a control rune or a sequence, which the JWT rule
//     reads, while held stands there, as the middle of a base64url run.
//
// Space runs then collapse to one space with none at either end; that line, before the cut, is
// RedactedLine, which another package takes whole. Redaction runs on the one line, so a credential
// a session split across lines is still found, and before the cut, so a cut never leaves a
// credential's head too short to match. The rules run until a pass changes nothing
// (redactCredentials), so the line is already a fixed point of them. After a cut, trailing space
// goes, then trailing words, and within a last word trailing characters, while the rules would
// still change what is left — a label the cut parted from its value, or a value it parted from the
// rest of its `***` — but never the last character, since one character matches no rule; so
// cleaning what this returns gives it back unchanged, and a cut never empties a detail that had
// text. The Legion plugin cleans its report the same way before it sends it (`cleanDetail` in
// `packages/pi-legion/src/capability-report.ts`); both are held to
// `packages/contracts/fixtures/capability-redaction.json`.
func cleanDetail(text string) string {
	line := RedactedLine(text)
	cut := strings.TrimRight(truncate(line, MaxReportDetail), " ")
	if len(cut) < len(line) {
		for redactCredentials(cut) != cut {
			if space := strings.LastIndexByte(cut, ' '); space > 0 {
				cut = cut[:space]
			} else {
				_, size := utf8.DecodeLastRuneInString(cut)
				cut = cut[:len(cut)-size]
			}
		}
	}
	return cut
}

// RedactedLine is text as one line with every credential masked: cleanDetail's line before its
// cut, with each space rune a space and each other control rune held, every credential redacted on
// that line and, when anything was held, on each of heldReadings in turn, and space runs collapsed
// to one space with none at either end. So each run of whitespace is one space, each control rune,
// control sequence and byte that is not UTF-8 is read away, and every credential shape is masked
// exactly as a capability report's detail is, both held to the one shared fixture. Another package
// that prints text a process chose takes it from here: the launcher's memory guard prints a
// process's arguments in its log and in the note to that process's role, and an argument can carry
// a token. Unlike a detail, the line is not cut: the caller cuts it to its own bound, and a caller
// that masks only the head of a longer text takes RedactedCutLine.
func RedactedLine(text string) string {
	marked := strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case r == '\u009b':
			return c1Introducer
		case unicode.IsControl(r), r == c1Introducer, r == opener, r == utf8.RuneError:
			return held
		}
		return r
	}, text)
	redacted := redactCredentials(marked)
	if strings.ContainsAny(marked, string(held)+string(c1Introducer)) {
		for _, reading := range heldReadings {
			if read := reading.pattern.ReplaceAllString(redacted, reading.replacement); read != redacted {
				redacted = redactCredentials(read)
			}
		}
	}
	return strings.Join(strings.Fields(redacted), " ")
}

// RedactedCutLine is RedactedLine for the head of a longer text, cut where the whole text has
// whitespace, so that each run of the head with no whitespace in it is a whole run of the text:
// RedactedLine's line of the head, with everything after the first opener of a match the cut may
// have left open (a credentialRules cutOpener whose cutClosed does not find it closed) one `***`
// to the line's end. The whole text's line masks such a match up to a closer past the cut, which
// the head does not hold, so without this the head's line would show what the whole one masks.
// What it masks after that opener the whole line may show, but a reader of a line already cut
// loses only more of its end. The memory guard masks a long process's arguments this way.
func RedactedCutLine(text string) string {
	line := RedactedLine(text)
	open := -1
	for _, rule := range credentialRules {
		if rule.cutOpener == nil {
			continue
		}
		for from := 0; ; {
			at := rule.cutOpener.FindStringIndex(line[from:])
			if at == nil {
				break
			}
			from += at[1]
			if !rule.cutClosed.MatchString(line[from:]) {
				if open < 0 || from < open {
					open = from
				}
				break
			}
		}
	}
	if open < 0 {
		return line
	}
	return line[:open] + "***"
}

// held is what cleanDetail holds a control rune as while it redacts, c1Introducer what it holds
// U+009B as, and opener what its second reading holds a control sequence's introducer as:
// noncharacters, which no text carries, so they stand for nothing else; a literal one in a detail
// is held like a control rune.
const (
	held         = '\uFFFF'
	c1Introducer = '\uFFFE'
	opener       = '\uFDD0'
)

// heldReadings are cleanDetail's readings of held text after the first, in order: an introducer,
// its parameters and intermediates as opener; an opener and an optional final byte as held; held
// as a space. A space or a `*` never stands in as an intermediate, so neither a word nor a redacted
// `***` reads as one, and nor does a `.`, which separates a JWT's segments.
var heldReadings = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?:\x{FFFF}\[|\x{FFFE})[0-?]*[!-)+,/-]*`), string(opener)},
	{regexp.MustCompile(`\x{FDD0}[@-~]?`), string(held)},
	{regexp.MustCompile(`\x{FFFF}`), " "},
}

// heldSequence, in a value class, is c1Introducer, opener, held alone, or a held escape followed by
// the `[`, parameters and final byte that complete a control sequence, a colour sequence such as
// `ESC [ 1 m` among them. A value runs across an opener because a reading turns every introducer
// into one, the one inside a value as well as the one before it. Parameters (`0`–`?`) and the final
// byte (`@`–`~`) share no character, so a class reads it one way only. The replacement character an
// invalid UTF-8 byte decodes to is held like a control rune, so a value runs across it too.
const heldSequence = `\x{FFFE}|\x{FDD0}|\x{FFFF}(?:\[[0-?]*[@-~])?`

// jwtFirstHeldSequence is heldSequence without opener: what a JWT's first segment runs across. A
// JWT may start right after an opener (`ESC [1eyJ…` reads `opener eyJ…`), so a first segment that
// also ran across one would let one run hold a start after each opener in it.
const jwtFirstHeldSequence = `\x{FFFE}|\x{FFFF}(?:\[[0-?]*[@-~])?`

// apiKeyRule and bearerRule are the API-key and bare bearer rules' patterns: each a label, its
// value, and the `://` after the value where one follows. RE2 has no lookahead, so each rule takes
// that `://` into its match and redactLabeledValue reads it there, rather than the rule standing
// itself down before a URL.
var apiKeyRule = regexp.MustCompile(`(?i)((?:api-key|application-key|ocp-apim-subscription-key|x-subscription-token)\\?["']?[ \x{FFFE}\x{FFFF}]*[:=][ \x{FFFE}\x{FFFF}]*\\?["']?)((?:[A-Za-z0-9._~+/=*-]|` + heldSequence + `)+)(://)?`)

var bearerRule = regexp.MustCompile(`(?i)\b(bearer[ \x{FFFE}\x{FFFF}]+)((?:[A-Za-z0-9._~+/=*-]|` + heldSequence + `)+)(://)?`)

// urlSchemeTail is a value's scheme-shaped tail: from the leftmost position where what the
// userinfo rule takes before its `://` runs on to the value's end.
var urlSchemeTail = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*$`)

// credentialRules are the credential shapes redactCredentials replaces, each keeping what names
// the credential and replacing the secret with `***`; the shared fixture names each with an
// example, in this order. They run on cleanDetail's line, where a space is the only whitespace and
// held or c1Introducer stands for a control rune: a value class runs across a heldSequence, and a
// separator takes a space or either marker. A quote may follow a backslash, as it does in JSON that
// was escaped again. Order matters: the Authorization header runs before the bare bearer rule, so a
// header under any scheme loses its whole value and keeps a listed scheme's label, and the GitHub
// rule before the JWT rule, so a stateless installation token (`ghs_<app id>_<JWT>`) reads
// `ghs_***`. Where one rule's mask would take what another rule reads, the mask stands back:
// an API-key value and a bare bearer value take no `:`, so before a URL after either label
// (`api-key: http://user:pw@host`, `Bearer _http://user:pw@host`) the value ends with the URL's
// scheme, and masking it would take the scheme the userinfo rule anchors on, since `***://` is
// no scheme — the userinfo would never be masked at all. So both rules take the `://` after
// their value as well, and redactLabeledValue leaves the value's scheme-shaped tail for the
// userinfo rule and masks what is glued before it; a new rule whose value class stops at a `:`,
// so that a URL's scheme can end its value, must leave that tail before `://` in the same way.
// The passes then do the rest, as they do for a userinfo the Authorization rule opens:
// where a control rune parts a userinfo from its `@` (`Bearer http://u:pw`, a control rune,
// an Authorization value that runs across spaces to a `)` before the `@`), the Authorization
// rule masks what stood between them on the first pass and the userinfo rule reaches its `@`
// on the next. The JWT rule starts nowhere its first segment's class could have run on from:
// not after held, a C1 introducer or a value character, and not after a held `[` and its
// parameters, followed by any final byte or by a `\uXXXX` escape whose backslash is that final
// byte. The plugin says so with a lookbehind; here the rule's last alternative matches that
// held prefix with `eyJ` and redactJWT gives it back unchanged. It may start after an opener,
// which its first segment does
// not run across, and right after a percent or `\u` escape that no held sequence closes. A JWT may
// also start right after held when its first segment crosses no held character and no opener, only
// a C1 introducer, so a JWT right after a replacement character is masked in the first pass, before
// the fourth reading would split it at a control inside it. And it may start right after a held
// `[`, its parameters and an optional final byte or `\uXXXX` when its first segment holds a C1
// introducer and crosses nothing else held, since reading 2 would turn that introducer into an
// opener its first segment can't cross. A JWT with no C1 introducer in its first segment is left to
// the later readings there, where its later segments cross what the first pass would stop at. These
// segments stop at the next held character, and a first segment ends at its one dot, so no start
// reads another's run: in the plugin's JavaScriptCore, which backtracks, a start inside a run its
// first segment reads through would read that run once per start. An Authorization value opened by
// an escaped quote ends at the next backslash or quote; one opened by a quote runs over escaped
// characters to an unescaped quote; one with neither runs to a closing bracket or the end of the
// line, since an AWS signature or a Digest credential carries commas, semicolons and quotes of its
// own. The API-key rule has no left anchor, so `api-key` also covers `x-api-key`, `x-goog-api-key`
// and Datadog's `dd-api-key`, and `application-key` covers `dd-application-key`. A rule whose value
// may be short takes `*` in its value class, and a quoted connection-string password keeps its
// quotes, so a value already redacted is matched whole and left as it is; the others need more than
// `***` to match at all.
//
// A rule whose match can still be open where a caller cut its text short says so with cutOpener,
// where a match of it starts, and cutClosed, which reads the line after that start and finds the
// match closed there; RedactedCutLine masks a cut line from the first opener it does not find
// closed to the line's end, and a new rule whose match can run past a cut must say so here. The
// userinfo rule is the one rule that can: it needs an `@` after its value, and its value class
// takes `*`, quotes and brackets, so another rule's mask can remove the space that kept it from an
// `@` past the cut (the Authorization value that runs across spaces to a `)`, or a quoted
// password, which the cut text masks to its end, where the userinfo never meets its `@`). Its
// value runs to the last `@` before ` `, `/`, `?` or `#`, so a userinfo masked at an `@` of the cut
// text can still run on to a later one; only a `/`, `?` or `#` the line keeps closes it, since a
// space in the line may stand for a control rune the rule read across. A rule whose value needs
// nothing after it (password, Authorization, the API-key headers, the JSON members, bearer, the
// query parameter) masks a value the cut parts to the end of the text, so it is never open at a
// cut; the `://` the API-key and bearer rules take after their value is no exception, since it
// holds no space and a cut where the whole text has whitespace never parts it from the value
// before it. A token
// rule (GitHub, the JWT, the prefixed tokens, AIza, AKIA) needs a least length or a
// terminator, but matches within one run with no whitespace in it, which a cut where the whole text
// has whitespace keeps whole, and takes no `*` in its value, so no other rule's `***` joins it to
// text past the cut.
var credentialRules = []struct {
	pattern *regexp.Regexp
	// replacement expands the match; replace, when set, computes it instead.
	replacement string
	replace     func(match string) string
	// cutOpener and cutClosed are set on a rule whose match can be open at a cut (RedactedCutLine).
	cutOpener, cutClosed *regexp.Regexp
}{
	{pattern: regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^ /?#]+@`), replacement: "${1}***@",
		cutOpener: regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://`), cutClosed: regexp.MustCompile(`^[^ /?#]*[/?#]`)},
	{pattern: regexp.MustCompile(`(?i)([?&](?:token|key|secret|password|access_token|accesstoken|refresh_token|id_token|auth_token|private_token|api_key|apikey|client_secret|sig|signature|x-amz-security-token|x-amz-signature|x-goog-signature)=)[^ &#"'<>]+`), replacement: "${1}***"},
	{pattern: regexp.MustCompile(`(?i)\b(password[ \x{FFFE}\x{FFFF}]*=[ \x{FFFE}\x{FFFF}]*)(?:(')(?:[^'\\]|\\.)*('?)|[^ &#"'<>]+)`), replacement: "${1}${2}***${3}"},
	{pattern: regexp.MustCompile(`(?i)(authorization\\?["']?[ \x{FFFE}\x{FFFF}]*[:=][ \x{FFFE}\x{FFFF}]*)(?:(\\["'](?:(?:bearer|basic|token|digest)[ \x{FFFE}\x{FFFF}]+)?)[^"'\\]+|(["'](?:(?:bearer|basic|token|digest)[ \x{FFFE}\x{FFFF}]+)?)(?:[^"'\\]|\\.)+|(\[?(?:(?:bearer|basic|token|digest)[ \x{FFFE}\x{FFFF}]+)?)[^)\]}>]+)`), replacement: "${1}${2}${3}${4}***"},
	{pattern: apiKeyRule, replace: func(m string) string { return redactLabeledValue(apiKeyRule, m) }},
	{pattern: regexp.MustCompile(`(?i)(\\?["'](?:api_key|access_token)\\?["'][ \x{FFFE}\x{FFFF}]*:[ \x{FFFE}\x{FFFF}]*\\?["'])[^"'\\]+`), replacement: "${1}***"},
	{pattern: bearerRule, replace: func(m string) string { return redactLabeledValue(bearerRule, m) }},
	{pattern: regexp.MustCompile(`\b(gh[pousr]_|github_pat_)(?:[A-Za-z0-9._-]|` + heldSequence + `){20,}`), replacement: "${1}***"},
	{pattern: regexp.MustCompile(`(?:\x{FFFF}eyJ(?:[A-Za-z0-9_-]|\x{FFFE}){8,}|\x{FFFF}\[[0-?]*(?:[@-~]|\\u[0-9A-Fa-f]{4})?eyJ[A-Za-z0-9_-]*\x{FFFE}(?:[A-Za-z0-9_-]|\x{FFFE})*|(?:^|[^A-Za-z0-9_\x{FFFE}\x{FFFF}-]|%[0-9A-Fa-f]{2}|\\u[0-9A-Fa-f]{4})eyJ(?:[A-Za-z0-9_-]|` + jwtFirstHeldSequence + `){8,})\.(?:[A-Za-z0-9_-]|` + heldSequence + `){8,}\.(?:[A-Za-z0-9_-]|` + heldSequence + `)*|\x{FFFF}\[[0-?]*(?:[@-~]?|\\u[0-9A-Fa-f]{4})eyJ`), replace: redactJWT},
	{pattern: regexp.MustCompile(`\b(sk-|dsp_|ddsat_|ddpat_|ddapp_|ya29\.|xox[a-z]-|glpat-|npm_)(?:[A-Za-z0-9_-]|` + heldSequence + `){20,}`), replacement: "${1}***"},
	{pattern: regexp.MustCompile(`\b(AIza)(?:[A-Za-z0-9_-]|` + heldSequence + `){35,}`), replacement: "${1}***"},
	{pattern: regexp.MustCompile(`\b(AKIA|ASIA)(?:(?:` + heldSequence + `)*[A-Z0-9]){16}\b`), replacement: "${1}***"},
}

// redactLabeledValue is an API-key or bearer rule match redacted. Each rule takes the `://` after
// its value into its match, since RE2 has no lookahead. A value followed by `://` can end with a
// URL's scheme (`api-key: http://user:pw@host`, `Bearer _http://user:pw@host`), and masking that
// scheme would leave `***://`, which is no scheme for the userinfo rule to anchor on. So the
// value's scheme-shaped tail (urlSchemeTail) stays: a match whose whole value is that tail is
// given back unchanged, and one with other value characters glued before the tail, which may be
// part of a token, keeps its label, `***` for those characters, the tail and the `://`. The
// userinfo rule has no left anchor, so it finds the scheme after that `***` and masks the
// userinfo. The next pass gives either back as it is: `*` is in the value class, and the tail of
// `***` and a scheme is that scheme. Any other match, one with no `://` or a value with no such
// tail, keeps its label and any `://` after its value, which is text of its own, and replaces the
// value.
func redactLabeledValue(rule *regexp.Regexp, match string) string {
	parts := rule.FindStringSubmatch(match)
	if parts[3] != "" {
		if tail := urlSchemeTail.FindStringIndex(parts[2]); tail != nil {
			if tail[0] == 0 {
				return match
			}
			return parts[1] + "***" + parts[2][tail[0]:] + parts[3]
		}
	}
	return parts[1] + "***" + parts[3]
}

// redactJWT is a JWT rule match redacted. One with no `.` is the rule's last alternative: a held
// `[` and its parameters, followed by any final byte or by a `\uXXXX` escape whose backslash is that
// final byte, and then `eyJ`, which no JWT starts after, so it is given back unchanged. Any other is
// a JWT, so it keeps what came before `eyJ` (held, and any sequence after it, for a JWT that starts
// right after held) and replaces the JWT.
func redactJWT(match string) string {
	if !strings.Contains(match, ".") {
		return match
	}
	return match[:strings.Index(match, "eyJ")] + "***"
}

// redactCredentials is text with every credentialRules match replaced, rule by rule, pass after
// pass until a pass changes nothing: a replacement can remove what kept an earlier rule from
// matching, as the Authorization rule removes the `/` that kept a userinfo's `@` out of reach.
// maxRedactionPasses bounds the passes; every case of the shared fixture settles well within it.
func redactCredentials(text string) string {
	for range maxRedactionPasses {
		next := redactOnce(text)
		if next == text {
			break
		}
		text = next
	}
	return text
}

// maxRedactionPasses is the most passes redactCredentials makes.
const maxRedactionPasses = 8

// redactOnce is text with every credentialRules match replaced, rule by rule, once.
func redactOnce(text string) string {
	for _, rule := range credentialRules {
		if rule.replace != nil {
			text = rule.pattern.ReplaceAllStringFunc(text, rule.replace)
		} else {
			text = rule.pattern.ReplaceAllString(text, rule.replacement)
		}
	}
	return text
}

// truncate is text cut to at most max bytes on a rune boundary, so a cut never leaves a partial
// character behind.
func truncate(text string, max int) string {
	if len(text) <= max {
		return text
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}
