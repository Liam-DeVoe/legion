package specgraph

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The Detail of each rule of Compare.
const (
	wantUnapprovedID   = "unapproved-id: the approved spec never held this id"
	wantUnapprovedText = "unapproved-text: the approved spec never held this text"
	wantRemovedID      = "removed-id: the approved spec removed this id and never held this text for it"
)

// The files of approved-text hold the current approved text of each node of its approved.json, a
// valid tree. The comparison passes, and the files hold each approved node at its current text.
// Each other fixture of testdata/compare is this one with one change.
func TestTheApprovedTextPasses(t *testing.T) {
	t.Parallel()
	tree := parseDir(t, "testdata/compare/approved-text/spec")
	if problems := Validate(tree, Options{}); len(problems) != 0 {
		t.Fatalf("Validate(approved-text): %+v, want none", problems)
	}
	got := compareFixture(t, "approved-text")
	if len(got.Problems) != 0 {
		t.Errorf("Problems %+v, want none", got.Problems)
	}
	if len(got.Holds) != 10 {
		t.Errorf("Holds %+v, want the 10 approved nodes", got.Holds)
	}
	for id, holding := range got.Holds {
		if holding != (Holding{Present: true, Version: 1, Current: true}) {
			t.Errorf("Holds[%s] = %+v, want version 1, current", id, holding)
		}
	}
}

// Each fixture holds one node in a text that the approved spec never held, of each kind that R2
// names and of a term, or one node whose id it never held. The comparison fails on that node alone
// and names it with its file and line (v-two-specs, the five "never held" controls).
func TestTheComparisonFailsOnATextNeverApproved(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		fixture string
		want    Problem
	}{
		{"one-word", Problem{Position{"orders.md", 7}, "an-order-survives-a-restart", "unapproved-text", wantUnapprovedText}},
		{"unapproved-statement", Problem{Position{"orders.md", 29}, "each-order-gets-one-bill", "unapproved-id", wantUnapprovedID}},
		{"check-text", Problem{Position{"orders.md", 11}, "a-restart-keeps-each-order", "unapproved-text", wantUnapprovedText}},
		{"component-purpose", Problem{Position{"components.md", 7}, "orders", "unapproved-text", wantUnapprovedText}},
		{"interface-joins", Problem{Position{"components.md", 11}, "orders-to-billing", "unapproved-text", wantUnapprovedText}},
		{"term-definition", Problem{Position{"components.md", 19}, "order", "unapproved-text", wantUnapprovedText}},
	} {
		t.Run(row.fixture, func(t *testing.T) {
			t.Parallel()
			got := compareFixture(t, row.fixture)
			if !reflect.DeepEqual(got.Problems, []Problem{row.want}) {
				t.Errorf("Problems:\n got %+v\nwant %+v", got.Problems, []Problem{row.want})
			}
			if got.Holds[row.want.Node].Present {
				t.Errorf("Holds[%s] = %+v, want not present: the files hold no approved text of it", row.want.Node, got.Holds[row.want.Node])
			}
		})
	}
}

// The files may hold an earlier approved text of a statement: the comparison passes, and the
// holding says which version the files hold and that it is not the current one (R2: "The files can
// hold the text of an earlier approval").
func TestTheFilesMayHoldAnEarlierApprovedText(t *testing.T) {
	t.Parallel()
	got := compareFixture(t, "earlier-text")
	if len(got.Problems) != 0 {
		t.Errorf("Problems %+v, want none", got.Problems)
	}
	if h := got.Holds["an-order-survives-a-restart"]; h != (Holding{Present: true, Version: 1, Current: false}) {
		t.Errorf("Holds[an-order-survives-a-restart] = %+v, want version 1, not current", h)
	}
	if h := got.Holds["acme-keeps-each-order"]; h != (Holding{Present: true, Version: 1, Current: true}) {
		t.Errorf("Holds[acme-keeps-each-order] = %+v, want version 1, current", h)
	}
}

