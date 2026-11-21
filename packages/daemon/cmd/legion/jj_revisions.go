package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// This file holds what the step (jjStep, jj.go) keeps of each revision argument of the push: the
// commits it names before any rewrite (readRevisions), and the words it reaches each dry run and
// the real push with (jjStep.words), which name exactly those commits, each one that a metaedit of
// the step replaced as the commit that replaced it.

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

// readRevisions reads each revision argument of the push with jj itself, before any rewrite:
// every commit it names (revisionSets, read by ids), the working-copy commit where it is one of
// them (working), and the commits of it that the step can rewrite (readRevision). A hidden commit
// the argument names is among them: jj's all() holds the hidden commits a revset names, and their
// ancestors. Each commit the step can rewrite is followed from that read, on its own (follow),
// with whether it is the working-copy commit. A hidden commit whose change has exactly one visible
// commit stands for that commit, and is the working-copy commit where that one is, as the commit
// id of @ is after a rewrite of @ hid it; one whose change has none, or more than one, stands for
// itself. An argument whose read jj refuses (a revset jj cannot parse or resolve, an ambiguous
// prefix, a commit that a git command made and that no jj command has imported yet) is followed by
// nothing, and goes to the dry run and the real push as the role wrote it (words): jj alone may
// still push such a commit, since the real push imports it, while none of the step's commands,
// which run with --ignore-working-copy, imports it or can read it. The dry run then fails as the
// read did, with jj's error, which the step cannot read (dryRunFilled).
func (s *jjStep) readRevisions() error {
	s.revisionSets = make([][]string, len(s.push.revisions))
	s.revisionCommits = make([][]*jjFollowed, len(s.push.revisions))
	var hidden []string
	for i, r := range s.push.revisions {
		set, working, err := s.ids(s.push.args[r.index][r.offset:])
		if err != nil {
			continue
		}
		if working != "" {
			s.working = working
		}
		commits, err := s.readRevision(r)
		if err != nil {
			return err
		}
		s.revisionSets[i] = set
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

// words is the push's words: each revision argument as the step gives it (revisionWord), and every
// other word as the role wrote it, every byte, each option's own spelling included. Without moved,
// a revision argument counts every followed commit that no longer stands for itself, whether
// readRevisions' reading of a hidden commit as its change's one visible commit or a metaedit of the
// step moved it: the words of each pass of the dry run (dryRunFilled), and the real push's after a
// step that ends without error. With moved, it counts only a commit a metaedit of the step moved
// (jjFollowed): the real push's words after a step that changes nothing more (errJJAsWritten),
// which are the role's own but for such a commit (the step's loop over a commit with no author can
// move one before a dry run it cannot read, and an earlier metaedit one before a metaedit that jj
// refuses). real is whether the words are the real push's, the only words that print the line of
// an argument that goes to the push unchecked (revisionWord).
func (s *jjStep) words(moved, real bool) []string {
	words := slices.Clone(s.push.args)
	for i, r := range s.push.revisions {
		words[r.index] = s.revisionWord(i, moved, real)
	}
	return words
}

// revisionWord is the word that holds revision argument i as the step gives it, with what stands
// in front of the argument in that word kept (the name of --named, or the option's own spelling
// where the value is attached to it). Its argument must name, as jj resolves it then, exactly the
// commits it sends (sends): each commit it named before any rewrite, or the commit that stands for
// one now that a counted followed commit no longer stands for (words' moved). It is:
//
//  1. the argument as the role wrote it, every byte, where no commit of it counts;
//  2. the same where jj resolves the argument as written now to exactly the commits it sends, as
//     jj moves `@`, a bookmark and a change id to the commit a metaedit made;
//  3. else the primary form, `(<argument>) ~ (<old>) ~ (<extra>) | <new> | <missing>`: old the ids
//     of the counted commits, new the commits that stand for them, extra the commits jj resolves
//     `(<argument>) ~ (<old>)` to that the argument does not send, and missing those it sends that
//     are neither among them nor new, each list joined by ` | `, `@` for the working-copy commit in
//     new and missing, and extra's and missing's parts left out where empty. Inside the form the
//     argument means what it meant in that read, so the form resolves to the commits it sends; it
//     goes to the push where jj resolves it so;
//  4. else, where jj resolves the form to other commits (a hidden commit written into it puts it
//     and its ancestors into all(), which can change what a filter of the argument means), or
//     refuses a read of 2 or 3, the commits it sends as their ids (jjExplicitIDs), where that word
//     is under jjWordBound bytes, else the primary form, where its word is, else the argument as
//     the role wrote it. The real push's words print one line that says which (unchecked), and the
//     push goes on.
//
// A read of 3 whose revset would not be under jjWordBound bytes is one the step does not run, as
// one jj refuses: the primary form, which holds that revset, would not be under the bound either.
// A change offset `<change>/<n>`, `latest()`, `heads()` or an author revset, which a rewrite of the
// step can make name other commits, thus names in the push the commits it named before the step.
func (s *jjStep) revisionWord(i int, moved, real bool) string {
	r := s.push.revisions[i]
	word := s.push.args[r.index]
	prefix, argument := word[:r.offset], word[r.offset:]
	var counted []*jjFollowed
	for _, f := range s.revisionCommits[i] {
		if f.commit != f.origin && (f.moved || !moved) {
			counted = append(counted, f)
		}
	}
	if len(counted) == 0 {
		return word
	}
	send := s.sends(i, counted)
	sent := make(map[string]bool, len(send))
	for _, c := range send {
		sent[c.id] = true
	}
	now, _, err := s.ids(argument)
	if err == nil && jjSameSet(now, sent) {
		return word
	}
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
	primary := less + " | " + strings.Join(added, " | ")
	var got []string
	resolved := false
	if err == nil && len(less) < jjWordBound {
		var left []string
		if left, _, err = s.ids(less); err == nil {
			primary = jjPrimary(less, left, added, send, sent, isNew)
			if len(primary) < jjWordBound {
				if got, _, err = s.ids(primary); err == nil {
					if jjSameSet(got, sent) {
						return prefix + primary
					}
					resolved = true
				}
			}
		}
	}
	ids := make([]string, len(send))
	for k, c := range send {
		ids[k] = c.word()
	}
	given, explicit := jjExplicitIDs(prefix, ids)
	formed := !explicit && len(prefix)+len(primary) < jjWordBound
	switch {
	case formed:
		given = prefix + primary
	case !explicit:
		given = word
	}
	if real {
		s.unchecked(argument, send, sent, explicit, formed, primary, got, resolved)
	}
	return given
}

// jjPrimary is the primary form of a revision argument (jjStep.revisionWord): less, the argument
// less its old commits, then ` ~ (<extra>)`, extra being each commit of left, the commits jj
// resolved less to, that send does not hold, then ` | <added>`, the commits that stand for the old
// ones, and ` | <missing>`, missing being each commit of send that neither left nor isNew holds,
// each list joined by ` | ` and each of extra's and missing's parts left out where it is empty.
// sent is send's ids.
func jjPrimary(less string, left, added []string, send []jjSent, sent, isNew map[string]bool) string {
	var extra, missing []string
	inLeft := make(map[string]bool, len(left))
	for _, id := range left {
		inLeft[id] = true
		if !sent[id] {
			extra = append(extra, id)
		}
	}
	for _, c := range send {
		if !inLeft[c.id] && !isNew[c.id] {
			missing = append(missing, c.word())
		}
	}
	primary := less
	if len(extra) > 0 {
		primary += " ~ (" + strings.Join(extra, " | ") + ")"
	}
	primary += " | " + strings.Join(added, " | ")
	if len(missing) > 0 {
		primary += " | " + strings.Join(missing, " | ")
	}
	return primary
}

// sends is the commits revision argument i sends after the step: each commit it named before any
// rewrite (revisionSets), or, for the origin of one of counted, the followed commits of it that no
// longer stand for themselves, the commit that stands for that one now, each once. The
// working-copy commit is one of counted's that stood for it after the step's read (jjFollowed's
// working), or, among the commits the argument named, the one the read of its whole set found to
// be it (working): no metaedit of the step moved that one, so it still is.
func (s *jjStep) sends(i int, counted []*jjFollowed) []jjSent {
	stands := make(map[string]*jjFollowed, len(counted))
	for _, f := range counted {
		stands[f.origin] = f
	}
	seen := map[string]bool{}
	var send []jjSent
	for _, id := range s.revisionSets[i] {
		c := jjSent{id: id, working: id == s.working}
		if f, ok := stands[id]; ok {
			c = jjSent{id: f.commit, working: f.working}
		}
		if !seen[c.id] {
			seen[c.id] = true
			send = append(send, c)
		}
	}
	return send
}

// unchecked prints the one line of a revision argument whose words are not ones jj resolved to
// send, the commits it sends, whose ids sent holds (jjStep.revisionWord): that the push gets them
// as their ids where explicit is true; else that the word of their ids would not be under
// jjWordBound bytes, and, where formed is true, the primary form the push gets instead, with the
// commits by which got, jj's resolution of that form, differs from send where resolved is true,
// and that jj did not resolve it where it is not; and else that the primary form's word would not
// be under the bound either, so the push gets the argument as the role wrote it.
func (s *jjStep) unchecked(argument string, send []jjSent, sent map[string]bool, explicit, formed bool, primary string, got []string, resolved bool) {
	switch {
	case explicit:
		fmt.Fprintf(s.stderr, "legion jj: revision `%s` names other commits after the step; the push gets the commits it named before the step, as the step left them, by their ids\n", argument)
		return
	case !formed:
		fmt.Fprintf(s.stderr, "legion jj: revision `%s` names other commits after the step, and neither the word of the ids of the %d commits it named before the step nor its primary form would be under %d bytes; the push gets it as written\n", argument, len(send), jjWordBound)
		return
	}
	differs := "which jj did not resolve"
	if resolved {
		var adds, leaves []string
		in := make(map[string]bool, len(got))
		for _, id := range got {
			in[id] = true
			if !sent[id] {
				adds = append(adds, id)
			}
		}
		for _, c := range send {
			if !in[c.id] {
				leaves = append(leaves, c.id)
			}
		}
		differs = "which adds " + jjCommitList(adds) + " and leaves out " + jjCommitList(leaves)
	}
	fmt.Fprintf(s.stderr, "legion jj: revision `%s` names other commits after the step, and the word of the ids of the %d commits it named before the step would not be under %d bytes; the push gets `%s`, %s\n", argument, len(send), jjWordBound, primary, differs)
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
