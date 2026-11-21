package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/projectaccess"
)

// PrivacyReaders are the accounts GET /v1/project-privacy answers, as natscallout.PrivacyReaders
// reads them from BROKER_NATS_SERVICES: each one's name in the answer's record, <kind>/<machine
// id>, by its account, among the Kubernetes accounts or the Google ones as the token it presents
// is. Which entries are readers is natscallout's to decide (Kind.ReadsProjectPrivacy), and this
// package keeps no list of its own.
type PrivacyReaders struct {
	// K8s maps each Kubernetes ServiceAccount subject that reads project privacy to its name.
	K8s map[string]string
	// Google maps each Google account address that reads project privacy to its name.
	Google map[string]string
}

// FreshPrivateProjects is the one read GET /v1/project-privacy makes of Dispatch: every private
// project as Dispatch answers it now, with no cache and no older answer to fall back on.
// *projectaccess.Client answers it.
type FreshPrivateProjects interface {
	PrivateProjectsFresh(ctx context.Context) ([]projectaccess.PrivateProject, error)
}

// projectPrivacyResponse is GET /v1/project-privacy's answer: every private project as Dispatch
// answered it to this call's read.
type projectPrivacyResponse struct {
	// Every private project, in key order; empty, never null, when no project is private.
	Projects []privateProject `json:"projects"`
}

// privateProject is one private project as Dispatch answered it.
type privateProject struct {
	// The project's key.
	Project string `json:"project"`
	// The Slack channel ids the project's owner named, as Dispatch answered them; empty, never
	// null, for a project naming none.
	SlackChannels []string `json:"slack_channels"`
	// When Dispatch last changed the channels, a change of the list or the project's switch to
	// private; null while Dispatch answers null, for a list never changed.
	SlackChannelsChangedAt *time.Time `json:"slack_channels_changed_at"`
}

// maxProjectPrivacyReads bounds how many calls of GET /v1/project-privacy ask Dispatch at once, in
// this process, and maxProjectPrivacyReadsPerReader how many of those one reader's account holds,
// every replica that runs as it together. A listener batches its decisions and keeps one call in
// flight per connection, so both are well past what a deployment's listeners hold; the cap per
// account keeps one account, the measurement job's runs included, from taking every slot. A call
// past either is answered 503 busy at once, asking Dispatch nothing, and a listener reads that as
// unknown, as it reads a 502.
const (
	maxProjectPrivacyReads          = 64
	maxProjectPrivacyReadsPerReader = 16
)

// privacyReads is the route's slots: one for each call asking Dispatch, at most
// maxProjectPrivacyReads, and the calls each reader's account holds, at most
// maxProjectPrivacyReadsPerReader.
type privacyReads struct {
	slots chan struct{}

	mu        sync.Mutex
	byAccount map[string]int
}

func newPrivacyReads() *privacyReads {
	return &privacyReads{slots: make(chan struct{}, maxProjectPrivacyReads), byAccount: map[string]int{}}
}

// take holds one slot for account, or reports false, holding nothing, when every slot or every
// one of the account's own is held.
func (p *privacyReads) take(account string) bool {
	select {
	case p.slots <- struct{}{}:
	default:
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byAccount[account] >= maxProjectPrivacyReadsPerReader {
		<-p.slots
		return false
	}
	p.byAccount[account]++
	return true
}

// give hands back the slot take held for account.
func (p *privacyReads) give(account string) {
	p.mu.Lock()
	if p.byAccount[account]--; p.byAccount[account] == 0 {
		delete(p.byAccount, account)
	}
	p.mu.Unlock()
	<-p.slots
}

// projectPrivacy answers a listener every private project as Dispatch answers it to this call
// (projectaccess.Client.PrivateProjectsFresh), in key order. A listener reads which projects are
// private only through this route, so the rule is here: one fresh PrivateProjectsFresh read per
// call, never PrivateProjects' cache, and never a list for a read that failed. The read keeps
// nothing between calls, and any failure of it is 502 dispatch_unavailable, never a list: an empty
// list is the one answer a listener cannot tell from the truth, and a listener reads a 502 as
// unknown and fails closed. A broker with no BROKER_PROJECT_ACCESS_URL has no read to make and
// answers the same 502. A call past maxProjectPrivacyReads, or past
// maxProjectPrivacyReadsPerReader of its account's, is 503 busy.
func (s *server) projectPrivacy(w http.ResponseWriter, r *http.Request, account string) {
	if s.deps.ProjectPrivacy == nil {
		writeError(w, http.StatusBadGateway, "dispatch_unavailable", "this broker has no BROKER_PROJECT_ACCESS_URL, so it cannot ask Dispatch which projects are private")
		return
	}
	projects, busy, err := s.readProjectPrivacy(r.Context(), account)
	switch {
	case busy:
		writeError(w, http.StatusServiceUnavailable, "busy", fmt.Sprintf("%d calls in all, or %d of this caller's, are already asking Dispatch which projects are private; ask again", maxProjectPrivacyReads, maxProjectPrivacyReadsPerReader))
		return
	case err != nil:
		// WARN, as every other read of Dispatch that fails is: an outage is Dispatch's, and this
		// route is called at every listener decision.
		slog.Warn("broker: read Dispatch's private projects failed", "error", err)
		writeError(w, http.StatusBadGateway, "dispatch_unavailable", "Dispatch did not answer which projects are private")
		return
	}
	writeJSON(w, http.StatusOK, projectPrivacyBody(projects))
}

// readProjectPrivacy asks Dispatch once, holding one of the route's slots for account while it
// asks, or answers busy, asking nothing, while none is free (privacyReads.take). The slot is given
// back before it returns, so before the answer is written: a listener that has its answer finds
// the slot free for its next call.
func (s *server) readProjectPrivacy(ctx context.Context, account string) (projects []projectaccess.PrivateProject, busy bool, err error) {
	if !s.privacyReads.take(account) {
		return nil, true, nil
	}
	defer s.privacyReads.give(account)
	projects, err = s.deps.ProjectPrivacy.PrivateProjectsFresh(ctx)
	return projects, false, err
}

// projectPrivacyBody is projects as GET /v1/project-privacy answers them: Dispatch's rows, sorted
// by key, each project's channels an empty list rather than null and its zero change time null.
func projectPrivacyBody(projects []projectaccess.PrivateProject) projectPrivacyResponse {
	body := projectPrivacyResponse{Projects: make([]privateProject, 0, len(projects))}
	for _, project := range projects {
		entry := privateProject{Project: project.Key, SlackChannels: project.SlackChannels}
		if entry.SlackChannels == nil {
			entry.SlackChannels = []string{}
		}
		if !project.SlackChannelsChangedAt.IsZero() {
			changed := project.SlackChannelsChangedAt
			entry.SlackChannelsChangedAt = &changed
		}
		body.Projects = append(body.Projects, entry)
	}
	slices.SortFunc(body.Projects, func(a, b privateProject) int { return strings.Compare(a.Project, b.Project) })
	return body
}
