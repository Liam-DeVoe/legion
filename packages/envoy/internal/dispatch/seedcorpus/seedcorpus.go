// Package seedcorpus is the seeded corpus of Dispatch's graph model that every test lane shares:
// the browser file and the shell drivers through cmd/dispatch-corpus, and the Go tests by calling
// Seed and Check directly. It is test support. No deployment builds it, it declares no HTTP route
// and it mounts nothing on any server: it writes through the API's own handlers, which api.Register
// places on a mux of this process that no listener serves (apiServer), through the store's own
// writer of a role's facts (writeRoles), and, for the one shape no writer of the tree makes, by
// one statement of its own (nestReplies). Decision 5 of dispatch://INFRA-1098's plan is why: a
// reply nested under another reply, as the walks over comments.reply_to read one, cannot be
// written through the API, whose comment write climbs to the thread head before it stores (api's
// threadHeadOf). It publishes no event and marks none published, its own or any other writer's,
// and it refuses a database holding events it did not write unless its caller says the database
// is a test copy (Options.TestCopy, `seed --test-copy`).
//
// # The record
//
// This corpus stands on dispatch://INFRA-1253's head 2afc1550 (legion/INFRA-1253), which holds
// dispatch://INFRA-1150 at 0cacc092 and an older dispatch://INFRA-1950. For each bullet of the
// third fixture of the Verification document (version 120) and each row two of its checks name,
// the kind it seeds and the head that covers it. "This head" is 2afc1550.
//
//  1. An answered ask with options and an answer: an `ask` block of the spec, settled into its asks
//     row and ask node, answered by alice through the answer route. This head.
//  2. A second ask still open: a second `ask` block, left open. This head.
//  3. An agent's decision with an uncertainty level and its reason: a `decision` block written by
//     the agent's session, with `uncertainty` and `uncertainty_reason` (block schema 5). This head.
//  4. A reading under that decision: a `reading` block inside the decision, its part_of the
//     decision's block node. This head.
//  5. Two requirements, one shaped by the answered ask: `requirement` blocks r-one and r-two, and
//     the ask's `shaped` pointer at r-one (a `shaped` tuple). This head.
//  6. A check that serves three requirements, two under one criterion and the third under another:
//     `check` k-three serving r-one, r-two and r-three, r-one and r-two serving criterion c-want and
//     r-three serving c-other (`serves` tuples; a requirement's serves is block schema 6). This head.
//  7. A check with a passing result recorded against its current words: k-pass and a pass recorded
//     through the results route (a `result` node). This head.
//  8. A check with a failing result: k-fail and a fail recorded. This head.
//  9. A check whose only result was recorded against earlier words: k-stale, a pass recorded, then
//     its words edited. This head.
//  10. A check that serves one requirement and holds no result: k-bare. This head.
//  11. A declaration of what a person wants, which the two requirements answer: `criterion` c-want,
//     served by r-one and r-two. This head.
//  12. Two plan_task nodes, one serving two requirements and one serving none, a size on one:
//     `plan_task` t-serves (r-one, r-two, size 2.5) and t-none in plan.md. This head.
//  13. A comment thread whose reply answers another reply: alice's comment, bob's reply to it and
//     alice's reply to bob's, each written through the comment route, which stores the comment a
//     reply names in comments.answers. This head; dispatch://INFRA-1152 adds the `answers` tuple
//     beside the column (883ea8b86b), which the same writes then append.
//  14. A second thread whose replies form a cycle: a comment and two replies written through the
//     route, whose comments.reply_to the loader then points at each other (nestReplies). This head.
//  15. A person who is the recipient of a message, a broadcast, a comment, an ask and a reply:
//     dispatch://INFRA-1152's delivery to a person (6fe7f108d3, inboxwrite.Record; 8dccf009d1, an
//     ask's create and a session's reply deliver to the person; bd993067ba, a mention delivers to
//     the person it names). No writer here records a person as the recipient of what reaches them,
//     so the bullet is the separate part seedINFRA1152 (infra1152.go), absent from this commit and
//     from the default seed: a part written here would write rows that hold none of the relation.
//  16. An agent with one session, one role it holds, its page, a message under that page and a
//     reply under that message: the session's node (its comment's authored_by), the role's node,
//     its page and the page's part_of, the role's held_by and the session's part_of the role, the
//     facts the outbox's record of a batch routed to the role states, written through the same
//     store writer by writeRoles, with the model issue routed to the role. This head. The
//     message under the page and the reply under that message are dispatch://INFRA-1152's (a
//     message on no issue hangs from its agent's page, d37dd05857): seedINFRA1152, absent.
//  17. A second issue this issue waits on, and a plan_task of this issue waiting on one of that
//     issue: the separate part seedINFRA1253 (infra1253.go), absent from this commit and from the
//     default seed. It waits on INFRA-1253 letting a needs pointer reach a plan_task of another
//     issue (pmdoc/pointers.go documentReach/itemReach): at this head a needs names a plan task of
//     an issue above the writing one alone, and an issue may not wait on an issue above it (the
//     create answers 409 DEPENDENCY_CYCLE), so no one second issue can be both.
//  18. A lens of kind pre-mortem with two risks, one pointing at a check: the lens route, the risk
//     route twice, one risk with `prevented_by` at k-pass. This head.
//  19. A lens of kind inversion that the pre-mortem lens triggered: a lens with `triggered_by` the
//     pre-mortem. This head.
//  20. An interaction node a second credential wrote, and one node held at `author` visibility:
//     bob's run of the command bar (the going route), an interaction node, which the store always
//     holds at visibility author for the credential that asked it. This head.
//  21. A criterion the person wrote in a comment, a second in an ask, a third in a message:
//     `criterion` blocks whose `parent` names alice's comment, her ask and her message (block
//     schema 6). This head.
//  22. A node held at `author` visibility whose parent, backlink and two neighbours are `project`:
//     alice's first-principles lens reserved for her, on requirement r-three between two project
//     lenses on it, and a project second-order lens on the issue triggered by it. This head.
//  23. An acyclic reply chain one node longer than the walk's depth bound: 34 comments, 33 reply_to
//     hops, one past the outbox's maxCommentThreadDepth (32), written through the route and nested
//     by comments.reply_to (nestReplies). alice writes its last link, so it leaves her no unread
//     row beside the two c-routes-deleted names. This head.
//  24. Two identities that must stay apart, one login in two letter cases and a role whose holder
//     changed: dana writes as "Dana" and as "dana", one person node; the role corpus-handover, to
//     which an issue of its own is routed, is held by one session and then by another, two claims
//     of writeRoles, the second retracting the first. This head.
//
// The rows two checks name:
//
//   - c-agent-page's message posted on an issue to the agent whose delivery attempt failed: an
//     issue message targeted at the agent's session, whose one delivery attempt the API records
//     failed ("envoy listener: ENVOY_URL is not configured"), as no Envoy listener answers this
//     process. This head (the messages row and its message_deliveries row); dispatch://INFRA-1152
//     adds the attempt's delivery_attempt node (0a4af24935), which the same write then makes.
//   - c-routes-deleted's two unread thread rows of alice: one the Inbox page draws, the thread of
//     her comment that bob answered; and one it does not draw, the thread of an open ask on an open
//     issue assigned to her, in which she wrote and bob wrote after. This head (user_threads rows).
//
// dispatch://INFRA-1149's head (6e4051ea) holds 22 commits this head does not, each a fix of the
// erase or a commit of main: no bullet needs a kind or a relation only it adds.
//
// Beside the seeded issue the corpus holds the two copied issues, replayed from the captures
// acme-1115.json and acme-1042.json by the agent session that wrote them: each spec, each section
// of decisions.md as a `decision` block of the spec's `## Decisions` part, as the API asks a
// decision to be written (api's decisionsAreNodes), and each ask as an asks row, answered by the
// capture's answerer, resolved or left open as the capture holds it. An approval opens no ask of a
// spec that stands approved at its current version, and each captured approval after an approved
// one was asked of a later version, so before such an approval the replay appends one paragraph to
// the copied spec, "A version of the replay, for the approval the capture holds as ask <id>.", the
// one text of a copied spec that is not the capture's. And the pre-mortem of
// acme-1098-premortem.json, converted by the API's own conversion (api.ConvertPremortem through its
// route) into one lens of kind pre-mortem with its eleven risks directly under it, none under
// another (decision 6).
package seedcorpus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// DefaultProject is the project the drivers name (PROJECT=CORE), which `bun run dispatch:local`
// seeds as well; the loader writes into it whether or not it exists already.
const DefaultProject = "CORE"

