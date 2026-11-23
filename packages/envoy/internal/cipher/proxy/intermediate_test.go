// packages/envoy/internal/cipher/proxy/intermediate_test.go
// The intermediate a broker pod asks Certificate Authority Service for: the request, the
// answer it takes and the ones it refuses, its renewal with 2 days left, its retries, the log
// line of each intermediate, and readiness while it holds none. The service is the stand-in
// the local stack runs (proxyrulestest.CAStand), and the clock is a row's own.
package proxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/sjawhar/envoy/internal/cipher/proxyrules/proxyrulestest"
	"github.com/sjawhar/envoy/internal/logtest"
	"github.com/sjawhar/envoy/internal/testwait"
)

const testPoolName = "projects/example-project/locations/us-west1/caPools/proxy"

// podCredential is what the rig's pod presents as its own Google credential: a string of its
// own, which opens nothing.
const podCredential = "the-pods-own-credential"

// poolRig is a pool authority against a CA stand, on a clock the row moves, logging into a
// buffer the row reads.
type poolRig struct {
	stand     *proxyrulestest.CAStand
	service   *proxyrulestest.Service
	clock     *steppedClock
	authority *Authority
	log       *syncBuffer
	pool      Pool
}

// syncBuffer is the log every rig writes, read by message.
type syncBuffer struct{ logtest.Buffer }

// lines is every logged line whose message is message, each as its fields.
func (b *syncBuffer) lines(message string) []map[string]any {
	var out []map[string]any
	for line := range strings.SplitSeq(b.String(), "\n") {
		var fields map[string]any
		if json.Unmarshal([]byte(line), &fields) == nil && fields["msg"] == message {
			out = append(out, fields)
		}
	}
	return out
}

