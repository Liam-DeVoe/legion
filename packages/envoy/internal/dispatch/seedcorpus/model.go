package seedcorpus

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// The titles of the seeded issue and of the issue the handover role holds, which the self-check
// finds them by.
const (
	modelTitle    = "The seeded issue of the graph model's corpus"
	handoverTitle = "The issue the handover role holds"
)

// resultCommit is the commit every recorded result names. The project maps no repository, so the
// results route asks none whether it holds it and stores it as given.
const resultCommit = "1111111111111111111111111111111111111111"

// chainComments is how many comments the acyclic reply chain holds: the root and one reply under
// each comment before it, so its leaf stands 33 reply_to hops below the root. The outbox's walks
// over comments.reply_to (outbox's maxCommentThreadDepth) take at most 32 hops from the comment
// they start at, so from the leaf the walk reads 33 comments and stops one short of the root.
const chainComments = 34

// The words of the check whose only result was recorded against earlier words, before the edit
// and after it.
const (
	staleWords  = "The page draws a reason under each decision."
	staleEdited = "The page draws each decision's reason under it, in the decision's own words."
)

// issueRef, specRef and verificationRef are the dispatch:// addresses of an issue, of a block of
// its spec and of a block of its Verification document, as a block pointer names them.
func issueRef(key string) string       { return "dispatch://" + key }
func specRef(key, block string) string { return "dispatch://" + key + "/spec#b-" + block }
func verificationRef(key, block string) string {
	return "dispatch://" + key + "/artifact/verification-md#b-" + block
}

// itemRef is the dispatch:// address of an ask, a comment or a message of an issue.
func itemRef(key, kind, id string) string { return "dispatch://" + key + "/" + kind + "/" + id }

// created is the id a write answers at its top level.
type created struct {
	ID string `json:"id"`
}

