// packages/envoy/cmd/cipher/proxy_trust_store_vars_test.go
//
// The decision to write no trust bundle rests on the machine's system trust store. x509.SystemCertPool
// reads that store through SSL_CERT_FILE and SSL_CERT_DIR where the command's environment sets
// them, but git's HTTPS transport (Debian's libcurl built on GnuTLS) and wget (GnuTLS) read
// neither variable: they read the system bundle alone, and GIT_SSL_CAINFO and the wgetrc that
// WGETRC names. So a command whose own SSL_CERT_FILE or SSL_CERT_DIR names the proxy's root, on a
// machine whose system store lacks it, still needs the bundle. Without it, git and wget refuse the
// proxy's certificate where, with the bundle, GIT_SSL_CAINFO and WGETRC carried the root to them.
package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/cipher/proxyrules/proxyrulestest"
)

// storeVarsChildInput is what TestProxyTrustStoreVarsChild reads: the environment the command has,
// which is also the child process's own environment, as os.Environ is in cmdExec.
type storeVarsChildInput struct {
	Environ       []string `json:"environ"`
	CACertificate string   `json:"ca_certificate"`
}

type storeVarsChildOutput struct {
	Bundle string            `json:"bundle"`
	Vars   map[string]string `json:"vars"`
	Error  string            `json:"error"`
}

// TestProxyTrustStoreVarsChild is the child process of runStoreVarsChild, skipped in every other
// run: x509.SystemCertPool reads SSL_CERT_FILE and SSL_CERT_DIR once per process.
func TestProxyTrustStoreVarsChild(t *testing.T) {
	inputPath := os.Getenv("AGENT_SECRETS_STORE_VARS_CHILD_INPUT")
	if inputPath == "" {
		t.Skip("subprocess of runStoreVarsChild")
	}
	data, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatalf("read the input: %v", err)
	}
	var in storeVarsChildInput
	if err := json.Unmarshal(data, &in); err != nil {
		t.Fatalf("parse the input: %v", err)
	}
	release := testRelease("AIRLOCK_ME", []string{"api.example.com"}, nil)
	release.CACertificate = in.CACertificate
	var out storeVarsChildOutput
	files, err := prepareAirlock(in.Environ, release, "grant-one", nil)
	if err == nil {
		out.Bundle = files.bundle
		out.Vars, err = airlockVars(in.Environ, release, files)
	}
	if err != nil {
		out.Error = err.Error()
	}
	answer, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("encode the output: %v", err)
	}
	if err := os.WriteFile(os.Getenv("AGENT_SECRETS_STORE_VARS_CHILD_OUTPUT"), answer, 0o600); err != nil {
		t.Fatalf("write the output: %v", err)
	}
}

// runStoreVarsChild runs TestProxyTrustStoreVarsChild with environ as both the command's
// environment and the process's own, on this machine's real system store: this process's
// SSL_CERT_FILE and SSL_CERT_DIR are not passed on, so only environ's can name a store.
func runStoreVarsChild(t *testing.T, caPEM string, environ []string) storeVarsChildOutput {
	t.Helper()
	scratch := t.TempDir()
	input, err := json.Marshal(storeVarsChildInput{Environ: environ, CACertificate: caPEM})
	if err != nil {
		t.Fatal(err)
	}
	inputPath, outputPath := filepath.Join(scratch, "input.json"), filepath.Join(scratch, "output.json")
	if err := os.WriteFile(inputPath, input, 0o600); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "SSL_CERT_FILE=") && !strings.HasPrefix(e, "SSL_CERT_DIR=") {
			env = append(env, e)
		}
	}
	cmd := exec.Command(self, "-test.run", "^TestProxyTrustStoreVarsChild$", "-test.count", "1")
	cmd.Env = append(append(env, environ...),
		"AGENT_SECRETS_STORE_VARS_CHILD_INPUT="+inputPath,
		"AGENT_SECRETS_STORE_VARS_CHILD_OUTPUT="+outputPath)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the child process: %v\n%s", err, combined)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("the child process wrote no answer: %v", err)
	}
	var out storeVarsChildOutput
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse the child's answer %q: %v", data, err)
	}
	if out.Error != "" {
		t.Fatalf("the child process: %s", out.Error)
	}
	return out
}

// TestPrepareAirlockWritesTheBundleWhereOnlyTheCommandsStoreVariablesNameTheRoot: the system store
// here is this machine's own, which lacks a root made for this test. The command's SSL_CERT_FILE
// (the system bundle plus the root) or SSL_CERT_DIR (a directory holding the root) names the
// root, so x509.SystemCertPool trusts it, but git and wget read neither variable. The bundle must
// still be written, with GIT_SSL_CAINFO and WGETRC naming it, as before the change.
func TestPrepareAirlockWritesTheBundleWhereOnlyTheCommandsStoreVariablesNameTheRoot(t *testing.T) {
	authority, err := proxyrulestest.NewAuthority("test proxy root for the store variables")
	if err != nil {
		t.Fatal(err)
	}
	root := string(authority.CertificatePEM())
	system := ""
	for _, path := range systemTrustFiles {
		if data, err := os.ReadFile(path); err == nil {
			system = string(data)
			break
		}
	}
	if system != "" && strings.Contains(system, strings.TrimSpace(root)) {
		t.Fatalf("the system bundle already holds the root made for this test")
	}
	fileWithRoot := writeLoginFile(t, "system-and-root.pem", system+root)
	dirWithRoot := filepath.Dir(writeLoginFile(t, "root.pem", root))
	for _, row := range []struct{ name, key, value string }{
		{name: "SSL_CERT_FILE names the system bundle plus the root", key: "SSL_CERT_FILE", value: fileWithRoot},
		{name: "SSL_CERT_DIR names a directory holding the root", key: "SSL_CERT_DIR", value: dirWithRoot},
	} {
		t.Run(row.name, func(t *testing.T) {
			runtimeDir := t.TempDir()
			out := runStoreVarsChild(t, root, []string{"XDG_RUNTIME_DIR=" + runtimeDir, row.key + "=" + row.value})
			t.Logf("the child answered: %+v", out)
			if out.Bundle == "" {
				t.Fatalf("prepareAirlock wrote no bundle, though the system store lacks the root and only the command's %s names it; git and wget, which read no %s, then refuse the proxy", row.key, row.key)
			}
			for _, key := range []string{"GIT_SSL_CAINFO", "CURL_CA_BUNDLE", "SSL_CERT_FILE"} {
				if out.Vars[key] != out.Bundle {
					t.Errorf("the command's %s is %q, want the bundle %q", key, out.Vars[key], out.Bundle)
				}
			}
			if out.Vars["WGETRC"] == "" {
				t.Errorf("the command's WGETRC is not set; wget reads the bundle from no other place")
			}
		})
	}
}
