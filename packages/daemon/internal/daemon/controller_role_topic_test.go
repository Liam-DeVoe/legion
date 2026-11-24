package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/wait"
)

// controllerRoleTopic is the role topic of project legion's controller,
// notifications.role.legion-<project>-controller (notify.RoleTopicPrefix and
// claim.ControllerToken), written out so that a change of either is a change this test reads.
const controllerRoleTopic = "notifications.role.legion-legion-controller"

// R2 (d) of dispatch://INFRA-1585: a controller notice row waits while no session holds the
// controller role, and while the registered controller session holds it but the listener reads it
// unresponsive, and logs each attempt as a wait, never as a failed row; once the registered
// session is the role's live holder, the row is published once, on the controller's role topic,
// without keep, under the row's own key, and finishes.
//
// The control is main's code (the pull request records the run): there the first attempt
// publishes the notice to the controller topic (notifications.legion.legion.controller) while no
// session holds the role, and an unresponsive holder's answer fails the attempt as an error.
func TestAControllerNoticeWaitsForALiveHolderOfTheControllerRoleThenGoesToItsRoleTopicOnce(t *testing.T) {
	t.Parallel()
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	registerControllerSession(t, pool, "legion", "ses_controller")
	listener := newControllerRoleListener()
	server := httptest.NewServer(listener)
	t.Cleanup(server.Close)
	clock := time.Now().UTC()
	row := mustOutboxRow(t, "LEGION-2", record.ControllerNotice{Kind: "held", Role: claim.RolePlanner, Phase: phase.Planning}, clock)
	enqueueOutbox(t, pool, records, row)
	// The row's own key names the id the store gave it, which the row built above does not hold.
	if err := pool.QueryRow(context.Background(), "select id from outbox").Scan(&row.ID); err != nil {
		t.Fatalf("read the controller notice row's id: %v", err)
	}
	// At debug, since settle logs a row's second wait there (the wait's first attempt is at INFO).
	var logged syncBuffer
	runner := &outbox{log: slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})), pool: pool, dispatchProject: "LEGION", records: records,
		notices: notify.New(server.URL, ""), project: "legion", now: func() time.Time { return clock }}
	attempt := func(what string) {
		t.Helper()
		if err := runner.RunOnce(context.Background()); err != nil {
			t.Fatalf("run the outbox %s: %v", what, err)
		}
	}

	attempt("while no session holds the controller role")
	if got := listener.publishedTopics(); len(got) != 0 {
		t.Fatalf("published to %v while no session holds the controller role, want nothing: the notice waits for a live holder", got)
	}
	if rows := outboxRows(t, pool); rows != 1 {
		t.Fatalf("%d outbox rows after the attempt with no holder, want the notice's row kept", rows)
	}

	listener.answer("ses_controller", notify.StateUnresponsive)
	clock = clock.Add(record.RetryBackoffMost + time.Second)
	attempt("while the registered controller session holds the role unresponsive")
	if got := listener.publishedTopics(); len(got) != 0 {
		t.Fatalf("published to %v while the controller role's holder is unresponsive, want nothing", got)
	}
	if rows := outboxRows(t, pool); rows != 1 {
		t.Fatalf("%d outbox rows after the attempt with an unresponsive holder, want the notice's row kept", rows)
	}

	listener.answer("ses_controller", "live")
	clock = clock.Add(record.RetryBackoffMost + time.Second)
	attempt("once the registered controller session holds the role live")
	published := listener.publishes()
	if len(published) != 1 {
		t.Fatalf("published %+v once the registered session holds the role live, want the notice once", published)
	}
	if p := published[0]; p.Topic != controllerRoleTopic || p.Message != "held on LEGION-2" || p.DedupeKey != record.OutboxKey(row.ID) ||
		p.Keep == nil || *p.Keep {
		keep := "absent"
		if p.Keep != nil {
			keep = fmt.Sprint(*p.Keep)
		}
		t.Fatalf("published %+v (keep %s), want held on LEGION-2 on %s under %s with keep false", p, keep, controllerRoleTopic, record.OutboxKey(row.ID))
	}
	if rows := outboxRows(t, pool); rows != 0 {
		t.Fatalf("%d outbox rows once the notice was published, want none", rows)
	}

	text := logged.String()
	if strings.Contains(text, "level=ERROR") || strings.Contains(text, `msg="outbox row failed"`) {
		t.Fatalf("the two waits were logged as a failure:\n%s", text)
	}
	var waits []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, `msg="outbox row waits"`) && strings.Contains(line, "kind=controller_notice") {
			waits = append(waits, line)
		}
	}
	if len(waits) != 2 || !strings.Contains(waits[0], "no holder") ||
		!strings.Contains(waits[1], "ses_controller") || !strings.Contains(waits[1], notify.StateUnresponsive) {
		t.Fatalf("logged the waits %q, want two: one naming no holder, then one naming ses_controller unresponsive:\n%s", waits, text)
	}
}

