package specgraph

import (
	"errors"
	"fmt"
)

// Unit is a work unit as the workflow spec defines it (dispatch://INFRA-3570/spec), as far as a
// wait reads it (R29): the statements it serves, the units it needs, and the statements it waits
// on. JSON tags are the form of units.json.
type Unit struct {
	ID      string   `json:"id" input:"nonzero"`
	Serves  []string `json:"serves" input:"present: [] serves no statement, and a file sets it"`
	Needs   []string `json:"needs" input:"present: [] needs no unit, and a file sets it"`
	WaitsOn []string `json:"waits_on" input:"present: [] waits on no statement, and a file sets it"`
}

// unitsFault is the refusal of the first unit of units that sets no id, which would read as a unit
// named "", or "" when each sets one. The reader of units.json and WaitCycle both refuse units by
// it.
func unitsFault(units []Unit) string {
	for i, u := range units {
		if u.ID == "" {
			return fmt.Sprintf("unit %d has no id", i+1)
		}
	}
	return ""
}

// WaitCycle is a cycle of waits among units, or nil when there is none. A unit waits on each unit
// it needs, and a wait on a statement counts as a wait on each unit that serves that statement
// (R29: "For the cycle rule of the workflow spec, a wait on a statement counts as a wait on each
// unit that serves that statement"). The cycle is the shortest from the smallest unit id that is on
// a cycle, written unit, statement, unit, …, back to that unit, with no statement between two units
// where the first needs the second; of two steps to one unit, a need comes first, then the
// statement with the smallest id. Only the rule is here: refusing the wait that closes a cycle is
// the workflow capability's. It gives an error, and no cycle, for a unit that sets no id
// (unitsFault).
func WaitCycle(units []Unit) ([]string, error) {
	if fault := unitsFault(units); fault != "" {
		return nil, errors.New("specgraph: " + fault)
	}
	servedBy := map[string][]string{}
	for _, u := range units {
		for _, s := range u.Serves {
			servedBy[s] = append(servedBy[s], u.ID)
		}
	}
	edges := make(map[string][]edge, len(units))
	for _, u := range units {
		for _, needed := range u.Needs {
			edges[u.ID] = append(edges[u.ID], edge{to: needed})
		}
		for _, s := range u.WaitsOn {
			for _, server := range servedBy[s] {
				edges[u.ID] = append(edges[u.ID], edge{to: server, relation: s})
			}
		}
	}
	loops := findLoops(edges)
	if len(loops) == 0 {
		return nil, nil
	}
	// findLoops writes unit, relation, unit, …; a need has no statement.
	cycle := make([]string, 0, len(loops[0]))
	for _, step := range loops[0] {
		if step != "" {
			cycle = append(cycle, step)
		}
	}
	return cycle, nil
}
