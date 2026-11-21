package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// This file holds what the step (jjStep, jj.go) keeps of each revision argument of the push: the
// commits it names before any rewrite (readRevisions), the words it reaches each dry run and the
// real push with (jjStep.given, jjStep.words), which name those commits as revisionGiven says, and
// whether the push's command would still be under jjArgMax bytes once the rewrite of setAuthors
// leaves each argument its words (checkArgMax).

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
// and costs one read: only sends, for an argument with a counted commit, and checkArgMax, for one
// with a followed commit, read the set, and each such commit is one the step can rewrite. Each
// commit the step can rewrite is followed from that read, on its own (follow), with whether it is
// the working-copy commit. A hidden commit whose change has exactly one visible commit stands for
// that commit, and is the working-copy commit where that one is, as the commit id of @ is after a
// rewrite of @ hid it; one whose change has none, or more than one, stands for itself. An argument
// whose read jj refuses (a revset jj cannot parse or resolve, an ambiguous prefix, a commit that a
// git command made and that no jj command has imported yet) is followed by nothing, and goes to
// the dry run and the real push as the role wrote it (given): jj alone may still push such a
// commit, since the real push imports it, while none of the step's commands, which run with
// --ignore-working-copy, imports it or can read it. The dry run then fails as the read did, with
// jj's error, which the step cannot read (dryRunFilled).
func (s *jjStep) readRevisions() error {
	s.revisionSets = make([][]jjSent, len(s.push.revisions))
	s.revisionCommits = make([][]*jjFollowed, len(s.push.revisions))
	var hidden []string
	for i := range s.push.revisions {
		_, argument := s.push.revision(i)
		commits, err := s.readRevision(argument)
		if err != nil {
			return err
		}
		if len(commits) == 0 {
			continue
		}
		ids, working, err := s.ids(argument)
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

// readRevision reads argument, a revision argument as the role wrote it, with jjCommitTemplate:
// the commits of `((<argument>) & mutable()) ~ ::remote_bookmarks()`, or none where jj refuses the
// read. Those are the commits of the argument that the step can rewrite: it rewrites no commit
// that a remote bookmark holds or that is under one (readSet, readFillable), and a metaedit moves
// only the commits it rewrites and their descendants, which no remote bookmark holds either.
// Leaving the rest out keeps small, for a broad argument such as `::<C>` on a pushed branch, both
// this read and what the step follows of it through each later read.
func (s *jjStep) readRevision(argument string) ([]jjCommitRead, error) {
	commits, err := s.read("((" + argument + ") & mutable()) ~ ::remote_bookmarks()")
	var failed jjStepFailure
	if errors.As(err, &failed) {
		return nil, nil
	}
	return commits, err
}

// jjGiven is a revision argument as the step gives it to a pass of the dry run or to the real push
// (revisionGiven): words, the words that hold it, each with what stands in front of the argument in
// its word (the name of --named, or the option's own spelling where the value is attached to it);
// ids, where those words give it as the ids of its commits, those ids, each a full commit id or
// `@`, and else nil; and line, the one line the step prints of it, or "" for none.
type jjGiven struct {
	words []string
	ids   []string
	line  string
}

// given is each revision argument as the step gives it to a pass (revisionGiven), by its place in
// push.revisions. Without moved, an argument counts every followed commit that no longer stands
// for itself, whether readRevisions' reading of a hidden commit as its change's one visible commit
// or a metaedit of the step moved it: the words of each pass of the dry run (dryRunFilled), and the
// real push's after a step that ends without error. With moved, it counts only a commit a metaedit
// of the step moved (jjFollowed): the real push's words after a step that changes nothing more
// (errJJAsWritten), which are the role's own but for such a commit (the step's loop over a commit
// with no author can move one before a dry run it cannot read or before checkArgMax, and an earlier
// metaedit one before a metaedit that jj refuses). Only the real push prints the lines (runJJ); a
// dry run drops them.
func (s *jjStep) given(moved bool) []jjGiven {
	given := make([]jjGiven, len(s.push.revisions))
	for i := range given {
		given[i] = s.revisionGiven(i, moved)
	}
	return given
}

// words is the push's words with each revision argument in the words that given gives it, and
// every other word as the role wrote it, every byte, each option's own spelling included; dry
// leaves out the words that stay on the real push alone (jjPush.pushOnly). An argument in more
// than one word repeats its option: each word of a value attached to its option (`-r<ids>`,
// `--revisions=<ids>`) carries the option as the role spelled it, and the word of the option
// stands again before each word after the first of a value that follows it (`-r <ids>`). jj takes
// -c and -r more than once and sends the union of what they name. --named names one commit, so
// its argument is never in more than one word.
func (s *jjStep) words(given []jjGiven, dry bool) []string {
	words := make([]string, 0, len(s.push.args))
	next := 0
	for j, word := range s.push.args {
		if dry && s.push.pushOnly[j] {
			continue
		}
		if next == len(s.push.revisions) || s.push.revisions[next].index != j {
			words = append(words, word)
			continue
		}
		for k, w := range given[next].words {
			if k > 0 && s.push.revisions[next].offset == 0 {
				words = append(words, s.push.args[j-1])
			}
			words = append(words, w)
		}
		next++
	}
	return words
}

// revisionGiven is revision argument i as the step gives it (jjGiven). Its words name, as jj
// resolves them then, exactly the commits the argument sends (sends): each commit it named before
// any rewrite, or the commit that stands for one now that a counted followed commit no longer
// stands for (given's moved). So each revision argument reaches each pass of the dry run and the
// real push naming exactly the commits it named before the step, each one a metaedit of the step
// replaced as the commit that replaced it, and nothing in the argument that a rewrite changes, a
// change offset `<change>/<n>`, `latest()`, `heads()` or an author revset, makes the push send a
// commit the role did not name. Its words are:
//
//  1. the argument as the role wrote it, every byte, where no commit of it counts;
//  2. the same where jj resolves the argument as written now to exactly the commits it sends, as
//     jj moves `@`, a bookmark and a change id to the commit a metaedit made;
//  3. else the primary form (checkedPrimary), where jj resolves it to exactly those commits and
//     its word is under jjWordBound bytes;
//  4. else, where jj resolves the form to other commits (a hidden commit written into it puts it
//     and its ancestors into all(), which can change what a filter of the argument means), or
//     refuses a read of 2 or 3, or the step runs no read of 3 (checkedPrimary), the commits it
//     sends as their ids, in as many words of its option as keep each under jjWordBound bytes
//     (jjWords, words), with a line that says so. Only the real push prints that line (runJJ),
//     and the push goes on.
//
// So no word the step writes in place of an argument is over jjWordBound bytes, and none names
// commits other than those the argument sends.
func (s *jjStep) revisionGiven(i int, moved bool) jjGiven {
	prefix, argument := s.push.revision(i)
	var counted []*jjFollowed
	for _, f := range s.revisionCommits[i] {
		if f.commit != f.origin && (f.moved || !moved) {
			counted = append(counted, f)
		}
	}
	if len(counted) == 0 {
		return s.written(i)
	}
	send, sendIDs := s.sends(i, counted)
	now, _, err := s.ids(argument)
	if err == nil && jjSameSet(now, sendIDs) {
		return s.written(i)
	}
	if primary, same := s.checkedPrimary(prefix, argument, err == nil, counted, send, sendIDs); same {
		return jjGiven{words: []string{prefix + primary}}
	}
	given := jjIDsGiven(prefix, send)
	given.line = fmt.Sprintf("legion jj: revision `%s` names other commits after the step; the push gets the commits it named before the step, as the step left them, by their ids", argument)
	if len(given.words) > 1 {
		given.line = fmt.Sprintf("legion jj: revision `%s` names other commits after the step; the push gets the %d commits it named before the step, as the step left them, by their ids, in %d words of its option, each under %d bytes", argument, len(given.ids), len(given.words), jjWordBound)
	}
	return given
}

// written is revision argument i as the role wrote it: its one word, every byte.
func (s *jjStep) written(i int) jjGiven {
	return jjGiven{words: []string{s.push.args[s.push.revisions[i].index]}}
}

// jjIDsGiven is a revision argument that gives the push sent, the commits it sends, as their ids
// (jjSent.word), in as many words of its option as keep each, prefix, what stands in front of the
// argument, then the ids joined by ` | `, under jjWordBound bytes (jjWords).
func jjIDsGiven(prefix string, sent []jjSent) jjGiven {
	ids := make([]string, len(sent))
	for k, c := range sent {
		ids[k] = c.word()
	}
	return jjGiven{words: jjWords(prefix, ids, ""), ids: ids}
}

// checkedPrimary is the primary form of revision argument argument (revisionGiven's 3),
// `(<argument>) ~ (<old>) ~ (<extra>) | <new> | <missing>` (jjPrimary): old the ids of counted's
// commits, new the commits that stand for them, extra the commits jj resolves less,
// `(<argument>) ~ (<old>)`, to that the argument does not send, and missing those it sends (send,
// whose ids sendIDs holds) that are neither among them nor new, `@` for the working-copy commit in
// new and missing. Inside the form the argument means what it meant in that read, so the form
// resolves to the commits it sends. same is whether jj resolves the form to exactly send's
// commits, the form's word, prefix and then the form, being under jjWordBound bytes. The step reads
// less only where readable, jj having resolved the argument as written now, and where the word of
// the form without that read, less then ` | <new>`, is under jjWordBound bytes: the form holds
// that one, so its word would not be under the bound either. It reads the form only where it read
// less and the form's word is under the bound.
func (s *jjStep) checkedPrimary(prefix, argument string, readable bool, counted []*jjFollowed, send []jjSent, sendIDs map[string]bool) (primary string, same bool) {
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
	if !readable || len(prefix)+len(primary) >= jjWordBound {
		return primary, false
	}
	left, _, err := s.ids(less)
	if err != nil {
		return primary, false
	}
	primary = jjPrimary(less, left, added, send, sendIDs, isNew)
	if len(prefix)+len(primary) >= jjWordBound {
		return primary, false
	}
	got, _, err := s.ids(primary)
	return primary, err == nil && jjSameSet(got, sendIDs)
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

// checkArgMax decides, before setAuthors rewrites rewrites, the current ids of the commits it
// would rewrite, whether the push can still run once each revision argument has the words the
// step would give it after that rewrite (revisionGiven). The step cannot know those words before
// the rewrite: an argument with a commit that the rewrite would move goes as the role wrote it
// where jj then resolves it to the commits it sends, else as its primary form where jj resolves
// that to them, and else as the ids of its commits, which a read that jj refuses, a check of the
// form that fails, or a commit the form would add, makes revisionGiven fall to. So the step counts
// such an argument at the largest of the three: the ids of every commit of its set (revisionSets,
// jjIDsGiven), each as long as the id that stands for it after the rewrite; one word of
// jjWordBound - 1 bytes, since revisionGiven gives the primary form only where its word is under
// jjWordBound bytes (checkedPrimary); and its word as the role wrote it, which may be longer than
// the bound. It counts every other argument as the role wrote it (largestGiven). Where the push's
// command counted so would not be under jjArgMax bytes (commandSize), the step prints one line
// that names the argument with the most ids, their number and the limit (asWritten), and answers
// errJJAsWritten: setAuthors rewrites nothing, and the push runs with the role's own words, but
// for an argument with a commit that the fill of a commit with no author moved (jjStep.given).
// Else it answers nil.
//
// The commits the rewrite would move of an argument are its followed commits (revisionCommits)
// that stand now for one of rewrites or for a descendant of one, which a metaedit moves: the
// commits of jj's `(<rewrites>)::`, read in as few commands as keep each revset under jjWordBound
// bytes (jjWords, eachID), among the commits of the argument's set that the step can rewrite, the
// only ones of that set a metaedit can move (readRevision); and each of its followed commits that
// no longer stands for itself already: a hidden commit that readRevisions reads as its change's
// one visible commit, or one that the fill moved (dryRunFilled). Those are among its followed
// commits, so the step reads no descendant where the push's command is under the limit with every
// argument that has a followed commit counted so: the answer would be nil all the same.
func (s *jjStep) checkArgMax(rewrites []string) error {
	asIDs := make([]jjGiven, len(s.push.revisions))
	for i, commits := range s.revisionCommits {
		if len(commits) > 0 {
			prefix, _ := s.push.revision(i)
			asIDs[i] = jjIDsGiven(prefix, s.revisionSets[i])
		}
	}
	size, widest := s.largestGiven(asIDs, func(*jjFollowed) bool { return true })
	if widest < 0 || size < jjArgMax {
		return nil
	}
	descendants, err := s.eachID(jjWords("(", jjCommitIDs(rewrites), ")::"))
	if err != nil {
		return err
	}
	moves := make(map[string]bool, len(descendants))
	for _, id := range descendants {
		moves[id] = true
	}
	size, widest = s.largestGiven(asIDs, func(f *jjFollowed) bool { return moves[f.commit] || f.commit != f.origin })
	if widest < 0 || size < jjArgMax {
		return nil
	}
	_, argument := s.push.revision(widest)
	return s.asWritten("set the author", fmt.Sprintf("revision `%s` would go to the push as the ids of its %d commits, and the push's arguments and environment would not be under %d bytes", argument, len(asIDs[widest].ids), jjArgMax))
}

// largestGiven is the bytes of the push's command (commandSize) with each revision argument as
// checkArgMax counts it, and widest, the place of the argument with the most ids among those with
// a followed commit that moves, as moves says, or -1 where there is none. Such an argument counts
// at the largest (givenSize, jjSize) of asIDs's words of it, one word of jjWordBound - 1 bytes,
// the longest word of a primary form (checkedPrimary), and its word as the role wrote it. Every
// other argument counts at its word as the role wrote it.
func (s *jjStep) largestGiven(asIDs []jjGiven, moves func(*jjFollowed) bool) (size, widest int) {
	written := make([]jjGiven, len(s.push.revisions))
	primary, extra := jjSize("")+jjWordBound-1, 0
	widest = -1
	for i, commits := range s.revisionCommits {
		written[i] = s.written(i)
		if !slices.ContainsFunc(commits, moves) {
			continue
		}
		own := s.givenSize(i, written[i])
		extra += max(s.givenSize(i, asIDs[i]), primary, own) - own
		if widest < 0 || len(asIDs[i].ids) > len(asIDs[widest].ids) {
			widest = i
		}
	}
	return s.commandSize(written) + extra, widest
}

// givenSize is the bytes that revision argument i, as g gives it, takes of the push's command
// (jjStep.words, jjSize): each of its words, and, where its value follows its option, that option's
// word again before each of them after the first.
func (s *jjStep) givenSize(i int, g jjGiven) int {
	size := jjSize(g.words...)
	if r := s.push.revisions[i]; r.offset == 0 {
		size += (len(g.words) - 1) * jjSize(s.push.args[r.index-1])
	}
	return size
}

// commandSize is the bytes that Linux counts against jjArgMax (jjSize) for the push with each
// revision argument as given gives it. The arguments are next and the push's words, with the dry
// run's flags (jjDryRunFlags), which its dry run has beside the same words, and the environment is
// the step's (env) with wrappedJJStepped set to a digest (jjStepped), which the real push has
// beside it; so each of the two commands counts that many bytes or fewer.
func (s *jjStep) commandSize(given []jjGiven) int {
	return jjSize(s.words(given, false)...) + jjSize(jjDryRunFlags...) + jjSize(s.env...) +
		jjSize(s.next, wrappedJJStepped+"=") + hex.EncodedLen(sha256.Size)
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
