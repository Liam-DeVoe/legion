//go:build basebuild

package api

// A diagnosis of INFRA-1098: what each statement of GET /api/v1/issues/{key}/references costs on an
// issue shaped like production's INFRA-1098, at each build REFS_COST_SHAS names.
//
// On 2026-11-24 at 11:55Z production answered that read of INFRA-1098 with 503 REFERENCES_TIMEOUT 7
// times of 10, each after 2.0-2.1 s. The route runs refs.Closure under one 2 s budget for all its
// statements (referencesClosureBound, boundedClosure and closureBudget in references.go), and
// Postgres's log put the cancel on different statements: the in statement twice, the comment
// loader of resolveNodes twice, the out statement once. The time is spread over the read's
// statements, so this test reads each one's share on a seed of the same shape, one build at a time.
//
// The test is TestIssueReferencesReadCost at the end of this file. It carries the basebuild tag
// and stands beside tree_read_cost_test.go, whose servers, client, database, owed counts and
// sampling it shares, and moved_read_cost_test.go, whose plan proxy and plans it shares, because
// it builds the server of each commit it names (testbaseline.Build), which needs a checkout that
// holds those commits. It skips unless REFS_COST_SHAS is set, and is run on its own, by name, as
//
//	REFS_COST_SHAS=<commit>,<commit> go test -tags basebuild ./internal/dispatch/api/ -run '^TestIssueReferencesReadCost$' -timeout 0 -count=1 -v
//
// from packages/envoy, with DISPATCH_TEST_DATABASE_URL naming a Postgres server it may create
// databases on, over TCP and without TLS (the plan proxy relays that connection in the clear),
// which preloads pg_stat_statements (readCostStatements; without it the statements' database time
// is not read, and the replays still are). READ_COST_KEEP_DATABASE=1, for diagnosis, keeps each
// build's database (readCostKeepDatabase), and REFS_COST_PPROF=1 takes a CPU profile of each
// build's server over its readings (refsCostProfiled).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"maps"
	"math"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/testbaseline"
)

// refsCostShasVariable names the builds the test measures: a comma-separated list of 40-character
// commits, each measured in turn, in the order named, on a database of its own.
const refsCostShasVariable = "REFS_COST_SHAS"

// refsCostPprofVariable, set to 1 for diagnosis, has each build's server serve its Go profiler and
// takes one CPU profile of it over its readings (refsCostProfiled).
const refsCostPprofVariable = "REFS_COST_PPROF"

// refsCostCheck starts every line the test prints about its builds.
const refsCostCheck = "refs-cost"

// The shape of K, production's INFRA-1098 as its API read it on 2026-11-24 at 11:55Z. Its own
// content: 13 markdown documents of refsCostDocumentBlocks blocks each, the first its spec, and
// refsCostFiles images and files; refsCostVersions versions in all, refsCostHeavyVersions of them
// the document of refsCostHeavyBlocks blocks'; refsCostComments comments, refsCostAsks asks and
// refsCostMessages messages.
var refsCostDocumentBlocks = []int{584, 375, 374, 333, 311, 290, 286, 233, 230, 201, 200, 77, 49}

const (
	refsCostFiles         = 26
	refsCostVersions      = 615
	refsCostHeavyBlocks   = 290
	refsCostHeavyVersions = 173
	refsCostComments      = 100
	refsCostAsks          = 25
	refsCostMessages      = 2
	// refsCostRequirements is how many requirement blocks K's spec holds, the blocks the serves of
	// K's children point at; each is two blocks of the spec, the typed block and its paragraph.
	refsCostRequirements = 30
)

// The links out of K's own content: 255 nodes, each by a mentions link. The documents are the
// answer's 65 members, each at depth 1.
const (
	refsCostOutIssues    = 136
	refsCostOutDocuments = 65
	refsCostOutComments  = 37
	refsCostOutAsks      = 17
	refsCostLinks        = refsCostOutIssues + refsCostOutDocuments + refsCostOutComments + refsCostOutAsks
)

// The links into K and its own content: 654 nodes of other issues, 647 of them by a mentions link
// and 25 by a serves link, each mentioning dispatch://K, one of K's documents or one of their
// blocks. A serves is a check block's pointer at a requirement of K's spec, which only a document
// of an issue under K may hold (pmdoc's documentReach), so the 25 serving documents are each the
// Verification document of a child of K; refsCostServesAlone of them hold the serves alone and the
// rest a mention too. Every other citing document, comment, ask and message stands on one of
// refsCostCiting issues.
const (
	refsCostInDocuments = 341
	refsCostInComments  = 250
	refsCostInAsks      = 33
	refsCostInMessages  = 30
	refsCostLinkedFrom  = refsCostInDocuments + refsCostInComments + refsCostInAsks + refsCostInMessages
	refsCostInMentions  = 647
	refsCostInServes    = 25
	refsCostServesAlone = refsCostLinkedFrom - refsCostInMentions
	refsCostCiting      = 100
)

// The rows beside them, which cite nothing of K: refsCostUnrelated issues of another project, each
// with its spec of refsCostUnrelatedBlocks blocks and refsCostUnrelatedComments comments, so the
// indexes the read probes are not tiny.
const (
	refsCostUnrelated         = 2_000
	refsCostUnrelatedBlocks   = 20
	refsCostUnrelatedComments = 3
)

// refsCostShapeSlack is how far a count read back may stray from the shape's before the seed is
// held to be another shape.
const refsCostShapeSlack = 0.10

// The projects, the agent sessions that open the asks, and the read's own measures.
const (
	refsCostProject   = "REFS"
	refsCostElsewhere = "ELSE"
	refsCostAskers    = 5
	// refsCostWarmUps is how many readings of the read are taken, and not counted, before its
	// readCostRuns readings.
	refsCostWarmUps = 20
	// refsCostDriveTries is how many times the read is driven through the plan proxy before the
	// test takes the statements of a read that never answered 200 (driveRefsCost).
	refsCostDriveTries = 20
	// refsCostDriven is the name the plan proxy keeps the read's statements under.
	refsCostDriven = "the references read"
	// refsCostReplayStatement is the name a statement is prepared under on the test's connection
	// while it is replayed.
	refsCostReplayStatement = "refs_cost_replay"
)

// refsCostBoundDeclaration is the line of references.go that declares referencesClosureBound, its
// value the second group; refsCostLiteralDuration is a value that reads no setting, with or without
// a comment after it.
var (
	refsCostBoundDeclaration = regexp.MustCompile(`(?m)^\s*((?:const|var)\s+)?referencesClosureBound\b[^=\n]*=\s*(.*)$`)
	refsCostLiteralDuration  = regexp.MustCompile(`^[0-9][0-9_.]*\s*\*\s*time\.(Second|Millisecond)\s*(//.*)?$`)
)

// ───── the seed, written through each build's own API ─────────────────────

// refsCostDocument is a document the seed wrote: its artifact id, its name and its dispatch://
// address, and, for one of K's markdown documents, the blocks and the versions the shape gives it.
type refsCostDocument struct {
	id, name, address string
	blocks, versions  int
}

// refsCostSeed is what the measures address in the seed: K's key, K's markdown documents, its
// spec first in refsCostDocumentBlocks' order, and its images and files; the blocks the blocks read
// lists for K's markdown documents together; and the addresses of K's documents and of some of
// their blocks that the citing content cites beside dispatch://K (inbound).
type refsCostSeed struct {
	issue                         string
	documents, files              []refsCostDocument
	blocks                        int
	documentTargets, blockTargets []string
}

// inbound is what the index-th citing source cites: dispatch://K for half of them, one of K's
// documents for a quarter, and one of their blocks for the last quarter, each list in turn.
func (seed *refsCostSeed) inbound(index int) string {
	switch index % 4 {
	case 2:
		return seed.documentTargets[(index/4)%len(seed.documentTargets)]
	case 3:
		return seed.blockTargets[(index/4)%len(seed.blockTargets)]
	}
	return "dispatch://" + seed.issue
}

