package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// This file holds what the step (jjStep, jj.go) keeps of each revision argument of the push: the
// commits it names before any rewrite (readRevisions), and the word it reaches each dry run and
// the real push with (jjStep.words), which names those commits as revisionWord says; one the step
// prints a line for (revisionWord's fallback) may not.

// jjSent is one commit a revision argument sends after the step (jjStep.sends): its full id, and
// whether it is the working-copy commit.
type jjSent struct {
	id      string
	working bool
}

// word is the commit as a revision argument's words write it: `@` for the working-copy commit,
// which the real push's own snapshot resolves, since it may rewrite that commit once more, and its
// full id for any other.
func (c jjSent) word() string {
	if c.working {
		return "@"
	}
	return c.id
}

// readRevisions reads each revision argument of the push with jj itself, before any rewrite: the
// commits of it that the step can rewrite (readRevision), and, for an argument with such a commit,
// every commit it names (revisionSets, read by ids), each with whether it is the working-copy
// commit. A hidden commit the argument names is among them: jj's all() holds the hidden commits a
// revset names, and their ancestors. An argument with no commit the step can rewrite has no set,
// and costs one read: only sends reads the set, for an argument with a counted commit, which is one
// the step can rewrite. Each commit the step can rewrite is followed from that read, on its own
// (follow), with whether it is the working-copy commit. A hidden commit whose change has exactly
// one visible commit stands for that commit, and is the working-copy commit where that one is, as
// the commit id of @ is after a rewrite of @ hid it; one whose change has none, or more than one,
// stands for itself. An argument whose read jj refuses (a revset jj cannot parse or resolve, an
// ambiguous prefix, a commit that a git command made and that no jj command has imported yet) is
// followed by nothing, and goes to the dry run and the real push as the role wrote it (words): jj
// alone may still push such a commit, since the real push imports it, while none of the step's
// commands, which run with --ignore-working-copy, imports it or can read it. The dry run then
// fails as the read did, with jj's error, which the step cannot read (dryRunFilled).
func (s *jjStep) readRevisions() error {
	s.revisionSets = make([][]jjSent, len(s.push.revisions))
	s.revisionCommits = make([][]*jjFollowed, len(s.push.revisions))
	var hidden []string
	for i, r := range s.push.revisions {
		commits, err := s.readRevision(r)
		if err != nil {
			return err
		}
		if len(commits) == 0 {
			continue
		}
		ids, working, err := s.ids(s.push.args[r.index][r.offset:])
		if err != nil {
			continue
		}
		s.revisionSets[i] = make([]jjSent, len(ids))
		for k, id := range ids {
			s.revisionSets[i][k] = jjSent{id: id, working: id == working}
		}
		for _, c := range commits {
			s.revisionCommits[i] = append(s.revisionCommits[i], s.follow(c, true))
			if c.hidden && !slices.Contains(hidden, c.change) {
				hidden = append(hidden, c.change)
			}
		}
	}
	if len(hidden) == 0 {
		return nil
	}
	visible, err := s.visible(hidden)
	if err != nil {
		return err
	}
	for _, commits := range s.revisionCommits {
		for _, f := range commits {
			if now := visible[f.change]; f.hidden && len(now) == 1 {
				f.commit, f.hidden, f.working = now[0].commit, false, now[0].working
			}
		}
	}
	return nil
}

// readRevision reads revision argument r as the role wrote it, with jjCommitTemplate: the commits
// of `((<argument>) & mutable()) ~ ::remote_bookmarks()`, or none where jj refuses the read. Those
// are the commits of the argument that the step can rewrite: it rewrites no commit that a remote
// bookmark holds or that is under one (readSet, readFillable), and a metaedit moves only the
// commits it rewrites and their descendants, which no remote bookmark holds either. Leaving the
// rest out keeps small, for a broad argument such as `::<C>` on a pushed branch, both this read and
// what the step follows of it through each later read.
func (s *jjStep) readRevision(r jjRevision) ([]jjCommitRead, error) {
	commits, err := s.read("((" + s.push.args[r.index][r.offset:] + ") & mutable()) ~ ::remote_bookmarks()")
	var failed jjStepFailure
	if errors.As(err, &failed) {
		return nil, nil
	}
	return commits, err
}

