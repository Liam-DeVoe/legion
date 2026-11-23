// Package buckets is `envoy-infra buckets`: the one command that creates the buckets a listener
// copies (bus.CopiedBuckets), and recreates one once it has checked that no writer of it runs.
//
// A write such a bucket acknowledged is lost when the bucket is deleted and created again, and the
// listener that made it told its caller it landed: the ack names the stream and its sequence, never
// the bucket's incarnation. So no listener creates one, and recreate, which deletes one, refuses
// while a listener of the deployment it can see runs, and, for the CI state bucket, an
// envoy-dispatch, which writes it too. A listener that finds a bucket missing serves its writes 503
// and names the bucket on /healthz until this command creates it.
//
// create takes no writer proof (createBuckets). A bucket that is absent holds no write to lose: the
// writes its deleted stream acknowledged were lost with it, and every listener has answered the
// writes that need it 503 since, so a create while listeners run loses nothing more. create makes
// each absent bucket at the copies asked for, with the bucket's own configuration
// (bus.CopiedBucketConfig, the configuration the listeners' bus declares for it), and never
// changes a bucket that is there. So it can run again: a bucket there at those copies with that
// configuration is present and left as it is, and one there with other copies or another
// configuration refuses the run, naming each difference, before anything is created. That lets a
// provisioning step run on every start, such as the single-node compose file's buckets service,
// which `docker compose up` runs again while the listener it provisioned still serves, and a Job
// run on every apply.
//
// recreate's and check's proof is best effort, observation of the places the operator names, and
// each of their runs says so (bestEffortWarning). The coming server-side hold will replace it with
// exclusion at the server: while it holds, the auth callout refuses every connection but the
// operator's, every other connection is kicked, and every server's connz shows the operator alone.
// Until then it reads:
//
//   - Kubernetes, for each --kube-context, in each --namespace, the deployment's namespaces (every
//     namespace is read in every context): no pod the API lists there runs a container, an init
//     container or an ephemeral container that has not terminated and may write one of the
//     buckets, as the writer map reads it from the container's configuration
//     (podContainer.writes). A namespace the API does not hold blocks. The pods are read through
//     the API alone, which GKE Autopilot allows; no node's kubelet runtime list is read (Autopilot
//     refuses nodes/proxy), so a pod deleted with --force whose containers still run is one way a
//     writer gets past. A program a container's launcher picks from what the pod does not show -
//     a script file, a file or a command's output it reads, a variable of the image's own
//     environment - is another (podContainer.launch).
//   - Hosts, for each --host, through ssh (or --ssh, a program taking ssh's arguments): no process
//     on the host is named envoy-listener (or envoy-dispatch). An unreachable host blocks, and so
//     does a query that did not run to its end or enumerated no process at all.
//   - This machine, with --this-machine: the same query, run here through sh with no ssh, read and
//     judged as a host's. In a container it reads the processes the container's PID namespace
//     holds, so it proves the machine only where the container shares the machine's (docker's
//     --pid host, compose's pid: host).
//   - The census, for each --connz (a hub server's monitoring URL): no connection named as a
//     writer's is open. It can only add a blocker: an empty census proves no process absent.
//
// --first-deployment adds one check to the proof, and replaces none of it: the server must hold
// neither the notification stream, which every listener and envoy-dispatch create at their first
// connect, nor any of the buckets. An empty server is no proof that no writer runs - a hub on a new
// disk is empty while its old clients still run - so the inventory is required with it as without.
// create takes none of these flags (createUsage): it would check nothing with them.
//
// The command takes its replies under --inbox-prefix: InboxPrefix, the buckets user's, by default,
// and _INBOX. for a listener's user, which subscribes _INBOX.> and no other reply prefix, such as
// a Job that runs create as the clustered hub's gke user. A request of create that the server
// refuses under the run's grant is sent again for up to refusalWait, since a grant just applied
// reaches a hub at its next reload (untilGranted).
//
// Until the auth service's cutover lets the operator connect through the auth service, no hub
// has the buckets user's key (the deployment repository declares that user with none), so
// `create --break-glass` runs the same create as a listener's NATS user, which may create these
// buckets, taking its replies on nats.go's default inbox prefix, which that user may receive on,
// and logging the operator who runs it (createCommand). The pull request that removes the create
// grant from the listener users at the cutover deletes the flag with it.
package buckets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/infra/internal/flagerr"
	"github.com/sjawhar/envoy/internal/bus"
)

// Runner runs a program with args and returns its standard output; an error carries what it wrote
// to standard error. The command runs kubectl and ssh through it.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// execRunner runs the program for real.
func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Command runs `envoy-infra buckets` with args, the words after "buckets", and returns its exit
// code: 0 done, 1 refused or failed, 2 a usage error.
func Command(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return command(ctx, args, stdout, stderr, execRunner, http.DefaultClient)
}

// operatorEnvironment is the environment the command connects with: the process's own, but for
// ENVOY_ALLOW_REMOTE_NATS, which it always answers "1". The command is the operator's, run from a
// workstation against the deployment's hub on another machine, which bus.Connect refuses unless
// that variable says the run means it (bus.AllowRemoteEnvVar); --nats names the hub explicitly.
func operatorEnvironment(name string) (string, bool) {
	if name == bus.AllowRemoteEnvVar {
		return "1", true
	}
	return os.LookupEnv(name)
}

// InboxPrefix is the prefix the command's reply inboxes take by default (--inbox-prefix,
// bus.WithInboxPrefix), so the deployment's buckets user is granted subscribe on InboxPrefix.>
// alone: a user granted _INBOX.> would receive every other client's replies, a session's push
// deliveries among them.
const InboxPrefix = "_INBOX_envoy_buckets"

// Grant is the NATS grant the command's user needs, and nothing more: the subjects it publishes to
// - the account's JetStream info, then, for each bucket a listener copies, its stream's info,
// create, update (raising its copies) and delete, and the notification stream's info, which
// --first-deployment reads - and the one it subscribes to, its own reply inboxes (InboxPrefix).
// The same user runs the restore after `check` (`nats stream restore` with
// --inbox-prefix=_INBOX_envoy_buckets), so the grant also holds each copied bucket's restore
// request and the subject its chunks go to (nats-server 2.15.1, jetstream_api.go:91-94 and
// 170-172). The deployment's buckets user is granted exactly this until the auth service's
// auth callout replaces the static user at the cutover.
func Grant() (publish, subscribe []string) {
	publish = []string{"$JS.API.INFO", "$JS.API.STREAM.INFO." + bus.Stream}
	for _, bucket := range bus.CopiedBuckets() {
		for _, verb := range []string{"INFO", "CREATE", "UPDATE", "DELETE", "RESTORE"} {
			publish = append(publish, "$JS.API.STREAM."+verb+".KV_"+bucket)
		}
		publish = append(publish, "$JS.SNAPSHOT.RESTORE.KV_"+bucket+".*")
	}
	return publish, []string{InboxPrefix + ".>"}
}

// bestEffortWarning is printed on every recreate and check run, and with the usage, to standard
// error, before anything else: the proof is observation, not exclusion, until the coming
// server-side hold excludes every writer at the server - the auth callout refusing every
// connection but the operator's and every other connection kicked - from the check through the
// change. Each way it names is a test that asserts today's outcome and must flip then
// (buckets_test.go, TestBestEffort...). create takes no proof, and its run needs no writer stopped
// (createBuckets), so the warning and its instruction are not its to print.
const bestEffortWarning = `envoy-infra buckets: WARNING: this proof is best effort. A writer gets past it six ways:
  - an omitted host, dev box or namespace: only the --kube-context, --namespace, --host and --this-machine you name are checked;
  - a renamed binary: a host is checked by process name (envoy-listener, listener, dispatch, ...);
  - a program a pod's launcher picks from what the pod does not show: a script file, a file or a command's output it reads, or a variable of the image's own environment;
  - a pod deleted with --force: a cluster is checked by the pods its API lists, not its nodes' runtimes;
  - a writer that starts between the final scan and the create: nothing holds writers out;
  - an unheld restore: check holds nothing through a later nats stream restore.
Before a recreate or a check, stop every envoy-listener of the deployment (and every
envoy-dispatch, for envoy_ci_state) and confirm on each host that it has ended. A create takes
no proof: it makes only a bucket that is absent. A server-side hold, which leaves no writer a
connection, replaces this.
`