// seedRefsCost writes the seed through the server's own API, as its own writers store it: no row
// is written by SQL. The issues and the items K's content cites come first, since K's content
// names them by key and id; then K and its own content; then the content citing K, which names
// K's blocks; then the unrelated rows.
func seedRefsCost(t *testing.T, s *readCostServer) *refsCostSeed {
	t.Helper()
	started := time.Now()
	for _, person := range []string{readCostReader, readCostWriter, readCostStranger} {
		s.expect(t, "sign in "+person, http.MethodGet, "/api/v1/whoami", person, nil, http.StatusOK)
	}
	s.expect(t, "create the project "+refsCostProject, http.MethodPost, "/api/v1/projects", readCostWriter,
		map[string]string{"key": refsCostProject, "name": "The issue whose references are read"}, http.StatusCreated)
	s.expect(t, "create the project "+refsCostElsewhere, http.MethodPost, "/api/v1/projects", readCostStranger,
		map[string]string{"key": refsCostElsewhere, "name": "Rows that cite nothing of the issue"}, http.StatusCreated)
	seed := seedRefsCostIssue(t, s, seedRefsCostOutbound(t, s))
	seedRefsCostInbound(t, s, seed)
	seedRefsCostUnrelated(t, s)
	t.Logf("%s: seeded K, %s, and the rows beside it through %s in %s", refsCostCheck, seed.issue, s.name, time.Since(started).Round(time.Millisecond))
	return seed
}

// seedRefsCostOutbound writes what K's content cites and answers the addresses, in this order:
// refsCostOutIssues issues, refsCostOutDocuments documents of those issues, refsCostOutComments
// comments and refsCostOutAsks asks on them.
func seedRefsCostOutbound(t *testing.T, s *readCostServer) []string {
	t.Helper()
	issues := make([]string, refsCostOutIssues)
	readCostInParallel(t, "issues K's content cites", refsCostOutIssues, func(index int) error {
		title := fmt.Sprintf("Neighbor %03d of the issue", index)
		key, err := refsCostCreateIssue(s, refsCostProject, title, refsCostDocumentText(title, 4, 1, nil), readCostWriter, "")
		issues[index] = key
		return err
	})
	documents := make([]string, refsCostOutDocuments)
	readCostInParallel(t, "documents of other issues K's content cites", refsCostOutDocuments, func(index int) error {
		title := fmt.Sprintf("Design %03d", index)
		uploaded, err := refsCostUpload(s, issues[index%len(issues)], fmt.Sprintf("design-%03d.md", index),
			refsCostDocumentText(title, 12, 1, nil), readCostWriter)
		documents[index] = uploaded.address
		return err
	})
	comments := make([]string, refsCostOutComments)
	readCostInParallel(t, "comments of other issues K's content cites", refsCostOutComments, func(index int) error {
		issue := issues[index%len(issues)]
		id, err := refsCostComment(s, issue, fmt.Sprintf("Comment %d on a neighbor of the issue.", index), readCostWriter)
		comments[index] = "dispatch://" + issue + "/comment/" + id
		return err
	})
	asks := make([]string, refsCostOutAsks)
	readCostInParallel(t, "asks of other issues K's content cites", refsCostOutAsks, func(index int) error {
		issue := issues[index%len(issues)]
		id, err := refsCostAsk(s, issue, fmt.Sprintf("Which way does question %d of a neighbor of the issue go?", index), index)
		asks[index] = "dispatch://" + issue + "/ask/" + id
		return err
	})
	addresses := make([]string, 0, refsCostLinks)
	for _, issue := range issues {
		addresses = append(addresses, "dispatch://"+issue)
	}
	return slices.Concat(addresses, documents, comments, asks)
}

// seedRefsCostIssue writes K and its own content as its writer, each address of outbound cited by
// one of its sources in turn - its markdown documents, its comments, its asks and its messages -
// and then reads the blocks of each of its markdown documents, for the shape and for the blocks
// the citing content cites.
func seedRefsCostIssue(t *testing.T, s *readCostServer, outbound []string) *refsCostSeed {
	t.Helper()
	sources := len(refsCostDocumentBlocks) + refsCostComments + refsCostAsks + refsCostMessages
	cites := make([][]string, sources)
	for index, address := range outbound {
		cites[index%sources] = append(cites[index%sources], address)
	}
	seed := &refsCostSeed{}
	seed.issue = createReadCostIssue(t, s, refsCostProject, "The issue whose references are read", refsCostSpecText(cites[0]), readCostWriter)
	seed.documents = append(seed.documents, refsCostDocument{id: readCostSpecOf(t, s, seed.issue), name: "spec",
		address: "dispatch://" + seed.issue + "/spec", blocks: refsCostDocumentBlocks[0], versions: 1})
	for index, versions := range refsCostVersionsOf() {
		seed.documents = append(seed.documents, refsCostDocument{name: fmt.Sprintf("notes-%02d.md", index+1),
			blocks: refsCostDocumentBlocks[index+1], versions: versions})
	}
	readCostInParallel(t, "of K's markdown documents but its spec, every version of each in turn", len(seed.documents)-1, func(index int) error {
		document := &seed.documents[index+1]
		title := fmt.Sprintf("Notes %02d of the issue", index+1)
		for version := 1; version <= document.versions; version++ {
			uploaded, err := refsCostUpload(s, seed.issue, document.name, refsCostDocumentText(title, document.blocks, version, cites[index+1]), readCostWriter)
			if err != nil {
				return fmt.Errorf("version %d of %s: %w", version, document.name, err)
			}
			document.id, document.address = uploaded.id, uploaded.address
		}
		return nil
	})
	seed.files = make([]refsCostDocument, refsCostFiles)
	readCostInParallel(t, "images and files of K", refsCostFiles, func(index int) error {
		name, contentType, content, err := refsCostFile(index)
		if err != nil {
			return err
		}
		seed.files[index], err = refsCostUploadFile(s, seed.issue, name, contentType, content, readCostWriter)
		return err
	})
	next := len(seed.documents)
	readCostInParallel(t, "comments on K", refsCostComments, func(index int) error {
		_, err := refsCostComment(s, seed.issue, refsCostCitingText(fmt.Sprintf("Comment %d of the issue's conversation.", index+1), cites[next+index]), readCostWriter)
		return err
	})
	next += refsCostComments
	readCostInParallel(t, "asks on K", refsCostAsks, func(index int) error {
		_, err := refsCostAsk(s, seed.issue, refsCostCitingText(fmt.Sprintf("Which way does question %d of the issue go?", index+1), cites[next+index]), index)
		return err
	})
	next += refsCostAsks
	readCostInParallel(t, "messages on K", refsCostMessages, func(index int) error {
		_, err := refsCostMessage(s, seed.issue, refsCostCitingText(fmt.Sprintf("Message %d on the issue.", index+1), cites[next+index]), readCostWriter)
		return err
	})

	for _, document := range seed.documents {
		var blocks []readCostBlock
		s.read(t, "read the blocks of K's "+document.name, "/api/v1/artifacts/"+document.id+"/blocks", &blocks)
		t.Logf("%s: %s lists %d blocks of K's %s, written as %d blocks in %d versions", refsCostCheck, s.name, len(blocks), document.name,
			document.blocks, document.versions)
		seed.blocks += len(blocks)
		seed.documentTargets = append(seed.documentTargets, document.address)
		for _, at := range []int{len(blocks) / 4, len(blocks) / 2, 3 * len(blocks) / 4} {
			if at < len(blocks) {
				seed.blockTargets = append(seed.blockTargets, document.address+"#b-"+blocks[at].ID)
			}
		}
	}
	for index := range refsCostRequirements {
		seed.blockTargets = append(seed.blockTargets, fmt.Sprintf("dispatch://%s/spec#b-r%d", seed.issue, index+1))
	}
	for _, file := range seed.files {
		seed.documentTargets = append(seed.documentTargets, file.address)
	}
	return seed
}

