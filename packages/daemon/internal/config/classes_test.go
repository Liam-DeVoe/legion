package config

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// settingsConfigTypes is Config and every struct type of this package reachable from it through
// exported fields, pointers, slices, arrays and map keys and elements, each once: the types whose
// exported fields TestEverySettingsPathHasAClass holds to the class table. The walk is a copy of
// api/settings_test.go's, which holds the same field set to what the state may carry; the two
// packages cannot share a test helper, and the copy is what makes a field added to Config fail
// both tests rather than one. A type of another package (a Kubernetes API type, time.Duration, a
// role, a backend, a compiled regexp) is a leaf: its fields are no legion.yaml keys.
func settingsConfigTypes() []reflect.Type {
	root := reflect.TypeFor[Config]()
	var types []reflect.Type
	var walk func(typ reflect.Type)
	walk = func(typ reflect.Type) {
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			walk(typ.Elem())
		case reflect.Map:
			walk(typ.Key())
			walk(typ.Elem())
		case reflect.Struct:
			if typ.PkgPath() != root.PkgPath() || slices.Contains(types, typ) {
				return
			}
			types = append(types, typ)
			for i := range typ.NumField() {
				if field := typ.Field(i); field.IsExported() {
					walk(field.Type)
				}
			}
		}
	}
	walk(root)
	return types
}

// classesDerived is every exported field the walk reaches that is no legion.yaml key and so has no
// row in Classes: a value the loader computes from a key that has one, or one it reads from the
// daemon's own environment. Nothing may be added here that an operator can write in their file.
var classesDerived = map[string]string{
	"Config.Warnings":              "derived: the loader's own lines for the operator about a key the file still sets and this daemon no longer reads, which no key of the file writes",
	"Kubernetes.PluginsFromBinary": "derived: whether runtime.kubernetes.plugins was filled from the binary's linked pair rather than from the file",
	"Config.StallLimits":           "read from the daemon's own environment, not the file: the three LEGION_STALL_* numbers it passes to every session it starts, held here because the floor under worker_stall_backstop_hours is computed from them (config.StallLimits)",
	"StallLimits.CallBound":        "read from the daemon's own environment: LEGION_STALL_CALL_BOUND_MS",
	"StallLimits.CallGrace":        "read from the daemon's own environment: LEGION_STALL_CALL_GRACE_MS",
	"StallLimits.Step":             "read from the daemon's own environment: LEGION_STALL_STEP_MS",
	"Variant.ConfigFilesField":     "derived: the key an arm's config_files were written under, which Project.ExperimentsDeclared fills as it builds the declared list and no key of the file writes",
	"Variant.MemoryBlock":          "derived: whether the arm is the `memory` block's own, which Project.ExperimentsDeclared sets as it maps the block in and no key of the file writes",
}

// Every exported field of every struct the walk reaches is in the class table or in
// classesDerived, and every row of the table names a field the walk reaches. A new field fails
// here until someone has read what the daemon does with its value and said when a change of it
// takes effect.
func TestEverySettingsPathHasAClass(t *testing.T) {
	seen := map[string]bool{}
	for _, typ := range settingsConfigTypes() {
		for i := range typ.NumField() {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			name := typ.Name() + "." + field.Name
			seen[name] = true
			_, classed := rowOf(name)
			_, derived := classesDerived[name]
			switch {
			case classed && derived:
				t.Errorf("%s is in both Classes and classesDerived", name)
			case !classed && !derived:
				t.Errorf("%s has no class: read what the daemon does with the value, then add a row to Classes in config/classes.go giving its legion.yaml path, its class (decision, rebuild, launch or fixed) and, where something holds it, the holder a change makes the daemon build again; a value the loader derives from another key goes in classesDerived instead", name)
			}
		}
	}
	for _, row := range Classes {
		if !seen[row.Field] {
			t.Errorf("Classes names %s, which is no exported field of a struct reachable from Config: drop the row, or name the field the setting now lands in", row.Field)
		}
	}
	for name := range classesDerived {
		if !seen[name] {
			t.Errorf("classesDerived names %s, which is no exported field of a struct reachable from Config: drop the row", name)
		}
	}
}

