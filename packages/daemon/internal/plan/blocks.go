package plan

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// The two types of block a plan holds. Every other block of the document is its prose.
// RoleBlockType is no type of Dispatch's block schema yet: the release that lets an owner define
// a role adds it there under this name, with the role's name as the block's id.
const (
	TaskBlockType = "plan_task"
	RoleBlockType = "plan_role"
)

// Block is one block of a version of the plan document, as Dispatch gives it: its id, its type,
// its attributes as its opening line writes them, and its text. Dispatch gives a version's typed
// blocks alone, since only a typed block keeps its id from one read of the version to the next.
type Block struct {
	ID, Type   string
	Attributes map[string]string
	Text       string
}

// FromBlocks reads one version of the plan document of issue from its blocks: each `plan_task`
// block is a task and each `plan_role` block a role the plan defines, in the document's order, and
// every other block is skipped. document is that document as a block reference names it before
// its `#b-`, as Dispatch writes one (FormatBlockRef in packages/envoy/internal/dispatch/text):
// `dispatch://<issue>/spec` for the issue's primary document, `dispatch://<issue>/artifact/<slug>`
// for any other.
//
// Each task block stands under the block its `parent` names (parentOf), or at the top where it has
// no `parent` or names a block of another document, and Places says what it is there: a task at the
// top, a part of a task, which is a task of the plan with that task in its PartOf, or an element of
// a task, which is no task of the plan and is kept in Elements for Validate. A `parent` that names
// an id of document that no block of blocks holds puts its block at the top, kept in
// Task.MissingParent, which Validate refuses. Each task's needs are the ids of the tasks they
// name, read against issue (resolveNeeds), so no plan holds a need as a word. A document with no
// task block is read as a plan with no task, which Validate refuses. It answers an error only for
// a block it cannot read at all: a task or a role with no id, one id on two such blocks, and a task
// block whose `steps` is outside the grammar (ParseSteps); the error names the block.
func FromBlocks(issue, artifactID, document string, version int, blocks []Block) (Plan, error) {
	p := Plan{ArtifactID: artifactID, Version: version}
	typeOf := map[string]string{}
	held := map[string]bool{}
	for _, block := range blocks {
		if block.ID != "" {
			held[block.ID] = true
		}
	}
	var taskBlocks []Block
	var read []Task
	var placed []TaskBlock
	for _, block := range blocks {
		if block.Type != TaskBlockType && block.Type != RoleBlockType {
			continue
		}
		if block.ID == "" {
			return Plan{}, fmt.Errorf("a %s block has no id, so no step of the plan can be named", block.Type)
		}
		if earlier, ok := typeOf[block.ID]; ok {
			return Plan{}, fmt.Errorf("the id %s names a %s block and a %s block, so it names no one block", block.ID, earlier, block.Type)
		}
		typeOf[block.ID] = block.Type
		if block.Type == RoleBlockType {
			p.RoleBlocks = append(p.RoleBlocks, block.ID)
			continue
		}
		task, err := taskOf(block)
		if err != nil {
			return Plan{}, err
		}
		parent, ofDocument := parentOf(block.Attributes["parent"], document)
		if ofDocument && !held[parent] {
			// A block that names a block of its document that the version lacks stands at the top,
			// and Validate refuses the version.
			task.MissingParent = parent
			if parent == "" {
				task.MissingParent = strings.TrimSpace(block.Attributes["parent"])
			}
			parent = ""
		}
		taskBlocks, read = append(taskBlocks, block), append(read, task)
		placed = append(placed, TaskBlock{ID: block.ID, Parent: parent, Owner: task.Owner})
	}
	places := Places(placed)
	for i, task := range read {
		place := places[task.ID]
		if place.Standing == ElementOfTask {
			p.Elements = append(p.Elements, elementOf(taskBlocks[i], task, place, placed[i].Parent))
			continue
		}
		task.PartOf = place.Task
		p.Tasks = append(p.Tasks, task)
	}
	return resolveNeeds(p, issue), nil
}

// taskOf is one `plan_task` block as a task, before Places says where it stands. `needs` is its
// words, each kept whole; `merges` is read by its presence, and only the word false says the task
// does not merge.
func taskOf(block Block) (Task, error) {
	attributes := block.Attributes
	task := Task{ID: block.ID, Owner: attributes["owner"], Text: block.Text}
	if needs := strings.Fields(attributes["needs"]); len(needs) > 0 {
		task.Needs = needs
	}
	if merges, ok := attributes["merges"]; ok && merges != "false" {
		task.Merges = true
	}
	steps, err := ParseSteps(attributes["steps"])
	if err != nil {
		return Task{}, fmt.Errorf("task %s: %w", block.ID, err)
	}
	if len(steps) > 0 {
		task.Steps = steps
	}
	return task, nil
}

