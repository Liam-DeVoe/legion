package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"

	"github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/natsgrant"
	"github.com/sjawhar/envoy/testnatsimages"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/natsauth"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/testnats"
	"github.com/sjawhar/legion/daemon/internal/testnkeys"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

const (
	dispatchGrantConsumer = "legion-go-CAPTURE-dispatch"
	githubGrantConsumer   = "legion-go-CAPTURE-github"
	wakeGrantConsumer     = "legion-go-CAPTURE-wake"
	// wakeGrantSubject is intake's legionWakeSubject, the third durable's whole filter, which
	// this package cannot read from there.
	wakeGrantSubject = "notifications.legion.wake"
)

// The deployment and this proof share natsgrant.Daemon: core wraps the same values as the
// NATS server grant, while this test drives the daemon's real connection, durable consumers,
// acknowledgements, and exceptions subscription through it.
func TestDaemonPermissionsOnRealServers(t *testing.T) {
	for _, image := range testnatsimages.Images {
		t.Run(image.Name, func(t *testing.T) {
			t.Run("the daemon restarts, reads its consumers' lag, acknowledges Dispatch, GitHub and the listener's wake, and receives exceptions", func(t *testing.T) {
				grant := natsgrant.Daemon()
				daemonSeed, admin, js, server, uri := startDaemonGrantServer(t, image.Ref, grant)
				defer admin.Close()
				stream := createDaemonNotificationStream(t, js)

				first := connectDaemonGrant(t, uri, daemonSeed, &syncBuffer{})
				for _, name := range []string{dispatchGrantConsumer, githubGrantConsumer, wakeGrantConsumer} {
					if _, err := stream.Consumer(t.Context(), name); err != nil {
						t.Fatalf("the first daemon creates %s: %v", name, err)
					}
				}
				first.closeNATS()

				logs := &syncBuffer{}
				restarted := connectDaemonGrant(t, uri, daemonSeed, logs)
				defer restarted.closeNATS()
				conn := restarted.conn.Load()

				exception := make(chan []byte, 1)
				subscription, err := subscribeNoticeExceptions(conn, func(data []byte) { exception <- append([]byte(nil), data...) })
				if err != nil {
					t.Fatalf("subscribe to role-lane exceptions: %v", err)
				}
				defer subscription.Unsubscribe()
				if err := conn.Flush(); err != nil {
					t.Fatalf("flush the exceptions subscription: %v", err)
				}

				publishDaemonGrantFixture(t, js, "../intake/testdata/dispatch/issue-updated.json")
				publishDaemonGrantFixture(t, js, "../intake/testdata/github/pr-opened.json")
				publishDaemonGrantEnvelope(t, js, wakeGrantSubject, "envoy", "listener-wake-grant",
					fmt.Sprintf(`{"kind":"listener_ready","at":%d}`, time.Now().UnixMilli()))
				// Before the consumers run, each has its one event waiting, so the state's lag read
				// makes every request it can under the grant: each consumer's info and its oldest
				// waiting message's stored-message get.
				for _, lag := range restarted.consumers.Lag(t.Context()) {
					if lag.Waiting != 1 || lag.OldestAt.IsZero() || lag.Unavailable != "" {
						t.Fatalf("lag of %s under the daemon grant = %+v, want its one event waiting with its time and nothing unavailable", lag.Name, lag)
					}
				}

				handler := &failFirstDaemonGrantHandler{}
				stop := runDaemonGrantConsumers(t, restarted, handler)
				defer stop()

				if err := admin.Publish("notifications.envoy.exceptions.notifications.role.example", []byte("retry this notice")); err != nil {
					t.Fatalf("publish the role-lane exception: %v", err)
				}
				if err := admin.Flush(); err != nil {
					t.Fatalf("flush the role-lane exception: %v", err)
				}

				var got []byte
				testwait.Eventually(t, "the daemon receiving the role-lane exception", func() bool {
					select {
					case got = <-exception:
						return true
					default:
						return false
					}
				})
				if string(got) != "retry this notice" {
					t.Fatalf("role-lane exception = %q, want %q", got, "retry this notice")
				}
				testwait.Eventually(t, "the daemon naking one delivery and applying its redelivery with every event type", func() bool {
					return handler.calls.Load() >= 4
				})
				requireDaemonGrantAcknowledged(t, stream, dispatchGrantConsumer, githubGrantConsumer, wakeGrantConsumer)
				if strings.Contains(logs.String(), "NATS refused the daemon a permission") {
					t.Fatalf("the daemon logged a permission refusal:\n%s", logs.String())
				}
				if serverLog := daemonGrantServerLogs(t, server); strings.Contains(serverLog, "Permissions Violation") {
					t.Fatalf("the server logged a permissions violation:\n%s", serverLog)
				}
				t.Log("restarted daemon created the three consumers, read their lag, NAKed then acknowledged every event kind, and received the role-lane exception without a permission refusal")
			})

			t.Run("without the dispatch consumer-create permission, the daemon and server report it", func(t *testing.T) {
				const removed = "$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS.>"
				grant := natsgrant.Daemon()
				held := len(grant.Publish)
				grant.Publish = slices.DeleteFunc(grant.Publish, func(subject string) bool { return subject == removed })
				if len(grant.Publish) != held-1 {
					t.Fatalf("the daemon grant does not contain %s: %v", removed, grant.Publish)
				}

				daemonSeed, admin, js, server, uri := startDaemonGrantServer(t, image.Ref, grant)
				defer admin.Close()
				createDaemonNotificationStream(t, js)

				logs := &syncBuffer{}
				cfg := daemonGrantConfig(uri)
				attempt, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				w := grantWorkflow(t, logs)
				if err := w.connect(attempt, cfg, natsConnection{seed: daemonSeed}); err == nil {
					t.Fatal("the daemon connected without the dispatch consumer-create permission")
				}

				denied := "$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS." + dispatchGrantConsumer
				var daemonLine, serverLine string
				testwait.Eventually(t, "the daemon log naming the refused JetStream API publish", func() bool {
					daemonLine = matchingLogLine(logs.String(), "NATS refused the daemon a permission", denied)
					return daemonLine != ""
				})
				var serverLogErr error
				testwait.Eventually(t, "the server log naming the refused JetStream API publish", func() bool {
					var serverLog string
					serverLog, serverLogErr = readDaemonGrantServerLogs(server)
					serverLine = matchingLogLine(serverLog, "Publish Violation", denied)
					return serverLine != ""
				})
				if serverLogErr != nil {
					t.Fatalf("read the server log: %v", serverLogErr)
				}
				t.Logf("observed daemon refusal: %s", daemonLine)
				t.Logf("observed server refusal: %s", serverLine)
			})

			t.Run("on fresh names the same grant creates, makes again, reads and acknowledges the daemon's fresh consumers, and the daemon sends no delete", func(t *testing.T) {
				grant := natsgrant.Daemon()
				daemonSeed, admin, js, server, uri := startDaemonGrantServer(t, image.Ref, grant)
				defer admin.Close()
				stream := createDaemonNotificationStream(t, js)
				cfg := daemonGrantConfig(uri)
				cfg.ConsumerNames = consumernames.ModeFresh
				logs := &syncBuffer{}

				first := grantWorkflow(t, logs)
				store := writeConvertedSentinel(t, first, cfg.Project)
				if err := first.connect(t.Context(), cfg, natsConnection{seed: daemonSeed}); err != nil {
					t.Fatalf("connect the daemon on fresh names: %v", err)
				}
				current := func(logical string) string {
					t.Helper()
					rec, _, err := store.Read(t.Context(), logical)
					if err != nil || !strings.HasPrefix(rec.Current, logical+"~") {
						t.Fatalf("the selection record of %s = %+v, %v; want a fresh name of it selected", logical, rec, err)
					}
					return rec.Current
				}
				gone := current(githubGrantConsumer)
				first.closeNATS()
				// Deleted outside the daemon, by the operator's credential: the next boot makes it
				// again under another fresh name, with the daemon's own grant.
				if err := stream.DeleteConsumer(t.Context(), gone); err != nil {
					t.Fatalf("delete %s as the operator: %v", gone, err)
				}

				restarted := grantWorkflow(t, logs)
				restarted.pool = first.pool
				if err := restarted.connect(t.Context(), cfg, natsConnection{seed: daemonSeed}); err != nil {
					t.Fatalf("connect the restarted daemon on fresh names: %v", err)
				}
				defer restarted.closeNATS()
				currents := []string{current(dispatchGrantConsumer), current(githubGrantConsumer), current(wakeGrantConsumer)}
				if currents[1] == gone {
					t.Fatalf("the restarted daemon selected %s, the consumer deleted outside it, want another fresh name", gone)
				}
				for _, name := range []string{dispatchGrantConsumer, githubGrantConsumer, wakeGrantConsumer} {
					if _, err := stream.Consumer(t.Context(), name); !errors.Is(err, jetstream.ErrConsumerNotFound) {
						t.Fatalf("the fixed name %s on fresh names: %v, want no such consumer", name, err)
					}
				}

				publishDaemonGrantFixture(t, js, "../intake/testdata/dispatch/issue-updated.json")
				publishDaemonGrantFixture(t, js, "../intake/testdata/github/pr-opened.json")
				publishDaemonGrantEnvelope(t, js, wakeGrantSubject, "envoy", "listener-wake-fresh-grant",
					fmt.Sprintf(`{"kind":"listener_ready","at":%d}`, time.Now().UnixMilli()))
				for _, lag := range restarted.consumers.Lag(t.Context()) {
					if lag.Waiting != 1 || lag.OldestAt.IsZero() || lag.Unavailable != "" {
						t.Fatalf("lag of %s on fresh names under the daemon grant = %+v, want its one event waiting with its time and nothing unavailable", lag.Name, lag)
					}
				}
				handler := &failFirstDaemonGrantHandler{}
				stop := runDaemonGrantConsumers(t, restarted, handler)
				defer stop()
				testwait.Eventually(t, "the daemon applying every event kind on fresh names", func() bool { return handler.calls.Load() >= 4 })
				requireDaemonGrantAcknowledged(t, stream, currents...)
				if strings.Contains(logs.String(), "NATS refused the daemon a permission") {
					t.Fatalf("the daemon logged a permission refusal on fresh names:\n%s", logs.String())
				}
				serverLog := daemonGrantServerLogs(t, server)
				if strings.Contains(serverLog, "Permissions Violation") {
					t.Fatalf("the server logged a permissions violation on fresh names:\n%s", serverLog)
				}
				t.Logf("two boots on fresh names created %v and made %s again as %s under the daemon grant, with no refusal", currents, gone, currents[1])
			})

			t.Run("on fresh names without the consumer-create permission, the daemon and server report the generation's create", func(t *testing.T) {
				const removed = "$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS.>"
				grant := natsgrant.Daemon()
				held := len(grant.Publish)
				grant.Publish = slices.DeleteFunc(grant.Publish, func(subject string) bool { return subject == removed })
				if len(grant.Publish) != held-1 {
					t.Fatalf("the daemon grant does not contain %s: %v", removed, grant.Publish)
				}
				daemonSeed, admin, js, server, uri := startDaemonGrantServer(t, image.Ref, grant)
				defer admin.Close()
				createDaemonNotificationStream(t, js)
				cfg := daemonGrantConfig(uri)
				cfg.ConsumerNames = consumernames.ModeFresh
				logs := &syncBuffer{}
				w := grantWorkflow(t, logs)
				writeConvertedSentinel(t, w, cfg.Project)
				attempt, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				if err := w.connect(attempt, cfg, natsConnection{seed: daemonSeed}); err == nil {
					w.closeNATS()
					t.Fatal("the daemon connected on fresh names without the consumer-create permission")
				}
				denied := "$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS." + dispatchGrantConsumer + "~"
				var daemonLine, serverLine string
				testwait.Eventually(t, "the daemon log naming the refused create of a fresh name", func() bool {
					daemonLine = matchingLogLine(logs.String(), "NATS refused the daemon a permission", denied)
					return daemonLine != ""
				})
				testwait.Eventually(t, "the server log naming the refused create of a fresh name", func() bool {
					serverLog, err := readDaemonGrantServerLogs(server)
					serverLine = matchingLogLine(serverLog, "Publish Violation", denied)
					return err == nil && serverLine != ""
				})
				t.Logf("observed daemon refusal: %s", daemonLine)
				t.Logf("observed server refusal: %s", serverLine)
			})
		})
	}
}

