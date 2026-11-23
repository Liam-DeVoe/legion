package tmux

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghconfig"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// ownerPaneRig is a fake server runtime whose ActsAs answers, for the architect it was built
// around, the role acting holds ("" for none: the owner does no step), and whose credential
// function runs hook, once, inside the first mint of the architect's own App that minting allows:
// the moment a writer has judged the pane's role and not written yet.
type ownerPaneRig struct {
	*fakeServerRuntime
	spec    runtime.SpawnSpec
	acting  atomic.Value
	minting atomic.Bool
	hooked  atomic.Bool
	hook    func()
}

func newOwnerPaneRig(t *testing.T) *ownerPaneRig {
	t.Helper()
	g := &ownerPaneRig{}
	g.acting.Store(claim.Role(""))
	var owner atomic.Value
	owner.Store(claim.Token(""))
	g.fakeServerRuntime = newFakeServerRuntimeWith(t, func(ctx context.Context, issue string, role claim.Role, repo ghrepo.Repository) (ghconfig.Bundle, error) {
		if role == claim.RoleArchitect && g.minting.Load() && g.hooked.CompareAndSwap(false, true) {
			g.hook()
		}
		return staticCredential(ctx, issue, role, repo)
	}, nil, func(o *Options) {
		o.ActsAs = func(_ context.Context, token claim.Token) (claim.Role, bool, error) {
			if role := g.acting.Load().(claim.Role); token == owner.Load().(claim.Token) && role != "" {
				return role, true, nil
			}
			return "", false, nil
		}
	})
	g.spec = g.fakeServerRuntime.spec(t, claim.RoleArchitect)
	owner.Store(g.spec.Claim)
	return g
}

// held is the token the hosts.yml of the architect's own owner, acme, holds.
func (g *ownerPaneRig) held(t *testing.T) string {
	t.Helper()
	return readHostsToken(t, ghconfig.Dir(runtime.GHConfigDir(g.stateDir, g.spec.Claim), "acme"))
}

// stepTold is what the outbox does before it tells the owner its step: the write of the owner's
// slot, then the ask whether its gh files hold the step's App, answering the ask's error.
func (g *ownerPaneRig) stepTold(ctx context.Context, t *testing.T) error {
	t.Helper()
	if err := g.WriteSlotCredential(ctx, g.spec.Claim); err != nil {
		t.Errorf("WriteSlotCredential of the owner's pane = %v", err)
	}
	return g.SlotHoldsCredential(ctx, g.spec.Claim, claim.RoleImplementer)
}

// The minute refresh judges the owner's pane on the architect's own App, then mints that App's
// bundle; in that mint the owner's step starts, and the outbox writes the pane for the step's App
// and asks whether its files hold it, before it tells the owner the step. The refresh then writes.
// The pane's files end on the step's App, the one the owner was told it holds: the refresh judges
// the role again where it writes, so a write judged before the step started never lands after the
// step's own. The control is the same refresh with no step starting in its mint.
func TestAStepsWriteInARefreshsMintIsNotUndoneByTheRefresh(t *testing.T) {
	for _, tc := range []struct {
		name   string
		starts bool
	}{
		{name: "control: no step starts in the refresh's mint"},
		{name: "the owner's step starts in the refresh's mint", starts: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			g := newOwnerPaneRig(t)
			if _, err := g.Spawn(ctx, g.spec); err != nil {
				t.Fatalf("Spawn of the architect: %v", err)
			}
			implement, review := staticCredentialToken(claim.RoleImplementer), staticCredentialToken(claim.RoleArchitect)
			if got := g.held(t); got != review {
				t.Fatalf("the architect's pane holds %q after its spawn with no step running, want %q", got, review)
			}
			var told error
			var heldWhenTold string
			var once sync.Once
			g.hook = func() {
				if !tc.starts {
					return
				}
				once.Do(func() {
					g.acting.Store(claim.RoleImplementer)
					told = g.stepTold(ctx, t)
					heldWhenTold = g.held(t)
				})
			}
			g.minting.Store(true)
			g.refreshGitHubCredentials(ctx)
			g.minting.Store(false)
			if !g.hooked.Load() {
				t.Fatal("the refresh never minted the architect's own App: the case saw no window")
			}
			want := review
			if tc.starts {
				want = implement
				if told != nil || heldWhenTold != implement {
					t.Fatalf("in the refresh's mint the step's ask answered %v with the pane holding %q, want nil with %q", told, heldWhenTold, implement)
				}
			}
			if got := g.held(t); got != want {
				t.Fatalf("after the refresh the owner's pane holds %q, want %q: the refresh's write, judged before the step started, landed after the step's", got, want)
			}
		})
	}
}