// parentOf is the block a `parent` word names, read as the needs' resolver reads a word: the id
// after its last `#b-` (neededID). ofDocument says whether the word names a block of document at
// all: it starts with document and `#b-`, as Dispatch writes a reference to one of its blocks, so
// the primary document's `/spec` form and a document's `/artifact/<slug>` form each name their own
// document alone. A word of another document, of another issue, of an ask, a comment or a message,
// and no word at all, name no block of document, and answer "" and false. Dispatch holds at most
// one reference in `parent`, and a value of more words is no one reference, so it names no block
// either. The id is "" for a word of document whose fragment does not decode.
func parentOf(word, document string) (id string, ofDocument bool) {
	words := strings.Fields(word)
	if len(words) != 1 || !strings.HasPrefix(words[0], document+"#b-") {
		return "", false
	}
	return neededID(words[0]), true
}

// elementOf is task, read from block, as an element of a task (place) that stands directly under
// the block under: each attribute it names that only a task carries out, read as taskOf reads each
// one, `steps` with a step, `owner` with a role and `merges` by its presence.
func elementOf(block Block, task Task, place Place, under string) Element {
	element := Element{ID: task.ID, Of: place.Task, Under: under}
	if task.NamesSteps() {
		element.Names = append(element.Names, "steps")
	}
	if task.Owner != "" {
		element.Names = append(element.Names, "owner")
	}
	if _, merges := block.Attributes["merges"]; merges {
		element.Names = append(element.Names, "merges")
	}
	return element
}

// Standing is where a task block stands in its plan (Places).
type Standing int

const (
	// AtTheTop is a task at the top of the plan, which the owner of the issue writes.
	AtTheTop Standing = iota
	// PartOfTask is a part of a task with a sub-owner, which that sub-owner writes. Legion carries
	// it out as it carries out a task at the top.
	PartOfTask
	// ElementOfTask is an element of a task: a planner writes it as the guide of the steps after
	// it, and Legion does not carry it out.
	ElementOfTask
)

// Place is where one task block stands: its standing, and the task it is a part or an element of,
// "" at the top.
type Place struct {
	Standing Standing
	Task     string
}

// TaskBlock is one task block of a plan as Places reads it: its id, the id of the block of the
// plan's document it stands directly under ("" for none), and the role of its sub-owner ("" for
// none).
type TaskBlock struct{ ID, Parent, Owner string }

// Places is where each task block of a plan stands, by its id. blocks are the plan's task blocks
// in the document's order, each id once, and each parent as the plan read gives it. A block whose
// parent is no task block of blocks is a task at the top. A block whose parent is a task with a
// sub-owner is a part of that task, and one whose parent is a task with no sub-owner is an element
// of it. A block whose parent is an element is an element of that element's task. So a part of a
// part whose task has a sub-owner is a part of it, and a task of any depth is a part or a task at
// the top.
//
// No parent that would close a cycle is taken, as Dispatch takes none in the tree of an issue: in
// the document's order, a block whose chain of parents comes back to it stands at the top, and
// each block after it stands under it as its parent says. Walking each block once in that order
// leaves no cycle, since a cycle that is still there runs through the block whose walk finds it.
//
// Until Dispatch serves the tree of an issue, the plan read (FromBlocks) gives each parent from
// the block's `parent`, and a `parent` that names an id of the plan's document that no typed block
// of the version holds is refused, a heading's among them: Dispatch's read of a version lists its
// typed blocks alone, so the read cannot tell a heading from a block that is gone. The tree read
// gives the node each block stands under, which lifts that limit: a heading is then no task block,
// and a block under it a task at the top.
func Places(blocks []TaskBlock) map[string]Place {
	byID := make(map[string]TaskBlock, len(blocks))
	parents := make(map[string]string, len(blocks))
	for _, block := range blocks {
		byID[block.ID] = block
		parents[block.ID] = block.Parent
	}
	for _, block := range blocks {
		seen := map[string]bool{}
		for above := parents[block.ID]; above != "" && !seen[above]; above = parents[above] {
			if above == block.ID {
				parents[block.ID] = ""
				break
			}
			seen[above] = true
		}
	}
	places := make(map[string]Place, len(blocks))
	var placeOf func(id string) Place
	placeOf = func(id string) Place {
		if place, ok := places[id]; ok {
			return place
		}
		var place Place
		if above, ok := byID[parents[id]]; ok {
			switch upper := placeOf(above.ID); {
			case upper.Standing == ElementOfTask:
				place = Place{Standing: ElementOfTask, Task: upper.Task}
			case above.Owner != "":
				place = Place{Standing: PartOfTask, Task: above.ID}
			default:
				place = Place{Standing: ElementOfTask, Task: above.ID}
			}
		}
		places[id] = place
		return place
	}
	for _, block := range blocks {
		placeOf(block.ID)
	}
	return places
}