// Every NATS connection a fresh daemon opens goes through its one dial (workflowRuntime.dial), and
// so dials as its own connection does: the dial hears the daemon's own connection and one
// generation of each of its three durables, named <logical>/<generation>; the server admits only
// the daemon's nkey user and the admin, so a generation's connection that presented another
// credential, or none, would end the boot; and when the server stops, each of the four connections
// logs its loss through the daemon's own events (natsauth.LogEvents). The control is round 2's
// generation dial, written apart from the daemon's with the same arguments: the dial then hears the
// daemon's own connection alone, and the row fails naming the generations it did not hear.
func TestEveryConnectionOfAFreshDaemonDialsAsItsOwn(t *testing.T) {
	daemonSeed, admin, js, server, uri := startDaemonGrantServer(t, testnatsimages.Images[0].Ref, natsgrant.Daemon())
	defer admin.Close()
	createDaemonNotificationStream(t, js)
	cfg := daemonGrantConfig(uri)
	cfg.ConsumerNames = consumernames.ModeFresh
	logs := &syncBuffer{}
	w := grantWorkflow(t, logs)
	var heardMu sync.Mutex
	var heard []string
	w.dialed = func(name string) {
		heardMu.Lock()
		defer heardMu.Unlock()
		heard = append(heard, name)
	}
	writeConvertedSentinel(t, w, cfg.Project)
	if err := w.connect(t.Context(), cfg, natsConnection{seed: daemonSeed}); err != nil {
		t.Fatalf("connect the daemon on fresh names, on a server that admits only its own user: %v", err)
	}
	defer w.closeNATS()
	heardMu.Lock()
	dialled := slices.Clone(heard)
	heardMu.Unlock()
	if !slices.Contains(dialled, "legion-daemon "+cfg.Project) {
		t.Fatalf("the one dial heard %v, want the daemon's own connection among them", dialled)
	}
	for _, logical := range []string{dispatchGrantConsumer, githubGrantConsumer, wakeGrantConsumer} {
		if !slices.ContainsFunc(dialled, func(name string) bool { return strings.HasPrefix(name, logical+"/") }) {
			t.Errorf("the one dial heard %v, want a generation connection of %s: it was dialled apart from the daemon's dial", dialled, logical)
		}
	}
	admin.Close()
	stop := 10 * time.Second
	if err := server.Stop(t.Context(), &stop); err != nil {
		t.Fatalf("stop the server: %v", err)
	}
	const lost, want = "NATS connection lost", 4
	testwait.Eventually(t, fmt.Sprintf("%d connections logging their loss through the daemon's events", want), func() bool {
		return strings.Count(logs.String(), lost) >= want
	})
	if got := strings.Count(logs.String(), lost); got != want {
		t.Fatalf("%d connections logged %q, want %d: the daemon's own and one generation of each durable:\n%s", got, lost, want, logs.String())
	}
}

