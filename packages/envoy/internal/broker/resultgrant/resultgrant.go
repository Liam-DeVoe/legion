// Package resultgrant holds short-lived CI capabilities in the broker, never in the child.
// A grant and its DPoP key belong to the issuing pod. Losing that pod ends the grant rather
// than silently changing the identity behind a write's idempotency key.
package resultgrant

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
	"github.com/sjawhar/envoy/brokeraddr"
	"github.com/sjawhar/envoy/internal/broker/airlock"
	"github.com/sjawhar/envoy/internal/broker/airlockproxy"
	"github.com/sjawhar/envoy/internal/broker/oauthwire"
	"github.com/sjawhar/envoy/internal/broker/requests"
	"github.com/sjawhar/envoy/internal/brokerclient"
)

const Name = "DISPATCH_RESULTS_TOKEN"
const grantPrefix = "ci-result-"

var ErrNotResults = errors.New("the broker did not mint a results-only credential for this workflow run")

// Release is the domain answer. The API uses its ordinary airlock wire conversion.
// Neither an access token nor a signing key is serializable here.
type Release struct {
	GrantID   string                  `json:"grant_id"`
	ExpiresAt time.Time               `json:"expires_at"`
	Airlock   requests.AirlockRelease `json:"airlock"`
}

type Options struct {
	BrokerURL               brokeraddr.Address
	ProxyURL, CACertificate string
	Rules                   airlock.Rules
	Fallback                airlockproxy.GrantSource
	Introspect              func(context.Context, string) (oauthwire.Introspection, error)
	HTTP                    *http.Client
	Now                     func() time.Time
}

type held struct {
	id, placeholder, token, session string
	expires                         time.Time
	key                             *ecdsa.PrivateKey
	transport                       http.RoundTripper
}
type Manager struct {
	options       Options
	mu            sync.Mutex
	byID          map[string]*held
	byPlaceholder map[string]*held
	hosts         []string
	nextPrune     time.Time
}