// seedRefsCostInbound writes the content of other issues that cites K: refsCostCiting issues, and
// on them the citing documents but the serving ones, the citing comments, asks and messages; and
// refsCostInServes children of K, each with a Verification document whose check serves a
// requirement of K's spec, all but refsCostServesAlone of them citing K too. Each citing source
// cites one address (refsCostSeed.inbound), the sources numbered in the order they are written.
func seedRefsCostInbound(t *testing.T, s *readCostServer, seed *refsCostSeed) {
	t.Helper()
	citing := make([]string, refsCostCiting)
	readCostInParallel(t, "issues whose content cites K", refsCostCiting, func(index int) error {
		title := fmt.Sprintf("Citing issue %03d", index)
		key, err := refsCostCreateIssue(s, refsCostProject, title, refsCostDocumentText(title, 6, 1, nil), readCostWriter, "")
		citing[index] = key
		return err
	})
	cited := 0
	documents := refsCostInDocuments - refsCostInServes
	readCostInParallel(t, "documents of other issues citing K", documents, func(index int) error {
		title := fmt.Sprintf("Citing note %03d", index)
		_, err := refsCostUpload(s, citing[index%len(citing)], fmt.Sprintf("citing-note-%03d.md", index),
			refsCostDocumentText(title, 12, 1, []string{seed.inbound(cited + index)}), readCostWriter)
		return err
	})
	cited += documents
	children := make([]string, refsCostInServes)
	readCostInParallel(t, "children of K", refsCostInServes, func(index int) error {
		title := fmt.Sprintf("Child %02d of the issue", index)
		key, err := refsCostCreateIssue(s, refsCostProject, title, refsCostDocumentText(title, 4, 1, nil), readCostWriter, seed.issue)
		children[index] = key
		return err
	})
	both := refsCostInServes - refsCostServesAlone
	readCostInParallel(t, "Verification documents of K's children, each serving a requirement of K's spec", refsCostInServes, func(index int) error {
		var cites []string
		if index < both {
			cites = []string{seed.inbound(cited + index)}
		}
		_, err := refsCostUpload(s, children[index], "verification.md", refsCostServingText(seed.issue, index, cites), readCostWriter)
		return err
	})
	cited += both
	readCostInParallel(t, "comments of other issues citing K", refsCostInComments, func(index int) error {
		_, err := refsCostComment(s, citing[index%len(citing)], refsCostCitingText(fmt.Sprintf("Comment %d of a citing issue.", index+1),
			[]string{seed.inbound(cited + index)}), readCostWriter)
		return err
	})
	cited += refsCostInComments
	readCostInParallel(t, "asks of other issues citing K", refsCostInAsks, func(index int) error {
		_, err := refsCostAsk(s, citing[index%len(citing)], refsCostCitingText(fmt.Sprintf("Which way does question %d of a citing issue go?", index+1),
			[]string{seed.inbound(cited + index)}), index)
		return err
	})
	cited += refsCostInAsks
	readCostInParallel(t, "messages of other issues citing K", refsCostInMessages, func(index int) error {
		_, err := refsCostMessage(s, citing[index%len(citing)], refsCostCitingText(fmt.Sprintf("Message %d on a citing issue.", index+1),
			[]string{seed.inbound(cited + index)}), readCostWriter)
		return err
	})
}

// seedRefsCostUnrelated writes the rows that cite nothing of K: refsCostUnrelated issues of another
// person in another project, each with its spec and refsCostUnrelatedComments comments.
func seedRefsCostUnrelated(t *testing.T, s *readCostServer) {
	t.Helper()
	issues := make([]string, refsCostUnrelated)
	readCostInParallel(t, "unrelated issues, each with its spec", refsCostUnrelated, func(index int) error {
		title := fmt.Sprintf("Unrelated ledger %04d", index)
		key, err := refsCostCreateIssue(s, refsCostElsewhere, title, refsCostDocumentText(title, refsCostUnrelatedBlocks, 1, nil), readCostStranger, "")
		issues[index] = key
		return err
	})
	readCostInParallel(t, "comments on the unrelated issues", refsCostUnrelated*refsCostUnrelatedComments, func(index int) error {
		_, err := refsCostComment(s, issues[index/refsCostUnrelatedComments],
			fmt.Sprintf("Entry %d of the ledger records the tide at the lighthouse.", index%refsCostUnrelatedComments+1), readCostStranger)
		return err
	})
}

// refsCostVersionsOf is how many versions each of K's markdown documents but its spec is written
// in: the document of refsCostHeavyBlocks blocks refsCostHeavyVersions, and the others the rest
// of refsCostVersions, less the spec's one and the files' one each, as evenly as it divides, the
// first of them taking one more.
func refsCostVersionsOf() []int {
	documents := refsCostDocumentBlocks[1:]
	shared, others := refsCostVersions-1-refsCostFiles-refsCostHeavyVersions, len(documents)-1
	versions := make([]int, len(documents))
	next := 0
	for index, blocks := range documents {
		if blocks == refsCostHeavyBlocks {
			versions[index] = refsCostHeavyVersions
			continue
		}
		versions[index] = shared / others
		if next < shared%others {
			versions[index]++
		}
		next++
	}
	return versions
}

// refsCostBlocks is how many blocks K's markdown documents hold together in the shape.
func refsCostBlocks() int {
	total := 0
	for _, blocks := range refsCostDocumentBlocks {
		total += blocks
	}
	return total
}

// refsCostSpecText is K's spec: a heading, refsCostRequirements requirement blocks, a paragraph
// citing each address of cites, and paragraphs filling it out to refsCostDocumentBlocks[0] blocks.
func refsCostSpecText(cites []string) string {
	var text strings.Builder
	text.WriteString("# The issue whose references are read\n\n")
	for index := range refsCostRequirements {
		fmt.Fprintf(&text, ":::requirement{#r%d}\nRequirement %d of the issue holds.\n:::\n\n", index+1, index+1)
	}
	for index, address := range cites {
		fmt.Fprintf(&text, "Paragraph %d of the spec cites %s for what it settles.\n\n", index+1, address)
	}
	for index := 1 + 2*refsCostRequirements + len(cites); index < refsCostDocumentBlocks[0]; index++ {
		fmt.Fprintf(&text, "Paragraph %d of the spec records one more step of the work.\n\n", index+1)
	}
	return text.String()
}

// refsCostDocumentText is a markdown document of blocks blocks: a heading, a paragraph naming its
// revision, which tells each version from the one before, a paragraph citing each address of
// cites, and paragraphs filling it out. Its words write no issue key but the addresses it cites.
func refsCostDocumentText(title string, blocks, revision int, cites []string) string {
	var text strings.Builder
	fmt.Fprintf(&text, "# %s\n\nRevision %d of %s.\n\n", title, revision, title)
	for index, address := range cites {
		fmt.Fprintf(&text, "Paragraph %d of %s cites %s for what it settles.\n\n", index+1, title, address)
	}
	for index := 2 + len(cites); index < blocks; index++ {
		fmt.Fprintf(&text, "Paragraph %d of %s records one more step of the work.\n\n", index-1, title)
	}
	return text.String()
}

// refsCostServingText is the Verification document of the index-th child of issue: one check
// serving a requirement of issue's spec, and a paragraph citing each address of cites.
func refsCostServingText(issue string, index int, cites []string) string {
	var text strings.Builder
	fmt.Fprintf(&text, ":::check{#c1 serves=\"dispatch://%s/spec#b-r%d\" surface=\"cli\" run=\"go test ./...\"}\n"+
		"Check 1 of child %d proves the requirement it serves.\n:::\n\n", issue, index%refsCostRequirements+1, index+1)
	for _, address := range cites {
		fmt.Fprintf(&text, "Child %d of the issue cites %s for what it settles.\n\n", index+1, address)
	}
	return text.String()
}

// refsCostCitingText is lead, then a sentence citing each address of cites.
func refsCostCitingText(lead string, cites []string) string {
	var text strings.Builder
	text.WriteString(lead)
	for _, address := range cites {
		fmt.Fprintf(&text, " It cites %s for what it settles.", address)
	}
	return text.String()
}