// Each row is sound on its own terms: a fixed row says why it is refused and names no holder, a
// rebuild row names the holder it rebuilds, a launch row names a holder a launch reads values
// from, and no other row carries a refusal.
//
// Two holders hand a launch anything: the runtime, which launches every pod and pane and records
// every launch key in each pod's launch-settings annotation, and the launch specs every launch is
// built from. A launch row naming any other holder is a value the daemon reads for itself and no
// launch reads, which that annotation would record all the same, so a change of it would drift
// every pod of the fleet and open an update round over each one: the path of the daemon's own
// broker token was such a row, held by the daemon's login and no launch.
func TestEveryClassRowIsWellFormed(t *testing.T) {
	for _, row := range Classes {
		switch row.Class {
		case ClassFixed:
			if row.Why == "" {
				t.Errorf("%s is fixed with no reason: the route refuses the path by name and prints Why", row.Field)
			}
			if row.Holder != "" {
				t.Errorf("%s is fixed and names the holder %q: nothing is rebuilt for a refused path", row.Field, row.Holder)
			}
		case ClassRebuild:
			if row.Holder == "" {
				t.Errorf("%s is a rebuild with no holder: name what the daemon builds again", row.Field)
			}
		case ClassDecision:
		case ClassLaunch:
			if row.Holder != HolderRuntime && row.Holder != HolderLaunchSpecs {
				t.Errorf("%s is a launch row held by %q, which no launch reads a value from: a value only the daemon reads is a rebuild of its holder", row.Field, row.Holder)
			}
		default:
			t.Errorf("%s has the class %q, which is none of the four", row.Field, row.Class)
		}
		if row.Class != ClassFixed && row.Why != "" {
			t.Errorf("%s is %s and carries a refusal reason, which only a fixed row has", row.Field, row.Class)
		}
		if row.Path == "" {
			t.Errorf("%s has no dotted path", row.Field)
		}
	}
}

// Two rows may share a path, where one legion.yaml key fills more than one field; ClassOf answers
// the first, so the rows that share it must agree.
func TestRowsThatShareAPathAgree(t *testing.T) {
	byPath := map[string]Row{}
	for _, row := range Classes {
		first, held := byPath[row.Path]
		if !held {
			byPath[row.Path] = row
			continue
		}
		if first.Class != row.Class || first.Holder != row.Holder || first.Why != row.Why || first.Credential != row.Credential {
			t.Errorf("%s and %s share the path %s and disagree: %+v against %+v", first.Field, row.Field, row.Path, first, row)
		}
	}
}

// Paths is the table's census with no path twice, in order, and it holds the keys an operator
// writes at the top level of the file.
func TestPathsIsTheSortedCensus(t *testing.T) {
	paths := Paths()
	if !slices.IsSorted(paths) {
		t.Errorf("Paths() is not sorted: %q", paths)
	}
	for i := 1; i < len(paths); i++ {
		if paths[i] == paths[i-1] {
			t.Errorf("Paths() holds %s twice", paths[i])
		}
	}
	for _, path := range []string{"admission_cap", "gates.design", "runtime", "runtime.kubernetes.image", "github_apps", "projects"} {
		if !slices.Contains(paths, path) {
			t.Errorf("Paths() does not hold %s", path)
		}
	}
	// Every top-level key the loader reads has a row, so an operator who names one is never told
	// it is unknown.
	for _, key := range loaderTopLevelKeys() {
		if _, known := ClassOf(key); !known {
			t.Errorf("the loader reads %s and the class table has no row for it", key)
		}
	}
}

// loaderTopLevelKeys is every top-level key readKeys reads, read from the key table it dispatches
// every key through (topLevelKeys), so a key added there with no class fails this test.
func loaderTopLevelKeys() []string {
	keys := make([]string, 0, len(topLevelKeys))
	for _, row := range topLevelKeys {
		keys = append(keys, row.Name)
	}
	return keys
}

