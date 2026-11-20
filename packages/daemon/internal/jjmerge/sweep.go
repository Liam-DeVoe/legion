package jjmerge

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// runID is what follows the workspace's prefix in a run's prefix: the run's random id and a
// dash. Only a run prefix of this shape is one of this workspace's runs, so a workspace whose
// name is another's name and a dash and more never matches here.
var runID = regexp.MustCompile(`^[0-9a-f]{16}-$`)

// carrierBookmark is the name of a carrier's bookmark: its run's prefix, then the carrier's role.
var carrierBookmark = regexp.MustCompile(`^(.+-)(?:` + strings.Join(carrierRoles[:], "|") + `)$`)

// removeLeftovers removes what a run in this workspace that was stopped before its end left: its
// carriers, with their bookmarks, and the commits that carry its line, jj's merge of the carriers
// and the merge. The lock keeps every other run of this workspace from going on now, so none of
// these is a live run's. Another workspace's are left alone, a run still going among them. A
// killed run's commit is otherwise a visible head that an idle release saves into the next pod
// and that a shared tmux clone keeps for every issue, and a merge it left holds jj's own merge of
// the parents.
func (m *merger) removeLeftovers(ctx context.Context) error {
	// A workspace that is not colocated sees a git ref only once it imports it, and a run killed
	// after it wrote its refs never imported them.
	if _, err := m.jj(ctx, "git", "import"); err != nil {
		return err
	}
	if err := m.remove(ctx, m.leftover, ""); err != nil {
		return fmt.Errorf("remove what an earlier merge in this workspace left: %w", err)
	}
	return nil
}

// leftover is whether run is the prefix of a run in this workspace, this run's included.
func (m *merger) leftover(run string) bool {
	rest, ok := strings.CutPrefix(run, m.prefix)
	return ok && runID.MatchString(rest)
}

// own is whether run is this run's prefix.
func (m *merger) own(run string) bool {
	return run == m.runPrefix
}

// remove abandons the commits of the runs ours matches: each carrier that a carrier bookmark of
// such a run names, each mutable merge that carries the line of such a run, and merge when it
// names one. It abandons those commits alone, never what stands on them. A member is kept with
// its bookmarks, and the run names it on stderr (keep), when a commit outside them stands on it
// (a role's commit on a carrier or on a merge a killed run left), when it is any workspace's
// working-copy commit, when a bookmark other than a carrier bookmark of such a run names it, or
// when a remote bookmark holds it: an abandon would move that commit onto other parents, delete
// that bookmark, or hide a commit that a remote holds. A bookmark of a carrier's name on a commit
// that is no carrier (notCarriers), a commit of the repository, makes that commit no member: the
// commit, the bookmark and its git ref stay where they are, and the run names the bookmark on
// stderr. One abandon takes the commits and their bookmarks together, so a run killed after it
// still finds each carrier by its bookmark. Then remove deletes each bookmark of such a run that
// jj still holds with no commit, and each git ref of such a run under refs/heads but a kept
// bookmark's. ours is asked only of the run prefixes of names under bookmarkPrefix and of the
// lines the commits carry. The bookmarks go through jj, which exports their removal in a
// colocated workspace; deleting a git ref under jj is only for a ref jj never imported or does not
// export.
func (m *merger) remove(ctx context.Context, ours func(run string) bool, merge string) error {
	out, err := m.jj(ctx, "bookmark", "list", "-T", `if(!remote, name ++ "\0" ++ if(normal_target, normal_target.commit_id()) ++ "\0")`, "glob:"+bookmarkPrefix+"*")
	if err != nil {
		return err
	}
	listed := nulFields(out)
	if len(listed)%2 != 0 {
		return fmt.Errorf("jj bookmark list printed %d fields, not name and target pairs: %q", len(listed), listed)
	}
	// targets are the commits the bookmarks of such runs name, each once. named holds each
	// target's bookmarks, which stay where the target stays. swept is every bookmark of such runs.
	var targets, untargeted, swept []string
	named := map[string][]string{}
	for i := 0; i < len(listed); i += 2 {
		name, target := listed[i], listed[i+1]
		if run, ok := bookmarkRun(name); !ok || !ours(run) {
			continue
		}
		swept = append(swept, name)
		if target == "" {
			untargeted = append(untargeted, name)
			continue
		}
		if _, ok := named[target]; !ok {
			targets = append(targets, target)
		}
		named[target] = append(named[target], name)
	}
	others, err := m.notCarriers(ctx, targets)
	if err != nil {
		return err
	}
	// members are the commits to abandon, each once: commit ids, and merge's change id.
	members := slices.DeleteFunc(slices.Clone(targets), func(id string) bool { return slices.Contains(others, id) })
	marked, err := m.marked(ctx, ours)
	if err != nil {
		return err
	}
	for _, id := range marked {
		if !slices.Contains(members, id) {
			members = append(members, id)
		}
	}
	if merge != "" {
		members = append(members, merge)
	}
	// keptNames are the bookmarks that stay, so their git refs stay too: those of a commit that is
	// no carrier, unless it carries a run's line and goes with the members, and those of a kept
	// member.
	var keptNames []string
	for _, id := range others {
		if slices.Contains(members, id) {
			continue
		}
		for _, name := range named[id] {
			fmt.Fprintf(m.stderr, "legion merge: left the bookmark %s where it is, on commit %s, which is not a commit a legion merge wrote.\n", name, id)
		}
		keptNames = append(keptNames, named[id]...)
	}
	if len(members) > 0 {
		set := "(" + strings.Join(members, " | ") + ")"
		kept, err := m.keep(ctx, set, swept)
		if err != nil {
			return err
		}
		abandon := set
		if len(kept) > 0 {
			abandon += " ~ (" + strings.Join(kept, " | ") + ")"
		}
		if _, err := m.jj(ctx, "abandon", abandon); err != nil {
			return err
		}
		for _, id := range kept {
			keptNames = append(keptNames, named[id]...)
		}
	}
	if len(untargeted) > 0 {
		if _, err := m.jj(ctx, append([]string{"bookmark", "delete", "--"}, untargeted...)...); err != nil {
			return err
		}
	}
	refs, err := m.git(ctx, nil, nil, "for-each-ref", "--format=%(refname)", "refs/heads/"+bookmarkPrefix+"*")
	if err != nil {
		return err
	}
	var deletes strings.Builder
	for _, ref := range workspace.OutputLines(string(refs)) {
		name := strings.TrimPrefix(ref, "refs/heads/")
		if run, ok := bookmarkRun(name); ok && ours(run) && !slices.Contains(keptNames, name) {
			fmt.Fprintf(&deletes, "delete %s\n", ref)
		}
	}
	if deletes.Len() == 0 {
		return nil
	}
	if _, err := m.git(ctx, []byte(deletes.String()), nil, "update-ref", "--stdin"); err != nil {
		return err
	}
	// A workspace that is not colocated keeps the record of a ref it imported until it imports
	// again, and would list the ref as a bookmark of the remote `git` until then.
	_, err = m.jj(ctx, "git", "import")
	return err
}