// refsCostFile is the index-th of K's images and files: a PNG for an even index, which the server
// stores as an image, and a text file for an odd one, which it stores as a file.
func refsCostFile(index int) (name, contentType string, content []byte, err error) {
	if index%2 == 1 {
		return fmt.Sprintf("log-%02d.txt", index), "text/plain", fmt.Appendf(nil, "Log %02d of the issue's runs.\n", index), nil
	}
	picture := image.NewGray(image.Rect(0, 0, 16, 16))
	picture.SetGray(index%16, 0, color.Gray{Y: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		return "", "", nil, fmt.Errorf("encode figure %d: %w", index, err)
	}
	return fmt.Sprintf("figure-%02d.png", index), "image/png", encoded.Bytes(), nil
}

// ───── the writes, each of which a writer goroutine may call ──────────────

// refsCostCreateIssue creates an issue of project held by assignee, who also writes it, under
// parent where one is named, and answers its key. It is forced past the near-copy check, which
// the seed's titles, numbered alike, would trip.
func refsCostCreateIssue(s *readCostServer, project, title, spec, assignee, parent string) (string, error) {
	body := map[string]any{"project": project, "title": title, "force": true, "assignee": assignee, "spec": spec}
	if parent != "" {
		body["parent"] = parent
	}
	answer, err := s.write(http.MethodPost, "/api/v1/issues", assignee, body, http.StatusCreated)
	if err != nil {
		return "", err
	}
	var created struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(answer.body, &created); err != nil || created.Key == "" {
		return "", fmt.Errorf("read the issue %q back: %v: %s", title, err, answer)
	}
	return created.Key, nil
}

// refsCostUpload writes a version of the markdown document name of issue as login: its first, or
// one replacing the version before whole.
func refsCostUpload(s *readCostServer, issue, name, content, login string) (refsCostDocument, error) {
	answer, err := s.write(http.MethodPost, "/api/v1/issues/"+issue+"/artifacts", login,
		map[string]string{"name": name, "content": content}, http.StatusCreated)
	if err != nil {
		return refsCostDocument{}, err
	}
	return refsCostUploaded(issue, answer)
}

// refsCostUploadFile writes the file name of issue as login as a multipart upload, its bytes
// declared as contentType.
func refsCostUploadFile(s *readCostServer, issue, name, contentType string, content []byte, login string) (refsCostDocument, error) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("name", name); err != nil {
		return refsCostDocument{}, err
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, name))
	header.Set("Content-Type", contentType)
	part, err := form.CreatePart(header)
	if err != nil {
		return refsCostDocument{}, err
	}
	if _, err := part.Write(content); err != nil {
		return refsCostDocument{}, err
	}
	if err := form.Close(); err != nil {
		return refsCostDocument{}, err
	}
	request, err := http.NewRequest(http.MethodPost, s.url("/api/v1/issues/"+issue+"/artifacts"), &body)
	if err != nil {
		return refsCostDocument{}, err
	}
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("X-Dispatch-User", login)
	response, err := s.client.Do(request)
	if err != nil {
		return refsCostDocument{}, fmt.Errorf("upload %s to %s on %s: %w", name, issue, s.name, err)
	}
	defer response.Body.Close()
	read, err := io.ReadAll(response.Body)
	if err != nil {
		return refsCostDocument{}, fmt.Errorf("read the answer to the upload of %s to %s on %s: %w", name, issue, s.name, err)
	}
	answer := readCostAnswer{status: response.StatusCode, body: read}
	if answer.status != http.StatusCreated {
		return refsCostDocument{}, fmt.Errorf("the upload of %s to %s on %s answered %s, want %d", name, issue, s.name, answer, http.StatusCreated)
	}
	return refsCostUploaded(issue, answer)
}