// ClassOf reads a concrete path against the templates the table writes: a name the file chose and
// one entry of a sequence.
func TestClassOfReadsTheTemplatedPaths(t *testing.T) {
	for _, tc := range []struct{ path, field string }{
		{"admission_cap", "Config.AdmissionCap"},
		{"gates.design", "Gates.Design"},
		{"projects.DEMO.slack_channels", "Project.SlackChannels"},
		{"projects.DEMO.memory.mode", "Memory.Mode"},
		{"github_apps.implement.app_id", "GitHubApp.AppID"},
		{"github_apps.review.token_command", "GitHubApp.TokenCommand"},
		{"runtime.kubernetes.resources.issue", "PodResources.Issue"},
		{"runtime.kubernetes.resources.controller.cpu", "Reservation.CPU"},
		{"runtime.kubernetes.scheduling.tolerations[1].effect", "Toleration.Effect"},
		{"provider_keys.ANTHROPIC_API_KEY", "ProviderKey.Env"},
	} {
		row, known := ClassOf(tc.path)
		if !known {
			t.Errorf("ClassOf(%q) found no row", tc.path)
			continue
		}
		if row.Field != tc.field {
			t.Errorf("ClassOf(%q) = %s, want %s", tc.path, row.Field, tc.field)
		}
	}
	// The two Apps are named, not templated: an App the file cannot hold is no setting.
	for _, path := range []string{"", "admission", "gates.designn", "projects.DEMO.merge", "projects.DEMO.memory.mode[x", "runtime.kubernetes.resources.issue.cpu.core", "github_apps.other.app_id"} {
		if row, known := ClassOf(path); known {
			t.Errorf("ClassOf(%q) = %s, want no row", path, row.Field)
		}
	}
}

// Every path the refusals name is one an operator can write, and each reason says what is in the
// way rather than only that the path is refused. A fixed path is one of two things: a thing boot
// bound, which nothing can rebind under the work it is serving (the sockets, the store, the
// project token, the runtime the daemon's processes run in), or a value boot settled into
// something the daemon never builds again (the idle release, in the workflow's idle pass and the
// outbox's backstop; a project's memory mode and instructions, in admission, the API server and
// the launch specs; a project's experiments, in admission, the workflow engine, the API server and
// the copies boot makes of their instructions; the wake pacing, in the one limiter the engine and
// every machine reserve from; whether the daemon launches its controller, in the keeper and the
// API; the review App, whose login the engine and the review checks judge by). A new fixed path
// belongs in this list deliberately, with a reason an operator can act on: the alternative is a
// key the route answers `rebuilt` for and that reaches nothing.
func TestTheFixedPathsAreTheOnesBootSettled(t *testing.T) {
	var fixed []string
	for _, row := range Classes {
		if row.Class == ClassFixed && !slices.Contains(fixed, row.Path) {
			fixed = append(fixed, row.Path)
		}
	}
	slices.Sort(fixed)
	want := []string{
		"bind", "controller", "github_apps.review", "github_apps.review.app_id",
		"github_apps.review.installations", "github_apps.review.private_key",
		"github_apps.review.private_key_command", "github_apps.review.private_key_secret",
		"github_apps.review.token_command", "hooks", "hooks[].every", "hooks[].moves", "hooks[].name", "hooks[].on",
		"hooks[].prompt", "hooks[].prompt_file", "hooks[].roles",
		"pod_update_grace_minutes", "pod_update_max_open_rounds", "pod_update_rounds_per_minute", "port",
		"postgres_dsn", "project", "projects.<KEY>.experiments", "projects.<KEY>.experiments.<NAME>",
		"projects.<KEY>.experiments.<NAME>.mode", "projects.<KEY>.experiments.<NAME>.on_variant",
		"projects.<KEY>.experiments.<NAME>.unit", "projects.<KEY>.experiments.<NAME>.variants",
		"projects.<KEY>.experiments.<NAME>.variants[].config_files",
		"projects.<KEY>.experiments.<NAME>.variants[].instructions",
		"projects.<KEY>.experiments.<NAME>.variants[].name",
		"projects.<KEY>.experiments.<NAME>.variants[].weight", "projects.<KEY>.memory",
		"projects.<KEY>.memory.instructions", "projects.<KEY>.memory.mode", "runtime",
		"runtime.kubernetes", "runtime.kubernetes.idle_release",
		"runtime.kubernetes.idle_release.backstop_minutes",
		"runtime.kubernetes.idle_release.idle_minutes", "runtime.kubernetes.namespace",
		"runtime.kubernetes.repository_cache.volume",
		"runtime.tmux", "runtime.tmux.take_over", "stalled_tree_notice_hours", "state_dir",
		"worker_background_hours",
		"worker_silence_minutes", "worker_stall_backstop_hours",
		"worker_stream_port", "worker_wake_burst", "worker_wakes_per_minute",
	}
	if !slices.Equal(fixed, want) {
		t.Errorf("the fixed paths are %q, want %q", fixed, want)
	}
	for _, row := range Classes {
		if row.Class == ClassFixed && len(strings.Fields(row.Why)) < 8 {
			t.Errorf("%s is refused with %q, which does not say what is in the way", row.Path, row.Why)
		}
	}
}

