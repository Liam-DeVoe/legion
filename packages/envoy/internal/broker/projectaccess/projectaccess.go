// Package projectaccess is the broker's one client of Dispatch's project-access routes: whether a
// project is private, whether a person may see it, and what kind of member a person is. Dispatch
// decides all three — the owner, the access list, a group's members, the full and restricted
// members — and the broker asks rather than keeps a copy, so a decision here is the one Dispatch
// would make for that person's own read.
//
// Every answer is plain data. The rule every caller of this package keeps is that no answer is
// asked for inside a database transaction, inside policy.Set.Evaluate, or while a row lock or an
// advisory lock is held: a caller resolves what it needs first and hands the answers on, so a
// Dispatch that is slow to answer holds no lock and no pooled connection of the broker's. The
// token exchange asks before it opens its transaction (internal/broker/oauth, exchangeGrant).
//
// Answers are cached CacheTTL per project and person and per person, which bounds how long a
// person removed from a project's access list goes on passing a check here. A failure to reach
// Dispatch is never cached and is never an answer: each caller decides what an outage means for
// its own check.
package projectaccess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sjawhar/envoy/internal/brokertoken"
)

// The member kinds Dispatch answers for a person.
const (
	// MemberFull is a person who signed in under the deployment's sign-in domain.
	MemberFull = "full"
	// MemberRestricted is a person an access list admitted from outside that domain, who sees
	// only the projects that name them.
	MemberRestricted = "restricted"
	// MemberNone is a person Dispatch holds no row for.
	MemberNone = "none"
)

// CacheTTL is how long one answer is kept. It is the bound on how long a person removed from a
// project's access list goes on passing a check made here, beside the life of a token minted
// before the removal (BROKER_ACCESS_TOKEN_SECONDS).
const CacheTTL = 30 * time.Second

// requestTimeout bounds one call to Dispatch, the dial included.
const requestTimeout = 10 * time.Second

// maxAnswerBytes caps the answer read from Dispatch. An answer is a handful of short fields and a
// list of repositories, so anything larger is not Dispatch's and is refused rather than held.
const maxAnswerBytes = 64 << 10

// maxCached bounds how many answers are kept at once, so a caller naming project keys at random
// cannot grow the cache without bound. An answer that does not fit is not kept, which costs that
// question a call to Dispatch and nothing else.
const maxCached = 4096

// The paths of Dispatch's two routes.
const (
	projectPath = "/api/v1/access/projects/"
	personPath  = "/api/v1/access/person"
)

var (
	// ErrProjectNotFound is a project Dispatch does not hold.
	ErrProjectNotFound = errors.New("projectaccess: dispatch holds no such project")
	// ErrNotVisible is a credential naming a project its person may not see, or one Dispatch
	// does not hold: the two are refused in the same words, so a refusal never says whether a
	// project exists.
	ErrNotVisible = errors.New("projectaccess: names a project its person cannot see")
	// ErrListRequired is a restricted member's credential that names no project: such a person
	// sees only the projects that name them, so their credential names which.
	ErrListRequired = errors.New("projectaccess: a restricted member's credential names its projects")
)

// NotVisibleError is Visible's refusal: the first project of a list its person may not see, or
// that Dispatch does not hold. It is ErrNotVisible (errors.Is), and its Project is what a caller
// names in its own refusal; its text is the same whichever of the two it was.
type NotVisibleError struct{ Project string }

func (e *NotVisibleError) Error() string { return ErrNotVisible.Error() + ": " + e.Project }

// Unwrap is ErrNotVisible.
func (e *NotVisibleError) Unwrap() error { return ErrNotVisible }

// Answer is Dispatch's answer for one project and one person.
type Answer struct {
	// Project is the project's key.
	Project string `json:"project"`
	// Visibility is "public" or "private".
	Visibility string `json:"visibility"`
	// Owner is the owner's login.
	Owner string `json:"owner"`
	// Person is the person asked about, empty for a question that named none.
	Person string `json:"person"`
	// MemberKind is the person's MemberFull, MemberRestricted or MemberNone.
	MemberKind string `json:"member_kind"`
	// Visible is whether the person may see the project, or, for a question naming no person,
	// whether it is public.
	Visible bool `json:"visible"`
	// Repositories is every "owner/name" repository Dispatch maps to the project, renames
	// included, nil when the answer carries none.
	Repositories []string `json:"repositories"`
}

