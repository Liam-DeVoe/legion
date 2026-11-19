package daemon

// The claims the sweep's field test (podupdate_sweep_contract_test.go) sets fields on, and how it
// sets them: the walk over every field of a claim at every depth (claimLeaves), the sets of those
// fields it sets together (sweepFieldBlocks), and the groups of every role it puts the claims of
// each set in (sweepKindGroups). The rig the tests drive the sweep's path with is
// podupdate_sweep_rig_test.go's.

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// sweepKindsOfFacts is the claims of facts, by index, that hold one of the sets of facts of.
func sweepKindsOfFacts(facts []supervise.Claim, of ...sweepFact) []int {
	var kinds []int
	for i, c := range facts {
		if slices.Contains(of, factsOf(c)) {
			kinds = append(kinds, i)
		}
	}
	return kinds
}

// sweepKindGroups is the claims of kinds, of the sets of facts of, in groups of one claim of every
// role, so that a field set on every claim of a group stands beside claims of every other role:
// in the group (b, a, k) the role of index r in sweepRoleList holds the claim of the facts of
// index (k + b·r) mod len(of), in the state of index (a + b·r) mod the states, for every
// arrangement b from 0 to the count of states less one. At b = 0 every claim of a group is of one
// state and one kind, so each claim stands beside a claim of every other role in its own state.
// The count of states is a prime larger than the count of roles, so over all the arrangements
// each claim stands beside each other role in each state, and in each kind of of where len(of)
// is prime too; sweepUnmetNeighbours says where a claim stands beside no claim of some role and
// state, and the field test fails on it. A state, role and set of facts no claim of kinds holds
// fails the test, naming them, rather than take some other claim in its place.
func sweepKindGroups(t *testing.T, facts []supervise.Claim, kinds []int, of []sweepFact) [][]int {
	t.Helper()
	states := supervise.States()
	index := map[[3]int]int{}
	for _, i := range kinds {
		c := facts[i]
		index[[3]int{slices.Index(states, c.State), sweepRoleIndex(c.Role), slices.Index(of, factsOf(c))}] = i
	}
	var groups [][]int
	for b := range states {
		for a := range states {
			for k := range of {
				g := make([]int, 0, sweepRoleCount)
				for r := range sweepRoleCount {
					key := [3]int{(a + b*r) % len(states), r, (k + b*r) % len(of)}
					i, ok := index[key]
					if !ok {
						t.Fatalf("no claim of the kinds tried is a %s %s claim of the facts %s, so no group of every role can hold one",
							states[key[0]], sweepRoleList[r], of[key[2]])
					}
					g = append(g, i)
				}
				groups = append(groups, g)
			}
		}
	}
	return groups
}

// sweepRaceKindGroups is the part of the all-role groups the detector runs: the same-state group
// of each kind the sweep asks, and each of its asked claims beside the ready claim of every other
// role. The plain build's sweepKindGroups holds every kind beside every state and role, and every
// pair alone; this smaller set keeps the states where the sweep's concurrent ask and publish run
// together under -race inside the race step's budget. Every selected claim and every lookup this
// construction needs must be present, or the test fails by name.
func sweepRaceKindGroups(t *testing.T, facts []supervise.Claim, kinds []int, of []sweepFact, asks func(supervise.Claim) bool) [][]int {
	t.Helper()
	states := supervise.States()
	index := map[[3]int]int{}
	for _, i := range kinds {
		c := facts[i]
		index[[3]int{slices.Index(states, c.State), sweepRoleIndex(c.Role), slices.Index(of, factsOf(c))}] = i
	}
	lookup := func(state int, role int, fact int) int {
		key := [3]int{state, role, fact}
		i, ok := index[key]
		if !ok {
			t.Fatalf("no claim of the race groups is a %s %s claim of the facts %s", states[state], sweepRoleList[role], of[fact])
		}
		return i
	}
	selected := slices.DeleteFunc(slices.Clone(kinds), func(i int) bool { return !asks(facts[i]) })
	var groups [][]int
	seen := map[[2]int]bool{}
	for _, i := range selected {
		c := facts[i]
		state, fact := slices.Index(states, c.State), slices.Index(of, factsOf(c))
		key := [2]int{state, fact}
		if seen[key] {
			continue
		}
		seen[key] = true
		g := make([]int, 0, sweepRoleCount)
		for r := range sweepRoleCount {
			g = append(g, lookup(state, r, fact))
		}
		groups = append(groups, g)
	}
	ready, session := slices.Index(states, supervise.StateReady), slices.Index(of, factSession)
	for _, i := range selected {
		g := []int{i}
		for r := range sweepRoleCount {
			if r != sweepRoleIndex(facts[i].Role) {
				g = append(g, lookup(ready, r, session))
			}
		}
		groups = append(groups, g)
	}
	if len(selected) == 0 || len(groups) == 0 {
		t.Fatal("the race build selected no claim or no group: it would hold no concurrent sweep path")
	}
	return groups
}