// refsCostUploaded is the document an upload to issue answered: its id, its name, and its address
// by its slug.
func refsCostUploaded(issue string, answer readCostAnswer) (refsCostDocument, error) {
	var uploaded struct {
		Artifact struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"artifact"`
	}
	if err := json.Unmarshal(answer.body, &uploaded); err != nil || uploaded.Artifact.ID == "" || uploaded.Artifact.Slug == "" {
		return refsCostDocument{}, fmt.Errorf("read the upload to %s back: %v: %s", issue, err, answer)
	}
	return refsCostDocument{id: uploaded.Artifact.ID, name: uploaded.Artifact.Name,
		address: "dispatch://" + issue + "/artifact/" + uploaded.Artifact.Slug}, nil
}

// refsCostComment writes a comment on issue as login and answers its id.
func refsCostComment(s *readCostServer, issue, body, login string) (string, error) {
	answer, err := s.write(http.MethodPost, "/api/v1/issues/"+issue+"/comments", login, map[string]string{"body": body}, http.StatusCreated)
	if err != nil {
		return "", err
	}
	return readCostIDOf(answer)
}

// refsCostAsk opens an ask on issue as the asker-th of refsCostAskers agent sessions, on the shared
// agent bearer, and answers its id.
func refsCostAsk(s *readCostServer, issue, question string, asker int) (string, error) {
	answer, err := s.write(http.MethodPost, "/api/v1/issues/"+issue+"/asks", "", map[string]any{
		"question": question,
		"actor":    map[string]string{"kind": "session", "id": fmt.Sprintf("ses_refs_cost_asker_%02d", asker%refsCostAskers)},
	}, http.StatusCreated)
	if err != nil {
		return "", err
	}
	return readCostIDOf(answer)
}

// refsCostMessage writes a message on issue as login and answers its id.
func refsCostMessage(s *readCostServer, issue, body, login string) (string, error) {
	answer, err := s.write(http.MethodPost, "/api/v1/issues/"+issue+"/messages", login, map[string]string{"body": body}, http.StatusCreated)
	if err != nil {
		return "", err
	}
	return readCostIDOf(answer)
}

// ───── the build, what it owes, and the shape it answers ──────────────────

// logRefsCostBound prints how commit declares referencesClosureBound, read from its own
// references.go: no build can be given another bound from outside unless it reads one from a
// setting, and the replays (replayRefsCost) give each statement's time whatever the bound.
func logRefsCostBound(t *testing.T, commit string) {
	t.Helper()
	const file = "packages/envoy/internal/dispatch/api/references.go"
	source, err := exec.Command("git", "show", commit+":"+file).Output()
	if err != nil {
		t.Logf("%s: build %s holds no %s (git show: %v), so how it bounds the read was not read; each statement is replayed on its own",
			refsCostCheck, commit, file, err)
		return
	}
	declared := refsCostBoundDeclaration.FindSubmatch(source)
	switch {
	case declared == nil:
		t.Logf("%s: build %s declares no referencesClosureBound in %s; each statement is replayed on its own", refsCostCheck, commit, file)
	case refsCostLiteralDuration.Match(bytes.TrimSpace(declared[2])):
		t.Logf("%s: build %s reads referencesClosureBound from nothing configurable (%s: %s), so the route is measured under that "+
			"bound as it is, and each statement is replayed on its own to give its time where the route answers 503",
			refsCostCheck, commit, file, bytes.TrimSpace(declared[0]))
	default:
		t.Logf("%s: build %s declares referencesClosureBound as %q in %s, which is no literal duration; this diagnosis knows no setting "+
			"that raises it, so the route is measured under the bound the build reads by default, and each statement is replayed on its own",
			refsCostCheck, commit, bytes.TrimSpace(declared[0]), file)
	}
}

// refsCostSourceHolds reports whether commit's tree under directory, a path from the repository's
// root, holds text.
func refsCostSourceHolds(t *testing.T, commit, text, directory string) bool {
	t.Helper()
	err := exec.Command("git", "grep", "--quiet", "--fixed-strings", text, commit, "--", ":(top)"+directory).Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false
	}
	t.Fatalf("%s: search build %s's %s for %q: %v", refsCostCheck, commit, directory, text, err)
	return false
}

// refsCostOwedCounts is what commit's server, booted over conn's database, can be waited on to
// owe nothing of: each count of readCostHeadOwed whose SQL reads a table and columns the build
// migrates, or where it reads none, readCostBaseOwed's count of the same name where that one
// does; a count of neither is said and left out. args is the commit where a count kept binds $1.
func refsCostOwedCounts(t *testing.T, conn *pgx.Conn, commit string) (counts []readCostOwedCount, args []any) {
	t.Helper()
	readable := func(count readCostOwedCount) (bool, error) {
		var countArgs []any
		if strings.Contains(count.sql, "$1") {
			countArgs = []any{commit}
		}
		var value int64
		err := conn.QueryRow(context.Background(), "select "+count.sql, countArgs...).Scan(&value)
		var postgres *pgconn.PgError
		if errors.As(err, &postgres) && (postgres.Code == "42P01" || postgres.Code == "42703") {
			return false, nil
		}
		return err == nil, err
	}
	for _, count := range readCostHeadOwed {
		candidates := []readCostOwedCount{count}
		for _, base := range readCostBaseOwed {
			if base.name == count.name && base.sql != count.sql {
				candidates = append(candidates, base)
			}
		}
		kept := false
		for _, candidate := range candidates {
			held, err := readable(candidate)
			if err != nil {
				t.Fatalf("%s: build %s: read the owed count %s: %v", refsCostCheck, commit, candidate.name, err)
			}
			if held {
				counts, kept = append(counts, candidate), true
				if strings.Contains(candidate.sql, "$1") {
					args = []any{commit}
				}
				break
			}
		}
		if !kept {
			t.Logf("%s: build %s migrates no table or column the owed count %s reads, so the build is not waited on for it", refsCostCheck, commit, count.name)
		}
	}
	return counts, args
}

// waitRefsCostOwed waits until s owes nothing of counts (waitReadCostOwed), and, where its build
// logs one (backfill), until it has logged the end of its boot's backfill (readCostBackfillDone).
func waitRefsCostOwed(t *testing.T, conn *pgx.Conn, s *readCostServer, counts []readCostOwedCount, args []any, backfill bool) {
	t.Helper()
	var ended func() bool
	if backfill {
		ended = func() bool { return strings.Contains(s.process.Output.String(), readCostBackfillDone) }
	}
	waitReadCostOwed(t, conn, s.name, counts, readCostDrainWait, ended, "boot_backfill_done", args...)
}

// holdRefsCostCount prints one count side read beside the shape's, and fails the test where it
// strays from it by more than refsCostShapeSlack. It fails without stopping, so the build's
// readings are still taken and printed beside the failure.
func holdRefsCostCount(t *testing.T, side, what string, read, want int) {
	t.Helper()
	t.Logf("%s: %s holds %s: %d, where production's INFRA-1098 holds %d", refsCostCheck, side, what, read, want)
	if math.Abs(float64(read-want)) > refsCostShapeSlack*float64(want) {
		t.Errorf("%s: %s holds %s: %d, more than %.0f%% from production's %d, so the seed is not INFRA-1098's shape",
			refsCostCheck, side, what, read, refsCostShapeSlack*100, want)
	}
}

// holdRefsCostContent reads K's own content back through s and holds it to the shape: its
// documents, those of them that are markdown and the rest, the blocks of the markdown ones, its
// versions in all and the heavy document's, and its comments, asks and messages.
func holdRefsCostContent(t *testing.T, s *readCostServer, seed *refsCostSeed) {
	t.Helper()
	var documents []struct {
		Name     string            `json:"name"`
		Kind     string            `json:"kind"`
		Versions []json.RawMessage `json:"versions"`
	}
	s.read(t, "read K's documents", "/api/v1/issues/"+seed.issue+"/artifacts", &documents)
	markdown, versions, heavy := 0, 0, -1
	for _, document := range documents {
		if document.Kind == "doc" {
			markdown++
		}
		versions += len(document.Versions)
		for _, written := range seed.documents {
			if written.blocks == refsCostHeavyBlocks && written.name == document.Name {
				heavy = len(document.Versions)
			}
		}
	}
	var comments, asks, messages []json.RawMessage
	s.read(t, "read K's comments", "/api/v1/issues/"+seed.issue+"/comments", &comments)
	s.read(t, "read K's asks", "/api/v1/issues/"+seed.issue+"/asks?state=all", &asks)
	s.read(t, "read K's messages", "/api/v1/issues/"+seed.issue+"/messages", &messages)
	side := s.name + " (K is " + seed.issue + ")"
	holdRefsCostCount(t, side, "documents of K", len(documents), len(refsCostDocumentBlocks)+refsCostFiles)
	holdRefsCostCount(t, side, "markdown documents of K", markdown, len(refsCostDocumentBlocks))
	holdRefsCostCount(t, side, "images and files of K", len(documents)-markdown, refsCostFiles)
	holdRefsCostCount(t, side, "blocks of K's markdown documents", seed.blocks, refsCostBlocks())
	holdRefsCostCount(t, side, "versions of K's documents", versions, refsCostVersions)
	holdRefsCostCount(t, side, fmt.Sprintf("versions of K's document of %d blocks", refsCostHeavyBlocks), heavy, refsCostHeavyVersions)
	holdRefsCostCount(t, side, "comments on K", len(comments), refsCostComments)
	holdRefsCostCount(t, side, "asks on K", len(asks), refsCostAsks)
	holdRefsCostCount(t, side, "messages on K", len(messages), refsCostMessages)
}

// refsCostLink is what the shape reads of one row of links or linked_from: its node's kind and the
// kinds of its links.
type refsCostLink struct {
	Node struct {
		Kind string `json:"kind"`
	} `json:"node"`
	Kinds []string `json:"kinds"`
}

// refsCostKinds counts links by their node's kind.
func refsCostKinds(links []refsCostLink) map[string]int {
	kinds := map[string]int{}
	for _, link := range links {
		kinds[link.Node.Kind]++
	}
	return kinds
}

// refsCostKindLine is kinds as a line, kind by kind in order.
func refsCostKindLine(kinds map[string]int) string {
	parts := make([]string, 0, len(kinds))
	for _, kind := range slices.Sorted(maps.Keys(kinds)) {
		parts = append(parts, fmt.Sprintf("%d %s", kinds[kind], kind))
	}
	return strings.Join(parts, ", ")
}

// holdRefsCostAnswer prints what one 200 answer of the read, body, holds - its members, its links
// and its linked_from by their node's kind, the linked_from by a mentions and by a serves link,
// and its size - and holds each count to the shape. from says which answer it is. The size is the
// JSON the client read, after the transport undid any compression; production's is not stated, so
// it is printed and held to nothing.
func holdRefsCostAnswer(t *testing.T, side, from string, body []byte) {
	t.Helper()
	var answer struct {
		Members    []json.RawMessage `json:"members"`
		Links      []refsCostLink    `json:"links"`
		LinkedFrom []refsCostLink    `json:"linked_from"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Errorf("%s: %s: decode %s: %v", refsCostCheck, side, from, err)
		return
	}
	links, linked := refsCostKinds(answer.Links), refsCostKinds(answer.LinkedFrom)
	mentions, serves := 0, 0
	for _, link := range answer.LinkedFrom {
		if slices.Contains(link.Kinds, "mentions") {
			mentions++
		}
		if slices.Contains(link.Kinds, "serves") {
			serves++
		}
	}
	t.Logf("%s: %s: %s holds %d members, %d links (%s) and %d linked_from (%s; %d by a mentions link, %d by a serves link), in %d bytes of JSON",
		refsCostCheck, side, from, len(answer.Members), len(answer.Links), refsCostKindLine(links), len(answer.LinkedFrom), refsCostKindLine(linked),
		mentions, serves, len(body))
	for _, held := range []struct {
		what       string
		read, want int
	}{
		{"members", len(answer.Members), refsCostOutDocuments},
		{"links", len(answer.Links), refsCostLinks},
		{"links to issues", links[graphstore.KindIssue], refsCostOutIssues},
		{"links to documents", links[graphstore.KindArtifact], refsCostOutDocuments},
		{"links to comments", links[graphstore.KindComment], refsCostOutComments},
		{"links to asks", links[graphstore.KindAsk], refsCostOutAsks},
		{"linked_from", len(answer.LinkedFrom), refsCostLinkedFrom},
		{"documents linking in", linked[graphstore.KindArtifact], refsCostInDocuments},
		{"comments linking in", linked[graphstore.KindComment], refsCostInComments},
		{"asks linking in", linked[graphstore.KindAsk], refsCostInAsks},
		{"messages linking in", linked[graphstore.KindMessage], refsCostInMessages},
		{"linked_from by a mentions link", mentions, refsCostInMentions},
		{"linked_from by a serves link", serves, refsCostInServes},
	} {
		holdRefsCostCount(t, side, held.what, held.read, held.want)
	}
}