const usage = `usage: envoy-infra buckets create|recreate|check --nats URL [options]
       envoy-infra buckets create --break-glass --operator NAME --bucket NAME --nats URL

  create     make every copied bucket (or each --bucket) that is absent, at --replicas copies
             with its own configuration, and take no writer proof: an absent bucket holds no
             write to lose. A bucket there at those copies with that configuration is present
             and left as it is (exit 0); one there with other copies or another configuration
             refuses the run, naming each difference, and nothing is created (exit 1)
  recreate   delete each --bucket and create it again, once no writer runs
  check      prove no writer runs, and change nothing

The writers are every envoy-listener of the deployment, and every envoy-dispatch when the CI state
bucket is among the buckets. recreate and check require the proof before anything changes, and
create refuses its flags, which would check nothing:
  --kube-context CTX   a Kubernetes context to check, repeatable ("" is kubectl's current one)
  --namespace NS       a namespace where the deployment's writers run, an RFC 1123 label (lower-case
                       letters, digits and '-'), repeatable and required with --kube-context. Every
                       --namespace is read in every --kube-context, and one a context does not hold
                       blocks: its pods are read through the API, and each container that may write
                       a named bucket blocks
  --host TARGET        a machine to query through ssh, repeatable
  --ssh PROGRAM        the program run as ssh (default ssh), such as a wrapper over SSM
  --this-machine       the machine the command runs on, queried here with no ssh; in a container
                       it sees the machine's processes only with the machine's PID namespace
                       (docker --pid host, compose pid: host)
  --connz URL          a hub server's monitoring URL whose connections to read, repeatable
  --first-deployment   also require that the server holds no notification stream and no bucket

  --nats URL           the NATS server, as a user that may create and delete the buckets'
                       streams (NATS_NKEY_SEED_FILE or NATS_NKEY_SEED in the environment)
  --inbox-prefix P     the prefix of the run's reply inboxes, which its user must subscribe
                       (default _INBOX_envoy_buckets, the buckets user's; _INBOX. for a
                       listener's user, which subscribes _INBOX.>)
  --bucket NAME        a copied bucket to act on, repeatable (default: every one, for create)
  --replicas N         the copies a bucket is created with, 1 to 5 (default ENVOY_NATS_REPLICAS, else 1)

create sends a request its user's grant refuses again every 5s, for up to 180s from its start, since
a grant just applied reaches a hub at its next reload; then the refusal fails the run.

Until the auth service's cutover, create runs with a listener's credential instead:
  --break-glass        run create as a listener's NATS user (NATS_NKEY_SEED_FILE or
                       NATS_NKEY_SEED), its replies on the default _INBOX. prefix; each --bucket
                       is judged and made as create does, and nothing is updated or deleted
  --operator NAME      who runs it, named in the line logged before each create; required with
                       --break-glass
`

// stringList is a repeatable flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

func command(ctx context.Context, args []string, stdout, stderr io.Writer, run Runner, client *http.Client) int {
	if len(args) == 0 || (args[0] != "create" && args[0] != "recreate" && args[0] != "check") {
		fmt.Fprint(stderr, bestEffortWarning)
		help := len(args) > 0 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help")
		fmt.Fprint(stderr, usage)
		if help {
			return 0
		}
		return 2
	}
	action := args[0]
	flags := flag.NewFlagSet("envoy-infra buckets "+action, flag.ContinueOnError)
	// The flag package's own message quotes a value it could not parse, so what it writes is
	// printed only after --help, whose listing holds no word given (see below).
	var parsed bytes.Buffer
	flags.SetOutput(&parsed)
	var f fence
	var buckets stringList
	var natsURL, operator, inboxPrefix string
	var breakGlass bool
	replicas := 0
	flags.Var((*stringList)(&f.kubeContexts), "kube-context", "a Kubernetes context to check")
	flags.Var((*stringList)(&f.namespaces), "namespace", "a namespace where the deployment's writers run, read in every --kube-context")
	flags.Var((*stringList)(&f.hosts), "host", "a machine to query through ssh")
	flags.BoolVar(&f.thisMachine, "this-machine", false, "the machine the command runs on, queried with no ssh")
	flags.StringVar(&f.ssh, "ssh", "ssh", "the program run as ssh")
	flags.Var((*stringList)(&f.connz), "connz", "a hub server's monitoring URL")
	flags.BoolVar(&f.firstDeployment, "first-deployment", false, "the server has never served a listener")
	flags.StringVar(&natsURL, "nats", "", "the NATS server")
	flags.StringVar(&inboxPrefix, "inbox-prefix", InboxPrefix, "the prefix of the run's reply inboxes")
	flags.Var(&buckets, "bucket", "a copied bucket")
	flags.IntVar(&replicas, "replicas", 0, "the copies a bucket is created with")
	flags.BoolVar(&breakGlass, "break-glass", false, "run create as a listener's NATS user")
	flags.StringVar(&operator, "operator", "", "who runs a --break-glass create")
	err := flags.Parse(args[1:])
	if action != "create" {
		fmt.Fprint(stderr, bestEffortWarning)
	}
	// No word the command was given is echoed where the parse could not read it as one of its own:
	// a seed may have been pasted there, and `envoy-infra seed` and `render` echo none either. A
	// stray argument is named by its position, a malformed value by its flag, and any other word
	// the parse refused not at all.
	if err != nil {
		switch {
		case errors.Is(err, flag.ErrHelp):
			fmt.Fprint(stderr, parsed.String())
		case strings.HasPrefix(err.Error(), "flag needs an argument: -"):
			// The flag package says this only of a flag it defines, so the name is one of ours.
			fmt.Fprintf(stderr, "envoy-infra buckets: %v\n%s", err, usage)
		default:
			if name := flagerr.Malformed(flags, err); name != "" {
				fmt.Fprintf(stderr, "envoy-infra buckets: --%s: a malformed value (not echoed: a seed may have been pasted there)\n%s", name, usage)
			} else {
				fmt.Fprintf(stderr, "envoy-infra buckets: a flag this command does not take, or a malformed one (not echoed: a seed may have been pasted there)\n%s", usage)
			}
		}
		return 2
	}
	if flags.NArg() > 0 {
		// The action is word 1 after "buckets", so the first word the flags left is word
		// len(args)-NArg()+1.
		fmt.Fprintf(stderr, "envoy-infra buckets: word %d after \"buckets\" is no flag and no flag's value (not echoed: a seed may have been pasted there)\n%s", len(args)-flags.NArg()+1, usage)
		return 2
	}
	// A flag's value reaches the lines the command writes - a blocker names each --host,
	// --kube-context and --connz, a refusal each --bucket, the break-glass line --operator - and
	// the programs it runs, so a value that holds an nkey seed (bus.HoldsSeed), whole or inside it,
	// is refused naming the flag, never the value, before anything reads it, as seed and render
	// refuse one.
	var seeded []string
	flags.Visit(func(set *flag.Flag) {
		if bus.HoldsSeed(set.Value.String()) {
			seeded = append(seeded, "--"+set.Name)
		}
	})
	if len(seeded) > 0 {
		fmt.Fprintf(stderr, "envoy-infra buckets: %s: an nkey seed, which is never an argument (not echoed)\n", strings.Join(seeded, ", "))
		return 2
	}
	if natsURL == "" {
		fmt.Fprintf(stderr, "envoy-infra buckets: --nats is required\n%s", usage)
		return 2
	}
	for _, bucket := range buckets {
		if _, ok := bus.CopiedBucketConfig(bucket, 1); !ok {
			fmt.Fprintf(stderr, "envoy-infra buckets: %s is no bucket a listener copies (%s)\n", bucket, strings.Join(bus.CopiedBuckets(), ", "))
			return 2
		}
	}
	// A bucket named twice is one bucket: it is read, logged and created or recreated once.
	named := map[string]bool{}
	buckets = slices.DeleteFunc(buckets, func(bucket string) bool {
		again := named[bucket]
		named[bucket] = true
		return again
	})
	if refusal := createUsage(flags, action, breakGlass, operator, len(buckets)); refusal != "" {
		fmt.Fprintf(stderr, "envoy-infra buckets: %s\n", refusal)
		return 2
	}
	if breakGlass {
		inboxPrefix = nats.InboxPrefix
	}
	inbox, ok := inboxOption(inboxPrefix)
	if !ok {
		fmt.Fprintf(stderr, "envoy-infra buckets: --inbox-prefix is no inbox prefix: want one or more subject tokens joined by dots, such as %s or %s, with no wildcard and no white space\n%s", InboxPrefix, nats.InboxPrefix, usage)
		return 2
	}
	if action == "recreate" && len(buckets) == 0 {
		fmt.Fprintln(stderr, "envoy-infra buckets: recreate needs each --bucket it deletes")
		return 2
	}
	if len(buckets) == 0 {
		buckets = bus.CopiedBuckets()
	}
	// The deployment's replica count: --replicas, or else ENVOY_NATS_REPLICAS read as every program
	// of the deployment reads it (bus.ParseNATSReplicas). Either outside 1 to bus.MaxReplicas is a
	// usage error, before anything connects.
	replicasSet := false
	flags.Visit(func(set *flag.Flag) { replicasSet = replicasSet || set.Name == "replicas" })
	if replicasSet {
		if replicas < 1 || replicas > bus.MaxReplicas {
			fmt.Fprintf(stderr, "envoy-infra buckets: --replicas %d is no replica count: want a whole number of copies from 1 to %d\n", replicas, bus.MaxReplicas)
			return 2
		}
	} else if replicas, err = bus.ParseNATSReplicas(os.Getenv(bus.ReplicasEnvVar)); err != nil {
		fmt.Fprintf(stderr, "envoy-infra buckets: %v\n", err)
		return 2
	}
	if action == "create" {
		return createCommand(ctx, stdout, stderr, natsURL, inbox, breakGlass, operator, buckets, replicas)
	}
	if len(f.kubeContexts) == 0 && len(f.hosts) == 0 && !f.thisMachine {
		fmt.Fprintln(stderr, "envoy-infra buckets: name every place a writer of the deployment runs: --kube-context for each cluster, --host for each machine and --this-machine for the one the command runs on; --first-deployment proves no writer absent and needs them too")
		return 2
	}
	// A cluster is read in the namespaces the operator names alone, so a container elsewhere
	// that shares a writer's name, such as an Actions runner's, is never counted. Every
	// --namespace is read in every --kube-context rather than paired with one: a namespace a
	// context does not hold blocks, naming the context, so one list is never weaker than pairs,
	// and the flags stay free of order, as every other flag of the command is.
	if len(f.kubeContexts) > 0 && len(f.namespaces) == 0 {
		fmt.Fprintln(stderr, "envoy-infra buckets: --kube-context needs each --namespace where the deployment's writers run (such as envoy and dispatch): the proof reads the pods of those namespaces alone")
		return 2
	}
	if len(f.namespaces) > 0 && len(f.kubeContexts) == 0 {
		fmt.Fprintln(stderr, "envoy-infra buckets: --namespace names a namespace of a --kube-context: name the context too")
		return 2
	}
	// A namespace reaches kubectl's arguments, and the API names a namespace with an RFC 1123 label
	// alone, so any other value is refused before anything runs: kubectl would read one starting
	// with '-', such as --all-namespaces or -oname, as a flag of its own, and list no namespace's
	// pods, or every one's.
	for _, namespace := range f.namespaces {
		if !namespaceName.MatchString(namespace) {
			fmt.Fprintf(stderr, "envoy-infra buckets: --namespace %q is no namespace name: want an RFC 1123 label, at most 63 lower-case letters, digits and '-', starting and ending with a letter or a digit\n%s", namespace, usage)
			return 2
		}
	}
	f.run, f.http, f.pid = run, client, os.Getpid()
	f.buckets = buckets
	f.dispatch = slices.Contains(buckets, bus.CIStateBucket)

	nc, err := bus.Connect([]string{natsURL}, append([]bus.ConnectOption{bus.WithName("envoy-infra-buckets"), bus.WithEnvironment(operatorEnvironment)}, inbox...)...)
	if err != nil {
		fmt.Fprintf(stderr, "envoy-infra buckets: connect to %s: %v\n", bus.NATSURLWithoutUserinfo(natsURL), err)
		return 1
	}
	defer nc.Close()
	js := nc.JS()

	if blockers := f.prove(ctx, stdout, js); len(blockers) > 0 {
		refuse(stderr, action, blockers, "")
		return 1
	}
	if action == "check" {
		fmt.Fprintln(stdout, "proven: no writer of the buckets runs")
		return 0
	}
	for _, bucket := range buckets {
		if err := js.DeleteKeyValue(bucket); err != nil && !errors.Is(err, nats.ErrBucketNotFound) && !errors.Is(err, nats.ErrStreamNotFound) {
			fmt.Fprintf(stderr, "envoy-infra buckets: delete %s: %v\n", bucket, err)
			return 1
		}
		fmt.Fprintf(stdout, "deleted %s\n", bucket)
	}
	// A writer that started between the proof and the delete may have written to the deleted
	// bucket, so the proof is taken again before anything is created.
	if blockers := f.prove(ctx, stdout, js); len(blockers) > 0 {
		refuse(stderr, "create", blockers, "the buckets are deleted and not created: stop each writer above, then run `envoy-infra buckets create`")
		return 1
	}
	return createDeleted(stdout, stderr, nc.Conn, js, buckets, replicas)
}

