package writerhold

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nuid"

	"github.com/sjawhar/envoy/infra/internal/flagerr"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// SigningKeySecret is the Secret Manager secret that holds the private half of the operator's hold
// signing key, which the operator's Google service account alone can read.
const SigningKeySecret = "operator-only-envoy-hold-signing"

// The words of `envoy-infra hold`.
const (
	actionStatus  = "status"
	actionPrepare = "prepare"
	actionSet     = "set"
	actionAbort   = "abort"
	actionRelease = "release"
	actionRestore = "restore"
	actionProve   = "prove"
	actionKeygen  = "keygen"
)

const usage = `usage: envoy-infra hold status|prepare|set|abort|release|restore|prove|keygen [options]

  status   --broker-url U --token-file F --verify-keys K [--hold ID]
  prepare  --workflow W --reason R --minters-report F   (and the writer's options below)
  set      (--hold ID | --workflow W --reason R) --minters-report F [--restored T=D]... [--snapshot T=S]...
  abort    --hold ID --reason R
  release  --hold ID --reason R
  restore  [--bootstrap | --hold ID] --reason R
  prove    --hold ID --restored T=D...
  keygen   --signing-key-secret projects/P/secrets/` + SigningKeySecret + `

  Every command but keygen: --broker-url U (the secrets broker), --token-file F (the operator's
  ServiceAccount token for the broker's NATS audience), --verify-keys K (BROKER_NATS_HOLD_SIGNING_KEYS).
  Every command that writes the hold: --signing-key-secret projects/P/secrets/` + SigningKeySecret + `.
  Every command that reaches the hub: --nats URL... and --probe-token-file F, and the hub's
  Kubernetes objects through --kube-namespace (envoy), --statefulset (envoy-nats) and --configmap
  (envoy-nats-config), with the in-cluster or KUBECONFIG configuration.

Exit codes: 0 done; 1 refused, naming what; 2 a usage error, or UNKNOWN: a reading that is not
whole, which is never a pass.
`

// options are a run's flags.
type options struct {
	HubFlags
	action                   string
	holdID, workflow, reason string
	bootstrap                bool
	restored, snapshots      flagerr.StringList
	daemonDatabases          flagerr.StringList
}

// KeyStore is the Secret Manager store of the operator's hold signing key: keygen writes its first
// version, and every command that writes the hold reads the version it serves. seed.Store, which
// `envoy-infra seed` writes nkey seeds through, is one.
type KeyStore interface {
	Put(ctx context.Context, name string, value []byte) (version string, err error)
	Current(ctx context.Context, name string) (version string, value []byte, err error)
}

// Command runs `envoy-infra hold` with args, the words after "hold", and returns its exit code.
// readers.Stores is the daemon stores' stream-id writer that hub-restore's abort runs (the census's
// own, CensusReaders); the other readers are the census's alone.
func Command(ctx context.Context, args []string, stdout, stderr io.Writer, readers CensusReaders) int {
	return CommandOn(ctx, args, stdout, stderr, RealDeps(readers))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, d Deps) int {
	actions := []string{actionStatus, actionPrepare, actionSet, actionAbort, actionRelease, actionRestore, actionProve, actionKeygen}
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
	err := dispatch(ctx, o, stdout, stderr, d)
	return ExitCode(stderr, "envoy-infra hold "+o.action, err)
}

// parse reads args into options, or answers the exit code of a usage error (or of --help).
func parse(args []string, stderr io.Writer) (options, int) {
	o := options{action: args[0]}
	flags := flag.NewFlagSet("envoy-infra hold "+o.action, flag.ContinueOnError)
	o.HubFlags.Register(flags)
	flags.StringVar(&o.holdID, "hold", "", "the hold's id")
	flags.StringVar(&o.workflow, "workflow", "", "the hold's workflow")
	flags.StringVar(&o.reason, "reason", "", "why, as the hold row records it")
	flags.BoolVar(&o.bootstrap, "bootstrap", false, "write the first row, released at gen 1")
	flags.Var(&o.restored, "restored", "a restored target and its disk or volume, <target>=<disk>")
	flags.Var(&o.snapshots, "snapshot", "a restored target and its snapshot, <target>=<snapshot>")
	flags.Var(&o.daemonDatabases, "daemon-database", "for hub-restore's abort: a daemon store's database, <project>=<file> (the file's first line holds its URL, its second the Cloud SQL instance that URL reaches); repeat")
	if code := flagerr.Parse(flags, args[1:], "hold", usage, stderr); code >= 0 {
		return o, code
	}
	if problem := o.usageProblem(); problem != "" {
		fmt.Fprintf(stderr, "envoy-infra hold %s: %s\n%s", o.action, problem, usage)
		return o, 2
	}
	return o, -1
}

