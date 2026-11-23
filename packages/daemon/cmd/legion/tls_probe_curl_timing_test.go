package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/bootprobe"
)

// The image's curl at a vendored chain. curlPastTheVendoredChain is curl 8.18.0 on OpenSSL 3.5.5,
// which verifies a chain only after the handshake, so it fails the server's signature first
// whatever root it holds (theorem-labs/agent-infra job 137340412198, log lines 7414 and 7415); curl
// 8.14.1 gives the same at a vendored chain with its CA-published root. curlRefusingAnUnknownIssuer
// is curl 8.14.1 on OpenSSL 3.5.7, which verifies a chain during the handshake, refusing a chain
// its root does not issue, captured on 2026-11-22 in the INFRA-5350 pod's worker image.
const (
	curlPastTheVendoredChain    = "curl: (35) TLS connect error: error:030000EA:digital envelope routines::provider signature failure"
	curlRefusingAnUnknownIssuer = "curl: (60) SSL certificate problem: unable to get local issuer certificate More details here: https://curl.se/docs/sslcerts.html  curl failed to verify the legitimacy of the server and therefore could not establish a secure connection to it. To learn more about this situation and how to fix it, please visit the webpage mentioned above."
)

// A curl that fails the server's signature at each vendored chain whatever root it gets, as curl
// 8.18.0 on OpenSSL does (theorem-labs/agent-infra job 137340412198), verifies a chain only after
// the handshake: no vendored chain can show it refusing one, since the probe cannot make the
// server's signature. The probe holds that curl to the public sites in place of the vendored
// chains, says why on its own line, and passes. On main 64887452 the probe held that curl to the
// vendored chains, as it holds every client outside its fixed after-the-handshake set, and failed
// with "curl accepts the vendored emSign chain". The test claims nothing of the vendored chains'
// own lines, which read differently once their leaves end on 2027-05-03, and nothing that depends
// on the day it runs.
func TestProbeImageHoldsACurlThatVerifiesAfterTheHandshakeToThePublicSites(t *testing.T) {
	// curl 8.18.0 on OpenSSL 3.5.5, the curl of curlPastTheVendoredChain, refusing a fixture chain
	// (theorem-labs/agent-infra job 137340412198, log lines 7411 and 7412).
	const refusesAFixture = "curl: (60) SSL certificate OpenSSL verify result: unable to get local issuer certificate (20)"
	omp := imageOmp(t)
	img := inImage(t, thisBinarysContract, omp)
	// The image's curl, run as the positive controls run it: past each vendored chain to the
	// server's signature, whatever root it holds, and refused at the control, whose chain no root
	// but the control's own issues.
	img.install(t, "curl", "#!/bin/sh\ncase \"$*\" in\n"+
		"*'--capath /nonexistent'*"+tlsControlHost+"*) echo '"+curlRefusingAnUnknownIssuer+"' >&2; exit 60 ;;\n"+
		"*'--capath /nonexistent'*) echo '"+curlPastTheVendoredChain+"' >&2; exit 35 ;;\n"+
		"esac\n")
	previousClients, previousControls, previousPublic := newImageTLSClients, newTLSPositiveControls, newPublicTLSSites
	t.Cleanup(func() {
		newImageTLSClients, newTLSPositiveControls, newPublicTLSSites = previousClients, previousControls, previousPublic
	})
	refused := tlsOutcome{status: tlsCertificateRefused, detail: refusesAFixture}
	pastTheServersSignature := tlsOutcome{status: tlsNetworkUnavailable, signatureFailed: true, detail: "exit status 35: " + curlPastTheVendoredChain}
	newImageTLSClients = theseTLSClients(fakeTLSClient{clientName: "curl", outcomes: map[string]tlsOutcome{
		"emSign":          refused,
		"AAA":             refused,
		"control":         {status: tlsConnectionAccepted, detail: "HTTP 200"},
		"vendored emSign": pastTheServersSignature,
		"vendored AAA":    pastTheServersSignature,
		"public emSign":   refused,
		"public AAA":      refused,
	}})
	newTLSPositiveControls, newPublicTLSSites = passingTLSPositiveControls, unaskedPublicTLSSites

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), append([]string{"legion", "probe-image"}, img.flags()...), &stdout, &stderr)

	if code != 0 || !strings.Contains(stdout.String(), bootprobe.OKPrefix) {
		t.Fatalf("probe-image exited %d, want 0 and the OK line: stderr %q, stdout %q", code, stderr.String(), stdout.String())
	}
	if strings.Contains(stdout.String(), "probe-image: TLS client curl vendored") {
		t.Errorf("stdout = %q, want curl held to no vendored chain; stderr %q", stdout.String(), stderr.String())
	}
	for _, host := range []string{tlsEmSignHost, tlsAAAHost} {
		if line := "probe-image: UNKNOWN: curl against " + host + ": not asked: the test asks no public site"; !strings.Contains(stdout.String(), line+"\n") {
			t.Errorf("stdout = %q, want curl held to the public site, the line %q; stderr %q", stdout.String(), line, stderr.String())
		}
	}
	if want := "probe-image: TLS curl verifies a chain after the handshake, so the probe holds it to the public sites: vendored emSign: exit status 35: curl: (35) TLS connect error"; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want the line beginning %q; stderr %q", stdout.String(), want, stderr.String())
	}
}