// refuse writes the refusal and each blocker.
func refuse(stderr io.Writer, action string, blockers []string, after string) {
	fmt.Fprintf(stderr, "envoy-infra buckets %s: refused: a writer of the buckets may still run, and a bucket created under it loses every write the deleted one acknowledged\n", action)
	for _, blocker := range blockers {
		fmt.Fprintf(stderr, "  blocked: %s\n", blocker)
	}
	if after != "" {
		fmt.Fprintf(stderr, "  %s\n", after)
	}
}

// createDeleted creates each of buckets, the ones recreate deleted, with its copied-bucket
// configuration, and leaves one another client made after the delete as it is.
func createDeleted(stdout, stderr io.Writer, conn *nats.Conn, js nats.JetStreamContext, buckets []string, replicas int) int {
	for _, bucket := range buckets {
		if _, err := js.KeyValue(bucket); err == nil {
			fmt.Fprintf(stdout, "%s exists; left as it is\n", bucket)
			continue
		} else if !errors.Is(err, nats.ErrBucketNotFound) {
			fmt.Fprintf(stderr, "envoy-infra buckets: read %s: %v\n", bucket, err)
			return 1
		}
		// An operator's command, run by hand until it ends: the create's waits end at the start's
		// hang guard (bus.EnsureKeyValue), under no context of its own.
		if _, err := bus.CreateCopiedBucket(context.Background(), conn, js, bucket, replicas); err != nil {
			fmt.Fprintf(stderr, "envoy-infra buckets: create %s: %v\n", bucket, err)
			return 1
		}
		fmt.Fprintf(stdout, "created %s (%d copies)\n", bucket, replicas)
	}
	return 0
}

// createUsage is why the flags of a run misuse create's flags, or "" when they do not: create
// takes no writer proof, so it takes no proof flag, which would check nothing; --break-glass is
// create's alone, takes its replies on _INBOX. with no --inbox-prefix, and needs --operator and
// each --bucket it creates; --operator names a break-glass run and nothing else. An operator name
// holding a control character, a line break among them, is refused, so the run's log line stays
// one line.
func createUsage(flags *flag.FlagSet, action string, breakGlass bool, operator string, buckets int) string {
	if !breakGlass && operator != "" {
		return "--operator names who runs a --break-glass create; leave it off without --break-glass"
	}
	if breakGlass && action != "create" {
		return "--break-glass is create's alone: it creates an absent bucket and does nothing else, so it neither deletes one (recreate) nor proves that no writer runs (check)"
	}
	if action != "create" {
		return ""
	}
	var proof []string
	inboxSet := false
	flags.Visit(func(set *flag.Flag) {
		switch set.Name {
		case "kube-context", "namespace", "host", "this-machine", "ssh", "connz", "first-deployment":
			proof = append(proof, "--"+set.Name)
		case "inbox-prefix":
			inboxSet = true
		}
	})
	if len(proof) > 0 {
		return "create takes no writer proof, so " + strings.Join(proof, " and ") + " would check nothing: leave them off (recreate and check take them)"
	}
	if !breakGlass {
		return ""
	}
	if inboxSet {
		return "--break-glass takes its replies on " + nats.InboxPrefix + " already: leave --inbox-prefix off"
	}
	if strings.TrimSpace(operator) == "" || strings.ContainsFunc(operator, unicode.IsControl) {
		return "--break-glass needs --operator, who runs it, on one line: the run logs it"
	}
	if buckets == 0 {
		return "--break-glass needs each --bucket it creates"
	}
	return ""
}

// inboxOption is the connection option the prefix --inbox-prefix names, or false when prefix is
// no inbox prefix: none for nats.go's own _INBOX. (written with or without its final dot), which a
// listener's user subscribes (_INBOX.>), and bus.WithInboxPrefix for any other, such as
// InboxPrefix, the buckets user's. A prefix is one or more subject tokens joined by dots, with no
// wildcard and no white space.
func inboxOption(prefix string) ([]bus.ConnectOption, bool) {
	prefix = strings.TrimSuffix(prefix, ".")
	if prefix == "" || strings.ContainsAny(prefix, "*>") || strings.ContainsFunc(prefix, unicode.IsSpace) || slices.Contains(strings.Split(prefix, "."), "") {
		return nil, false
	}
	if prefix+"." == nats.InboxPrefix {
		return nil, true
	}
	return []bus.ConnectOption{bus.WithInboxPrefix(prefix)}, true
}

// createCommand is create: it connects to natsURL as the user the environment names
// (NATS_NKEY_SEED_FILE or NATS_NKEY_SEED), its replies under the prefix inbox sets
// (--inbox-prefix), and makes each of buckets that is absent (createBuckets) until ctx ends, which
// main makes an interrupt or SIGTERM do. The default is the buckets user's prefix, InboxPrefix; a
// listener's user, which subscribes _INBOX.> alone, runs it with _INBOX., as a Job running as the
// clustered hub's gke user does. A break-glass run is a listener user's until the auth service's
// cutover, on _INBOX., and logs one line on standard error before each create, naming the
// operator, the hub, the bucket and that the run is break-glass.
//
// The connection logs no line for the server's refusal of a publish
// (bus.WithPublishRefusalsUnlogged): each refused request answers that refusal, and the run sends
// it again for up to refusalWait and says so once (untilGranted), where a line at ERROR for each
// refused send would fill that wait.
func createCommand(ctx context.Context, stdout, stderr io.Writer, natsURL string, inbox []bus.ConnectOption, breakGlass bool, operator string, buckets []string, replicas int) int {
	name := "envoy-infra-buckets"
	if breakGlass {
		name = "envoy-infra-buckets-break-glass"
	}
	hub := bus.NATSURLWithoutUserinfo(natsURL)
	nc, err := bus.Connect([]string{natsURL}, append([]bus.ConnectOption{bus.WithName(name), bus.WithEnvironment(operatorEnvironment), bus.WithPublishRefusalsUnlogged()}, inbox...)...)
	if err != nil {
		fmt.Fprintf(stderr, "envoy-infra buckets: connect to %s: %v\n", hub, err)
		return 1
	}
	defer nc.Close()
	announce := func(string) {}
	if breakGlass {
		announce = func(bucket string) {
			fmt.Fprintf(stderr, "envoy-infra buckets: break-glass: operator %s creates %s on %s, as the environment's NATS user and with no writer proof\n", operator, bucket, hub)
		}
	}
	return createBuckets(ctx, stdout, stderr, nc.Conn, nc.JS(), buckets, replicas, announce)
}