// Each fresh generation's connection is watched for a reconnect as the daemon's own is
// (reconnectWatchdogWith): one that stays disconnected warns "NATS has not reconnected" every
// reconnectWarnEvery, naming the connection, until it reconnects or closes. Its durable reads
// nothing meanwhile, and nats.go logs its loss once. The daemon's own connection's watchdog runs
// beside Run and is not started here, so each warn the row reads is a generation's. The control is
// the code before this row, which watched the daemon's own connection alone: no generation warned.
func TestEachFreshGenerationsConnectionIsWatchedForItsReconnect(t *testing.T) {
	daemonSeed, admin, js, server, uri := startDaemonGrantServer(t, testnatsimages.Images[0].Ref, natsgrant.Daemon())
	defer admin.Close()
	createDaemonNotificationStream(t, js)
	cfg := daemonGrantConfig(uri)
	cfg.ConsumerNames = consumernames.ModeFresh
	logs := &syncBuffer{}
	w := grantWorkflow(t, logs)
	w.reconnectPollInterval, w.reconnectWarnEvery = 50*time.Millisecond, 500*time.Millisecond
	writeConvertedSentinel(t, w, cfg.Project)
	if err := w.connect(t.Context(), cfg, natsConnection{seed: daemonSeed}); err != nil {
		t.Fatalf("connect the daemon on fresh names: %v", err)
	}
	defer w.closeNATS()
	admin.Close()
	stop := 10 * time.Second
	if err := server.Stop(t.Context(), &stop); err != nil {
		t.Fatalf("stop the server: %v", err)
	}
	logicals := []string{dispatchGrantConsumer, githubGrantConsumer, wakeGrantConsumer}
	testwait.Eventually(t, "a reconnect warn from each generation's connection", func() bool {
		logged := logs.String()
		for _, logical := range logicals {
			if !strings.Contains(logged, `msg="NATS has not reconnected" connection=`+logical+"/") {
				return false
			}
		}
		return true
	})
}

