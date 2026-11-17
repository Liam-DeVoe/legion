package docs

import (
	"context"
	"reflect"

	"github.com/sjawhar/envoy/internal/dispatch/asks"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/processes"
)

// A person's answer is held once, on the ask's node (dispatch://INFRA-1150 R3), and no attribute
// of an ask block in a document holds it. The release before this one kept a second copy in the
// block: the ask's state, who answered it, when, what they chose and wrote, and, on a copy of an
// ask on another document, the ask and the document it was copied from. A page of that release
// still reads them from the block, so while a process of it serves beside one of this build,
// settlement keeps them in step with the asks row, which both builds write
// (rollWindowAskAttributes). Once processes.Live shows the whole fleet on one build, the fleet
// guard (anotherBuildServes), read at each write, makes settlement take them out of the live
// document instead (withoutStoredAskAttributes), so no version holds them. The schema declares
// them as it always did, so the build before compiles the store's schema as it compiles its own;
// only `invalid` is declared `markdown: false`, never reaches stored bytes, and stays in the live
// tree for the ask card to read (G1). The first version a document writes without them carries
// its approval (R10, carryVerdict).

// anotherBuildServes reports whether processes.Live, read through q, lists processes of more
// than one build: two commits, an unstamped build's empty commit being one build with every other
// empty commit, as the decisions step reads the fleet (DecisionStep.anotherBuildLive). It compares
// the live processes' commits among themselves and needs no commit of this process's own, which
// the document service is not given: every process registers in server_processes before it serves
// (processes.Register), and its row stays live while it heartbeats, so this process is among the
// processes listed, and a list of one commit is this build alone. A database no process registers
// in, as a docs test's is, lists none and holds no other build either.
func anotherBuildServes(ctx context.Context, q processes.Querier) (bool, error) {
	live, err := processes.Live(ctx, q)
	if err != nil {
		return false, err
	}
	for _, process := range live {
		if process.Commit != live[0].Commit {
			return true, nil
		}
	}
	return false, nil
}

// storedAskAttributes is the server attributes of an ask block that its markdown carries, read off
// the block schema, which names each once: the seven of the eight a version can store - state,
// answered_by, answered_at, selected, answer, copied_from and copied_from_document - and never
// `invalid`, which no rendering writes (pmdoc.BlockAttributeSchema.InMarkdown).
func storedAskAttributes() []string {
	attributes, _ := pmdoc.BlockTypeAttributes("ask")
	names := make([]string, 0, len(attributes))
	for _, attribute := range attributes {
		if attribute.Server && attribute.InMarkdown() {
			names = append(names, attribute.Name)
		}
	}
	return names
}

// rollWindowAskAttributes is the write that keeps the attributes of an ask block in step with
// ask, its row, while another build serves (twoBuilds), as the release before keeps them: the
// state always; who answered, when, the options chosen (never a nil list, which renders as null
// from one tree and as [] from the room) and the words, for an answered ask; and for a copy, the
// ask and copied, the address of the document it is on. Each other attribute of the seven a
// version can store (storedAskAttributes) is taken off. The write reports whether it changed the
// block, and is made again on the live document (repairLive). On a fleet of one build it is nil:
// settlement then takes the seven out of the block (askReadableWrite).
func rollWindowAskAttributes(twoBuilds bool, ask model.Ask, copied string) func(*pmdoc.Node) bool {
	if !twoBuilds {
		return nil
	}
	return func(node *pmdoc.Node) bool {
		desired := pmdoc.Attrs{"state": ask.State}
		if copied != "" {
			desired["copied_from"] = ask.ID
			desired["copied_from_document"] = copied
		}
		if ask.State == asks.StateAnswered && ask.Answer != nil {
			desired["answered_by"] = ask.Answer.User
			desired["answered_at"] = asks.AnsweredAtValue(ask.Answer.At)
			desired["selected"] = append([]string{}, ask.Answer.Selected...)
			if ask.Answer.Text != nil {
				desired["answer"] = *ask.Answer.Text
			}
		}
		changed := false
		for _, name := range storedAskAttributes() {
			want, wanted := desired[name]
			got, held := node.Attrs[name]
			switch {
			case wanted && held && sameAskAttribute(got, want):
			case wanted:
				node.Attrs[name] = want
				changed = true
			case held:
				delete(node.Attrs, name)
				changed = true
			}
		}
		return changed
	}
}

// sameAskAttribute reports whether got, an attribute value an ask block holds, is want: a list
// of strings equals another of the same items in the same order whichever of []string and []any
// holds it, as a tree read from the room holds a list another tree holds as []string.
func sameAskAttribute(got, want any) bool {
	gotItems, gotIsItems := askStringItems(got)
	wantItems, wantIsItems := askStringItems(want)
	if gotIsItems || wantIsItems {
		return gotIsItems && wantIsItems && reflect.DeepEqual(gotItems, wantItems)
	}
	return reflect.DeepEqual(got, want)
}

func askStringItems(value any) ([]string, bool) {
	switch items := value.(type) {
	case []string:
		return items, true
	case []any:
		out := make([]string, len(items))
		for index, item := range items {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			out[index] = text
		}
		return out, true
	default:
		return nil, false
	}
}

// withoutStoredAskAttributes takes the seven attributes a version can store
// (storedAskAttributes) off node, an ask block, and reports whether it held any. A block holds
// them once the fleet is one build only where the release before wrote them, or where a browser
// put them on it, a forged state or answerer among them.
func withoutStoredAskAttributes(node *pmdoc.Node) bool {
	changed := false
	for _, name := range storedAskAttributes() {
		if _, held := node.Attrs[name]; held {
			delete(node.Attrs, name)
			changed = true
		}
	}
	return changed
}

// askReadableWrite is settlement's write to an ask block it reads (readAskBlockStrictly), whose
// ask, or the ask it is a copy of on the document whose address is copied, is ask: the block
// parses, so it carries no `invalid` and no old reason shows on it (G1); and its other server
// attributes are kept in step with ask while another build serves (rollWindowAskAttributes), and
// taken out once the fleet is one build (withoutStoredAskAttributes).
func askReadableWrite(twoBuilds bool, ask model.Ask, copied string) func(*pmdoc.Node) bool {
	keep := rollWindowAskAttributes(twoBuilds, ask, copied)
	return func(node *pmdoc.Node) bool {
		changed := clearAskInvalidAttribute(node)
		if keep != nil {
			return keep(node) || changed
		}
		return withoutStoredAskAttributes(node) || changed
	}
}