// An approved statement and its check that the files do not hold are in Holds, not present, and
// the comparison passes: a statement that is not built is no fault of the files (R2).
func TestAStatementTheFilesDoNotHoldIsNotPresent(t *testing.T) {
	t.Parallel()
	got := compareFixture(t, "not-in-files")
	if len(got.Problems) != 0 {
		t.Errorf("Problems %+v, want none", got.Problems)
	}
	for _, id := range []string{"orders-reach-billing-only-through-the-interface", "no-order-skips-the-interface"} {
		if h, ok := got.Holds[id]; !ok || h.Present {
			t.Errorf("Holds[%s] = %+v, %t; want an entry that is not present", id, h, ok)
		}
	}
	if h := got.Holds["an-order-survives-a-restart"]; !h.Present || !h.Current {
		t.Errorf("Holds[an-order-survives-a-restart] = %+v, want present at its current text", h)
	}
}

// The text that Print writes for each current approved node, copied into files, passes the
// comparison; the same files with one planted word in one statement fail it on that statement
// (v-two-specs: "A change copies the text that Dispatch prints ... The same text with one word
// changed fails it").
func TestAPrintedTextPassesAndOneWordMoreFails(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir("testdata/compare")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Run(entry.Name(), func(t *testing.T) {
			t.Parallel()
			approved := readApproved(t, filepath.Join("testdata/compare", entry.Name(), "approved.json"))
			current, problems := approved.Current()
			if len(problems) != 0 {
				t.Fatalf("Current: %+v", problems)
			}
			dir := t.TempDir()
			write := func(id, block string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(block), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, node := range nodesOf(current) {
				block, err := node.node.Print()
				if err != nil {
					t.Fatal(err)
				}
				write(node.id, block)
			}
			got := Compare(parseDir(t, dir), approved)
			if len(got.Problems) != 0 {
				t.Errorf("Problems of the printed text: %+v, want none", got.Problems)
			}
			for id, h := range got.Holds {
				if !h.Present || !h.Current {
					t.Errorf("Holds[%s] = %+v, want present at its current text", id, h)
				}
			}

			planted := current.Statements[0]
			planted.Text = strings.Replace(planted.Text, " ", " planted ", 1)
			block, err := planted.Print()
			if err != nil {
				t.Fatal(err)
			}
			write(planted.ID, block)
			want := []Problem{{Position{planted.ID + ".md", 1}, planted.ID, "unapproved-text", wantUnapprovedText}}
			if got := Compare(parseDir(t, dir), approved); !reflect.DeepEqual(got.Problems, want) {
				t.Errorf("Problems with one planted word:\n got %+v\nwant %+v", got.Problems, want)
			}
		})
	}
}

// A node that uses an id the approved spec removed, in a text that the node never held there,
// fails removed-id: the id is used again (R1: "never used again"). Its check, which the files hold
// in its approved text, passes.
func TestARemovedIdThatComesBackIsRefused(t *testing.T) {
	t.Parallel()
	got := compareFixture(t, "removed-id-reused")
	want := []Problem{{Position{"orders.md", 29}, "an-order-is-paid-once", "removed-id", wantRemovedID}}
	if !reflect.DeepEqual(got.Problems, want) {
		t.Errorf("Problems:\n got %+v\nwant %+v", got.Problems, want)
	}
}