// sweepUnmetNeighbours is each claim of kinds, with each role other than its own and each state,
// such that no group of groups puts a claim of that role in that state beside it, named.
func sweepUnmetNeighbours(facts []supervise.Claim, kinds []int, groups [][]int) (missing []string) {
	states := supervise.States()
	beside := map[int][]bool{}
	for _, g := range groups {
		for _, i := range g {
			seen := beside[i]
			if seen == nil {
				seen = make([]bool, sweepRoleCount*len(states))
				beside[i] = seen
			}
			for _, j := range g {
				if j != i {
					seen[sweepRoleIndex(facts[j].Role)*len(states)+slices.Index(states, facts[j].State)] = true
				}
			}
		}
	}
	for _, i := range kinds {
		for r, role := range sweepRoleList {
			if role == facts[i].Role {
				continue
			}
			for s, state := range states {
				if seen := beside[i]; seen == nil || !seen[r*len(states)+s] {
					missing = append(missing, fmt.Sprintf("a %s %s claim beside %s", state, role, describeSwept(facts[i])))
				}
			}
		}
	}
	return missing
}

// sweepFlips is, for each leaf, whether setting it on some kind of claim changes whether the rule
// owes that claim an ask with a session (sweepOwed): a session on a claim with none, a delivered
// ask or an answer on a claim the rule owes one.
func sweepFlips(facts []supervise.Claim, kinds []int, leaves []claimLeaf, at time.Time) []bool {
	flips := make([]bool, len(leaves))
	for l, leaf := range leaves {
		for _, i := range kinds {
			c := facts[i]
			if !leaf.empty(c) {
				continue
			}
			owed := sweepOwed(c)
			if leaf.set(&c, at); sweepOwed(c) != owed {
				flips[l] = true
				break
			}
		}
	}
	return flips
}

// sweepFieldBlocks is the sets of leaves, by index, that the field test sets together on each kind
// of claim, so that every pair of leaves stands together in one set, and no set that holds a pair
// whose answer the rule leaves as it is also holds a leaf that changes the answer (flips), which
// would hide what a filter on that pair does. The leaves that change no answer are the points of an
// affine plane of order q, the least prime whose square is at least their count, and each line of
// the plane is a set: any two points of the plane lie on one line. Each leaf that changes an answer
// is set with each run of q of the others in turn, and with each other leaf that changes one,
// alone. Last, every leaf that changes no answer is set at once, alone and with each leaf that
// changes one, so a filter on any number of fields set together fails too.
func sweepFieldBlocks(flips []bool) [][]int {
	var keep, change []int
	for l, f := range flips {
		if f {
			change = append(change, l)
		} else {
			keep = append(keep, l)
		}
	}
	q := 2
	for q*q < len(keep) || !sweepPrime(q) {
		q++
	}
	var blocks [][]int
	line := func(point func(i int) int) {
		var block []int
		for i := range q {
			if p := point(i); p < len(keep) {
				block = append(block, keep[p])
			}
		}
		if len(block) >= 2 {
			blocks = append(blocks, block)
		}
	}
	for m := range q {
		for b := range q {
			line(func(x int) int { return x*q + (m*x+b)%q })
		}
	}
	for x := range q {
		line(func(y int) int { return x*q + y })
	}
	for i, f := range change {
		for start := 0; start < len(keep); start += q {
			blocks = append(blocks, append([]int{f}, keep[start:min(start+q, len(keep))]...))
		}
		for _, g := range change[i+1:] {
			blocks = append(blocks, []int{f, g})
		}
	}
	blocks = append(blocks, slices.Clone(keep))
	for _, f := range change {
		blocks = append(blocks, append([]int{f}, keep...))
	}
	return blocks
}

