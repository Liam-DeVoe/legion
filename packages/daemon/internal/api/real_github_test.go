package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/config"
)

// githubAppsLib is the live proofs' App lib, scripts/e2e/lib/github-apps.sh: the one place this
// deployment's App pair, the checks on the three inputs that override it and on the review App's
// key command, which it requires and no default stands behind, and the token command are written.
var githubAppsLib = filepath.Join("..", "..", "..", "..", "scripts", "e2e", "lib", "github-apps.sh")

// liveProofAppInputs is the two inputs githubAppsLib reads for the implement role, each
// overriding one default: the App's numeric id and the name of the secrets broker's github-app
// secret that mints its token. The lib reads the review role's inputs too, which the shell stage
// proofs set; liveProofImplementApp reads the implement App alone and names none of them, so no
// run of it needs the operator's review key command.
var liveProofAppInputs = []string{
	"LEGION_E2E_IMPLEMENT_APP_ID", "LEGION_E2E_IMPLEMENT_APP_SECRET",
}

// liveProofApp is one role's App as githubAppsLib resolves it.
type liveProofApp struct{ id, secret, tokenCommand string }

// runGitHubAppsLib runs script in bash with lib as $1 and args after it, in environ less the
// variables that make bash run other code or trace its own: an exported SHELLOPTS with xtrace
// would print a refused input's value on stderr, BASHOPTS sets shell options, a BASH_ENV or ENV
// file could write to the stdout the caller parses, BASH_XTRACEFD and PS4 steer a trace, and an
// exported function replaces a builtin the lib calls. --norc keeps out the rc files a
// non-interactive bash built with SSH_SOURCE_BASHRC (Debian's and Ubuntu's) reads when SSH_CLIENT
// is set at a shell level below 2, as under a plain `ssh host go test …`.
func runGitHubAppsLib(lib string, environ []string, script string, args ...string) (stdout []byte, stderr string, err error) {
	command := exec.Command("bash", append([]string{"--norc", "-c", script, "bash", lib}, args...)...)
	command.Env = slices.DeleteFunc(slices.Clone(environ), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return strings.HasPrefix(name, "BASH_FUNC_") || slices.Contains(
			[]string{"SHELLOPTS", "BASHOPTS", "BASH_ENV", "ENV", "BASH_XTRACEFD", "PS4"}, name)
	})
	var errOut bytes.Buffer
	command.Stderr = &errOut
	stdout, err = command.Output()
	return stdout, strings.TrimSpace(errOut.String()), err
}

// liveProofImplementApp resolves the implement App by running githubAppsLib's own
// require_app_inputs and app_token_command in an environment of environ, so what
// TestLiveProofAppsReadTheLib holds is the lib's default, its input checks and its token command,
// and no second copy of any of them exists to drift. It asks for the implement role alone: the
// review App has no default credential, so naming that role would make every run of it need the
// operator's key command. A refusal returns the lib's message, which names the input and never
// its value.
func liveProofImplementApp(environ []string) (implement liveProofApp, err error) {
	return liveProofImplementAppFrom(githubAppsLib, environ)
}

// liveProofImplementAppFrom is liveProofImplementApp reading the lib at lib. It refuses anything
// but exactly three fields, each non-empty and on one line, and a token command that failed.
// Stray output on a line of its own is refused with them; stray output with no line break joins a
// field, which proveLiveProofApps's check then refuses, since no secret mints for that id.
func liveProofImplementAppFrom(lib string, environ []string) (implement liveProofApp, err error) {
	const script = `set -euo pipefail
fail() { printf '%s\n' "$*" >&2; exit 1; }
source "$1"
require_app_inputs implement
command=$(app_token_command "$implement_app_secret")
printf '%s\0%s\0%s\0' "$implement_app_id" "$implement_app_secret" "$command"`
	stdout, stderr, runErr := runGitHubAppsLib(lib, environ, script)
	if runErr != nil {
		return liveProofApp{}, fmt.Errorf("%s refused the App inputs (%v): %s", lib, runErr, stderr)
	}
	fields := strings.Split(strings.TrimSuffix(string(stdout), "\x00"), "\x00")
	if len(fields) != 3 {
		return liveProofApp{}, fmt.Errorf("%s printed %d fields for the implement App, want 3", lib, len(fields))
	}
	for index, field := range fields {
		if field == "" || strings.ContainsAny(field, "\r\n") {
			return liveProofApp{}, fmt.Errorf("%s printed field %d of the implement App empty or on more than one line", lib, index+1)
		}
	}
	return liveProofApp{fields[0], fields[1], fields[2]}, nil
}

