// Package agent is the loop of the Dispatch server's agent process (envoy-dispatch agent): it
// says hello to the server under a new instance id, keeps one renew waiting, which carries the
// process's health, and claims ready runs, making every read and write of Dispatch as a call of
// agent/v1 (going/agentwire) to the server.
//
// This build has no model loop yet: each run it claims, it ends at once as failed with
// ReasonUnbuilt, so a run never waits on an agent that cannot work it.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/going/agentwire"
)

// ReasonUnbuilt is the stop reason of each run this build's agent claims: it has no model loop to
// work the run with.
const ReasonUnbuilt = "agent_unbuilt: this build's agent process has no model loop, so it ends each run it claims"

// retryAfter is how long the loop waits before it makes a renew or a claim again after one failed
// for any reason but its context ending: the server may be starting or stopping.
const retryAfter = time.Second

// Settings are the chat model's settings, the only configuration the agent process is given: where
// it generates text, as the server's DISPATCH_ANSWERER names it ("" for no model), the endpoint
// and the model, and the credential's parts - the Azure deployment's API version, scope, quota and
// federated sign-in, or the token file of an OpenAI-shaped endpoint. It holds paths and names, no
// secret.
type Settings struct {
	Place              string
	Endpoint           string
	Model              string
	APIVersion         string
	Scope              string
	TokensPerMinute    int
	TokenFile          string
	TenantID           string
	ClientID           string
	FederatedTokenFile string
}

// Run is the agent's loop at the server's socket client calls, until ctx ends, when it returns
// nil. It fails only when the server refuses its hello, which the process supervisor answers by
// starting the agent again.
func Run(ctx context.Context, client agentwire.Client, _ Settings) error {
	instance := uuid.NewString()
	if _, err := client.Hello(ctx, agentwire.HelloRequest{Instance: instance}); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("say hello to the server: %w", err)
	}
	var held leases
	var renewing sync.WaitGroup
	renewing.Go(func() { renew(ctx, client, instance, &held) })
	defer renewing.Wait()
	for ctx.Err() == nil {
		answer, err := client.Claim(ctx, agentwire.ClaimRequest{Instance: instance})
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("agent: claim a run", "error", err)
				pause(ctx)
			}
			continue
		}
		if answer.Run == nil {
			continue
		}
		run := answer.Run
		held.set(&agentwire.Lease{Run: run.ID, LeaseToken: run.LeaseToken})
		end := agentwire.EndRequest{Run: run.ID, LeaseToken: run.LeaseToken, State: agentwire.StateFailed, Reason: ReasonUnbuilt}
		if err := client.End(ctx, end); err != nil && ctx.Err() == nil {
			var refused *agentwire.Error
			if !errors.As(err, &refused) || refused.Code != agentwire.CodeStaleLease {
				slog.Warn("agent: end a run", "run", run.ID, "error", err)
			}
		}
		held.set(nil)
	}
	return nil
}

// renew keeps one renew of the instance's leases waiting at the server until ctx ends, each
// carrying the process's health.
func renew(ctx context.Context, client agentwire.Client, instance string, held *leases) {
	for ctx.Err() == nil {
		holding := held.list()
		health := agentwire.RuntimeHealth()
		health.Working = len(holding)
		if _, err := client.Renew(ctx, agentwire.RenewRequest{Instance: instance, Leases: holding, Health: health}); err != nil && ctx.Err() == nil {
			slog.Warn("agent: renew", "error", err)
			pause(ctx)
		}
	}
}

// pause waits retryAfter, or until ctx ends.
func pause(ctx context.Context) {
	timer := time.NewTimer(retryAfter)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// leases is the lease the loop holds, if any, which each renew names.
type leases struct {
	mu   sync.Mutex
	held *agentwire.Lease
}

func (l *leases) set(lease *agentwire.Lease) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held = lease
}

func (l *leases) list() []agentwire.Lease {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held == nil {
		return []agentwire.Lease{}
	}
	return []agentwire.Lease{*l.held}
}