// Private reports whether the project is private.
func (a Answer) Private() bool { return a.Visibility == "private" }

// Access is every question a broker reader asks Dispatch; *Client answers all three.
type Access interface {
	// Access is Dispatch's answer for project and person; ErrProjectNotFound for a project it
	// does not hold.
	Access(ctx context.Context, project, person string) (Answer, error)
	// MemberKind is the person's member kind.
	MemberKind(ctx context.Context, person string) (string, error)
	// States answers one fact per key, for the caller that must resolve every answer before it
	// opens a transaction: present for a project Dispatch holds, absent for one it does not. It
	// is one call per key against the cache, not a batch route.
	States(ctx context.Context, projects []string) (map[string]Answer, error)
}

// Client asks Dispatch's routes at one base URL, presenting the broker's own account token.
type Client struct {
	base    string
	account brokertoken.Account
	http    *http.Client
	now     func() time.Time

	mu      sync.Mutex
	answers map[accessKey]cachedAnswer
	kinds   map[string]cachedKind
}

type accessKey struct{ project, person string }

type cachedAnswer struct {
	answer Answer
	// missing is a remembered ErrProjectNotFound, which is an answer as much as Answer is.
	missing bool
	at      time.Time
}

type cachedKind struct {
	kind string
	at   time.Time
}

// New is a client of Dispatch at baseURL (BROKER_PROJECT_ACCESS_URL), presenting account
// (BROKER_PROJECT_ACCESS_TOKEN_FILE's reader) on every call. A nil httpClient is
// http.DefaultClient; each call is bounded by its own 10 s whatever the client.
func New(baseURL string, account brokertoken.Account, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		base:    strings.TrimRight(baseURL, "/"),
		account: account,
		http:    httpClient,
		now:     time.Now,
		answers: map[accessKey]cachedAnswer{},
		kinds:   map[string]cachedKind{},
	}
}

// Access is Dispatch's answer for project and person, from the cache while it is younger than
// CacheTTL. A person is asked about by their lowercase login; an empty person asks whether the
// project is public.
func (c *Client) Access(ctx context.Context, project, person string) (Answer, error) {
	key := accessKey{project: project, person: strings.ToLower(strings.TrimSpace(person))}
	if cached, ok := c.cachedAnswer(key); ok {
		if cached.missing {
			return Answer{}, fmt.Errorf("%w: %s", ErrProjectNotFound, project)
		}
		return cached.answer, nil
	}
	query := url.Values{}
	if key.person != "" {
		query.Set("person", key.person)
	}
	var answer Answer
	status, err := c.get(ctx, projectPath+url.PathEscape(project), query, &answer)
	switch {
	case err != nil:
		return Answer{}, err
	case status == http.StatusNotFound:
		c.keepAnswer(key, cachedAnswer{missing: true})
		return Answer{}, fmt.Errorf("%w: %s", ErrProjectNotFound, project)
	}
	c.keepAnswer(key, cachedAnswer{answer: answer})
	return answer, nil
}

// MemberKind is the person's member kind, from the cache while it is younger than CacheTTL.
func (c *Client) MemberKind(ctx context.Context, person string) (string, error) {
	person = strings.ToLower(strings.TrimSpace(person))
	if person == "" {
		return "", errors.New("projectaccess: a member kind is asked of a person, and none was named")
	}
	c.mu.Lock()
	cached, ok := c.kinds[person]
	c.mu.Unlock()
	if ok && c.now().Sub(cached.at) < CacheTTL {
		return cached.kind, nil
	}
	var answer struct {
		MemberKind string `json:"member_kind"`
	}
	status, err := c.get(ctx, personPath, url.Values{"person": {person}}, &answer)
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound || answer.MemberKind == "" {
		return "", fmt.Errorf("projectaccess: dispatch answered %d with no member kind for %s", status, person)
	}
	c.mu.Lock()
	if len(c.kinds) >= maxCached {
		c.sweepLocked()
	}
	if len(c.kinds) < maxCached {
		c.kinds[person] = cachedKind{kind: answer.MemberKind, at: c.now()}
	}
	c.mu.Unlock()
	return answer.MemberKind, nil
}