// After the person approves the removal of a statement and its check, the files hold them until
// the change that removes them lands (R22: the approval comes "before the work on it starts"). A
// change that does not touch them, here one that builds the new text of another statement,
// passes while the files hold their last approved text or an earlier one (R2: the comparison
// fails only on "a text that the approved spec never held"). A removed node is not in Holds,
// since it has no current text.
func TestAChangeThatLeavesARemovedStatementPasses(t *testing.T) {
	t.Parallel()
	approved := readApproved(t, "testdata/compare/removed-kept/approved.json")
	files := readFiles(t, "testdata/compare/removed-kept/spec")
	last := string(files["orders.md"])
	earlier := strings.Replace(last, " and refunds each second payment.", ".", 1)
	if earlier == last {
		t.Fatal("the fixture holds no last approved text of the removed statement")
	}
	for name, content := range map[string]string{"its last approved text": last, "an earlier approved text": earlier} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			files := map[string][]byte{"orders.md": []byte(content), "components.md": files["components.md"]}
			tree, problems := Parse(files)
			if len(problems) != 0 {
				t.Fatal(problems)
			}
			if _, ok := tree.StatementByID("an-order-is-paid-once"); !ok {
				t.Fatal("the files do not hold the removed statement; the test removes nothing")
			}
			got := Compare(tree, approved)
			if len(got.Problems) != 0 {
				t.Errorf("Problems %+v, want none", got.Problems)
			}
			for _, id := range []string{"an-order-is-paid-once", "a-second-payment-is-refused"} {
				if h, ok := got.Holds[id]; ok {
					t.Errorf("Holds[%s] = %+v, want no entry for a removed node", id, h)
				}
			}
			if h := got.Holds["an-order-survives-a-restart"]; h != (Holding{Present: true, Version: 2, Current: true}) {
				t.Errorf("Holds[an-order-survives-a-restart] = %+v, want version 2, current", h)
			}
		})
	}
}

// One unapproved statement among three approved ones, one of them at an earlier approved text, is
// the one node the comparison names. The statement at its earlier text and the approved check that
// serves the unapproved statement are no cause (prototype: a comparison read false at every
// commit while it named another cause).
func TestTheFirstFailingNodeIsNamedNotTheGate(t *testing.T) {
	t.Parallel()
	got := compareFixture(t, "one-of-four")
	want := []Problem{{Position{"orders.md", 29}, "each-order-gets-one-bill", "unapproved-text", wantUnapprovedText}}
	if !reflect.DeepEqual(got.Problems, want) {
		t.Errorf("Problems:\n got %+v\nwant %+v", got.Problems, want)
	}
	for id, want := range map[string]Holding{
		"acme-keeps-each-order":                           {Present: true, Version: 1, Current: true},
		"an-order-survives-a-restart":                     {Present: true, Version: 1, Current: false},
		"orders-reach-billing-only-through-the-interface": {Present: true, Version: 1, Current: true},
		"each-order-gets-one-bill":                        {},
		"billing-sends-one-bill":                          {Present: true, Version: 1, Current: true},
	} {
		if got := got.Holds[id]; got != want {
			t.Errorf("Holds[%s] = %+v, want %+v", id, got, want)
		}
	}
}

// ReadApproved refuses a version whose block does not parse, here a text line that starts with
// :::, and names the node and the version (Review focus 1).
func TestTheApprovedFixtureRefusesAnUnprintableBlock(t *testing.T) {
	t.Parallel()
	_, err := ReadApproved(strings.NewReader(`{"nodes":[{"id":"a-page-shows-it","versions":[` +
		`{"version":1,"at":"2026-11-10T00:00:00Z","block":":::requirement{#a-page-shows-it kind=\"behavior\"}\nThe page shows it.\n:::\n"},` +
		`{"version":2,"at":"2026-11-11T00:00:00Z","block":":::requirement{#a-page-shows-it kind=\"behavior\"}\nThe page shows it.\n::: and more\n:::\n"}]}]}`))
	if err == nil {
		t.Fatal("ReadApproved accepted a block that does not parse")
	}
	for _, want := range []string{`"a-page-shows-it"`, "version 2", "bad-block-line"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ReadApproved: %q, want an error that names %s", err, want)
		}
	}
}

