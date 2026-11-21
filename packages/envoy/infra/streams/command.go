package streams

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/bus/signedrecord"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/internal/streamset"
	"github.com/sjawhar/envoy/natscred"
	"github.com/sjawhar/envoy/natsgrant"
)

// measureInbox is the inbox prefix the measurement job's grant subscribes (natsgrant.Measure).
const measureInbox = "_INBOX_envoy_measure"

// commandUsage is `envoy-infra streams`' usage.
const commandUsage = `usage:
  envoy-infra streams check  --nats-url <urls> (--token-file <file> | --nkey-seed-file <file>) [settings]
  envoy-infra streams ensure --nats-url <urls> --token-file <file> --broker-url <url> --hold-verify-keys <keys> [settings]
  envoy-infra streams update --nats-url <urls> --token-file <file> --broker-url <url> --hold-verify-keys <keys> [settings]
  envoy-infra streams census --nats-url <urls> --token-file <file> --broker-url <url> --hold-verify-keys <keys>
                             --hold-id <id> --family <family>... [--listener-machine-id <id>...]
                             [--daemon-consumer <name>=<filter>[,<filter>...]...] [settings]

check runs as the measurement job, ensure, update and census as the operator: the token file holds
the platform token the hub's auth service admits the identity by (user service), and the operator's
token is also its token at the broker's /v1/nats-hold routes, which ensure (released), update and
census (held) read the hold's phase from first. A static hub's measurement user connects by its
nkey seed instead. Exit: 0 clean, 1 a failure or a refusal, 2 UNKNOWN or a usage error.
settings: --replicas (ENVOY_NATS_REPLICAS, default 1), --stream-max-bytes (ENVOY_STREAM_MAX_BYTES),
          --session-max-age (ENVOY_SESSION_MAX_AGE)`

// Command runs `envoy-infra streams <word>` with args, the words after streams, and returns its exit
// code: the report's (Report.ExitCode), 1 for a refused phase or an unconverted census, and 2 for a
// usage error.
func Command(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "-help" {
		fmt.Fprintln(stderr, commandUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	word := args[0]
	flags := flag.NewFlagSet("envoy-infra streams "+word, flag.ContinueOnError)
	flags.SetOutput(stderr)
	natsURLs := flags.String("nats-url", "", "the hub's NATS URLs, comma-separated")
	tokenFile := flags.String("token-file", "", "the platform token the hub's auth service admits this identity by")
	seedFile := flags.String("nkey-seed-file", "", "a static hub's measurement user's nkey seed (check alone)")
	brokerURL := flags.String("broker-url", "", "the broker whose /v1/nats-hold routes hold the hold's row")
	verifyKeys := flags.String("hold-verify-keys", "", "the operator's hold signing public keys, as BROKER_NATS_HOLD_SIGNING_KEYS writes them")
	holdID := flags.String("hold-id", "", "the hold the census runs in")
	var families, machineIDs, daemons listFlag
	flags.Var(&families, "family", "a cutover family the declared set names (session, dispatch, daemon/<project key>); repeat")
	flags.Var(&machineIDs, "listener-machine-id", "a machine id of the deployment's listeners; repeat")
	flags.Var(&daemons, "daemon-consumer", "a Legion daemon's consumer, <name>=<filter>[,<filter>...]; repeat")
	replicas := flags.String("replicas", os.Getenv(bus.ReplicasEnvVar), "the deployment's replica count")
	maxBytes := flags.String("stream-max-bytes", os.Getenv(bus.StreamMaxBytesEnvVar), "the notification stream's byte limit")
	maxAge := flags.String("session-max-age", os.Getenv(bus.SessionMaxAgeEnvVar), "the session bucket's age")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	usage := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "envoy-infra streams %s: "+format+"\n", append([]any{word}, a...)...)
		fmt.Fprintln(stderr, commandUsage)
		return 2
	}
	switch word {
	case "check", "ensure", "update", "census":
	default:
		return usage("the word is check, ensure, update or census")
	}
	settings, err := readSettings(*replicas, *maxBytes, *maxAge)
	if err != nil {
		return usage("%v", err)
	}
	if *natsURLs == "" {
		return usage("--nats-url is required")
	}
	operator := word != "check"
	switch {
	case *tokenFile == "" && *seedFile == "":
		return usage("--token-file or --nkey-seed-file is required")
	case *tokenFile != "" && *seedFile != "":
		return usage("name --token-file or --nkey-seed-file, not both")
	case operator && *seedFile != "":
		return usage("the operator connects by its token (--token-file), never a seed")
	case operator && (*brokerURL == "" || *verifyKeys == ""):
		return usage("--broker-url and --hold-verify-keys are required")
	case word == "census" && (*holdID == "" || len(families) == 0):
		return usage("--hold-id and at least one --family are required")
	}
	inbox := measureInbox
	if operator {
		inbox = natsgrant.OperatorInbox
	}
	conn, err := connect(*natsURLs, *tokenFile, *seedFile, inbox, "envoy-infra streams "+word)
	if err != nil {
		fmt.Fprintf(stderr, "envoy-infra streams %s: connect: %v\n", word, err)
		return 2
	}
	defer conn.Close()

	var report Report
	if !operator {
		report = Check(ctx, conn, settings)
		return printReport(stdout, report)
	}
	keys, err := signedrecord.ParsePublicKeys("--hold-verify-keys", *verifyKeys)
	if err != nil {
		return usage("%v", err)
	}
	client, err := holdClient(*brokerURL, *tokenFile, keys)
	if err != nil {
		return usage("%v", err)
	}
	rowPhase := func(ctx context.Context) (natsgrant.OperatorPhase, error) {
		row, err := client.Row(ctx)
		return row.Phase, err
	}
	switch word {
	case "ensure":
		report, err = Ensure(ctx, conn, settings, rowPhase)
	case "update":
		report, err = Update(ctx, conn, settings, rowPhase)
	case "census":
		shapes, shapeErr := daemonShapes(daemons)
		if shapeErr != nil {
			return usage("%v", shapeErr)
		}
		js, jsErr := jetstream.New(conn)
		if jsErr != nil {
			fmt.Fprintf(stderr, "envoy-infra streams census: %v\n", jsErr)
			return 2
		}
		report, err = Census(ctx, conn, CensusInput{
			Settings: settings,
			Phase:    rowPhase,
			Families: families,
			Cutovers: func(ctx context.Context) (map[string]string, error) {
				rows, err := client.Cutovers(ctx)
				if err != nil {
					return nil, err
				}
				states := make(map[string]string, len(rows))
				for _, row := range rows {
					states[row.Family] = row.State
				}
				return states, nil
			},
			Barrier: func(ctx context.Context, bucket string) error {
				return holdrecord.Barrier(ctx, js, bucket, *holdID)
			},
			Consumers:                DeclaredConsumers(ConsumerSet{ListenerMachineIDs: machineIDs, Daemons: shapes}),
			RecordedSessionConsumers: unreadRecords,
		})
	}
	if err != nil {
		fmt.Fprintf(stderr, "envoy-infra %v\n", err)
		return 1
	}
	return printReport(stdout, report)
}