// ───── the readings ───────────────────────────────────────────────────────

// refsCostReading is the build's readings of the read: their nearest-rank p50, p90 and p99, every
// reading in the order taken, how many answered 503, the database time of the readings and of the
// idle interval after them (readCostStatements), and the body of the first answer of 200 among the
// warm-ups and the readings, nil where none answered 200.
type refsCostReading struct {
	p50, p90, p99  time.Duration
	runs           []time.Duration
	unavailable    int
	database, idle *readCostDatabaseTime
	answer         []byte
}

// refsCostPercentiles is runs' nearest-rank p50, p90 and p99 (percentileOfDismissedScale).
func refsCostPercentiles(runs []time.Duration) (p50, p90, p99 time.Duration) {
	sorted := slices.Sorted(slices.Values(runs))
	return percentileOfDismissedScale(sorted, 0.50), percentileOfDismissedScale(sorted, 0.90), percentileOfDismissedScale(sorted, 0.99)
}

// measureRefsCostReads reads path on s as the reader refsCostWarmUps times, uncounted, and then
// readCostRuns times between two snapshots of the database time (readCostStatements.measure). A
// reading is its answer's time whether it answered 200 or 503 REFERENCES_TIMEOUT; any other answer
// fails the test. Where s serves its profiler (REFS_COST_PPROF), the readings take one CPU profile
// of it (startReadCostProfile), written to $TMPDIR/read-cost-refs-<commit>.pprof.
func measureRefsCostReads(t *testing.T, timing *readCostStatements, s *readCostServer, commit, path string) refsCostReading {
	t.Helper()
	var reading refsCostReading
	var refusal string
	take := func() (time.Duration, bool) {
		t.Helper()
		answer, err := s.send(http.MethodGet, path, readCostReader, nil)
		switch {
		case err != nil:
			t.Fatalf("%s: build %s: GET %s: %v", refsCostCheck, commit, path, err)
		case answer.status == http.StatusOK:
			if reading.answer == nil {
				reading.answer = answer.body
			}
		case answer.status == http.StatusServiceUnavailable:
			if refusal == "" {
				refusal = answer.String()
			}
			return answer.took, true
		default:
			t.Fatalf("%s: build %s: GET %s answered %s, want 200 or 503", refsCostCheck, commit, path, answer)
		}
		return answer.took, false
	}
	var warmUp time.Duration
	for range refsCostWarmUps {
		took, _ := take()
		warmUp = max(warmUp, took)
	}
	var profile *readCostProfile
	reading.runs = make([]time.Duration, 0, readCostRuns)
	reading.database, reading.idle = timing.measure(t, readCostRuns, func() {
		if s.pprof != "" {
			profile = startReadCostProfile(t, s, "refs-"+commit[:12], warmUp)
		}
		for range readCostRuns {
			took, unavailable := take()
			reading.runs = append(reading.runs, took)
			if unavailable {
				reading.unavailable++
			}
		}
		if profile != nil {
			profile.ended = time.Now()
		}
	})
	reading.p50, reading.p90, reading.p99 = refsCostPercentiles(reading.runs)
	t.Logf("%s: build %s: GET %s: p50 %.1f ms, p90 %.1f ms, p99 %.1f ms over %d readings after %d uncounted, %d of them answered 503%s",
		refsCostCheck, commit, path, msOf(reading.p50), msOf(reading.p90), msOf(reading.p99), readCostRuns, refsCostWarmUps,
		reading.unavailable, readCostDatabaseLine(reading.database, reading.idle))
	if refusal != "" {
		t.Logf("%s: build %s: the first answer of 503 was %s", refsCostCheck, commit, refusal)
	}
	t.Logf("%s: build %s: every reading in order, in ms: %v", refsCostCheck, commit, readCostMilliseconds(reading.runs))
	if profile != nil {
		profile.read(t, s)
	}
	return reading
}

// ───── the statements: captured, explained and replayed ───────────────────

// refsCostStatement is one statement the route sent the database for the read, as the plan proxy
// kept it: what it is (label), out or in for the out and the in statement of refs.Closure, whether
// its plans are printed (explained: a statement of refs.Closure, or every select of a build that
// tags none), and its replay - each execution's time, the rows its first execution answered, its
// percentiles, or why it was not replayed.
type refsCostStatement struct {
	label, role   string
	explained     bool
	statement     movedCostStatement
	runs          []time.Duration
	rows          int
	p50, p90, p99 time.Duration
	err           error
}

// driveRefsCost drives path on s, a build booted through proxy, until it answers 200, at most
// refsCostDriveTries times, with proxy keeping every statement s sends meanwhile, and answers the
// body of the answer of 200, nil where none answered 200. A read the bound cancels sends no
// statement after the one cancelled, so the statements kept are a whole read's only once one read
// answered 200.
func driveRefsCost(t *testing.T, proxy *movedCostProxy, s *readCostServer, commit, path string) []byte {
	t.Helper()
	proxy.drive(refsCostDriven)
	defer proxy.drive("")
	unavailable := 0
	for range refsCostDriveTries {
		answer, err := s.send(http.MethodGet, path, readCostReader, nil)
		switch {
		case err != nil:
			t.Fatalf("%s: build %s: GET %s through the plan proxy: %v", refsCostCheck, commit, path, err)
		case answer.status == http.StatusOK:
			t.Logf("%s: build %s: GET %s through the plan proxy answered 200 after %d answers of 503, so the statements kept are every "+
				"statement of a whole read", refsCostCheck, commit, path, unavailable)
			return answer.body
		case answer.status == http.StatusServiceUnavailable:
			unavailable++
		default:
			t.Fatalf("%s: build %s: GET %s through the plan proxy answered %s, want 200 or 503", refsCostCheck, commit, path, answer)
		}
	}
	t.Logf("%s: build %s: GET %s through the plan proxy answered 503 to each of %d reads, so the statements kept stop where the bound "+
		"cancelled the read, and a loader of resolveNodes no read reached is neither explained nor replayed", refsCostCheck, commit, path, refsCostDriveTries)
	return nil
}

// refsCostSelect reports whether sql, past its leading comments, is a select or a with, which the
// replay may run; a with that writes is refused by the replay's read-only transaction.
func refsCostSelect(sql string) bool {
	text := strings.TrimSpace(sql)
	for strings.HasPrefix(text, "/*") {
		end := strings.Index(text, "*/")
		if end < 0 {
			return false
		}
		text = strings.TrimSpace(text[end+2:])
	}
	lower := strings.ToLower(text)
	return strings.HasPrefix(lower, "select") || strings.HasPrefix(lower, "with")
}

// refsCostHead is the first readCostStatementText characters of sql, its whitespace collapsed.
func refsCostHead(sql string) string {
	head := []rune(strings.Join(strings.Fields(sql), " "))
	return string(head[:min(len(head), readCostStatementText)])
}