// writeModelIssue writes the seeded issue and everything the third fixture's bullets hang on it,
// in the order their pointers need: the issue, the conversation the criteria name as their
// parents, the spec's blocks, the Verification document and the plan, the answer and the
// results, the comment threads, the lenses, the command bar's run, the agents, the named rows and
// the two identities. The record at the head of seedcorpus.go names each.
func (s *seeder) writeModelIssue(ctx context.Context) error {
	var issue model.Issue
	if err := s.api.call(ctx, asPerson(alice), http.MethodPost, "/api/v1/issues", map[string]any{
		"project": s.project, "title": modelTitle, "force": true,
		"spec": "## Summary\n\nThe seeded issue every lane of the graph model's checks reads.\n",
	}, &issue, http.StatusCreated); err != nil {
		return fmt.Errorf("create the seeded issue: %w", err)
	}
	key := issue.Key
	s.corpus.set("model.issue", key)
	s.corpus.set("model.spec", issue.PrimaryArtifactID)

	// The conversation alice writes the three criteria in (bullet 21).
	var said created
	if err := s.api.call(ctx, asPerson(alice), http.MethodPost, "/api/v1/issues/"+key+"/comments",
		map[string]any{"body": "I want every decision on one page, with who made it."}, &said); err != nil {
		return err
	}
	var asked model.Ask
	if err := s.api.call(ctx, asPerson(alice), http.MethodPost, "/api/v1/issues/"+key+"/asks",
		map[string]any{"question": "Can the page also say when each decision was made?"}, &asked); err != nil {
		return err
	}
	var messaged created
	if err := s.api.call(ctx, asPerson(alice), http.MethodPost, "/api/v1/issues/"+key+"/messages",
		map[string]any{"body": "I want an answered ask drawn as the person wrote it."}, &messaged); err != nil {
		return err
	}
	s.corpus.set("model.criterion_comment", said.ID)
	s.corpus.set("model.criterion_ask", asked.ID)
	s.corpus.set("model.criterion_message", messaged.ID)

	// The spec's blocks, written by the agent's session: the criteria, the requirements, the two
	// asks and the agent's decision with its reading (bullets 1 to 6, 11 and 21).
	spec := strings.Join([]string{
		":::criterion{#c-want}\nA person wants every decision of the issue on one page, with who made it.\n:::\n",
		":::criterion{#c-other}\nA person wants each decision's reason kept beside it.\n:::\n",
		":::criterion{#c-said parent=\"" + itemRef(key, "comment", said.ID) + "\"}\nEvery decision is on one page, with who made it.\n:::\n",
		":::criterion{#c-asked parent=\"" + itemRef(key, "ask", asked.ID) + "\"}\nThe page says when each decision was made.\n:::\n",
		":::criterion{#c-messaged parent=\"" + itemRef(key, "message", messaged.ID) + "\"}\nAn answered ask is drawn as the person wrote it.\n:::\n",
		":::requirement{#r-one serves=\"" + specRef(key, "c-want") + "\"}\nThe page lists every answered ask.\n:::\n",
		":::requirement{#r-two serves=\"" + specRef(key, "c-want") + "\"}\nThe page lists every decision an agent made.\n:::\n",
		":::requirement{#r-three serves=\"" + specRef(key, "c-other") + "\"}\nEach decision keeps its reason.\n:::\n",
		":::ask{#q-answered urgency=\"high\" multiple=\"false\" shaped=\"" + specRef(key, "r-one") + "\"}\nWhich page lists the answered asks?\n\n" +
			"- The issue page: one page holds every answer\n- A page of their own: the answers stand apart\n:::\n",
		":::ask{#q-open urgency=\"med\" multiple=\"false\"}\nWhich order do the rows take?\n\n" +
			"- Newest first: the last answer leads\n- Oldest first: the page reads as it happened\n:::\n",
		"## Decisions\n",
		"::::decision{#d-agent uncertainty=\"medium\" uncertainty_reason=\"No person has read the page yet, so the order of its rows may change.\"}\n" +
			"The rows are drawn from the tree read, newest first.\n\n" +
			":::reading{#g-agent}\nThe agent reads its decision as one row for each node the decisions view answers.\n:::\n::::\n",
	}, "\n")
	if err := s.api.call(ctx, asSession(agentSession), http.MethodPost, "/api/v1/issues/"+key+"/artifacts/spec/edits",
		map[string]any{"ops": []map[string]any{{"op": "insert", "after": "end", "markdown": spec}}}, nil); err != nil {
		return fmt.Errorf("write the seeded spec's blocks: %w", err)
	}

	// The Verification document and the plan (bullets 6 to 10 and 12).
	check := func(id, serves, words string) string {
		return ":::check{#" + id + " serves=\"" + serves + "\" surface=\"cli\" run=\"go test ./...\"}\n" + words + "\n:::\n"
	}
	verification := strings.Join([]string{
		check("k-three", specRef(key, "r-one")+" "+specRef(key, "r-two")+" "+specRef(key, "r-three"), "The page draws every answered ask, every decision and each reason."),
		check("k-pass", specRef(key, "r-one"), "The page draws every answered ask."),
		check("k-fail", specRef(key, "r-two"), "The page draws every decision an agent made."),
		check("k-stale", specRef(key, "r-three"), staleWords),
		check("k-bare", specRef(key, "r-one"), "The page draws an answered ask in the person's words."),
	}, "\n")
	if err := s.upload(ctx, asSession(agentSession), key, "verification.md", verification); err != nil {
		return err
	}
	plan := ":::plan_task{#t-serves serves=\"" + specRef(key, "r-one") + " " + specRef(key, "r-two") + "\" size=\"2.5\"}\nDraw the answered asks and the decisions on the page.\n:::\n\n" +
		":::plan_task{#t-none}\nRead the page with a person before it ships.\n:::\n"
	if err := s.upload(ctx, asSession(agentSession), key, "plan.md", plan); err != nil {
		return err
	}

	// The answered ask and the open one (bullets 1 and 2).
	asks, err := s.blockAsks(ctx, key, []string{"q-answered", "q-open"})
	if err != nil {
		return err
	}
	s.corpus.set("model.ask_answered", asks["q-answered"])
	s.corpus.set("model.ask_open", asks["q-open"])
	if err := s.api.call(ctx, asPerson(alice), http.MethodPost, "/api/v1/asks/"+asks["q-answered"]+"/answer", map[string]any{
		"selected": []string{"The issue page"}, "text": "One page, so nobody hunts for an answer.", "expected_edited_at": nil,
	}, nil); err != nil {
		return fmt.Errorf("answer q-answered: %w", err)
	}

	// The results (bullets 7 to 9).
	for _, result := range []struct{ check, verdict string }{{"k-pass", "pass"}, {"k-fail", "fail"}, {"k-stale", "pass"}} {
		if err := s.api.call(ctx, asSession(recorderSession), http.MethodPost, "/api/v1/issues/"+key+"/results", map[string]any{
			"check": result.check, "role": "tester", "verdict": result.verdict, "commit": resultCommit,
			"negative_control": "A page that drew a row from no node; the check read the stray row and failed.",
		}, nil); err != nil {
			return fmt.Errorf("record %s on %s: %w", result.verdict, result.check, err)
		}
	}
	if err := s.api.call(ctx, asSession(agentSession), http.MethodPost, "/api/v1/issues/"+key+"/artifacts/verification-md/edits",
		map[string]any{"ops": []map[string]any{{"op": "replace", "find": staleWords, "with": staleEdited}}}, nil); err != nil {
		return fmt.Errorf("edit the words of k-stale: %w", err)
	}

	if err := s.writeThreads(ctx, key, asks["q-open"]); err != nil {
		return err
	}
	if err := s.writeLenses(ctx, key); err != nil {
		return err
	}
	// Bob's run of the command bar (bullet 20).
	var run created
	if err := s.api.call(ctx, asPerson(bob), http.MethodPost, "/api/v1/going/requests",
		map[string]any{"words": "Which decisions of the seeded issue are still open?", "project": s.project}, &run); err != nil {
		return fmt.Errorf("ask the command bar as bob: %w", err)
	}
	s.corpus.set("model.interaction", run.ID)
	return s.writeAgents(ctx, key)
}

