package api

import (
	"fmt"
	"strings"
	"testing"
)

// heldVersions is the version numbers memo keeps, the most recently used first. It fails t unless
// what the memo charges is exactly what its entries were charged, its map and its list hold the
// same entries, and the charge is within the memo's bound.
func heldVersions(t *testing.T, memo *storedCutMemo) []int {
	t.Helper()
	var numbers []int
	var charged int64
	for element := memo.order.Front(); element != nil; element = element.Next() {
		entry := element.Value.(*storedCutEntry)
		if memo.items[entry.key] != element {
			t.Fatalf("version %d is in the memo's list but its map names another element", entry.key.number)
		}
		numbers = append(numbers, entry.key.number)
		charged += entry.size
	}
	if len(memo.items) != len(numbers) {
		t.Fatalf("the memo's map holds %d entries and its list %d", len(memo.items), len(numbers))
	}
	if memo.held != charged {
		t.Fatalf("the memo charges %d bytes, and its entries were charged %d", memo.held, charged)
	}
	if memo.held > memo.limit {
		t.Fatalf("the memo charges %d bytes, past its bound of %d", memo.held, memo.limit)
	}
	return numbers
}

// The memo of stored versions' cuts (storedCuts) is held to its bound in bytes, under a bound of
// three entries here rather than 32 MiB. Each store keeps what is charged within the bound,
// dropping the least recently used entries first, as many as the new entry needs; a read marks
// its entry the most recently used; an entry charged more than the whole bound is not kept, and
// the entries already kept stay; a version whose markdown the parser refuses is parsed once and
// then served as refused; and an entry is served only for the markdown it was cut from.
func TestTheStoredCutMemoKeepsTheMostRecentlyUsedCutsWithinItsBound(t *testing.T) {
	const markdownBytes = 100
	entry := int64(markdownBytes + storedCutOverheadBytes)
	memo := newStoredCutMemo(3 * entry)
	key := func(number int) storedVersionKey {
		return storedVersionKey{artifactID: "2e5d211a-b79d-48a7-9e08-3eece00fffed", number: number}
	}
	// markdown is version number's text, size bytes long; cutOf is a cut of it with no decisions,
	// charged its bytes and one overhead.
	markdown := func(number, size int) string {
		prefix := fmt.Sprintf("version %d ", number)
		return prefix + strings.Repeat("x", size-len(prefix))
	}
	cutOf := func(number, size int) documentCut {
		return documentCut{markdown: markdown(number, size), cuts: map[sectionKey]decisionCut{}, read: true}
	}
	holds := func(want string) {
		t.Helper()
		if got := fmt.Sprint(heldVersions(t, memo)); got != want {
			t.Fatalf("the memo holds versions %s, most recently used first, want %s", got, want)
		}
	}

	for number := 1; number <= 3; number++ {
		memo.put(key(number), cutOf(number, markdownBytes))
		heldVersions(t, memo)
	}
	holds("[3 2 1]")
	if memo.held != memo.limit {
		t.Fatalf("three entries are charged %d bytes, want the whole bound %d", memo.held, memo.limit)
	}

	// A read marks version 1 the most recently used, so the next store drops version 2.
	if cut, held := memo.get(key(1), markdown(1, markdownBytes)); !held || !cut.read {
		t.Fatalf("the read of version 1 = %#v, %v; want its cut", cut, held)
	}
	holds("[1 3 2]")
	memo.put(key(4), cutOf(4, markdownBytes))
	holds("[4 1 3]")
	memo.put(key(5), cutOf(5, markdownBytes))
	holds("[5 4 1]")

	// An entry charged two entries' bytes drops the two least recently used.
	memo.put(key(6), cutOf(6, int(2*entry)-storedCutOverheadBytes))
	holds("[6 5]")

	// An entry charged more than the whole bound is not kept, and drops nothing.
	memo.put(key(7), cutOf(7, int(memo.limit)))
	holds("[6 5]")
	if _, held := memo.get(key(7), markdown(7, int(memo.limit))); held {
		t.Fatalf("the memo serves version 7, charged past its bound of %d", memo.limit)
	}

	// A refused version is parsed once and then served as refused.
	refused := "[spec]: https://example.com/spec\n\n## A decision\n\nIts text.\n"
	if _, read := decisionCuts(refused); read {
		t.Fatalf("the parser reads %q; the test needs markdown it refuses", refused)
	}
	parsed := 0
	decisionCutsObserver = func(string) { parsed++ }
	t.Cleanup(func() { decisionCutsObserver = nil })
	for range 2 {
		if cut := memo.cut(key(8), refused); cut.read || len(cut.cuts) != 0 || cut.markdown != refused {
			t.Fatalf("the cut of the refused version 8 = %#v, want it refused, with no cuts", cut)
		}
	}
	if parsed != 1 {
		t.Fatalf("two cuts of the refused version 8 parsed it %d times, want once", parsed)
	}
	holds("[8 6]")

	// An entry is served only for the markdown it was cut from: another markdown of version 8 is
	// parsed, and its cut replaces the one kept.
	if _, held := memo.get(key(8), refused+"Rewritten.\n"); held {
		t.Fatal("the memo serves version 8's cut for another markdown")
	}
	if cut := memo.cut(key(8), refused+"Rewritten.\n"); parsed != 2 || cut.markdown != refused+"Rewritten.\n" {
		t.Fatalf("the cut of version 8's other markdown = %#v after %d parses, want it parsed again", cut, parsed)
	}
	holds("[8 6]")
}