// ReadApproved refuses each fixture that is not an approved spec, naming the fault, so no fixture
// gives a current text by accident.
func TestReadApprovedRefusesAMalformedFixture(t *testing.T) {
	t.Parallel()
	block := func(id, text string) string {
		return `":::requirement{#` + id + ` kind=\"behavior\"}\n` + text + `\n:::\n"`
	}
	version := func(n, at, block string) string {
		return `{"version":` + n + `,"at":"` + at + `","block":` + block + `}`
	}
	good := version("1", "2026-11-10T00:00:00Z", block("a-page-shows-it", "The page shows it."))
	for _, row := range []struct{ name, json, want string }{
		{"an unknown field", `{"nodes":[{"id":"a-page-shows-it","removd":true,"versions":[` + good + `]}]}`, `member "/nodes/0/removd" is not in the format`},
		{"a field written in other letters", `{"nodes":[{"id":"a-page-shows-it","Removed":true,"versions":[` + good + `]}]}`, `member "/nodes/0/Removed" is not in the format`},
		{"one field two times, in two cases", `{"nodes":[{"id":"a-page-shows-it","removed":false,"Removed":true,"versions":[` + good + `]}]}`, `member "/nodes/0/Removed" is not in the format`},
		{"one field two times", `{"nodes":[{"id":"a-page-shows-it","removed":false,"removed":true,"versions":[` + good + `]}]}`, `member "/nodes/0/removed" is in one object two times`},
		{"a byte that is not UTF-8", "{\"nodes\":[{\"id\":\"a-page-shows-\xffit\",\"versions\":[" + good + "]}]}", "it is not UTF-8"},
		{"text after the object", `{"nodes":[]} {}`, "text after the JSON value"},
		{"no JSON value", ``, "it holds no JSON value"},
		{"a node with no id", `{"nodes":[{"id":"","versions":[` + good + `]}]}`, "a node has no id"},
		{"one id on two nodes", `{"nodes":[{"id":"a-page-shows-it","versions":[` + good + `]},{"id":"a-page-shows-it","versions":[` + good + `]}]}`, `node "a-page-shows-it" is in the approved spec two times`},
		{"a node with no version", `{"nodes":[{"id":"a-page-shows-it","removed":true,"versions":[]}]}`, `approved node "a-page-shows-it": it has no version`},
		{"version 0", `{"nodes":[{"id":"a-page-shows-it","versions":[` + version("0", "2026-11-10T00:00:00Z", block("a-page-shows-it", "The page shows it.")) + `]}]}`,
			`approved node "a-page-shows-it" version 0: the versions are 1 and more, oldest first`},
		{"versions out of order", `{"nodes":[{"id":"a-page-shows-it","versions":[` + version("2", "2026-11-10T00:00:00Z", block("a-page-shows-it", "The page shows it.")) + `,` + good + `]}]}`,
			`approved node "a-page-shows-it" version 1: the versions are 1 and more, oldest first`},
		{"a version older than the one before it", `{"nodes":[{"id":"a-page-shows-it","versions":[` + good + `,` + version("2", "2026-11-09T00:00:00Z", block("a-page-shows-it", "The page shows all.")) + `]}]}`,
			`approved node "a-page-shows-it" version 2: it is older than version 1`},
		{"a version with no time", `{"nodes":[{"id":"a-page-shows-it","versions":[{"version":1,"block":` + block("a-page-shows-it", "The page shows it.") + `}]}]}`,
			`approved node "a-page-shows-it" version 1: it has no time`},
		{"a block of another node", `{"nodes":[{"id":"a-page-shows-it","versions":[` + version("1", "2026-11-10T00:00:00Z", block("a-page-shows-all", "The page shows all.")) + `]}]}`,
			`approved node "a-page-shows-it" version 1: bad-approved-block: the block holds node "a-page-shows-all"`},
		{"a block of two nodes", `{"nodes":[{"id":"a-page-shows-it","versions":[` + version("1", "2026-11-10T00:00:00Z", block("a-page-shows-it", `The page shows it.\n:::\n\n:::term{#page name=\"page\"}\nA page is a view.`)) + `]}]}`,
			`approved node "a-page-shows-it" version 1: bad-approved-block: the block holds 2 nodes`},
		{"a block that Print does not write", `{"nodes":[{"id":"a-page-shows-it","versions":[` + version("1", "2026-11-10T00:00:00Z", `":::requirement{#a-page-shows-it  kind=\"behavior\"}\nThe page shows it.\n:::\n"`) + `]}]}`,
			`approved node "a-page-shows-it" version 1: bad-approved-block: the block is not as Print writes it`},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			a, err := ReadApproved(strings.NewReader(row.json))
			if err == nil || !strings.Contains(err.Error(), row.want) {
				t.Errorf("ReadApproved = %+v, %v; want an error holding %q", a, err, row.want)
			}
		})
	}
}