// A launch of the owner judges its pane's role, then mints that role's bundle; in that mint the
// owner's step starts, and the outbox writes the owner's slot and asks whether its files hold the
// step's App. No pane of the claim is watched yet, so the ask is a wait, and the launch writes the
// pane for the role judged where it writes, the step's: so the files the owner is told about hold
// the step's App whenever the ask answers nil, and once the launch ends the ask, made again as the
// outbox makes a held notice's again, answers nil.
func TestAStepThatStartsInALaunchsMintIsToldOnlyOnceThePaneHoldsItsApp(t *testing.T) {
	ctx := context.Background()
	g := newOwnerPaneRig(t)
	var told error
	g.hook = func() {
		g.acting.Store(claim.RoleImplementer)
		told = g.stepTold(ctx, t)
	}
	g.minting.Store(true)
	if _, err := g.Spawn(ctx, g.spec); err != nil {
		t.Fatalf("Spawn of the architect: %v", err)
	}
	g.minting.Store(false)
	if !g.hooked.Load() {
		t.Fatal("the launch never minted the architect's own App: the case saw no window")
	}
	implement := staticCredentialToken(claim.RoleImplementer)
	held := g.held(t)
	if told == nil && held != implement {
		t.Fatalf("the step's ask in the launch's mint answered nil, and the pane the launch then wrote holds %q, want %q", held, implement)
	}
	if !errors.Is(told, runtime.ErrSlotCredentialPending) && told != nil {
		t.Fatalf("the step's ask in the launch's mint = %v, want nil or runtime.ErrSlotCredentialPending", told)
	}
	if err := g.stepTold(ctx, t); err != nil {
		t.Fatalf("the step's ask again after the launch = %v, want nil", err)
	}
	if got := g.held(t); got != implement {
		t.Fatalf("the owner's pane holds %q after the launch and the step's write, want %q", got, implement)
	}
}

// A relaunch of the owner whose earlier pane is still watched, its process gone and not yet let go,
// judges the pane's role and mints that role's bundle; in that mint the owner's step starts, and
// the outbox writes the slot through the earlier pane's entry and asks whether its files hold the
// step's App. They do, so the owner is told the step. The relaunch then lets the earlier pane go
// (refuseLiveIncarnation) and writes the claim's gh files under the launch lock. They stay on the
// step's App: the relaunch judges the role again there (writeGitHubCredential), so its write,
// judged before the step started, never lands after the step's write the owner was told of. Only
// that second judgment holds this, since the file check answered before the relaunch wrote.
func TestARelaunchJudgedBeforeAStepDoesNotUndoTheWriteTheOwnerWasToldOf(t *testing.T) {
	ctx := context.Background()
	g := newOwnerPaneRig(t)
	if _, err := g.Spawn(ctx, g.spec); err != nil {
		t.Fatalf("Spawn of the architect: %v", err)
	}
	implement := staticCredentialToken(claim.RoleImplementer)
	var told error
	var heldWhenTold string
	g.hook = func() {
		g.acting.Store(claim.RoleImplementer)
		told = g.stepTold(ctx, t)
		heldWhenTold = g.held(t)
	}
	g.minting.Store(true)
	if _, err := g.Spawn(ctx, g.spec); err != nil {
		t.Fatalf("the relaunch of the architect: %v", err)
	}
	g.minting.Store(false)
	if !g.hooked.Load() {
		t.Fatal("the relaunch never minted the architect's own App: the case saw no window")
	}
	if told != nil || heldWhenTold != implement {
		t.Fatalf("the case's premise: in the relaunch's mint the step's ask answered %v with the pane holding %q, want nil with %q, the earlier pane still watched", told, heldWhenTold, implement)
	}
	if got := g.held(t); got != implement {
		t.Fatalf("after the relaunch the owner's pane holds %q, want %q: the relaunch's write, judged before the step started, landed after the step's write the owner was told of", got, implement)
	}
}

// The tmux runtime says a pane holds an App only where the pane's files hold it: an owner's pane
// whose files hold the review App, the architect's own, is not said to hold the implement App, and
// the ask is a wait (runtime.ErrSlotCredentialPending); once the owner's step runs and the pane is
// written for it, the same ask answers nil.
func TestATmuxPaneHoldingAnotherAppIsNotSaidToHoldTheStepsApp(t *testing.T) {
	ctx := context.Background()
	g := newOwnerPaneRig(t)
	if _, err := g.Spawn(ctx, g.spec); err != nil {
		t.Fatalf("Spawn of the architect: %v", err)
	}
	if got, review := g.held(t), staticCredentialToken(claim.RoleArchitect); got != review {
		t.Fatalf("the architect's pane holds %q after its spawn with no step running, want %q", got, review)
	}
	err := g.SlotHoldsCredential(ctx, g.spec.Claim, claim.RoleImplementer)
	if !errors.Is(err, runtime.ErrSlotCredentialPending) {
		t.Fatalf("SlotHoldsCredential(the architect, implementer) with the review App in its files = %v, want runtime.ErrSlotCredentialPending", err)
	}
	g.acting.Store(claim.RoleImplementer)
	if err := g.stepTold(ctx, t); err != nil {
		t.Fatalf("SlotHoldsCredential(the architect, implementer) once its pane is written for its step = %v, want nil", err)
	}
}
