package api

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/agentstream"
)

// The sessions of TestEveryLiveViewKeepsTheRuleOfItsHTTPRead, by the uuids an agent view names:
// one has acted in PRIV alone, one in PUB alone, and one is on no roster and has done nothing.
const (
	liveReadsPrivSession   = "0b8a3c1e-5f2d-4a7b-9c3e-1d2f3a4b5c61"
	liveReadsPubSession    = "0b8a3c1e-5f2d-4a7b-9c3e-1d2f3a4b5c62"
	liveReadsAbsentSession = "0b8a3c1e-5f2d-4a7b-9c3e-1d2f3a4b5c63"
)

// liveReadsWait bounds a wait for a live view's first answer; liveReadsQuiet is how long a view
// that answers as an absent session must then stay silent.
const (
	liveReadsWait  = 5 * time.Second
	liveReadsQuiet = 300 * time.Millisecond
)

// TestEveryLiveViewKeepsTheRuleOfItsHTTPRead: every view kind /ws/live serves (deliveryLiveKinds
// and the server's liveKinds) names the GET row whose rule it keeps (liveViewReads), and answers
// each caller of verification v16 as that row answers the caller: refused with the row's own code
// where the row refuses, and served where it serves. An agent view keeps its row's session rule
// as well (access.SessionClause, which GET /api/v1/agents lists sessions by): of a session the
// caller's roster lists, the view relays the session; of any other, it answers exactly what a
// session nobody answers for answers, and never tells the session a viewer is attached.
//
// Control: today's views (no read rule on the live socket). bob's timeline is served where its
// GET row refuses him, and alice's agent view relays PRIV's session, and the test fails naming each.
func TestEveryLiveViewKeepsTheRuleOfItsHTTPRead(t *testing.T) {
	roster := `[
		{"session_id":"` + liveReadsPrivSession + `","title":"Private","capabilities":["btw"],"last_seen":200},
		{"session_id":"` + liveReadsPubSession + `","title":"Public","capabilities":["btw"],"last_seen":100}
	]`
	listener, _ := newBroadcastListener(t, roster)
	source := agentstream.NewMemory()
	for _, session := range []string{liveReadsPrivSession, liveReadsPubSession} {
		source.SetReplay(session, agentstream.Frame(`{"v":1,"kind":"replay","session":"`+session+`"}`))
	}
	seed := seedPrivateProjectWith(t, testServerOptions{envoyURL: listener.URL, agentStream: source})
	seedHubSettings(t, seed.database)
	for session, project := range map[string]string{liveReadsPrivSession: seedPrivate, liveReadsPubSession: seedPublic} {
		seed.asSession(t, session, project, http.MethodPost, "/api/v1/issues/"+seed.issue[project]+"/comments",
			map[string]any{"body": "Done on " + project + "."}, http.StatusCreated)
	}
	url := liveURLOf(t, seed.handler)

	kinds := slices.Concat(slices.Sorted(maps.Keys(deliveryLiveKinds)), slices.Sorted(maps.Keys(seed.server.liveKinds)))
	for _, kind := range kinds {
		route, named := liveViewReads[kind]
		if !named {
			t.Fatalf("the live view kind %q names no GET row whose rule it keeps (liveViewReads)", kind)
		}
		if _, classed := readClasses[route]; !classed || !strings.HasPrefix(route, http.MethodGet+" ") {
			t.Fatalf("the live view kind %q keeps the rule of %q, which is no GET row of the table", kind, route)
		}
	}
	now := time.Now().UTC()
	from, to := now.Add(-7*24*time.Hour).Format(time.RFC3339), now.Format(time.RFC3339)
	// view and target are each kind's view, and the target of the GET row whose rule it keeps,
	// over the same window or session.
	view := func(kind, session string) LiveView {
		switch kind {
		case deliveryLiveTimeline:
			return LiveView{DeliveryLiveView: timelineRange(now.Add(-7*24*time.Hour), now, "")}
		case deliveryLiveMeasures:
			return LiveView{DeliveryLiveView: measuresRange(now.Add(-7*24*time.Hour), now, "")}
		case deliveryLivePipeline:
			return LiveView{DeliveryLiveView: livePipelineView(nil)}
		case liveAgentView:
			return agentView(session)
		}
		return eventsView()
	}
	target := func(kind, session string) string {
		route := strings.TrimPrefix(liveViewReads[kind], http.MethodGet+" ")
		route = strings.ReplaceAll(route, "{session_id}", session)
		if kind == deliveryLiveTimeline || kind == deliveryLiveMeasures {
			route += "?from=" + from + "&to=" + to
		}
		return route
	}
	// first opens a socket as caller, subscribes the view, and answers the first frame it is sent.
	first := func(caller privateCaller, kind, session string) (*socketClient, any) {
		t.Helper()
		client := dialLive(t, url, seed.liveHeader(t, caller))
		client.send(t, liveSubscribe("v", view(kind, session), ""))
		return client, client.next(t, liveReadsWait, fmt.Sprintf("%s's %s view", caller, kind))
	}

	for _, caller := range privateCallers {
		for _, kind := range kinds {
			answer := seed.request(t, caller, http.MethodGet, target(kind, liveReadsAbsentSession), nil)
			_, frame := first(caller, kind, liveReadsAbsentSession)
			refusal, refused := frame.(LiveError)
			if answer.Code >= http.StatusBadRequest {
				var body struct {
					Code string `json:"code"`
				}
				_ = json.Unmarshal(answer.Body.Bytes(), &body)
				if !refused || refusal.Code != body.Code {
					t.Errorf("%s: GET %s answers %d %s, and the %s view answers %+v, want the same refusal", caller, target(kind, liveReadsAbsentSession), answer.Code, body.Code, kind, frame)
				}
				continue
			}
			if refused {
				t.Errorf("%s: GET %s answers %d, and the %s view refuses it: %+v", caller, target(kind, liveReadsAbsentSession), answer.Code, kind, refusal)
			}
		}

		// The agent view's session rule, for a caller its row serves.
		if answer := seed.request(t, caller, http.MethodGet, target(liveAgentView, liveReadsAbsentSession), nil); answer.Code != http.StatusOK {
			continue
		}
		listed := map[string]bool{}
		for _, agent := range decodeBody[[]struct {
			SessionID string `json:"session_id"`
		}](t, seed.mustRequest(t, caller, http.MethodGet, "/api/v1/agents", nil, http.StatusOK)) {
			listed[agent.SessionID] = true
		}
		absent, absentFrame := first(caller, liveAgentView, liveReadsAbsentSession)
		absent.quiet(t, liveReadsQuiet, fmt.Sprintf("%s's agent view of a session nobody answers for", caller))
		for _, session := range []string{liveReadsPrivSession, liveReadsPubSession} {
			watched := source.Watches(session)
			client, frame := first(caller, liveAgentView, session)
			got, _ := json.Marshal(frame)
			if listed[session] {
				if agent, ok := frame.(LiveAgentFrame); !ok || string(agent.Data) != `{"v":1,"responding":true}` {
					t.Errorf("%s: GET /api/v1/agents lists %s, and its agent view answers %s, want the session's responder", caller, session, got)
				}
				continue
			}
			want, _ := json.Marshal(absentFrame)
			if string(got) != string(want) {
				t.Errorf("%s: GET /api/v1/agents does not list %s, and its agent view answers %s, want what a session nobody answers for answers, %s", caller, session, got, want)
				continue
			}
			client.quiet(t, liveReadsQuiet, fmt.Sprintf("%s's agent view of %s, which its roster does not list", caller, session))
			if now := source.Watches(session); now != watched {
				t.Errorf("%s: its agent view of %s, which its roster does not list, told the session a viewer is attached (%d times)", caller, session, now-watched)
			}
		}
	}
}
