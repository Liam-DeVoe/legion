// packages/envoy/internal/cipher/proxy/intermediate_bounds_test.go
// The bounds a pod holds the pool's answer to, whatever the pool's issuance policy answers: an
// intermediate of 7 days at most, of path length 0, whose name constraints permit the names of
// the profiles file's hosts and no other. An answer past any of them is refused like any other
// refused answer: logged WARN with the reason, not taken, and the pod not ready on it.
package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/testwait"
)

// boundsAnswer is the shape of an intermediate a row gives the pod: a stand-in pool's answer to
// each request (the CSR's key certified by the row's root), or a mounted intermediate.
type boundsAnswer struct {
	pathLen      int // MaxPathLen; 0 is path length 0, -1 is no constraint
	lifetime     time.Duration
	names        []string // the name constraints; nil is none
	noConstraint bool
}

// boundsTemplate is answer's certificate, starting at notBefore.
func boundsTemplate(answer boundsAnswer, notBefore time.Time) *x509.Certificate {
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "bounds intermediate"},
		NotBefore: notBefore, NotAfter: notBefore.Add(answer.lifetime),
		BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	if answer.pathLen == 0 {
		template.MaxPathLenZero = true
	} else {
		template.MaxPathLen = answer.pathLen
	}
	if !answer.noConstraint {
		template.PermittedDNSDomains, template.PermittedDNSDomainsCritical = answer.names, true
	}
	return template
}

func boundsPool(t *testing.T, root testCertificate, now func() time.Time, answer boundsAnswer) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PEMCSR string `json:"pemCsr"`
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		block, _ := pem.Decode([]byte(body.PEMCSR))
		if block == nil {
			http.Error(w, "no CSR", http.StatusBadRequest)
			return
		}
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		template := boundsTemplate(answer, now())
		der, err := x509.CreateCertificate(rand.Reader, template, root.cert, csr.PublicKey, root.key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"pemCertificate":      string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
			"pemCertificateChain": []string{string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.cert.Raw}))},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

// TestThePodRefusesAnAnswerPastThePoolsBounds: an answer of path length 1, of no path length
// constraint, of 30 days, with a name the profiles file does not bind, or with no name
// constraints at all is refused: a WARN line naming why, no intermediate taken or logged, and the
// pod not ready. Control: the same stand-in answering 7 days, path length 0 and the hosts' names
// is taken.
func TestThePodRefusesAnAnswerPastThePoolsBounds(t *testing.T) {
	hosts := []string{"s.airlock.test", "*.googleapis.com"}
	policy := []string{"googleapis.com", "s.airlock.test"}
	week := 7 * 24 * time.Hour
	for _, row := range []struct {
		name   string
		answer boundsAnswer
		taken  bool
		why    string // a word the refusal's reason holds, lower case
	}{
		{"the policy's own answer (control)", boundsAnswer{pathLen: 0, lifetime: week, names: policy}, true, ""},
		{"path length 1", boundsAnswer{pathLen: 1, lifetime: week, names: policy}, false, "path length"},
		{"no path length constraint", boundsAnswer{pathLen: -1, lifetime: week, names: policy}, false, "path length"},
		{"30 days", boundsAnswer{pathLen: 0, lifetime: 30 * 24 * time.Hour, names: policy}, false, "life"},
		{"a name the profiles file does not bind", boundsAnswer{pathLen: 0, lifetime: week, names: append([]string{"evil.example"}, policy...)}, false, "name"},
		{"no name constraints", boundsAnswer{pathLen: 0, lifetime: week, noConstraint: true}, false, "name"},
	} {
		t.Run(row.name, func(t *testing.T) {
			now := time.Now().Truncate(time.Second)
			clock := &steppedClock{now: now}
			root := testRoot(t, "bounds root")
			server := boundsPool(t, root, clock.Now, row.answer)
			log := &syncBuffer{}
			authority := NewPoolAuthority(AuthorityOptions{Clock: clock, Hosts: hosts, Log: slog.New(slog.NewJSONHandler(log, nil))})
			r := &poolRig{clock: clock, authority: authority, log: log, pool: Pool{Name: testPoolName, Endpoint: server.URL}}
			r.run(t, IntermediateRenewBefore)
			testwait.Until(t, "the first answer to be taken or refused", func() (string, bool) {
				return log.String(), authority.Certificate() != nil || len(log.lines("proxy intermediate request failed")) > 0
			})
			if row.taken {
				if authority.Certificate() == nil || authority.Ready() != nil {
					t.Fatalf("the control answer was not taken (Ready: %v); log:\n%s", authority.Ready(), log.String())
				}
				verifyLeaf(t, authority, "s.airlock.test", root.cert, clock.Now())
				return
			}
			if got := authority.Certificate(); got != nil {
				t.Fatalf("the pod took the answer (%s): serial %x, %s to %s, max path length %d (zero %v), names %v; want it refused",
					row.name, got.SerialNumber, got.NotBefore.UTC().Format(time.RFC3339), got.NotAfter.UTC().Format(time.RFC3339),
					got.MaxPathLen, got.MaxPathLenZero, got.PermittedDNSDomains)
			}
			if lines := log.lines("proxy intermediate"); len(lines) > 0 {
				t.Errorf("an answer past the bounds was logged as an intermediate: %v", lines)
			}
			if err := authority.Ready(); err == nil {
				t.Error("Ready after the refused answer = nil; want not ready")
			}
			failure := log.lines("proxy intermediate request failed")[0]
			if level, why := failure["level"], strings.ToLower(fmt.Sprint(failure["error"])); level != "WARN" || !strings.Contains(why, row.why) {
				t.Errorf("the refusal is logged %v; want WARN with a reason naming %q", failure, row.why)
			}
		})
	}
}

