//go:build linux

package launcher

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/lookpath"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// roleWorkspace readies the workspace the role is told it works in (LEGION_WORKSPACE), in the
// role's own namespace once it is built, and makes it the directory the shim starts in. A
// reviewer whose LEGION_WORKSPACE is its own clone of the issue (workspace.ReviewLocation of the
// issue's workspace on the same volume) makes that clone from the issue's shared clone, or brings
// the one there to the head the daemon gives it (workspace.ReviewHeadEnv), refusing to start
// where the clone cannot stand there, with the first compiled git and jj on its PATH, the image's
// own and never their wrappers (workspace.ProvisionReviewClone): this runs as the reviewer's
// role-init, after the launcher has answered the start, so the wait counts against the
// registration deadline a launching role is given, which holds the pod's provisioning bound.
// Every other role covers the volume's reviews directory with an empty filesystem of its own, as
// the roles directory is covered, so no role but the reviewer sees or writes a reviewer's clone. A
// role told no workspace, the controller, has none to ready.
func roleWorkspace(ctx context.Context, cfg RoleInitConfig) error {
	dir := os.Getenv("LEGION_WORKSPACE")
	if dir == "" {
		return nil
	}
	issue := workspace.IssueWorkspace(dir)
	switch {
	case cfg.Role == string(claim.RoleReviewer) && issue != dir:
		request, err := workspace.ReviewRequestFor(dir, os.Getenv("LEGION_ISSUE"), os.Getenv("LEGION_DEFAULT_BRANCH"), os.Getenv(workspace.ReviewHeadEnv))
		if err != nil {
			return err
		}
		request.Log = func(line string) { fmt.Fprintln(os.Stderr, "role-init: the reviewer's own clone: "+line) }
		tools := map[string]string{}
		for _, name := range []string{"git", "jj"} {
			if tools[name] = compiledOnPath(name); tools[name] == "" {
				return fmt.Errorf("the reviewer's own clone: no compiled %s on PATH (%s)", name, os.Getenv("PATH"))
			}
		}
		if err := workspace.ProvisionReviewClone(ctx, workspace.NewRunner(workspace.CommandTimeout, tools), request); err != nil {
			return fmt.Errorf("the reviewer's own clone: %w", err)
		}
	case cfg.Role != string(claim.RoleReviewer):
		// A workspace outside a state directory's layout has no reviews directory beside it, and
		// no reviewer's clone the layout could name.
		if root, err := workspace.ReviewRootOf(issue); err == nil {
			if err := coverReviews(root); err != nil {
				return err
			}
		}
	}
	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("start the role in its workspace: %w", err)
	}
	return nil
}

// coverReviews puts an empty filesystem of the role's own over root, the volume's reviews
// directory, making root first where no reviewer has yet. A link there is refused rather than
// followed: the cover would land on what it names and leave root itself uncovered.
func coverReviews(root string) error {
	if info, err := os.Lstat(root); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("cover %s: it is a link", root)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("cover %s: %w", root, err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("cover %s: %w", root, err)
	}
	if err := mountTmpfs(root, "size=1m,mode=0755"); err != nil {
		return fmt.Errorf("cover %s: %w", root, err)
	}
	return nil
}

// compiledOnPath is the first executable named name on PATH that is a compiled program
// (lookpath.Kind), "" when there is none: the image's own git and jj, behind its wrappers.
func compiledOnPath(name string) string {
	found, _ := lookpath.Others(os.Getenv("PATH"), name, "")
	for _, tool := range found {
		if _, compiled, err := lookpath.Kind(tool.Path); err == nil && compiled {
			return tool.Path
		}
	}
	return ""
}
