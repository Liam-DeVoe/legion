// packages/envoy/cmd/agent-secrets/redact_core_test.go
//
// What the machine, enroll and airlock code prints in place of a value a person could have put a
// secret in: AGENT_SECRETS_APPROVE_URL only as internal/redact's URL prints it, enroll's help
// without the directory AGENT_SECRETS_KEY_DIR names, and the airlock's refusals without the
// broker's proxy URL or the paths of the files a grant writes. Every secret here is made up.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/redact"
)

// TestMachineLoginPrintsTheApproveURLOnlyAsRedactPrintsIt: an AGENT_SECRETS_APPROVE_URL that
// carries a credential, or that is no plain http(s) URL, is not printed, and the line names the
// setting in its place; a plain one still prints as the page under it, and names the setting too.
func TestMachineLoginPrintsTheApproveURLOnlyAsRedactPrintsIt(t *testing.T) {
	binary := buildAgentSecrets(t)
	sock := fakeLoginHelper(t, "KQ7M-X4PZ", []string{"issued"})
	for _, row := range []struct {
		name, approve, want string
		secrets             []string
	}{
		// The user name and password are joined to the URL here so no line reads as a URL that
		// carries a credential.
		{"a credential in the URL", "https://" + "tokuser9f3" + ":" + "hunter2pw-approve-4c8a" + "@dispatch.example", redact.NotPrinted, []string{"tokuser9f3", "hunter2pw-approve-4c8a"}},
		{"a token pasted in place of the URL", "hunter2pw-approve-token-71bd", redact.NotPrinted, []string{"hunter2pw-approve-token-71bd"}},
		{"a query in the URL", "https://dispatch.example/?token=hunter2pw-approve-q-0e3f", redact.NotPrinted, []string{"hunter2pw-approve-q-0e3f"}},
		{"control: a plain URL", "https://dispatch.example/", "enter it at https://dispatch.example/credentials/machine", nil},
	} {
		t.Run(row.name, func(t *testing.T) {
			stdout, stderr, exit := runAgentSecrets(t, binary, "http://unused", t.TempDir(),
				[]string{"AGENT_SECRETS_HELPER_SOCK=" + sock, "AGENT_SECRETS_APPROVE_URL=" + row.approve}, "machine", "login")
			if exit != 0 {
				t.Fatalf("exit %d, want 0 (stdout %q, stderr %q)", exit, stdout, stderr)
			}
			for _, secret := range row.secrets {
				if strings.Contains(stdout+stderr, secret) {
					t.Errorf("stdout %q, stderr %q hold %q", stdout, stderr, secret)
				}
			}
			for _, want := range []string{row.want, "AGENT_SECRETS_APPROVE_URL"} {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout %q does not say %q", stdout, want)
				}
			}
		})
	}
}

// TestEnrollHelpPrintsNoKeyDirectory: enroll's help, and the usage a flag it does not take
// prints, never print the directory AGENT_SECRETS_KEY_DIR names as --out's default: a person's
// key dir is where key.pem is, and a help text is pasted wherever a person asks for help. The
// help still says AGENT_SECRETS_KEY_DIR is --out's default, and still prints the default of
// --pod-token-file, which is this program's own constant.
func TestEnrollHelpPrintsNoKeyDirectory(t *testing.T) {
	const secret = "hunter2pw-keydir-6e2f18"
	t.Setenv("AGENT_SECRETS_KEY_DIR", filepath.Join(t.TempDir(), secret))
	for _, row := range []struct {
		args []string
		exit int
	}{
		{[]string{"-h"}, 0},
		{[]string{"--no-such-flag"}, exitUsageError},
	} {
		var stdout, stderr bytes.Buffer
		if code := cmdEnroll(row.args, &stdout, &stderr); code != row.exit {
			t.Fatalf("enroll %v: exit %d, want %d (stderr %q)", row.args, code, row.exit, stderr.String())
		}
		if strings.Contains(stdout.String()+stderr.String(), secret) {
			t.Errorf("enroll %v printed the key dir: %q", row.args, stderr.String())
		}
		for _, want := range []string{"AGENT_SECRETS_KEY_DIR when not given", `(default "` + defaultPodTokenFile + `")`} {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("enroll %v: stderr %q does not say %q", row.args, stderr.String(), want)
			}
		}
	}
}

// TestAnAirlockProxyURLThatCannotBeUsedIsNotPrinted: a proxy URL in the broker's answer that
// does not parse, or names no host, is refused without the URL or the parser's words about it,
// which quote it whole; a proxy URL that names a host is still used.
func TestAnAirlockProxyURLThatCannotBeUsedIsNotPrinted(t *testing.T) {
	for _, row := range []struct {
		name, proxy, secret string
	}{
		{"a URL that does not parse", "http://" + "agent-secrets" + ":" + "hunter2pw-proxy-9d0b33" + "@[::1", "hunter2pw-proxy-9d0b33"},
		{"a URL that names no host", "hunter2pw-proxy-nohost-1a7c", "hunter2pw-proxy-nohost-1a7c"},
	} {
		t.Run(row.name, func(t *testing.T) {
			release := testRelease("AIRLOCK_ME", nil, nil)
			release.ProxyURL = row.proxy
			_, err := airlockProxyURL(release)
			if err == nil {
				t.Fatalf("airlockProxyURL accepted %q", row.proxy)
			}
			if strings.Contains(err.Error(), row.secret) {
				t.Errorf("the refusal %q holds %q", err, row.secret)
			}
			if !strings.Contains(err.Error(), "airlock proxy URL") {
				t.Errorf("the refusal %q does not name the airlock proxy URL", err)
			}
		})
	}
	t.Run("control: a URL that names a host", func(t *testing.T) {
		if _, err := airlockProxyURL(testRelease("AIRLOCK_ME", nil, nil)); err != nil {
			t.Fatalf("airlockProxyURL: %v", err)
		}
	})
}

// TestAnAirlockDirectoryThatCannotBePutInPlaceNamesNoPath: when a grant's directory cannot take
// its final name, the refusal says which step failed and the system's reason, and not the paths
// of the directory and the files in it, which hold the wgetrc the command reads. A root with
// nothing in the way still takes the grant's files.
func TestAnAirlockDirectoryThatCannotBePutInPlaceNamesNoPath(t *testing.T) {
	const secret = "xdg-hunter2pw-airlock-8b4e20"
	base := filepath.Join(t.TempDir(), secret)
	root := filepath.Join(base, "agent-secrets", "airlock")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	// A file where the grant's directory goes: the rename of a directory onto it is refused.
	if err := os.WriteFile(filepath.Join(root, "grant-one"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := prepareAirlock([]string{"XDG_RUNTIME_DIR=" + base}, testRelease("AIRLOCK_ME", nil, nil), "grant-one", nil)
	if err == nil {
		t.Fatal("prepareAirlock put its directory in place over a file")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the refusal %q holds the path %q", err, secret)
	}
	for _, want := range []string{"put the airlock directory in place", "rename: not a directory"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not say %q", err, want)
		}
	}

	t.Run("control: nothing in the way", func(t *testing.T) {
		if err := os.Remove(filepath.Join(root, "grant-one")); err != nil {
			t.Fatal(err)
		}
		files, err := prepareAirlock([]string{"XDG_RUNTIME_DIR=" + base}, testRelease("AIRLOCK_ME", nil, nil), "grant-one", nil)
		if err != nil {
			t.Fatalf("prepareAirlock: %v", err)
		}
		if _, err := os.Stat(files.wgetrc); err != nil {
			t.Fatalf("the wgetrc: %v", err)
		}
	})
}
