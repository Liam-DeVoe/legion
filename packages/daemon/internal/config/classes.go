package config

import (
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

// Class is when a change of one setting takes effect, which is what the daemon can do with the new
// value while it runs.
type Class string

const (
	// ClassDecision is a value read per decision: the change takes effect at once, with nothing
	// rebuilt.
	ClassDecision Class = "decision"
	// ClassRebuild is a value a client, a listener or a connection holds: the change takes effect
	// once that holder is built again, which Row.Holder names.
	ClassRebuild Class = "rebuild"
	// ClassLaunch is a value a pod's or a pane's own launch reads: the change reaches a role at its
	// next launch. Row.Holder names the holder that must be built again first, because the holder
	// is what the next launch reads the value from.
	ClassLaunch Class = "launch"
	// ClassFixed is a value nothing can change under a running daemon: the route refuses the path
	// by name, with Row.Why, and every resolution of the file pins the path back to the value
	// boot settled (live.go's pinFixed), so no snapshot carries a fixed value the daemon is not
	// running on and an operator's edit of such a key reaches the next boot and nothing before it.
	ClassFixed Class = "fixed"
)

// Outcome is what one applied change means for the caller that asked for it.
type Outcome string

const (
	OutcomeAtOnce     Outcome = "at-once"
	OutcomeRebuilt    Outcome = "rebuilt"
	OutcomeNextLaunch Outcome = "next-launch"
)

// Outcome is the class's outcome. A fixed path is refused rather than applied, so it has none.
func (c Class) Outcome() Outcome {
	switch c {
	case ClassDecision:
		return OutcomeAtOnce
	case ClassRebuild:
		return OutcomeRebuilt
	case ClassLaunch:
		return OutcomeNextLaunch
	}
	return ""
}

// Credential is which of three things the value of a Secret row is, which decides one question
// Secret does not: whether the settings route may take the value. The route keeps every change it
// applies in a database every project's daemon shares, so that the change outlives the daemon's
// restart (internal/settings' Keeper.Route), and what may be kept there is where a credential
// lives, never the credential (CarriesCredential).
type Credential string

const (
	// CredentialLocator is a value that says where a credential lives and holds none: a file's
	// path, the name of a secretsd key or of a providers Secret's key, a command that prints one,
	// a context of a kubeconfig. The route stores it like any other change, so it survives a
	// restart until the file changes that key.
	CredentialLocator Credential = "locator"
	// CredentialAddress is an address, or text holding addresses, whose userinfo may carry a
	// credential, and a query parameter Row.SecretQuery names may too. The value the call carries
	// decides (CarriesCredential): one that carries a credential is refused by name, pointing at
	// Row.CredentialIn, and one that carries none is stored as a locator is.
	CredentialAddress Credential = "address"
	// CredentialInlined is the credential itself. The route refuses every value of it by name,
	// pointing at Row.CredentialIn, and the file arm takes it, since the file is where an
	// operator writes it and the daemon keeps nothing of the file but a digest.
	CredentialInlined Credential = "inlined"
)

// The holders a rebuild names. Each is one thing the daemon builds from the configuration and can
// build again without stopping the work it is doing: a short stable string, because an operator
// reads it in a log line and the daemon's rebuild observer switches on it.
const (
	// HolderNATS is the NATS connection and its durable consumers.
	HolderNATS = "nats"
	// HolderDispatchClient is the Dispatch client every workflow call goes through.
	HolderDispatchClient = "dispatch-client"
	// HolderEnvoyPublisher is the Envoy listener's client, which every notice, the clean stop of
	// a dead role's session and the controller's liveness probe go through.
	HolderEnvoyPublisher = "envoy-publisher"
	// HolderGitHubApps is the GitHub App credential manager.
	HolderGitHubApps = "github-apps"
	// HolderAPIServer is the settings the API server holds; its socket is never rebound.
	HolderAPIServer = "api-server"
	// HolderWorkerStream is the worker stream listener's held settings; its socket is never
	// rebound and no worker connection drops.
	HolderWorkerStream = "worker-stream"
	// HolderControllerKeeper is the controller tick's wake, which the keeper's holder keeps. The
	// keeper itself is settled at boot (`controller` is fixed), so the tick is all it rebuilds.
	HolderControllerKeeper = "controller-keeper"
	// HolderRuntime is the process runtime, Sandbox or tmux, which holds everything a launch reads.
	HolderRuntime = "runtime"
	// HolderLaunchSpecs is the launch specs the supervisor builds each claim's process from.
	HolderLaunchSpecs = "launch-specs"
	// HolderSecretsLogin is the daemon's own login at the secrets broker
	// (`runtime.kubernetes.agent_secrets`), which logs in with the daemon's own token and enrolls
	// every pod the runtime launches. A Kubernetes daemon holds it, with no login while the
	// configuration names no broker.
	HolderSecretsLogin = "agent-secrets-login"
	// HolderRepositoryMapping is the project's repository mapping, which holds the deployment's
	// repository equivalence (`repository_renames`) it compares two names with.
	HolderRepositoryMapping = "repository-mapping"
	// HolderGatedSteps is the project's Dispatch row, which carries the design-gate steps the
	// deployment gates (`gates.design`) so a person reading Dispatch sees what it holds a
	// tree's work for. The gate itself is read per decision; the row is the one copy of the
	// list, and only a daemon with a workflow holds it.
	HolderGatedSteps = "gated-steps"
)

// Row is one settable unit of `legion.yaml`, keyed by the Go field it is read into.
type Row struct {
	// Field is the field the value lands in, `<Type>.<Field>`, exactly as
	// api/settings_test.go's settingsEmitted writes it. It is the key the census test holds the
	// table to, so a field added to Config with no row fails that test.
	Field string
	// Path is the dotted path `legion.yaml` writes the unit as. A segment in angle brackets is a
	// name the file chooses (`projects.<KEY>.slack_channels`), and a `[]` suffix one entry of a
	// sequence (`runtime.kubernetes.scheduling.tolerations[].key`); ClassOf matches a concrete
	// path against both. Two rows share a path where one key fills more than one field.
	Path string
	// Class is when a change of the unit takes effect.
	Class Class
	// Holder is what must be built again for the change to reach anything, "" when nothing holds
	// the value. Required on a rebuild row; a launch row names the holder the next launch reads
	// the value from. It is the one holder an operator is told was rebuilt (Change.Holder); a
	// value more than one thing holds names the rest in Also.
	Holder string
	// Also are the other holders a change of this row must build again, beside Holder: each is
	// built again in the daemon's registration order, and none is named in the answer the caller
	// reads, which says what the change did and not how many things the daemon holds it in.
	Also []string
	// Why is why a fixed path is refused, as the refusal says it; "" for every other class.
	Why string
	// Block is whether the field is a block the diff descends into rather than a value it
	// compares: the paths that change are the block's own members'. Path is still the block's own
	// key, which ClassOf answers for a caller that sets the whole block.
	Block bool
	// Keyed is whether the field is a map of blocks under names the file chooses itself, each
	// name the `<KEY>` segment of its members' paths. The walk visits such a field whole, as one
	// value at its own path, and then each member of each entry at the path the entry's name
	// makes of the member's row (live.go's WalkSettings, Row.PathAt), so the diff reports the
	// map's own change beside each member's that moved, a pod records a launch member its launch
	// reads, and the pin of the fixed paths refuses a fixed member by its own path with the
	// file's name written into it (live.go's pinKeyed). A row is Keyed or Block and never both:
	// Block is one block the diff descends into and never compares whole, Keyed a map it
	// compares whole and descends into as well.
	Keyed bool
	// Secret is whether the value is a credential, the source of one, the path of a file holding
	// one, or an address whose userinfo or query may carry one. Such a value renders as
	// "(withheld)" in a Change, never as itself: the judgement is api.SettingsOf's, which leaves
	// each of them out of the unauthenticated state (api/settings.go, api/settings_test.go's
	// settingsExcluded).
	Secret bool
	// Credential is which of three things a Secret row's value is, which is where the value may
	// be kept rather than how it is shown: every Secret row names one and no other row does. A
	// block row names the kind of what it holds beside the members that have rows of their own,
	// since a value set at the block is judged member by member (CarriesCredential): an App is a
	// locator, and its `private_key` member the credential itself.
	Credential Credential
	// CredentialIn is the keys of legion.yaml the credential belongs in where the value of an
	// address or inlined row would carry it, which the route's refusal names. Nil on a locator
	// row, and on a row no key of the file holds its credential apart from, whose refusal says so.
	CredentialIn []string
	// SecretQuery is the query parameters of an address row's value that carry a credential, as
	// the daemon's own reading of that value names them (api.pluginSourceOf); nil where nothing
	// names one, which is every address the loader refuses a query on.
	SecretQuery []string
}

// PathAt is this row's path with the name the file chose written into the one `<name>` segment
// a row under a keyed map carries: `projects.<KEY>.memory.mode` at the key DEMO is
// `projects.DEMO.memory.mode`, which is the path a refusal of that project's member names,
// since no operator can act on a template. The path itself for an empty key and for a row that
// carries no such segment.
func (r Row) PathAt(key string) string {
	if key == "" {
		return r.Path
	}
	segments := strings.Split(r.Path, ".")
	for i, segment := range segments {
		if strings.HasPrefix(segment, "<") && strings.HasSuffix(segment, ">") {
			segments[i] = key
			return strings.Join(segments, ".")
		}
	}
	return r.Path
}

// The reasons the fixed paths are refused, named here because the loader's own refusals quote
// them and a test holds each to its row.
const (
	whyProject  = "the project token names every claim, every record in the store and the daemon's own registry entry: a daemon that changed it would own none of the work it is running"
	whyAddress  = "the API's listening socket is bound at boot, and LEGION_LISTEN_FD may hand the daemon one it never bound; there is nothing to rebuild and no way to rebind under the requests it is serving"
	whyStream   = "the worker stream's listening socket is bound at boot and every running shim holds a connection to it; rebinding it would cut every worker off"
	whyState    = "every running claim names files under it — its prompt, its secrets, its session and its workspace — and nothing moves them"
	whyStore    = "the store's pool is connected at boot and holds every record the daemon is working from"
	whyRuntime  = "the processes the daemon supervises are that runtime's, panes of its own tmux server or pods of its Sandboxes, and no running process moves to the other"
	whyNamespc  = "every Sandbox, pod, volume, Service and Secret the daemon works with lives in it: a daemon that changed it would see none of the pods it is supervising"
	whyCacheVol = "a repository cache's volume is its Sandbox's claim template, made with the cache and kept through every replacement of its pod, so a new size reaches only a cache made after a restart and never one the daemon is already serving from"
	whyTakeOve  = "it is read once at boot, when the daemon adopts the claims of the tmux server it takes over; a later change has nothing left to adopt"
	whyIdleRel  = "the idle release is settled at boot into the workflow's idle pass and the outbox's backstop, and the daemon builds neither again: a change of it reaches the next boot and nothing before it"
	whyMemory   = "a tree's memory group is drawn once, as the tree is opened, under the mode admission and the API server were given at boot, and the instructions are read at boot into the launch specs: a change of either reaches the next boot and no tree already drawn"
	whyExperim  = "each unit is drawn once, as the daemon opens a tree or first records an issue, under the experiments admission, the workflow engine and the API server were given at boot, and each arm's instructions are copied into the state directory at boot, where every launch reads them: a change of any of them reaches the next boot and no unit already drawn"
	whyPodSweep = "the pod-update sweep reads both limits off the configuration the daemon booted on, and nothing builds that reader again: a change of either reaches the next boot and no sweep before it"
	whyPodGrace = "an update round states its end, its opening plus this bound, in the ask it sends every role (internal/daemon's askRound), so the sweep reads the bound off the configuration the daemon booted on (internal/daemon's supervision.cfg, roundBound) and every role a round asks within one boot is told the same end, the one the round is then held to: a change of it reaches the next boot and no sweep before it"
	whyWakePace = "the daemon builds its one wake limiter at boot and hands it to the workflow engine and to every machine it supervises, and it builds none of them again: a change of either reaches the next boot and no wake before it"
	whyStallBst = "the stall backstop reads the role's bound off the configuration the daemon booted on (internal/daemon's supervision.cfg, backstopStalledTurns), and nothing builds that reader again: a change of it reaches the next boot and no sweep before it"
	whySilence  = "the silence sweep reads both bounds off the configuration the daemon booted on (internal/daemon's supervision.cfg, tellSilentPhaseWorkers), and admission is handed the same two at boot for the tick's silent list (admit's UseWorkerSilence, internal/daemon's openWorkflow), and nothing builds either reader again: a change of either reaches the next boot and no sweep or tick before it"
	whyStalled  = "the stalled-tree notice's bound is handed to admission once, at boot (admit's UseStalledTreeNotice, internal/daemon's openWorkflow), and nothing builds admission again: a change of it reaches the next boot and no admission pass before it"
	whyHooks    = "the hook list the engine fires is built once, at boot, from the file's hooks and the words read then for every hook that names a prompt_file (internal/daemon's readBoot, engineHooks and openWorkflow), and the daemon builds it again at no later moment: a change of it reaches the next boot and no firing before it"
	whyCtlMode  = "the daemon decides at boot whether it launches the project's controller itself or leaves it to the operator, and a switch under a running tree would start or stop a controller beneath it, where a mistake is two controllers or none: the switch is rare and deliberate, so a change of it reaches the next boot and nothing before it"
	whyReview   = "the review App's login is read once, at boot, and the engine, every review body and the check of a reviewer's write access judge by it (internal/daemon's appLoginsAtBoot, engineConfig, reviewBody and reviewerCanWrite): another App under open rounds would change whose reviews decide them, and a mistake there misattributes a verdict, so a change of any key of it reaches the next boot and nothing before it"
)

// Caller is which arm asked for a change through the settings route: what that caller may set.
//
// It is the rule of INFRA-1077's spec, "Which settings a projected token may change": the
// operator bearer and the daemon's own file may change every key except those fixed for the
// process, and a projected ServiceAccount token — Dispatch's path, whose caller is any
// deployment-scoped Dispatch user, a person or an agent — only a key whose value is plain data
// (Row.Plain), which a `gates` key is not: the API server and the launch specs each hold the
// design gate.
type Caller string

const (
	// CallerOperator is the operator bearer, and the boot's own restore of what the store kept:
	// every path the loader takes, which is every row but one fixed for the process.
	CallerOperator Caller = "operator"
	// CallerProjected is a caller holding a projected ServiceAccount token: only a row whose
	// value is plain data (Row.Plain). Every other path is refused by name, with the reason its
	// row gives it (Caller.Admits).
	CallerProjected Caller = "projected"
)

// The two reasons a projected ServiceAccount token is refused a path, by what the value is. Both
// are quoted in a Refusal, so a caller reads which of the two it met.
const (
	whyNotData = "a projected ServiceAccount token changes only the settings the daemon reads at each decision, and this one names a command to run, a credential, the file holding one, or an address the daemon connects to"
	whyNotRead = "a projected ServiceAccount token changes only the settings the daemon reads at each decision, and this one is held by a client, a listener, a launch, or the rule of who may call this route"
)

// Admits is whether this caller may set path, and why not when it may not. A path the class
// table does not hold is admitted here and refused by Apply as an unknown setting, so no caller
// learns from this answer alone which paths the daemon has.
func (c Caller) Admits(path string) (bool, string) {
	row, known := ClassOf(path)
	if !known || c != CallerProjected {
		return true, ""
	}
	if row.Plain() {
		return true, ""
	}
	// A path fixed for the process is refused as the apply refuses it to every caller, with its
	// row's own reason: the two below say what this caller may not set, and no caller sets it.
	if row.Class == ClassFixed {
		return false, row.Why
	}
	if row.Secret {
		return false, whyNotData
	}
	return false, whyNotRead
}

// Plain is whether this row's value is plain data: a value the daemon reads at each decision,
// holds nowhere, and hands to nothing else — no command it runs, no credential, no file holding
// one, no address it connects to or listens on, and none of the rules of who may call the
// settings route.
//
// It is a property of the row and no second list: the class says the daemon reads the value as
// it decides rather than handing it to a client, a listener, a launch or a command, which leaves
// out every command, address, listener and `settings_route` key, each of them held; Holder says
// something holds a copy of it all the same, which is what a `gates` key does — the API server
// and the launch specs each hold the design gate, and a gate is the rule that says a person
// decides a tree's specification, so a caller holding a token the whole service shares is not
// the party that may switch it off; and Secret says the value is a credential, the source of
// one, the path of a file holding one, or an address that may carry one. Today the rows it
// admits are `admission_cap`, the supervision limits (`launch_failure_limit`,
// `prompt_failure_limit`, `prompt_retire_limit`, `tree_stop_timeout_seconds`), the workflow's
// own bounds (`linger_hours`, `production_check_hours`, `review_round_cap`) and `capabilities`
// with `capabilities.decided`.
func (r Row) Plain() bool { return r.Class == ClassDecision && !r.Secret && r.Holder == "" }

// userinfo is a URL authority whose userinfo is not empty, `scheme://userinfo@`, anywhere in a
// text. It is where the loader has a NATS or an Envoy credential live (endpointURL: "use URL
// userinfo or a secret for credentials"), and what the state masks as `xxxxx@`
// (api.endpointOf). It is matched in the text rather than parsed out of it, because a variable's
// value may hold several addresses (`nats://a@h1,nats://b@h2`) that no parse of one URL reads,
// and a `/`, `?` or `#` ends an authority, so an `@` in a path or a query is no userinfo.
var userinfo = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s/?#@]+@`)

// CarriesCredential is whether the settings route refuses value for path because the value
// carries a credential, and the refusal it answers. The route keeps every change it applies in a
// database every project's daemon shares, so a credential is never taken through it whoever
// asks, and the refusal names the key the credential belongs in (Row.CredentialIn) or says that
// no key holds it. The file arm takes such a value, since the file is where an operator writes
// it and the daemon keeps nothing of the file but a digest.
//
// It reads the value the call carries and nothing else, so a value that carries no credential is
// never refused here whatever its key is called:
//
//   - at an inlined row (CredentialInlined), every value but null, which withdraws the key and
//     carries nothing;
//   - at an address row (CredentialAddress), a value holding a string with a non-empty userinfo
//     in a URL authority (userinfo), or a URL with a query parameter Row.SecretQuery names; an
//     address with neither is a locator, and is stored;
//   - a mapping member by member, each at the path it sits at, and a list entry by entry, at
//     `path[i]`, so a block set whole is judged by every member it carries: `github_apps` set
//     with an inline private key is refused naming `github_apps.implement.private_key`.
//
// A path fixed for the process is not judged: Live.Apply refuses it to every caller with its own
// row's reason, which is the answer to what the caller asked. A fixed member of a block the call
// sets whole is judged, since the block's whole value is what the route would keep.
func CarriesCredential(path string, value any) (string, bool) {
	if row, known := ClassOf(path); known && row.Class == ClassFixed {
		return "", false
	}
	return carriedCredential(path, reflect.ValueOf(value))
}

// carriedCredential is CarriesCredential's walk of one value at the path it sits at.
func carriedCredential(path string, value reflect.Value) (string, bool) {
	value = held(value)
	if !value.IsValid() {
		return "", false
	}
	if row, known := ClassOf(path); known {
		switch row.Credential {
		case CredentialInlined:
			return credentialRefusal(path, "is the credential itself", row), true
		case CredentialAddress:
			if where := carriedIn(value, row.SecretQuery); where != "" {
				return credentialRefusal(path, "carries a credential in "+where, row), true
			}
		}
	}
	switch value.Kind() {
	case reflect.Map:
		for _, name := range namesOf(value) {
			if why, carried := carriedCredential(path+"."+name.String(), value.MapIndex(name)); carried {
				return why, true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range value.Len() {
			if why, carried := carriedCredential(fmt.Sprintf("%s[%d]", path, i), value.Index(i)); carried {
				return why, true
			}
		}
	}
	return "", false
}

// carriedIn is where an address row's value carries a credential, as the refusal says it, ""
// where it carries none. It reads every string the value holds — the value itself, a list's
// entries, a mapping's values, never a mapping's keys, which are names — for a URL authority
// with userinfo (userinfo), and, where query names any parameter, for a URL carrying one of them.
func carriedIn(value reflect.Value, query []string) string {
	value = held(value)
	switch value.Kind() {
	case reflect.String:
		text := value.String()
		if userinfo.MatchString(text) {
			return "an address's userinfo"
		}
		if len(query) == 0 {
			return ""
		}
		if parsed, err := url.Parse(text); err == nil {
			carried := parsed.Query()
			for _, name := range query {
				if carried.Has(name) {
					return "the query parameter " + name
				}
			}
		}
	case reflect.Map:
		for _, name := range namesOf(value) {
			if where := carriedIn(value.MapIndex(name), query); where != "" {
				return where
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range value.Len() {
			if where := carriedIn(value.Index(i), query); where != "" {
				return where
			}
		}
	}
	return ""
}

// held is the value an interface or a pointer holds, followed to the end, and the zero Value for
// a nil one: a route change arrives decoded from JSON, and a test's from Go values of its own.
func held(value reflect.Value) reflect.Value {
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return reflect.Value{}
		}
		value = value.Elem()
	}
	return value
}

// namesOf is a mapping's keys in order, so two walks of one value refuse it with one reason; a
// mapping keyed by anything but a string is no block of legion.yaml and has none.
func namesOf(mapping reflect.Value) []reflect.Value {
	if mapping.Type().Key().Kind() != reflect.String {
		return nil
	}
	names := mapping.MapKeys()
	slices.SortFunc(names, func(a, b reflect.Value) int { return strings.Compare(a.String(), b.String()) })
	return names
}

// credentialRefusal is the route's refusal of path, whose value is or carries a credential as
// what says, naming where the credential belongs instead. A row whose credential no other key of
// the file holds says so, and that the change belongs in the file.
func credentialRefusal(path, what string, row Row) string {
	const kept = ", and the settings route keeps what it applies in a database every project's daemon shares"
	const file = "legion.yaml, which the daemon reads and does not store"
	in := strings.Join(row.CredentialIn, " or ")
	switch {
	case in == "":
		return path + " " + what + kept + ", while no other key of legion.yaml holds that credential: this change belongs in " + file
	case row.Credential == CredentialInlined:
		return path + " " + what + kept + ": name where it lives in " + in + ", or write it in " + file
	default:
		return path + " " + what + kept + ": put the credential in " + in + " and send the address without it, or write this change in " + file
	}
}

// Classes is every settable unit of `legion.yaml`, one row per field the loader reads it into,
// except a GitHub App's fields, which have one row under each App, since the two Apps' classes
// differ.
//
// The class is decided from what the daemon does with the value, read from its readers: where a
// field is read in more than one place the strictest class wins (fixed over rebuild over launch
// over decision) and the row says so. Holder is a second axis: it names what the daemon must build
// again for the new value to reach anything, which a launch row has too, since a launch reads the
// value from the runtime that launches it.
//
// A field of a nested block carries that block's whole path, so the table is flat: the diff walks
// Config and reads each field's path here, descending into a Block row rather than comparing it.
var Classes = []Row{
	// The identity and the sockets: boot bound them and the work in flight is tied to them.
	{Field: "Config.Project", Path: "project", Class: ClassFixed, Why: whyProject},
	{Field: "Config.Port", Path: "port", Class: ClassFixed, Why: whyAddress},
	{Field: "Config.Bind", Path: "bind", Class: ClassFixed, Why: whyAddress},
	{Field: "Config.WorkerStreamPort", Path: "worker_stream_port", Class: ClassFixed, Why: whyStream},
	{Field: "Config.StateDir", Path: "state_dir", Class: ClassFixed, Why: whyState},
	// The store's address carries the daemon's own database password in its userinfo. It is
	// fixed, so the route refuses it with whyStore before its value is read, and no key of the
	// file holds that password apart from it.
	{Field: "Config.PostgresDSN", Path: "postgres_dsn", Class: ClassFixed, Why: whyStore, Secret: true, Credential: CredentialAddress},

	// The runtime selector and its two blocks. `runtime` is one key the loader splits into the
	// selector and the block it names, so Config.Runtime and Runtime.Name share its path: the
	// first is the block the diff descends into, the second the selector's own value.
	{Field: "Config.Runtime", Path: "runtime", Class: ClassFixed, Why: whyRuntime, Block: true},
	{Field: "Runtime.Name", Path: "runtime", Class: ClassFixed, Why: whyRuntime},
	{Field: "Runtime.Kubernetes", Path: "runtime.kubernetes", Class: ClassFixed, Why: whyNamespc, Block: true},
	{Field: "Runtime.Tmux", Path: "runtime.tmux", Class: ClassFixed, Why: whyTakeOve, Block: true},

	// Read per decision: admission reads the cap at every admission, and the supervisor its
	// budgets at every launch it is deciding about (internal/daemon's liveBudgets).
	{Field: "Config.AdmissionCap", Path: "admission_cap", Class: ClassDecision},
	{Field: "Config.LaunchFailureLimit", Path: "launch_failure_limit", Class: ClassDecision},
	{Field: "Config.PromptFailureLimit", Path: "prompt_failure_limit", Class: ClassDecision},
	{Field: "Config.PromptRetireLimit", Path: "prompt_retire_limit", Class: ClassDecision},
	// The boot deadline and its interval count. Each machine reads both per decision
	// (supervise.Timeouts), and the Sandbox runtime holds them too: every wait of a launch, of
	// the pod's own observation and of a Sandbox deletion is bounded by the boot timeout the
	// runtime was built with, and the launch bound is derived from it when it is built
	// (runtime/sandbox's launchBound). The stricter class is the runtime's rebuild, as
	// `worker_stop_timeout_seconds` below is.
	{Field: "Config.WorkerBootTimeout", Path: "worker_boot_timeout_seconds", Class: ClassRebuild, Holder: HolderRuntime},
	{Field: "Config.WorkerBootRegistrationDeadlineIntervals", Path: "worker_boot_registration_deadline_intervals", Class: ClassRebuild, Holder: HolderRuntime},
	// No holder reads it today: the whole of its behaviour is the value the state carries.
	{Field: "Config.TreeStopTimeout", Path: "tree_stop_timeout_seconds", Class: ClassDecision},

	// The workflow's own decisions, each read as the decision is made.
	{Field: "Config.Linger", Path: "linger_hours", Class: ClassDecision},
	{Field: "Config.ProductionCheckBound", Path: "production_check_hours", Class: ClassDecision},
	// The update round's bound, which the sweep reads off the configuration the daemon booted on
	// (internal/daemon's supervision.cfg) at each decision it holds — whether the round has
	// reached its replacement, whether it is told late, and the end its ask states — so the end
	// each role is told is the bound the round is held to.
	{Field: "Config.PodUpdateGrace", Path: "pod_update_grace_minutes", Class: ClassFixed, Why: whyPodGrace},
	// The pod-update sweep's two limits, which it reads off the configuration the daemon booted
	// on (internal/daemon's supervision.cfg) rather than at each sweep.
	{Field: "Config.PodUpdateMaxOpenRounds", Path: "pod_update_max_open_rounds", Class: ClassFixed, Why: whyPodSweep},
	{Field: "Config.PodUpdateRoundsPerMinute", Path: "pod_update_rounds_per_minute", Class: ClassFixed, Why: whyPodSweep},
	// Each role's stall backstop, which the sweep reads off the configuration the daemon booted
	// on, the same way the two limits above it do. The floor the loader holds this bound above
	// is computed from the three LEGION_STALL_* numbers of the daemon's own environment, which
	// are no key of the file (config.StallLimits).
	{Field: "Config.WorkerStallBackstops", Path: "worker_stall_backstop_hours", Class: ClassFixed, Why: whyStallBst},
	// The two bounds a phase worker's quiet is read against: the silence sweep reads both off
	// the configuration the daemon booted on, as the stall backstop above does, and admission
	// is handed both once, at boot, for the silent list of every tick notice.
	{Field: "Config.WorkerSilence", Path: "worker_silence_minutes", Class: ClassFixed, Why: whySilence},
	{Field: "Config.WorkerBackground", Path: "worker_background_hours", Class: ClassFixed, Why: whySilence},
	// The bound a tree's quiet is read against before the project's controller is told it has
	// stopped: admission is handed it once, at boot, as it is the two bounds above, and reads
	// that copy on every pass (admit's tellStalledTrees).
	{Field: "Config.StalledTreeNotice", Path: "stalled_tree_notice_hours", Class: ClassFixed, Why: whyStalled},
	// The pace the daemon lets the wakes it causes out at, which it reads once, at boot, into
	// the one limiter the engine and every machine reserve from (internal/daemon's run,
	// reserveWake).
	{Field: "Config.WorkerWakesPerMinute", Path: "worker_wakes_per_minute", Class: ClassFixed, Why: whyWakePace},
	{Field: "Config.WorkerWakeBurst", Path: "worker_wake_burst", Class: ClassFixed, Why: whyWakePace},
	{Field: "Config.ReviewRoundCap", Path: "review_round_cap", Class: ClassDecision},
	// The capability report is built at every read of the state, from the settings holder
	// (internal/daemon's supervision.deployment), so a decision an operator records reaches the
	// next read with nothing rebuilt.
	{Field: "Config.Capabilities", Path: "capabilities", Class: ClassDecision, Block: true},
	{Field: "Capabilities.Decided", Path: "capabilities.decided", Class: ClassDecision},
	// The design gate is read per decision — the workflow engine and the outbox each read it
	// from the settings holder as they decide — and three things hold a copy of it: the API
	// server, which a reader asks for the gate; the launch specs, which write it into every
	// tree architect's and the controller's prompt; and the project's Dispatch row, which
	// shows a person what the deployment holds a tree's work for. All three are built again
	// here, so the next launch is told the policy the daemon is deciding by and the row says
	// it without a restart; a row names the one holder an operator is told was rebuilt
	// (Change.Holder), which is why this one names the API server and leaves the other two to
	// Also.
	{Field: "Config.Gates", Path: "gates", Class: ClassDecision, Holder: HolderAPIServer, Also: []string{HolderLaunchSpecs, HolderGatedSteps}, Block: true},
	{Field: "Gates.Design", Path: "gates.design", Class: ClassDecision, Holder: HolderAPIServer, Also: []string{HolderLaunchSpecs, HolderGatedSteps}},
	// The deployment's own hooks. The engine is handed the list boot built — the file's hooks,
	// each carrying the words it wrote in its key or, for a hook that names a `prompt_file`,
	// the words boot read into a map of its own keyed by hook name (internal/daemon's readBoot
	// and engineHooks) — and the daemon builds that list again at no later moment, since a hook
	// whose words live only in that map would otherwise stand in front of its roles with none.
	// So the whole key is settled at boot: a change of it reaches the next boot. The list is one
	// value the walk compares whole, as `projects.<KEY>.slack_channels` is, and the rows of one
	// entry's members below it are what ClassOf answers a caller that names one.
	{Field: "Config.Hooks", Path: "hooks", Class: ClassFixed, Why: whyHooks},
	{Field: "Hook.Name", Path: "hooks[].name", Class: ClassFixed, Why: whyHooks},
	{Field: "Hook.On", Path: "hooks[].on", Class: ClassFixed, Why: whyHooks},
	{Field: "Hook.Moves", Path: "hooks[].moves", Class: ClassFixed, Why: whyHooks},
	{Field: "Hook.Every", Path: "hooks[].every", Class: ClassFixed, Why: whyHooks},
	{Field: "Hook.Roles", Path: "hooks[].roles", Class: ClassFixed, Why: whyHooks},
	{Field: "Hook.Prompt", Path: "hooks[].prompt", Class: ClassFixed, Why: whyHooks},
	{Field: "Hook.PromptFile", Path: "hooks[].prompt_file", Class: ClassFixed, Why: whyHooks},

	// Held by a client, a listener or a connection.
	// The worker stream holds it as the bound on every hello and every request, and intake
	// holds it twice over: it is the AckWait the durable consumers are opened with, and the
	// limit one fact's handling is cut at is derived from that window as they open
	// (intake.HandlerLimitFor, read in intake's normalizedSpec) and never read again. So the
	// consumers are named beside the stream, and a route change of this key reopens them on the
	// window it settled; left off, the route would answer `rebuilt` while intake went on
	// cutting a handler at the limit it booted with.
	{Field: "Config.WorkerRPCTimeout", Path: "worker_rpc_timeout_seconds", Class: ClassRebuild, Holder: HolderWorkerStream,
		Also: []string{HolderNATS}},
	// Also supervise.Timeouts.Stop, read per decision; the runtime holds the stop grace it passes
	// each shim, so the stricter class is the runtime's rebuild.
	{Field: "Config.WorkerStopTimeout", Path: "worker_stop_timeout_seconds", Class: ClassRebuild, Holder: HolderRuntime},
	// The runtime's sweep period and its adopt timeout: both are its options, settled when it is
	// built. ProbeInterval is supervise.Timeouts.Probe too, and the stricter class wins.
	{Field: "Config.ProbeInterval", Path: "probe_interval_seconds", Class: ClassRebuild, Holder: HolderRuntime},
	{Field: "Config.SlowCommandTimeout", Path: "slow_command_timeout_seconds", Class: ClassRebuild, Holder: HolderRuntime},
	{Field: "Config.ControllerWakeInterval", Path: "controller_wake_interval_seconds", Class: ClassRebuild, Holder: HolderControllerKeeper},
	// Whether the daemon launches the project's controller itself: boot builds the keeper and the
	// API's answer to who registers the controller from it, and nothing builds either again.
	{Field: "Config.ControllerLaunch", Path: "controller", Class: ClassFixed, Why: whyCtlMode},
	// The Envoy listener's address and bearer: the daemon's Envoy holder is every reader the
	// daemon has of the listener (the outbox, the settings announcer, the clean session stop and
	// the controller's liveness probe), and each launch is handed them too, the address by the
	// runtime (ENVOY_URL) and the bearer by the launch specs (ENVOY_TOKEN). So each row names
	// what carries it into the next launch beside the holder.
	//
	// An address's credential belongs in the file key beside it: the Envoy bearer in
	// `envoy_token_file`, a NATS user's seed in the two seed files, Dispatch's bearer in
	// `dispatch_token_file`. Each of those holds the path of a file, which is a locator.
	{Field: "Config.EnvoyURL", Path: "envoy_url", Class: ClassRebuild, Holder: HolderEnvoyPublisher,
		Also: []string{HolderRuntime}, Secret: true, Credential: CredentialAddress, CredentialIn: []string{"envoy_token_file"}},
	{Field: "Config.EnvoyTokenFile", Path: "envoy_token_file", Class: ClassRebuild, Holder: HolderEnvoyPublisher,
		Also: []string{HolderLaunchSpecs}, Secret: true, Credential: CredentialLocator},
	// NATS's addresses and seeds, and Dispatch's address and bearer, are each held by the
	// daemon's own client of the service and handed to every launch too: the runtime tells each
	// launch ENVOY_NATS_URL and DISPATCH_URL (tmux's pane variables, the Sandbox runtime's pod
	// environment) and carries the Dispatch bearer (the pane token file under tmux, each claim's
	// Secret under kubernetes), each read when the runtime is built, and the launch specs carry
	// the pane seed (NATS_NKEY_SEED). Whether there is a pane seed at all is the Sandbox
	// runtime's too: it decides whether every pod reads the seed from the providers Secret
	// (internal/daemon's launchSecretNames and providersSecrets). So each row names what carries
	// it into the next launch beside the holder, as the Envoy rows above do.
	{Field: "Config.NatsURLs", Path: "nats_urls", Class: ClassRebuild, Holder: HolderNATS,
		Also: []string{HolderRuntime}, Secret: true, Credential: CredentialAddress,
		CredentialIn: []string{"nats_nkey_seed_file", "nats_daemon_nkey_seed_file"}},
	{Field: "Config.NatsNkeySeedFile", Path: "nats_nkey_seed_file", Class: ClassRebuild, Holder: HolderNATS,
		Also: []string{HolderLaunchSpecs, HolderRuntime}, Secret: true, Credential: CredentialLocator},
	{Field: "Config.NatsDaemonNkeySeedFile", Path: "nats_daemon_nkey_seed_file", Class: ClassRebuild, Holder: HolderNATS, Secret: true,
		Credential: CredentialLocator},
	{Field: "Config.DispatchURL", Path: "dispatch_url", Class: ClassRebuild, Holder: HolderDispatchClient,
		Also: []string{HolderRuntime}, Secret: true, Credential: CredentialAddress, CredentialIn: []string{"dispatch_token_file"}},
	{Field: "Config.DispatchTokenFile", Path: "dispatch_token_file", Class: ClassRebuild, Holder: HolderDispatchClient,
		Also: []string{HolderRuntime}, Secret: true, Credential: CredentialLocator},
	// The bearer the operator routes compare against: the API server holds its hash.
	{Field: "Config.OperatorTokenFile", Path: "operator_token_file", Class: ClassRebuild, Holder: HolderAPIServer, Secret: true,
		Credential: CredentialLocator},
	// The settings route's own admission rule: the API server holds the verifier it checks a
	// projected ServiceAccount token with, and a change of any of the three builds that verifier
	// again (internal/daemon's apiHolder). Building one reads nothing — the issuer is discovered
	// at the first token it verifies — so the rebuild costs the running daemon no network read.
	{Field: "Config.SettingsRoute", Path: "settings_route", Class: ClassRebuild, Holder: HolderAPIServer, Block: true},
	{Field: "SettingsRoute.OIDCIssuer", Path: "settings_route.oidc_issuer", Class: ClassRebuild, Holder: HolderAPIServer},
	{Field: "SettingsRoute.Audience", Path: "settings_route.audience", Class: ClassRebuild, Holder: HolderAPIServer},
	{Field: "SettingsRoute.ServiceAccounts", Path: "settings_route.service_accounts", Class: ClassRebuild, Holder: HolderAPIServer},
	// The repository equivalence every comparison of two repository names reads. Four things
	// hold it: the API server's settings, the project's repository mapping, the intake
	// consumers' own decoding of a GitHub fact, and the gh bundles the runtime's launches hand
	// each role. The operator is told the API server was rebuilt, and the other three are built
	// again with it, so no reader is left comparing names by the boot's value.
	{Field: "Config.RepositoryRenames", Path: "repository_renames", Class: ClassRebuild, Holder: HolderAPIServer,
		Also: []string{HolderRepositoryMapping, HolderNATS, HolderRuntime}},
	// The projects block, which `projects` is the one path of: the block is no Block row, so the
	// diff compares it whole and names `projects` whatever member moved, beside the member's own
	// path (Keyed), which is what a caller setting one member by name is answered from. Two
	// things hold it: the launch specs, which copy the project's Slack channels into the
	// controller's prompt and its commit trailer repositories into every pane's environment; and
	// the runtime, which hands the daemon-launched controller's pod the same channels and
	// relaunches it when they change (sandbox.Options.ControllerSlackChannels), and records in
	// every pod's launch settings the trailer repositories and the memory overlays its launch
	// read (sandbox.LaunchSettings). The operator is told the launch specs were rebuilt, and the
	// runtime is built again with them, so the next launch reads the project the daemon is
	// running on and a pod launched on the old one is compared against the new.
	//
	// It is the one keyed row: the file names the entries itself, and the walk and the pin of
	// the fixed paths descend into them, project by project, so the fixed rows below
	// (`memory.mode`, `memory.instructions` and `experiments`) are pinned and refused under the
	// key the file chose while every other member of the entry settles under its own class
	// (live.go's pinKeyed).
	{Field: "Config.Projects", Path: "projects", Class: ClassRebuild, Holder: HolderLaunchSpecs,
		Also: []string{HolderRuntime}, Keyed: true},
	{Field: "Project.SlackChannels", Path: "projects.<KEY>.slack_channels", Class: ClassRebuild, Holder: HolderLaunchSpecs,
		Also: []string{HolderRuntime}},
	// The repositories whose commits a pane stamps the issue key into: a pane reads the key from
	// its own launch environment (LEGION_ISSUE_TRAILER, built in the launch specs), so a change
	// reaches a role at its next launch. So its row names two holders, as `instructions` does:
	// the launch specs, which build that key into each launch, and the Sandbox runtime, which
	// records the value of the daemon's own project in each issue pod's launch-settings
	// annotation (runtime/sandbox's LaunchSettings, made once per build of the runtime), where an
	// update round reads a pod's drift from. The workflow runtime keeps a copy of its own, but
	// only to say at the boot's mapping load which named repository the project does not serve
	// (logUnservedCommitIssueTrailerRepos), and nothing else in the daemon reads it.
	{Field: "Project.CommitIssueTrailerRepos", Path: "projects.<KEY>.commit_issue_trailer_repos",
		Class: ClassLaunch, Holder: HolderLaunchSpecs, Also: []string{HolderRuntime}},
	// Who works the production check a merge closing one of the project's issues opens: the
	// workflow engine reads it as it decides the merge (internal/daemon's engineConfig, composed
	// at every decision), and the state's settings report is rebuilt from it, so a change reaches
	// the next merge with nothing rebuilt. It decides the checks opened after it, never one
	// already open: a running check keeps the role its record carries.
	{Field: "Project.ProductionCheck", Path: "projects.<KEY>.production_check", Class: ClassDecision},
	// The project's memory block: the overlays a memory-group role's Oh My Pi loads reach that
	// role at its next launch, through the runtime that builds its pod (sandboxOptions'
	// ExperimentVariants, which carries the block's own arm) and records the daemon's own
	// project's overlays in each issue pod's launch settings (runtime/sandbox's LaunchSettings),
	// while the mode and the instructions are settled at boot — admission draws each tree it
	// opens under the mode it was given, the API server's own spawn reads that same value, and
	// the instructions file is copied once into the state directory the launch specs read it
	// from.
	{Field: "Project.Memory", Path: "projects.<KEY>.memory", Class: ClassFixed, Why: whyMemory, Block: true},
	{Field: "Memory.Mode", Path: "projects.<KEY>.memory.mode", Class: ClassFixed, Why: whyMemory},
	{Field: "Memory.ConfigFiles", Path: "projects.<KEY>.memory.config_files", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Memory.InstructionsPath", Path: "projects.<KEY>.memory.instructions", Class: ClassFixed, Why: whyMemory},
	// The project's declared experiments beside the block (Project.ExperimentsDeclared, which
	// maps the block in as the experiment named memory): admission draws each tree it opens, the
	// workflow engine each issue it first records and the API server's own spawn each tree it
	// opens, all under the list each was handed at boot (internal/daemon's openWorkflow and
	// serve), and boot copies every arm's instructions into the state directory once, which is
	// where a launch reads them from (experiment.Launch). So the whole key is settled at boot, as
	// `hooks` is: the list is one value the walk compares whole, and the rows of its members
	// below are what ClassOf answers a caller that names one.
	{Field: "Project.Experiments", Path: "projects.<KEY>.experiments", Class: ClassFixed, Why: whyExperim},
	{Field: "Experiment.Name", Path: "projects.<KEY>.experiments.<NAME>", Class: ClassFixed, Why: whyExperim},
	{Field: "Experiment.Mode", Path: "projects.<KEY>.experiments.<NAME>.mode", Class: ClassFixed, Why: whyExperim},
	{Field: "Experiment.Unit", Path: "projects.<KEY>.experiments.<NAME>.unit", Class: ClassFixed, Why: whyExperim},
	{Field: "Experiment.OnVariant", Path: "projects.<KEY>.experiments.<NAME>.on_variant", Class: ClassFixed, Why: whyExperim},
	{Field: "Experiment.Variants", Path: "projects.<KEY>.experiments.<NAME>.variants", Class: ClassFixed, Why: whyExperim},
	{Field: "Variant.Name", Path: "projects.<KEY>.experiments.<NAME>.variants[].name", Class: ClassFixed, Why: whyExperim},
	{Field: "Variant.Weight", Path: "projects.<KEY>.experiments.<NAME>.variants[].weight", Class: ClassFixed, Why: whyExperim},
	{Field: "Variant.ConfigFiles", Path: "projects.<KEY>.experiments.<NAME>.variants[].config_files", Class: ClassFixed, Why: whyExperim},
	{Field: "Variant.InstructionsPath", Path: "projects.<KEY>.experiments.<NAME>.variants[].instructions", Class: ClassFixed, Why: whyExperim},
	// Each App's credential: the manager mints every role's token from it. The diff compares each
	// App whole — neither GitHubApps field is a Block row — so the member rows below are what
	// ClassOf answers a caller naming one member, and each names its App's path rather than a
	// `<app>` template because the two Apps' members differ in class. The GitHubApp fields are
	// read twice, once per App, which is why each of them has two rows; no walk descends into an
	// App, so neither row is what the walk or the pin reads.
	//
	// An App's credential is one of four sources, of which three say where its key lives — a
	// command that prints it, the name of the secretsd key holding it, and the broker command
	// that leases a token from a key the daemon never holds — and are locators. `private_key` is
	// the key itself. No key of the file holds an App's key as a file's path, so its refusal
	// names the two key sources beside it, which say where the key lives instead.
	{Field: "Config.GitHubApps", Path: "github_apps", Class: ClassRebuild, Holder: HolderGitHubApps, Block: true},
	{Field: "GitHubApps.Implement", Path: "github_apps.implement", Class: ClassRebuild, Holder: HolderGitHubApps, Secret: true,
		Credential: CredentialLocator},
	{Field: "GitHubApp.AppID", Path: "github_apps.implement.app_id", Class: ClassRebuild, Holder: HolderGitHubApps},
	{Field: "GitHubApp.Installations", Path: "github_apps.implement.installations", Class: ClassRebuild, Holder: HolderGitHubApps},
	{Field: "GitHubApp.PrivateKey", Path: "github_apps.implement.private_key", Class: ClassRebuild, Holder: HolderGitHubApps, Secret: true,
		Credential: CredentialInlined, CredentialIn: []string{"github_apps.implement.private_key_secret", "github_apps.implement.private_key_command"}},
	{Field: "GitHubApp.PrivateKeyCommand", Path: "github_apps.implement.private_key_command", Class: ClassRebuild, Holder: HolderGitHubApps, Secret: true,
		Credential: CredentialLocator},
	{Field: "GitHubApp.PrivateKeySecret", Path: "github_apps.implement.private_key_secret", Class: ClassRebuild, Holder: HolderGitHubApps, Secret: true,
		Credential: CredentialLocator},
	// The fourth source of an App's credential, for a deployment whose keys only a secrets broker
	// holds: the manager runs it for a token, so a change of it rebuilds the manager as the three
	// key sources do.
	{Field: "GitHubApp.TokenCommand", Path: "github_apps.implement.token_command", Class: ClassRebuild, Holder: HolderGitHubApps, Secret: true,
		Credential: CredentialLocator},
	// The review App is settled at boot, every key of it: its login is read once, then, and is
	// what decides whose review settles a round (whyReview). The App is one value the pin puts
	// back whole, its key and the command or the secret it comes from among it, so a rebuild of
	// the App manager for a change of the implement App builds it on the review App boot read.
	{Field: "GitHubApps.Review", Path: "github_apps.review", Class: ClassFixed, Why: whyReview, Secret: true, Credential: CredentialLocator},
	{Field: "GitHubApp.AppID", Path: "github_apps.review.app_id", Class: ClassFixed, Why: whyReview},
	{Field: "GitHubApp.Installations", Path: "github_apps.review.installations", Class: ClassFixed, Why: whyReview},
	{Field: "GitHubApp.PrivateKey", Path: "github_apps.review.private_key", Class: ClassFixed, Why: whyReview, Secret: true,
		Credential: CredentialInlined, CredentialIn: []string{"github_apps.review.private_key_secret", "github_apps.review.private_key_command"}},
	{Field: "GitHubApp.PrivateKeyCommand", Path: "github_apps.review.private_key_command", Class: ClassFixed, Why: whyReview, Secret: true,
		Credential: CredentialLocator},
	{Field: "GitHubApp.PrivateKeySecret", Path: "github_apps.review.private_key_secret", Class: ClassFixed, Why: whyReview, Secret: true,
		Credential: CredentialLocator},
	{Field: "GitHubApp.TokenCommand", Path: "github_apps.review.token_command", Class: ClassFixed, Why: whyReview, Secret: true,
		Credential: CredentialLocator},

	// Read when a pod or a pane launches: the runtime and the launch specs hold them, and the
	// change reaches a role at its next launch.
	//
	// The daemon's API takes no credential in its address — a pod presents its boot token and its
	// grants, never userinfo — so no key of the file holds one for `daemon_url`.
	{Field: "Config.DaemonURL", Path: "daemon_url", Class: ClassLaunch, Holder: HolderRuntime, Secret: true, Credential: CredentialAddress},
	{Field: "Config.AdvertiseHost", Path: "advertise_host", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Config.OmpInvocation", Path: "omp_invocation", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Config.OmpLaunchPrefix", Path: "omp_launch_prefix", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Config.RoleConfigFiles", Path: "role_config_files", Class: ClassLaunch, Holder: HolderRuntime},
	// `worker_backends` is read by two things, so its row names two holders: the launch specs,
	// which carry a role's backend into its spec, and the runtime, which holds the launcher that
	// backend is spawned by (internal/daemon's paneLaunchers under tmux: a pane of a backend the
	// runtime holds no launcher for is refused). The operator is told the launch specs were
	// rebuilt, and the runtime is built again with them, so a role moved onto a harness reaches
	// it at its next launch.
	{Field: "Config.WorkerBackends", Path: "worker_backends", Class: ClassLaunch, Holder: HolderLaunchSpecs,
		Also: []string{HolderRuntime}},
	// `instructions` is read by two things, so its row names two holders: the launch specs, which
	// copy the file into the one every role's prompt ends with, and the Sandbox runtime, which
	// records the value in each pod's launch-settings annotation (runtime/sandbox's
	// LaunchSettings, made once per build of the runtime), where an update round reads a pod's
	// drift from, for both pod kinds. The operator is told the launch specs were rebuilt, and the
	// runtime is built again with them, so a pod launched on the old instructions is brought onto
	// the new ones.
	{Field: "Config.InstructionsPath", Path: "instructions", Class: ClassLaunch, Holder: HolderLaunchSpecs,
		Also: []string{HolderRuntime}},
	// The provider credentials themselves. One `provider_keys` entry is `<NAME>: <source>`: its
	// name and its source land in three fields of one entry, so the three share the entry's path.
	// Under tmux the runtime's build reads each source, as the daemon, into a file of its own the
	// pane's launch reads (internal/daemon's providerKeyFiles), at boot and again whenever the
	// entries or their files have moved; under kubernetes the runtime hands each pod the key of
	// the providers Secret. So a change reaches a role at its next launch.
	//
	// A source is never the credential: it is the name of the secretsd key or of the providers
	// Secret's key that holds it, a variable's name the loader holds to envVarName, or `file:` and
	// the path of the file that holds it (readProviderKeys). So each is a locator.
	{Field: "Config.ProviderKeys", Path: "provider_keys", Class: ClassLaunch, Holder: HolderRuntime, Secret: true, Credential: CredentialLocator},
	{Field: "ProviderKey.Env", Path: "provider_keys.<NAME>", Class: ClassLaunch, Holder: HolderRuntime, Secret: true, Credential: CredentialLocator},
	{Field: "ProviderKey.Secret", Path: "provider_keys.<NAME>", Class: ClassLaunch, Holder: HolderRuntime, Secret: true, Credential: CredentialLocator},
	{Field: "ProviderKey.File", Path: "provider_keys.<NAME>", Class: ClassLaunch, Holder: HolderRuntime, Secret: true, Credential: CredentialLocator},

	// The kubernetes block. The runtime holds all of it — it caches each pod's shape when it is
	// built — so every change of it rebuilds the runtime; what the operator is told is whether the
	// value does anything before the next pod is built.
	{Field: "Kubernetes.Namespace", Path: "runtime.kubernetes.namespace", Class: ClassFixed, Why: whyNamespc},
	// The idle release, which the workflow settles at boot into its idle pass and the outbox's
	// backstop (internal/daemon's workflow.go, idle.go): neither is built again.
	{Field: "Kubernetes.IdleRelease", Path: "runtime.kubernetes.idle_release", Class: ClassFixed, Why: whyIdleRel, Block: true},
	{Field: "IdleReleaseConfig.IdleMinutes", Path: "runtime.kubernetes.idle_release.idle_minutes", Class: ClassFixed, Why: whyIdleRel},
	{Field: "IdleReleaseConfig.BackstopMinutes", Path: "runtime.kubernetes.idle_release.backstop_minutes", Class: ClassFixed, Why: whyIdleRel},
	{Field: "Kubernetes.Image", Path: "runtime.kubernetes.image", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Kubernetes.StorageClass", Path: "runtime.kubernetes.storage_class", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Kubernetes.IssueVolume", Path: "runtime.kubernetes.issue_volume", Class: ClassLaunch, Holder: HolderRuntime},
	// The store's kind, and the name of the providers Secret's key or the Cloud SQL login behind
	// it: where the sessions' database credential lives, never the credential.
	{Field: "Kubernetes.SessionStore", Path: "runtime.kubernetes.session_store", Class: ClassLaunch, Holder: HolderRuntime, Secret: true,
		Credential: CredentialLocator},
	{Field: "Kubernetes.Setup", Path: "runtime.kubernetes.setup", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Kubernetes.TestPostgres", Path: "runtime.kubernetes.test_postgres", Class: ClassLaunch, Holder: HolderRuntime},
	// The skills marketplace an issue pod's own init container clones: the runtime holds it, it
	// shapes the pod (runtime/sandbox's podShapeRecord.SkillsRepository, which the drift read
	// compares), and a change of it reaches a role at its next launch.
	{Field: "Kubernetes.SkillsRepository", Path: "runtime.kubernetes.skills_repository", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Kubernetes.Scheduling", Path: "runtime.kubernetes.scheduling", Class: ClassLaunch, Holder: HolderRuntime, Block: true},
	{Field: "Scheduling.NodeSelector", Path: "runtime.kubernetes.scheduling.node_selector", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Scheduling.Tolerations", Path: "runtime.kubernetes.scheduling.tolerations", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Scheduling.PriorityClass", Path: "runtime.kubernetes.scheduling.priority_class", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Toleration.Key", Path: "runtime.kubernetes.scheduling.tolerations[].key", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Toleration.Operator", Path: "runtime.kubernetes.scheduling.tolerations[].operator", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Toleration.Value", Path: "runtime.kubernetes.scheduling.tolerations[].value", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Toleration.Effect", Path: "runtime.kubernetes.scheduling.tolerations[].effect", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Kubernetes.Resources", Path: "runtime.kubernetes.resources", Class: ClassLaunch, Holder: HolderRuntime, Block: true},
	{Field: "PodResources.Issue", Path: "runtime.kubernetes.resources.issue", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "PodResources.Controller", Path: "runtime.kubernetes.resources.controller", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Reservation.CPU", Path: "runtime.kubernetes.resources.<pod>.cpu", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Reservation.CPURequest", Path: "runtime.kubernetes.resources.<pod>.cpu_request", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Reservation.Memory", Path: "runtime.kubernetes.resources.<pod>.memory", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Reservation.MemoryRequest", Path: "runtime.kubernetes.resources.<pod>.memory_request", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Reservation.EphemeralStorage", Path: "runtime.kubernetes.resources.<pod>.ephemeral_storage", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Reservation.EphemeralStorageRequest", Path: "runtime.kubernetes.resources.<pod>.ephemeral_storage_request", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Kubernetes.Pod", Path: "runtime.kubernetes.pod", Class: ClassLaunch, Holder: HolderRuntime, Block: true},
	// The operator's own variables, which the loader refuses a credential-shaped name in, so a
	// credential reaches a pod as a key of the providers Secret or a Secret the pod mounts. A
	// value may still be an address, and one with userinfo is refused through the route by the
	// address rule, pointing at those two keys.
	{Field: "PodConfig.Env", Path: "runtime.kubernetes.pod.env", Class: ClassLaunch, Holder: HolderRuntime, Secret: true,
		Credential: CredentialAddress, CredentialIn: []string{"provider_keys", "runtime.kubernetes.pod.volumes"}},
	{Field: "PodConfig.Volumes", Path: "runtime.kubernetes.pod.volumes", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "PodConfig.VolumeMounts", Path: "runtime.kubernetes.pod.volume_mounts", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "PodConfig.ServiceAccount", Path: "runtime.kubernetes.pod.service_account", Class: ClassLaunch, Holder: HolderRuntime},
	// The secrets broker every pod is enrolled with. A pod reads three of the block's members,
	// each a launch key of the runtime, which records them in every pod's launch-settings
	// annotation: `url`, the broker's address, which the runtime tells each pod it launches, and
	// `audience` and `token_expiry_seconds`, the audience and the lifetime of the pod's own
	// ServiceAccount token, which the runtime projects into each pod for the broker
	// (internal/daemon's sandboxOptions, runtime/sandbox's brokerTokenProjection). The daemon's own
	// login at the broker (internal/daemon's secretsLoginHolder) reads `url` as well, logs in there
	// and enrolls every pod the runtime launches, so `url` names the runtime and the login beside
	// it, and a pod is enrolled at the broker it is told.
	//
	// `token_file` differs: it is the path of the daemon's own projected token, which the login
	// alone reads and logs in with, and no pod reads it, a pod presenting the token projected into
	// it instead. So it is a rebuild of the login, with no launch holder beside it and no place in
	// any pod's annotation: a change of it builds the login again on the file it names and drifts
	// no pod, where a change of a launch key replaces every pod whose launch reads it. The
	// login is a holder of its own and no part of the runtime because the kubelet rewrites
	// `token_file` in place through its life: as one of the runtime's credential files, every such
	// rewrite would build the Sandbox runtime again, its informers and refreshers with it.
	{Field: "Kubernetes.AgentSecrets", Path: "runtime.kubernetes.agent_secrets", Class: ClassLaunch, Holder: HolderRuntime,
		Also: []string{HolderSecretsLogin}, Block: true},
	{Field: "AgentSecretsConfig.URL", Path: "runtime.kubernetes.agent_secrets.url", Class: ClassLaunch, Holder: HolderRuntime,
		Also: []string{HolderSecretsLogin}},
	{Field: "AgentSecretsConfig.Audience", Path: "runtime.kubernetes.agent_secrets.audience", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "AgentSecretsConfig.TokenExpirySeconds", Path: "runtime.kubernetes.agent_secrets.token_expiry_seconds", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "AgentSecretsConfig.TokenFile", Path: "runtime.kubernetes.agent_secrets.token_file", Class: ClassRebuild, Holder: HolderSecretsLogin, Secret: true,
		Credential: CredentialLocator},
	// The plugin pair a stale-plugin relaunch installs into a kept pod. One reader holds it: the
	// launch specs, which resolve the pair from the configuration each build of them is given and
	// hand it to the launch of a claim on another plugin contract (internal/daemon's
	// stalePluginInstall), and which answer from it whether such a claim can be relaunched at all
	// (specs' pluginRelaunchable). No pod's own launch reads it: a pod launched fresh loads the
	// image's own copies, which the boot gate holds to the daemon's contract, and so does a pod a
	// launch replaces for any other reason (runtime/sandbox's Spawn and relaunch clear
	// spec.Plugins). The relaunch that installs the pair keeps the pod it installs into, so no pod
	// is ever replaced to carry another pair. So it is a rebuild of the launch specs, with no
	// runtime beside them and no place in any pod's launch-settings annotation, as
	// `agent_secrets.token_file` above is the login's: a change of it builds the launch specs again,
	// the next stale-plugin relaunch installs the pair it names, and it drifts no pod, where a
	// launch key would open an update round over every pod whose launch reads it, each pod's disk
	// deleted with the pod.
	//
	// A pin is a version, a tarball's path, or an http(s) URL, and a private or pre-signed
	// tarball's URL carries its credential in its userinfo or in its query: `X-Amz-Signature` is
	// the one parameter the daemon's own reading of a pin names (api.pluginSourceOf, which drops
	// the query from the state). No key of the file holds that credential apart from the URL.
	{Field: "Kubernetes.Plugins", Path: "runtime.kubernetes.plugins", Class: ClassRebuild, Holder: HolderLaunchSpecs, Block: true},
	{Field: "PluginsConfig.PiLegion", Path: "runtime.kubernetes.plugins.pi_legion", Class: ClassRebuild, Holder: HolderLaunchSpecs,
		Secret: true, Credential: CredentialAddress, SecretQuery: []string{"X-Amz-Signature"}},
	{Field: "PluginsConfig.PiEnvoy", Path: "runtime.kubernetes.plugins.pi_envoy", Class: ClassRebuild, Holder: HolderLaunchSpecs,
		Secret: true, Credential: CredentialAddress, SecretQuery: []string{"X-Amz-Signature"}},
	// The daemon's own cluster credential, and the repository caches it keeps: pods of the
	// runtime's own, replaced when the runtime is built again, never role launches. The
	// kubeconfig's path and the name of a context in it are where that credential lives.
	{Field: "Kubernetes.Kubeconfig", Path: "runtime.kubernetes.kubeconfig", Class: ClassRebuild, Holder: HolderRuntime, Secret: true,
		Credential: CredentialLocator},
	{Field: "Kubernetes.Context", Path: "runtime.kubernetes.context", Class: ClassRebuild, Holder: HolderRuntime, Secret: true,
		Credential: CredentialLocator},
	{Field: "Kubernetes.RepositoryCache", Path: "runtime.kubernetes.repository_cache", Class: ClassRebuild, Holder: HolderRuntime, Block: true},
	{Field: "RepositoryCacheConfig.Volume", Path: "runtime.kubernetes.repository_cache.volume", Class: ClassFixed, Why: whyCacheVol},
	{Field: "RepositoryCacheConfig.RefreshMinutes", Path: "runtime.kubernetes.repository_cache.refresh_minutes", Class: ClassRebuild, Holder: HolderRuntime},
	{Field: "RepositoryCacheConfig.RetainHours", Path: "runtime.kubernetes.repository_cache.retain_hours", Class: ClassRebuild, Holder: HolderRuntime},
	{Field: "RepositoryCacheConfig.Resources", Path: "runtime.kubernetes.repository_cache.resources", Class: ClassRebuild, Holder: HolderRuntime},

	// The tmux block: what every pane's launch reads, and the take-over boot read once. The
	// loader refuses a credential-shaped name in `pane_env` and a value holding a URL with a
	// password (runtime.PaneVariableRefusal), since a secret reaches a pane as a file it names in
	// `provider_keys`; the route refuses a value with any userinfo, pointing there.
	{Field: "Tmux.PaneEnv", Path: "runtime.tmux.pane_env", Class: ClassLaunch, Holder: HolderRuntime, Secret: true,
		Credential: CredentialAddress, CredentialIn: []string{"provider_keys"}},
	{Field: "Tmux.PiConfigFiles", Path: "runtime.tmux.pi_config_files", Class: ClassLaunch, Holder: HolderRuntime},
	{Field: "Tmux.TakeOver", Path: "runtime.tmux.take_over", Class: ClassFixed, Why: whyTakeOve},
}

// rows indexes Classes by field. The table is a literal, so the index is built once.
var rows = func() map[string]Row {
	index := make(map[string]Row, len(Classes))
	for _, row := range Classes {
		index[row.Field] = row
	}
	return index
}()

// rowOf is the row the field `<Type>.<Field>` is read into.
func rowOf(field string) (Row, bool) {
	row, ok := rows[field]
	return row, ok
}

// Paths is every dotted path the table holds, each once, sorted. Two fields of one key share a
// path, so it is shorter than the table.
func Paths() []string {
	paths := make([]string, 0, len(Classes))
	for _, row := range Classes {
		paths = append(paths, row.Path)
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}

// ClassOf is the row a dotted path belongs to: the row whose path it is, or the first row whose
// path is the template it matches — a `<name>` segment matching any name the file chose, and a
// `name[]` segment any entry of that sequence. Rows that share a path agree on their class, so the
// first is the answer.
func ClassOf(path string) (Row, bool) {
	for _, row := range Classes {
		if row.Path == path {
			return row, true
		}
	}
	for _, row := range Classes {
		if matchesTemplate(row.Path, path) {
			return row, true
		}
	}
	return Row{}, false
}

// matchesTemplate is whether a concrete dotted path is one of the paths a template names.
func matchesTemplate(template, path string) bool {
	want, got := strings.Split(template, "."), strings.Split(path, ".")
	if len(want) != len(got) {
		return false
	}
	for i, segment := range want {
		switch {
		case segment == got[i]:
		case strings.HasPrefix(segment, "<") && strings.HasSuffix(segment, ">"):
			// A name the file chose, which may not be empty and names one level.
			if got[i] == "" {
				return false
			}
		case strings.HasSuffix(segment, "[]"):
			// One entry of a sequence, written `name[0]`.
			name := strings.TrimSuffix(segment, "[]")
			index, ok := strings.CutPrefix(got[i], name+"[")
			if !ok || !strings.HasSuffix(index, "]") || len(index) < 2 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