// bookmarkRun is the run prefix of name, a carrier's bookmark, or false where name has no
// carrier's role at its end.
func bookmarkRun(name string) (string, bool) {
	match := carrierBookmark.FindStringSubmatch(name)
	if match == nil {
		return "", false
	}
	return match[1], true
}

// notCarriers is each commit of targets, the commits that a sweep's carrier bookmarks name, that
// is not a carrier. git plumbing writes each carrier under carrierEmail, with no parent in the
// repository or on another carrier, and the run gives each one its bookmark, so a carrier is a
// mutable commit of that author whose every ancestor but jj's root() is a target too. A target
// with an ancestor outside the targets and root() descends from a parent of a target that is
// outside them, so one jj log reads every such target. The author tells a carrier from a commit
// whose only parent is root(), which a person or a role can make too. A bookmark of a carrier's
// name is on a commit that is not a carrier only where a person, a role or a tool put it, and an
// abandon of that commit would lose the repository's own work, or a parent the run is about to
// merge.
func (m *merger) notCarriers(ctx context.Context, targets []string) ([]string, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	set := "(" + strings.Join(targets, " | ") + ")"
	return m.list(ctx, fmt.Sprintf("%[1]s & ((parents(%[1]s) ~ %[1]s ~ root()):: | ~mutable() | ~author_email(exact:%[2]s))", set, workspace.JJString(carrierEmail)), "commit_id")
}

// marked is each mutable merge that carries the line of a run ours matches. Each commit a run
// marks is a merge, X of the carriers k2 and k3 or the merge of the two parents, so a role's
// commit with one parent is never one, even when it carries a line it copied from a merge's
// description. The revset finds each merge whose description holds a line of this workspace's
// runs, and then the line must be one that ours matches: the run prefix of a workspace whose name
// is this one's, a dash and more also holds this workspace's prefix. An immutable commit is left
// out, since jj abandons none, and so the search reads only the commits that are not on the base.
func (m *merger) marked(ctx context.Context, ours func(run string) bool) ([]string, error) {
	revset := fmt.Sprintf("mutable() & merges() & description(substring:%s)", workspace.JJString(runTrailer+m.prefix))
	out, err := m.jj(ctx, "log", "--no-graph", "-r", revset, "-T", `commit_id ++ "\0" ++ description ++ "\0"`)
	if err != nil {
		return nil, err
	}
	fields := nulFields(out)
	if len(fields)%2 != 0 {
		return nil, fmt.Errorf("jj log printed %d fields, not commit and description pairs: %q", len(fields), fields)
	}
	var ids []string
	for i := 0; i < len(fields); i += 2 {
		for line := range strings.Lines(fields[i+1]) {
			if run, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), runTrailer); ok && ours(run) {
				ids = append(ids, fields[i])
				break
			}
		}
	}
	return ids, nil
}