// usageProblem is what the flags of a run lack or misuse for its action, or "".
func (o options) usageProblem() string {
	need := func(flagName, value string) string {
		if value == "" {
			return "--" + flagName + " is required"
		}
		return ""
	}
	var problems []string
	add := func(p string) {
		if p != "" {
			problems = append(problems, p)
		}
	}
	if o.action == actionKeygen {
		add(need("signing-key-secret", o.KeySecret))
		return strings.Join(problems, "; ")
	}
	add(need("broker-url", o.BrokerURL))
	add(need("token-file", o.TokenFile))
	add(need("verify-keys", o.VerifyKeys))
	writes := o.action != actionStatus
	if writes {
		add(need("signing-key-secret", o.KeySecret))
		add(need("reason", reasonFor(o)))
	}
	switch o.action {
	case actionPrepare:
		add(need("workflow", o.workflow))
		add(need("minters-report", o.MintersReport))
		if o.holdID != "" {
			add("--hold is not taken: prepare makes the hold's id")
		}
	case actionSet:
		if o.holdID == "" && o.workflow == "" {
			add("--hold, for a prepared hold, or --workflow, for a hold with no prepare step, is required")
		}
		if o.holdID != "" && o.workflow != "" {
			add("--hold and --workflow are not taken together: a prepared hold's workflow is in its record")
		}
		problems = append(problems, o.HubFlags.Problems("")...)
	case actionAbort, actionRelease:
		add(need("hold", o.holdID))
	case actionRestore:
		if o.bootstrap && o.holdID != "" {
			add("--bootstrap and --hold are exclusive: --bootstrap writes the first row into an empty log")
		}
	case actionProve:
		add(need("hold", o.holdID))
		if len(o.restored) == 0 {
			add("--restored is required: a prove names each target it proves")
		}
	}
	if o.action == actionProve {
		problems = append(problems, o.HubFlags.Problems(o.holdID)...)
		add(need("probe-token-file", o.ProbeTokenFile))
	}
	slices.Sort(problems)
	problems = slices.Compact(problems)
	if o.workflow != "" && !slices.Contains(workflows(), o.workflow) {
		add(fmt.Sprintf("--workflow %q is not one of %s", o.workflow, strings.Join(workflows(), ", ")))
	}
	for _, pair := range append(append([]string{}, o.restored...), o.snapshots...) {
		if target, value, ok := strings.Cut(pair, "="); !ok || target == "" || value == "" {
			add(fmt.Sprintf("%q is not <target>=<value>", pair))
		}
	}
	return strings.Join(problems, "; ")
}

// reasonFor is the run's reason; prove takes none of its own, since its entry carries its disks.
func reasonFor(o options) string {
	if o.action == actionProve {
		return "prove"
	}
	return o.reason
}

// workflows are the hold's workflows, as holdrecord's table names them.
func workflows() []string {
	return []string{
		string(holdrecord.WorkflowCutover), string(holdrecord.WorkflowRollback), string(holdrecord.WorkflowSelectionRecovery),
		string(holdrecord.WorkflowBucketsCreate), string(holdrecord.WorkflowBucketsRecreate), string(holdrecord.WorkflowBucketsRestore),
		string(holdrecord.WorkflowStreamsUpdate), string(holdrecord.WorkflowHubRestore), string(holdrecord.WorkflowStreamsCensus),
	}
}

