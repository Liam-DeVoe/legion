package api

import (
	"container/list"
	"sync"
)

// storedCutsBytes bounds what the cuts of stored versions this process keeps hold (storedCuts):
// 32 MiB. A cut's texts and headings are slices of its version's markdown, so an entry holds the
// whole markdown however few decisions it cuts, and the bound is on bytes rather than on entries:
// it holds the cuts of more than 600 stored versions of 25 to 43 KB each, at the overhead of 40
// decisions apiece (storedCutOverheadBytes). It is half the bound the display trees
// (pmdoc.DisplayCacheBytes) and the list answers (listAnswerCacheBytes) of the same process are
// held to: a bound a server carries beside its pools, not a target to fill.
const storedCutsBytes = 32 << 20

// storedCutOverheadBytes is what an entry costs beside its markdown, charged once for the entry
// and once for each decision it cuts: the list's element, the map's slot, the key and the entry
// itself, and for each decision its key, its cut, the 64 bytes of its sum and its slot in the
// cut's map. It is a round number above what those come to on a 64-bit build.
const storedCutOverheadBytes = 256

// storedCuts is the cut of each stored version a decisions view has read (scanStoredDecisions),
// by its document and its number, the parser's refusal included, so a version is parsed at most
// once while its cut stays here, however many views read it. The bulk of an entry is the bytes of
// its markdown (storedCutsBytes), so the memo holds the most recently used cuts within that bound
// and drops the least recently used first.
//
// Nothing in this repository but its tests changes a stored version's markdown or its renderer
// stamp. Of every `update artifact_versions` outside the tests, files/backfill.go sets `content`,
// a file version's bytes, migration 0041 sets doc_update_version, and migration 0062 sets
// markdown to itself, which leaves its text as it was, to fire the search trigger. No insert into
// artifact_versions updates on conflict, and renderer_version is written only by the insert of
// docs/mutation.go (docs.RenderStampColumns). The one delete, a document's removal
// (artifactRemovalClears), takes the document with its versions, whose cuts no view reads again
// and which leave as the least recently used. So a version's cut never goes stale, and the memo
// keeps no stamp anyway: the stamp is read with each row. An entry is served only for the markdown
// it was cut from all the same, which costs one comparison of two strings and no parse, so a row
// rewritten past this server - as the tests store a version the way a build of another renderer
// version did (respellVersion) - is cut again rather than read by another markdown's cut.
var storedCuts = newStoredCutMemo(storedCutsBytes)

// storedVersionKey names one stored version: its document, and its number.
type storedVersionKey struct {
	artifactID string
	number     int
}

// storedCutEntry is one kept cut with the key that finds it, so eviction can drop the map's entry
// from the list's own element, and what it was charged, so eviction gives back exactly that.
type storedCutEntry struct {
	key  storedVersionKey
	cut  documentCut
	size int64
}

// storedCutMemo is the cuts kept by their version, the least recently used first out, charged at
// most the limit it was made with. Every method takes its lock, so one memo serves every view of a
// process. A kept cut is shared by every view that reads it, and none writes to it. It is
// bytelru's list and map over a value of their own: bytelru keeps byte slices alone, and a cut is
// a map of slices into its markdown.
type storedCutMemo struct {
	limit int64

	mu    sync.Mutex
	held  int64
	items map[storedVersionKey]*list.Element
	order *list.List // of *storedCutEntry, the most recently used at the front
}

func newStoredCutMemo(limit int64) *storedCutMemo {
	return &storedCutMemo{limit: limit, items: map[storedVersionKey]*list.Element{}, order: list.New()}
}

// cut is key's markdown cut into its decisions: the cut kept for key when it was cut from that
// markdown, else decisionCuts' parse of it, which is then kept. The parse runs outside the lock,
// so two views that miss one version at once both parse it, and the second cut kept replaces the
// first.
func (m *storedCutMemo) cut(key storedVersionKey, markdown string) documentCut {
	if cut, held := m.get(key, markdown); held {
		return cut
	}
	cut := documentCut{markdown: markdown}
	cut.cuts, cut.read = decisionCuts(markdown)
	m.put(key, cut)
	return cut
}

// get is the cut kept for key when it was cut from markdown, marked the most recently used.
func (m *storedCutMemo) get(key storedVersionKey, markdown string) (documentCut, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	element, held := m.items[key]
	if !held {
		return documentCut{}, false
	}
	entry := element.Value.(*storedCutEntry)
	if entry.cut.markdown != markdown {
		return documentCut{}, false
	}
	m.order.MoveToFront(element)
	return entry.cut, true
}

// put keeps cut under key in place of any cut kept there, and drops the least recently used
// entries until what is charged fits the limit. An entry charged more than the whole limit is
// never kept, so one enormous version cannot empty the memo for every other.
func (m *storedCutMemo) put(key storedVersionKey, cut documentCut) {
	size := int64(len(cut.markdown)) + int64(1+len(cut.cuts))*storedCutOverheadBytes
	if size > m.limit {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if element, held := m.items[key]; held {
		m.remove(element)
	}
	m.items[key] = m.order.PushFront(&storedCutEntry{key: key, cut: cut, size: size})
	m.held += size
	for m.held > m.limit {
		m.remove(m.order.Back())
	}
}

// remove drops element's entry, and what it was charged. Its caller holds the lock.
func (m *storedCutMemo) remove(element *list.Element) {
	entry := m.order.Remove(element).(*storedCutEntry)
	delete(m.items, entry.key)
	m.held -= entry.size
}
