// The list of every key a KV bucket's stream holds a message on (KeyValue.StoredKeys), beside its
// test: what a kvwatch read compares its copy against, so a key the bucket holds and the read's
// consumer has not delivered is read before the read completes.

package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// ErrIncompleteKeyList is a list of a bucket's keys whose pages do not add up to one list: the
// stream changed between two of its pages or after its last page's state was read, or a page
// answered fewer subjects than the total says remain (StoredKeys).
var ErrIncompleteKeyList = errors.New("the bucket's key list did not add up to one list")

// ErrKeyListBehind is a list of a bucket's keys from a copy of its stream that had not yet
// stored the sequence the list was asked through (StoredKeys).
var ErrKeyListBehind = errors.New("the bucket's key list came from a copy of its stream behind the sequence asked for")

// subjectsPage is one STREAM.INFO answer with its subject list (nats-server v2.15.1
// jetstream_api.go:503-507): the page's place in the whole list, and the stream's state with that
// page of its subjects.
type subjectsPage struct {
	Error *jetstream.APIError    `json:"error,omitempty"`
	Total int                    `json:"total"`
	State *jetstream.StreamState `json:"state,omitempty"`
}

// StoredKeys lists every key the bucket's stream holds a message on, from a copy of the stream
// that has stored at least sequence through: the stream's STREAM.INFO, asked for its subjects under
// `$KV.<bucket>.>` (subjects_filter), page by page. The read barrier's key (BarrierKey) is left
// out, since no record of the bucket is stored under it.
//
// The server counts the messages it stores on each subject from its per-subject index
// (nats-server v2.15.1 jetstream_api.go:2206-2245, filestore.go:5093-5106, memstore.go:1002-1024),
// and a delete or purge marker is a message, so a key whose newest message is a marker is listed
// like any other. A caller that wants only the keys holding a value reads each listed key it
// lacks (HeldFromLeader, which answers a marker as nats.ErrKeyNotFound).
//
// Which copy of the stream answers is nats-server's: its leader, but also one of its members while
// the stream has no leader, or a new member while a stream of one replica scales up
// (jetstream_api.go:2100-2134). Each page names the last sequence the answering copy has stored,
// so a page whose copy has not stored through is refused (ErrKeyListBehind): a copy that has
// stored through has applied every message up to it, so it holds a message on every key that held
// a record at through, whether that record or a later one.
//
// A list of more than JSMaxSubjectDetails subjects, 100,000 (jetstream_api.go:494-495), comes in
// pages of the subjects sorted by name (jetstream_api.go:2214-2243), each read from the stream as
// it is when asked for. A key stored or removed between two pages can shift a later page past a
// subject, so the pages are one list only when the stream's state is the same in each and they add
// up to the total the first one gave. One answer reads the stream's state (jetstream_api.go:2192)
// before its subjects (:2207), under separate store locks (filestore.go:10110-10128, 5085-5106),
// so the last page's subjects can be newer than the state it names: a subject that sorts before
// the page's offset expires and one after it is added, and a key moves into the range an earlier
// page already read. So a list of more than one page asks the stream once more after its last
// page, for its state alone, and that state must be the first page's too. A list of one page needs
// no second ask: its subjects come from one read of the store's per-subject index, taken after
// the state whose last sequence is checked against through, so they are the store as one moment
// after through holds it. A list whose pages or that last ask differ, or that falls short of the
// total, is ErrIncompleteKeyList.
//
// ctx bounds the whole list, every page included. A ctx carrying no deadline is bounded by
// RequestTimeout, as every other read of a bucket's stream through this handle is. The request is
// sent to the default JetStream API prefix (`$JS.API.`), as the handle's own jetstream context,
// which wrapKeyValue builds with jetstream.New, sends every other request; nats.go's own paged
// StreamInfo is not used because it neither compares its pages nor stops at a page that answers
// fewer subjects than remain (nats.go@v1.53.1 jetstream/stream.go:457-502).
func (kv KeyValue) StoredKeys(ctx context.Context, through uint64) ([]string, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, RequestTimeout)
		defer cancel()
	}
	bucket := kv.Bucket()
	prefix := "$KV." + bucket + "."
	conn := kv.streams.Conn()
	keys, err := storedKeys(prefix, through, func(offset int) (subjectsPage, error) {
		request, err := json.Marshal(struct {
			SubjectsFilter string `json:"subjects_filter"`
			Offset         int    `json:"offset,omitempty"`
		}{prefix + ">", offset})
		if err != nil {
			return subjectsPage{}, err
		}
		answer, err := conn.RequestWithContext(ctx, "$JS.API.STREAM.INFO.KV_"+bucket, request)
		if err != nil {
			return subjectsPage{}, err
		}
		var page subjectsPage
		if err := json.Unmarshal(answer.Data, &page); err != nil {
			return subjectsPage{}, fmt.Errorf("decode the stream's answer: %w", err)
		}
		if page.Error != nil {
			if page.Error.ErrorCode == jetstream.JSErrCodeStreamNotFound {
				return subjectsPage{}, fmt.Errorf("%w: %w", nats.ErrStreamNotFound, page.Error)
			}
			return subjectsPage{}, page.Error
		}
		if page.State == nil {
			return subjectsPage{}, errors.New("the stream's answer holds no state")
		}
		return page, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list the keys of KV bucket %s from its stream: %w", bucket, err)
	}
	return keys, nil
}

