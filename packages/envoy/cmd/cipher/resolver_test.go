// packages/envoy/cmd/cipher/resolver_test.go
//
// The test's resolver: a row owns the answer for each host name its requests look up, so no row
// takes its result from the machine's resolver. useResolver sets it in this process, and
// runResolved runs agent-secrets as a child process with it.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"testing"
)

// noSuchHost is the answer of the test's resolver for a name that has no address: the dial fails
// as the dialer fails when its lookup finds no such host, and nothing asks the network.
const noSuchHost = ""

// resolvedHostsVar holds, as JSON, the hosts that a child process of runResolved looks up its
// names in (TestAgentSecretsResolvedHelper).
const resolvedHostsVar = "AGENT_SECRETS_TEST_RESOLVED_HOSTS"

// useResolver makes hosts the resolver of every request that this process sends through
// http.DefaultTransport for the rest of t. Every broker call goes through it, because the client
// of newClient sets no transport of its own. A name in hosts dials the address that it maps to,
// or fails as a name with no such host where it maps to noSuchHost. A name that is not in hosts
// fails with an error that names it, and an IP address dials as it is. No name goes to the
// machine's resolver, and no request goes through a proxy that the environment names, because the
// proxy would look up the name in place of hosts.
func useResolver(t *testing.T, hosts map[string]string) {
	t.Helper()
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatalf("http.DefaultTransport is a %T, not the *http.Transport that useResolver dials through", http.DefaultTransport)
	}
	own := base.Clone()
	dial := own.DialContext
	own.Proxy = nil
	own.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if _, err := netip.ParseAddr(host); err == nil {
			return dial(ctx, network, address)
		}
		to, owned := hosts[host]
		switch {
		case !owned:
			return nil, fmt.Errorf("the test's resolver has no answer for %s", host)
		case to == noSuchHost:
			return nil, &net.OpError{Op: "dial", Net: network, Err: &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}}
		}
		return dial(ctx, network, net.JoinHostPort(to, port))
	}
	http.DefaultTransport = own
	t.Cleanup(func() {
		http.DefaultTransport = base
		own.CloseIdleConnections()
	})
}

// runResolved is runAgentSecrets with the test's resolver: it runs this test binary as
// agent-secrets (TestAgentSecretsResolvedHelper), which looks up each host name in hosts
// (useResolver) and never in the machine's resolver.
func runResolved(t *testing.T, hosts map[string]string, broker, keyDir string, extraEnv []string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(hosts)
	if err != nil {
		t.Fatal(err)
	}
	env := append([]string{resolvedHostsVar + "=" + string(encoded)}, extraEnv...)
	return runAgentSecrets(t, self, broker, keyDir, env, append([]string{"-test.run=^TestAgentSecretsResolvedHelper$", "--"}, args...)...)
}

// TestAgentSecretsResolvedHelper is agent-secrets in a child process of runResolved: it runs run
// on the arguments after "--", with the hosts in resolvedHostsVar as its resolver (useResolver),
// and exits with the code of run. The exec form replaces this process with the command, as it
// replaces agent-secrets.
func TestAgentSecretsResolvedHelper(t *testing.T) {
	encoded := os.Getenv(resolvedHostsVar)
	if encoded == "" {
		t.Skip("subprocess helper")
	}
	var hosts map[string]string
	if err := json.Unmarshal([]byte(encoded), &hosts); err != nil {
		t.Fatalf("%s holds no hosts: %v", resolvedHostsVar, err)
	}
	useResolver(t, hosts)
	os.Exit(run(flag.Args(), os.Stdout, os.Stderr))
}
