package consumernames

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/sjawhar/envoy/infra/internal/flagerr"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/holdrecord"
)

// The words of `envoy-infra consumer-names`.
const (
	actionStatus       = "status"
	actionPrepare      = "prepare"
	actionCreateBucket = "create-bucket"
	actionReplay       = "replay"
	actionConvert      = "convert"
	actionCleanup      = "cleanup"
	actionRestore      = "restore"
	actionReplace      = "replace"
	actionEnroll       = "enroll"
)

// actions are the words, in the usage's order.
var actions = []string{actionStatus, actionPrepare, actionCreateBucket, actionReplay, actionConvert, actionCleanup,
	actionRestore, actionReplace, actionEnroll}

const usage = `usage: envoy-infra consumer-names status|prepare|create-bucket|replay|convert|cleanup|restore|replace|enroll [options]

  status         [--hold ID]                               (the row released)
  prepare        --hold ID                                 (the row prepare)
  create-bucket  --hold ID [--replicas N]                  (the row held, and every command below)
  create-bucket  --hold ID --after-loss [--replay-from SEQ] [--backup DIR] [--replicas N]
  replay         --hold ID --key K --from SEQ [--final]
  convert        --hold ID
  cleanup        --hold ID
  restore        --hold ID (--prepare | --finish | --key K --from SEQ)
  replace        --hold ID --listener MACHINE-ID
  enroll         --hold ID --daemon PROJECT-KEY

  Every command: --broker-url U (the secrets broker), --token-file F (the operator's
  ServiceAccount token, for the broker's routes and the hub), --verify-keys K
  (BROKER_NATS_HOLD_SIGNING_KEYS) and --nats URL..., the hub's client URLs. Every command but
  status: --signing-key-secret projects/P/secrets/operator-only-envoy-hold-signing. Dispatch's
  store: --dispatch-database-file F, a file holding its database URL with the operator's
  credential; each Legion daemon's: --daemon-database PROJECT-KEY=F, once per daemon.

Exit codes: 0 done; 1 refused, naming what; 2 a usage error, or UNKNOWN: a reading that is not
whole, which is never a pass.
`

// stringList is a repeatable flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// options are a run's flags.
type options struct {
	action                                      string
	brokerURL, tokenFile, verifyKeys, keySecret string
	nats                                        stringList
	holdID                                      string
	dispatchDatabaseFile                        string
	daemonDatabases                             stringList
	replicas                                    int
	afterLoss                                   bool
	replayFrom                                  uint64
	backup                                      string
	key                                         string
	from                                        uint64
	final                                       bool
	prepare, finish                             bool
	listener                                    string
	daemon                                      string
}

// daemonDatabase is the database file --daemon-database names for project, and whether it names
// one.
func (o options) daemonDatabase(project string) (string, bool) {
	for _, pair := range o.daemonDatabases {
		if p, file, _ := strings.Cut(pair, "="); p == project {
			return file, true
		}
	}
	return "", false
}

// daemonProjects are the projects --daemon-database names, in its order.
func (o options) daemonProjects() []string {
	var out []string
	for _, pair := range o.daemonDatabases {
		p, _, _ := strings.Cut(pair, "=")
		out = append(out, p)
	}
	return out
}

// Command runs `envoy-infra consumer-names` with args, the words after "consumer-names", and
// returns its exit code.
func Command(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runCommand(ctx, args, stdout, stderr, realEnv())
}

func runCommand(ctx context.Context, args []string, stdout, stderr io.Writer, e env) int {
	if len(args) == 0 || !slices.Contains(actions, args[0]) {
		fmt.Fprint(stderr, usage)
		if len(args) > 0 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help") {
			return 0
		}
		return 2
	}
	o, code := parse(args, stderr)
	if code >= 0 {
		return code
	}
	r := &run{o: o, e: e, stdout: stdout, stderr: stderr}
	defer r.close()
	return report(stderr, o.action, r.dispatch(ctx))
}