// The people and agents the corpus writes as. alice is the person every check signs in as, bob
// the second credential, and dana the login written in two letter cases. The sessions and roles
// are the corpus's own.
const (
	alice = "alice"
	bob   = "bob"
	dana  = "dana"
	// danaCased is dana's login as one of her writes presents it.
	danaCased = "Dana"

	agentSession    = "ses_corpus_agent"
	agentRole       = "corpus-agent"
	handoverRole    = "corpus-handover"
	handoverFirst   = "ses_corpus_handover_first"
	handoverSecond  = "ses_corpus_handover_second"
	recorderSession = "ses_corpus_recorder"
)

// Options are one run of the seed: the project it writes into, the directory holding the three
// captures (FindFixtures), and whether the database is a test copy, which the caller says of a
// database that holds events already (ErrForeignEvents).
type Options struct {
	Project  string
	Fixtures string
	TestCopy bool
}

// ErrForeignEvents refuses a database that holds events the loader did not write, which a caller
// has not said is a test copy (Options.TestCopy): such a database may be somebody's own, and the
// corpus would write its issues, comments and roles among that somebody's work.
var ErrForeignEvents = errors.New("the database holds events the loader did not write, so it is neither empty nor one its caller says is a test copy")

// Corpus is what a seed wrote that a driver reads by name: each issue's key, each item's id and
// each node's address, in the order written.
type Corpus struct {
	names []string
	value map[string]string
}