// refsCostStatements is the selects the route sent for the read through proxy, in the order it
// first sent each, less the server's background work (movedCostProxy.statements) and the budget's
// statement_timeout before each statement of the read (setStatementBudget). The statements of
// refs.Closure, which start with refs.ClosureTag, are the out statement, the in statement and then
// each loader of resolveNodes, in the order Closure sends them; at a build that tags none, every
// select is numbered and explained.
func refsCostStatements(t *testing.T, proxy *movedCostProxy, commit string) []refsCostStatement {
	t.Helper()
	sent, background := proxy.statements(refsCostDriven)
	if len(background) > 0 {
		heads := make([]string, len(background))
		for index, statement := range background {
			heads[index] = fmt.Sprintf("%q", refsCostHead(statement.sql))
		}
		t.Logf("%s: build %s: %d statements the read sent are the server's background work too, sent while no read was driven, and "+
			"are neither explained nor replayed: %s", refsCostCheck, commit, len(background), strings.Join(heads, ", "))
	}
	tagged := slices.ContainsFunc(sent, func(statement movedCostStatement) bool { return strings.HasPrefix(statement.sql, refs.ClosureTag) })
	if !tagged {
		t.Logf("%s: build %s starts no statement with refs.ClosureTag, so every select the read sent is explained and replayed, "+
			"numbered in the order it was sent", refsCostCheck, commit)
	}
	var statements []refsCostStatement
	closure, other := 0, 0
	for _, statement := range sent {
		if !refsCostSelect(statement.sql) || strings.Contains(statement.sql, "set_config('statement_timeout'") {
			continue
		}
		kept := refsCostStatement{statement: statement}
		switch {
		case tagged && strings.HasPrefix(statement.sql, refs.ClosureTag):
			kept.explained = true
			switch closure {
			case 0:
				kept.label, kept.role = "out statement (linksOutQuery)", "out"
			case 1:
				kept.label, kept.role = "in statement (linksInQuery)", "in"
			default:
				kept.label = fmt.Sprintf("loader %d of resolveNodes", closure-1)
			}
			closure++
		case tagged:
			other++
			kept.label = fmt.Sprintf("statement %d of the route outside refs.Closure", other)
		default:
			other++
			kept.label, kept.explained = fmt.Sprintf("statement %d of the route", other), true
		}
		statements = append(statements, kept)
	}
	return statements
}

// explainRefsCost prints the two plans of each explained statement, run over conn with the
// parameters the build bound (explainMovedCost: EXPLAIN (ANALYZE, BUFFERS) EXECUTE under a custom
// and a generic plan, JIT off): first a line of the statement's label and each plan's execution
// time, then its parameters, its text and the two plans. Nothing is bound on a plan.
func explainRefsCost(t *testing.T, conn *pgx.Conn, commit, path string, statements []refsCostStatement) {
	t.Helper()
	for _, statement := range statements {
		if !statement.explained {
			continue
		}
		plans, err := explainMovedCost(context.Background(), conn, statement.statement)
		if err != nil {
			t.Errorf("%s: build %s: explain the %s of GET %s: %v\n%s", refsCostCheck, commit, statement.label, path, err, statement.statement.sql)
			continue
		}
		times := make([]string, len(plans))
		shown := make([]string, len(plans))
		for mode, plan := range plans {
			took := "no execution time"
			if match := movedCostExecutionTime.FindStringSubmatch(plan); match != nil {
				took = match[1]
			}
			times[mode] = movedCostPlanModes[mode].plan + " " + took
			shown[mode] = fmt.Sprintf("%s plan (plan_cache_mode = %s):\n%s", movedCostPlanModes[mode].plan, movedCostPlanModes[mode].mode, plan)
		}
		t.Logf("%s: build %s: plans of the %s: %s\nthe %s of GET %s, bound %s:\n%s\n%s", refsCostCheck, commit, statement.label,
			strings.Join(times, ", "), statement.label, path, movedCostParameters(statement.statement), statement.statement.sql, strings.Join(shown, "\n"))
	}
}

// replayRefsCost replays each statement over conn (replayRefsCostStatement) and prints its
// percentiles, the time of its first execution, the rows it answered and the head of its text. A
// statement of refs.Closure that cannot be replayed fails the test; any other is said and passed.
func replayRefsCost(t *testing.T, conn *pgx.Conn, commit string, statements []refsCostStatement) {
	t.Helper()
	for index := range statements {
		statement := &statements[index]
		statement.runs, statement.rows, statement.err = replayRefsCostStatement(context.Background(), conn, statement.statement)
		if statement.err != nil {
			report := t.Logf
			if statement.explained {
				report = t.Errorf
			}
			report("%s: build %s: the %s was not replayed: %v: %s", refsCostCheck, commit, statement.label, statement.err, refsCostHead(statement.statement.sql))
			continue
		}
		statement.p50, statement.p90, statement.p99 = refsCostPercentiles(statement.runs)
		t.Logf("%s: build %s: the %s, replayed %d times: p50 %.3f ms, p90 %.3f ms, p99 %.3f ms, the first %.3f ms; %d rows: %s",
			refsCostCheck, commit, statement.label, len(statement.runs), msOf(statement.p50), msOf(statement.p90), msOf(statement.p99),
			msOf(statement.runs[0]), statement.rows, refsCostHead(statement.statement.sql))
	}
}

// replayRefsCostStatement prepares statement over conn as the build's pgx prepared it, with the
// parameter types it sent, and executes it readCostRuns times in a read-only transaction rolled
// back after, with the values and formats the build bound, plan_cache_mode at its default, so
// Postgres plans it as it plans the build's own prepared statement over its executions. Each
// execution is timed from its send to its last row; it answers those times and the rows the first
// execution answered. The database's JIT is off (readCostJITOff), as the route's transaction sets
// it.
func replayRefsCostStatement(ctx context.Context, conn *pgx.Conn, statement movedCostStatement) (runs []time.Duration, rows int, err error) {
	pg := conn.PgConn()
	if _, err := pg.Prepare(ctx, refsCostReplayStatement, statement.sql, statement.oids); err != nil {
		return nil, 0, fmt.Errorf("prepare: %w", err)
	}
	defer func() {
		if closed := pg.Deallocate(ctx, refsCostReplayStatement); closed != nil && err == nil {
			err = fmt.Errorf("deallocate: %w", closed)
		}
	}()
	if _, err := pg.Exec(ctx, "begin transaction read only").ReadAll(); err != nil {
		return nil, 0, fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if _, closed := pg.Exec(ctx, "rollback").ReadAll(); closed != nil && err == nil {
			err = fmt.Errorf("rollback: %w", closed)
		}
	}()
	runs = make([]time.Duration, 0, readCostRuns)
	for execution := range readCostRuns {
		started := time.Now()
		result := pg.ExecPrepared(ctx, refsCostReplayStatement, statement.values, statement.formats, nil).Read()
		took := time.Since(started)
		if result.Err != nil {
			return nil, 0, fmt.Errorf("execution %d: %w", execution+1, result.Err)
		}
		if execution == 0 {
			rows = len(result.Rows)
		}
		runs = append(runs, took)
	}
	return runs, rows, nil
}

// holdRefsCostRows holds links and linked_from to the rows the out and the in statement's replays
// answered, where no answer of the route was 200: those are the nodes the route answers before
// resolveNodes names them, every one of which the seed writes for the reader to see. Members are
// read from no statement and held to nothing.
func holdRefsCostRows(t *testing.T, side string, statements []refsCostStatement) {
	t.Helper()
	t.Logf("%s: %s: no read of the route answered 200, so links and linked_from are held to the rows of the out and the in statement's "+
		"replays, and members to nothing", refsCostCheck, side)
	for _, held := range []struct {
		role, what string
		want       int
	}{{"out", "rows of the out statement, the links the route would answer", refsCostLinks},
		{"in", "rows of the in statement, the linked_from the route would answer", refsCostLinkedFrom}} {
		index := slices.IndexFunc(statements, func(statement refsCostStatement) bool { return statement.role == held.role && statement.err == nil })
		if index < 0 {
			t.Errorf("%s: %s: the %s statement was not replayed, so it holds nothing of the shape", refsCostCheck, side, held.role)
			continue
		}
		holdRefsCostCount(t, side, held.what, statements[index].rows, held.want)
	}
}

// ───── the run ────────────────────────────────────────────────────────────

// refsCostResult is one build's measure, which the run's last lines set side by side: its
// readings of the route, nil until they are taken, and its statements with their replays.
type refsCostResult struct {
	commit     string
	reading    *refsCostReading
	statements []refsCostStatement
}

