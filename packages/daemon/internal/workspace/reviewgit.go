package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/sjawhar/legion/daemon/internal/runtime/shellprefix"
)

// A reviewer's git runs no program its repository or another role supplies: no diff
// driver's command or text conversion, no hook and no fsmonitor command. Each of those is a
// program a git configuration names, and a head reaches one in two ways. A repository the head
// commits below its root (a directory holding HEAD, objects/ and refs/, which git takes for a
// repository of its own) carries a configuration, attributes and hooks that every git run below
// that directory reads as its own. And a run of another role — the implementer's or the
// tester's, of the head's own code — writes the shared clone's configuration and hooks and the
// configuration files the roles of a machine share. The head's .gitattributes alone runs
// nothing: it names a driver, whose program only a configuration can name.
//
// So the reviewer's git reads its own clone, which no other role works in (ProvisionReviewClone),
// no global or system configuration (GIT_CONFIG_GLOBAL=/dev/null, GIT_CONFIG_NOSYSTEM=1), and
// core.fsmonitor off and core.hooksPath at /dev/null after every file, through GIT_CONFIG_COUNT
// (ReviewGitEnv) and, for the git the reviewer runs itself, as `-c` before the subcommand, after
// the caller's own: git takes the last `-c` of a name. /dev/null is the hooks directory the rest of
// the daemon pins (pinnedGitConfig): no hook is ever found under it, and no role can put one
// there. A diff, a log, a show, a format-patch, a whatchanged, a blame and an annotate run with
// --no-ext-diff and --no-textconv, which no configuration can take back: every one but
// format-patch runs a driver's command or its text conversion by default (measured with git 2.43:
// diff runs the command, and log, show, whatchanged, blame and annotate the text conversion), and
// format-patch takes both flags too. A git that deprecates whatchanged (git 2.56.1 does) runs it
// only with --i-still-use-this, which git reads wherever it stands, so a reviewer that passes it
// keeps both flags in front of it; the reviewer's git adds no such option itself, and a bare
// whatchanged gets git's own refusal. A caller's own --ext-diff or --textconv after them would take
// them back, so the reviewer's git refuses a command of those that names either. On the tmux
// runtime this is the shim InstallReviewGit writes, first on the reviewer's PATH; in a pod the
// image's git wrapper (packages/daemon/docker/git), first on every role's PATH, for
// LEGION_ROLE=reviewer.
//
// What this leaves open: a git alias, which only a configuration defines and which neither the
// shim nor the wrapper reads, and a program git runs for some other setting (a filter, a pager,
// a credential helper, a difftool) that a repository the head commits names. On the tmux runtime
// every pane runs as the daemon's own user, so any role can rewrite the shim; there this is
// defence, not a boundary (config.go).

// reviewDiffCommands are the subcommands the reviewer's git runs with --no-ext-diff and
// --no-textconv: every one that runs a diff driver's command or text conversion by default, and
// format-patch. The image's git wrapper (packages/daemon/docker/git) holds the same list.
var reviewDiffCommands = []string{"diff", "log", "show", "format-patch", "whatchanged", "blame", "annotate"}

// ReviewGitConfig is the configuration a reviewer's git reads after every file: the fsmonitor
// off, and the hooks at /dev/null.
func ReviewGitConfig() [][2]string {
	return [][2]string{{"core.fsmonitor", "false"}, {"core.hooksPath", "/dev/null"}}
}

// ReviewGitEnv is a reviewer's git environment, as NAME=value entries: no global and no system
// configuration (isolatedGitConfig), and pairs as GIT_CONFIG_COUNT, GIT_CONFIG_KEY_<n> and
// GIT_CONFIG_VALUE_<n>, which git reads after every configuration file, as it reads `-c`. A
// runtime that sets pairs of its own for every process passes them first and ReviewGitConfig
// last, so the reviewer's win.
func ReviewGitEnv(pairs [][2]string) []string {
	env := append(slices.Clone(isolatedGitConfig), "GIT_CONFIG_COUNT="+strconv.Itoa(len(pairs)))
	for i, pair := range pairs {
		index := strconv.Itoa(i)
		env = append(env, "GIT_CONFIG_KEY_"+index+"="+pair[0], "GIT_CONFIG_VALUE_"+index+"="+pair[1])
	}
	return env
}

// ReviewGitDir is where a tmux daemon installs its reviewers' git (InstallReviewGit), under its
// state directory.
func ReviewGitDir(stateDir string) string { return filepath.Join(stateDir, "review-git") }

// ReviewGit is a tmux reviewer's git: Bin, the directory whose git is the shim InstallReviewGit
// wrote.
type ReviewGit struct {
	Bin string
}