// words is the push's words, and the lines the step prints of them: each revision argument as the
// step gives it (revisionWord), with its line where it has one, and every other word as the role
// wrote it, every byte, each option's own spelling included. Without moved, a revision argument
// counts every followed commit that no longer stands for itself, whether readRevisions' reading of
// a hidden commit as its change's one visible commit or a metaedit of the step moved it: the words
// of each pass of the dry run (dryRunFilled), and the real push's after a step that ends without
// error. With moved, it counts only a commit a metaedit of the step moved (jjFollowed): the real
// push's words after a step that changes nothing more (errJJAsWritten), which are the role's own
// but for such a commit (the step's loop over a commit with no author can move one before a dry
// run it cannot read, and an earlier metaedit one before a metaedit that jj refuses). Only the real
// push's words print their lines (runJJ); a dry run's drop them.
func (s *jjStep) words(moved bool) (words, lines []string) {
	words = slices.Clone(s.push.args)
	for i, r := range s.push.revisions {
		word, line := s.revisionWord(i, moved)
		words[r.index] = word
		if line != "" {
			lines = append(lines, line)
		}
	}
	return words, lines
}

// revisionWord is the word that holds revision argument i as the step gives it, with what stands
// in front of the argument in that word kept (the name of --named, or the option's own spelling
// where the value is attached to it), and line, the one line the step prints of it, or "" for
// none. The word names, as jj resolves it then, exactly the commits the argument sends (sends):
// each commit it named before any rewrite, or the commit that stands for one now that a counted
// followed commit no longer stands for (words' moved). So each revision argument reaches each pass
// of the dry run and the real push naming exactly the commits it named before the step, each one a
// metaedit of the step replaced as the commit that replaced it, and nothing in the argument that a
// rewrite changes, a change offset `<change>/<n>`, `latest()`, `heads()` or an author revset, makes
// the push send a commit the role did not name. Only two words of 4, each of which prints a line,
// may name other commits: the primary form that jj did not resolve to them, and the argument as
// the role wrote it. The word is:
//
//  1. the argument as the role wrote it, every byte, where no commit of it counts;
//  2. the same where jj resolves the argument as written now to exactly the commits it sends, as
//     jj moves `@`, a bookmark and a change id to the commit a metaedit made;
//  3. else the primary form (checkedPrimary), where jj resolves it to exactly those commits;
//  4. else, where jj resolves the form to other commits (a hidden commit written into it puts it
//     and its ancestors into all(), which can change what a filter of the argument means), or
//     refuses a read of 2 or 3, or the step runs no read of 3 (checkedPrimary), the commits it
//     sends as their ids (jjExplicitIDs), where that word is under jjWordBound bytes, else the
//     primary form, where its word is, else the argument as the role wrote it, with a line that
//     says which: for the primary form, with the commits by which jj's resolution of it differs
//     from those it sends, or that jj did not resolve it. Only the real push prints that line
//     (runJJ), and the push goes on.
func (s *jjStep) revisionWord(i int, moved bool) (word, line string) {
	r := s.push.revisions[i]
	word = s.push.args[r.index]
	prefix, argument := word[:r.offset], word[r.offset:]
	var counted []*jjFollowed
	for _, f := range s.revisionCommits[i] {
		if f.commit != f.origin && (f.moved || !moved) {
			counted = append(counted, f)
		}
	}
	if len(counted) == 0 {
		return word, ""
	}
	send, sendIDs := s.sends(i, counted)
	now, _, err := s.ids(argument)
	if err == nil && jjSameSet(now, sendIDs) {
		return word, ""
	}
	primary, got, resolved, same := s.checkedPrimary(argument, err == nil, counted, send, sendIDs)
	if same {
		return prefix + primary, ""
	}
	ids := make([]string, len(send))
	for k, c := range send {
		ids[k] = c.word()
	}
	given, explicit := jjExplicitIDs(prefix, ids)
	switch {
	case explicit:
		return given, fmt.Sprintf("legion jj: revision `%s` names other commits after the step; the push gets the commits it named before the step, as the step left them, by their ids", argument)
	case len(prefix)+len(primary) < jjWordBound:
		differs := "which jj did not resolve"
		if resolved {
			in := make(map[string]bool, len(got))
			for _, id := range got {
				in[id] = true
			}
			var leaves []string
			for _, c := range send {
				if !in[c.id] {
					leaves = append(leaves, c.id)
				}
			}
			differs = "which adds " + jjCommitList(jjNotIn(got, sendIDs)) + " and leaves out " + jjCommitList(leaves)
		}
		return prefix + primary, fmt.Sprintf("legion jj: revision `%s` names other commits after the step, and the word of the ids of the %d commits it named before the step would not be under %d bytes; the push gets `%s`, %s", argument, len(send), jjWordBound, primary, differs)
	default:
		return word, fmt.Sprintf("legion jj: revision `%s` names other commits after the step, and neither the word of the ids of the %d commits it named before the step nor its primary form would be under %d bytes; the push gets it as written", argument, len(send), jjWordBound)
	}
}

