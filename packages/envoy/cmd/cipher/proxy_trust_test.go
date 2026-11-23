// packages/envoy/cmd/cipher/proxy_trust_test.go
//
// When a command needs no trust bundle: the machine's system trust store already trusts every
// root the proxy's release carries, and no trust variable or wgetrc the command has names a file
// that lacks one. Then nothing is written and no trust variable is set. Otherwise prepareAirlock
// writes the bundle and every trust variable names it. The roots here are made for each test and
// are public certificates, so these tests print what they compare.
package main

import (
	"crypto/x509"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/cipher/proxyrules/proxyrulestest"
)

// proxyTestRoot is a self-signed certificate authority made for one test, in the proxy root's
// shape, as the PEM a release carries.
func proxyTestRoot(t *testing.T, name string) string {
	t.Helper()
	authority, err := proxyrulestest.NewAuthority(name)
	if err != nil {
		t.Fatalf("make %s: %v", name, err)
	}
	return string(authority.CertificatePEM())
}

// proxyTestPool is a certificate pool that holds the roots given, standing for a system store.
func proxyTestPool(t *testing.T, roots ...string) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	for _, root := range roots {
		if !pool.AppendCertsFromPEM([]byte(root)) {
			t.Fatalf("the test pool took no certificate from %q", root)
		}
	}
	return pool
}

// TestProxyRootsTrustedOnlyWhenTheStoreHoldsEveryRoot: the store must trust each root the release
// carries. While the proxy's root changes, a release carries two, the old root and the new, so a
// store that holds one of the two still gets the bundle. A release with no root that parses is no
// root the store trusts.
func TestProxyRootsTrustedOnlyWhenTheStoreHoldsEveryRoot(t *testing.T) {
	newRoot, oldRoot := proxyTestRoot(t, "test proxy root"), proxyTestRoot(t, "test old authority")
	for _, row := range []struct {
		name    string
		release string
		store   []string
		want    bool
	}{
		{name: "the store holds the one root", release: newRoot, store: []string{newRoot}, want: true},
		{name: "the store holds another root", release: newRoot, store: []string{oldRoot}},
		{name: "an empty store", release: newRoot},
		{name: "a two-root release and a store that holds one of them", release: oldRoot + newRoot, store: []string{newRoot}},
		{name: "a two-root release and a store that holds both", release: oldRoot + newRoot, store: []string{newRoot, oldRoot}, want: true},
		{name: "a release with no certificate", release: "", store: []string{newRoot}},
		{name: "a release whose one block does not parse", release: testAirlockCertificate, store: []string{newRoot}},
		{name: "a release with a root and a block that does not parse", release: newRoot + testAirlockCertificate, store: []string{newRoot}},
		{name: "a release with a block that does not parse and a root", release: testAirlockCertificate + newRoot, store: []string{newRoot}},
	} {
		t.Run(row.name, func(t *testing.T) {
			if got := proxyRootsTrusted(nil, row.release, proxyTestPool(t, row.store...)); got != row.want {
				t.Fatalf("proxyRootsTrusted = %v, want %v", got, row.want)
			}
		})
	}
}