// Every Secret row says which of the three things its value is, and no other row does: Secret is
// how the value is shown, Credential where it may be kept, and a row with the one and not the
// other is a value whose route change nobody decided. A locator is stored and points at nothing;
// a key a refusal points the credential at is one an operator can write, never this row itself;
// and a query parameter is read on an address alone.
func TestEverySecretRowSaysWhatItsValueIs(t *testing.T) {
	for _, row := range Classes {
		switch {
		case row.Secret && row.Credential == "":
			t.Errorf("%s (%s) is Secret and names no Credential: say whether its value is a locator, an address or the credential itself", row.Field, row.Path)
		case !row.Secret && row.Credential != "":
			t.Errorf("%s (%s) names the Credential %q and is not Secret, so its value would be shown", row.Field, row.Path, row.Credential)
		}
		switch row.Credential {
		case "", CredentialLocator:
			if len(row.CredentialIn) > 0 || len(row.SecretQuery) > 0 {
				t.Errorf("%s (%s) carries no credential and names %v and %v", row.Field, row.Path, row.CredentialIn, row.SecretQuery)
			}
		case CredentialAddress:
		case CredentialInlined:
			if len(row.SecretQuery) > 0 {
				t.Errorf("%s (%s) is the credential itself and names query parameters %v", row.Field, row.Path, row.SecretQuery)
			}
		default:
			t.Errorf("%s (%s) names the Credential %q, which is none of the three", row.Field, row.Path, row.Credential)
		}
		for _, key := range row.CredentialIn {
			if _, known := ClassOf(key); !known || key == row.Path {
				t.Errorf("%s (%s) points its credential at %q, which is no other key of legion.yaml", row.Field, row.Path, key)
			}
		}
	}
}