// upload writes name to the issue as a new document, or as its next version where the issue holds
// one of that name.
func (s *seeder) upload(ctx context.Context, who writer, key, name, markdown string) error {
	if err := s.api.call(ctx, who, http.MethodPost, "/api/v1/issues/"+key+"/artifacts",
		map[string]any{"name": name, "content": markdown}, nil, http.StatusCreated); err != nil {
		return fmt.Errorf("upload %s to %s: %w", name, key, err)
	}
	return nil
}

// comment writes one comment on the issue as who, replying to replyTo where it is not "", and
// answers its id.
func (s *seeder) comment(ctx context.Context, who writer, key, body, replyTo string) (string, error) {
	input := map[string]any{"body": body}
	if replyTo != "" {
		input["reply_to"] = replyTo
	}
	var written created
	if err := s.api.call(ctx, who, http.MethodPost, "/api/v1/issues/"+key+"/comments", input, &written); err != nil {
		return "", err
	}
	return written.ID, nil
}

// writeThreads writes the comment threads: the reply that answers a reply (bullet 13), the thread
// whose replies form a cycle (bullet 14), the chain one node longer than the walk reads (bullet
// 23), and alice's two unread thread rows (c-routes-deleted), one in the thread of the open ask.
func (s *seeder) writeThreads(ctx context.Context, key, openAsk string) error {
	root, err := s.comment(ctx, asPerson(alice), key, "Should the page fold the answered asks away?", "")
	if err != nil {
		return err
	}
	reply, err := s.comment(ctx, asPerson(bob), key, "Fold them once the person has read them.", root)
	if err != nil {
		return err
	}
	answer, err := s.comment(ctx, asPerson(alice), key, "Read by whom, the person who answered or anybody?", reply)
	if err != nil {
		return err
	}
	s.corpus.set("thread.root", root)
	s.corpus.set("thread.reply", reply)
	s.corpus.set("thread.reply_to_reply", answer)

	cycleRoot, err := s.comment(ctx, asPerson(alice), key, "The rows of this thread answer each other.", "")
	if err != nil {
		return err
	}
	first, err := s.comment(ctx, asPerson(bob), key, "This reply answers the one below it.", cycleRoot)
	if err != nil {
		return err
	}
	second, err := s.comment(ctx, asPerson(alice), key, "And this one answers the one above it.", first)
	if err != nil {
		return err
	}
	s.corpus.set("cycle.root", cycleRoot)
	s.corpus.set("cycle.first", first)
	s.corpus.set("cycle.second", second)

	chain := make([]string, 0, chainComments)
	for index := range chainComments {
		parent := ""
		if index > 0 {
			parent = chain[index-1]
		}
		// alice and bob take turns, alice writing the last link, so that the chain leaves alice no
		// unread thread row: her two unread rows are the two c-routes-deleted names.
		who := asPerson(alice)
		if (chainComments-1-index)%2 == 1 {
			who = asPerson(bob)
		}
		id, err := s.comment(ctx, who, key, fmt.Sprintf("Link %d of the long reply chain.", index), parent)
		if err != nil {
			return fmt.Errorf("write link %d of the reply chain: %w", index, err)
		}
		chain = append(chain, id)
	}
	s.corpus.set("chain.root", chain[0])
	s.corpus.set("chain.leaf", chain[len(chain)-1])

	// The nesting no route writes: each reply of the cycle under the other, and each link of the
	// chain under the link before it.
	nests := []nest{{comment: first, parent: second}, {comment: second, parent: first}}
	for index := 1; index < len(chain); index++ {
		nests = append(nests, nest{comment: chain[index], parent: chain[index-1]})
	}
	if err := s.nestReplies(ctx, key, nests); err != nil {
		return err
	}

	// alice's unread thread row the Inbox draws: her comment, which bob answered after her.
	drawn, err := s.comment(ctx, asPerson(alice), key, "Is the folded part open by default?", "")
	if err != nil {
		return err
	}
	if _, err := s.comment(ctx, asPerson(bob), key, "Closed, with a count beside it.", drawn); err != nil {
		return err
	}
	s.corpus.set("inbox.drawn_thread", drawn)
	// And the one it does not draw: the thread of the open ask on her own open issue, where she
	// wrote and bob wrote after her.
	for _, who := range []string{alice, bob} {
		if err := s.api.call(ctx, asPerson(who), http.MethodPost, "/api/v1/issues/"+key+"/comments",
			map[string]any{"body": "A word on the order, from " + who + ".", "ask_id": openAsk}, nil); err != nil {
			return fmt.Errorf("comment in the open ask's thread as %s: %w", who, err)
		}
	}
	s.corpus.set("inbox.undrawn_thread", openAsk)
	return nil
}