// States answers each of projects Dispatch holds, as it is to a question naming no person, and
// leaves out each it does not. Any other failure is the whole answer's.
func (c *Client) States(ctx context.Context, projects []string) (map[string]Answer, error) {
	states := make(map[string]Answer, len(projects))
	for _, project := range projects {
		answer, err := c.Access(ctx, project, "")
		if errors.Is(err, ErrProjectNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		states[project] = answer
	}
	return states, nil
}

func (c *Client) cachedAnswer(key accessKey) (cachedAnswer, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cached, ok := c.answers[key]
	if !ok || c.now().Sub(cached.at) >= CacheTTL {
		return cachedAnswer{}, false
	}
	return cached, true
}

func (c *Client) keepAnswer(key accessKey, answer cachedAnswer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	answer.at = c.now()
	if len(c.answers) >= maxCached {
		c.sweepLocked()
	}
	if len(c.answers) < maxCached {
		c.answers[key] = answer
	}
}

// sweepLocked drops every answer older than CacheTTL. c.mu is held.
func (c *Client) sweepLocked() {
	now := c.now()
	for key, cached := range c.answers {
		if now.Sub(cached.at) >= CacheTTL {
			delete(c.answers, key)
		}
	}
	for person, cached := range c.kinds {
		if now.Sub(cached.at) >= CacheTTL {
			delete(c.kinds, person)
		}
	}
}

// get asks one route and decodes a 200 into into. It answers 404 as a status with no error, since
// the project route's 404 is an answer; every other status, an account token it could not read, a
// transport failure and a body it could not decode are errors naming what failed and never the
// token.
func (c *Client) get(ctx context.Context, path string, query url.Values, into any) (int, error) {
	bearer, err := c.account.Read()
	if err != nil {
		return 0, fmt.Errorf("projectaccess: read the broker's account token: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, fmt.Errorf("projectaccess: build the request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return 0, fmt.Errorf("projectaccess: ask dispatch: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAnswerBytes+1))
	if err != nil {
		return 0, fmt.Errorf("projectaccess: read dispatch's answer: %w", err)
	}
	if len(body) > maxAnswerBytes {
		return 0, fmt.Errorf("projectaccess: dispatch's answer to %s is over %d bytes", path, maxAnswerBytes)
	}
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return http.StatusNotFound, nil
	default:
		return 0, fmt.Errorf("projectaccess: dispatch answered %s %d", path, response.StatusCode)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return 0, fmt.Errorf("projectaccess: decode dispatch's answer to %s: %w", path, err)
	}
	return http.StatusOK, nil
}

// Visible answers nil when person may see every one of projects, else a *NotVisibleError (which
// is ErrNotVisible) naming the first it may not see. A project Dispatch does not hold is the same
// refusal, so the text never says whether a project exists. Any other failure is returned as it
// is, and is no refusal.
func Visible(ctx context.Context, a Access, person string, projects []string) error {
	for _, project := range projects {
		answer, err := a.Access(ctx, project, person)
		if errors.Is(err, ErrProjectNotFound) {
			return &NotVisibleError{Project: project}
		}
		if err != nil {
			return err
		}
		if !answer.Visible {
			return &NotVisibleError{Project: project}
		}
	}
	return nil
}

// ListAllowed is the one statement of the rule for a person's list, used by the exchange and by
// an enrollment alike. A non-empty list must pass Visible. An empty list is refused with
// ErrListRequired only when MemberKind answers MemberRestricted; MemberFull, MemberNone and any
// error from MemberKind answer nil, so a person Dispatch has never seen, and an unreachable
// Dispatch, mint and enroll as today.
func ListAllowed(ctx context.Context, a Access, person string, projects []string) error {
	if len(projects) > 0 {
		return Visible(ctx, a, person, projects)
	}
	kind, err := a.MemberKind(ctx, person)
	if err != nil || kind != MemberRestricted {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrListRequired, person)
}