// The three kinds, path by path. A locator says where a credential lives and is stored like any
// other route change; an address is judged by the value a call carries; an inlined row is the
// credential and is refused whatever its value. A Secret row added to the table fails here until
// someone has said which it is.
func TestTheCredentialKindsAreTheOnesTheValuesAre(t *testing.T) {
	kinds := map[Credential][]string{}
	for _, row := range Classes {
		if row.Credential != "" && !slices.Contains(kinds[row.Credential], row.Path) {
			kinds[row.Credential] = append(kinds[row.Credential], row.Path)
		}
	}
	want := map[Credential][]string{
		CredentialLocator: {
			"dispatch_token_file", "envoy_token_file", "github_apps.implement",
			"github_apps.implement.private_key_command", "github_apps.implement.private_key_secret",
			"github_apps.implement.token_command", "github_apps.review", "github_apps.review.private_key_command",
			"github_apps.review.private_key_secret", "github_apps.review.token_command",
			"nats_daemon_nkey_seed_file", "nats_nkey_seed_file", "operator_token_file", "provider_keys",
			"provider_keys.<NAME>", "runtime.kubernetes.agent_secrets.token_file", "runtime.kubernetes.context",
			"runtime.kubernetes.kubeconfig", "runtime.kubernetes.session_store",
		},
		CredentialAddress: {
			"daemon_url", "dispatch_url", "envoy_url", "nats_urls", "postgres_dsn",
			"runtime.kubernetes.plugins.pi_envoy", "runtime.kubernetes.plugins.pi_legion",
			"runtime.kubernetes.pod.env", "runtime.tmux.pane_env",
		},
		CredentialInlined: {"github_apps.implement.private_key", "github_apps.review.private_key"},
	}
	for kind, paths := range kinds {
		slices.Sort(paths)
		if !slices.Equal(paths, want[kind]) {
			t.Errorf("the %s rows are %q, want %q", kind, paths, want[kind])
		}
	}
	if len(kinds) != len(want) {
		t.Errorf("the table names the kinds %v, want the three", slices.Sorted(maps.Keys(kinds)))
	}
}