// resolveNeeds is p, the plan of issue, with each task's Needs as task ids: each word's id after its
// last "#b-", decoded as Dispatch encodes a block id (neededID), or the word itself where it holds
// no "#b-". The ids keep the order the block writes the words in, and a task a block names twice is
// needed once. The plan's document is a document of issue (register_plan refuses any other), so a
// `dispatch://` reference names a task of p only when it is of issue (issueOf): one into another
// issue, and one that starts with no issue key, names no task of p, whatever its block id, and
// stays in Task.OutsideIssue. A word whose id is an element of p (Plan.Elements) stays in
// Task.ElementNeeds, and any other word whose id is no task of p stays in Task.Unresolved.
// Validate refuses each of the three.
func resolveNeeds(p Plan, issue string) Plan {
	ids := make(map[string]bool, len(p.Tasks))
	for _, task := range p.Tasks {
		ids[task.ID] = true
	}
	elements := make(map[string]bool, len(p.Elements))
	for _, element := range p.Elements {
		elements[element.ID] = true
	}
	tasks := make([]Task, len(p.Tasks))
	for i, task := range p.Tasks {
		words := task.Needs
		task.Needs, task.Unresolved, task.OutsideIssue, task.ElementNeeds = nil, nil, nil, nil
		for _, word := range words {
			if of, reference := issueOf(word); reference && of != issue {
				if !slices.Contains(task.OutsideIssue, word) {
					task.OutsideIssue = append(task.OutsideIssue, word)
				}
				continue
			}
			id := neededID(word)
			switch {
			case elements[id]:
				if !slices.Contains(task.ElementNeeds, word) {
					task.ElementNeeds = append(task.ElementNeeds, word)
				}
			case !ids[id]:
				if !slices.Contains(task.Unresolved, word) {
					task.Unresolved = append(task.Unresolved, word)
				}
			case !slices.Contains(task.Needs, id):
				task.Needs = append(task.Needs, id)
			}
		}
		tasks[i] = task
	}
	p.Tasks = tasks
	return p
}

// issueOf is the issue a `needs` word names when it is a `dispatch://` reference, as Dispatch reads
// one (parseDispatch in packages/envoy/internal/dispatch/text): the key the reference starts with,
// before its first `/` or `#`, when that key is an issue key, and "" for a reference that starts
// with a project's key, `agent` or anything else, which names no issue. reference says whether the
// word is a `dispatch://` reference at all: a word with none names a task or a block of the plan's
// own document. The document a reference of the plan's own issue names is not read: a need names
// a task by its block id alone, so a block of another document of the issue with a task's id names
// that task.
func issueOf(word string) (issue string, reference bool) {
	rest, ok := strings.CutPrefix(word, "dispatch://")
	if !ok {
		return "", false
	}
	key, _, _ := strings.Cut(rest, "#")
	key, _, _ = strings.Cut(key, "/")
	if !claim.IsIssueKey(key) {
		return "", true
	}
	return key, true
}

// pin is the `@v<N>` that pins a reference to version N, as Dispatch reads one.
var pin = regexp.MustCompile(`^v[1-9][0-9]*$`)

// neededID is the id of the task a `needs` word names: the block id after the word's last `#b-`,
// read as Dispatch reads a block reference's fragment (EncodeBlockID and blockID in
// packages/envoy/internal/dispatch/text): an `@` is only ever a pin, which names a version of the
// document and no other block, and the rest is percent-decoded. A word with no `#b-` is the id
// itself. It is "" for a fragment with a malformed pin or an escape that does not decode, an id no
// task has: FromBlocks refuses a block with none.
func neededID(word string) string {
	at := strings.LastIndex(word, "#b-")
	if at < 0 {
		return word
	}
	encoded := word[at+len("#b-"):]
	if unpinned, version, pinned := strings.Cut(encoded, "@"); pinned {
		if !pin.MatchString(version) {
			return ""
		}
		encoded = unpinned
	}
	// url.PathUnescape answers "" with its error, for an escape that does not decode.
	id, _ := url.PathUnescape(encoded)
	return id
}