// parse reads args into options, or answers the exit code of a usage error (or of --help).
func parse(args []string, stderr io.Writer) (options, int) {
	o := options{action: args[0]}
	flags := flag.NewFlagSet("envoy-infra consumer-names "+o.action, flag.ContinueOnError)
	var parsed bytes.Buffer
	flags.SetOutput(&parsed)
	flags.StringVar(&o.brokerURL, "broker-url", "", "the secrets broker's base URL")
	flags.StringVar(&o.tokenFile, "token-file", "", "the operator's ServiceAccount token")
	flags.StringVar(&o.verifyKeys, "verify-keys", "", "BROKER_NATS_HOLD_SIGNING_KEYS")
	flags.StringVar(&o.keySecret, "signing-key-secret", "", "projects/<project>/secrets/operator-only-envoy-hold-signing")
	flags.Var(&o.nats, "nats", "a hub client URL")
	flags.StringVar(&o.holdID, "hold", "", "the hold's id")
	flags.StringVar(&o.dispatchDatabaseFile, "dispatch-database-file", "", "a file holding Dispatch's database URL")
	flags.Var(&o.daemonDatabases, "daemon-database", "a Legion daemon's project key and a file holding its database URL, <project>=<file>")
	flags.IntVar(&o.replicas, "replicas", 0, "the selection bucket's copies (default ENVOY_NATS_REPLICAS, else 1)")
	flags.BoolVar(&o.afterLoss, "after-loss", false, "create the selection bucket again after it was lost")
	flags.Uint64Var(&o.replayFrom, "replay-from", 0, "where a key with no record starts after a loss (default the stream's first sequence)")
	flags.StringVar(&o.backup, "backup", "", "a directory holding a backup of KV_"+bus.SessionConsumersBucket+" to restore first")
	flags.StringVar(&o.key, "key", "", "a logical name")
	flags.Uint64Var(&o.from, "from", 0, "the stream sequence the logical name starts at")
	flags.BoolVar(&o.final, "final", false, "replay's last entry of a selection recovery")
	flags.BoolVar(&o.prepare, "prepare", false, "restore's first step")
	flags.BoolVar(&o.finish, "finish", false, "restore's last step")
	flags.StringVar(&o.listener, "listener", "", "the listener's machine id")
	flags.StringVar(&o.daemon, "daemon", "", "the Legion daemon's project key")
	if err := flags.Parse(args[1:]); err != nil {
		switch {
		case errors.Is(err, flag.ErrHelp):
			fmt.Fprint(stderr, parsed.String())
			return o, 0
		case strings.HasPrefix(err.Error(), "flag needs an argument: -"):
			fmt.Fprintf(stderr, "envoy-infra consumer-names: %v\n%s", err, usage)
		default:
			if name := flagerr.Malformed(flags, err); name != "" {
				fmt.Fprintf(stderr, "envoy-infra consumer-names: --%s: a malformed value (not echoed)\n%s", name, usage)
			} else {
				fmt.Fprintf(stderr, "envoy-infra consumer-names: a flag this command does not take, or a malformed one (not echoed)\n%s", usage)
			}
		}
		return o, 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "envoy-infra consumer-names: word %d after \"consumer-names\" is no flag and no flag's value (not echoed)\n%s", len(args)-flags.NArg()+1, usage)
		return o, 2
	}
	// A flag's value reaches the lines the command writes and the hold's journal, so a value that
	// holds an nkey seed is refused naming the flag, never the value, as seed, buckets and hold
	// refuse one.
	var seeded []string
	flags.Visit(func(set *flag.Flag) {
		if bus.HoldsSeed(set.Value.String()) {
			seeded = append(seeded, "--"+set.Name)
		}
	})
	if len(seeded) > 0 {
		fmt.Fprintf(stderr, "envoy-infra consumer-names: %s: an nkey seed, which is never an argument (not echoed)\n", strings.Join(seeded, ", "))
		return o, 2
	}
	if problem := o.usageProblem(); problem != "" {
		fmt.Fprintf(stderr, "envoy-infra consumer-names %s: %s\n%s", o.action, problem, usage)
		return o, 2
	}
	return o, -1
}