func grantLogger(logs *syncBuffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(logs, nil))
}

func daemonGrantConfig(uri string) config.Config {
	const project = "CAPTURE"
	return config.Config{
		Project: project, NatsURLs: []string{uri}, WorkerRPCTimeout: time.Second,
		Projects: map[string]config.Project{project: {}},
	}
}

// grantWorkflow is the workflow the grant tests connect: a record of its project names
// sjawhar/legion, the repository the fixtures' events name, so GitHub intake hears it from the
// boot's read of the records (heardAtBoot), and a stand-in GitHub names it, as preparing the
// Dispatch fixture's todo issue reads it.
func grantWorkflow(t *testing.T, logs *syncBuffer) *workflowRuntime {
	t.Helper()
	repos := newRepositories("CAPTURE", ghrepo.Renames{})
	repos.fill(ghrepo.MustParse("sjawhar/legion"))
	github := newNamedGitHub(t, map[string]string{"sjawhar/legion": "sjawhar/legion@main"})
	w := &workflowRuntime{pool: isolatedOutboxPool(t), records: projectRecords{Store: record.NewStore(), project: "CAPTURE"},
		repositories: repos, defaultBranches: newDefaultBranches(roleTokens{}, repos, github.url, defaultBranchTTL, time.Now), log: grantLogger(logs)}
	if err := pgx.BeginFunc(t.Context(), w.pool, func(tx pgx.Tx) error {
		return w.records.PutIssue(t.Context(), tx, record.Issue{Key: "CAPTURE-90", Tree: "CAPTURE-90", Project: "CAPTURE", Title: "a tree on sjawhar/legion",
			Phase: phase.Implementing, Generation: 1, Status: "in_progress", Rank: "A", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion"})
	}); err != nil {
		t.Fatalf("record the tree whose repository GitHub intake hears: %v", err)
	}
	return w
}

// writeConvertedSentinel writes project's sentinel in w's database as the operator's convert
// leaves it: the cutover done, with the stream identity convert records. It returns the project's
// selection store, which a test reads the records of.
func writeConvertedSentinel(t *testing.T, w *workflowRuntime, project string) *consumernames.PGStore {
	t.Helper()
	family, err := consumernames.DaemonFamily(project)
	if err != nil {
		t.Fatal(err)
	}
	store, err := consumernames.NewPGStore(family, w.pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteSentinel(t.Context(), consumernames.StoreState{Family: family, Cutover: consumernames.CutoverDone, StreamID: "notifications-as-convert-recorded-it"}, 0); err != nil {
		t.Fatalf("write the sentinel as the operator's convert does: %v", err)
	}
	return store
}

func connectDaemonGrant(t *testing.T, uri, seed string, logs *syncBuffer) *workflowRuntime {
	t.Helper()
	cfg := daemonGrantConfig(uri)
	w := grantWorkflow(t, logs)
	if err := w.connect(t.Context(), cfg, natsConnection{seed: seed}); err != nil {
		t.Fatalf("connect the daemon: %v", err)
	}
	return w
}

func runDaemonGrantConsumers(t *testing.T, w *workflowRuntime, handlers ...intake.Handler) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.consumers.Run(ctx, handlers...) }()
	return func() {
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("run the daemon consumers: %v", err)
		}
	}
}