func dispatch(ctx context.Context, o options, stdout, stderr io.Writer, d Deps) error {
	if o.action == actionKeygen {
		store, err := d.keyStore(ctx)
		if err != nil {
			return err
		}
		return keygen(ctx, store, o.KeySecret, stdout, stderr)
	}
	records, err := d.Records(ctx, o.HubFlags, o.action != actionStatus)
	if err != nil {
		return err
	}
	switch o.action {
	case actionStatus:
		return status(ctx, records, o.holdID, stdout)
	case actionRestore:
		return restore(ctx, records, o, stdout)
	case actionPrepare:
		op, err := d.Operator(ctx, o.HubFlags)
		if err != nil {
			return err
		}
		return prepare(ctx, records, op, o, d.Clock(), stdout)
	case actionAbort:
		op, err := d.Operator(ctx, o.HubFlags)
		if err != nil {
			return err
		}
		return abort(ctx, records, op, d.Readers.Stores, o, stdout)
	case actionRelease:
		op, err := d.Operator(ctx, o.HubFlags)
		if err != nil {
			return err
		}
		return release(ctx, records, op, o, stdout)
	case actionSet:
		op, err := d.Operator(ctx, o.HubFlags)
		if err != nil {
			return err
		}
		return set(ctx, records, op, o, d.Clock(), stdout)
	case actionProve:
		op, err := d.Operator(ctx, o.HubFlags)
		if err != nil {
			return err
		}
		return prove(ctx, records, op, o, stdout)
	}
	return fmt.Errorf("no action %q", o.action)
}

// status prints the hold row, verified, and, with a hold id, that hold's journal.
func status(ctx context.Context, records *holdrecord.Client, holdID string, stdout io.Writer) error {
	row, err := records.Row(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "row: gen %d, phase %s, hold %q, signed at %s\n", row.Gen, row.Phase, row.HoldID, row.SignedAt.Format(time.RFC3339Nano))
	if holdID == "" {
		holdID = row.HoldID
	}
	if holdID == "" {
		return nil
	}
	d, gen, err := records.Journal(ctx, holdID)
	if err != nil {
		return err
	}
	printJournal(stdout, holdID, d, gen)
	return nil
}

// printJournal prints a hold's journal: its workflow, completion and each entry.
func printJournal(stdout io.Writer, holdID string, d holdrecord.Done, gen uint64) {
	fmt.Fprintf(stdout, "hold %s: workflow %s, record gen %d, complete %t, hold_gen %d\n", holdID, d.Workflow, gen, d.Complete, d.HoldGen)
	for i, op := range d.Ops {
		fmt.Fprintf(stdout, "  %d. %s %s%s%s\n", i+1, op.Command, op.State, mapLine(" inputs", op.Inputs), mapLine(" outputs", op.Outputs))
	}
}

func mapLine(label string, m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k]
	}
	return label + " " + strings.Join(parts, " ")
}