// writeLenses writes the lenses: the pre-mortem with its two risks, one prevented by k-pass
// (bullet 18); the inversion it triggered (bullet 19); and, on requirement r-three, alice's lens
// reserved for her between two project lenses, with a project lens on the issue triggered by it
// (bullet 22). Every lens is alice's: a person's credential proves an author, which a lens
// reserved for its author needs.
func (s *seeder) writeLenses(ctx context.Context, key string) error {
	lens := func(on, kind, verdict, triggeredBy, visibility string) (string, error) {
		input := map[string]any{"on": on, "kind": kind, "verdict": verdict}
		if triggeredBy != "" {
			input["triggered_by"] = triggeredBy
		}
		if visibility != "" {
			input["visibility"] = visibility
		}
		var invoked struct {
			Address string `json:"address"`
		}
		if err := s.api.call(ctx, asPerson(alice), http.MethodPost, "/api/v1/issues/"+key+"/lenses", input, &invoked); err != nil {
			return "", fmt.Errorf("invoke the %s lens on %s: %w", kind, on, err)
		}
		return invoked.Address, nil
	}
	premortem, err := lens(issueRef(key), "pre-mortem", "The page fails if a decision is drawn without who made it.", "", "")
	if err != nil {
		return err
	}
	s.corpus.set("lens.premortem", premortem)
	for index, risk := range []struct{ heading, warning, prevention, preventedBy string }{
		{"An answered ask is drawn as an agent's decision", "A person's answer shows an agent's mark", "Draw an answered ask from its row", verificationRef(key, "k-pass")},
		{"A decision loses its reason", "A row shows no reason where the node holds one", "Read the reason from the node", ""},
	} {
		input := map[string]any{"lens": premortem, "heading": risk.heading, "early_warning": risk.warning, "prevention": risk.prevention}
		if risk.preventedBy != "" {
			input["prevented_by"] = risk.preventedBy
		}
		var written struct {
			Address string `json:"address"`
		}
		if err := s.api.call(ctx, asPerson(alice), http.MethodPost, "/api/v1/issues/"+key+"/risks", input, &written); err != nil {
			return fmt.Errorf("write risk %d: %w", index, err)
		}
		s.corpus.set(fmt.Sprintf("lens.risk.%d", index), written.Address)
	}
	inversion, err := lens(issueRef(key), "inversion", "Draw only what a person decided, and the agent's decisions apart.", premortem, "")
	if err != nil {
		return err
	}
	s.corpus.set("lens.inversion", inversion)

	before, err := lens(specRef(key, "r-three"), "trade-off", "A reason kept beside its decision costs a row of height.", "", "")
	if err != nil {
		return err
	}
	hidden, err := lens(specRef(key, "r-three"), "first-principles", "A reason is the decision's own words, so it is kept with them.", "", "author")
	if err != nil {
		return err
	}
	after, err := lens(specRef(key, "r-three"), "boring-solution", "Keep the reason in the decision's block.", "", "")
	if err != nil {
		return err
	}
	backlink, err := lens(issueRef(key), "second-order", "Once reasons are kept, a reader expects one on every decision.", hidden, "")
	if err != nil {
		return err
	}
	s.corpus.set("lens.hidden", hidden)
	s.corpus.set("lens.hidden_before", before)
	s.corpus.set("lens.hidden_after", after)
	s.corpus.set("lens.hidden_backlink", backlink)
	return nil
}