// TestProxyRootsTrustedReadsTheTrustVariablesTheCommandHas: a trust variable the command already
// has replaces its client's roots, and with no bundle the command keeps it as it is. So a file it
// names that lacks a root takes the proxy away from that client, and the bundle is still written.
// NODE_EXTRA_CA_CERTS is the exception: Node adds its file to the roots it has, so a file without
// the root takes nothing away. A file that cannot be read cannot show that it holds the root.
func TestProxyRootsTrustedReadsTheTrustVariablesTheCommandHas(t *testing.T) {
	root, other := proxyTestRoot(t, "test proxy root"), proxyTestRoot(t, "test private authority")
	store := proxyTestPool(t, root)
	without := writeLoginFile(t, "private.pem", other)
	with := writeLoginFile(t, "both.pem", other+root)
	unreadable := filepath.Join(t.TempDir(), "not-there.pem")

	for _, key := range airlockBundleVars {
		want := key == "NODE_EXTRA_CA_CERTS"
		if got := proxyRootsTrusted([]string{key + "=" + without}, root, store); got != want {
			t.Errorf("%s naming a file without the root: proxyRootsTrusted = %v, want %v", key, got, want)
		}
		if got := proxyRootsTrusted([]string{key + "=" + with}, root, store); !got {
			t.Errorf("%s naming a file that holds the root: proxyRootsTrusted = false, want true", key)
		}
	}
	if proxyRootsTrusted([]string{"REQUESTS_CA_BUNDLE=" + unreadable}, root, store) {
		t.Errorf("REQUESTS_CA_BUNDLE naming a file that cannot be read: proxyRootsTrusted = true, want false")
	}
	if !proxyRootsTrusted([]string{"CURL_CA_BUNDLE="}, root, store) {
		t.Errorf("an empty CURL_CA_BUNDLE, which names no file: proxyRootsTrusted = false, want true")
	}
	// The last entry wins, as execve reads it.
	if proxyRootsTrusted([]string{"CURL_CA_BUNDLE=" + with, "CURL_CA_BUNDLE=" + without}, root, store) {
		t.Errorf("CURL_CA_BUNDLE set twice, the last naming a file without the root: proxyRootsTrusted = true, want false")
	}
}

// TestProxyRootsTrustedReadsTheWgetrcTheCommandHas: wget reads no trust variable, only its
// wgetrc, and with no bundle the command keeps the WGETRC it has. A ca_directory there replaces
// the system store, so the last one, the one wget reads, must hold every root; a ca_certificate
// adds to the store's trust and takes nothing away; and a wgetrc or a directory this process
// cannot read is one it cannot vouch for. wget ignores case, '_' and '-' in a command's name, and
// skips a line that opens with '#'.
func TestProxyRootsTrustedReadsTheWgetrcTheCommandHas(t *testing.T) {
	root, other := proxyTestRoot(t, "test proxy root"), proxyTestRoot(t, "test private authority")
	store := proxyTestPool(t, root)
	without := writeLoginFile(t, "private.pem", other)
	dirWithout := filepath.Dir(writeLoginFile(t, "private.pem", other))
	dirWith := filepath.Dir(writeLoginFile(t, "root.pem", root))
	if err := os.WriteFile(filepath.Join(dirWith, "private.pem"), []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "not-there")
	for _, row := range []struct {
		name   string
		wgetrc string
		want   bool
	}{
		{name: "a wgetrc with no trust setting", wgetrc: "tries = 3\n", want: true},
		{name: "a ca_certificate without the root", wgetrc: "ca_certificate = " + without + "\n", want: true},
		{name: "a ca_directory that holds the root", wgetrc: "tries = 3\nca_directory = " + dirWith + "\n", want: true},
		{name: "a ca_directory without the root", wgetrc: "tries = 3\nca_directory = " + dirWithout + "\n"},
		{name: "the same setting spelt with dashes and capitals", wgetrc: "CA-Directory=" + dirWithout + "\n"},
		{name: "two ca_directory lines, the last without the root", wgetrc: "ca_directory = " + dirWith + "\nca_directory = " + dirWithout + "\n"},
		{name: "two ca_directory lines, the last with the root", wgetrc: "ca_directory = " + dirWithout + "\nca_directory = " + dirWith + "\n", want: true},
		{name: "a commented-out ca_directory", wgetrc: "# ca_directory = " + dirWithout + "\n", want: true},
		{name: "a ca_directory that cannot be read", wgetrc: "ca_directory = " + missing + "\n"},
	} {
		t.Run(row.name, func(t *testing.T) {
			wgetrc := writeLoginFile(t, "wgetrc", row.wgetrc)
			if got := proxyRootsTrusted([]string{"WGETRC=" + wgetrc}, root, store); got != row.want {
				t.Fatalf("proxyRootsTrusted with the wgetrc %q = %v, want %v", row.wgetrc, got, row.want)
			}
		})
	}
	if proxyRootsTrusted([]string{"WGETRC=" + missing}, root, store) {
		t.Errorf("WGETRC naming a file that cannot be read: proxyRootsTrusted = true, want false")
	}
}