// R2 (e): every kind of controller notice goes to the controller's role topic, a project's tick
// (keyed by the Dispatch project alone) and a stopped tree's notice among them, each under its
// own row's key.
//
// The control is main's code: there both go to the controller topic.
func TestATickAndATreeStalledNoticeGoToTheControllersRoleTopic(t *testing.T) {
	t.Parallel()
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	registerControllerSession(t, pool, "legion", "ses_controller")
	listener := newControllerRoleListener()
	listener.answer("ses_controller", "live")
	server := httptest.NewServer(listener)
	t.Cleanup(server.Close)
	clock := time.Now().UTC()
	for _, row := range []record.OutboxRow{
		mustOutboxRow(t, "LEGION", record.ControllerNotice{Kind: record.TickNotice}, clock),
		mustOutboxRow(t, "LEGION-1", record.ControllerNotice{Kind: record.TreeStalledNotice, Reason: "the tree of LEGION-1 has not moved"}, clock),
	} {
		enqueueOutbox(t, pool, records, row)
	}
	runner := &outbox{log: quietLogger(), pool: pool, dispatchProject: "LEGION", records: records,
		notices: notify.New(server.URL, ""), project: "legion", now: func() time.Time { return clock }}
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("run the outbox: %v", err)
	}
	var messages []string
	for _, p := range listener.publishes() {
		if p.Topic != controllerRoleTopic {
			t.Fatalf("published %q to %s, want every controller notice on %s", p.Message, p.Topic, controllerRoleTopic)
		}
		messages = append(messages, p.Message)
	}
	slices.Sort(messages)
	if want := []string{"tick on LEGION", "tree-stalled on LEGION-1"}; !slices.Equal(messages, want) {
		t.Fatalf("published %v, want %v on %s", messages, want, controllerRoleTopic)
	}
	if rows := outboxRows(t, pool); rows != 0 {
		t.Fatalf("%d outbox rows once both were published, want none", rows)
	}
}

// R2 (f): the controller's role topic follows the deployment's controller mode (heldTopic). Under
// `controller: operator` the role is the operator's own controller's, so a launched controller
// claim an earlier boot retired (stopLaunchedController), whose session another claim holds,
// holds no controller notice back: it is published once the operator's registered session holds
// the role live. Under `controller: daemon` the launched claim is the project's controller, so a
// notice waits while that claim records a session it does not hold, and is published while it
// holds its own.
//
// The controls are main's code, which publishes every one of them to the controller topic; and
// heldTopic holding the controller's role topic under either mode, which holds the notice under
// `controller: operator` too.
func TestTheControllerRoleTopicIsHeldOnlyToTheDaemonsOwnController(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	notice := func(t *testing.T) record.OutboxRow {
		row := mustOutboxRow(t, "LEGION-941", record.ControllerNotice{Kind: "held", Role: claim.RolePlanner, Phase: phase.Planning}, time.Now())
		row.ID = 99
		return row
	}
	t.Run("controller: operator, a retired launched claim not holding its session", func(t *testing.T) {
		t.Parallel()
		listener, runner := controllerRoleRig(t, false, "ses_operator", supervise.StateRetired, "ses_controller")
		if err := runner.execute(ctx, notice(t)); err != nil {
			t.Fatalf("the controller notice under controller: operator = %v, want it published to the operator's controller", err)
		}
		if got, want := listener.publishedTopics(), []string{controllerRoleTopic}; !slices.Equal(got, want) {
			t.Fatalf("published to %v, want %v: the operator's controller holds the role", got, want)
		}
	})
	t.Run("controller: daemon, its claim not holding its session", func(t *testing.T) {
		t.Parallel()
		listener, runner := controllerRoleRig(t, true, "ses_controller", supervise.StateReady, "ses_controller")
		err := runner.execute(ctx, notice(t))
		if !errors.Is(err, wait.ErrWaiting) || !errors.Is(err, notify.ErrNoHolder) {
			t.Fatalf("the controller notice = %v, want a wait on the gate's refusal", err)
		}
		if got := listener.publishedTopics(); len(got) != 0 {
			t.Fatalf("published to %v while the daemon's controller claim records a session it does not hold", got)
		}
	})
	t.Run("controller: daemon, its claim holding its session", func(t *testing.T) {
		t.Parallel()
		listener, runner := controllerRoleRig(t, true, "ses_controller", supervise.StateReady)
		if err := runner.execute(ctx, notice(t)); err != nil {
			t.Fatalf("the controller notice while the daemon's controller claim holds its session = %v, want it published", err)
		}
		if got, want := listener.publishedTopics(), []string{controllerRoleTopic}; !slices.Equal(got, want) {
			t.Fatalf("published to %v, want %v: the controller claim holds its own session", got, want)
		}
	})
}