type failFirstDaemonGrantHandler struct{ calls atomic.Int32 }

func (h *failFirstDaemonGrantHandler) Apply(context.Context, pgx.Tx, intake.Fact) (intake.Result, error) {
	if h.calls.Add(1) == 1 {
		return intake.Result{}, errors.New("exercise the daemon consumer's NAK path")
	}
	return intake.Result{}, nil
}

func publishDaemonGrantFixture(t *testing.T, js jetstream.JetStream, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var envelope struct {
		Topic string `json:"topic"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if _, err := js.Publish(t.Context(), envelope.Topic, data); err != nil {
		t.Fatalf("publish %s: %v", path, err)
	}
}

// publishDaemonGrantEnvelope publishes an Envoy envelope from source on subject, eventID its id
// and payload its payload: the listener's wake report, which no captured fixture carries. Each
// subject's decoder refuses an envelope from another source, so the caller names the one its
// subject takes.
func publishDaemonGrantEnvelope(t *testing.T, js jetstream.JetStream, subject, source, eventID, payload string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"event_id": eventID, "source": source, "source_event_id": eventID + "-source", "topic": subject,
		"dedupe_key": eventID, "issued_at": 1, "payload_summary": eventID, "payload": payload, "trace_id": eventID + "-trace",
	})
	if err != nil {
		t.Fatalf("encode the %s envelope: %v", subject, err)
	}
	if _, err := js.Publish(t.Context(), subject, data); err != nil {
		t.Fatalf("publish %s: %v", subject, err)
	}
}

func requireDaemonGrantAcknowledged(t *testing.T, stream jetstream.Stream, consumers ...string) {
	t.Helper()
	testwait.Eventually(t, "the daemon acknowledging every retained event type", func() bool {
		for _, name := range consumers {
			consumer, err := stream.Consumer(t.Context(), name)
			if err != nil {
				return false
			}
			info, err := consumer.Info(t.Context())
			if err != nil || info.NumPending != 0 || info.NumAckPending != 0 || info.Delivered.Consumer == 0 {
				return false
			}
		}
		return true
	})
}

func createDaemonNotificationStream(t *testing.T, js jetstream.JetStream) jetstream.Stream {
	t.Helper()
	stream, err := js.CreateStream(t.Context(), jetstream.StreamConfig{Name: "ENVOY_NOTIFICATIONS", Subjects: []string{"notifications.>"}})
	if err != nil {
		t.Fatalf("create the notification stream: %v", err)
	}
	return stream
}

func startDaemonGrantServer(t *testing.T, image string, grant natsgrant.Permissions) (string, *nats.Conn, jetstream.JetStream, *tcnats.NATSContainer, string) {
	t.Helper()
	daemonSeed, daemonPublic := testnkeys.User(t)
	adminSeed, adminPublic := testnkeys.User(t)
	// The server's configuration reads JSON, but not Go's HTML escaping of '>' as \u003e.
	var permissions strings.Builder
	encoder := json.NewEncoder(&permissions)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(map[string]map[string][]string{
		"publish":   {"allow": grant.Publish},
		"subscribe": {"allow": grant.Subscribe},
	}); err != nil {
		t.Fatalf("encode the daemon permissions: %v", err)
	}
	server, uri := testnats.StartNkeyAuthorizedUsers(t, image,
		testnats.NkeyUser{Public: daemonPublic, Permissions: strings.TrimSpace(permissions.String())},
		testnats.NkeyUser{Public: adminPublic})
	admin, err := natsauth.Connect([]string{uri}, adminSeed, t.Name()+" admin", nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect the admin user: %v", err)
	}
	js, err := jetstream.New(admin)
	if err != nil {
		t.Fatalf("open JetStream as the admin user: %v", err)
	}
	return daemonSeed, admin, js, server, uri
}

func daemonGrantServerLogs(t *testing.T, server *tcnats.NATSContainer) string {
	t.Helper()
	text, err := readDaemonGrantServerLogs(server)
	if err != nil {
		t.Fatalf("read the server log: %v", err)
	}
	return text
}

func readDaemonGrantServerLogs(server *tcnats.NATSContainer) (string, error) {
	logs, err := server.Logs(context.Background())
	if err != nil {
		return "", err
	}
	defer logs.Close()
	text, err := io.ReadAll(logs)
	return string(text), err
}

func matchingLogLine(text string, terms ...string) string {
	for _, line := range strings.Split(text, "\n") {
		if slices.ContainsFunc(terms, func(term string) bool { return !strings.Contains(line, term) }) {
			continue
		}
		return line
	}
	return ""
}