// sweepPrime is whether n is a prime.
func sweepPrime(n int) bool {
	for d := 2; d*d <= n; d++ {
		if n%d == 0 {
			return false
		}
	}
	return n >= 2
}

// sweepUncoveredPairs is each pair of leaves that some kind of claim leaves empty and that no set of
// blocks puts together on it: those of a block that the kind leaves empty are set on it together.
func sweepUncoveredPairs(facts []supervise.Claim, kinds []int, leaves []claimLeaf, blocks [][]int) (missing []string) {
	n := len(leaves)
	seen := map[string]bool{}
	for _, i := range kinds {
		empty := make([]bool, n)
		key := make([]byte, n)
		for l, leaf := range leaves {
			if empty[l] = leaf.empty(facts[i]); empty[l] {
				key[l] = 1
			}
		}
		if seen[string(key)] {
			continue
		}
		seen[string(key)] = true
		covered := make([]bool, n*n)
		for _, block := range blocks {
			for x, a := range block {
				if !empty[a] {
					continue
				}
				for _, b := range block[x+1:] {
					if empty[b] {
						covered[a*n+b], covered[b*n+a] = true, true
					}
				}
			}
		}
		for a := range n {
			for b := a + 1; b < n; b++ {
				if empty[a] && empty[b] && !covered[a*n+b] {
					missing = append(missing, fmt.Sprintf("%s and %s on %s", leaves[a].name, leaves[b].name, describeSwept(facts[i])))
				}
			}
		}
	}
	return missing
}

// sweepLeafNames names the leaves of block, the first few of a long one.
func sweepLeafNames(leaves []claimLeaf, block []int) string {
	var names []string
	for _, l := range block[:min(len(block), 4)] {
		names = append(names, leaves[l].name)
	}
	if len(block) > 4 {
		return fmt.Sprintf("the %d fields %s and %d more", len(block), strings.Join(names, ", "), len(block)-4)
	}
	return strings.Join(names, " and ")
}

// sweepSliceLength is how many elements claimLeaves walks each slice of the claim to:
// launch_failure_limit's shipped value, which the daemon loads where a legion.yaml leaves the key
// unset (config.Config.LaunchFailureLimit), and which is how many turn outcomes a claim's window
// holds (supervise's turnOutcomeWindow) there.
func sweepSliceLength(t *testing.T) int {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legion.yaml")
	body := "project: demo\nstate_dir: /var/lib/legion\npostgres_dsn: postgres://legion@127.0.0.1:5432/legion\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadForValidation(path, func(string) string { return "" })
	if err != nil {
		t.Fatalf("load a legion.yaml that leaves launch_failure_limit unset: %v", err)
	}
	if cfg.LaunchFailureLimit < 1 {
		t.Fatalf("launch_failure_limit's shipped value is %d, want a positive number", cfg.LaunchFailureLimit)
	}
	return cfg.LaunchFailureLimit
}

// claimLeaf is one exported field of supervise.Claim at any depth below it, through structs,
// pointers and a slice's elements, that holds a value of its own: a string, a bool, a number or a
// time. name is its path ("Pending.ConfirmedAt", "TurnOutcomes[0].Generation",
// "TurnOutcomes[2].Generation"), and top is the claim's own field it is under.
type claimLeaf struct {
	name, top string
	path      []leafStep
}

// leafStep is one step of a leaf's path: a struct's field (kind reflect.Struct, field its index),
// the value a pointer points to (reflect.Pointer), or a slice's element (reflect.Slice, field its
// index, which gives the slice that index plus one elements).
type leafStep struct {
	kind  reflect.Kind
	field int
}

