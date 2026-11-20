package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/projectaccess"
)

// ServiceKinds is BROKER_NATS_SERVICES as the broker's HTTP routes read it: the kind of service
// each account the setting lists is bound to, by the kind of identity token the account presents.
// natscallout.ServiceKinds builds it from the parsed setting.
type ServiceKinds struct {
	// K8s maps each Kubernetes ServiceAccount subject the setting lists to its kind.
	K8s map[string]string
	// Google maps each Google account address the setting lists to its kind.
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
// this process. A listener batches its decisions and keeps one call in flight per connection, so
// 64 is well past what the listeners of a deployment hold; a call past it is answered 503 busy at
// once, asking Dispatch nothing, and a listener reads that as unknown, as it reads a 502.
const maxProjectPrivacyReads = 64

// projectPrivacy answers a listener every private project as Dispatch answers it to this call
// (projectaccess.Client.PrivateProjectsFresh), in key order. The read keeps nothing between
// calls, and any failure of it is 502 dispatch_unavailable, never a list: an empty list is the
// one answer a listener cannot tell from the truth, and a listener reads a 502 as unknown and
// fails closed. A broker with no BROKER_PROJECT_ACCESS_URL has no read to make and answers the
// same 502. A call past maxProjectPrivacyReads is 503 busy.
func (s *server) projectPrivacy(w http.ResponseWriter, r *http.Request) {
	if s.deps.ProjectPrivacy == nil {
		writeError(w, http.StatusBadGateway, "dispatch_unavailable", "this broker has no BROKER_PROJECT_ACCESS_URL, so it cannot ask Dispatch which projects are private")
		return
	}
	projects, busy, err := s.readProjectPrivacy(r.Context())
	switch {
	case busy:
		writeError(w, http.StatusServiceUnavailable, "busy", fmt.Sprintf("%d calls are already asking Dispatch which projects are private; ask again", maxProjectPrivacyReads))
		return
	case err != nil:
		slog.Error("broker: read Dispatch's private projects failed", "error", err)
		writeError(w, http.StatusBadGateway, "dispatch_unavailable", "Dispatch did not answer which projects are private")
		return
	}
	writeJSON(w, http.StatusOK, projectPrivacyBody(projects))
}

// readProjectPrivacy asks Dispatch once, holding one of the route's maxProjectPrivacyReads slots
// while it asks, or answers busy, asking nothing, while every slot is held. The slot is given
// back before it returns, so before the answer is written: a listener that has its answer finds
// the slot free for its next call.
func (s *server) readProjectPrivacy(ctx context.Context) (projects []projectaccess.PrivateProject, busy bool, err error) {
	select {
	case s.privacyReads <- struct{}{}:
	default:
		return nil, true, nil
	}
	defer func() { <-s.privacyReads }()
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