func New(options Options) (*Manager, error) {
	rule := options.Rules.Bound(Name)
	if rule == nil || rule.Kind != airlock.KindDPoP || options.ProxyURL == "" || options.CACertificate == "" || options.Introspect == nil {
		return nil, errors.New("results grants require the DISPATCH_RESULTS_TOKEN dpop airlock rule, proxy, authority and token reader")
	}
	if len(rule.HeaderNames()) != 1 || !strings.EqualFold(rule.HeaderNames()[0], "authorization") {
		return nil, errors.New("the results grant must carry its placeholder only in Authorization")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Manager{options: options, byID: map[string]*held{}, byPlaceholder: map[string]*held{}, hosts: append([]string(nil), rule.Hosts...)}, nil
}

type jobIdentity string

func (j jobIdentity) Token(context.Context) (string, string, error) {
	return string(j), oauthwire.SubjectTokenTypeGitHub, nil
}
func (jobIdentity) Describe() string { return "the workflow run presented for this results grant" }

// Create trades the signed job identity under a key made here and checks the broker's own
// introspection before releasing a placeholder. The OAuth exchange, not request parameters,
// selects the project, person, run and scope. No launcher credential or enrollment is made.
func (m *Manager) Create(ctx context.Context, subject string) (Release, error) {
	if strings.TrimSpace(subject) == "" {
		return Release{}, ErrNotResults
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Release{}, err
	}
	client, err := brokerclient.New(brokerclient.Config{BrokerURL: m.options.BrokerURL, Key: key, Subject: jobIdentity(subject), NoLauncherCredential: true, HTTP: m.options.HTTP})
	if err != nil {
		return Release{}, err
	}
	token, err := client.Token(ctx)
	if err != nil {
		return Release{}, err
	}
	info, err := m.options.Introspect(ctx, token.AccessToken)
	if err != nil {
		return Release{}, err
	}
	thumb, err := (&jose.JSONWebKey{Key: &key.PublicKey}).Thumbprint(crypto.SHA256)
	if err != nil {
		return Release{}, err
	}
	now := m.options.Now()
	if !info.Active || info.ClientID != oauthwire.ExchangeClientID || info.ProjectScope != oauthwire.ProjectScopeResults || info.Scope != oauthwire.ScopeResults || len(info.Projects) != 1 || info.CI == nil || info.Session == "" || info.Thumbprint != base64.RawURLEncoding.EncodeToString(thumb) || info.ExpiresAt <= now.Unix() {
		return Release{}, ErrNotResults
	}
	expires := minTime(token.Expiry, time.Unix(info.ExpiresAt, 0), now.Add(10*time.Minute))
	var opaque [20]byte
	if _, err := rand.Read(opaque[:]); err != nil {
		return Release{}, err
	}
	placeholder := requests.PlaceholderPrefix + base32.StdEncoding.EncodeToString(opaque[:])
	grant := &held{id: grantPrefix + uuid.NewString(), placeholder: placeholder, token: token.AccessToken, session: info.Session, expires: expires, key: key}
	m.mu.Lock()
	if !now.Before(m.nextPrune) {
		for _, old := range m.byID {
			if !now.Before(old.expires) {
				m.drop(old)
			}
		}
		m.nextPrune = now.Add(time.Minute)
	}
	m.byID[grant.id], m.byPlaceholder[placeholder] = grant, grant
	m.mu.Unlock()
	return Release{GrantID: grant.id, ExpiresAt: expires, Airlock: requests.AirlockRelease{ProxyURL: m.options.ProxyURL, CACertificate: m.options.CACertificate, ProxyPassword: placeholder, Names: map[string]requests.AirlockName{Name: {Value: placeholder, Slots: map[string]string{requests.SlotValue: placeholder}, Hosts: append([]string(nil), m.hosts...)}}}}, nil
}
func minTime(values ...time.Time) time.Time {
	out := values[0]
	for _, value := range values[1:] {
		if value.Before(out) {
			out = value
		}
	}
	return out
}
func (m *Manager) drop(grant *held) {
	delete(m.byID, grant.id)
	delete(m.byPlaceholder, grant.placeholder)
}
func (m *Manager) live(id string) (*held, error) {
	grant := m.byID[id]
	if grant == nil {
		return nil, requests.ErrGrantNotLive
	}
	if !m.options.Now().Before(grant.expires) {
		m.drop(grant)
		return nil, requests.ErrGrantNotLive
	}
	return grant, nil
}

func (m *Manager) AirlockConnection(ctx context.Context, placeholder string) (requests.AirlockConnection, error) {
	m.mu.Lock()
	grant := m.byPlaceholder[placeholder]
	if grant != nil {
		grant, err := m.live(grant.id)
		if err != nil {
			m.mu.Unlock()
			return requests.AirlockConnection{}, err
		}
		answer := requests.AirlockConnection{GrantID: grant.id, SessionID: grant.session, Expires: &grant.expires, Names: map[string]requests.AirlockGrantName{Name: {Slots: map[string]string{requests.SlotValue: placeholder}}}}
		m.mu.Unlock()
		return answer, nil
	}
	m.mu.Unlock()
	if m.options.Fallback == nil {
		return requests.AirlockConnection{}, requests.ErrGrantNotLive
	}
	return m.options.Fallback.AirlockConnection(ctx, placeholder)
}
func (m *Manager) AirlockValues(ctx context.Context, id string) (map[string]requests.AirlockSecret, string, error) {
	if !strings.HasPrefix(id, grantPrefix) {
		if m.options.Fallback == nil {
			return nil, "", requests.ErrGrantNotLive
		}
		return m.options.Fallback.AirlockValues(ctx, id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	grant, err := m.live(id)
	if err != nil {
		return nil, "", err
	}
	return map[string]requests.AirlockSecret{Name: {Slots: map[string]string{requests.SlotValue: grant.token}, Expires: grant.expires}}, m.PolicyVersion(), nil
}
func (m *Manager) PolicyVersion() string {
	if m.options.Fallback == nil {
		return "results"
	}
	return m.options.Fallback.PolicyVersion()
}
func (m *Manager) AirlockAccessPlaceholder(ctx context.Context, id, name, slot string) (string, error) {
	if strings.HasPrefix(id, grantPrefix) || m.options.Fallback == nil {
		return "", requests.ErrGrantNotLive
	}
	return m.options.Fallback.AirlockAccessPlaceholder(ctx, id, name, slot)
}

// DPoPTransport never signs a token chosen by the command, and is built once per grant.
// Signing occurs after the reverse proxy has fixed the real destination URL and method.
func (m *Manager) DPoPTransport(ctx context.Context, id string, base http.RoundTripper) (http.RoundTripper, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	grant, err := m.live(id)
	if err != nil {
		return nil, err
	}
	if grant.transport == nil {
		grant.transport = &ownedTransport{token: grant.token, signed: brokerclient.NewDPoPTransport(grant.key, base)}
	}
	return grant.transport, nil
}

type ownedTransport struct {
	token  string
	signed http.RoundTripper
}

func (t *ownedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if len(r.Header.Values("Authorization")) != 1 || r.Header.Get("Authorization") != "DPoP "+t.token {
		return nil, ErrNotResults
	}
	return t.signed.RoundTrip(r)
}