func newPoolRig(t *testing.T, hosts []string, policy []string) *poolRig {
	t.Helper()
	now := time.Now().Truncate(time.Second)
	clock := &steppedClock{now: now}
	stand, err := proxyrulestest.NewCAStand(proxyrulestest.CAOptions{Names: policy, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	service, err := proxyrulestest.NewService(proxyrulestest.ServiceOptions{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	service.AnswerWith(stand.Answer)
	log := &syncBuffer{}
	authority := NewPoolAuthority(AuthorityOptions{Clock: clock, Hosts: hosts, Log: slog.New(slog.NewJSONHandler(log, nil))})
	return &poolRig{
		stand: stand, service: service, clock: clock, authority: authority, log: log,
		pool: Pool{Name: testPoolName, Endpoint: service.URL(), Token: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: podCredential})},
	}
}

// run starts the authority's intermediates on the rig's pool with renewBefore, until the row ends.
func (r *poolRig) run(t *testing.T, renewBefore time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.authority.Intermediates(ctx, r.pool, renewBefore)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// awaitIssued waits for the stand to have issued n certificates and the authority to sign
// with the last of them.
func (r *poolRig) awaitIssued(t *testing.T, n int) *x509.Certificate {
	t.Helper()
	var last *x509.Certificate
	testwait.Until(t, fmt.Sprintf("the stand to issue %d intermediates and the authority to sign with the last", n), func() (string, bool) {
		issued := r.stand.Issued()
		if len(issued) < n {
			return fmt.Sprintf("%d issued", len(issued)), false
		}
		last = issued[len(issued)-1]
		current := r.authority.Certificate()
		return r.log.String(), current != nil && current.Equal(last)
	})
	return last
}

// awaitSleeps waits for the loop to have called SleepUntil n times, and answers the last target.
func (r *poolRig) awaitSleeps(t *testing.T, n int) time.Time {
	t.Helper()
	var target time.Time
	testwait.Until(t, fmt.Sprintf("the intermediates loop to sleep %d times", n), func() (string, bool) {
		sleeps := r.clock.sleeps()
		if len(sleeps) < n {
			return fmt.Sprintf("%d sleeps: %v\n%s", len(sleeps), sleeps, r.log.String()), false
		}
		target = sleeps[n-1]
		return "", true
	})
	return target
}

func (r *poolRig) certificateRequests() []proxyrulestest.Request {
	var out []proxyrulestest.Request
	for _, seen := range r.service.Requests() {
		if strings.HasSuffix(seen.Path, "/certificates") {
			out = append(out, seen)
		}
	}
	return out
}

// TestAPoolAuthorityAsksForAnIntermediateAndSignsWithIt: the pod sends one CSR for a key of its
// own, with its token, asking for 7 days as an authority of path length 0 for the names of the
// bound hosts; it signs every bound host's leaf under the answer, which chains to the stand's
// root; it logs one line naming the serial, the names and the end; the release carries the root;
// and it is not ready until the answer arrives.
func TestAPoolAuthorityAsksForAnIntermediateAndSignsWithIt(t *testing.T) {
	r := newPoolRig(t, []string{"s.airlock.test", "*.googleapis.com"}, nil)
	if err := r.authority.Ready(); err == nil || !strings.Contains(err.Error(), "no intermediate") {
		t.Fatalf("Ready before the first answer = %v; want not ready, naming no intermediate", err)
	}
	if _, err := r.authority.leaf("s.airlock.test"); err == nil {
		t.Fatal("the authority signed a leaf with no intermediate")
	}
	if r.authority.ReleaseRoots() != "" {
		t.Fatal("the release carries a root before the pod knows its chain")
	}
	r.run(t, IntermediateRenewBefore)
	intermediate := r.awaitIssued(t, 1)

	requests := r.certificateRequests()
	if len(requests) != 1 {
		t.Fatalf("the pod sent %d certificate requests; want one", len(requests))
	}
	seen := requests[0]
	if seen.Method != http.MethodPost || seen.Path != "/v1/"+testPoolName+"/certificates" {
		t.Errorf("the request is %s %s; want POST /v1/<pool>/certificates", seen.Method, seen.Path)
	}
	if got := seen.Header.Get("Authorization"); got != "Bearer "+podCredential {
		t.Errorf("the request's Authorization is %q; want the pod's own token", got)
	}
	if !regexp.MustCompile(`(^|&)certificateId=[a-zA-Z0-9_-]{1,63}(&|$)`).MatchString(seen.Query) ||
		!regexp.MustCompile(`(^|&)requestId=[0-9a-f-]{36}(&|$)`).MatchString(seen.Query) {
		t.Errorf("the request's query is %q; want a certificateId and a requestId", seen.Query)
	}
	var body struct {
		PEMCSR   string `json:"pemCsr"`
		Lifetime string `json:"lifetime"`
	}
	if err := json.Unmarshal([]byte(seen.Body), &body); err != nil {
		t.Fatal(err)
	}
	if body.Lifetime != "604800s" {
		t.Errorf("the request asks for %q; want 7 days, 604800s", body.Lifetime)
	}
	block, _ := pem.Decode([]byte(body.PEMCSR))
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !csr.PublicKey.(*ecdsa.PublicKey).Equal(intermediate.PublicKey) || csr.PublicKeyAlgorithm != x509.ECDSA {
		t.Error("the CSR is not for the key the answer certified")
	}
	// The stand issued the request's names, since its policy names none: what the pod asked for.
	if !slices.Equal(intermediate.PermittedDNSDomains, []string{"googleapis.com", "s.airlock.test"}) {
		t.Errorf("the pod asked for %v; want each bound host, *.googleapis.com as googleapis.com", intermediate.PermittedDNSDomains)
	}
	if !intermediate.IsCA || !intermediate.MaxPathLenZero {
		t.Error("the CSR did not ask for an authority of path length 0")
	}

	verifyLeaf(t, r.authority, "s.airlock.test", r.stand.Root().Certificate(), r.clock.Now())
	verifyLeaf(t, r.authority, "storage.googleapis.com", r.stand.Root().Certificate(), r.clock.Now())
	if err := r.authority.Ready(); err != nil {
		t.Errorf("Ready with an intermediate: %v", err)
	}
	if want := string(r.stand.Root().CertificatePEM()); r.authority.ReleaseRoots() != want {
		t.Errorf("the release carries %q; want the stand's root", r.authority.ReleaseRoots())
	}
	lines := r.log.lines("proxy intermediate")
	if len(lines) != 1 {
		t.Fatalf("the pod logged %d intermediate lines; want one:\n%s", len(lines), r.log.String())
	}
	line := lines[0]
	if line["level"] != "INFO" || line["serial"] != fmt.Sprintf("%x", intermediate.SerialNumber) ||
		line["names"] != "googleapis.com s.airlock.test" || line["not_after"] != intermediate.NotAfter.UTC().Format(time.RFC3339) {
		t.Errorf("the intermediate's line is %v; want INFO with its serial %x, its names and its end %s", line, intermediate.SerialNumber, intermediate.NotAfter.UTC().Format(time.RFC3339))
	}
}

// TestNoLeafForAnAddressVerifiesUnderThePodsIntermediate: a leaked pod key signs no leaf a
// client trusts for an IP address. The pod's CSR asks for every IPv4 and IPv6 address excluded,
// the stand issues it as a pool's policy does, and a leaf the pod's own intermediate key signs
// for 10.0.0.5, 169.254.169.254 or 2001:db8::5 fails verification against the root. Controls: a
// leaf for a bound host verifies, and one for another DNS name is refused by the permitted names.
// A URI or email name is no TLS server identity: a leaf carrying one is valid for no host.
func TestNoLeafForAnAddressVerifiesUnderThePodsIntermediate(t *testing.T) {
	r := newPoolRig(t, []string{"s.airlock.test"}, nil)
	r.run(t, IntermediateRenewBefore)
	intermediate := r.awaitIssued(t, 1)

	var body struct {
		PEMCSR string `json:"pemCsr"`
	}
	if err := json.Unmarshal([]byte(r.certificateRequests()[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(body.PEMCSR))
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	asked := askedConstraints(t, csr)
	if got := fmt.Sprint(asked.ExcludedIPRanges); got != "[0.0.0.0/0 ::/0]" || !asked.PermittedDNSDomainsCritical {
		t.Errorf("the CSR asks to exclude %s (critical %v); want [0.0.0.0/0 ::/0], critical", got, asked.PermittedDNSDomainsCritical)
	}

	r.authority.mu.Lock()
	key := r.authority.signer.key
	r.authority.mu.Unlock()
	roots, intermediates := x509.NewCertPool(), x509.NewCertPool()
	roots.AddCert(r.stand.Root().Certificate())
	intermediates.AddCert(intermediate)
	verify := func(shape func(*x509.Certificate), host string) error {
		t.Helper()
		leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{
			SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "probe"},
			NotBefore: r.clock.Now().Add(-time.Minute), NotAfter: r.clock.Now().Add(time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		shape(template)
		der, err := x509.CreateCertificate(rand.Reader, template, intermediate, &leafKey.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		_, err = leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: intermediates, CurrentTime: r.clock.Now()})
		return err
	}
	if err := verify(func(c *x509.Certificate) { c.DNSNames = []string{"s.airlock.test"} }, "s.airlock.test"); err != nil {
		t.Fatalf("control: a leaf for the bound host = %v; want it trusted", err)
	}
	if err := verify(func(c *x509.Certificate) { c.DNSNames = []string{"evil.example"} }, "evil.example"); err == nil || !strings.Contains(err.Error(), "not permitted") {
		t.Errorf("control: a leaf for evil.example = %v; want it refused by the permitted names", err)
	}
	for _, address := range []string{"10.0.0.5", "169.254.169.254", "2001:db8::5"} {
		ip := net.ParseIP(address)
		err := verify(func(c *x509.Certificate) { c.IPAddresses = []net.IP{ip} }, address)
		if err == nil || !strings.Contains(err.Error(), "excluded") {
			t.Errorf("a leaf the pod's key signs for %s = %v; want it refused, the address excluded", address, err)
		}
	}
	uri, err := url.Parse("spiffe://evil.example/workload")
	if err != nil {
		t.Fatal(err)
	}
	for name, shape := range map[string]func(*x509.Certificate){
		"a URI name":    func(c *x509.Certificate) { c.URIs = []*url.URL{uri} },
		"an email name": func(c *x509.Certificate) { c.EmailAddresses = []string{"ops@evil.example"} },
	} {
		if err := verify(shape, "evil.example"); err == nil {
			t.Errorf("a leaf with only %s is valid for evil.example; want it valid for no host", name)
		}
	}
}

// askedConstraints is the name constraints csr asks for, read off a throwaway certificate that
// carries the requested extension, since the standard library reads them out of a certificate and
// not out of a request.
func askedConstraints(t *testing.T, csr *x509.CertificateRequest) *x509.Certificate {
	t.Helper()
	for _, extension := range csr.Extensions {
		if !extension.Id.Equal(oidNameConstraints) {
			continue
		}
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Minute), ExtraExtensions: []pkix.Extension{extension}}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return cert
	}
	t.Fatal("the CSR asks for no name constraints")
	return nil
}

// TestThePodRenewsWithTwoDaysLeft: the loop sleeps until 2 days before the intermediate's end;
// at 5 days and 1 minute it has a new intermediate and signs every leaf under it; the new one
// gets its own line.
func TestThePodRenewsWithTwoDaysLeft(t *testing.T) {
	r := newPoolRig(t, []string{"s.airlock.test"}, []string{"s.airlock.test"})
	r.run(t, IntermediateRenewBefore)
	first := r.awaitIssued(t, 1)
	if target := r.awaitSleeps(t, 1); !target.Equal(first.NotAfter.Add(-2 * 24 * time.Hour)) {
		t.Fatalf("the loop sleeps until %v; want 2 days before the intermediate's end %v", target, first.NotAfter)
	}
	r.clock.set(first.NotBefore.Add(4 * 24 * time.Hour))
	if got := len(r.stand.Issued()); got != 1 {
		t.Fatalf("at 4 days the stand has issued %d intermediates; want the first alone", got)
	}
	r.clock.set(first.NotBefore.Add(5*24*time.Hour + time.Minute))
	second := r.awaitIssued(t, 2)
	if second.Equal(first) {
		t.Fatal("the renewal answered the first intermediate")
	}
	leaf := verifyLeaf(t, r.authority, "s.airlock.test", r.stand.Root().Certificate(), r.clock.Now())
	if err := leaf.CheckSignatureFrom(second); err != nil {
		t.Errorf("the leaf after the renewal is not signed by the new intermediate: %v", err)
	}
	if lines := r.log.lines("proxy intermediate"); len(lines) != 2 {
		t.Errorf("the pod logged %d intermediate lines; want one per intermediate, 2", len(lines))
	}
}

// TestWithNoRenewalMarginThePodKeepsItsFirstIntermediate is the negative control: with the margin
// at 0 the loop sleeps until the end, so at 5 days and 1 minute it still signs with the first.
func TestWithNoRenewalMarginThePodKeepsItsFirstIntermediate(t *testing.T) {
	r := newPoolRig(t, []string{"s.airlock.test"}, []string{"s.airlock.test"})
	r.run(t, 0)
	first := r.awaitIssued(t, 1)
	if target := r.awaitSleeps(t, 1); !target.Equal(first.NotAfter) {
		t.Fatalf("with the margin at 0 the loop sleeps until %v; want the intermediate's end %v", target, first.NotAfter)
	}
	r.clock.set(first.NotBefore.Add(5*24*time.Hour + time.Minute))
	leaf := verifyLeaf(t, r.authority, "s.airlock.test", r.stand.Root().Certificate(), r.clock.Now())
	if err := leaf.CheckSignatureFrom(first); err != nil {
		t.Errorf("at 5 days and 1 minute with the margin at 0 the leaf is not the first intermediate's: %v", err)
	}
	if got := len(r.stand.Issued()); got != 1 {
		t.Errorf("the stand issued %d intermediates; want the first alone", got)
	}
}

// TestThePodRetriesWithBackoffUntilItGetsOne: while the service fails, the pod waits 1 second,
// then twice as long each time up to 5 minutes, and logs each failure; it gets no intermediate
// and its leaves end no later than the one it holds; when the service answers it has a new one.
func TestThePodRetriesWithBackoffUntilItGetsOne(t *testing.T) {
	r := newPoolRig(t, []string{"s.airlock.test"}, []string{"s.airlock.test"})
	r.run(t, IntermediateRenewBefore)
	first := r.awaitIssued(t, 1)
	r.awaitSleeps(t, 1)
	r.stand.Fail(true)
	// Past the renewal's start: each failure sleeps, and each sleep is let out at once.
	r.clock.set(first.NotBefore.Add(6*24*time.Hour + 12*time.Hour))
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second,
		64 * time.Second, 128 * time.Second, 256 * time.Second, 5 * time.Minute, 5 * time.Minute}
	for i, wait := range want {
		at := r.clock.Now()
		if target := r.awaitSleeps(t, 2+i); !target.Equal(at.Add(wait)) {
			t.Fatalf("retry %d sleeps until %v; want %v after the failure, at %v", i+1, target, wait, at.Add(wait))
		}
		r.clock.set(at.Add(wait))
	}
	// The failure after the last wait above, and the loop asleep again behind it, before the
	// stand answers: a clock moved while that request was still out would be moved under it.
	next := r.awaitSleeps(t, 2+len(want))
	if got := len(r.stand.Issued()); got != 1 {
		t.Fatalf("the stand issued %d intermediates while failing; want none past the first", got)
	}
	if failures := r.log.lines("proxy intermediate request failed"); len(failures) < len(want) {
		t.Errorf("the pod logged %d failed requests; want one per attempt, %d at least", len(failures), len(want))
	}
	leaf := verifyLeaf(t, r.authority, "s.airlock.test", r.stand.Root().Certificate(), r.clock.Now())
	if leaf.NotAfter.After(first.NotAfter) {
		t.Errorf("the leaf while the service fails ends %v; want no later than the intermediate's end %v", leaf.NotAfter, first.NotAfter)
	}
	r.stand.Fail(false)
	r.clock.set(next)
	r.awaitIssued(t, 2)
}

// TestThePodRefusesAnAnswerItCannotSignWith: an answer whose names leave out a bound host, as a
// pool whose policy was not applied before the roll answers, is refused, so the pod is not ready
// and logs which host; once the policy names it the next attempt is taken. An answer under
// another root than the first one's is refused too, since every release the pod has made names
// that root.
func TestThePodRefusesAnAnswerItCannotSignWith(t *testing.T) {
	r := newPoolRig(t, []string{"s.airlock.test", "u.airlock.test"}, []string{"s.airlock.test"})
	r.run(t, IntermediateRenewBefore)
	testwait.Until(t, "a refused answer to be logged", func() (string, bool) {
		return r.log.String(), len(r.log.lines("proxy intermediate request failed")) > 0
	})
	failure := r.log.lines("proxy intermediate request failed")[0]
	if level, why := failure["level"], fmt.Sprint(failure["error"]); level != "WARN" || !strings.Contains(why, "u.airlock.test") {
		t.Errorf("the refusal is logged %v; want WARN naming the host the answer leaves out", failure)
	}
	if err := r.authority.Ready(); err == nil {
		t.Fatal("Ready after a refused answer = nil; want not ready")
	}
	r.stand.SetNames([]string{"s.airlock.test", "u.airlock.test"})
	// The retry's own moment, once the loop is asleep until it.
	r.clock.set(r.awaitSleeps(t, 1))
	first := r.awaitIssued(t, 2)
	verifyLeaf(t, r.authority, "u.airlock.test", r.stand.Root().Certificate(), r.clock.Now())

	// Another root: a new stand on the same address, its own root.
	other, err := proxyrulestest.NewCAStand(proxyrulestest.CAOptions{Names: []string{"s.airlock.test", "u.airlock.test"}, Now: r.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	r.service.AnswerWith(other.Answer)
	before := len(r.log.lines("proxy intermediate request failed"))
	r.clock.set(first.NotAfter.Add(-time.Hour))
	testwait.Until(t, "the answer under another root to be refused", func() (string, bool) {
		failures := r.log.lines("proxy intermediate request failed")
		return r.log.String(), len(failures) > before && strings.Contains(fmt.Sprint(failures[len(failures)-1]["error"]), "another root")
	})
	if !r.authority.Certificate().Equal(first) {
		t.Error("the pod took an intermediate under another root")
	}
}
