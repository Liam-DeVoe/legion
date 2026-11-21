package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/access"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// A switch to private first closes the project to developer boxes (access.StartClosing): Dispatch
// revokes every live personal token that lists it, the membership answer says it is closed, and the
// secrets broker mints and renews no developer-box token that lists it. The switch completes only
// once the broker counts no such token still live, and no sooner than developerBoxSettle after the
// closing state began; until then it answers 409 DEVELOPER_BOX_TOKENS_LIVE, and the owner's next
// switch completes it. The closing state is committed whatever the switch answers, so the broker
// reads it and the tokens stay revoked. The owner's switch to public ends it.

// developerBoxSettle is how long after its closing state began a switch to private first trusts
// the broker's count of 0: the broker holds Dispatch's answer about a project for at most 30
// seconds (projectaccess.CacheTTL), and stores a developer box's token only while the answer it
// rests on is younger than that, so no token stored more than 30 seconds after the closing state
// began rests on an answer from before it. The other 30 seconds cover the two clocks, the broker's
// and Dispatch's database's, disagreeing. A variable only so a test lowers it.
var developerBoxSettle = time.Minute

// errDeveloperBoxTokensLive is a switch to private while the broker may still hold a developer-box
// token that lists the project: 409 DEVELOPER_BOX_TOKENS_LIVE, naming the count and when the
// closing state began.
var errDeveloperBoxTokensLive = errors.New("the switch to private completes once no developer-box token that lists the project is live")

// errDeveloperBoxTokensUnknown is a switch to private whose count of the broker's developer-box
// tokens could not be read: 503 DEVELOPER_BOX_TOKENS_UNAVAILABLE, naming the cause.
var errDeveloperBoxTokensUnknown = errors.New("the switch to private waits for the secrets broker's count of the developer-box tokens that list the project, and the broker did not answer it")

// refusalKept is a write's refusal that keeps what the write did before it: ownerWrite commits
// the write's transaction, publishes what it appended, and then answers err. The closing state is
// the one write that refuses so (closeForDeveloperBoxes), and it refuses before any other write
// of the body runs.
type refusalKept struct{ err error }

func (r refusalKept) Error() string { return r.err.Error() }
func (r refusalKept) Unwrap() error { return r.err }

// closeForDeveloperBoxes is the closing step of project key's switch to private, in the switch's
// transaction tx under the project's owner lock: it starts the closing state where the project is
// in none, and answers what that appended. It answers nil when the switch may go on: the
// deployment has no secrets broker (Deps.AgentSecrets nil), so no developer-box token reads it,
// or the broker counts no developer-box token of the project live and the closing state is
// developerBoxSettle old. Otherwise it answers a refusalKept of errDeveloperBoxTokensLive, or of
// errDeveloperBoxTokensUnknown where the broker did not answer, so the closing state commits.
func (s *server) closeForDeveloperBoxes(ctx context.Context, tx pgx.Tx, key string, by model.Actor) ([]model.Event, error) {
	state, err := access.ReadClosing(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	var appended []model.Event
	since, started := state.Since, state.Since == nil
	if started {
		_, event, revoked, err := access.StartClosing(ctx, tx, s.deps.Events, key, by)
		if err != nil {
			return nil, err
		}
		appended = append(appended, event)
		since = &state.Now
		slog.Info("dispatch: a project's switch to private started its closing state, revoking the personal tokens that list it",
			"project", key, "personal_tokens_revoked", revoked)
	}
	if s.deps.AgentSecrets == nil {
		return appended, nil
	}
	count, err := s.deps.AgentSecrets.DeveloperBoxTokens(ctx, key)
	if err != nil {
		return appended, refusalKept{fmt.Errorf("%w (%v); %s stays closing and its switch completes on a later request", errDeveloperBoxTokensUnknown, err, key)}
	}
	settled := since.Add(developerBoxSettle)
	if count.Live == 0 && !started && !state.Now.Before(settled) {
		return appended, nil
	}
	last := ""
	if count.LastExpiresAt != nil {
		last = ", the last expiring at " + count.LastExpiresAt.UTC().Format(time.RFC3339)
	}
	return appended, refusalKept{fmt.Errorf("%s is closing since %s, and the secrets broker holds %d live developer-box tokens that list it%s; %w and no sooner than %s",
		key, since.UTC().Format(time.RFC3339), count.Live, last, errDeveloperBoxTokensLive, settled.UTC().Format(time.RFC3339))}
}