// usageProblem is what the flags of a run lack or misuse for its action, or "".
func (o options) usageProblem() string {
	var problems []string
	add := func(p string) {
		if p != "" {
			problems = append(problems, p)
		}
	}
	need := func(flagName string, set bool) {
		if !set {
			add("--" + flagName + " is required")
		}
	}
	need("broker-url", o.brokerURL != "")
	need("token-file", o.tokenFile != "")
	need("verify-keys", o.verifyKeys != "")
	need("nats", len(o.nats) > 0)
	if o.action != actionStatus {
		need("signing-key-secret", o.keySecret != "")
		need("hold", o.holdID != "")
	}
	only := func(action, flagName string, set bool) {
		if set && o.action != action {
			add("--" + flagName + " is " + action + "'s alone")
		}
	}
	only(actionCreateBucket, "after-loss", o.afterLoss)
	only(actionCreateBucket, "backup", o.backup != "")
	only(actionCreateBucket, "replicas", o.replicas != 0)
	only(actionReplace, "listener", o.listener != "")
	only(actionEnroll, "daemon", o.daemon != "")
	only(actionReplay, "final", o.final)
	if o.action != actionReplay && o.action != actionRestore {
		if o.key != "" || o.from != 0 {
			add("--key and --from are replay's and restore's alone")
		}
	}
	if o.action != actionRestore && (o.prepare || o.finish) {
		add("--prepare and --finish are restore's alone")
	}
	if o.replayFrom != 0 && !o.afterLoss {
		add("--replay-from is create-bucket --after-loss's alone")
	}
	if o.backup != "" && !o.afterLoss {
		add("--backup is create-bucket --after-loss's alone")
	}
	if o.replicas < 0 || o.replicas > 5 {
		add("--replicas is 1 to 5")
	}
	switch o.action {
	case actionReplay:
		need("key", o.key != "")
		need("from", o.from != 0)
	case actionRestore:
		modes := 0
		for _, set := range []bool{o.prepare, o.finish, o.key != ""} {
			if set {
				modes++
			}
		}
		if modes != 1 {
			add("exactly one of --prepare, --finish and --key is required")
		}
		if (o.key != "") != (o.from != 0) {
			add("--key and --from go together")
		}
	case actionReplace:
		need("listener", o.listener != "")
	case actionEnroll:
		need("daemon", o.daemon != "")
		if o.daemon != "" {
			if _, ok := o.daemonDatabase(o.daemon); !ok {
				add("--daemon-database " + o.daemon + "=<file> is required: enroll writes that store's marker")
			}
		}
	}
	seen := map[string]bool{}
	for _, pair := range o.daemonDatabases {
		project, file, ok := strings.Cut(pair, "=")
		switch {
		case !ok || project == "" || file == "":
			add(fmt.Sprintf("--daemon-database %q is not <project>=<file>", pair))
		case seen[project]:
			add("--daemon-database names project " + project + " twice")
		}
		seen[project] = true
	}
	return strings.Join(problems, "; ")
}

// report writes err's line and answers the exit code: 0 for none, 2 for an UNKNOWN reading (an
// *Unknown, or holdrecord.ErrUnknown, a row or record that does not verify), and 1 for every
// refusal.
func report(stderr io.Writer, action string, err error) int {
	if err == nil {
		return 0
	}
	var unknown *Unknown
	if errors.As(err, &unknown) || errors.Is(err, holdrecord.ErrUnknown) {
		line := err.Error()
		if !strings.HasPrefix(line, "UNKNOWN") {
			line = "UNKNOWN " + line
		}
		fmt.Fprintf(stderr, "envoy-infra consumer-names %s: %s\n", action, line)
		return 2
	}
	fmt.Fprintf(stderr, "envoy-infra consumer-names %s: %v\n", action, err)
	return 1
}

// Unknown is a reading that is not whole: the command acts on nothing it did not read, and exits
// 2, never passing.
type Unknown struct {
	What string
	Err  error
}

func (u *Unknown) Error() string {
	if u.Err == nil {
		return "UNKNOWN " + u.What
	}
	return "UNKNOWN " + u.What + ": " + u.Err.Error()
}

func (u *Unknown) Unwrap() error { return u.Err }

func unknownf(err error, format string, args ...any) error {
	return &Unknown{What: fmt.Sprintf(format, args...), Err: err}
}

// formatSeq is a stream sequence as the journal's entries write it.
func formatSeq(seq uint64) string { return strconv.FormatUint(seq, 10) }