// set records one name.
func (c *Corpus) set(name, value string) {
	if c.value == nil {
		c.value = map[string]string{}
	}
	if _, held := c.value[name]; !held {
		c.names = append(c.names, name)
	}
	c.value[name] = value
}

// Get is the value written under name, "" for none.
func (c Corpus) Get(name string) string { return c.value[name] }

// Print writes each name as one `key=value` line, in the order written.
func (c Corpus) Print(out io.Writer) {
	for _, name := range c.names {
		fmt.Fprintf(out, "%s=%s\n", name, c.value[name])
	}
}

// Seed writes the corpus into the project options names of database, which is migrated and holds
// the block schema the store does. It refuses a database that holds any event before it writes,
// naming how many, unless options say it is a test copy (ErrForeignEvents), and a project that
// holds the corpus already, since a second copy would make every count the self-check reads twice
// what it wrote. It publishes no event and marks none published.
func Seed(ctx context.Context, database *store.Store, options Options) (Corpus, error) {
	if options.Project == "" {
		options.Project = DefaultProject
	}
	captures, err := readCaptures(options.Fixtures)
	if err != nil {
		return Corpus{}, err
	}
	var foreign int
	if err := database.Pool.QueryRow(ctx, `select count(*) from events`).Scan(&foreign); err != nil {
		return Corpus{}, fmt.Errorf("count the events the database holds: %w", err)
	}
	if foreign > 0 && !options.TestCopy {
		return Corpus{}, fmt.Errorf("%w: it holds %d events; seed an empty database, or say this one is a test copy (--test-copy)", ErrForeignEvents, foreign)
	}
	if held, err := corpusHeld(ctx, database.Pool, options.Project); err != nil {
		return Corpus{}, err
	} else if held != "" {
		return Corpus{}, fmt.Errorf("project %s holds the corpus already (%s): seed an empty database", options.Project, held)
	}
	people := []string{alice, bob, dana}
	for _, capture := range captures.copied {
		people = append(people, capture.answerers()...)
	}
	server, err := startAPI(ctx, database, people)
	if err != nil {
		return Corpus{}, err
	}
	s := &seeder{api: server, database: database, project: options.Project}
	written := s.write(ctx, captures)
	closed := server.close(ctx)
	if err := errors.Join(written, closed); err != nil {
		return Corpus{}, err
	}
	return s.corpus, nil
}

// seeder is one run of Seed: the in-process API, the database, the project and what it wrote.
type seeder struct {
	api      *apiServer
	database *store.Store
	project  string
	corpus   Corpus
}

// write writes every part of the default corpus in turn.
func (s *seeder) write(ctx context.Context, captures captures) error {
	if err := s.ensureProject(ctx); err != nil {
		return err
	}
	for _, capture := range captures.copied {
		if err := s.replayCopiedIssue(ctx, capture); err != nil {
			return err
		}
	}
	if err := s.replayPremortem(ctx, captures.premortem); err != nil {
		return err
	}
	return s.writeModelIssue(ctx)
}

// ensureProject creates the project as alice, its default assignee, unless it exists already.
func (s *seeder) ensureProject(ctx context.Context) error {
	return s.api.call(ctx, asPerson(alice), "POST", "/api/v1/projects",
		map[string]any{"key": s.project, "name": "Seeded corpus"}, nil, 201, 409)
}

// FindFixtures finds the directory of the captures, packages/dispatch/e2e/fixtures, in from or a
// directory above it: a driver runs the command from packages/dispatch, and a Go test from its own
// package's directory.
func FindFixtures(from string) (string, error) {
	directory, err := filepath.Abs(from)
	if err != nil {
		return "", err
	}
	for {
		for _, candidate := range []string{
			filepath.Join(directory, "e2e", "fixtures"),
			filepath.Join(directory, "packages", "dispatch", "e2e", "fixtures"),
		} {
			if _, err := os.Stat(filepath.Join(candidate, copiedFiles[0])); err == nil && strings.Contains(candidate, filepath.Join("dispatch", "e2e")) {
				return candidate, nil
			}
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", fmt.Errorf("no packages/dispatch/e2e/fixtures/%s in %s or above it: name the directory with --fixtures", copiedFiles[0], from)
		}
		directory = parent
	}
}