// checkedPrimary is the primary form of revision argument argument (revisionWord's 3),
// `(<argument>) ~ (<old>) ~ (<extra>) | <new> | <missing>` (jjPrimary): old the ids of counted's
// commits, new the commits that stand for them, extra the commits jj resolves less,
// `(<argument>) ~ (<old>)`, to that the argument does not send, and missing those it sends (send,
// whose ids sendIDs holds) that are neither among them nor new, `@` for the working-copy commit in
// new and missing. Inside the form the argument means what it meant in that read, so the form
// resolves to the commits it sends. got is the commits jj resolves the form to, where resolved,
// and same is whether those are exactly send's. The step reads less only where readable, jj having
// resolved the argument as written now, and where less is under jjWordBound bytes: the form holds
// less, so it would not be under the bound either. It reads the form only where it read less and
// the form is under the bound. A read it does not run leaves the form unresolved, as one jj
// refuses. Without the read of less, the form is less, then ` | <new>`.
func (s *jjStep) checkedPrimary(argument string, readable bool, counted []*jjFollowed, send []jjSent, sendIDs map[string]bool) (primary string, got []string, resolved, same bool) {
	var old, added []string
	isNew := map[string]bool{}
	for _, f := range counted {
		old = append(old, f.origin)
		if !isNew[f.commit] {
			isNew[f.commit] = true
			added = append(added, jjSent{id: f.commit, working: f.working}.word())
		}
	}
	less := "(" + argument + ") ~ (" + strings.Join(old, " | ") + ")"
	primary = less + " | " + strings.Join(added, " | ")
	if !readable || len(less) >= jjWordBound {
		return primary, nil, false, false
	}
	left, _, err := s.ids(less)
	if err != nil {
		return primary, nil, false, false
	}
	primary = jjPrimary(less, left, added, send, sendIDs, isNew)
	if len(primary) >= jjWordBound {
		return primary, nil, false, false
	}
	if got, _, err = s.ids(primary); err != nil {
		return primary, nil, false, false
	}
	return primary, got, true, jjSameSet(got, sendIDs)
}

