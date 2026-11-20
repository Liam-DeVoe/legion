package main

import (
	"errors"
	"regexp"
	"strings"
)

// jjPushRef is one line of a dry run's "Changes to push to <remote>:" block.
type jjPushRef struct {
	Remote string // the block's remote
	Tag    bool   // a tag line (`tag: <name> [...]`), not a bookmark line
	Name   string
	Action string // "add", "move forward", "move backward", "move sideways", "delete"
	From   string // the hex commit id the ref moves or is deleted from; "" for add
	To     string // the hex commit id the ref is added or moved to; "" for delete
}

// jjDryRun is what a dry run printed on standard error.
type jjDryRun struct {
	Refs    []jjPushRef // every line of every block, in order
	Nothing bool        // jj printed `Nothing changed.`
}

// The shapes of a line inside a block. jj 0.45 prints `  bookmark: <name> [<action>]`; jj 0.38
// prints `  <Action> bookmark <name> …`. Each name is everything between the words around it, so
// a name is read whole whatever it holds.
var (
	jjRefLine         = regexp.MustCompile(`^  (bookmark|tag): (.+) \[(.+)\]$`)
	jjRefAdd          = regexp.MustCompile(`^add to ([0-9a-f]+)$`)
	jjRefMove         = regexp.MustCompile(`^move (forward|backward|sideways) from ([0-9a-f]+) to ([0-9a-f]+)$`)
	jjRefDelete       = regexp.MustCompile(`^delete from ([0-9a-f]+)$`)
	jjOldRefAdd       = regexp.MustCompile(`^  Add (bookmark|tag) (.+) to ([0-9a-f]+)$`)
	jjOldRefMove      = regexp.MustCompile(`^  Move (forward|backward|sideways) (bookmark|tag) (.+) from ([0-9a-f]+) to ([0-9a-f]+)$`)
	jjOldRefDelete    = regexp.MustCompile(`^  Delete (bookmark|tag) (.+) from ([0-9a-f]+)$`)
	jjNoAuthorRefusal = regexp.MustCompile(`^Error: Won't push commit ([0-9a-f]+) since it has no author and/or committer set$`)
)

// parseJJDryRun reads a dry run's standard error, in either jj 0.45's shape
// (`  bookmark: <name> [add to <id>]`, `[move forward|backward|sideways from <a> to <b>]`,
// `[delete from <a>]`, `  tag: <name> [add to <id>]`) or jj 0.38's
// (`  Add bookmark <name> to <id>`, `  Move forward|sideways|backward bookmark <name> from <a> to <b>`,
// `  Delete bookmark <name> from <a>`, and the same with `tag`). Lines outside a block are passed
// over. The error names the first line inside a block that it could not place, or says the output
// has neither a block nor `Nothing changed.`.
func parseJJDryRun(stderr string) (jjDryRun, error) {
	var run jjDryRun
	remote, blocks := "", 0
	for line := range strings.SplitSeq(strings.TrimSuffix(stderr, "\n"), "\n") {
		if heading, ok := strings.CutPrefix(line, "Changes to push to "); ok && strings.HasSuffix(heading, ":") {
			remote = strings.TrimSuffix(heading, ":")
			blocks++
			continue
		}
		if remote == "" || !strings.HasPrefix(line, "  ") {
			remote = ""
			if line == "Nothing changed." {
				run.Nothing = true
			}
			continue
		}
		ref, ok := parseJJRefLine(line)
		if !ok {
			return jjDryRun{}, errors.New(strings.TrimSpace(line))
		}
		ref.Remote = remote
		run.Refs = append(run.Refs, ref)
	}
	if blocks == 0 && !run.Nothing {
		return jjDryRun{}, errors.New("the dry run printed neither a block of changes nor `Nothing changed.`")
	}
	return run, nil
}

// parseJJRefLine reads one line inside a block, in either release's shape.
func parseJJRefLine(line string) (jjPushRef, bool) {
	if parts := jjRefLine.FindStringSubmatch(line); parts != nil {
		ref := jjPushRef{Tag: parts[1] == "tag", Name: parts[2]}
		action := parts[3]
		if add := jjRefAdd.FindStringSubmatch(action); add != nil {
			ref.Action, ref.To = "add", add[1]
			return ref, true
		}
		if move := jjRefMove.FindStringSubmatch(action); move != nil {
			ref.Action, ref.From, ref.To = "move "+move[1], move[2], move[3]
			return ref, true
		}
		if deleted := jjRefDelete.FindStringSubmatch(action); deleted != nil {
			ref.Action, ref.From = "delete", deleted[1]
			return ref, true
		}
		return jjPushRef{}, false
	}
	if parts := jjOldRefAdd.FindStringSubmatch(line); parts != nil {
		return jjPushRef{Tag: parts[1] == "tag", Name: parts[2], Action: "add", To: parts[3]}, true
	}
	if parts := jjOldRefMove.FindStringSubmatch(line); parts != nil {
		return jjPushRef{Tag: parts[2] == "tag", Name: parts[3], Action: "move " + parts[1], From: parts[4], To: parts[5]}, true
	}
	if parts := jjOldRefDelete.FindStringSubmatch(line); parts != nil {
		return jjPushRef{Tag: parts[1] == "tag", Name: parts[2], Action: "delete", From: parts[3]}, true
	}
	return jjPushRef{}, false
}

// jjNoAuthorCommit is the commit id that jj's refusal
// `Error: Won't push commit <id> since it has no author and/or committer set` names, and line, that
// refusal's line, if stderr holds it.
func jjNoAuthorCommit(stderr string) (id, line string, ok bool) {
	for l := range strings.SplitSeq(stderr, "\n") {
		if parts := jjNoAuthorRefusal.FindStringSubmatch(l); parts != nil {
			return parts[1], l, true
		}
	}
	return "", "", false
}