// claimLeaves is every leaf of supervise.Claim, and the name of each field at any depth that holds
// a value this test cannot set — an interface, a map, a function, a channel, a struct with no
// exported field, or a type that holds itself — which the tests fail on. A slice is walked at each
// of its elements up to elements (sweepSliceLength), the element at index i giving the slice i+1
// elements, so that a statement that reads how many elements a slice holds (a claim's turn
// outcomes) is tried with one, with two and with three, the most a claim holds under the shipped
// launch_failure_limit.
func claimLeaves(elements int) (leaves []claimLeaf, unset []string) {
	timeType := reflect.TypeOf(time.Time{})
	var walk func(t reflect.Type, name, top string, path []leafStep, seen []reflect.Type)
	walk = func(t reflect.Type, name, top string, path []leafStep, seen []reflect.Type) {
		if t == timeType {
			leaves = append(leaves, claimLeaf{name: name, top: top, path: slices.Clone(path)})
			return
		}
		if slices.Contains(seen, t) {
			unset = append(unset, name+" (a type that holds itself)")
			return
		}
		seen = append(slices.Clone(seen), t)
		switch t.Kind() {
		case reflect.String, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
			leaves = append(leaves, claimLeaf{name: name, top: top, path: slices.Clone(path)})
		case reflect.Pointer:
			walk(t.Elem(), name, top, append(slices.Clone(path), leafStep{kind: reflect.Pointer}), seen)
		case reflect.Slice:
			for i := range elements {
				walk(t.Elem(), fmt.Sprintf("%s[%d]", name, i), top, append(slices.Clone(path), leafStep{kind: reflect.Slice, field: i}), seen)
			}
		case reflect.Struct:
			exported := 0
			for i := range t.NumField() {
				if f := t.Field(i); f.IsExported() {
					exported++
					walk(f.Type, name+"."+f.Name, top, append(slices.Clone(path), leafStep{kind: reflect.Struct, field: i}), seen)
				}
			}
			if exported == 0 {
				unset = append(unset, fmt.Sprintf("%s (%s, no exported field)", name, t))
			}
		default:
			unset = append(unset, fmt.Sprintf("%s (%s)", name, t))
		}
	}
	claimType := reflect.TypeOf(supervise.Claim{})
	for i := range claimType.NumField() {
		if f := claimType.Field(i); f.IsExported() {
			walk(f.Type, f.Name, f.Name, []leafStep{{kind: reflect.Struct, field: i}}, nil)
		}
	}
	return leaves, unset
}

// empty is whether c leaves the leaf empty: a nil pointer on its path, a slice too short to hold
// the element its path names, or a zero value at its end.
func (l claimLeaf) empty(c supervise.Claim) bool {
	v := reflect.ValueOf(c)
	for _, s := range l.path {
		switch s.kind {
		case reflect.Struct:
			v = v.Field(s.field)
		case reflect.Pointer:
			if v.IsNil() {
				return true
			}
			v = v.Elem()
		case reflect.Slice:
			if v.Len() <= s.field {
				return true
			}
			v = v.Index(s.field)
		}
	}
	return v.IsZero()
}

// set sets the leaf on *c to a value that is not empty: at for a time, "x" for a string, true for
// a bool and 1 for a number. Each pointer and slice on its path is copied first, or made where it
// is nil or too short to hold the element the path names, so no claim that shares one with c is
// written through.
func (l claimLeaf) set(c *supervise.Claim, at time.Time) {
	v := reflect.ValueOf(c).Elem()
	for _, s := range l.path {
		switch s.kind {
		case reflect.Struct:
			v = v.Field(s.field)
		case reflect.Pointer:
			p := reflect.New(v.Type().Elem())
			if !v.IsNil() {
				p.Elem().Set(v.Elem())
			}
			v.Set(p)
			v = p.Elem()
		case reflect.Slice:
			n := max(v.Len(), s.field+1)
			elems := reflect.MakeSlice(v.Type(), n, n)
			reflect.Copy(elems, v)
			v.Set(elems)
			v = elems.Index(s.field)
		}
	}
	switch {
	case v.Type() == reflect.TypeOf(time.Time{}):
		v.Set(reflect.ValueOf(at))
	case v.Kind() == reflect.String:
		v.SetString("x")
	case v.Kind() == reflect.Bool:
		v.SetBool(true)
	case v.CanInt():
		v.SetInt(1)
	case v.CanUint():
		v.SetUint(1)
	case v.CanFloat():
		v.SetFloat(1)
	}
}