// createBuckets reads each of buckets' streams (STREAM.INFO KV_<bucket>) and judges each against its
// own stream at replicas copies (judgeBucket):
//
//   - a bucket the server reports missing is absent, and is made below;
//   - one that matches is present: it is left as it is, and the run says so;
//   - one that differs refuses the run before anything is created, naming each difference: a bucket
//     there at other copies most often says the copies asked for are not the deployment's, and the
//     absent buckets would be made at those copies too;
//   - a read answering anything but "not found" fails the run, and nothing is created.
//
// It then makes each absent bucket with no writer proof, through bus.CreateMissingCopiedBucket,
// which creates only a bucket the server does not hold, sends a lookup or create the server left
// unanswered again, and opens, raises and updates no bucket it finds there: a create the server
// answers "stream name already in use" is answered so, with no read or update of that stream
// after it, whatever the server reports for the stream, so the run sends no STREAM.UPDATE on any
// server version. announce is called before each create. A create that loses a race to another
// client - the bucket found by the create's own lookup, or made between that lookup and the create
// with another configuration, which the server answers "stream name already in use"
// (bus.ErrBucketExists) - reads the stream again and applies the same rule: present when it matches,
// refused naming each difference when it does not, and the run stops there. A bucket made between
// the lookup and the create with the same configuration is the one this create asked for, which
// JetStream answers as made.
//
// The server's version decides the stream a create makes (bus.CopiedBucketStreamConfig: its
// discard policy, new from nats-server 2.7.2 on), so it is read once, before the first read, and a
// connection that reports none - ConnectedServerVersion answers "" while it is not connected -
// refuses the run with its own line and reads nothing (bus.ServerVersion): nats.go and the bus read
// "" as a server too old for a bucket, and every present bucket would be judged against a stream
// made for such a server, discard old, and refused naming its discard policy as a difference. The
// create reads the version again where nats.go does, after the account's info (bus's
// addBucketStream), and a connection that reconnects in between reports none there: that create
// sends no STREAM.CREATE and the run is refused with the same line, where it would have made a
// stream of discard old.
//
// A read or a create the server refuses under the connection's grant is sent again until
// refusalWait has passed since the run began (untilGranted). Once ctx has ended the run stops where
// it is, starts no create after it and fails naming ctx's cause (stopped): every read and wait ends
// with ctx, the check before each create reads it, and the create sends no request once it has
// ended. A create it had already sent when ctx ended is not called back, and lands: the line says
// so.
func createBuckets(ctx context.Context, stdout, stderr io.Writer, conn *nats.Conn, js nats.JetStreamContext, buckets []string, replicas int, announce func(bucket string)) int {
	deadline := time.Now().Add(refusalWait)
	version, err := bus.ServerVersion(conn)
	if err != nil {
		return noServerVersion(stderr, "nothing is read or created")
	}
	var absent, differ []string
	for _, bucket := range buckets {
		missing, differences, err := judgeBucket(ctx, stderr, deadline, js, version, bucket, replicas)
		switch {
		case ctx.Err() != nil:
			return stopped(ctx, stderr)
		case err != nil:
			fmt.Fprintf(stderr, "envoy-infra buckets: %v\n", err)
			return 1
		case missing:
			absent = append(absent, bucket)
		case len(differences) > 0:
			differ = append(differ, fmt.Sprintf("KV_%s: %s", bucket, strings.Join(differences, "; ")))
		}
	}
	if len(differ) > 0 {
		fmt.Fprintf(stderr, "envoy-infra buckets create: refused: a bucket named is there with other copies or another configuration than its own at %d copies, and is left as it is; nothing is created\n", replicas)
		for _, line := range differ {
			fmt.Fprintf(stderr, "  differs: %s\n", line)
		}
		return 1
	}
	for _, bucket := range buckets {
		if !slices.Contains(absent, bucket) {
			fmt.Fprintf(stdout, "%s present at %d copies with its own configuration: left as it is\n", bucket, replicas)
		}
	}
	for _, bucket := range absent {
		if ctx.Err() != nil {
			return stopped(ctx, stderr)
		}
		announce(bucket)
		_, err := untilGranted(ctx, stderr, deadline, "create KV_"+bucket, func() (bus.KeyValue, error) {
			return bus.CreateMissingCopiedBucket(ctx, conn, js, bucket, replicas)
		})
		if err == nil {
			fmt.Fprintf(stdout, "created %s (%d copies)\n", bucket, replicas)
			continue
		}
		if ctx.Err() != nil {
			return stopped(ctx, stderr)
		}
		if errors.Is(err, bus.ErrNoServerVersion) {
			return noServerVersion(stderr, "KV_"+bucket+" is not created")
		}
		if !errors.Is(err, bus.ErrBucketExists) {
			fmt.Fprintf(stderr, "envoy-infra buckets: create %s: %v\n", bucket, err)
			return 1
		}
		missing, differences, readErr := judgeBucket(ctx, stderr, deadline, js, version, bucket, replicas)
		switch {
		case ctx.Err() != nil:
			return stopped(ctx, stderr)
		case readErr != nil:
			fmt.Fprintf(stderr, "envoy-infra buckets create: KV_%s was made by another client after the read (%v), and the read again failed: %v\n", bucket, err, readErr)
			return 1
		case missing:
			fmt.Fprintf(stderr, "envoy-infra buckets create: KV_%s was made by another client after the read (%v), and the read again reports it missing\n", bucket, err)
			return 1
		case len(differences) > 0:
			fmt.Fprintf(stderr, "envoy-infra buckets create: refused: KV_%s was made by another client after the read (%v), with other copies or another configuration than its own at %d copies, and is left as it is\n", bucket, err, replicas)
			fmt.Fprintf(stderr, "  differs: KV_%s: %s\n", bucket, strings.Join(differences, "; "))
			return 1
		}
		fmt.Fprintf(stdout, "%s present at %d copies with its own configuration, made by another client after the read: left as it is\n", bucket, replicas)
	}
	return 0
}

// noServerVersion refuses a create whose connection reports no server version where the version
// chooses the stream a bucket is judged against or created as (bus.ServerVersion), saying what the
// run left undone: a bucket a line above says was created stays.
func noServerVersion(stderr io.Writer, undone string) int {
	fmt.Fprintf(stderr, "envoy-infra buckets create: refused: the connection reports no server version, as one that is not connected does, and each bucket is judged against the stream its create makes on the hub's version; %s: run create again\n", undone)
	return 1
}

// stopped fails a create whose ctx has ended, naming ctx's cause: main ends ctx on an interrupt or
// SIGTERM, whose cause names the signal. The run starts no create after it. A create it had sent
// before - the create of the bucket it was on, whose answer it stops waiting for - still reaches
// the hub and lands, so the line says so and that create run again reads what the hub holds. A
// bucket a line above says was created stays.
func stopped(ctx context.Context, stderr io.Writer) int {
	fmt.Fprintf(stderr, "envoy-infra buckets create: stopped: %v; it starts no create after this, but one it had sent may still land: run create again to see what the hub holds\n", context.Cause(ctx))
	return 1
}

// refusalWait and refusalRetry bound create's requests the server refuses under the connection's
// grant (bus.ErrPublishDenied, which the bus names as soon as the server reports the refusal). A
// grant just applied reaches a hub only when the hub's configuration reloader has reloaded its
// auth.conf, which the apply that made the grant cannot see, so a run right after that apply, such
// as a Job the same apply starts, can be refused for a while. A refused request changes nothing, so
// it is sent again every refusalRetry until refusalWait has passed since the run began, and only
// then fails the run. A test lowers both.
var (
	refusalWait  = 180 * time.Second
	refusalRetry = 5 * time.Second
)

// untilGranted is send's answer, sent again every refusalRetry while the server refuses it under the
// connection's grant, another send fits before deadline and ctx has not ended. Its first refusal is
// logged on stderr, naming what and the refusal, and no later one is: the connection logs none of
// them either (createCommand). A wait between two sends ends when ctx does, and the answer is then
// ctx's error, with no send after it.
func untilGranted[T any](ctx context.Context, stderr io.Writer, deadline time.Time, what string, send func() (T, error)) (T, error) {
	for logged := false; ; logged = true {
		value, err := send()
		if ctx.Err() != nil || !errors.Is(err, bus.ErrPublishDenied) || !time.Now().Add(refusalRetry).Before(deadline) {
			return value, err
		}
		if !logged {
			fmt.Fprintf(stderr, "envoy-infra buckets: %s: refused under this user's grant, sent again every %s for up to %s from the start, since a grant just applied reaches the hub at its next reload: %v\n", what, refusalRetry, refusalWait, err)
		}
		select {
		case <-ctx.Done():
			return value, ctx.Err()
		case <-time.After(refusalRetry):
		}
	}
}

// judgeBucket reads bucket's stream and reports whether the server holds it, and when it does, each
// way it differs from the stream the bucket's create makes at replicas copies on a server reporting
// version: bus.CopiedBucketStreamConfig, the configuration the listeners' bus declares for the
// bucket made into its stream as nats.go makes it (bucketDifferences). A read the server refuses
// under the connection's grant is sent again until deadline (untilGranted). A read answering
// anything else but "not found" is an error naming the bucket. Each read ends with ctx, and at
// the bus's request bound (bus.RequestTimeout) within it.
func judgeBucket(ctx context.Context, stderr io.Writer, deadline time.Time, js nats.JetStreamContext, version, bucket string, replicas int) (missing bool, differences []string, err error) {
	info, err := untilGranted(ctx, stderr, deadline, "read KV_"+bucket, func() (*nats.StreamInfo, error) {
		read, cancel := context.WithTimeout(ctx, bus.RequestTimeout)
		defer cancel()
		return js.StreamInfo("KV_"+bucket, nats.Context(read))
	})
	if errors.Is(err, nats.ErrStreamNotFound) {
		return true, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("read %s: %w", bucket, err)
	}
	want, ok := bus.CopiedBucketStreamConfig(bucket, replicas, version)
	if !ok {
		return false, nil, fmt.Errorf("read %s: no copied bucket's stream", bucket)
	}
	return false, bucketDifferences(bucket, info.Config, want), nil
}

