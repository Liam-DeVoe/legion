package specgraph

// Unit is a work unit as the workflow spec defines it (dispatch://INFRA-3570/spec), as far as a
// wait reads it (R29): the statements it serves, the units it needs, and the statements it waits
// on. JSON tags are the form of units.json.
type Unit struct {
	ID      string   `json:"id"`
	Serves  []string `json:"serves"`
	Needs   []string `json:"needs"`
	WaitsOn []string `json:"waits_on"`
}

// WaitCycle is a cycle of waits among units, or nil when there is none. A unit waits on each unit
// it needs, and a wait on a statement counts as a wait on each unit that serves that statement
// (R29: "For the cycle rule of the workflow spec, a wait on a statement counts as a wait on each
// unit that serves that statement"). The cycle is the shortest from the smallest unit id that is on
// a cycle, written unit, statement, unit, …, back to that unit, with no statement between two units
// where the first needs the second; of two steps to one unit, a need comes first, then the
// statement with the smallest id. Only the rule is here: refusing the wait that closes a cycle is
// the workflow capability's.
func WaitCycle(units []Unit) []string {
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
		return nil
	}
	// findLoops writes unit, relation, unit, …; a need has no statement.
	cycle := make([]string, 0, len(loops[0]))
	for _, step := range loops[0] {
		if step != "" {
			cycle = append(cycle, step)
		}
	}
	return cycle
}
