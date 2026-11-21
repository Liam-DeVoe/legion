package api

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// retriedItems is what the Retry rows retry, all carol's and all on PUB's issue nobody claimed
// (seedHiddenHolders' unclaimed), by name: comments that mention nothing, a role of PRIV
// (role:legion-priv-architect, which session-pub holds), session-priv (which acts in PRIV), a role
// session-priv holds (role:reviewer, so the mention resolved to session-priv), session-pub and that
// role of PRIV together, and session-pub alone; and messages with no target, to that role of PRIV,
// and to session-priv. carol sees PRIV, so each mention and each message reached its session.
type retriedItems struct {
	comments map[string]string
	messages map[string]string
}

// The names of the retried items, and the targets they name.
const (
	noMention          = "no mention"
	privateRoleMention = "a role of PRIV"
	hiddenSession      = "session-priv"
	hiddenHolderMntn   = "a role session-priv holds"
	visibleAndHidden   = "session-pub and a role of PRIV"
	visibleOnly        = "session-pub alone"
	noTarget           = "no target"
	unmentionedTarget  = "role:nobody-mentions"
)

// writeRetriedItems writes retriedItems through the routes as carol and waits for every mention's
// send to settle, so a Retry finds no attempt pending.
func writeRetriedItems(t *testing.T, seed hiddenHolderSeed) retriedItems {
	t.Helper()
	items := retriedItems{comments: map[string]string{}, messages: map[string]string{}}
	key := seed.unclaimed
	for name, targets := range map[string][]string{
		noMention:          nil,
		privateRoleMention: {"role:" + privateProjectRole},
		hiddenSession:      {"session:" + sessionOfPrivate},
		hiddenHolderMntn:   {"role:" + hiddenHolderRole},
		visibleAndHidden:   {"session:" + sessionOfPublic, "role:" + privateProjectRole},
		visibleOnly:        {"session:" + sessionOfPublic},
	} {
		body := map[string]any{"body": "A remark carol makes."}
		if len(targets) > 0 {
			mentions := []map[string]string{}
			for _, target := range targets {
				mentions = append(mentions, map[string]string{"target": target})
			}
			body["mentions"] = mentions
		}
		items.comments[name] = createdID(t, seed.request(t, callerCarol, http.MethodPost, "/api/v1/issues/"+key+"/comments", body))
	}
	for name, target := range map[string]string{
		noTarget:           "",
		privateRoleMention: "role:" + privateProjectRole,
		hiddenSession:      "session:" + sessionOfPrivate,
	} {
		body := map[string]any{"body": "A note carol sends."}
		if target != "" {
			body["target"], body["delivery"] = target, "btw"
		}
		items.messages[name] = createdID(t, seed.request(t, callerCarol, http.MethodPost, "/api/v1/issues/"+key+"/messages", body))
	}
	awaitBackgroundWrites(t, seed.server)
	return items
}

// retry is caller's Retry of comment or message id (kind "comments" or "messages") with body, and
// the statements the request made.
func retry(t *testing.T, seed hiddenHolderSeed, counter *contextStatements, caller privateCaller, kind, id string, body map[string]any) (*httptest.ResponseRecorder, int64) {
	t.Helper()
	before := counter.sent.Load()
	answer := seed.requestIn(t, context.WithValue(t.Context(), contextStatementsKey{}, true), caller, http.MethodPost, "/api/v1/"+kind+"/"+id+"/deliveries", body)
	sent := counter.sent.Load() - before
	awaitBackgroundWrites(t, seed.server)
	return answer, sent
}

// retryBodies are the three Retries the rows make of a comment whose mention its caller does not
// read: with no target, naming the hidden target, and naming a target nobody mentions.
func retryBodies(hidden string) map[string]map[string]any {
	return map[string]map[string]any{
		"no target":                {"delivery": "btw"},
		"the hidden target":        {"delivery": "btw", "target": hidden},
		"a target nobody mentions": {"delivery": "btw", "target": unmentionedTarget},
	}
}