// jjPrimary is the primary form of a revision argument (checkedPrimary): less, the argument less
// its old commits, then ` ~ (<extra>)`, extra being each commit of left, the commits jj resolved
// less to, that send does not hold, then ` | <added>`, the commits that stand for the old ones, and
// ` | <missing>`, missing being each commit of send that neither left nor isNew holds, each list
// joined by ` | ` and each of extra's and missing's parts left out where it is empty. sendIDs is
// send's ids.
func jjPrimary(less string, left, added []string, send []jjSent, sendIDs, isNew map[string]bool) string {
	inLeft := make(map[string]bool, len(left))
	for _, id := range left {
		inLeft[id] = true
	}
	var missing []string
	for _, c := range send {
		if !inLeft[c.id] && !isNew[c.id] {
			missing = append(missing, c.word())
		}
	}
	primary := less
	if extra := jjNotIn(left, sendIDs); len(extra) > 0 {
		primary += " ~ (" + strings.Join(extra, " | ") + ")"
	}
	primary += " | " + strings.Join(added, " | ")
	if len(missing) > 0 {
		primary += " | " + strings.Join(missing, " | ")
	}
	return primary
}

// jjNotIn is each of ids that set does not hold, in order.
func jjNotIn(ids []string, set map[string]bool) []string {
	var out []string
	for _, id := range ids {
		if !set[id] {
			out = append(out, id)
		}
	}
	return out
}

// sends is the commits revision argument i sends after the step, send, and their ids, sendIDs:
// each commit it named before any rewrite (revisionSets), or, for the origin of one of counted, the
// followed commits of it that no longer stand for themselves, the commit that stands for that one
// now, each once. The working-copy commit is one of counted's that stood for it after the step's
// read (jjFollowed's working), or, among the commits the argument named, the one the read of its
// whole set found to be it (revisionSets): no metaedit of the step moved that one, so it still is.
func (s *jjStep) sends(i int, counted []*jjFollowed) (send []jjSent, sendIDs map[string]bool) {
	stands := make(map[string]*jjFollowed, len(counted))
	for _, f := range counted {
		stands[f.origin] = f
	}
	sendIDs = make(map[string]bool, len(s.revisionSets[i]))
	for _, c := range s.revisionSets[i] {
		if f, ok := stands[c.id]; ok {
			c = jjSent{id: f.commit, working: f.working}
		}
		if !sendIDs[c.id] {
			sendIDs[c.id] = true
			send = append(send, c)
		}
	}
	return send, sendIDs
}

// jjCommitList is ids joined by `, `, or `no commit` where there is none.
func jjCommitList(ids []string) string {
	if len(ids) == 0 {
		return "no commit"
	}
	return strings.Join(ids, ", ")
}

// jjExplicitIDs is the word of a revision argument that gives the push its commits as their ids:
// prefix, what stands in front of the argument in its word, then ids, each a full commit id or
// `@`, joined by ` | `. ok is whether that word is under jjWordBound bytes; where it is not,
// there is no word, and the step builds none.
func jjExplicitIDs(prefix string, ids []string) (word string, ok bool) {
	size := len(prefix) + len(" | ")*max(len(ids)-1, 0)
	for _, id := range ids {
		size += len(id)
	}
	if size >= jjWordBound {
		return "", false
	}
	return prefix + strings.Join(ids, " | "), true
}

// jjSameSet is whether ids, commit ids each once as jj prints them, are exactly the ids of set.
func jjSameSet(ids []string, set map[string]bool) bool {
	if len(ids) != len(set) {
		return false
	}
	for _, id := range ids {
		if !set[id] {
			return false
		}
	}
	return true
}

// ids reads the commits of revset in one command: each one's full id, once, in jj's order, and
// the id of the one that is the working-copy commit of the workspace the command runs in
// (current_working_copy, which jj reads from the operation, --ignore-working-copy or not), or ""
// where none is. A read jj refuses, or one that cannot start, is a jjStepFailure, its one error.
func (s *jjStep) ids(revset string) (ids []string, working string, err error) {
	out, err := s.jj(s.env, "--color=never", "log", "--no-graph", "-r", revset, "-T", `commit_id ++ if(current_working_copy, " @") ++ "\n"`)
	if err != nil {
		return nil, "", err
	}
	for line := range strings.Lines(out) {
		id, mark, _ := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
		if id == "" {
			continue
		}
		ids = append(ids, id)
		if mark == "@" {
			working = id
		}
	}
	return ids, working, nil
}