// proveLiveProofApps proves implement, the App the stage proofs act as, against GitHub as every
// stage proof does: it writes implement as legion.yaml's `github_apps` block to
// work/github-apps.yaml and runs the lib's own check_github_apps on that file, so the implement
// App's token command must release that App's own token (GraphQL viewer, then GET /apps/<slug>)
// with the write permission its role needs, `contents`, which the review App does not hold. It
// returns the lib's notes, and on a refusal the lib's reason, which names the App by role and id
// and never a token.
func proveLiveProofApps(environ []string, work string, implement config.GitHubApp) (notes string, err error) {
	// A JSON string is a YAML double-quoted scalar, as the lib's own write_github_apps quotes it.
	id, _ := json.Marshal(implement.AppID)
	command, _ := json.Marshal(implement.TokenCommand)
	block := "github_apps:\n" + fmt.Sprintf("  implement:\n    app_id: %s\n    token_command: %s\n", id, command)
	if err := os.WriteFile(filepath.Join(work, "github-apps.yaml"), []byte(block), 0o600); err != nil {
		return "", err
	}
	const script = `set -euo pipefail
fail() { printf '%s\n' "$*" >&2; exit 1; }
note() { printf '%s\n' "$*" >&2; }
work=$2
source "$1"
check_github_apps implement`
	_, stderr, runErr := runGitHubAppsLib(githubAppsLib, environ, script, work)
	if runErr != nil {
		return stderr, fmt.Errorf("%s could not prove the Apps against GitHub (%v): %s", githubAppsLib, runErr, stderr)
	}
	return stderr, nil
}

