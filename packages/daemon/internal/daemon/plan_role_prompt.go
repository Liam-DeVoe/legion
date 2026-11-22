package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/claim"
	stepplan "github.com/sjawhar/legion/daemon/internal/plan" // the package's own `plan` type holds the bare name
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// planRolePromptFile is the daemon's own copy of the prompt of a role an owner's plan defines:
// <stateDir>/plan-roles/<sha256 of the prompt, in hex>.md, with mode 0600. A launch reads a role's
// prompt from a file (runtime.PromptParts.RolePromptPaths), and a copy named by its text is the
// one file every version that gives the role that text shares: two versions of one prompt read
// one file, and a changed prompt is another file. A copy in place is used again only while its
// text hashes to its name. A copy a crash cut short, empty or holding part of the prompt, hashes
// to another name, so it is written again before the launch uses it. The write goes through a
// file of its own beside it, synced, and a rename, so a reader finds the whole prompt or the copy
// it replaces, and a crash after the rename leaves the whole prompt.
func planRolePromptFile(stateDir, prompt string) (string, error) {
	sum := sha256.Sum256([]byte(prompt))
	dir := filepath.Join(stateDir, "plan-roles")
	path := filepath.Join(dir, hex.EncodeToString(sum[:])+".md")
	held, err := os.ReadFile(path)
	if err == nil && sha256.Sum256(held) == sum {
		return path, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("the copy of a plan role's prompt: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("the copy of a plan role's prompt: %w", err)
	}
	staged, err := os.CreateTemp(dir, ".prompt-*")
	if err != nil {
		return "", fmt.Errorf("the copy of a plan role's prompt: %w", err)
	}
	defer os.Remove(staged.Name())
	if _, err := staged.WriteString(prompt); err != nil {
		staged.Close()
		return "", fmt.Errorf("the copy of a plan role's prompt: %w", err)
	}
	if err := staged.Sync(); err != nil {
		staged.Close()
		return "", fmt.Errorf("the copy of a plan role's prompt: %w", err)
	}
	if err := staged.Close(); err != nil {
		return "", fmt.Errorf("the copy of a plan role's prompt: %w", err)
	}
	if err := os.Rename(staged.Name(), path); err != nil {
		return "", fmt.Errorf("the copy of a plan role's prompt: %w", err)
	}
	return path, nil
}

// planRoleOf is the daemon's one reader of a role an owner's plan defines: the role of issue's
// plan in force named role (record.Store.PlanRoleOf, at the issue's current generation), and
// whether the plan defines one. It reads the daemon's own store in a transaction of its own, as
// the supervisor's predicates read a record (launchVariants), so a launch never waits on Dispatch:
// the store kept the role when the version that defines it was taken.
func planRoleOf(st *store.Store) func(ctx context.Context, issue string, role claim.Role) (record.PlanRole, bool, error) {
	records := record.NewStore()
	return func(ctx context.Context, issue string, role claim.Role) (record.PlanRole, bool, error) {
		var defined record.PlanRole
		var found bool
		err := st.Tx(ctx, func(tx pgx.Tx) error {
			var err error
			defined, found, err = records.PlanRoleOf(ctx, tx, issue, string(role))
			return err
		})
		if err != nil {
			return record.PlanRole{}, false, fmt.Errorf("the role %s of the plan of %s: %w", role, issue, err)
		}
		return defined, found, nil
	}
}

// roleApps is the one value every reader of a claim's App holds (appauth.RoleApps): deployed, the
// App of each role the deployment defines (deploymentApps), and planRole, the daemon's one reader
// of a role of a plan (planRoleOf), read for the role's type, so a role of an issue's plan acts as
// the App of its type.
func roleApps(deployed appauth.DeploymentApps, planRole func(ctx context.Context, issue string, role claim.Role) (record.PlanRole, bool, error)) appauth.RoleApps {
	return appauth.RoleApps{Deployed: deployed, Plan: func(ctx context.Context, issue string, role claim.Role) (appauth.Planned, error) {
		defined, found, err := planRole(ctx, issue, role)
		if err != nil {
			return appauth.Planned{}, err
		}
		return appauth.Planned{Defines: found, Type: stepplan.Type(defined.Type)}, nil
	}}
}
