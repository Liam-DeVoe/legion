package holdrecord

import (
	"errors"
	"testing"
)

// admittedOps is a record in which command c of w has every entry it needs before its admission:
// hold set's intent and done, a done hold prove first for an epochal command, and each command it
// requires done.
func admittedOps(w Workflow, c string) []Operation {
	ops := []Operation{{Command: CommandSet, State: stateIntent}, {Command: CommandSet, State: stateDone}}
	if Epochal(w, c) {
		ops = append(ops, Operation{Command: CommandProve, State: stateIntent}, Operation{Command: CommandProve, State: stateDone})
	}
	for _, q := range Requires(w, c) {
		if q == CommandProve && Epochal(w, c) {
			continue
		}
		ops = append(ops, Operation{Command: q, State: stateIntent}, Operation{Command: q, State: stateDone})
	}
	return ops
}

// No command strands a hold. For every workflow and every command, once the command's intent is
// written and the command can never succeed, its own stop is admitted (it is Stoppable), and after
// that stop hold abort is admitted, but in hub-restore after a done hold prove, whose own rule
// excludes abort (a stopped census there is ended by another prove and census, or not at all); and
// while any other command's intent is pending, abort is refused ErrBusy. Fails if a command a check
// can refuse after its intent is not Stoppable: the count of stranded commands is not 0.
func TestEveryCommandHasAWayOut(t *testing.T) {
	stranded, checked := 0, 0
	for _, w := range current.Workflows() {
		for _, c := range Commands(w) {
			if c == CommandAbort {
				continue
			}
			before := Done{Workflow: w, Ops: admittedOps(w, c)}
			if err := Decide(before, Request{Command: c}); err != nil {
				t.Errorf("%s's %q is not admitted after the entries it needs: %v", w, c, err)
				continue
			}
			checked++
			live := Done{Workflow: w, Ops: append(append([]Operation{}, before.Ops...), Operation{Command: c, State: stateIntent})}
			if !Stoppable(w, c) && Decide(live, Request{Command: CommandRelease}) != nil && Decide(live, Request{Command: CommandAbort}) != nil {
				stranded++
				t.Errorf("%s's %q strands the hold: it is not Stoppable, and with its intent pending release and abort are refused", w, c)
				continue
			}
			stopped := Done{Workflow: w, Ops: append(append([]Operation{}, live.Ops...), Operation{Command: c, State: stateStop})}
			_, proved := Proven(stopped)
			err := Decide(stopped, Request{Command: CommandAbort})
			switch {
			case w == WorkflowHubRestore && proved:
				if !errors.Is(err, ErrExcluded) {
					t.Errorf("hub-restore's %q stopped after a done prove: abort answered %v, want hub-restore's own rule, ErrExcluded", c, err)
				}
			case err != nil:
				t.Errorf("%s's %q stopped: abort answered %v, want admitted", w, c, err)
			}
			for _, x := range Commands(w) {
				if x == c || x == CommandAbort || Decide(stopped, Request{Command: x}) != nil {
					continue
				}
				busy := Done{Workflow: w, Ops: append(append([]Operation{}, stopped.Ops...), Operation{Command: x, State: stateIntent})}
				if err := Decide(busy, Request{Command: CommandAbort}); !errors.Is(err, ErrBusy) {
					t.Errorf("%s's %q stopped and %q live: abort answered %v, want ErrBusy", w, c, x, err)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no command was checked")
	}
	t.Logf("%d commands checked, %d stranded", checked, stranded)
}