// ReadApproved reads each field of the fixture, and the current text of a node is its newest
// version unless the approved spec removed the node.
func TestCurrentIsTheNewestTextOfEachNodeThatIsNotRemoved(t *testing.T) {
	t.Parallel()
	approved := readApproved(t, "testdata/compare/removed-kept/approved.json")
	removed := approved.Nodes["an-order-is-paid-once"]
	if !removed.Removed || removed.PartlyChecked || len(removed.Versions) != 2 || removed.Versions[1].Version != 2 ||
		!removed.Versions[1].At.Equal(time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("Nodes[an-order-is-paid-once] = %+v", removed)
	}
	current, problems := approved.Current()
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	if got := ids(nodesOf(current), func(n anyNode) string { return n.id }); !reflect.DeepEqual(got, []string{
		"acme-keeps-each-order", "an-order-survives-a-restart", "orders-reach-billing-only-through-the-interface",
		"a-restart-keeps-each-order", "no-order-skips-the-interface",
		"billing", "orders", "orders-to-billing", "shipping", "order",
	}) {
		t.Errorf("Current holds %q", got)
	}
	if s, _ := current.StatementByID("an-order-survives-a-restart"); s.Text != "ACME keeps each stored order and its state after the order service starts again." {
		t.Errorf("Current text of an-order-survives-a-restart: %q, want version 2", s.Text)
	}
	if got, want := approved.Since("an-order-survives-a-restart"), time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Since(an-order-survives-a-restart) = %v, want %v", got, want)
	}
	for _, id := range []string{"an-order-is-paid-once", "no-such-node"} {
		if got := approved.Since(id); !got.IsZero() {
			t.Errorf("Since(%s) = %v, want the zero time", id, got)
		}
	}

	partly := readApproved(t, "testdata/compare/approved-text/approved.json")
	if partly.Nodes["acme-keeps-each-order"].PartlyChecked {
		t.Error("PartlyChecked read true from false")
	}
}

// An approved spec that a caller builds, as the pilot builds it from the store, can hold a block
// that does not parse. Current leaves that node out and names it with its version; Compare
// counts no text of it as approved.
func TestCurrentNamesABlockThatDoesNotParse(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	broken := ":::requirement{#a-page-shows-it kind=\"behavior\"}\nThe page shows it.\n::: and more\n:::\n"
	approved := Approved{Nodes: map[string]ApprovedNode{
		"a-page-shows-it": {ID: "a-page-shows-it", Versions: []ApprovedVersion{{Version: 1, At: at, Block: broken}}},
	}}
	current, problems := approved.Current()
	if len(current.Statements) != 0 {
		t.Errorf("Current holds %+v, want no statement", current.Statements)
	}
	want := []Problem{{Position{"approved a-page-shows-it version 1", 3}, "a-page-shows-it", "bad-block-line", "bad-block-line: the line opens no block"}}
	if !reflect.DeepEqual(problems, want) {
		t.Errorf("Current problems:\n got %+v\nwant %+v", problems, want)
	}
	files, _ := Parse(map[string][]byte{"spec.md": []byte(broken)})
	if got := Compare(files, approved).Problems; len(got) != 1 || got[0].Rule != "unapproved-text" {
		t.Errorf("Compare: %+v, want one unapproved-text", got)
	}
}

// readApproved reads the approved fixture at path and fails the test when ReadApproved refuses it.
func readApproved(t *testing.T, path string) Approved {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	approved, err := ReadApproved(f)
	if err != nil {
		t.Fatalf("ReadApproved(%s): %v", path, err)
	}
	return approved
}

// compareFixture compares the spec files of testdata/compare/<name>/spec with its approved.json.
func compareFixture(t *testing.T, name string) Comparison {
	t.Helper()
	dir := filepath.Join("testdata/compare", name)
	return Compare(parseDir(t, filepath.Join(dir, "spec")), readApproved(t, filepath.Join(dir, "approved.json")))
}
