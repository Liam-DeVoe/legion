package githubtest

import (
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"
)

// Checks is GitHub as a GitHub App's client reads the check runs of a commit. Not built yet: it
// answers every request 404 and keeps it (Unanswered).
type Checks struct {
	// URL is the server's root.
	URL string

	mu         sync.Mutex
	unanswered []string
}

// CheckRun is one check run of a commit.
type CheckRun struct {
	ID          int64
	Name        string
	Status      string
	Conclusion  string
	CompletedAt time.Time
	HTMLURL     string
	Output      CheckRunOutput
	AppSlug     string
}

// CheckRunOutput is a check run's output, as its job wrote it.
type CheckRunOutput struct {
	Title, Summary string
}

// ServeChecks starts Checks; it is closed when the test ends.
func ServeChecks(t *testing.T) *Checks {
	t.Helper()
	c := &Checks{}
	c.URL = Server(t, Spec{Handle: func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.unanswered = append(c.unanswered, r.Method+" "+r.URL.Path)
		c.mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
	}})
	return c
}

// SetCheckRun sets run on commit sha of repo. Not built yet: it sets nothing.
func (c *Checks) SetCheckRun(repo, sha string, run CheckRun) {}

// Unanswered is each request the server answered 404, as "METHOD /path", in the order it came.
func (c *Checks) Unanswered() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.unanswered)
}

// ChecksSettlement is what the listener's CI summary loop settles for a commit of a pull request.
type ChecksSettlement struct {
	Repo       string
	Number     int
	SHA        string
	Runs       []CheckRun
	Generation uint64
}

// PublishChecksSettlement publishes s on the NATS server at natsURL. Not built yet: it publishes
// nothing.
func PublishChecksSettlement(t *testing.T, natsURL string, s ChecksSettlement) {}