// ErrRecordsUnread is the census's read of the session consumers KV_envoy_session_consumers
// records, which this build cannot make: the records are written by the writer hold's consumer-names step,
// whose record format no package of this build reads yet. A census run from this command is
// therefore UNKNOWN, as it must be rather than remove the session consumers it cannot tell from
// planted ones; the writer hold's workflow calls Census with its own reader.
var ErrRecordsUnread = errors.New("this build reads no session consumer records: the writer hold's consumer-names step owns their format")

func unreadRecords(context.Context) ([]ConsumerShape, error) { return nil, ErrRecordsUnread }

// readSettings reads the deployment's settings as each program reads them (bus.ParseNATSReplicas,
// bus.ParseStreamMaxBytes, bus.ParseSessionMaxAge).
func readSettings(replicas, maxBytes, maxAge string) (streamset.Settings, error) {
	var settings streamset.Settings
	var err error
	if settings.Replicas, err = bus.ParseNATSReplicas(replicas); err != nil {
		return settings, err
	}
	if settings.StreamMaxBytes, err = bus.ParseStreamMaxBytes(maxBytes); err != nil {
		return settings, err
	}
	if settings.SessionMaxAge, err = bus.ParseSessionMaxAge(maxAge); err != nil {
		return settings, err
	}
	return settings, nil
}

// connect connects to the hub at urls as the token file's identity (user service, the token as the
// password), or as the seed file's nkey user, asking on inbox.
func connect(urls, tokenFile, seedFile, inbox, name string) (*nats.Conn, error) {
	options := []nats.Option{nats.Name(name), nats.CustomInboxPrefix(inbox), nats.Timeout(10 * time.Second), nats.MaxReconnects(0)}
	if seedFile != "" {
		credential, err := nats.NkeyOptionFromSeed(seedFile)
		if err != nil {
			return nil, err
		}
		options = append(options, credential)
	} else {
		token, err := readToken(tokenFile)
		if err != nil {
			return nil, err
		}
		options = append(options, nats.UserInfo(natscred.ServiceUser, token))
	}
	return nats.Connect(urls, options...)
}

// readToken is the token path holds, trimmed; an empty file is refused naming the path.
func readToken(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("%s holds no token", path)
	}
	return token, nil
}

// holdClient is the operator's client of the broker's /v1/nats-hold routes, presenting the token
// the file holds, read again at each call, and verifying every answer against keys. It reads alone:
// no command here writes the hold, so it holds no signing key.
func holdClient(brokerURL, tokenFile string, keys []ed25519.PublicKey) (*holdrecord.Client, error) {
	return holdrecord.New(holdrecord.Config{
		BrokerURL:  brokerURL,
		Token:      func(context.Context) (string, error) { return readToken(tokenFile) },
		VerifyKeys: keys,
	})
}

// daemonShapes reads each --daemon-consumer, <name>=<filter>[,<filter>...], as a Legion daemon's
// pull consumer of the notification stream, acknowledged one message at a time.
func daemonShapes(entries []string) ([]ConsumerShape, error) {
	var shapes []ConsumerShape
	for _, entry := range entries {
		name, filters, ok := strings.Cut(entry, "=")
		if !ok || name == "" || filters == "" {
			return nil, fmt.Errorf("--daemon-consumer %q is not <name>=<filter>[,<filter>...]", entry)
		}
		shapes = append(shapes, ConsumerShape{
			Stream: bus.Stream, Name: name, FilterSubjects: strings.Split(filters, ","), AckPolicy: nats.AckExplicitPolicy,
		})
	}
	return shapes, nil
}

// printReport prints report's lines and returns its exit code.
func printReport(stdout io.Writer, report Report) int {
	for _, line := range report.Lines() {
		fmt.Fprintln(stdout, line)
	}
	return report.ExitCode()
}

// listFlag is a flag given more than once, each value kept in order.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }
