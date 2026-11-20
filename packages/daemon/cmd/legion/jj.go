package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"syscall"

	legionclaim "github.com/sjawhar/legion/daemon/internal/claim" // main_test.go's `claim` helper holds the bare name
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// jjUsage is the help of `legion jj`, which reads its own arguments (passThrough), --help among
// them.
const jjUsage = `usage: legion jj --real <jj> -- <jj argument>…
       legion jj --after <directory> -- <jj argument>…

A jj wrapper runs this command, never a person. It runs a jj with the arguments after --.
Before a jj git push, it first sets the author of each commit the push sends from that
commit's first Legion-Role trailer: the identity LEGION_ROLE_IDENTITIES gives that role, by
one jj metaedit --update-author for each role. It prints one line for each commit it rewrote.

  --real <jj>          run <jj>, the real jj (the pod's form: /usr/local/bin/jj)
  --after <directory>  run the next jj on PATH after <directory>, the wrapper's own directory
                       (the form of the jj first on a tmux pane's PATH)
`

// wrappedJJAt is legion jj's loop guard's variable, as wrappedGitAt is legion git's: a chain of
// jj programs (wrapperChain). Each run of `legion jj` sets it in the environment of every jj it
// runs, the step's own commands and the jj it replaces itself with: to the chain pickAfter gives
// (--after), or to the real jj alone (--real). A legion jj that finds it set is not the first of
// its chain, and runs no step: a host's jj wrapper that runs jj by PATH came back into the pane's
// jj, a controller started from a pane has two Legion bin directories on PATH, or a command of
// the step came back. Its name starts with neither LEGION_ nor JJ_: fixtureenv drops both
// prefixes, and jj reads JJ_ variables of its own.
const wrappedJJAt = "WRAPPED_JJ_AT"

// runJJ is `legion jj`. Both of Legion's jj wrappers run it: the pod's /opt/legion/bin/jj
// (packages/daemon/docker/jj) hands it each `jj git push` with --real /usr/local/bin/jj after its
// two setup checks, and the jj first on a tmux pane's PATH (workerbin.Install's bin/jj) hands it
// every command with --after and its own directory. It reads its arguments and puts back the
// signals' actions as runGit does (startWrapper), chooses the jj to run (pickAfter for --after),
// runs the author step before a push (jjStep), and replaces itself with that jj (execWrapped), so
// a stop of the process group and its budgets reach jj as before. A command that is no push, a
// push the step leaves alone (readJJPush), a run that finds wrappedJJAt set, and a process with
// no table (runtime.RoleIdentitiesVariable unset, as a controller's is) run that jj at once, with
// nothing printed and no other process started. A table that is set but cannot be used
// (runtime.ReadRoleIdentities) prints one line, and then runs the same. After a step that
// changes nothing more (errJJAsWritten), past a dry run it cannot read or a metaedit that jj
// refuses because the commit it would make exists already (jjStep.metaedit), the push gets the
// role's own words, but for a commit id whose commit a metaedit of the step moved
// (jjPush.withRevisions).
//
// An interrupt or a terminate ends legion jj where it stands. The step's own commands are
// children in this process's group, so a signal sent to the group ends them too, and the push
// never starts.
func runJJ(ctx context.Context, args []string, _, stderr io.Writer) int {
	if status, runs := startWrapper(ctx, "jj", jjUsage, args, stderr); !runs {
		return status
	}
	jjArgs := args[3:]
	marker, chained := os.LookupEnv(wrappedJJAt)
	next, chain := args[1], wrapperChain{ran: []string{args[1]}}
	if args[0] == "--after" {
		pick, err := pickAfter("jj", args[1], os.Getenv("PATH"), marker)
		if err != nil {
			fmt.Fprintf(stderr, "legion jj: %v\n", err)
			return 127
		}
		next, chain = pick.run, pick.chain
	}
	env := withVariable(os.Environ(), wrappedJJAt, chain.String())
	push := readJJPush(jjArgs)
	value, set := os.LookupEnv(runtime.RoleIdentitiesVariable)
	if chained || !push.steps() || !set {
		return execWrapped("legion jj", next, jjArgs, env, nil, stderr)
	}
	table, why := runtime.ReadRoleIdentities(value)
	if why != "" {
		fmt.Fprintf(stderr, "legion jj: %s is %s; the push runs with no author step\n", runtime.RoleIdentitiesVariable, why)
		return execWrapped("legion jj", next, jjArgs, env, nil, stderr)
	}
	step := &jjStep{push: push, next: next, env: env, table: table, stderr: stderr}
	err := step.rewrite()
	step.report()
	var failed jjStepFailure
	switch {
	case errors.As(err, &failed):
		fmt.Fprint(stderr, failed.output)
		return failed.code
	case err != nil && !errors.Is(err, errJJAsWritten):
		fmt.Fprintf(stderr, "legion jj: %v\n", err)
		return 1
	}
	return execWrapped("legion jj", next, push.withRevisions(step.ids, errors.Is(err, errJJAsWritten)), env, nil, stderr)
}

// jjPush is a jj command line as legion jj reads it (readJJPush).
type jjPush struct {
	args []string
	// push is whether the command's subcommand pair is `git push`.
	push bool
	// leave is whether a push runs no step: one that sends nothing (--dry-run, -h, --help), or
	// one at an earlier operation (--at-op, --at-operation).
	leave bool
	// options are the words of the caller's -R/--repository, --config, --config-file and
	// --config-toml, each with its value, in the order written: what every command of the step
	// carries first, so it reads the repository and the configuration the push reads.
	options []string
	// pushOnly are the indexes of the words that stay on the real push alone: --quiet, --debug,
	// --no-integrate-operation, --ignore-immutable, and --color with its value.
	pushOnly map[int]bool
	// revisions are the push's revision arguments, in order.
	revisions []jjRevision
}

// jjRevision is one revision argument of a push: the value of -c/--change or
// -r/--revision/--revisions, or the revision of --named <name>=<revision>, which is the word at
// index from offset on.
type jjRevision struct{ index, offset int }

// jjGlobalValues are jj's global options whose value may be the next word: the list of the pod's
// wrapper (packages/daemon/docker/jj), which finds the subcommand pair past them.
var jjGlobalValues = []string{"-R", "--repository", "--at-operation", "--at-op", "--color", "--config", "--config-file", "--config-toml"}

// jjPushValues are jj's global options and `jj git push`'s own whose value may be the next word.
var jjPushValues = slices.Concat(jjGlobalValues, []string{"--remote", "-b", "--bookmark", "-t", "--tag", "-r", "--revision", "--revisions", "-c", "--change", "--named", "-o", "--option"})

// readJJPush reads args, a jj command line. It finds the subcommand pair as the pod's wrapper
// (packages/daemon/docker/jj) finds it: the first two words that are no option and no option's
// value, jj's global options being the ones that take one. Past the pair it reads `git push`'s
// options too. An option takes its value as the next word, after `=` (`--change=X`, `-c=X`), or
// attached to a short name (`-cX`), as clap reads each.
func readJJPush(args []string) jjPush {
	p := jjPush{args: args, pushOnly: map[int]bool{}}
	var pair []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		values := jjGlobalValues
		if len(pair) == 2 {
			values = jjPushValues
		}
		name, offset, option := jjOption(arg, values)
		if !option {
			switch arg {
			case "--dry-run", "-h", "--help":
				p.leave = true
			case "--quiet", "--debug", "--no-integrate-operation", "--ignore-immutable":
				p.pushOnly[i] = true
			default:
				if !strings.HasPrefix(arg, "-") && len(pair) < 2 {
					pair = append(pair, arg)
				}
			}
			continue
		}
		first, at := i, i
		if offset < 0 {
			if i+1 == len(args) {
				break
			}
			i++
			at, offset = i, 0
		}
		switch name {
		case "-R", "--repository", "--config", "--config-file", "--config-toml":
			p.options = append(p.options, args[first:at+1]...)
		case "--at-operation", "--at-op":
			p.leave = true
		case "--color":
			for j := first; j <= at; j++ {
				p.pushOnly[j] = true
			}
		case "-c", "--change", "-r", "--revision", "--revisions":
			p.revisions = append(p.revisions, jjRevision{index: at, offset: offset})
		case "--named":
			if eq := strings.IndexByte(args[at][offset:], '='); eq >= 0 {
				p.revisions = append(p.revisions, jjRevision{index: at, offset: offset + eq + 1})
			}
		}
	}
	p.push = len(pair) == 2 && pair[0] == "git" && pair[1] == "push"
	return p
}

// jjOption reads arg as one of the options among values, by its name: the name, and the offset
// in arg of the value it holds itself (`--name=value`, `-Xvalue`, `-X=value`), or -1 when its
// value is the next word.
func jjOption(arg string, values []string) (name string, offset int, ok bool) {
	for _, value := range values {
		switch {
		case arg == value:
			return value, -1, true
		case strings.HasPrefix(value, "--"):
			if strings.HasPrefix(arg, value+"=") {
				return value, len(value) + 1, true
			}
		case strings.HasPrefix(arg, value):
			if arg[len(value)] == '=' {
				return value, len(value) + 1, true
			}
			return value, len(value), true
		}
	}
	return "", 0, false
}

// steps is whether the command is a push the step runs before.
func (p jjPush) steps() bool { return p.push && !p.leave }

// withRevisions is the command's words with each commit-id revision argument whose commit no
// longer stands for itself replaced by the commit that does (jjFollowed), in its own spelling:
// the dry run's words before dryRun adds to them, and the real push's after a dry run the step
// read. With moved, it replaces only an argument whose commit a metaedit of the step moved
// (jjFollowed): the real push's words after a step that changes nothing more (errJJAsWritten),
// which are the role's own but for such a commit (the step's loop over a commit with no author
// can move one before a dry run it cannot read, and an earlier metaedit one before a metaedit
// that jj refuses).
func (p jjPush) withRevisions(ids map[int]*jjFollowed, moved bool) []string {
	words := slices.Clone(p.args)
	for _, r := range p.revisions {
		if f := ids[r.index]; f != nil && f.commit != f.origin && (f.moved || !moved) {
			words[r.index] = words[r.index][:r.offset] + f.commit
		}
	}
	return words
}

// dryRun is the dry run of the push: its own words with ids' commits in place, without the
// words that stay on the real push alone, then --dry-run, --ignore-working-copy, --color=never,
// and `--config ui.quiet=false` as the last configuration, so that neither a flag nor the
// repository's configuration silences it.
func (p jjPush) dryRun(ids map[int]*jjFollowed) []string {
	var words []string
	for i, word := range p.withRevisions(ids, false) {
		if !p.pushOnly[i] {
			words = append(words, word)
		}
	}
	return append(words, "--dry-run", "--ignore-working-copy", "--color=never", "--config", "ui.quiet=false")
}

// jjStep is the step that runs before a push: it reads what the push sends from its dry run, and
// sets the author of each of those commits whose first Legion-Role trailer names a role of the
// table, where it is not that role's identity, by one jj metaedit --update-author for each role.
// Each of its jj commands runs next, one after another, as a child in this process's group, with
// the caller's repository and configuration options (jjPush.options) and --ignore-working-copy,
// so it takes no snapshot.
type jjStep struct {
	push   jjPush
	next   string   // the jj each command of the step runs, and the push after them
	env    []string // the environment of each, the loop guard's variable set
	table  map[legionclaim.Role]runtime.GitIdentity
	stderr io.Writer
	// ids are the commit-id revision arguments, by the index of the word that holds each: what
	// the commit each names stands for now.
	ids map[int]*jjFollowed
	// followed is each commit the step follows through its rewrites (advance).
	followed []*jjFollowed
	// rewrites are the commits a metaedit of the step rewrote or found rewritten already, in
	// order.
	rewrites []jjRewrite
}

// jjFollowed is a commit the step follows through each of its metaedits: origin, the commit id
// the step read, and commit, the commit that stands for it now, of change; hidden is whether that
// commit is hidden now. arg is whether it is a revision argument's, and moved whether a metaedit
// of the step moved it to another commit, the one that replaced it (advance) or the one a rewrite
// before the step made of it already (existing), apart from readIDs' reading of a hidden commit
// as its change's one visible commit.
type jjFollowed struct {
	origin, commit, change string
	hidden, arg, moved     bool
}

// jjRewrite is a commit a metaedit of the step rewrote under role's identity, or found rewritten
// so already (existing).
type jjRewrite struct {
	role   legionclaim.Role
	commit *jjFollowed
}

// jjStepFailure is a command of the step that failed: legion jj ends with code, after output,
// what the command printed on standard error, and runs no push. Two go on past one: readIDs, past
// the failure of its read of a revision argument, and metaedit, past jj's refusal to make a
// commit that exists already (jjCommitExists) and the failure of its read of that commit.
type jjStepFailure struct {
	code   int
	output string
}

func (f jjStepFailure) Error() string { return fmt.Sprintf("a command of the step exited %d", f.code) }

// errJJAsWritten is a step that has printed its one line of what it could not do and changes
// nothing more: read what the push sends (unreadable), or set an author where jj refuses to make
// a commit that exists already (metaedit). The push then runs as the role wrote it, but for a
// commit id whose commit a metaedit of the step moved (jjPush.withRevisions).
var errJJAsWritten = errors.New("the step changes nothing more; the push runs as written")

// rewrite runs the step: the hidden-id read (readIDs), the dry run and the loop over a commit with
// no author (dryRunFilled), and the rewrite of every other author (setAuthors). An error is a
// jjStepFailure, a step that changes nothing more (errJJAsWritten), or output of jj's it cannot
// parse.
func (s *jjStep) rewrite() error {
	if err := s.readIDs(); err != nil {
		return err
	}
	heads, err := s.dryRunFilled()
	if err != nil {
		return err
	}
	return s.setAuthors(heads)
}

// readIDs reads each revision argument that is lower-case hex: it is a commit id when the
// one commit it resolves to has an id that starts with it, and is followed from that commit. A
// hidden commit whose change has exactly one visible commit stands for that commit, in the dry
// run and in the real push. One whose change has none, or more than one, stands for itself. So
// does an argument whose read fails: jj alone may still push it, as it pushes a commit that a git
// command made and that no jj command has imported yet. The step's commands run with
// --ignore-working-copy, so none of them imports it from git or can read it, and the real push
// imports it. The dry run then fails as the read did, with jj's error, which the step cannot
// read (dryRunFilled).
func (s *jjStep) readIDs() error {
	s.ids = map[int]*jjFollowed{}
	for _, r := range s.push.revisions {
		word := s.push.args[r.index][r.offset:]
		if !isLowerHex(word) {
			continue
		}
		commits, err := s.read(word)
		var failed jjStepFailure
		if errors.As(err, &failed) {
			continue
		}
		if err != nil {
			return err
		}
		if len(commits) != 1 || !strings.HasPrefix(commits[0].commit, word) {
			continue
		}
		f := s.follow(commits[0], true)
		if f.hidden {
			visible, err := s.visible([]string{f.change})
			if err != nil {
				return err
			}
			if now := visible[f.change]; len(now) == 1 {
				f.commit, f.hidden = now[0], false
			}
		}
		s.ids[r.index] = f
	}
	return nil
}

// dryRunFilled is what the push sends, read from its dry run: the full ids of the commits it adds
// or moves a ref to (parseJJDryRun, fullHeads). A dry run that jj refuses because a commit it
// sends has no author (jjNoAuthorCommit) gets that commit filled, by the metaedit of the role its
// trailer names, and runs again. jj prints each commit id short, as its first 12 hex digits, and
// the step never hands jj such an id to resolve: jj would resolve it as a prefix over every commit
// of the repository, hidden ones and other issues' included, and fail where a second commit starts
// with it. Each pass reads instead, with jjCommitTemplate, the commits the step can fill
// (fillable), and takes the refused commit for the one commit of that read whose id starts with
// the refusal's. A refused commit that the step can fill, one the push sends that is mutable and
// that no remote holds, is in that read, so a single match is that commit. One it cannot fill,
// immutable or held by another remote, is not, and a commit of the read is taken for it only where
// the two ids share their first 12 digits. The loop has a bound it can prove: a dry run that names
// a commit an earlier pass already named or made, or a pass beyond the number of commits the first
// pass read, ends it as a dry run the step cannot read. So does a refusal whose id starts the id
// of no commit of the read, or of more than one, a commit whose trailer names no role of the
// table, and any other failure of the dry run.
func (s *jjStep) dryRunFilled() ([]string, error) {
	var named, made []string
	bound, passes := -1, 0
	for {
		_, stderr, code, err := s.command(s.env, s.push.dryRun(s.ids))
		if err != nil {
			return nil, err
		}
		if code == 0 {
			heads, err := parseJJDryRun(stderr)
			if err != nil {
				return nil, s.unreadable(err.Error())
			}
			return s.fullHeads(heads)
		}
		id, refusal, ok := jjNoAuthorCommit(stderr)
		if !ok {
			return nil, s.unreadable(jjErrorLine(stderr, code))
		}
		fillable, err := s.read(s.fillable())
		if err != nil {
			return nil, err
		}
		if bound < 0 {
			bound = len(fillable)
		}
		matches := jjStartingWith(fillable, func(c jjCommitRead) string { return c.commit }, id)
		if len(matches) != 1 || passes >= bound {
			return nil, s.unreadable(refusal)
		}
		c := matches[0]
		if slices.Contains(named, c.commit) || slices.Contains(made, c.commit) {
			return nil, s.unreadable(refusal)
		}
		if _, ok := s.table[c.role]; !ok {
			return nil, s.unreadable(refusal)
		}
		passes++
		named = append(named, c.commit)
		f := s.follow(c, false)
		if err := s.metaedit(c.role, []*jjFollowed{f}); err != nil {
			return nil, err
		}
		if f.commit != f.origin {
			made = append(made, f.commit)
		}
	}
}

// fillable is the revset of the commits the step can fill: `mutable() ~ ::remote_bookmarks()`,
// and, where the push has revision arguments, every mutable ancestor of the commits they name as
// its dry run names them (revisionTerms), each of those commits among them, that no remote
// bookmark holds, hidden ones included: jj's all() holds a hidden commit that a revset names, and
// its ancestors, and a commit the push sends is visible or such an ancestor.
func (s *jjStep) fillable() string {
	terms := s.revisionTerms()
	if len(terms) == 0 {
		return "mutable() ~ ::remote_bookmarks()"
	}
	return "(mutable() | (::(" + strings.Join(terms, " | ") + ") & mutable())) ~ ::remote_bookmarks()"
}

// fullHeads is the full commit id of each of heads, the ids a dry run prints short. It reads, in
// one command, the ids of the commits of `bookmarks() | tags()` and of each revision argument as
// the dry run names it (revisionTerms): every ref a push adds or moves goes to a local bookmark's
// or a tag's target or to a commit a revision argument names, so the commit a head stands for is
// in that read, and is the one commit of it whose id starts with the head. A head that starts the
// id of no commit of the read, or of more than one, makes the dry run one the step cannot read.
func (s *jjStep) fullHeads(heads []string) ([]string, error) {
	if len(heads) == 0 {
		return nil, nil
	}
	out, err := s.jj(s.env, "--color=never", "log", "--no-graph", "-r", strings.Join(append([]string{"bookmarks()", "tags()"}, s.revisionTerms()...), " | "), "-T", `commit_id ++ "\n"`)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(out)
	full := make([]string, len(heads))
	for i, head := range heads {
		matches := jjStartingWith(ids, func(id string) string { return id }, head)
		if len(matches) != 1 {
			return nil, s.unreadable(fmt.Sprintf("the dry run's commit id %s starts the id of %d commits that a bookmark, a tag or a revision of the push names, not of one", head, len(matches)))
		}
		full[i] = matches[0]
	}
	return full, nil
}

// revisionTerms is each revision argument of the push as its dry run names it (jjPush.dryRun),
// in parentheses, so that each is one term of a revset.
func (s *jjStep) revisionTerms() []string {
	words := s.push.withRevisions(s.ids, false)
	terms := make([]string, len(s.push.revisions))
	for i, r := range s.push.revisions {
		terms[i] = "(" + words[r.index][r.offset:] + ")"
	}
	return terms
}

// jjStartingWith is each of items whose commit id, as id reads it, starts with short.
func jjStartingWith[T any](items []T, id func(T) string, short string) []T {
	var matches []T
	for _, item := range items {
		if strings.HasPrefix(id(item), short) {
			matches = append(matches, item)
		}
	}
	return matches
}

// setAuthors rewrites the author of each commit the push sends: the set
// `(::(<each of heads>) ~ ::remote_bookmarks()) & mutable()`, heads being the full ids of the dry
// run's new commits (fullHeads), every commit of which whose role is in the table and whose
// author's email is not that role's. It runs one metaedit for each role with such commits, in
// claim.Roles' order, and reads the set again before each metaedit after its first, from what the
// dry run's new commits stand for after the one before. Nothing to rewrite runs no metaedit.
func (s *jjStep) setAuthors(heads []string) error {
	if len(heads) == 0 {
		return nil
	}
	set, err := s.readSet(heads)
	if err != nil {
		return err
	}
	// A new commit outside the set is immutable or held by a remote, and so is every commit under
	// it: no metaedit of the step reaches it, and the set is read again without it.
	var tips []*jjFollowed
	for _, head := range heads {
		if i := slices.IndexFunc(set, func(c jjSetCommit) bool { return c.commit.commit == head }); i >= 0 {
			tips = append(tips, set[i].commit)
		}
	}
	roles := map[legionclaim.Role]bool{}
	for _, c := range set {
		roles[c.role] = roles[c.role] || s.misauthored(c)
	}
	first := true
	for _, role := range legionclaim.Roles {
		if !roles[role] {
			continue
		}
		if !first {
			var now []string
			for _, tip := range tips {
				now = append(now, tip.commit)
			}
			if set, err = s.readSet(now); err != nil {
				return err
			}
		}
		var commits []*jjFollowed
		for _, c := range set {
			if c.role == role && s.misauthored(c) {
				commits = append(commits, c.commit)
			}
		}
		if len(commits) == 0 {
			continue
		}
		if err := s.metaedit(role, commits); err != nil {
			return err
		}
		first = false
	}
	return nil
}

// jjSetCommit is one commit of the set the push sends, as the step follows it.
type jjSetCommit struct {
	commit *jjFollowed
	email  string
	role   legionclaim.Role
}

// misauthored is whether c's trailer names a role of the table whose email is not c's author's.
func (s *jjStep) misauthored(c jjSetCommit) bool {
	identity, ok := s.table[c.role]
	return ok && c.email != identity.Email
}

// readSet reads the set of commits the push sends whose new commits are heads, full commit ids,
// each of which commit_id() names exactly, each commit followed (follow).
func (s *jjStep) readSet(heads []string) ([]jjSetCommit, error) {
	named := make([]string, len(heads))
	for i, head := range heads {
		named[i] = "commit_id(" + head + ")"
	}
	commits, err := s.read("(::(" + strings.Join(named, " | ") + ") ~ ::remote_bookmarks()) & mutable()")
	if err != nil {
		return nil, err
	}
	set := make([]jjSetCommit, len(commits))
	for i, c := range commits {
		set[i] = jjSetCommit{commit: s.follow(c, false), email: c.email, role: c.role}
	}
	return set, nil
}

// metaedit runs `jj metaedit --update-author` over commits under role's identity: the caller's
// options first, then that identity as the command's last --config user.name and user.email,
// each a TOML string (workspace.TOMLString), since a --config of the push's outranks JJ_USER and
// JJ_EMAIL, which are set to the same. Then it follows every followed commit through it
// (advance), and records each of commits as one role rewrote. A commit's timestamps are whole
// seconds, so the commit the metaedit would make of one of commits can exist already: an earlier
// rewrite of that commit in the same second made it, a refused push's step among them, and a
// `jj undo` or a `jj op restore` may have hidden it since. jj then refuses the whole metaedit and
// changes nothing (jjExistingCommit). Where that commit can stand for one of commits (existing),
// the step records that one's rewrite, and the metaedit runs again over the rest of commits, if
// any: each pass has one commit fewer, so the passes end. Otherwise the step prints one line that
// names jj's error, and answers errJJAsWritten. Any other failure is answered as it is: a
// jjStepFailure runs no push.
func (s *jjStep) metaedit(role legionclaim.Role, commits []*jjFollowed) error {
	identity := s.table[role]
	env := withVariable(withVariable(slices.Clone(s.env), "JJ_USER", identity.Name), "JJ_EMAIL", identity.Email)
	for len(commits) > 0 {
		in := map[string]bool{}
		var hidden []string
		args := []string{"metaedit", "--update-author"}
		for _, c := range commits {
			in[c.commit] = true
			args = append(args, "-r", c.commit)
			if c.hidden && !slices.Contains(hidden, c.change) {
				hidden = append(hidden, c.change)
			}
		}
		// A hidden commit's change can already have a visible commit that replaced it, which
		// names it in jj evolog as the commit the metaedit makes will: the one visible before is
		// not this step's.
		var before map[string][]string
		if len(hidden) > 0 {
			var err error
			if before, err = s.visible(hidden); err != nil {
				return err
			}
		}
		args = append(args, "--config", "user.name="+workspace.TOMLString(identity.Name), "--config", "user.email="+workspace.TOMLString(identity.Email))
		_, err := s.jj(env, args...)
		made, line, exists := jjExistingCommit(err)
		if !exists {
			if err != nil {
				return err
			}
			for _, c := range commits {
				s.rewrites = append(s.rewrites, jjRewrite{role: role, commit: c})
			}
			return s.advance(in, before)
		}
		taken, err := s.existing(made, commits)
		if err != nil {
			return err
		}
		if taken == nil {
			fmt.Fprintf(s.stderr, "legion jj: could not set the author: %s; the push runs as written\n", line)
			return errJJAsWritten
		}
		s.rewrites = append(s.rewrites, jjRewrite{role: role, commit: taken})
		commits = slices.DeleteFunc(commits, func(c *jjFollowed) bool { return c == taken })
	}
	return nil
}

// jjCommitExists is the line of jj's refusal to make a commit that exists already that names
// the commit, `Newly-created commit <id> already exists`, which jj 0.38 and 0.45 alike print as
// the cause of `Internal error: Unexpected error from backend`. Its group is the commit id.
var jjCommitExists = regexp.MustCompile(`Newly-created commit ([0-9a-f]+) already exists`)

// jjExistingCommit reads err, the failure of a command of the step (jjStepFailure), as jj's
// refusal to make a commit that exists already (jjCommitExists): ok is whether it is one, id the
// commit id it names, and line the words of it that name that commit.
func jjExistingCommit(err error) (id, line string, ok bool) {
	var failed jjStepFailure
	if !errors.As(err, &failed) {
		return "", "", false
	}
	match := jjCommitExists.FindStringSubmatch(failed.output)
	if match == nil {
		return "", "", false
	}
	return match[1], match[0], true
}

// existing is the one of commits that made stands for, made being a commit id that jj refused
// to make again because a commit has it already: the one commit of commits that is of made's
// change, where that one is hidden and no other of commits is. made is then the commit the
// metaedit would have made of it, and a hidden commit has no visible descendant for the refused
// metaedit to rebase; only another hidden commit of commits could descend from it, and the next
// pass would rewrite that one onto the old commit, not onto made. Every followed commit that
// stands for that one stands for made now, and has moved, hidden as made is. It is nil where made
// is of no change of commits, of the change of more than one, or of a visible one's, whose
// bookmarks and descendants only the refused metaedit would have moved onto made, where another
// of commits is hidden, and where the step cannot read made.
func (s *jjStep) existing(made string, commits []*jjFollowed) (*jjFollowed, error) {
	read, err := s.read("commit_id(" + made + ")")
	var failed jjStepFailure
	if errors.As(err, &failed) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(read) != 1 || read[0].commit != made {
		return nil, nil
	}
	var taken *jjFollowed
	for _, c := range commits {
		if c.change != read[0].change {
			continue
		}
		if taken != nil {
			return nil, nil
		}
		taken = c
	}
	if taken == nil || !taken.hidden || slices.ContainsFunc(commits, func(c *jjFollowed) bool { return c != taken && c.hidden }) {
		return nil, nil
	}
	old := taken.commit
	for _, f := range s.followed {
		if f.commit == old {
			f.commit, f.hidden, f.moved = made, read[0].hidden, true
		}
	}
	return taken, nil
}