// bucketDifferences names each way have, the stream of bucket the server holds, differs from want,
// the stream its create makes (bus.CopiedBucketStreamConfig, a copy of nats.go@v1.55.0 kv.go:437-523,
// CreateKeyValue): its copies, its history (a key's values kept), its maximum age, storage, byte and
// value-size limits and compression, its discard policy and direct get, the subjects it takes, and
// the streams it mirrors or sources and the subjects it republishes to, each of which a copied
// bucket's own stream has none of. The session bucket's maximum age is left out: the listeners
// set it on the running bucket from ENVOY_SESSION_MAX_AGE (bus.SetKeyValueMaxAge,
// internal/session), so a deployment that sets that variable holds the bucket at another age than
// the one it was made with, which is its own.
func bucketDifferences(bucket string, have, want nats.StreamConfig) []string {
	fields := []struct {
		name       string
		same       bool
		have, want any
	}{
		{"replicas", have.Replicas == want.Replicas, have.Replicas, want.Replicas},
		{"history", have.MaxMsgsPerSubject == want.MaxMsgsPerSubject, have.MaxMsgsPerSubject, want.MaxMsgsPerSubject},
		{"max age", have.MaxAge == want.MaxAge || bucket == bus.SessionBucket, have.MaxAge, want.MaxAge},
		{"storage", have.Storage == want.Storage, have.Storage, want.Storage},
		{"max bytes", have.MaxBytes == want.MaxBytes, have.MaxBytes, want.MaxBytes},
		{"max value size", have.MaxMsgSize == want.MaxMsgSize, have.MaxMsgSize, want.MaxMsgSize},
		{"compression", have.Compression == want.Compression, have.Compression, want.Compression},
		{"discard policy", have.Discard == want.Discard, have.Discard, want.Discard},
		{"direct get", have.AllowDirect == want.AllowDirect, have.AllowDirect, want.AllowDirect},
		{"subjects", slices.Equal(have.Subjects, want.Subjects), listed(have.Subjects), listed(want.Subjects)},
		{"mirror", reflect.DeepEqual(have.Mirror, want.Mirror), sourcesNamed(have.Mirror), sourcesNamed(want.Mirror)},
		{"sources", len(have.Sources)+len(want.Sources) == 0 || reflect.DeepEqual(have.Sources, want.Sources), sourcesNamed(have.Sources...), sourcesNamed(want.Sources...)},
		{"republish", reflect.DeepEqual(have.RePublish, want.RePublish), republished(have.RePublish), republished(want.RePublish)},
	}
	var differences []string
	for _, field := range fields {
		if !field.same {
			differences = append(differences, fmt.Sprintf("%s %v, want %v", field.name, field.have, field.want))
		}
	}
	return differences
}

// listed is subjects as a difference names them: joined by commas, or "none".
func listed(subjects []string) string {
	if len(subjects) == 0 {
		return "none"
	}
	return strings.Join(subjects, ",")
}

// sourcesNamed is the streams a mirror or sources name, as a difference names them: each stream's
// name, joined by commas, or "none".
func sourcesNamed(sources ...*nats.StreamSource) string {
	var names []string
	for _, source := range sources {
		if source != nil {
			names = append(names, source.Name)
		}
	}
	return listed(names)
}

// republished is a stream's republish as a difference names it: the subjects it republishes and
// where to, or "none".
func republished(republish *nats.RePublish) string {
	if republish == nil {
		return "none"
	}
	if republish.HeadersOnly {
		return republish.Source + " to " + republish.Destination + " (headers only)"
	}
	return republish.Source + " to " + republish.Destination
}

// fence is the proof that no writer of the buckets runs.
type fence struct {
	kubeContexts []string
	// namespaces are the namespaces of each Kubernetes context whose pods the proof reads: the
	// deployment's, where its writers run (--namespace).
	namespaces []string
	hosts      []string
	// thisMachine queries the machine the command runs on, through sh rather than ssh; pid is the
	// command's own process id, which proveThisMachine reads.
	thisMachine     bool
	pid             int
	ssh             string
	connz           []string
	firstDeployment bool
	// buckets are the buckets the run acts on, whose writers the proof looks for.
	buckets []string
	// dispatch says envoy-dispatch is a writer too: the CI state bucket is among the buckets.
	dispatch bool
	run      Runner
	http     *http.Client
}

// listenerBuckets are the copied buckets every listener opens and writes, whatever routes it
// mounts: bus.CopiedBuckets without the CI state bucket, as cmd/listener's listenerCopiedBuckets
// builds them for a listener without the GitHub webhook route. A listener that mounts that route
// opens bus.CIStateBucket too, which envoy-dispatch also writes.
var listenerBuckets = slices.DeleteFunc(bus.CopiedBuckets(), func(bucket string) bool { return bucket == bus.CIStateBucket })

// podContainer is the part of a pod's container - a container, an init container (a native
// sidecar among them) or an ephemeral container - the proof reads to tell which buckets it may
// write.
type podContainer struct {
	Name    string   `json:"name"`
	Command []string `json:"command"`
	// Args are the words Kubernetes appends to Command, or to the image's entrypoint when Command is
	// empty: a launcher's program or a shell's script among them (launch).
	Args []string `json:"args"`
	Env  []envVar `json:"env"`
	// EnvFrom are the Secrets and ConfigMaps each of whose keys becomes a variable of the
	// container, named with the source's prefix before the key; the pod shows neither.
	EnvFrom []struct {
		Prefix string `json:"prefix"`
	} `json:"envFrom"`
	// pod is the pod the container is listed in, whose fields a variable of its env may take
	// (fieldRef); nil for a container read alone, whose such variables are then unread.
	pod *listedPod
}

// envVar is one variable of a container's env: a value, or one taken from elsewhere (valueFrom).
type envVar struct {
	Name      string     `json:"name"`
	Value     string     `json:"value"`
	ValueFrom *valueFrom `json:"valueFrom"`
}

// valueFrom is where a variable takes its value when the pod does not give it as text: a field of
// the pod (fieldRef) or a resource of a container (resourceFieldRef), which the proof reads, or a
// key of a Secret or a ConfigMap, which the pod does not show.
type valueFrom struct {
	FieldRef *struct {
		FieldPath string `json:"fieldPath"`
	} `json:"fieldRef"`
	ResourceFieldRef json.RawMessage `json:"resourceFieldRef"`
}

// writes is the copied buckets container c may write, read from its configuration by the writer
// map:
//
//   - a launcher (launch) whose words name a listener - envoy-listener, or ENVOY_MACHINE_ID or
//     ENVOY_WEBHOOKS, which it may set - such as [/bin/sh, -c, 'ENVOY_MACHINE_ID=x exec
//     envoy-listener'] or [tini, --, envoy-listener], writes every copied bucket: the launcher may
//     set ENVOY_MACHINE_ID and ENVOY_WEBHOOKS itself, which the pod does not show, so the proof
//     fails closed. So does a container whose command or args expand a variable the pod's env sets
//     but does not show (launch's unread), which may start any writer;
//   - envoy-dispatch - named so, or running envoy-dispatch - writes the CI state bucket;
//   - a listener - a container whose environment sets ENVOY_MACHINE_ID, which every listener
//     requires, or that is named or runs as a listener (listener, envoy-listener, or a command
//     running envoy-listener or the held-port wrapper envoy-listener-heldport) - writes
//     listenerBuckets, and the CI state bucket too when its ENVOY_WEBHOOKS may mount the GitHub
//     route (namesGitHub), or is set from a Secret, a ConfigMap or a field, whose value the pod
//     does not show;
//   - a container whose envFrom may set a variable the map reads (envFromMaySet) may be a writer
//     the pod does not show, so it counts as one: one whose envFrom may set ENVOY_MACHINE_ID is a
//     listener, and a listener whose envFrom may set ENVOY_WEBHOOKS writes the CI state bucket.
//     A container nothing else names a writer, with an envFrom source of no prefix, so writes
//     every copied bucket: the proof fails closed;
//   - a container whose launcher's words name envoy-dispatch writes the CI state bucket too,
//     beside whatever the rest of the map reads it to write;
//   - any other container writes none: the launcher rule adds none for a launcher whose words name
//     neither binary, nor for one that picks its program from what the pod does not show, such as
//     a script file, a file or a command's output it reads, or a variable the image's own
//     environment sets (bestEffortWarning).
//
// The proof reads only the namespaces the operator names, so a container elsewhere that is named
// listener, such as an Actions runner's, is never read.
func (c podContainer) writes() []string {
	started := c.launch()
	if started.unread != "" || started.listener() {
		return bus.CopiedBuckets()
	}
	if c.Name == "envoy-dispatch" || started.binary == dispatchBinary {
		return []string{bus.CIStateBucket}
	}
	listener := c.Name == "listener" || c.Name == "envoy-listener" || started.binary == listenerBinary
	github := false
	// Kubernetes sets the last of two variables of one name, so the last one is read.
	for _, env := range c.Env {
		switch env.Name {
		case "ENVOY_MACHINE_ID":
			listener = true
		case "ENVOY_WEBHOOKS":
			github = env.ValueFrom != nil || namesGitHub(env.Value)
		}
	}
	listener = listener || c.envFromMaySet("ENVOY_MACHINE_ID")
	github = github || c.envFromMaySet("ENVOY_WEBHOOKS")
	var written []string
	if listener {
		written = slices.Clone(listenerBuckets)
	}
	if (listener && github) || started.names == dispatchBinary {
		written = append(written, bus.CIStateBucket)
	}
	return written
}