// proxyTrustChildInput and proxyTrustChildOutput are what TestProxyTrustChild reads and writes:
// the environment and release it prepares the command's files for, and the paths and variables
// that prepareAirlock and airlockVars answered.
type proxyTrustChildInput struct {
	Environ       []string `json:"environ"`
	CACertificate string   `json:"ca_certificate"`
}

type proxyTrustChildOutput struct {
	Bundle string            `json:"bundle"`
	Wgetrc string            `json:"wgetrc"`
	Vars   map[string]string `json:"vars"`
	Error  string            `json:"error"`
}

// TestProxyTrustChild is the child process of runProxyTrustChild, skipped in every other run.
// x509.SystemCertPool reads SSL_CERT_DIR once per process, so each store a test stands up needs a
// process of its own.
func TestProxyTrustChild(t *testing.T) {
	inputPath := os.Getenv("AGENT_SECRETS_PROXY_TRUST_CHILD_INPUT")
	if inputPath == "" {
		t.Skip("subprocess of runProxyTrustChild")
	}
	data, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatalf("read the input: %v", err)
	}
	var in proxyTrustChildInput
	if err := json.Unmarshal(data, &in); err != nil {
		t.Fatalf("parse the input: %v", err)
	}
	release := testRelease("AIRLOCK_ME", []string{"api.example.com"}, nil)
	release.CACertificate = in.CACertificate
	var out proxyTrustChildOutput
	files, err := prepareAirlock(in.Environ, release, "grant-one", nil)
	if err == nil {
		out.Bundle, out.Wgetrc = files.bundle, files.wgetrc
		out.Vars, err = airlockVars(in.Environ, release, files)
	}
	if err != nil {
		out.Error = err.Error()
	}
	answer, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("encode the output: %v", err)
	}
	if err := os.WriteFile(os.Getenv("AGENT_SECRETS_PROXY_TRUST_CHILD_OUTPUT"), answer, 0o600); err != nil {
		t.Fatalf("write the output: %v", err)
	}
}

// runProxyTrustChild runs TestProxyTrustChild in a process of its own whose SSL_CERT_DIR is a
// directory holding the store roots, the way a system store holds them, and answers what it
// prepared for a release carrying caPEM, with environ as the environment the command has.
func runProxyTrustChild(t *testing.T, store []string, caPEM string, environ []string) proxyTrustChildOutput {
	t.Helper()
	storeDir := t.TempDir()
	for i, root := range store {
		if err := os.WriteFile(filepath.Join(storeDir, "root-"+strconv.Itoa(i)+".pem"), []byte(root), 0o644); err != nil {
			t.Fatalf("write the store's root: %v", err)
		}
	}
	scratch := t.TempDir()
	input, err := json.Marshal(proxyTrustChildInput{Environ: environ, CACertificate: caPEM})
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
	cmd := exec.Command(self, "-test.run", "^TestProxyTrustChild$", "-test.count", "1")
	cmd.Env = append(os.Environ(),
		"SSL_CERT_DIR="+storeDir,
		"AGENT_SECRETS_PROXY_TRUST_CHILD_INPUT="+inputPath,
		"AGENT_SECRETS_PROXY_TRUST_CHILD_OUTPUT="+outputPath)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the child process: %v\n%s", err, combined)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("the child process wrote no answer: %v", err)
	}
	var out proxyTrustChildOutput
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse the child's answer %q: %v", data, err)
	}
	if out.Error != "" {
		t.Fatalf("the child process: %s", out.Error)
	}
	return out
}