// TestARetryNamesNoMentionItsCallerDoesNotRead: alice, an open member who does not see PRIV, reads
// none of three of carol's mentions on PUB's issue - a role of PRIV, session-priv, and a role
// session-priv holds - since GET /api/v1/comments leaves each out for her
// (leaveOutHiddenCommentSessions). So POST /api/v1/comments/{id}/deliveries answers each such comment
// byte for byte as it answers carol's comment that mentions nothing - with no target, naming the
// hidden target, and naming a target nobody mentions - at the same cost in statements, and names
// neither the role nor the session. Of a comment mentioning session-pub and that role of PRIV, a
// Retry with no target takes session-pub, the one mention she reads, as a comment mentioning
// session-pub alone answers it, and naming the role answers as that comment answers it. carol, who
// sees PRIV, retries the role's mention with no target and reaches session-pub: the listener
// answers, so alice's answers are not a listener's silence.
//
// Control: retryableMentionTargets without leaveOutHiddenCommentSessions (the unfiltered read
// restored); alice's Retry then answers 201 naming role:legion-priv-architect and session-priv, and
// 400 for a target nobody mentions, and each row fails naming the target.
func TestARetryNamesNoMentionItsCallerDoesNotRead(t *testing.T) {
	counter := &contextStatements{}
	seed := seedHiddenHolders(t, testServerOptions{store: storetest.TracedStore(t, storetest.Open(t), counter)})
	items := writeRetriedItems(t, seed)
	comment := func(caller privateCaller, name string, body map[string]any) (*httptest.ResponseRecorder, int64) {
		t.Helper()
		return retry(t, seed, counter, caller, "comments", items.comments[name], body)
	}

	if reached, _ := comment(callerCarol, privateRoleMention, map[string]any{"delivery": "btw"}); reached.Code != http.StatusCreated ||
		!strings.Contains(reached.Body.String(), `"session_id":"`+sessionOfPublic+`"`) || !strings.Contains(reached.Body.String(), `"state":"sent"`) {
		t.Fatalf("carol's Retry of her mention of role:%s answers %d %s, want it sent to %s", privateProjectRole, reached.Code, reached.Body.String(), sessionOfPublic)
	}

	for name, hidden := range map[string]string{
		privateRoleMention: "role:" + privateProjectRole,
		hiddenSession:      "session:" + sessionOfPrivate,
		hiddenHolderMntn:   "role:" + hiddenHolderRole,
	} {
		for retried, body := range retryBodies(hidden) {
			// Each pair is counted after a first call of the same pair, so neither pays a cache's
			// first fill.
			comment(callerAlice, name, body)
			comment(callerAlice, noMention, body)
			got, gotCost := comment(callerAlice, name, body)
			want, wantCost := comment(callerAlice, noMention, body)
			if got.Code != want.Code || got.Body.String() != want.Body.String() {
				t.Errorf("alice's Retry with %s of carol's mention of %s answers %d %s; of carol's comment that mentions nothing, %d %s: want one answer",
					retried, hidden, got.Code, strings.TrimSpace(got.Body.String()), want.Code, strings.TrimSpace(want.Body.String()))
			}
			for _, named := range []string{privateProjectRole, sessionOfPrivate, hiddenHolderRole} {
				if strings.Contains(got.Body.String(), named) {
					t.Errorf("alice's Retry with %s of carol's mention of %s names %s: %s", retried, hidden, named, strings.TrimSpace(got.Body.String()))
				}
			}
			if gotCost != wantCost {
				t.Errorf("alice's Retry with %s of carol's mention of %s costs %d statements; of carol's comment that mentions nothing, %d: want the same",
					retried, hidden, gotCost, wantCost)
			}
		}
	}

	for retried, body := range retryBodies("role:" + privateProjectRole) {
		got, _ := comment(callerAlice, visibleAndHidden, body)
		want, _ := comment(callerAlice, visibleOnly, body)
		gotText := timestampsAndIDs.ReplaceAllString(got.Body.String(), "<varies>")
		wantText := timestampsAndIDs.ReplaceAllString(want.Body.String(), "<varies>")
		if got.Code != want.Code || gotText != wantText {
			t.Errorf("alice's Retry with %s of carol's mentions of session-pub and role:%s answers %d %s; of her mention of session-pub alone, %d %s: want one answer",
				retried, privateProjectRole, got.Code, strings.TrimSpace(gotText), want.Code, strings.TrimSpace(wantText))
		}
		if retried == "no target" && (got.Code != http.StatusCreated || !strings.Contains(got.Body.String(), `"target":"session:`+sessionOfPublic+`"`)) {
			t.Errorf("alice's Retry with no target of carol's mentions of session-pub and role:%s answers %d %s, want it to take session:%s, the one mention she reads",
				privateProjectRole, got.Code, strings.TrimSpace(got.Body.String()), sessionOfPublic)
		}
	}
}