// advance follows each followed commit through the metaedit that just ran over in: a
// commit that metaedit hid, itself or by rebasing it, stands now for the visible commit that
// replaced it (successor), and has moved. A commit that was hidden already and that the metaedit
// did not name stays as it is. before is what s.visible read of the changes of in's hidden
// commits ahead of the metaedit.
func (s *jjStep) advance(in map[string]bool, before map[string][]string) error {
	var moving []*jjFollowed
	var changes []string
	for _, f := range s.followed {
		if f.hidden && !in[f.commit] {
			continue
		}
		moving = append(moving, f)
		if !slices.Contains(changes, f.change) {
			changes = append(changes, f.change)
		}
	}
	if len(moving) == 0 {
		return nil
	}
	visible, err := s.visible(changes)
	if err != nil {
		return err
	}
	successors := map[string]string{}
	for _, f := range moving {
		now := visible[f.change]
		if slices.Contains(now, f.commit) {
			f.hidden = false
			continue
		}
		next, known := successors[f.commit]
		if !known {
			if next, err = s.successor(f.commit, now, before[f.change]); err != nil {
				return err
			}
			successors[f.commit] = next
		}
		if next == "" {
			f.hidden = true
			continue
		}
		f.commit, f.hidden, f.moved = next, false, true
	}
	return nil
}

// successor is the commit that replaced old, which is hidden now, among visible, the visible
// commits of old's change: the one there is, or, where the change is divergent, the one whose
// `jj evolog` names old second. Where several do, it is the one the step's own metaedit made, the
// one not among before. It is "" when no single commit replaced old.
func (s *jjStep) successor(old string, visible, before []string) (string, error) {
	switch len(visible) {
	case 0:
		return "", nil
	case 1:
		return visible[0], nil
	}
	var named []string
	for _, commit := range visible {
		out, err := s.jj(s.env, "--color=never", "evolog", "-r", commit, "--no-graph", "-n", "2", "-T", `commit.commit_id() ++ "\0"`)
		if err != nil {
			return "", err
		}
		if entries := strings.Split(out, "\x00"); len(entries) > 1 && entries[1] == old {
			named = append(named, commit)
		}
	}
	if len(named) > 1 {
		named = slices.DeleteFunc(named, func(commit string) bool { return slices.Contains(before, commit) })
	}
	if len(named) == 1 {
		return named[0], nil
	}
	return "", nil
}

// follow is the followed commit that c is now, or a new one that starts at c. A revision
// argument's (arg) is always new: it stands for what the role named, not a commit the step read.
func (s *jjStep) follow(c jjCommitRead, arg bool) *jjFollowed {
	if !arg {
		if i := slices.IndexFunc(s.followed, func(f *jjFollowed) bool { return !f.arg && f.commit == c.commit }); i >= 0 {
			return s.followed[i]
		}
	}
	f := &jjFollowed{origin: c.commit, commit: c.commit, change: c.change, hidden: c.hidden, arg: arg}
	s.followed = append(s.followed, f)
	return f
}

// visible is the visible commits of each of changes, by change id.
func (s *jjStep) visible(changes []string) (map[string][]string, error) {
	named := make([]string, len(changes))
	for i, change := range changes {
		named[i] = "change_id(" + change + ")"
	}
	commits, err := s.read(strings.Join(named, " | "))
	if err != nil {
		return nil, err
	}
	visible := map[string][]string{}
	for _, c := range commits {
		if !c.hidden {
			visible[c.change] = append(visible[c.change], c.commit)
		}
	}
	return visible, nil
}

// report prints one line for each commit a metaedit of the step rewrote or found rewritten
// already (existing), with the id the step read and the id of the commit that stands for it after
// every metaedit of the step. A commit that the step changed not at all, or that stands for a
// hidden commit, prints nothing.
func (s *jjStep) report() {
	for _, r := range s.rewrites {
		if f := r.commit; !f.hidden && f.commit != f.origin {
			fmt.Fprintf(s.stderr, "legion jj: %s: %s -> %s\n", r.role, f.origin, f.commit)
		}
	}
}