// ChangedTasks is the ids of the tasks next changes against held, in the order of their ids: each
// task whose block says anything else (its steps, its text, `needs`, `owner`, `merges`, or the task
// it is a part of, which its `parent` gives), each task held has and next does not, and each task
// next adds. A change of the prose outside the task blocks changes no task, and neither does a
// change of `serves`, which says what a task is for and nothing Legion carries out, nor a change of
// an element, which is no task (Places). The answer is never nil.
//
// It compares the tasks as the blocks write them, so a held plan is read from the blocks of the
// version that was given, not from the steps a task runs (a task with no steps runs the project's
// defaults, and its block still names none).
func ChangedTasks(held, next Plan) []string {
	before := make(map[string]Task, len(held.Tasks))
	for _, task := range held.Tasks {
		before[task.ID] = task
	}
	after := make(map[string]bool, len(next.Tasks))
	changed := []string{}
	for _, task := range next.Tasks {
		after[task.ID] = true
		if was, ok := before[task.ID]; !ok || !sameBlock(was, task) {
			changed = append(changed, task.ID)
		}
	}
	for _, task := range held.Tasks {
		if !after[task.ID] {
			changed = append(changed, task.ID)
		}
	}
	slices.Sort(changed)
	return changed
}

// OwnershipRefusals is why giver may not give next over held: one reason for each task next
// changes (ChangedTasks) whose writer (WriterOf) is not giver, in held where held has the task or
// in next where next has it, in the order of the ids, and none when giver writes each. The owner
// of the issue writes the tasks of the top level, and the sub-owner of a task writes its parts,
// so the owner's reason says what it can do instead. A first version is compared with an empty
// held plan, so the owner's first version that writes a part is refused as a later one is. A task
// that next removes together with the task it is a part of, directly or through its tasks, is no
// change of that task's parts but goes with it, since a part cannot outlive its task, so it is
// refused nothing.
func OwnershipRefusals(held, next Plan, giver Giver) []Refusal {
	before := make(map[string]Task, len(held.Tasks))
	for _, task := range held.Tasks {
		before[task.ID] = task
	}
	after := make(map[string]bool, len(next.Tasks))
	for _, task := range next.Tasks {
		after[task.ID] = true
	}
	var refusals []Refusal
	for _, id := range ChangedTasks(held, next) {
		task, inHeld := before[id]
		if !after[id] && removedWithItsTask(task, before, after) {
			continue
		}
		var writers []string
		if inHeld {
			writers = append(writers, held.WriterOf(id))
		}
		if after[id] {
			writers = append(writers, next.WriterOf(id))
		}
		for _, writer := range writers {
			if writer == string(giver) {
				continue
			}
			reason := fmt.Sprintf("task %s is not a part of task %s, and the sub-owner of task %s changes only the parts of %s.", id, giver, giver, giver)
			if giver == "" {
				reason = fmt.Sprintf("task %s is a part of task %s, and only the sub-owner of task %s changes the parts of %s. "+
					"Leave the parts of %s to its sub-owner, which writes them, or write this work as tasks of your own, with needs, outside task %s.",
					id, writer, writer, writer, writer, writer)
			}
			refusals = append(refusals, Refusal{Task: id, Reason: reason})
			break
		}
	}
	return refusals
}

// removedWithItsTask says whether a held task that the next version lacks is a part of a task
// the next version lacks too, directly or through its tasks: before is the held plan's tasks by
// id, and after the ids of the next version's.
func removedWithItsTask(task Task, before map[string]Task, after map[string]bool) bool {
	seen := map[string]bool{}
	for above := task.PartOf; above != "" && !seen[above]; above = before[above].PartOf {
		seen[above] = true
		if _, held := before[above]; !held {
			return false
		}
		if !after[above] {
			return true
		}
	}
	return false
}

// sameBlock says whether two tasks of one id say the same thing: their needs are compared as the
// tasks they name. Neither side holds a word that names no task: the held plan is read from stored
// rows, and a later version passed Validate, which refuses each such word.
func sameBlock(a, b Task) bool {
	return a.Text == b.Text && a.PartOf == b.PartOf && a.Owner == b.Owner && a.Merges == b.Merges &&
		slices.Equal(a.Needs, b.Needs) && slices.Equal(a.Steps, b.Steps)
}