// controllerRoleRig is an outbox of project legion, `controller: daemon` when launched, whose
// store records registered as the project's controller session and whose listener names that
// session the role's live holder. Its supervisor holds the launched controller's claim, on
// session ses_controller in state, and each of taken is a session another claim, legion-943's
// implementer, recorded first.
func controllerRoleRig(t *testing.T, launched bool, registered string, state supervise.ClaimState, taken ...string) (*controllerRoleListener, *outbox) {
	t.Helper()
	ctx := context.Background()
	pool := isolatedOutboxPool(t)
	registerControllerSession(t, pool, "legion", registered)
	listener := newControllerRoleListener()
	listener.answer(registered, "live")
	server := httptest.NewServer(listener)
	t.Cleanup(server.Close)
	sup, _ := newOutboxSupervisor(t, "legion", t.TempDir())
	claims := sup.deps.Store.(*outboxClaimStore)
	holder := mustClaimToken(t, "LEGION-943", claim.RoleImplementer)
	for _, s := range taken {
		if _, err := claims.HoldSessions(ctx, "legion", holder, supervise.RecordedHolds(s, "")); err != nil {
			t.Fatalf("hold %s for %s, the claim that recorded it first: %v", s, holder, err)
		}
	}
	if _, _, err := sup.Create(ctx, supervise.Claim{Token: claim.ControllerToken("legion"), Role: claim.RoleController, Session: "ses_controller",
		State: state, Project: "legion", Generation: 1, SessionFile: "/sessions/ses_controller.jsonl"}, ""); err != nil {
		t.Fatalf("create the launched controller's claim: %v", err)
	}
	runner := newOutbox(pool, record.NewStore(), &outboxDispatch{}, notify.New(server.URL, ""), sup, nil, nil, nil, nil,
		"legion", "LEGION", t.TempDir(), "", nil, nil, launched, quietLogger())
	return listener, runner
}

// registerControllerSession records session as the registered controller of the project token
// project, as `legion controller start`'s mint and the session's registration leave the record.
func registerControllerSession(t *testing.T, pool *store.Pool, project, session string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, pool.Config().ConnString())
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	defer st.Close()
	generation, err := st.MintController(ctx, project, []byte("capability"))
	if err != nil {
		t.Fatalf("mint the controller capability: %v", err)
	}
	if ok, err := st.RegisterController(ctx, project, generation, session, []byte("secret"), time.Now(), 0); err != nil || !ok {
		t.Fatalf("register %s as the controller = %t, %v", session, ok, err)
	}
}

// controllerRoleListener is the Envoy listener's two routes as a controller notice meets them:
// the role lookup (GET /v1/roles/<role>, as roleGetHandler and writeRoleHolderError answer it)
// and the publish (POST /v1/messages/publish). It names one holder of every role, in one state:
// none at first, which is the 404 of a role no claim names; `live`, which is the 200; or any
// other state, which is the 404 of a lapsed holder in that state. A publish to a role topic is
// answered as the listener answers keep: false, by that same holder: a live one takes it with
// 200, and every other answer is the lookup's 404, which stores nothing. A publish to any other
// topic is taken. Every publish taken is recorded.
type controllerRoleListener struct {
	mu        sync.Mutex
	holder    string
	state     string
	published []controllerRolePublish
}

type controllerRolePublish struct {
	Topic     string `json:"topic"`
	Message   string `json:"message"`
	DedupeKey string `json:"dedupe_key"`
	Keep      *bool  `json:"keep"`
}

func newControllerRoleListener() *controllerRoleListener { return &controllerRoleListener{} }

// answer is the holder every role lookup and role publish names from now on.
func (l *controllerRoleListener) answer(holder, state string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.holder, l.state = holder, state
}

func (l *controllerRoleListener) publishes() []controllerRolePublish {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.published)
}

func (l *controllerRoleListener) publishedTopics() []string {
	var topics []string
	for _, p := range l.publishes() {
		topics = append(topics, p.Topic)
	}
	return topics
}

func (l *controllerRoleListener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if role, ok := strings.CutPrefix(r.URL.Path, "/v1/roles/"); ok && r.Method == http.MethodGet {
		l.writeHolder(w, role)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v1/messages/publish" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"not found"}`)
		return
	}
	body, err := io.ReadAll(r.Body)
	var published controllerRolePublish
	if err != nil || json.Unmarshal(body, &published) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if role, ok := strings.CutPrefix(published.Topic, notify.RoleTopicPrefix); ok && (l.holder == "" || l.state != "live") {
		l.writeHolder(w, role)
		return
	}
	l.published = append(l.published, published)
	_, _ = io.WriteString(w, `{}`)
}

// writeHolder is the role lookup's answer for role, by the holder the listener names.
func (l *controllerRoleListener) writeHolder(w http.ResponseWriter, role string) {
	switch {
	case l.holder == "":
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":"role %s is unclaimed","reason":"unclaimed"}`, role)
	case l.state == "live":
		var body bytes.Buffer
		_ = json.NewEncoder(&body).Encode(map[string]any{"role": role, "holder": l.holder, "last_seen": time.Now().UnixMilli(),
			"holder_state": "live", "capabilities": []string{}})
		_, _ = w.Write(body.Bytes())
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":"role %s holder %s lapsed; claim retained","reason":"holder_lapsed","holder":%q,"holder_state":%q,"last_seen":1,"claim_released":false}`,
			role, l.holder, l.holder, l.state)
	}
}