// storedKeys collects the keys under prefix that read's pages list, read from offset 0 on, as
// StoredKeys documents.
func storedKeys(prefix string, through uint64, read func(offset int) (subjectsPage, error)) ([]string, error) {
	var keys []string
	var first jetstream.StreamState
	listed := map[string]struct{}{}
	for offset, total, pages := 0, 0, 1; ; pages++ {
		page, err := read(offset)
		if err != nil {
			return nil, err
		}
		if err := current(page, through); err != nil {
			return nil, err
		}
		state := *page.State
		if offset == 0 {
			first, total = state, page.Total
		} else if err := sameList(page, offset, first, total); err != nil {
			return nil, err
		}
		for subject := range state.Subjects {
			key, ok := strings.CutPrefix(subject, prefix)
			if !ok {
				return nil, fmt.Errorf("the stream listed subject %s, outside %s>", subject, prefix)
			}
			if _, again := listed[key]; again {
				return nil, fmt.Errorf("%w: key %s is listed twice", ErrIncompleteKeyList, key)
			}
			listed[key] = struct{}{}
			if key != BarrierKey {
				keys = append(keys, key)
			}
		}
		offset += len(state.Subjects)
		if offset >= total {
			if pages == 1 {
				return keys, nil
			}
			// The stream's state once more, read after the last page's subjects: an offset past
			// the list answers the state with no subject.
			last, err := read(offset)
			if err != nil {
				return nil, err
			}
			if err := current(last, through); err != nil {
				return nil, err
			}
			if err := sameList(last, offset, first, total); err != nil {
				return nil, err
			}
			return keys, nil
		}
		if len(state.Subjects) == 0 {
			return nil, fmt.Errorf("%w: a page at %d listed no subject of the %d the first page counted", ErrIncompleteKeyList, offset, total)
		}
	}
}

// current refuses page when the copy of the stream that answered it may lack a key that held a
// record at sequence through (ErrKeyListBehind), as StoredKeys documents.
func current(page subjectsPage, through uint64) error {
	if page.State.LastSeq < through {
		return fmt.Errorf("%w: the copy that answered has stored through sequence %d, the list was asked through %d",
			ErrKeyListBehind, page.State.LastSeq, through)
	}
	return nil
}

// sameList refuses page, asked for at offset, when the stream it read is not the one the first
// page read, of total subjects and state first (ErrIncompleteKeyList).
func sameList(page subjectsPage, offset int, first jetstream.StreamState, total int) error {
	state := *page.State
	if page.Total == total && state.FirstSeq == first.FirstSeq && state.LastSeq == first.LastSeq && state.Msgs == first.Msgs {
		return nil
	}
	return fmt.Errorf("%w: the answer at %d of %d subjects read the stream at sequences %d-%d, %d messages, where the first read %d-%d, %d messages, %d subjects",
		ErrIncompleteKeyList, offset, page.Total, state.FirstSeq, state.LastSeq, state.Msgs, first.FirstSeq, first.LastSeq, first.Msgs, total)
}