// The writer binaries a container's command runs or a launcher's words name, by the name the writer
// map reads.
const (
	listenerBinary = "envoy-listener"
	dispatchBinary = "envoy-dispatch"
)

// launch is what a container's command and args start, worked out once for the writer map (writes)
// and for its blockers (launchNote).
type launch struct {
	// binary is the writer binary the command's first word runs, by its base name: listenerBinary
	// for envoy-listener and for the held-port wrapper envoy-listener-heldport, which runs it;
	// dispatchBinary for envoy-dispatch. Its later words and its args are its own flags, so such a
	// command has no launcher.
	binary string
	// launcher is the program that runs the launcher's words: the command's first word, by its base
	// name, or "" for the image's entrypoint when the container sets args alone.
	launcher string
	// names is the writer the launcher's words name, read with the value of each variable they
	// expand that the pod's env sets: listenerBinary for a word holding envoy-listener, or
	// ENVOY_MACHINE_ID or ENVOY_WEBHOOKS, which a launcher sets for a listener it starts
	// (marker says which); dispatchBinary for one holding envoy-dispatch; "" for neither.
	names, marker string
	// via is the expansion whose value names the writer, "" when a word itself does.
	via string
	// unread is the first expansion in the launcher's words, or in the command's first word, of a
	// variable the pod's env sets from a Secret, a ConfigMap or an envFrom source, whose value the
	// pod does not show: the program it starts may be any writer.
	unread string
}

// listener reports whether the launcher's words name a listener.
func (l launch) listener() bool { return l.names == listenerBinary }

// launch reads what c's command and args start, as the kubelet runs them. Each word is first
// expanded as the kubelet expands it (kubeExpand): a $(VAR) reference becomes the value of VAR
// where c's env sets it (environment), and $$ becomes $. A command whose first word is no writer
// binary - a shell, env, tini, dumb-init or any other program, or the image's entrypoint when c
// sets args alone, which Kubernetes appends to it - is a launcher, and its later words and its
// args are what it runs, such as the script of [/bin/sh, -c, 'ENVOY_MACHINE_ID=x exec
// envoy-listener'], the program of [env, ENVOY_MACHINE_ID=x, envoy-listener], or args
// [envoy-listener] under an entrypoint of [tini, --]. A shell expands its own $VAR and ${VAR}
// from the same environment, so the value of each variable c's env sets that those words expand is
// read beside them: [/bin/sh, -c, 'exec "$PROGRAM"'] with PROGRAM=/usr/local/bin/envoy-dispatch
// names envoy-dispatch. Any word or value holding a writer's name counts, so a launcher that only
// mentions it, such as a probe that reads the listener's URL, blocks too: the proof fails closed.
//
// An expansion of a variable c's env sets but whose value the pod does not show - one from a
// Secret or a ConfigMap, or one an envFrom source may set - is unread, and may start any writer,
// such as [$(PROGRAM)] or [tini, --] with args [$(PROGRAM)] where PROGRAM comes from a Secret:
// the proof fails closed on it. A variable c's env and envFrom do not set at all - a shell's own,
// such as a script's loop variable, or one the image's environment sets, such as TMPDIR - is the
// script's or the image's, which the proof does not read, as it does not read the image's
// entrypoint: a program a launcher picks from one, from a script file, or from a file or a
// command's output it reads gets past the proof (bestEffortWarning).
func (c podContainer) launch() launch {
	lookup := c.environment()
	var l launch
	var unread []string
	expand := func(words []string) []string {
		expanded := make([]string, len(words))
		for i, word := range words {
			var refs []string
			expanded[i], refs = kubeExpand(word, lookup)
			unread = append(unread, refs...)
		}
		return expanded
	}
	command, words := expand(c.Command), expand(c.Args)
	if len(command) > 0 {
		l.launcher = path.Base(command[0])
		switch l.launcher {
		case "envoy-listener", "envoy-listener-heldport":
			l.binary = listenerBinary
			return l
		case "envoy-dispatch":
			l.binary = dispatchBinary
			return l
		}
		words = slices.Concat(command[1:], words)
	}
	if len(unread) > 0 {
		l.unread = unread[0]
		return l
	}
	// Each text the launcher runs: its words, then the value of each variable they expand that c's
	// env sets, and of each such variable those values expand in turn (an eval reads them again).
	// The loop reads texts it appends to, so it ranges by index.
	type text struct{ via, value string }
	texts := []text{{value: strings.Join(words, "\n")}}
	seen := map[string]bool{}
	for i := 0; i < len(texts); i++ {
		for _, match := range shellExpansion.FindAllStringSubmatch(texts[i].value, -1) {
			// The expansion as a blocker names it: $NAME, or ${NAME} for any braced form.
			expansion := "$" + match[1]
			if strings.HasPrefix(match[0], "${") {
				expansion = "${" + match[1] + "}"
			}
			variable := lookup(match[1])
			switch {
			case !variable.set || seen[match[1]]:
			case !variable.read:
				l.unread = expansion
				return l
			default:
				seen[match[1]] = true
				texts = append(texts, text{via: expansion, value: variable.value})
			}
		}
	}
	for _, t := range texts {
		for _, marker := range []struct{ marker, binary string }{
			{"envoy-listener", listenerBinary}, {"ENVOY_MACHINE_ID", listenerBinary}, {"ENVOY_WEBHOOKS", listenerBinary}, {"envoy-dispatch", dispatchBinary},
		} {
			if strings.Contains(t.value, marker.marker) && (l.names == "" || l.names == dispatchBinary && marker.binary == listenerBinary) {
				l.names, l.marker, l.via = marker.binary, marker.marker, t.via
			}
		}
	}
	return l
}

