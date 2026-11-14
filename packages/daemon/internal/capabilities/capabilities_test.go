package capabilities

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// every Name constant, so the table is held to the declared names and nothing else.
var names = []Name{
	Subagents, EvalJS, EvalPython, Browser, LSP, CodeGraph, WebSearch, Skills, MCP, RepositoryExtensions,
	DispatchEnvoyTools, GitHub, Secrets, ModelFallback, Toolchain, ClaudeCode, ResourceLimits, PoolCapacity,
	Network, OperatorSetup, ProductionIdentities,
}

// The table is the one declared list: every Name constant appears in it exactly once, and no row
// names anything else.
func TestTableNamesEveryCapabilityOnce(t *testing.T) {
	seen := map[Name]int{}
	for _, row := range Table {
		seen[row.Name]++
		if !slices.Contains(names, row.Name) {
			t.Errorf("Table names %q, which no Name constant declares", row.Name)
		}
		if row.Summary == "" {
			t.Errorf("row %s has no summary", row.Name)
		}
	}
	for _, name := range names {
		if seen[name] != 1 {
			t.Errorf("Table names %s %d times, want once", name, seen[name])
		}
	}
	if len(Table) != len(names) {
		t.Errorf("Table has %d rows, want the %d names", len(Table), len(names))
	}
}

// Every row is checked at one of the four sites.
func TestEveryRowHasASite(t *testing.T) {
	for _, row := range Table {
		if !slices.Contains([]Site{SiteImage, SiteLive, SiteDeployment, SiteWithheld}, row.Site) {
			t.Errorf("row %s is checked at %q, which is no site", row.Name, row.Site)
		}
	}
}

// The deployment rows are what a legion.yaml decided line may name, in the table's order.
func TestDecidableIsTheDeploymentRowsInTableOrder(t *testing.T) {
	if got, want := Decidable(), []Name{Secrets, ModelFallback, ResourceLimits, PoolCapacity}; !slices.Equal(got, want) {
		t.Errorf("Decidable() = %v, want %v", got, want)
	}
}

// The live rows are what each claimed session measures while it runs, in the table's order.
func TestLiveIsTheLiveRowsInTableOrder(t *testing.T) {
	if got, want := Live(), []Name{Subagents, WebSearch, MCP, RepositoryExtensions, DispatchEnvoyTools, GitHub}; !slices.Equal(got, want) {
		t.Errorf("Live() = %v, want %v", got, want)
	}
}

// Every live row cites the Legion issue whose check proves it: the unchecked row's detail names it
// (Deployment.Report), so a row with no citation would print an empty parenthesis.
func TestEveryLiveRowHasACitation(t *testing.T) {
	for _, row := range Table {
		if row.Site != SiteLive {
			continue
		}
		if !strings.HasPrefix(row.Citation, "dispatch://LEGION-") {
			t.Errorf("live row %s cites %q, want the dispatch://LEGION-<n> issue whose check proves it", row.Name, row.Citation)
		}
	}
}

// A withheld row cites the Legion issue that withholds it, and only Legion's own keys: the root
// AGENTS.md keeps the deployment repository's project key out of the tree.
func TestWithheldRowsCiteALegionIssue(t *testing.T) {
	for _, row := range Table {
		if row.Site != SiteWithheld {
			continue
		}
		if !strings.HasPrefix(row.Citation, "dispatch://LEGION-") {
			t.Errorf("withheld row %s cites %q, want a dispatch://LEGION-<n> issue", row.Name, row.Citation)
		}
	}
	for _, row := range Table {
		for _, cited := range strings.Split(row.Citation, ",") {
			if cited = strings.TrimSpace(cited); cited != "" && !strings.HasPrefix(cited, "dispatch://LEGION-") {
				t.Errorf("row %s cites %q, which is not a LEGION issue", row.Name, cited)
			}
		}
	}
}