// restore writes a lost hold row back through POST /v1/nats-hold/restore, at the log's next gen,
// which the route names (restoreAt). --bootstrap writes the first row, gen 1 released
// (holdrecord.Client.Bootstrap), and only into an empty log; a log with entries is refused, naming
// its next gen and `hold restore`. Otherwise it sends released first, and held only when the route
// refuses released with 400 bad_transition: the route's signed row log decides, not the journal.
// The route refuses released after a held entry of a hold that has not ended, a row lost mid-hold,
// so the row comes back held with its hold id and the hold goes on closed. After any other entry
// (released, or prepare: a row lost in prepare, before hold set's move or after an abort from
// prepare) the route stores released, with the hold's id when --hold names a hold that has not
// ended (a hold with no journal, lost in prepare before its journal was written, has not), so
// `hold abort` can end it (the table admits abort on a released row whose journal shows prepare,
// or which has no journal), and with none for an ended hold or no --hold. After a prepare entry the
// route stores released only with that entry's hold id (409 hold_mismatch for another or none). A
// released row refused after a held entry with no --hold names `hold restore --hold`, and nothing
// is written, so no row reads released and every writer stays refused. The command bounds no gen of
// its own: holdrecord.RestoreRow and Bootstrap restore at one above the highest row gen their
// client has verified, and a new process has verified none, so after a row loss the route's
// per-replica floor of the row and its signature check are the guards.
func restore(ctx context.Context, records *holdrecord.Client, o options, stdout io.Writer) error {
	var lost *holdrecord.RestoreGenError
	var refused *holdrecord.RouteError
	if o.bootstrap {
		err := records.Bootstrap(ctx)
		switch {
		case errors.As(err, &lost):
			return refusef(StepPreflight, "--bootstrap writes the first row only into an empty log, and the log's next gen is %d%s: restore the row with `hold restore`, naming an open hold with --hold <its hold id>", lost.RestoreGen, brokerSays(err))
		case errors.As(err, &refused) && refused.Code == "row_exists":
			return refusef(StepPreflight, "--bootstrap writes the first row only into an empty log, and a row is there (row_exists%s)", brokerSays(err))
		case err != nil:
			return restoreUnknown(err)
		}
		fmt.Fprintln(stdout, "wrote the first row: gen 1, phase released")
		return nil
	}
	holdID, cohortLeft := "", false
	if o.holdID != "" {
		journaled, err := hasJournal(ctx, records, o.holdID)
		if err != nil {
			return err
		}
		holdID = o.holdID
		if journaled {
			d, _, err := records.Journal(ctx, o.holdID)
			if err != nil {
				return err
			}
			if holdEnded(d) {
				holdID = ""
			}
		}
		if holdID != "" {
			cohort, err := records.Records(ctx, o.holdID, "cohort")
			if err != nil {
				return err
			}
			cohortLeft = len(cohort) > 0
			// The broker takes any hold id after a released entry, so an id no hold left anything
			// under, a typo, would land on the row and send the operator to end a hold that is not
			// there.
			if !journaled && !cohortLeft {
				return refusef(StepPreflight, "hold %s has no journal and no cohort record, so no hold of that id is left to end: restore the row with no --hold", o.holdID)
			}
		}
	}
	row, gen, err := restoreAt(ctx, records, natsgrant.OperatorPhaseReleased, holdID, o.reason)
	if errors.As(err, &refused) && refused.Code == "bad_transition" {
		if holdID == "" {
			// The route's 400 names no hold id for this code, and no route lists the log, so the
			// line names where the held hold's id was printed: the prepare and set that began it.
			// `hold status` reads the row first, so it shows nothing while the row is lost.
			return refusef(StepPreflight, "the broker refused a released restore while a hold is held (%v%s): the log's latest entry is a held row of a hold that has not ended, so the row was lost mid-hold; the hold's id is the one `hold prepare` and `hold set` printed when they began it; restore the row with `hold restore --hold <that id>`, which brings it back held", err, brokerSays(err))
		}
		// The route's log holds a held row of this hold, which has not ended: the row was lost
		// mid-hold, and comes back held at the gen the route named for the released one.
		row, err = records.RestoreRow(ctx, gen, natsgrant.OperatorPhaseHeld, holdID, o.reason)
	}
	if errors.As(err, &refused) && refused.Code == "hold_mismatch" {
		// After a prepare entry the broker restores released only with that entry's hold id, which
		// its 409 names (RouteError.HoldID), since no route lists the log.
		if refused.HoldID != "" {
			return refusef(StepPreflight, "the log's latest entry is a prepare row of hold %s, and the broker restores released after it only with that hold's id (%v%s): restore it with `hold restore --hold %s`", refused.HoldID, err, brokerSays(err), refused.HoldID)
		}
		other := ""
		if holdID != "" {
			other = " of a hold other than " + holdID
		}
		return refusef(StepPreflight, "the log's latest entry is a prepare row%s, and the broker restores released after it only with that hold's id, which its answer does not name (%v%s): restore it with `hold restore --hold <its hold id>`", other, err, brokerSays(err))
	}
	if err != nil {
		return restoreUnknown(err)
	}
	fmt.Fprintf(stdout, "restored the row: gen %d, phase %s, hold %q\n", row.Gen, row.Phase, row.HoldID)
	if row.Phase == natsgrant.OperatorPhaseReleased && holdID != "" {
		if cohortLeft {
			fmt.Fprintf(stdout, "hold %s has not ended: its cohort record is still in the broker's database; end it with `hold abort --hold %s`, or begin again with a new `hold prepare`\n", o.holdID, o.holdID)
		} else {
			fmt.Fprintf(stdout, "hold %s has not ended: it has no cohort record, and its journal is still open; end it with `hold abort --hold %s`\n", o.holdID, o.holdID)
		}
	}
	return nil
}