// shellExpansion is a shell's expansion of a variable, $VAR or ${VAR...}, the variable's name
// captured. A special parameter ($1, $@, $?, $$ and the rest) takes its value from the words the
// shell is given or from the shell itself, which the proof reads already, so it is not one.
var shellExpansion = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)`)

// variable is a variable of a container as the kubelet sets it (environment): set when c's env or
// envFrom sets it, and read when the pod shows its value, value.
type variable struct {
	value     string
	set, read bool
}

// environment is how the kubelet sets c's variables, as far as the pod shows them
// (k8s.io/kubernetes pkg/kubelet/kubelet_pods.go, makeEnvironmentVariables): each envFrom source's
// keys, then each env entry in order, a later one of a name taking its place. An entry's value is
// expanded against the variables set before it (kubeExpand), and read only when every reference it
// holds is; one from a field of the pod is read from the pod (field), and one from a resource is a
// quantity, which names no program. One from a Secret or a ConfigMap, and any name an envFrom
// source may set that no entry sets, is set but not read. Any other name is not set by the pod.
func (c podContainer) environment() func(name string) variable {
	vars := map[string]variable{}
	lookup := func(name string) variable {
		if v, ok := vars[name]; ok {
			return v
		}
		for _, source := range c.EnvFrom {
			if strings.HasPrefix(name, source.Prefix) {
				return variable{set: true}
			}
		}
		return variable{}
	}
	for _, env := range c.Env {
		v := variable{set: true}
		switch {
		case env.ValueFrom == nil:
			var unread []string
			v.value, unread = kubeExpand(env.Value, lookup)
			v.read = len(unread) == 0
		case env.ValueFrom.FieldRef != nil:
			v.value, v.read = c.field(env.ValueFrom.FieldRef.FieldPath)
		case len(env.ValueFrom.ResourceFieldRef) > 0:
			v.read = true
		}
		vars[env.Name] = v
	}
	return lookup
}

// field is the value the kubelet gives a variable from fieldPath, a field of c's pod (the downward
// API's fields a variable may take): its name, namespace, uid, a label or an annotation, its
// node, its service account, or an address, which names no program. read is false for any other
// path, and for a container read with no pod.
func (c podContainer) field(fieldPath string) (value string, read bool) {
	if c.pod == nil {
		return "", false
	}
	metadata := c.pod.Metadata
	switch fieldPath {
	case "metadata.name":
		return metadata.Name, true
	case "metadata.namespace":
		return metadata.Namespace, true
	case "metadata.uid":
		return metadata.UID, true
	case "spec.nodeName":
		return c.pod.Spec.NodeName, true
	case "spec.serviceAccountName":
		return c.pod.Spec.ServiceAccountName, true
	case "status.hostIP", "status.hostIPs", "status.podIP", "status.podIPs":
		return "", true
	}
	for prefix, values := range map[string]map[string]string{"metadata.labels": metadata.Labels, "metadata.annotations": metadata.Annotations} {
		if key, ok := strings.CutPrefix(fieldPath, prefix+"['"); ok {
			if key, ok = strings.CutSuffix(key, "']"); ok {
				return values[key], true
			}
		}
	}
	return "", false
}

// kubeExpand is word as the kubelet expands a container's command, args and env values
// (k8s.io/kubernetes third_party/forked/golang/expansion, Expand): $$ becomes $, and $(NAME)
// becomes NAME's value where lookup says the pod sets it. A reference to a name the pod does not
// set, and one incomplete, stays as it is. unread is each reference to a variable the pod sets but
// whose value it does not show, which stays as it is too. The loop steps over what each $ starts,
// so it moves its own index.
func kubeExpand(word string, lookup func(name string) variable) (expanded string, unread []string) {
	var out strings.Builder
	for i := 0; i < len(word); i++ {
		if word[i] != '$' || i+1 == len(word) {
			out.WriteByte(word[i])
			continue
		}
		switch word[i+1] {
		case '$':
			out.WriteByte('$')
		case '(':
			end := strings.IndexByte(word[i+2:], ')')
			if end < 0 {
				out.WriteString("$(")
				break
			}
			reference := word[i : i+3+end]
			switch v := lookup(word[i+2 : i+2+end]); {
			case v.set && v.read:
				out.WriteString(v.value)
			case v.set:
				unread = append(unread, reference)
				out.WriteString(reference)
			default:
				out.WriteString(reference)
			}
			i += 2 + end
			continue
		default:
			out.WriteByte('$')
			out.WriteByte(word[i+1])
		}
		i++
	}
	return out.String(), unread
}

// launchNote is what a blocker on c, which writes written of the buckets, adds when its launch is
// what makes c a writer of them (launch): the expansion whose value the pod does not show, or the
// writer the launcher's words name and the launcher, quoted since a pod's words can hold any bytes,
// and for a listener that the launcher may set the variables the writer map reads, which the pod
// does not show. envoy-dispatch is named only on a blocker of the CI state bucket, the one bucket
// it adds. It is "" for any other container.
func (c podContainer) launchNote(written []string) string {
	started := c.launch()
	through := "the image's entrypoint"
	if started.launcher != "" {
		through = fmt.Sprintf("%q", started.launcher)
	}
	named := started.marker
	if started.via != "" {
		named = fmt.Sprintf("%s through %q, which the pod's env sets", started.marker, started.via)
	}
	switch {
	case started.unread != "":
		return fmt.Sprintf(" (its command or args expand %q, whose value the pod does not show, so it may start any writer)", started.unread)
	case started.listener():
		return fmt.Sprintf(" (its command or args name %s, run through %s, which may set ENVOY_MACHINE_ID and ENVOY_WEBHOOKS where the pod does not show them)", named, through)
	case started.names == dispatchBinary && slices.Contains(written, bus.CIStateBucket):
		return fmt.Sprintf(" (its command or args name %s, run through %s)", named, through)
	}
	return ""
}

// namesGitHub reports whether value, ENVOY_WEBHOOKS as a container's env sets it, may mount the
// listener's GitHub route: one of its comma-separated entries is github once strings.TrimSpace
// trims it, as the listener reads it (internal/webhook's LoadWebhookConfig), or it holds a $(VAR)
// reference, which Kubernetes replaces with another variable's value before the listener reads it.
func namesGitHub(value string) bool {
	return strings.Contains(value, "$(") || slices.ContainsFunc(strings.Split(value, ","), func(entry string) bool { return strings.TrimSpace(entry) == "github" })
}

// envFromMaySet reports whether c's envFrom may set the variable name: a source's prefix ("" when
// it has none) starts name, so one of the source's keys could become it, and c's env does not set
// name, which Kubernetes lets take precedence over every envFrom source.
func (c podContainer) envFromMaySet(name string) bool {
	if slices.ContainsFunc(c.Env, func(env envVar) bool { return env.Name == name }) {
		return false
	}
	for _, source := range c.EnvFrom {
		if strings.HasPrefix(name, source.Prefix) {
			return true
		}
	}
	return false
}

// writerProcesses are the names a writer's process runs as on a machine (its /proc/<pid>/comm, the
// executable's name cut to 15 bytes), as shell case patterns: every name this repository builds or
// runs the listener as - envoy-listener (docker/Dockerfile, the plugins, every e2e script),
// listener (`go run ./cmd/listener`, scripts/dev-stack.sh, scripts/e2e-refill.sh) and the held-port
// wrapper's envoy-listener-heldport, whose comm is cut to envoy-listener- - and, when the CI state
// bucket is among the buckets, every name it runs Dispatch as: envoy-dispatch, dispatch, and the
// dispatch-<build> names of the e2e scripts' two-build comparisons (dispatch-e2e, dispatch-head,
// dispatch-base). A binary renamed to anything else is not recognised; that is one of the ways a
// writer gets past this proof (bestEffortWarning).
func (f fence) writerProcesses() []string {
	names := []string{"envoy-listener", "envoy-listener-*", "listener"}
	if f.dispatch {
		names = append(names, "envoy-dispatch", "dispatch", "dispatch-*")
	}
	return names
}

// writerConnections are the names a writer's NATS connections carry: every listener's role lane
// and its session consumers' (internal/bus), and Dispatch's. The listener's main connection carries
// the bus's default name, which other programs share, so the role lane, which every listener
// opens, is what names it.
func (f fence) writerConnections() []string {
	names := []string{"envoy-role-lane", "envoy-session-consumers"}
	if f.dispatch {
		names = append(names, "dispatch")
	}
	return names
}

// prove takes every proof the fence was given and returns what blocks: nothing when every writer
// is proven ended. It writes each proof it took to out.
func (f fence) prove(ctx context.Context, out io.Writer, js nats.JetStreamContext) []string {
	var blockers []string
	if f.firstDeployment {
		blockers = append(blockers, firstDeploymentBlockers(js)...)
		if len(blockers) == 0 {
			fmt.Fprintln(out, "proven: the server holds no notification stream and no copied bucket, so no listener has connected to it")
		}
	}
	for _, kube := range f.kubeContexts {
		blockers = append(blockers, f.provePods(ctx, out, kube)...)
	}
	for _, host := range f.hosts {
		blockers = append(blockers, f.proveHost(ctx, out, host)...)
	}
	if f.thisMachine {
		blockers = append(blockers, f.proveThisMachine(ctx, out)...)
	}
	for _, monitor := range f.connz {
		blockers = append(blockers, f.census(ctx, out, monitor)...)
	}
	return blockers
}

// firstDeploymentBlockers is what keeps a server from being a first deployment's: the
// notification stream, which every listener and envoy-dispatch create at their first connect, or
// any copied bucket.
func firstDeploymentBlockers(js nats.JetStreamContext) []string {
	var blockers []string
	for _, stream := range append([]string{bus.Stream}, prefixed(bus.CopiedBuckets())...) {
		_, err := js.StreamInfo(stream)
		switch {
		case errors.Is(err, nats.ErrStreamNotFound):
		case err != nil:
			blockers = append(blockers, fmt.Sprintf("--first-deployment: could not read %s: %v", stream, err))
		default:
			blockers = append(blockers, fmt.Sprintf("--first-deployment: the server holds %s, so a writer has run against it: name the writers to prove ended instead", stream))
		}
	}
	return blockers
}

func prefixed(buckets []string) []string {
	streams := make([]string, len(buckets))
	for i, bucket := range buckets {
		streams[i] = "KV_" + bucket
	}
	return streams
}

// subprocessTimeout bounds each kubectl and ssh call the proof makes: a connected API request or
// remote command that stops answering is killed at it (exec.CommandContext) and blocks the proof,
// rather than holding the command until the operator interrupts it. kubectl's own request timeout
// (kubectlRequestTimeout) sits inside it, so kubectl reports its own expiry first. A test lowers it.
var subprocessTimeout = time.Minute

// kubectlRequestTimeout is kubectl's --request-timeout, within subprocessTimeout.
const kubectlRequestTimeout = "45s"

// call runs name with args through the fence's runner within subprocessTimeout, and answers a call
// that did not finish in it as an error naming the bound.
func (f fence) call(ctx context.Context, name string, args ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, subprocessTimeout)
	defer cancel()
	out, err := f.run(bounded, name, args...)
	if errors.Is(bounded.Err(), context.DeadlineExceeded) {
		return out, fmt.Errorf("%s did not finish within %s: %w", name, subprocessTimeout, bounded.Err())
	}
	return out, err
}

// kubectl runs kubectl in context kube ("" for the current one), with kubectl's own request
// timeout, within subprocessTimeout.
func (f fence) kubectl(ctx context.Context, kube string, args ...string) ([]byte, error) {
	args = append([]string{"--request-timeout=" + kubectlRequestTimeout}, args...)
	if kube != "" {
		args = append([]string{"--context", kube}, args...)
	}
	return f.call(ctx, "kubectl", args...)
}

// namespaceName is the name the Kubernetes API gives a namespace: an RFC 1123 label, at most 63
// lower-case letters, digits and '-', starting and ending with a letter or a digit. It is the
// shape routes' dnsLabel and the gcp components' namespace rule check; theirs are unexported, and
// gcp's lives in a package of Pulumi components this command does not link.
var namespaceName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// podList is the part of a Kubernetes PodList the fence reads.
type podList struct {
	Items []listedPod `json:"items"`
}

// listedPod is the part of a pod the fence reads: its init containers (native sidecars,
// restartPolicy Always, run beside its containers), containers and ephemeral containers, and their
// statuses, and the fields a container's variable may take (podContainer.field). A pod's containers
// of every kind hold names no other of them holds.
type listedPod struct {
	Metadata struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		UID         string            `json:"uid"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		NodeName            string         `json:"nodeName"`
		ServiceAccountName  string         `json:"serviceAccountName"`
		InitContainers      []podContainer `json:"initContainers"`
		Containers          []podContainer `json:"containers"`
		EphemeralContainers []podContainer `json:"ephemeralContainers"`
	} `json:"spec"`
	Status struct {
		InitContainerStatuses      []containerStatus `json:"initContainerStatuses"`
		ContainerStatuses          []containerStatus `json:"containerStatuses"`
		EphemeralContainerStatuses []containerStatus `json:"ephemeralContainerStatuses"`
	} `json:"status"`
}