// unreported is a normalised report as Normalize fills one out from a wire that sent nothing:
// every live row in Table order, not OK, saying the session did not report it — with the given
// rows in their names' place.
func unreported(sent ...Row) []Row {
	rows := make([]Row, 0, len(Live()))
	for _, name := range Live() {
		row := Row{Name: name, Detail: "not reported by this session"}
		for _, candidate := range sent {
			if candidate.Name == name {
				row = candidate
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// Normalize turns what a session sent into the rows the daemon keeps: a name Table has no live
// row for is dropped and returned, a detail is one line (newlines, tabs, control characters and
// ANSI control sequences out, whitespace runs collapsed) cut on a rune boundary when longer than
// MaxReportDetail, a failing row with no detail left reads "no detail reported" while a passing
// row's empty detail stays empty, a live row the wire lacks is filled in as not reported, the rows
// come out in Table order whatever order the wire sent them, and a name sent twice keeps the first.
func TestNormalizeKeepsTheLiveRowsInTableOrder(t *testing.T) {
	long := strings.Repeat("€", 400)
	for _, testCase := range []struct {
		name        string
		rows        []claim.CapabilityRow
		wantKept    []Row
		wantDropped []string
	}{
		{
			name:     "nothing sent",
			wantKept: unreported(),
		},
		{
			name: "drops an unknown name",
			rows: []claim.CapabilityRow{
				{Name: "subagents", OK: true, Detail: "4 agents"},
				{Name: "teleport", OK: false, Detail: "no such row"},
				{Name: "eval-js", OK: true, Detail: "an image row, not a live one"},
			},
			wantKept:    unreported(Row{Name: Subagents, OK: true, Detail: "4 agents"}),
			wantDropped: []string{"teleport", "eval-js"},
		},
		{
			name:        "drops an unknown name cleaned, since the log quotes it",
			rows:        []claim.CapabilityRow{{Name: "teleport\nghp_EXAMPLEtoken0123456789abcdefghijklmn", OK: false, Detail: "no such row"}},
			wantKept:    unreported(),
			wantDropped: []string{"teleport ghp_***"},
		},
		{
			name:     "cuts a long detail on a rune boundary",
			rows:     []claim.CapabilityRow{{Name: "web-search", OK: false, Detail: long}},
			wantKept: unreported(Row{Name: WebSearch, Detail: strings.Repeat("€", 341)}),
		},
		{
			name:     "keeps a detail of exactly the limit, cleaned before the cut",
			rows:     []claim.CapabilityRow{{Name: "mcp", OK: true, Detail: "\n " + strings.Repeat("a", MaxReportDetail) + "\t"}},
			wantKept: unreported(Row{Name: MCP, OK: true, Detail: strings.Repeat("a", MaxReportDetail)}),
		},
		{
			name:     "floors a failing row's empty detail",
			rows:     []claim.CapabilityRow{{Name: "github", OK: false, Detail: ""}},
			wantKept: unreported(Row{Name: GitHub, Detail: "no detail reported"}),
		},
		{
			name:     "floors a failing row's detail that is only whitespace",
			rows:     []claim.CapabilityRow{{Name: "github", OK: false, Detail: "  \n\t "}},
			wantKept: unreported(Row{Name: GitHub, Detail: "no detail reported"}),
		},
		{
			name:     "keeps a passing row's empty detail empty",
			rows:     []claim.CapabilityRow{{Name: "github", OK: true, Detail: ""}},
			wantKept: unreported(Row{Name: GitHub, OK: true, Detail: ""}),
		},
		{
			name:     "puts a detail on one line with its control characters and sequences out",
			rows:     []claim.CapabilityRow{{Name: "mcp", OK: false, Detail: "not connected: evil\nserver\x1b[31m (boom)\x1b"}},
			wantKept: unreported(Row{Name: MCP, Detail: "not connected: evil server (boom)"}),
		},
		{
			name: "orders the rows as Table does",
			rows: []claim.CapabilityRow{
				{Name: "github", OK: true, Detail: "gh api user: octocat"},
				{Name: "dispatch-envoy-tools", OK: true, Detail: "dispatch read answered"},
				{Name: "repository-extensions", OK: true, Detail: "2 extensions"},
				{Name: "mcp", OK: true, Detail: "no server configured"},
				{Name: "web-search", OK: true, Detail: "web_search active"},
				{Name: "subagents", OK: true, Detail: "4 agents"},
			},
			wantKept: []Row{
				{Name: Subagents, OK: true, Detail: "4 agents"},
				{Name: WebSearch, OK: true, Detail: "web_search active"},
				{Name: MCP, OK: true, Detail: "no server configured"},
				{Name: RepositoryExtensions, OK: true, Detail: "2 extensions"},
				{Name: DispatchEnvoyTools, OK: true, Detail: "dispatch read answered"},
				{Name: GitHub, OK: true, Detail: "gh api user: octocat"},
			},
		},
		{
			name: "keeps the first of a name sent twice",
			rows: []claim.CapabilityRow{
				{Name: "mcp", OK: true, Detail: "first"},
				{Name: "mcp", OK: false, Detail: "second"},
			},
			wantKept: unreported(Row{Name: MCP, OK: true, Detail: "first"}),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			kept, dropped := Normalize(testCase.rows)

			if !reflect.DeepEqual(kept, testCase.wantKept) {
				t.Errorf("kept = %+v\nwant   %+v", kept, testCase.wantKept)
			}
			if !slices.Equal(dropped, testCase.wantDropped) {
				t.Errorf("dropped = %q, want %q", dropped, testCase.wantDropped)
			}
			for _, row := range kept {
				if len(row.Detail) > MaxReportDetail {
					t.Errorf("%s: detail is %d bytes, want at most %d", row.Name, len(row.Detail), MaxReportDetail)
				}
				if !utf8.ValidString(row.Detail) {
					t.Errorf("%s: detail %q is not valid UTF-8: the cut split a rune", row.Name, row.Detail)
				}
			}
		})
	}
}

// readFixture decodes one file of packages/contracts/fixtures into into.
func readFixture(t *testing.T, name string, into any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "fixtures", name))
	if err != nil {
		t.Fatalf("read the fixture %s: %v", name, err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode the fixture %s: %v", name, err)
	}
}

// redactionCase is one entry of packages/contracts/fixtures/capability-redaction.json's patterns:
// a detail and what it reads cleaned, each prefixed with Pad letters `a` where it has one. The
// plugin's cleanDetail is held to the same cases (packages/pi-legion/src/capability-report.test.ts).
type redactionCase struct {
	Pattern string `json:"pattern"`
	Name    string `json:"name"`
	Pad     int    `json:"pad"`
	Detail  string `json:"detail"`
	Want    string `json:"want"`
}

// Normalize cleans every detail as the shared fixture's cases say — one line, every credential
// redacted, cut at MaxReportDetail — since a kept detail reaches the unauthenticated state route,
// the log and the store whatever the session sent. Cleaning is idempotent: the session's plugin
// cleans its report the same way before the daemon cleans it again, so each want stays as it is.
func TestNormalizeCleansEachDetailAsTheSharedFixtureSays(t *testing.T) {
	var fixture struct {
		Patterns []redactionCase `json:"patterns"`
	}
	readFixture(t, "capability-redaction.json", &fixture)
	for _, testCase := range fixture.Patterns {
		t.Run(testCase.Pattern+": "+testCase.Name, func(t *testing.T) {
			padding := strings.Repeat("a", testCase.Pad)
			want := padding + testCase.Want
			for _, sent := range []string{padding + testCase.Detail, want} {
				kept, _ := Normalize([]claim.CapabilityRow{{Name: "github", OK: true, Detail: sent}})
				if got := kept[slices.IndexFunc(kept, func(row Row) bool { return row.Name == GitHub })].Detail; got != want {
					t.Errorf("Normalize(%q) detail = %q\nwant %q", sent, got, want)
				}
			}
		})
	}
}

// redactCredentials stops because its passes reach a fixed point, not because it ran out of them:
// every detail and want of the shared fixture settles in fewer than maxRedactionPasses passes, the
// userinfo an Authorization value opens among them, which takes a second pass to redact.
func TestRedactCredentialsReachesAFixedPointWithinItsBound(t *testing.T) {
	var fixture struct {
		Patterns []redactionCase `json:"patterns"`
	}
	readFixture(t, "capability-redaction.json", &fixture)
	most := 0
	for _, testCase := range fixture.Patterns {
		for _, text := range []string{testCase.Detail, testCase.Want} {
			passes := 0
			for next := redactOnce(text); next != text; next = redactOnce(text) {
				text = next
				passes++
			}
			if passes >= maxRedactionPasses {
				t.Errorf("%s: %s: %q takes %d changing passes, the bound is %d", testCase.Pattern, testCase.Name, text, passes, maxRedactionPasses)
			}
			most = max(most, passes)
		}
	}
	if most < 2 {
		t.Errorf("no case took a second changing pass (most %d), so nothing shows the passes repeat", most)
	}
}

// RedactedLine masks what a capability report's detail masks, since the memory guard prints a
// process's arguments through it: every case of the shared fixture reads from it as the fixture
// says, but those a pad pushes past MaxReportDetail, whose want is cleanDetail's cut, which
// RedactedLine does not make.
func TestRedactedLineMasksEachUncutCaseOfTheSharedFixture(t *testing.T) {
	var fixture struct {
		Patterns []redactionCase `json:"patterns"`
	}
	readFixture(t, "capability-redaction.json", &fixture)
	uncut := 0
	for _, testCase := range fixture.Patterns {
		padding := strings.Repeat("a", testCase.Pad)
		detail := padding + testCase.Detail
		if len(detail) > MaxReportDetail {
			continue
		}
		uncut++
		if got, want := RedactedLine(detail), padding+testCase.Want; got != want {
			t.Errorf("%s: %s: RedactedLine(%q) = %q\nwant %q", testCase.Pattern, testCase.Name, detail, got, want)
		}
	}
	t.Logf("%d of %d cases uncut", uncut, len(fixture.Patterns))
}

// RedactedCutLine masks a userinfo the cut leaves open from its scheme to the line's end: one cut
// before its `@`, one another rule's mask ran to the end of the cut text, and one masked at an `@`
// of the cut text that a control rune, read as a space, leaves open to a later `@` past the cut.
// A userinfo, masked or not, that a `/`, `?` or `#` closes is as RedactedLine gives it, and so is
// every detail of the shared fixture with no scheme in it.
func TestRedactedCutLineMasksAUserinfoTheCutLeavesOpenToTheEnd(t *testing.T) {
	for _, testCase := range []struct {
		name, text string
		// want is what RedactedCutLine gives; empty, it is what RedactedLine gives.
		want string
	}{
		{"a userinfo cut before its at sign", "curl https://user:pw", "curl https://***"},
		{"a scheme in capitals", "curl HTTPS://USER:PW", "curl HTTPS://***"},
		{"a scheme the cut text ends at", "curl https://", "curl https://***"},
		{"a userinfo an Authorization value ran to the end of", "curl http://user:SECRETPASS\x07Authorization:a w w", "curl http://***"},
		{"a userinfo a quoted password ran to the end of", "curl http://u:P\x07password='a b", "curl http://***"},
		{"a userinfo masked at an at sign, open after it", "dial postgres://legion:p@ss\x07Authorization:a w", "dial postgres://***"},
		{"a masked userinfo at the end of the cut text", "get http://***@h", "get http://***"},
		{"the first scheme left open, after one a path closes", "get https://***@h/x then http://u:p more", "get https://***@h/x then http://***"},
		{"a masked userinfo a path closes", "get http://***@h/x more", ""},
		{"a userinfo followed by its at sign and a path", "get https://user:pw@h/x more", ""},
		{"a userinfo followed by its at sign and a query", "get https://user:pw@h?q=1 more", ""},
		{"a userinfo followed by its at sign and a fragment", "get https://user:pw@h#top more", ""},
		{"a URL with no userinfo, closed by its path", "get https://example.org/a more", ""},
		{"no scheme", "make -j8 all", ""},
	} {
		want := testCase.want
		if want == "" {
			want = RedactedLine(testCase.text)
		}
		if got := RedactedCutLine(testCase.text); got != want {
			t.Errorf("%s: RedactedCutLine(%q) = %q, want %q", testCase.name, testCase.text, got, want)
		}
	}
	var fixture struct {
		Patterns []redactionCase `json:"patterns"`
	}
	readFixture(t, "capability-redaction.json", &fixture)
	plain := 0
	for _, testCase := range fixture.Patterns {
		if strings.Contains(testCase.Detail, "://") {
			continue
		}
		plain++
		if got, want := RedactedCutLine(testCase.Detail), RedactedLine(testCase.Detail); got != want {
			t.Errorf("%s: %s: RedactedCutLine(%q) = %q, want RedactedLine's %q", testCase.Pattern, testCase.Name, testCase.Detail, got, want)
		}
	}
	t.Logf("%d of %d details with no scheme", plain, len(fixture.Patterns))
}

// An API-key or bare bearer value that is a URL's scheme with other value characters glued
// before it (`Bearer _http://…`, `api-key: 1http://…`) stands the rule back no more than a value
// that is no URL at all, so the rule masks the whole value with the scheme in it. The userinfo
// rule anchors on the scheme alone (`[a-z][a-z0-9+.-]*://`, which the same text without the label
// matches after the glued characters), and `***://` is no scheme, so where a control rune parts the
// userinfo from its `@` and only a later pass can reach that `@`, the password shows. Measured in
// a gVisor issue pod on 2026-11-14 at e9af0140: `curl -H Bearer _http://u:SECRETPASS\x07
// Authorization:a b)@h` gave `curl -H Bearer ***://u:SECRETPASS Authorization:***)@h`, while
// `curl _http://u:SECRETPASS\x07Authorization:a b)@h` gave `curl _http://***@h`; the plugin's
// cleanDetail gave the same two lines.
func TestRedactedLineMasksAUserinfoAfterALabelWhoseValueGluesTextToItsScheme(t *testing.T) {
	for _, label := range []string{"Bearer ", "api-key: ", "application-key=", "ocp-apim-subscription-key: ", "x-subscription-token: "} {
		for _, glued := range []string{"_", "x_", "~", "/", "=", "*", "1", "+", ".", "-"} {
			text := "curl -H " + label + glued + "http://u:SECRETPASS\x07Authorization:a b)@h"
			if got := RedactedLine(text); strings.Contains(got, "SECRETPASS") {
				t.Errorf("RedactedLine(%q) = %q, which shows the password", text, got)
			}
			if got := RedactedCutLine(text); strings.Contains(got, "SECRETPASS") {
				t.Errorf("RedactedCutLine(%q) = %q, which shows the password", text, got)
			}
		}
	}
}

// cutBoundaryPads are the lengths of the run of letters a case whose cleaned text is `cleaned`
// bytes long is pushed by, so that the MaxReportDetail cut lands at each boundary of that text in
// turn. A sweep of every length between them cleans each case ten thousand times over and reads
// nothing these eight do not. In order, each is: no letters at all, the case cleaned on its own;
// the start of the window a sweep covers, sixteen bytes short of the first cut; the longest pad
// that still leaves the case whole; the shortest pad whose cut takes the case's last byte, and one
// past it; the pad whose cut lands in the middle of the case, where a label can be parted from its
// value; the pad whose cut lands on the case's first byte, so all of its own text goes; and the end
// of that window, where the cut lands inside the letters themselves. Each is clamped into the
// window and the list de-duplicated, so a case as long as the cut answers fewer than eight.
func cutBoundaryPads(cleaned int) []int {
	first := MaxReportDetail - cleaned
	pads := []int{0, first - 16, first - 1, first, first + 1, MaxReportDetail - cleaned/2, MaxReportDetail - 1, MaxReportDetail + 8}
	for i, pad := range pads {
		pads[i] = min(max(pad, 0), MaxReportDetail+8)
	}
	slices.Sort(pads)
	return slices.Compact(pads)
}

// At the cut, cleaning keeps what came before it and is idempotent: every case of the shared
// fixture, pushed by a run of letters so that the cut at MaxReportDetail lands at each boundary of
// its cleaned text (cutBoundaryPads), still starts with those letters (as many as fit), is — for a
// case with no pad of its own — a prefix of the letters and the case cleaned whole, and comes back
// from a second cleaning as the first left it. A label the cut parts from its value is dropped,
// not redacted only by the daemon's pass, and nothing before it goes with it.
func TestCleanDetailKeepsWhatCameBeforeTheCutAndIsIdempotent(t *testing.T) {
	var fixture struct {
		Patterns []redactionCase `json:"patterns"`
	}
	readFixture(t, "capability-redaction.json", &fixture)
	pushes := 0
	for _, testCase := range fixture.Patterns {
		whole := cleanDetail(testCase.Detail)
		for _, letters := range cutBoundaryPads(len(whole)) {
			pushes++
			padding := strings.Repeat("a", letters)
			once := cleanDetail(padding + " " + testCase.Detail)
			label := fmt.Sprintf("%s: %s, pushed by %d letters", testCase.Pattern, testCase.Name, letters)
			if twice := cleanDetail(once); twice != once {
				t.Errorf("%s: cleaned again\n %q\nwant\n %q", label, twice, once)
			}
			if !strings.HasPrefix(once, padding[:min(letters, MaxReportDetail)]) {
				t.Errorf("%s: lost its padding: %q", label, once)
			}
			// With no letters there is no space before the case either: cleaning drops that one
			// separator, so what the push cleans to is the case cleaned whole.
			pushed := strings.TrimPrefix(padding+" "+whole, " ")
			if testCase.Pad == 0 && !strings.HasPrefix(pushed, once) {
				t.Errorf("%s: %q is not a prefix of the case cleaned whole, %q", label, once, whole)
			}
		}
	}
	t.Logf("%d cases, %d pushed cases in all", len(fixture.Patterns), pushes)
}

// What the session's plugin sends for the shared fixture's branches —
// packages/contracts/fixtures/capability-redaction-plugin-report.json, which the plugin's test
// holds to what it sends — is already clean, so Normalize keeps every row of it as it came.
func TestNormalizeKeepsThePluginsReportAsItIs(t *testing.T) {
	var sent struct {
		Rows []claim.CapabilityRow `json:"rows"`
	}
	readFixture(t, "capability-redaction-plugin-report.json", &sent)
	if len(sent.Rows) != len(Live()) {
		t.Fatalf("the plugin's report has %d rows, want one per live row (%d)", len(sent.Rows), len(Live()))
	}

	kept, dropped := Normalize(sent.Rows)

	if len(dropped) > 0 {
		t.Fatalf("dropped %q, want every row of the plugin's report kept", dropped)
	}
	for _, row := range sent.Rows {
		want := Row{Name: Name(row.Name), OK: row.OK, Detail: row.Detail}
		if got := kept[slices.IndexFunc(kept, func(k Row) bool { return k.Name == want.Name })]; got != want {
			t.Errorf("kept %+v\nwant %+v", got, want)
		}
	}
}