// restoreAt sends the restore of a row of phase and holdID at gen 1, and again at the gen the
// route names when it refuses gen 1 (a log with entries), and returns the row, the gen it sent last
// and its error. It is the command's one place for the restore's gen.
func restoreAt(ctx context.Context, records *holdrecord.Client, phase natsgrant.OperatorPhase, holdID, reason string) (holdrecord.Row, uint64, error) {
	gen := uint64(1)
	row, err := records.RestoreRow(ctx, gen, phase, holdID, reason)
	var lost *holdrecord.RestoreGenError
	if errors.As(err, &lost) {
		gen = lost.RestoreGen
		row, err = records.RestoreRow(ctx, gen, phase, holdID, reason)
	}
	return row, gen, err
}

// restoreUnknown is err, a restore the broker did not take, as the command answers it, with the
// broker's message beside the code (brokerSays). A 409 clock_skew (the row's signed_at outside the
// five-minute allowance of the broker's clock, or before the latest row the broker accepted) and a
// 409 below_floor (a record it judges the restore by reads as unknown: a log entry or a journal
// that does not verify, one signed by a key the broker no longer lists, or one below its floor)
// each store nothing and are UNKNOWN naming the code, since neither says anything of the log's
// latest entry, so neither is a reason to send held. Every other refusal is returned as it is,
// with the message.
func restoreUnknown(err error) error {
	var refused *holdrecord.RouteError
	switch {
	case !errors.As(err, &refused):
		return err
	case refused.Code == "clock_skew":
		return unknownf(StepPreflight, err, "the broker refused the restore 409 clock_skew and stored nothing%s: this machine's clock and the broker's differ by more than the allowance, or the row would be signed before the latest row the broker accepted; correct the clock and run it again", brokerSays(err))
	case refused.Code == "below_floor":
		return unknownf(StepPreflight, err, "the broker refused the restore 409 below_floor and stored nothing: a record it judges the restore by reads as unknown%s", brokerSays(err))
	case refused.Message == "":
		return err
	}
	return fmt.Errorf("%w%s", err, brokerSays(err))
}

// brokerSays is the message of err's route refusal (RouteError.Message, #4679 round 8), which
// names what the broker refused: a hold and whether a held restore is admitted, the rows a database
// admin repairs by hand, a key id, the times compared. It is printed beside the code, quoted as
// data, since part of it (a record's hold id) is a database writer's choice. "" for none.
func brokerSays(err error) string {
	var refused *holdrecord.RouteError
	if !errors.As(err, &refused) || refused.Message == "" {
		return ""
	}
	return fmt.Sprintf("; the broker says %q", refused.Message)
}

// keygen makes the operator's hold signing key: an Ed25519 pair, whose private half, the seed as
// unpadded base64url, it adds as the first version of the secret; it prints the public half alone,
// in the form BROKER_NATS_HOLD_SIGNING_KEYS takes (signedrecord.ParsePublicKeys). A secret that
// already holds a version is refused by the store, and nothing is written.
func keygen(ctx context.Context, store KeyStore, secret string, stdout, stderr io.Writer) error {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	seedBytes := private.Seed()
	value := []byte(base64.RawURLEncoding.EncodeToString(seedBytes))
	clear(seedBytes)
	defer clear(value)
	clear(private)
	version, err := store.Put(ctx, secret, value)
	if err != nil {
		return fmt.Errorf("add the signing key to %s: %w", secret, err)
	}
	fmt.Fprintf(stderr, "envoy-infra hold keygen: wrote version %s of %s; the public half follows on standard output\n", version, secret)
	fmt.Fprintln(stdout, base64.RawURLEncoding.EncodeToString(public))
	return nil
}

// newHoldID is a fresh hold id: a nuid, one subject token with no '.', so the bucket barrier's key
// hold.barrier.<hold id> is one key.
func newHoldID() string { return nuid.Next() }