// TestAMountedIntermediatePastTheBoundsIsRefused: the mounted form is held to the same bounds as
// the pool's answer. A mounted intermediate (BROKER_AIRLOCK_CA_CERT_FILE with its chain to its
// root) of path length 1, of no path length constraint, of 30 days, with a name the profiles
// file does not bind, or with no name constraints refuses the start, naming why. Control: one of
// 7 days, path length 0 and the hosts' names starts. Today's self-signed root is not an
// intermediate and still starts (TestTodaysSelfSignedRootStillSigns).
func TestAMountedIntermediatePastTheBoundsIsRefused(t *testing.T) {
	hosts := []string{"s.airlock.test", "*.googleapis.com"}
	policy := []string{"googleapis.com", "s.airlock.test"}
	week := 7 * 24 * time.Hour
	for _, row := range []struct {
		name   string
		answer boundsAnswer
		starts bool
		why    string // a word the refusal holds, lower case
	}{
		{"7 days, path length 0, the hosts' names (control)", boundsAnswer{pathLen: 0, lifetime: week, names: policy}, true, ""},
		{"path length 1", boundsAnswer{pathLen: 1, lifetime: week, names: policy}, false, "path length"},
		{"no path length constraint", boundsAnswer{pathLen: -1, lifetime: week, names: policy}, false, "path length"},
		{"30 days", boundsAnswer{pathLen: 0, lifetime: 30 * 24 * time.Hour, names: policy}, false, "life"},
		{"a name the profiles file does not bind", boundsAnswer{pathLen: 0, lifetime: week, names: append([]string{"evil.example"}, policy...)}, false, "name"},
		{"no name constraints", boundsAnswer{pathLen: 0, lifetime: week, noConstraint: true}, false, "name"},
	} {
		t.Run(row.name, func(t *testing.T) {
			root := testRoot(t, "mounted bounds root")
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			der, err := x509.CreateCertificate(rand.Reader, boundsTemplate(row.answer, time.Now().Add(-time.Hour)), root.cert, &key.PublicKey, root.key)
			if err != nil {
				t.Fatal(err)
			}
			cert, err := x509.ParseCertificate(der)
			if err != nil {
				t.Fatal(err)
			}
			certFile, keyFile := writeChain(t, testCertificate{cert, key}, root.cert)
			authority, err := NewAuthority(certFile, keyFile, AuthorityOptions{Hosts: hosts})
			if row.starts {
				if err != nil {
					t.Fatalf("the control intermediate was refused: %v", err)
				}
				verifyLeaf(t, authority, "s.airlock.test", root.cert, time.Now())
				return
			}
			if err == nil {
				t.Fatalf("a mounted intermediate (%s) was taken: %s to %s, max path length %d (zero %v), names %v; want the start refused",
					row.name, cert.NotBefore.UTC().Format(time.RFC3339), cert.NotAfter.UTC().Format(time.RFC3339),
					cert.MaxPathLen, cert.MaxPathLenZero, cert.PermittedDNSDomains)
			}
			if !strings.Contains(strings.ToLower(err.Error()), row.why) {
				t.Errorf("the refusal reads %q; want it to name %q", err, row.why)
			}
		})
	}
}