// refsCostProfiled reads REFS_COST_PPROF before anything is built or read. Unset, it answers false.
// Set to 1, it answers true: each build's server serves its profiler (startReadCostServerProfiled)
// and its readings take one CPU profile of it. Set to anything else, it fails, naming the value it
// takes.
func refsCostProfiled(t *testing.T) bool {
	t.Helper()
	switch named := strings.TrimSpace(os.Getenv(refsCostPprofVariable)); named {
	case "":
		return false
	case "1":
		t.Logf("%s: %s=1 takes a CPU profile of each build's server over its readings, written to $TMPDIR/read-cost-refs-<commit>.pprof",
			refsCostCheck, refsCostPprofVariable)
		return true
	default:
		t.Fatalf("%s=%q: it takes 1, which profiles each build's server over its readings, or is left unset", refsCostPprofVariable, named)
		return false
	}
}

// measureRefsCost measures the build of commit, filling result as it goes. It builds the commit
// and boots it over a database of its own, seeds K through it, waits until it owes nothing of what
// its writes left, vacuums, and holds K's own content to the shape; reads the route
// (measureRefsCostReads), holds the first answer of 200 to the shape and prints the statements
// behind the readings (logReadCostStatements); then stops it, boots it again through the plan
// proxy, drives the read once to its end where the bound lets it (driveRefsCost), stops it, and
// prints the plans of the read's statements of refs.Closure (explainRefsCost) and each select's
// replay (replayRefsCost).
func measureRefsCost(t *testing.T, client *http.Client, listener, commit string, keep, profiled bool, result *refsCostResult) {
	began := time.Now()
	t.Cleanup(func() {
		t.Logf("%s: build %s took %s of wall time", refsCostCheck, commit, time.Since(began).Round(time.Second))
	})
	logRefsCostBound(t, commit)
	backfill := refsCostSourceHolds(t, commit, "dispatch: boot backfill done", "packages/envoy/cmd/dispatch")
	databaseURL := readCostDatabase(t, keep)
	binary := testbaseline.Build(t, commit, refsCostShasVariable)
	conn := readCostConnect(t, databaseURL)
	holdReadCostJITOff(t, refsCostCheck, conn)
	timing := newReadCostStatements(t, refsCostCheck, databaseURL)
	name := "the build of " + commit
	side := "build " + commit

	server := startReadCostServerProfiled(t, client, name, binary, commit, databaseURL, listener, profiled)
	owed, args := refsCostOwedCounts(t, conn, commit)
	seed := seedRefsCost(t, server)
	waitRefsCostOwed(t, conn, server, owed, args, backfill)
	readCostVacuum(t, conn, "references read of "+commit)
	holdRefsCostContent(t, server, seed)

	path := "/api/v1/issues/" + seed.issue + "/references"
	reading := measureRefsCostReads(t, timing, server, commit, path)
	result.reading = &reading
	if reading.answer != nil {
		holdRefsCostAnswer(t, side, "the first answer of 200 to GET "+path, reading.answer)
	}
	logReadCostStatements(t, refsCostCheck, "GET "+path, side, reading.database)
	server.stop(t)

	proxy, throughProxy := startMovedCostProxy(t, databaseURL)
	captured := startReadCostServer(t, client, name+" through the plan proxy", binary, commit, throughProxy, listener)
	waitRefsCostOwed(t, conn, captured, owed, args, backfill)
	driven := driveRefsCost(t, proxy, captured, commit, path)
	if reading.answer == nil && driven != nil {
		holdRefsCostAnswer(t, side, "the answer of 200 to GET "+path+" through the plan proxy", driven)
	}
	// The proxy tells the read's statements from the server's background work by the texts the
	// server sends while no read is driven, as c-moved-read-cost does (movedCostPlanIdle).
	time.Sleep(movedCostPlanIdle)
	captured.stop(t)
	result.statements = refsCostStatements(t, proxy, commit)
	explainRefsCost(t, conn, commit, path, result.statements)
	replayRefsCost(t, conn, commit, result.statements)
	if reading.answer == nil && driven == nil {
		holdRefsCostRows(t, side, result.statements)
	}
}

// What each statement of GET /api/v1/issues/{key}/references costs on an issue shaped like
// production's INFRA-1098, at each build REFS_COST_SHAS names (this file's head says why).
//
// Each build is a subtest named by its commit, in the order named, on a database of its own that
// is dropped when the subtest ends (measureRefsCost). Its lines start "refs-cost: build <commit>":
// the bound it declares, K's own content and the answer held to the shape, the p50, p90 and p99
// of its readCostRuns readings with how many answered 503, the statements behind those readings
// from pg_stat_statements with their calls, time and shared blocks hit and read per reading, the
// EXPLAIN (ANALYZE, BUFFERS) plans of the out statement, the in statement and each loader of
// resolveNodes, and each select of the read replayed readCostRuns times. A statement the bound
// cancels completes in no reading, so pg_stat_statements holds none of its time, and its replay is
// where its time is read. The last lines set the builds side by side.
//
// A count of the seed more than refsCostShapeSlack from the shape fails the test without stopping
// it, so the build is still measured. The test skips, naming the variable, where REFS_COST_SHAS is
// unset, and fails before it builds anything where it names anything but 40-character commits this
// checkout holds, or where READ_COST_KEEP_DATABASE or REFS_COST_PPROF holds a value they do not
// take.
func TestIssueReferencesReadCost(t *testing.T) {
	named := strings.TrimSpace(os.Getenv(refsCostShasVariable))
	if named == "" {
		t.Skipf("%s is unset: it names the comma-separated 40-character commits whose GET /api/v1/issues/{key}/references this "+
			"diagnosis measures, each on a database of its own", refsCostShasVariable)
	}
	began := time.Now()
	t.Cleanup(func() {
		t.Logf("%s: TestIssueReferencesReadCost took %s of wall time", refsCostCheck, time.Since(began).Round(time.Second))
	})
	var commits []string
	for commit := range strings.SplitSeq(named, ",") {
		commit = strings.TrimSpace(commit)
		if !readCostFullCommit.MatchString(commit) {
			t.Fatalf("%s=%q names %q, which is no 40-character commit: the diagnosis builds each commit it names, whole", refsCostShasVariable, named, commit)
		}
		if err := exec.Command("git", "cat-file", "-e", commit+"^{commit}").Run(); err != nil {
			t.Fatalf("%s names %s, which this checkout does not hold (git cat-file: %v); a shallow clone lacks it", refsCostShasVariable, commit, err)
		}
		commits = append(commits, commit)
	}
	keep := readCostKeepDatabase(t)
	profiled := refsCostProfiled(t)
	client := readCostClient()
	listener := readCostListener(t)
	results := make([]*refsCostResult, 0, len(commits))
	for _, commit := range commits {
		result := &refsCostResult{commit: commit}
		results = append(results, result)
		t.Run(commit, func(t *testing.T) { measureRefsCost(t, client, listener, commit, keep, profiled, result) })
	}

	t.Logf("%s: the builds side by side: commit, the route's p50, p90 and p99 in ms over %d readings, its answers of 503, its database "+
		"time per reading in ms, then each statement of refs.Closure replayed %d times, its p50/p90 in ms", refsCostCheck, readCostRuns, readCostRuns)
	for _, result := range results {
		if result.reading == nil {
			t.Logf("%s: %s\tnot measured: its subtest ended before its readings", refsCostCheck, result.commit)
			continue
		}
		reading := result.reading
		cells := []string{result.commit, fmt.Sprintf("%.1f", msOf(reading.p50)), fmt.Sprintf("%.1f", msOf(reading.p90)),
			fmt.Sprintf("%.1f", msOf(reading.p99)), fmt.Sprintf("%d of %d", reading.unavailable, readCostRuns), reading.database.readCell()}
		for _, statement := range result.statements {
			if statement.explained && statement.err == nil {
				cells = append(cells, fmt.Sprintf("%s %.3f/%.3f", statement.label, msOf(statement.p50), msOf(statement.p90)))
			}
		}
		t.Logf("%s: %s", refsCostCheck, strings.Join(cells, "\t"))
	}
}