// containerStatus is the part of a container's status the fence reads: whether it has terminated.
type containerStatus struct {
	Name  string `json:"name"`
	State struct {
		Terminated *struct{} `json:"terminated"`
	} `json:"state"`
}

// among is each of written that is one of buckets.
func among(written, buckets []string) []string {
	return slices.DeleteFunc(written, func(bucket string) bool { return !slices.Contains(buckets, bucket) })
}

// envFromNote is what a blocker on c adds when c's envFrom is what makes it a writer of more of
// buckets: the variables the writer map reads that the envFrom may set, which the pod does not
// show. It is "" when c's env and command alone name what it writes of buckets, and when the
// envFrom sets neither variable, but one c's command or args expand (launchNote names that).
func (c podContainer) envFromNote(buckets []string) string {
	shown := c
	shown.EnvFrom = nil
	if len(among(c.writes(), buckets)) == len(among(shown.writes(), buckets)) {
		return ""
	}
	var maySet []string
	for _, name := range []string{"ENVOY_MACHINE_ID", "ENVOY_WEBHOOKS"} {
		if c.envFromMaySet(name) {
			maySet = append(maySet, name)
		}
	}
	if len(maySet) == 0 {
		return ""
	}
	return fmt.Sprintf(" (its envFrom may set %s, which the pod does not show)", strings.Join(maySet, " and "))
}

// provePods proves, for one Kubernetes context, that no writer of the buckets runs in the
// deployment's namespaces: for each namespace, which must exist (a namespace the API does not
// hold lists no pod, and a misspelt name would prove nothing), no pod the API lists runs a
// container, an init container or an ephemeral container that has not terminated and may write
// one of the buckets (podContainer.writes). Each namespace reaches kubectl as namespace/<ns> and
// --namespace=<ns>, which kubectl never reads as a flag of its own. It reads the namespaces' pods
// through the API alone, which GKE Autopilot allows; a node's kubelet runtime list (nodes/proxy),
// which Autopilot refuses, is not read, so a pod deleted with --force whose containers still run
// is not seen (bestEffortWarning). Each read that fails blocks.
func (f fence) provePods(ctx context.Context, out io.Writer, kube string) []string {
	label := kube
	if label == "" {
		label = "the current context"
	}
	var blockers []string
	read := 0
	for _, namespace := range f.namespaces {
		if _, err := f.kubectl(ctx, kube, "get", "namespace/"+namespace, "-o", "name"); err != nil {
			blockers = append(blockers, fmt.Sprintf("kubernetes %s: namespace %s: read it: %v", label, namespace, err))
			continue
		}
		raw, err := f.kubectl(ctx, kube, "get", "pods", "--namespace="+namespace, "-o", "json")
		var pods podList
		if err == nil {
			err = json.Unmarshal(raw, &pods)
		}
		if err != nil {
			blockers = append(blockers, fmt.Sprintf("kubernetes %s: list pods in namespace %s: %v", label, namespace, err))
			continue
		}
		read += len(pods.Items)
		for _, pod := range pods.Items {
			statuses := slices.Concat(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses, pod.Status.EphemeralContainerStatuses)
			for _, kind := range []struct {
				name       string
				containers []podContainer
			}{
				{"init container", pod.Spec.InitContainers},
				{"container", pod.Spec.Containers},
				{"ephemeral container", pod.Spec.EphemeralContainers},
			} {
				for _, container := range kind.containers {
					container.pod = &pod
					written := among(container.writes(), f.buckets)
					if len(written) == 0 || slices.ContainsFunc(statuses, func(status containerStatus) bool {
						return status.Name == container.Name && status.State.Terminated != nil
					}) {
						continue
					}
					blockers = append(blockers, fmt.Sprintf("kubernetes %s: pod %s/%s (uid %s, node %s) runs %s %s, which writes %s%s%s",
						label, namespace, pod.Metadata.Name, pod.Metadata.UID, pod.Spec.NodeName, kind.name, container.Name, strings.Join(written, ", "), container.launchNote(written), container.envFromNote(f.buckets)))
				}
			}
		}
	}
	if len(blockers) == 0 {
		fmt.Fprintf(out, "proven: kubernetes %s: none of the %d pods in namespace %s runs a writer of %s\n", label, read, strings.Join(f.namespaces, ", "), strings.Join(f.buckets, ", "))
	}
	return blockers
}

// hostQueryDone ends a host query that ran to its end, after the count of the processes it read.
const hostQueryDone = "envoy-buckets-host-query-done"

// hostQuery is the shell the host query runs: each process whose name is a writer's, its pid and
// name, then how many processes it read and hostQueryDone. A process's name (/proc/<pid>/comm) is
// readable whoever runs the query, where its executable is not.
func hostQuery(names []string) string {
	return `n=0; for d in /proc/[0-9]*; do read -r c < "$d/comm" 2>/dev/null || continue; n=$((n+1)); case "$c" in ` +
		strings.Join(names, "|") + `) echo "${d#/proc/} $c";; esac; done; echo "processes=$n"; echo ` + hostQueryDone
}

// proveHost proves that no writer process runs on host, through ssh: the query ran to its end, read
// at least one process, and found none named as a writer (processBlockers).
func (f fence) proveHost(ctx context.Context, out io.Writer, host string) []string {
	raw, err := f.call(ctx, f.ssh, "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", host, hostQuery(f.writerProcesses()))
	if err != nil {
		return []string{fmt.Sprintf("host %s: unreachable or the query failed: %v", host, err)}
	}
	return f.processBlockers(out, "host "+host, raw)
}

// proveThisMachine proves that no writer process runs on the machine the command runs on: the host
// query, run here through sh, judged as a host's (processBlockers). In a container it reads only
// the PID namespace the container holds, which is the machine's with docker's --pid host. A command
// that is process 1 of its PID namespace is a container's entrypoint in a namespace of its own -
// on a machine, process 1 is its init - so its query would read the container's processes alone
// and report the machine clean: it blocks instead. A container whose entrypoint is something else
// (a shell that runs the command) is not caught this way, which the usage and the runbook say.
func (f fence) proveThisMachine(ctx context.Context, out io.Writer) []string {
	if f.pid == 1 {
		return []string{"this machine: the command is process 1 of its PID namespace, so it runs in a container that does not share the machine's, and its query would read that container's processes alone: run it with docker's --pid host, or compose's pid: host"}
	}
	raw, err := f.call(ctx, "sh", "-c", hostQuery(f.writerProcesses()))
	if err != nil {
		return []string{fmt.Sprintf("this machine: the query failed: %v", err)}
	}
	return f.processBlockers(out, "this machine", raw)
}

// processBlockers judges raw, what the host query printed on place: it blocks unless the query ran
// to its end and read at least one process, and on each process named as a writer.
func (f fence) processBlockers(out io.Writer, place string, raw []byte) []string {
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 2 || lines[len(lines)-1] != hostQueryDone {
		return []string{fmt.Sprintf("%s: the query did not run to its end: %q", place, string(raw))}
	}
	count, err := strconv.Atoi(strings.TrimPrefix(lines[len(lines)-2], "processes="))
	if err != nil || count == 0 {
		return []string{fmt.Sprintf("%s: the query read no process at all, so it proves nothing: %q", place, string(raw))}
	}
	var blockers []string
	for _, line := range lines[:len(lines)-2] {
		blockers = append(blockers, fmt.Sprintf("%s: process %s runs", place, line))
	}
	if len(blockers) == 0 {
		fmt.Fprintf(out, "proven: %s: none of its %d processes is %s\n", place, count, strings.Join(f.writerProcesses(), " or "))
	}
	return blockers
}

// connzPage is the part of a /connz page the census reads.
type connzPage struct {
	Total       int `json:"total"`
	Offset      int `json:"offset"`
	Connections []struct {
		Name string `json:"name"`
		IP   string `json:"ip"`
		Port int    `json:"port"`
		User string `json:"authorized_user"`
	} `json:"connections"`
}

// census reads every page of monitor's /connz and blocks on each connection named as a writer's.
// It proves nothing on its own: a frozen process holds no connection, so it finds a live writer no
// inventory named and is no substitute for one.
func (f fence) census(ctx context.Context, out io.Writer, monitor string) []string {
	names := f.writerConnections()
	var blockers []string
	read := 0
	for offset := 0; ; {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(monitor, "/")+"/connz?auth=true&limit=1024&offset="+strconv.Itoa(offset), nil)
		if err != nil {
			cancel()
			return append(blockers, fmt.Sprintf("connz %s: %v", monitor, err))
		}
		response, err := f.http.Do(request)
		var page connzPage
		if err == nil {
			err = json.NewDecoder(response.Body).Decode(&page)
			_ = response.Body.Close()
		}
		cancel()
		if err != nil {
			return append(blockers, fmt.Sprintf("connz %s: %v", monitor, err))
		}
		for _, connection := range page.Connections {
			if slices.Contains(names, connection.Name) {
				blockers = append(blockers, fmt.Sprintf("connz %s: connection %q from %s:%d as %q is open", monitor, connection.Name, connection.IP, connection.Port, connection.User))
			}
		}
		read += len(page.Connections)
		offset += len(page.Connections)
		if len(page.Connections) == 0 || offset >= page.Total {
			break
		}
	}
	if len(blockers) == 0 {
		fmt.Fprintf(out, "census: connz %s: none of %d connections is named %s (this adds to the proof above and is none on its own)\n", monitor, read, strings.Join(names, " or "))
	}
	return blockers
}