// CarriesCredential reads the value a route call carries and nothing else: an inlined key is a
// credential whatever it holds but null, an address carries one in a non-empty userinfo or in a
// query parameter its row names, a block is read member by member at each member's own path,
// and a value carrying none is no refusal whatever its key. Every refusal names the path and
// where the credential belongs, and never quotes the value.
func TestCarriesCredentialReadsTheValueTheCallCarries(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		value      any
		// reason is what the refusal names, nil for a value that carries no credential.
		reason []string
		// secret is the part of the value the refusal must not quote.
		secret string
	}{
		{name: "an address with a password", path: "dispatch_url", value: "https://tok-dispatch:pw@dispatch.example",
			reason: []string{"dispatch_url carries a credential in an address's userinfo", "put the credential in dispatch_token_file"}, secret: "tok-dispatch:pw"},
		{name: "an address with a bare user", path: "envoy_url", value: "http://tok-envoy@envoy.example:9020",
			reason: []string{"envoy_url carries a credential", "envoy_token_file"}, secret: "tok-envoy"},
		{name: "one NATS URL of several with a token", path: "nats_urls", value: []any{"nats://h1:4222", "nats://tok-nats@h2:4222"},
			reason: []string{"nats_urls carries a credential", "nats_nkey_seed_file or nats_daemon_nkey_seed_file"}, secret: "tok-nats"},
		{name: "a NATS list of Go strings", path: "nats_urls", value: []string{"nats://tok-go@h1:4222"},
			reason: []string{"nats_urls carries a credential"}, secret: "tok-go"},
		{name: "the daemon's address, which no key holds a credential for", path: "daemon_url", value: "http://tok-daemon:pw@10.0.0.5:13370",
			reason: []string{"daemon_url carries a credential", "no other key of legion.yaml holds that credential", "belongs in legion.yaml"}, secret: "tok-daemon:pw"},
		{name: "a pin's pre-signed query", path: "runtime.kubernetes.plugins.pi_envoy", value: "https://cache.example/pi-envoy.tgz?X-Amz-Signature=sig-pin",
			reason: []string{"runtime.kubernetes.plugins.pi_envoy carries a credential in the query parameter X-Amz-Signature", "no other key"}, secret: "sig-pin"},
		{name: "a pin inside its block", path: "runtime.kubernetes.plugins",
			value:  map[string]any{"pi_envoy": "9.7.0", "pi_legion": "https://tok-block:pw@cache.example/pi-legion.tgz"},
			reason: []string{"runtime.kubernetes.plugins.pi_legion carries a credential in an address's userinfo"}, secret: "tok-block:pw"},
		{name: "a pane variable listing addresses", path: "runtime.tmux.pane_env", value: map[string]any{"NATS_LIST": "nats://h0:4222,nats://tok-pane@h1:4222"},
			reason: []string{"runtime.tmux.pane_env carries a credential", "provider_keys"}, secret: "tok-pane"},
		{name: "a pod variable with a database password", path: "runtime.kubernetes.pod.env", value: map[string]any{"DB": "postgres://tok-pod:pw@db/app"},
			reason: []string{"runtime.kubernetes.pod.env carries a credential", "provider_keys or runtime.kubernetes.pod.volumes"}, secret: "tok-pod:pw"},
		{name: "an inlined key", path: "github_apps.implement.private_key", value: "key-inline",
			reason: []string{"github_apps.implement.private_key is the credential itself", "github_apps.implement.private_key_secret or github_apps.implement.private_key_command"}, secret: "key-inline"},
		{name: "a fixed App's key inside the block set whole", path: "github_apps", value: map[string]any{
			"implement": map[string]any{"app_id": "1", "private_key_secret": "IMPLEMENT_KEY"},
			"review":    map[string]any{"app_id": "2", "private_key": "key-review"},
		}, reason: []string{"github_apps.review.private_key is the credential itself"}, secret: "key-review"},

		{name: "an @ in an address's path", path: "dispatch_url", value: "https://dispatch.example/a@b"},
		{name: "an @ in an address's query", path: "dispatch_url", value: "https://dispatch.example/?who=a@b"},
		{name: "addresses with no userinfo", path: "nats_urls", value: []string{"nats://h1:4222", "nats://h2:4222"}},
		{name: "a pin's query the code names no credential in", path: "runtime.kubernetes.plugins.pi_envoy", value: "https://cache.example/pi-envoy.tgz?X-Amz-Date=20261115&v=2"},
		{name: "a pin's tarball path", path: "runtime.kubernetes.plugins.pi_envoy", value: "/opt/legion/pi-envoy.tgz"},
		{name: "a pod variable holding a mail address", path: "runtime.kubernetes.pod.env", value: map[string]any{"GIT_EMAIL": "ana@example.com", "HTTP_PROXY": "http://proxy:3128"}},
		{name: "a withdrawn inlined key", path: "github_apps.implement.private_key", value: nil},
		{name: "an App on a key command", path: "github_apps.implement", value: map[string]any{"app_id": "1", "private_key_command": "agent-secrets get KEY"}},
		{name: "a credential file's path", path: "operator_token_file", value: "/etc/legion/operator-token"},
		{name: "a provider key's name", path: "provider_keys.GEMINI_API_KEY", value: "GEMINI_API_KEY_TESTS"},
		{name: "the provider keys whole", path: "provider_keys", value: map[string]any{"GEMINI_API_KEY": "GEMINI_API_KEY_TESTS"}},
		{name: "a fixed address, which the apply refuses with its own reason", path: "postgres_dsn", value: "postgres://legion:pw@db/legion"},
		{name: "a fixed App's key by its own path", path: "github_apps.review.private_key", value: "key"},
		{name: "a path no row holds", path: "no_such_key", value: map[string]any{"private_key": "key"}},
		{name: "a list of blocks", path: "runtime.kubernetes.scheduling.tolerations", value: []any{map[string]any{"key": "pool", "value": "legion"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			why, carried := CarriesCredential(tc.path, tc.value)
			if carried != (tc.reason != nil) {
				t.Fatalf("CarriesCredential(%s) = %q, %v; want carried %v", tc.path, why, carried, tc.reason != nil)
			}
			if !carried && why != "" {
				t.Fatalf("CarriesCredential(%s) carries nothing and says %q", tc.path, why)
			}
			for _, want := range tc.reason {
				if !strings.Contains(why, want) {
					t.Errorf("the refusal of %s says %q, which names no %q", tc.path, why, want)
				}
			}
			if tc.secret != "" && strings.Contains(why, tc.secret) {
				t.Errorf("the refusal of %s quotes the credential: %q", tc.path, why)
			}
		})
	}
}