// keep is each commit of set, a revset of a run's commits, that stays, with each commit of set
// below it, since an abandon of a commit moves the commits above it. A commit of set stays when a
// commit outside set stands on it, when it is any workspace's working-copy commit, when a local
// bookmark names it that is none of carriers, the bookmarks of the runs the sweep takes, or when
// `::remote_bookmarks()` holds it (held). jj deletes each bookmark of a commit it abandons and
// says nothing of it, and a tracked remote bookmark, as a push leaves it, keeps the commit it
// holds mutable. keep names each commit that stays on stderr, one line each, with why it stays.
func (m *merger) keep(ctx context.Context, set string, carriers []string) ([]string, error) {
	held, err := m.held(ctx, set, carriers)
	if err != nil {
		return nil, err
	}
	standing := fmt.Sprintf("((children(%[1]s) ~ %[1]s) | working_copies()", set)
	for _, id := range slices.Sorted(maps.Keys(held)) {
		standing += " | " + id
	}
	standing += ")"
	kept, err := m.list(ctx, fmt.Sprintf("%s & ::%s", set, standing), "commit_id")
	if err != nil {
		return nil, err
	}
	for _, id := range kept {
		on, err := m.list(ctx, fmt.Sprintf("%s:: & %s", id, standing), "commit_id")
		if err != nil {
			return nil, err
		}
		var why []string
		switch others := slices.DeleteFunc(on, func(c string) bool { return c == id }); len(others) {
		case 0:
		case 1:
			why = append(why, "commit "+others[0]+" stands on it")
		default:
			why = append(why, "commits "+strings.Join(others, " ")+" stand on it")
		}
		why = append(why, held[id]...)
		fmt.Fprintf(m.stderr, "legion merge: kept commit %s, which a legion merge made, because %s.\n", id, strings.Join(why, " and "))
	}
	return kept, nil
}

// held is why each commit of set stays on its own, read with one jj log: it is a working-copy
// commit, a local bookmark that is none of carriers names it, or `::remote_bookmarks()` holds it.
// A commit of set with no such reason is not in the map. jj prints each commit's id, the two
// answers, the count of its local bookmarks and their names, each ended by NUL, which no name
// holds.
func (m *merger) held(ctx context.Context, set string, carriers []string) (map[string][]string, error) {
	out, err := m.jj(ctx, "log", "--no-graph", "-r", set+" & (working_copies() | bookmarks() | ::remote_bookmarks())", "-T",
		`commit_id ++ "\0" ++ self.contained_in("working_copies()") ++ "\0" ++ self.contained_in("::remote_bookmarks()") ++ "\0" ++ local_bookmarks.len() ++ "\0" ++ local_bookmarks.map(|b| b.name() ++ "\0").join("")`)
	if err != nil {
		return nil, err
	}
	fields := nulFields(out)
	held := map[string][]string{}
	for len(fields) > 0 {
		if len(fields) < 4 {
			return nil, fmt.Errorf("jj log printed %q, not a commit, two answers and a count of bookmarks", fields)
		}
		count, err := strconv.Atoi(fields[3])
		if err != nil || count < 0 || len(fields) < 4+count {
			return nil, fmt.Errorf("jj log printed %q, not a commit, two answers and a count of bookmarks", fields)
		}
		id, workingCopy, remote := fields[0], fields[1] == "true", fields[2] == "true"
		names := slices.DeleteFunc(fields[4:4+count], func(name string) bool { return slices.Contains(carriers, name) })
		fields = fields[4+count:]
		var why []string
		if workingCopy {
			why = append(why, "it is a working-copy commit")
		}
		switch len(names) {
		case 0:
		case 1:
			why = append(why, "the bookmark "+names[0]+" names it")
		default:
			why = append(why, "the bookmarks "+strings.Join(names, " ")+" name it")
		}
		if remote {
			why = append(why, "a remote bookmark is on it or on a commit above it")
		}
		if len(why) > 0 {
			held[id] = why
		}
	}
	return held, nil
}