// TestARetryOfAMessageNamesNoTargetItsCallerDoesNotRead: alice reads carol's messages on PUB's
// issue to a role of PRIV and to session-priv with no target (leaveOutHiddenMessageSessions), so
// POST /api/v1/messages/{id}/deliveries answers each byte for byte as it answers carol's message
// with no target, at the same cost in statements, and names neither the role nor the session. The
// seed's listener answers: carol's Retry of the same two messages reaches session-pub and
// session-priv, so alice's answers are not a listener's silence.
//
// Control: createDelivery without leaveOutHiddenMessageSessions (the unfiltered target restored);
// alice's Retry then answers 201, its error naming role:legion-priv-architect and its session
// session-priv, and the row fails naming each.
func TestARetryOfAMessageNamesNoTargetItsCallerDoesNotRead(t *testing.T) {
	counter := &contextStatements{}
	seed := seedHiddenHolders(t, testServerOptions{store: storetest.TracedStore(t, storetest.Open(t), counter)})
	items := writeRetriedItems(t, seed)
	message := func(caller privateCaller, name string) (*httptest.ResponseRecorder, int64) {
		t.Helper()
		return retry(t, seed, counter, caller, "messages", items.messages[name], map[string]any{"delivery": "btw"})
	}
	for name, session := range map[string]string{privateRoleMention: sessionOfPublic, hiddenSession: sessionOfPrivate} {
		if reached, _ := message(callerCarol, name); reached.Code != http.StatusCreated ||
			!strings.Contains(reached.Body.String(), `"session_id":"`+session+`"`) || !strings.Contains(reached.Body.String(), `"state":"sent"`) {
			t.Fatalf("carol's Retry of her message to %s answers %d %s, want it sent to %s", name, reached.Code, reached.Body.String(), session)
		}
	}
	for name, hidden := range map[string]string{privateRoleMention: "role:" + privateProjectRole, hiddenSession: "session:" + sessionOfPrivate} {
		message(callerAlice, name)
		message(callerAlice, noTarget)
		got, gotCost := message(callerAlice, name)
		want, wantCost := message(callerAlice, noTarget)
		if got.Code != want.Code || got.Body.String() != want.Body.String() {
			t.Errorf("alice's Retry of carol's message to %s answers %d %s; of carol's message with no target, %d %s: want one answer",
				hidden, got.Code, strings.TrimSpace(got.Body.String()), want.Code, strings.TrimSpace(want.Body.String()))
		}
		for _, named := range []string{privateProjectRole, sessionOfPrivate, sessionOfPublic} {
			if strings.Contains(got.Body.String(), named) {
				t.Errorf("alice's Retry of carol's message to %s names %s: %s", hidden, named, strings.TrimSpace(got.Body.String()))
			}
		}
		if gotCost != wantCost {
			t.Errorf("alice's Retry of carol's message to %s costs %d statements; of carol's message with no target, %d: want the same", hidden, gotCost, wantCost)
		}
	}
}

// TestWithNoPrivateProjectARetryAnswersAsBefore (R9): once PRIV is public again the deployment
// holds no private project and alice sees every object, so each Retry of the rows above answers
// her as the base did: a mention of the role of PRIV, of session-priv and of the role it holds each
// reaches its session, the comment mentioning session-pub and the role refuses a Retry with no
// target, and naming that role or a target nobody mentions answers as the base answered it; each
// message reaches its session. Each answer is logged with its times and ids blanked, beside the
// sha256 of what it logs, so a run at the base and a run at the head compare byte for byte.
//
// Control: retryableMentionTargets answering no target at all; alice's Retries of the comments then
// answer 404 and the row fails.
func TestWithNoPrivateProjectARetryAnswersAsBefore(t *testing.T) {
	counter := &contextStatements{}
	seed := seedHiddenHolders(t, testServerOptions{store: storetest.TracedStore(t, storetest.Open(t), counter)})
	items := writeRetriedItems(t, seed)
	seed.setVisibility(t, seedPrivate, model.ProjectPublic)
	for _, row := range []struct {
		kind, name string
		body       map[string]any
		status     int
		holds      string
	}{
		{"comments", privateRoleMention, map[string]any{"delivery": "btw"}, http.StatusCreated, `"session_id":"` + sessionOfPublic + `"`},
		{"comments", privateRoleMention, map[string]any{"delivery": "btw", "target": unmentionedTarget}, http.StatusBadRequest, "target must name one of the comment mentions"},
		{"comments", hiddenSession, map[string]any{"delivery": "btw", "target": "session:" + sessionOfPrivate}, http.StatusCreated, `"session_id":"` + sessionOfPrivate + `"`},
		{"comments", hiddenHolderMntn, map[string]any{"delivery": "btw"}, http.StatusCreated, `"session_id":"` + sessionOfPrivate + `"`},
		{"comments", visibleAndHidden, map[string]any{"delivery": "btw"}, http.StatusBadRequest, "target is required when a comment has multiple mentions"},
		{"comments", visibleAndHidden, map[string]any{"delivery": "btw", "target": "role:" + privateProjectRole}, http.StatusCreated, `"session_id":"` + sessionOfPublic + `"`},
		{"messages", privateRoleMention, map[string]any{"delivery": "btw"}, http.StatusCreated, `"session_id":"` + sessionOfPublic + `"`},
		{"messages", hiddenSession, map[string]any{"delivery": "btw"}, http.StatusCreated, `"session_id":"` + sessionOfPrivate + `"`},
	} {
		id := items.comments[row.name]
		if row.kind == "messages" {
			id = items.messages[row.name]
		}
		answer, _ := retry(t, seed, counter, callerAlice, row.kind, id, row.body)
		blanked := timestampsAndIDs.ReplaceAllString(answer.Body.String(), "<varies>")
		t.Logf("R9 %s of %s with %v: %d sha256 %x: %s", row.kind, row.name, row.body, answer.Code, sha256.Sum256([]byte(blanked)), strings.TrimSpace(blanked))
		if answer.Code != row.status || !strings.Contains(answer.Body.String(), row.holds) {
			t.Errorf("PRIV public: alice's Retry of the %s %q with %v answers %d %s, want %d holding %s, as before",
				row.kind, row.name, row.body, answer.Code, strings.TrimSpace(answer.Body.String()), row.status, row.holds)
		}
	}
}