// TestPrepareAirlockLeavesTheTrustAloneWhereTheSystemStoreHoldsTheRoot runs prepareAirlock and
// airlockVars against a real system store, x509.SystemCertPool read through SSL_CERT_DIR, with
// and without the release's roots in it. Where the store trusts every root and no trust variable
// or wgetrc of the command names a file that lacks one, no file is written and none of the trust
// variables or WGETRC is set, while the proxy still is. Every other row writes the bundle, holding
// every root, and points each trust variable and WGETRC at it.
func TestPrepareAirlockLeavesTheTrustAloneWhereTheSystemStoreHoldsTheRoot(t *testing.T) {
	newRoot, oldRoot, private := proxyTestRoot(t, "test proxy root"), proxyTestRoot(t, "test old authority"), proxyTestRoot(t, "test private authority")
	withoutRoot := writeLoginFile(t, "private.pem", private)
	withRoot := writeLoginFile(t, "private-and-root.pem", private+newRoot)
	wgetrcWithoutRoot := writeLoginFile(t, "wgetrc", "ca_directory = "+filepath.Dir(withoutRoot)+"\n")
	for _, row := range []struct {
		name       string
		store      []string
		release    string
		environ    []string
		wantBundle bool
	}{
		{name: "the store holds the root", store: []string{newRoot}, release: newRoot},
		{name: "the store lacks the root", store: []string{private}, release: newRoot, wantBundle: true},
		{name: "a two-root release and a store that holds one of them", store: []string{newRoot}, release: oldRoot + newRoot, wantBundle: true},
		{name: "a two-root release and a store that holds both", store: []string{newRoot, oldRoot}, release: oldRoot + newRoot},
		{name: "the command's CURL_CA_BUNDLE names a file without the root", store: []string{newRoot}, release: newRoot, environ: []string{"CURL_CA_BUNDLE=" + withoutRoot}, wantBundle: true},
		{name: "the command's CURL_CA_BUNDLE names a file that holds the root", store: []string{newRoot}, release: newRoot, environ: []string{"CURL_CA_BUNDLE=" + withRoot}},
		{name: "the command's wgetrc names a ca_directory without the root", store: []string{newRoot}, release: newRoot, environ: []string{"WGETRC=" + wgetrcWithoutRoot}, wantBundle: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			runtimeDir := t.TempDir()
			environ := append([]string{"XDG_RUNTIME_DIR=" + runtimeDir}, row.environ...)
			out := runProxyTrustChild(t, row.store, row.release, environ)
			t.Logf("the child answered: %+v", out)
			if out.Vars["HTTPS_PROXY"] == "" || out.Vars["NO_PROXY"] == "" {
				t.Errorf("the command's HTTPS_PROXY is %q and NO_PROXY %q, want both set whatever the store holds", out.Vars["HTTPS_PROXY"], out.Vars["NO_PROXY"])
			}
			grantDir := filepath.Join(runtimeDir, "agent-secrets", "airlock", "grant-one")
			if !row.wantBundle {
				if out.Bundle != "" || out.Wgetrc != "" {
					t.Errorf("prepareAirlock answered the files %q and %q, want none", out.Bundle, out.Wgetrc)
				}
				for _, key := range append(slices.Clone(airlockBundleVars), "WGETRC") {
					if value, set := out.Vars[key]; set {
						t.Errorf("the command's %s is set to %q, want it left as the command has it", key, value)
					}
				}
				if _, err := os.Stat(filepath.Join(runtimeDir, "agent-secrets")); !os.IsNotExist(err) {
					t.Errorf("the runtime directory holds agent-secrets' files (stat: %v), want nothing written", err)
				}
				return
			}
			if want := filepath.Join(grantDir, "ca-bundle.pem"); out.Bundle != want {
				t.Fatalf("the bundle is at %q, want %q", out.Bundle, want)
			}
			bundle, err := os.ReadFile(out.Bundle)
			if err != nil {
				t.Fatalf("read the bundle: %v", err)
			}
			for _, root := range []string{newRoot, oldRoot} {
				if strings.Contains(row.release, root) && !strings.Contains(string(bundle), root) {
					t.Errorf("the bundle lacks a root the release carries:\n%s", bundle)
				}
			}
			for _, key := range airlockBundleVars {
				if out.Vars[key] != out.Bundle {
					t.Errorf("the command's %s is %q, want the bundle %q", key, out.Vars[key], out.Bundle)
				}
			}
			if want := filepath.Join(grantDir, "wgetrc"); out.Vars["WGETRC"] != want {
				t.Errorf("the command's WGETRC is %q, want %q", out.Vars["WGETRC"], want)
			}
		})
	}
}