// unreadable prints the one line of a dry run the step cannot read, naming line, what of it the
// step could not read: its first line that the step could not read or act on, jj's refusal of a
// commit with no author that the step cannot fill among them (dryRunFilled), or a commit id of a
// block that the step could not place among the commits it read (fullHeads), and answers
// errJJAsWritten: the push then runs with the role's own words, but for a commit id whose commit
// a metaedit of the step moved (jjPush.withRevisions).
func (s *jjStep) unreadable(line string) error {
	fmt.Fprintf(s.stderr, "legion jj: could not read what the push sends: %s; the push runs as written\n", line)
	return errJJAsWritten
}

// jjCommitTemplate prints, for each commit, its change id, its commit id, whether it is hidden,
// its author's email and the value of its first Legion-Role trailer, each ended by NUL, which
// none of them holds.
const jjCommitTemplate = `change_id ++ "\0" ++ commit_id ++ "\0" ++ hidden ++ "\0" ++ author.email() ++ "\0" ++ trailers.filter(|t| t.key() == "Legion-Role").map(|t| t.value()).join("\n") ++ "\0"`

// jjCommitRead is one commit as jjCommitTemplate prints it.
type jjCommitRead struct {
	change, commit string
	hidden         bool
	email          string
	role           legionclaim.Role
}

// read is each commit of revset, read with jjCommitTemplate.
func (s *jjStep) read(revset string) ([]jjCommitRead, error) {
	out, err := s.jj(s.env, "--color=never", "log", "--no-graph", "-r", revset, "-T", jjCommitTemplate)
	if err != nil {
		return nil, err
	}
	fields := strings.Split(out, "\x00")
	if len(fields)%5 != 1 || fields[len(fields)-1] != "" {
		return nil, fmt.Errorf("jj log -r %s printed %q, which is not one record of five fields for each commit", revset, out)
	}
	commits := make([]jjCommitRead, 0, len(fields)/5)
	for i := 0; i+5 < len(fields); i += 5 {
		role, _, _ := strings.Cut(fields[i+4], "\n")
		commits = append(commits, jjCommitRead{
			change: fields[i], commit: fields[i+1], hidden: fields[i+2] == "true", email: fields[i+3], role: legionclaim.Role(role),
		})
	}
	return commits, nil
}

// jj runs one jj command of the step and answers what it printed on standard output: next, with
// the caller's options (jjPush.options), then --ignore-working-copy, then args, in env. A command
// that cannot start or exits non-zero is a jjStepFailure, with what it printed on standard error.
func (s *jjStep) jj(env []string, args ...string) (string, error) {
	stdout, stderr, code, err := s.command(env, slices.Concat(s.push.options, []string{"--ignore-working-copy"}, args))
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", jjStepFailure{code: code, output: stderr}
	}
	return stdout, nil
}

// command runs next with args in env and waits for it, as a child in this process's group, with
// no standard input. It answers what the command printed and its status, a death by a signal
// being 128 and the signal's number, as a shell reads it. A file the kernel refuses to run runs
// with /bin/sh, as execWrapped runs it. A command that cannot start is a jjStepFailure whose
// status is execWrapped's for the same failure.
func (s *jjStep) command(env, args []string) (stdout, stderr string, code int, err error) {
	var out, errOut bytes.Buffer
	run := func(path string, argv []string) error {
		out.Reset()
		errOut.Reset()
		cmd := &exec.Cmd{Path: path, Args: argv, Env: env, Stdout: &out, Stderr: &errOut}
		return cmd.Run()
	}
	err = run(s.next, append([]string{s.next}, args...))
	if errors.Is(err, syscall.ENOEXEC) {
		err = run("/bin/sh", append([]string{"/bin/sh", s.next}, args...))
	}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			code = 128 + int(status.Signal())
		}
		return out.String(), errOut.String(), code, nil
	case err != nil:
		code = 126
		if errors.Is(err, syscall.ENOENT) {
			code = 127
		}
		return "", "", 0, jjStepFailure{code: code, output: fmt.Sprintf("legion jj: run %s: %v\n", s.next, err)}
	}
	return out.String(), errOut.String(), 0, nil
}

// jjErrorLine is the line the step names when it cannot read a dry run that exited code: the
// first line of output that starts with `Error: `, jj's error, which may follow a notice such as
// the `Creating bookmark push-<change> for revision <change>` of `jj git push -c`; else the first
// line that is not blank, or what stands for it when there is none.
func jjErrorLine(output string, code int) string {
	first := ""
	for line := range strings.SplitSeq(output, "\n") {
		if strings.HasPrefix(line, "Error: ") {
			return strings.TrimSpace(line)
		}
		if first == "" {
			first = strings.TrimSpace(line)
		}
	}
	if first != "" {
		return first
	}
	return fmt.Sprintf("the dry run exited %d and printed nothing", code)
}

// isLowerHex is whether word is lower-case hex, as a commit id or its prefix is.
func isLowerHex(word string) bool {
	return word != "" && strings.Trim(word, "0123456789abcdef") == ""
}