// InstallReviewGit writes the reviewer's git shim at dir/bin/git and answers it. Every reviewer
// launch of the daemon runs it, so a shim a reviewer edited is gone at the next launch. The shim
// is written to a temporary name and renamed over the old one, so a launch beside a running
// reviewer never leaves that reviewer half a script.
func InstallReviewGit(dir string) (ReviewGit, error) {
	git := ReviewGit{Bin: filepath.Join(dir, "bin")}
	if err := os.MkdirAll(git.Bin, 0o700); err != nil {
		return ReviewGit{}, fmt.Errorf("make the reviewer's git directory: %w", err)
	}
	staged, err := os.CreateTemp(git.Bin, ".git-*")
	if err != nil {
		return ReviewGit{}, fmt.Errorf("write the reviewer's git: %w", err)
	}
	defer func() { _ = os.Remove(staged.Name()) }()
	_, err = staged.WriteString(reviewGitScript(git.Bin))
	if err == nil {
		err = staged.Chmod(0o700)
	}
	if closeErr := staged.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(staged.Name(), filepath.Join(git.Bin, "git"))
	}
	if err != nil {
		return ReviewGit{}, fmt.Errorf("write the reviewer's git: %w", err)
	}
	return git, nil
}

// Env is a tmux reviewer's environment for its git: PATH, which is path with the shim's directory
// first, exactly once, and ReviewGitEnv of base and then ReviewGitConfig.
func (g ReviewGit) Env(path string, base [][2]string) map[string]string {
	kept := slices.DeleteFunc(filepath.SplitList(path), func(dir string) bool { return dir == g.Bin })
	env := map[string]string{"PATH": strings.Join(append([]string{g.Bin}, kept...), string(filepath.ListSeparator))}
	for _, entry := range ReviewGitEnv(slices.Concat(base, ReviewGitConfig())) {
		name, value, _ := strings.Cut(entry, "=")
		env[name] = value
	}
	return env
}

// reviewGitScript is the shim: it runs the first git on PATH after bin, the directory it is in,
// with `-c core.fsmonitor=false -c core.hooksPath=/dev/null` after the caller's own global options
// and before the subcommand, and with --no-ext-diff and --no-textconv after the subcommand when it
// is one of reviewDiffCommands, refusing such a command that names --ext-diff or --textconv itself
// before a `--`. It finds the subcommand as the image's git wrapper does
// (packages/daemon/docker/git), and like that wrapper it rebuilds only the words in front of the
// subcommand: a POSIX shell that rebuilt the whole list would take time growing with the square
// of its length, and a leak check's `git rev-list` is thousands of words long. Only an absolute
// entry of PATH is read, so a git in the directory a command runs in — the head's checkout — is
// never the one it runs.
func reviewGitScript(bin string) string {
	return `#!/bin/sh
# A reviewer's git on the tmux runtime, written by the Legion daemon
# (packages/daemon/internal/workspace/reviewgit.go, which says what it is for).
set -u
bin=` + shellprefix.Literal(bin) + `

real=
after=0
IFS=:
set -f
for dir in $PATH; do
  case $dir in
    "$bin") after=1; continue ;;
    /*) ;;
    *) continue ;;
  esac
  if [ "$after" -eq 1 ] && [ -f "$dir/git" ] && [ -x "$dir/git" ]; then
    real=$dir/git
    break
  fi
done
set +f
unset IFS
if [ -z "$real" ]; then
  echo "legion: no git on PATH after $bin, the reviewer's git that runs it" >&2
  exit 127
fi

value=0
ahead=0
sub=
for arg; do
  if [ "$value" -eq 1 ]; then
    value=0
  else
    case $arg in
      -C | -c | --git-dir | --work-tree | --namespace | --config-env | --attr-source | --shallow-file) value=1 ;;
      --help | -h | --version | -v | --html-path | --man-path | --info-path) break ;;
      --exec-path=*) ;;
      --exec-path*) break ;;
      -*) ;;
      *) sub=$arg; break ;;
    esac
  fi
  ahead=$((ahead + 1))
done
if [ -z "$sub" ]; then
  exec "$real" "$@"
fi

front=
while [ "$ahead" -gt 0 ]; do
  word=$1
  shift
  ahead=$((ahead - 1))
  quoted=
  while :; do
    case $word in
      *\'*)
        quoted=$quoted${word%%\'*}\'\\\'\'
        word=${word#*\'}
        ;;
      *) break ;;
    esac
  done
  front="$front '$quoted$word'"
done
shift
flags=
case $sub in
  ` + strings.Join(reviewDiffCommands, " | ") + `)
    flags='--no-ext-diff --no-textconv'
    for arg; do
      case $arg in
        --) break ;;
        --ext-diff | --textconv)
          echo "legion: a reviewer's git runs no diff driver: refusing $sub $arg" >&2
          exit 2
          ;;
      esac
    done
    ;;
esac
eval "set -- $front"' -c core.fsmonitor=false -c core.hooksPath=/dev/null "$sub" '"$flags"' "$@"'
exec "$real" "$@"
`
}