// TestLiveProofAppsReadTheLib proves liveProofImplementApp, ungated: with no input set it answers exactly
// the implement App the lib's default_implement_app_* assignments hold, each set input replaces
// its value, and a malformed or empty input is refused naming the input and never its value.
// What it asserts of the lib's defaults is the implement pair alone, and it asserts the implement
// App's token command asks the broker for that App's secret with the owner, repositories and
// read-only narrowing the daemon's environment names. The lib's review defaults are
// scripts/e2e/lib/app-logins.test.ts's business.
func TestLiveProofAppsReadTheLib(t *testing.T) {
	inputs := liveProofAppInputs
	base := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !slices.Contains(inputs, name) {
			base = append(base, entry)
		}
	}
	with := func(set ...string) []string { return append(slices.Clone(base), set...) }

	lib, err := os.ReadFile(githubAppsLib)
	if err != nil {
		t.Fatalf("read the live proofs' App lib: %v", err)
	}
	defaults := map[string]string{}
	for _, match := range regexp.MustCompile(`(?m)^default_(implement_app_(?:id|secret))=(.*)$`).FindAllStringSubmatch(string(lib), -1) {
		if _, twice := defaults[match[1]]; twice {
			t.Fatalf("%s assigns default_%s twice", githubAppsLib, match[1])
		}
		defaults[match[1]] = match[2]
	}
	if len(defaults) != 2 {
		t.Fatalf("%s assigns %d of the two default_implement_app_* values, want both: %v", githubAppsLib, len(defaults), defaults)
	}

	implement, err := liveProofImplementApp(with())
	if err != nil {
		t.Fatal(err)
	}
	if implement.id != defaults["implement_app_id"] || implement.secret != defaults["implement_app_secret"] {
		t.Errorf("with no input the implement App is %s/%s, want the lib's default %s/%s",
			implement.id, implement.secret, defaults["implement_app_id"], defaults["implement_app_secret"])
	}
	if !strings.HasPrefix(implement.tokenCommand, "agent-secrets "+implement.secret+" --ttl 12h --reason=legion-live-proof-github-app-token ") {
		t.Errorf("the implement App's token command reads %q, want `agent-secrets %s --ttl 12h --reason=… -- …`", implement.tokenCommand, implement.secret)
	}

	implement, err = liveProofImplementApp(with(
		"LEGION_E2E_IMPLEMENT_APP_ID=101", "LEGION_E2E_IMPLEMENT_APP_SECRET=EXAMPLE_IMPLEMENT_APP",
	))
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{implement.id, implement.secret}; !slices.Equal(got, []string{"101", "EXAMPLE_IMPLEMENT_APP"}) {
		t.Errorf("with every input set the App is %v, want the inputs", got)
	}
	// The token command is what the daemon runs, so it must ask the broker for the input's secret,
	// narrowed to the owner and to read-only exactly when the daemon's environment asks for each
	// (appauth.TokenCommandOwner, appauth.TokenCommandReadOnly), and print the release as the
	// broker hands it over. It names no repository: the daemon asks for no narrowing by
	// repository (INFRA-2543). A stand-in `agent-secrets` records its arguments and puts a
	// release in the command's environment as the real one does.
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "agent-secrets"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >\"$STUB_ARGS\"\nname=$1\nwhile [ \"$1\" != -- ]; do shift; done\nshift\nexport \"$name=$STUB_VALUE\"\nexec \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	release := `{"installations":{"acme":{"installation_id":1,"token":"ghs_stub","expires_at":"2026-11-03T13:00:00Z"}}}`
	secret := "EXAMPLE_IMPLEMENT_APP"
	for _, ask := range []struct {
		owner, readOnly, args string
	}{
		{"", "", secret + " --ttl 12h --reason=legion-live-proof-github-app-token -- sh -c printf %s \"$" + secret + "\""},
		{"acme", "", secret + " --ttl 12h --reason=legion-live-proof-github-app-token --github-owner acme -- sh -c printf %s \"$" + secret + "\""},
		{"acme", "1", secret + " --ttl 12h --reason=legion-live-proof-github-app-token --github-owner acme --github-read-only -- sh -c printf %s \"$" + secret + "\""},
	} {
		args := filepath.Join(t.TempDir(), "args")
		run := exec.Command("sh", "-c", implement.tokenCommand)
		run.Env = append(with(), "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"), "STUB_VALUE="+release, "STUB_ARGS="+args,
			"LEGION_GITHUB_OWNER="+ask.owner, "LEGION_GITHUB_READ_ONLY="+ask.readOnly)
		got, err := run.Output()
		if err != nil || string(got) != release {
			t.Errorf("the implement App's token command for owner %q read-only %q printed %q (err %v), want the release", ask.owner, ask.readOnly, got, err)
		}
		if seen, _ := os.ReadFile(args); strings.TrimSpace(string(seen)) != ask.args {
			t.Errorf("the implement App's token command for owner %q read-only %q ran agent-secrets %q, want %q", ask.owner, ask.readOnly, strings.TrimSpace(string(seen)), ask.args)
		}
	}

	pasted := strings.Repeat("Zm9vYmFy", 9) // 72 base64 characters: a value, not a secret's name
	refuses := func(t *testing.T, input, value string, extra ...string) {
		t.Helper()
		_, err := liveProofImplementApp(append(with(input+"="+value), extra...))
		if err == nil || !strings.Contains(err.Error(), input) || (value != "" && strings.Contains(err.Error(), value)) {
			t.Errorf("%s=%q with %v: err = %v, want a refusal naming the input and not its value", input, value, extra, err)
		}
	}
	refuses(t, "LEGION_E2E_IMPLEMENT_APP_SECRET", pasted)
	refuses(t, "LEGION_E2E_IMPLEMENT_APP_ID", "")
	refuses(t, "LEGION_E2E_IMPLEMENT_APP_ID", "0123")
	refuses(t, "LEGION_E2E_IMPLEMENT_APP_SECRET", pasted, "SHELLOPTS=braceexpand:hashall:interactive-comments:xtrace")

	t.Run("a UTF-8 locale admits nothing the C locale refuses", func(t *testing.T) {
		// Under en_US.UTF-8 bash's [0-9] matches fullwidth digits, which is what the lib's local
		// LC_ALL=C guards against. Without that locale bash falls back to C, where the case
		// proves nothing, so it is skipped rather than passed.
		probe := exec.Command("bash", "--norc", "-c", `[[ ３ =~ ^[0-9]$ ]]`)
		probe.Env = append(with(), "LC_ALL=en_US.UTF-8")
		if probe.Run() != nil {
			t.Skip("bash under LC_ALL=en_US.UTF-8 does not match a fullwidth digit with [0-9] here: the en_US.UTF-8 locale is absent, so this case cannot show the guard")
		}
		refuses(t, "LEGION_E2E_IMPLEMENT_APP_ID", "３２０２６３６", "LC_ALL=en_US.UTF-8")
		refuses(t, "LEGION_E2E_IMPLEMENT_APP_SECRET", "K\u00e9Y", "LC_ALL=en_US.UTF-8")
	})

	startup := filepath.Join(t.TempDir(), "bash_env")
	if err := os.WriteFile(startup, []byte("printf 'noise from BASH_ENV\\n'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, inherited := range [][]string{
		{"BASH_ENV=" + startup},
		{"BASH_FUNC_printf%%=() { builtin printf 'HIJACK|'; builtin printf \"$@\"; }"},
	} {
		implement, err = liveProofImplementApp(append(with(), inherited...))
		if err != nil || implement.id != defaults["implement_app_id"] {
			t.Errorf("with %v inherited the implement App is %q (err %v), want the lib's default %s", inherited, implement.id, err, defaults["implement_app_id"])
		}
	}

	t.Run("an ssh environment's rc file does not reach the lib's child", func(t *testing.T) {
		// A non-interactive bash built with SSH_SOURCE_BASHRC reads ~/.bashrc when SSH_CLIENT is set
		// at a shell level below 2; this one writes to stdout and exports a function. Where bash is
		// built without it, no rc file is read with or without --norc, so the case is skipped.
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("echo 'bashrc ran'\nhijack() { :; }\nexport -f hijack\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		ssh := append(with(), "SSH_CLIENT=192.0.2.1 1 22", "SHLVL=0", "HOME="+home)
		probe := exec.Command("bash", "-c", ":")
		probe.Env = ssh
		if out, _ := probe.Output(); !strings.Contains(string(out), "bashrc ran") {
			t.Skip("this bash reads no ~/.bashrc for a non-interactive shell under SSH_CLIENT (built without SSH_SOURCE_BASHRC), so this case cannot show --norc")
		}
		implement, err := liveProofImplementApp(ssh)
		if err != nil || implement.id != defaults["implement_app_id"] {
			t.Errorf("under an ssh environment the implement App is %q (err %v), want the lib's default %s", implement.id, err, defaults["implement_app_id"])
		}
	})

	t.Run("the token check reads the block the proofs act with", func(t *testing.T) {
		// A stand-in bun takes app-logins.ts's place: it keeps the block it was given and answers as
		// GitHub agreed, or refuses as a GitHub that answered another id. The block must be the one
		// the lib's own write_github_apps writes for the same Apps.
		stub, work, want := t.TempDir(), t.TempDir(), t.TempDir()
		bun := "#!/bin/sh\ncp \"$4\" \"$STUB_KEEP\"\n[ -z \"$STUB_REFUSE\" ] || { echo 'app-logins: the implement App answered another id' >&2; exit 1; }\necho '{\"implement\":\"implement[bot]\",\"review\":\"review[bot]\"}'\n"
		if err := os.WriteFile(filepath.Join(stub, "bun"), []byte(bun), 0o700); err != nil {
			t.Fatal(err)
		}
		path := "PATH=" + stub + string(os.PathListSeparator) + os.Getenv("PATH")
		kept := filepath.Join(stub, "kept.yaml")
		implement, err := liveProofImplementApp(with())
		if err != nil {
			t.Fatal(err)
		}
		apps := config.GitHubApps{
			Implement: config.GitHubApp{AppID: implement.id, TokenCommand: implement.tokenCommand},
		}
		if _, err := proveLiveProofApps(append(with(), path, "STUB_KEEP="+kept), work, apps.Implement); err != nil {
			t.Fatalf("a GitHub that agrees: %v", err)
		}
		if _, stderr, err := runGitHubAppsLib(githubAppsLib, with(), `set -euo pipefail
fail() { printf '%s\n' "$*" >&2; exit 1; }
work=$2
source "$1"
require_app_inputs implement
write_github_apps implement`, want); err != nil {
			t.Fatalf("the lib's write_github_apps: %v: %s", err, stderr)
		}
		got, _ := os.ReadFile(kept)
		lib, _ := os.ReadFile(filepath.Join(want, "github-apps.yaml"))
		if len(got) == 0 || !bytes.Equal(got, lib) {
			t.Errorf("the token check read\n%s\nwant the block the lib writes for the same Apps\n%s", got, lib)
		}
		if _, err := proveLiveProofApps(append(with(), path, "STUB_KEEP="+kept, "STUB_REFUSE=1"), t.TempDir(), apps.Implement); err == nil || !strings.Contains(err.Error(), "answered another id") {
			t.Errorf("a GitHub that refuses: err = %v, want the checker's refusal", err)
		}
	})

	t.Run("the lib's child inherits none of bash's startup or trace variables", func(t *testing.T) {
		stdout, stderr, err := runGitHubAppsLib(githubAppsLib, append(with(),
			"SHELLOPTS=braceexpand:hashall:interactive-comments", "BASHOPTS=extglob", "BASH_ENV="+startup,
			"ENV="+startup, "BASH_XTRACEFD=2", "PS4=+ ", "BASH_FUNC_hijack%%=() { :; }",
		), `exec env -0`)
		if err != nil {
			t.Fatalf("env in the lib's child: %v: %s", err, stderr)
		}
		for _, entry := range strings.Split(string(stdout), "\x00") {
			name, _, _ := strings.Cut(entry, "=")
			if strings.HasPrefix(name, "BASH_FUNC_") || slices.Contains([]string{"SHELLOPTS", "BASHOPTS", "BASH_ENV", "ENV", "BASH_XTRACEFD", "PS4"}, name) {
				t.Errorf("the lib's child inherits %s", name)
			}
		}
	})

	t.Run("a lib whose output is malformed yields no App", func(t *testing.T) {
		// Each scratch lib sources the real one, then breaks one thing about what it prints. The
		// control breaks nothing, so a scratch lib that fails to load cannot pass as a refusal.
		real, err := filepath.Abs(githubAppsLib)
		if err != nil {
			t.Fatal(err)
		}
		wrap := `eval "$(declare -f require_app_inputs | sed '1s/^require_app_inputs/real_require_app_inputs/')"` + "\n"
		for _, scratch := range []struct{ name, body, refusal string }{
			{"the control", wrap + `require_app_inputs() { real_require_app_inputs "$@"; }`, ""},
			{"a fourth field", wrap + `require_app_inputs() { real_require_app_inputs "$@"; printf 'extra\0'; }`, "printed 4 fields"},
			{"stray output on a line of its own", wrap + `require_app_inputs() { real_require_app_inputs "$@"; echo debug; }`, "field 1 of the implement App empty or on more than one line"},
			{"an empty token command", `app_token_command() { :; }`, "field 3 of the implement App empty or on more than one line"},
			{"a token command that failed", `app_token_command() { printf 'agent-secrets %s' "$1"; return 3; }`, "exit status 3"},
			{"a token command on two lines", `app_token_command() { printf 'agent-secrets %s\nmore' "$1"; }`, "field 3 of the implement App empty or on more than one line"},
		} {
			lib := filepath.Join(t.TempDir(), "github-apps.sh")
			if err := os.WriteFile(lib, []byte("source "+strconv.Quote(real)+"\n"+scratch.body+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			implement, err := liveProofImplementAppFrom(lib, with())
			switch {
			case scratch.refusal == "" && (err != nil || implement.id != defaults["implement_app_id"]):
				t.Errorf("%s: got App %+v (err %v), want the lib's default implement App", scratch.name, implement, err)
			case scratch.refusal != "" && (err == nil || !strings.Contains(err.Error(), scratch.refusal)):
				t.Errorf("%s: got App %+v (err %v), want a refusal saying %q", scratch.name, implement, err, scratch.refusal)
			}
		}
	})
}
