package writerhold

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Census is the writer census's complete reading of the operator's account, $G: every stream
// with its configuration, as STREAM.LIST listed it, and every consumer of each, by stream name, as
// CONSUMER.LIST listed it.
type Census struct {
	Streams   []jetstream.StreamInfo
	Consumers map[string][]jetstream.ConsumerInfo
}

// TakeCensus is the writer census (§3a step 2), on nc, the operator's connection under the held
// grant: no writer inside the server targets what the hold protects. A user with STREAM.UPDATE or
// CONSUMER.CREATE can set up, before the hold, a write the server then makes on its own during it,
// so the census reads every stream of the account with its configuration (STREAM.LIST, paged),
// the stream names (STREAM.NAMES, paged), which must hold the same streams, and every consumer of
// each stream (CONSUMER.LIST.<stream>, paged). A list counts only when it is complete: a non-empty
// `missing` or `offline`, a page that adds no entry before offset+len reaches total, or a total
// that changes between pages is UNKNOWN, and so is a stream of present, the protected streams the
// step's own STREAM.INFO reads found, that STREAM.LIST does not hold, so a listing that saw nothing
// cannot read as clean. The one `missing` entry it accepts is a stream of missingAccepted, the
// streams the hold's workflow declares absent (MissingAccepted), while its own STREAM.INFO answers
// that stream absent: a stream assignment the server keeps with no stream running, which a
// restore that died leaves (STREAM.LIST counts it in its total, names it missing, and STREAM.NAMES
// lists it). Such a stream runs no consumer, so the census lists none of it. Any other `missing`
// entry is UNKNOWN.
//
// The protected subjects are $KV.>, every KV bucket's, and the subjects of every protected stream
// as its listed configuration holds them (ENVOY_NOTIFICATIONS' among them); the protected streams
// are ENVOY_NOTIFICATIONS and every KV_ stream. The census refuses, naming the stream or consumer:
// a republish or subject transform whose destination is not a literal subject (a wildcard token,
// a $<digits> token or a {{...}} function builds its output from the source subject's tokens, so
// its spelling does not show what it writes) or is a literal one that overlaps a protected
// subject; a push consumer whose deliver_subject overlaps a protected subject; a protected stream
// with sources or a mirror; and a protected stream with allow_msg_schedules, since a persisted
// schedule's target must be one of its own stream's subjects (nats-server stream.go:6874-6906),
// so a schedule writes a protected subject only from a protected stream.
func TakeCensus(ctx context.Context, nc *nats.Conn, present, missingAccepted []string) (*Census, error) {
	return takeCensus(ctx, nc, StepCensus, present, missingAccepted)
}

// takeCensus is the census in step: StepCensus for the hold's census, StepPostcheck for the
// postcheck's.
func takeCensus(ctx context.Context, nc *nats.Conn, step string, present, missingAccepted []string) (*Census, error) {
	accept := func(ctx context.Context, missing []string) error {
		return absentAssignments(ctx, nc, step, missing, missingAccepted)
	}
	streams, missing, err := listAll(ctx, nc, step, natsserver.JSApiStreamList,
		func(page *listPage[jetstream.StreamInfo]) []jetstream.StreamInfo { return page.Streams },
		func(info jetstream.StreamInfo) string { return info.Config.Name }, accept)
	if err != nil {
		return nil, err
	}
	names, _, err := listAll(ctx, nc, step, natsserver.JSApiStreams,
		func(page *listPage[string]) []string { return page.Streams },
		func(name string) string { return name }, nil)
	if err != nil {
		return nil, err
	}
	listed := make([]string, 0, len(streams))
	for _, info := range streams {
		listed = append(listed, info.Config.Name)
	}
	if err := sameStreams(step, append(slices.Clone(listed), missing...), names); err != nil {
		return nil, err
	}
	for _, name := range present {
		if !slices.Contains(listed, name) {
			return nil, unknownf(step, nil, "STREAM.LIST does not hold %s, which its STREAM.INFO read found present", name)
		}
	}
	census := &Census{Streams: streams, Consumers: make(map[string][]jetstream.ConsumerInfo, len(streams))}
	for _, name := range listed {
		consumers, _, err := listAll(ctx, nc, step, fmt.Sprintf(natsserver.JSApiConsumerListT, name),
			func(page *listPage[jetstream.ConsumerInfo]) []jetstream.ConsumerInfo { return page.Consumers },
			func(info jetstream.ConsumerInfo) string { return info.Name }, nil)
		if err != nil {
			return nil, err
		}
		census.Consumers[name] = consumers
	}
	if err := writersInside(step, census); err != nil {
		return nil, err
	}
	return census, nil
}

// absentAssignments accepts missing, the names one STREAM.LIST page answered `missing`, only when
// each is a stream of accepted that its STREAM.INFO answers absent, in step: a name outside
// accepted is UNKNOWN as any list that is not whole, and so is one whose STREAM.INFO finds it or
// does not answer.
func absentAssignments(ctx context.Context, nc *nats.Conn, step string, missing, accepted []string) error {
	for _, name := range missing {
		if !slices.Contains(accepted, name) {
			return unknownf(step, nil, "%s answered missing %v: the list is not complete", natsserver.JSApiStreamList, missing)
		}
		_, found, err := streamInfoOnce(ctx, nc, name)
		if err != nil {
			return unknownf(step, err, "%s answered %s missing, and its STREAM.INFO did not answer", natsserver.JSApiStreamList, name)
		}
		if found {
			return unknownf(step, nil, "%s answered %s missing, and its STREAM.INFO answers it present: the list is not complete", natsserver.JSApiStreamList, name)
		}
	}
	return nil
}

// sameStreams is UNKNOWN, in step, unless STREAM.LIST's streams, listed, and STREAM.NAMES's,
// names, are the same set: the two are answered from the same stream assignments, so a difference
// means one of them did not see a stream.
func sameStreams(step string, listed, names []string) error {
	var onlyListed, onlyNamed []string
	for _, name := range listed {
		if !slices.Contains(names, name) {
			onlyListed = append(onlyListed, name)
		}
	}
	for _, name := range names {
		if !slices.Contains(listed, name) {
			onlyNamed = append(onlyNamed, name)
		}
	}
	if len(onlyListed) > 0 || len(onlyNamed) > 0 {
		return unknownf(step, nil, "STREAM.LIST and STREAM.NAMES do not hold the same streams: only STREAM.LIST holds %v, only STREAM.NAMES holds %v", onlyListed, onlyNamed)
	}
	return nil
}

// listPage is one page of a paged JetStream list answer: STREAM.LIST's and STREAM.NAMES's entries
// are under `streams`, CONSUMER.LIST's under `consumers` (nats-server jetstream_api.go's
// JSApiStreamListResponse, JSApiStreamNamesResponse and JSApiConsumerListResponse).
type listPage[T any] struct {
	Error     *jetstream.APIError `json:"error,omitempty"`
	Total     int                 `json:"total"`
	Offset    int                 `json:"offset"`
	Limit     int                 `json:"limit"`
	Missing   []string            `json:"missing,omitempty"`
	Offline   map[string]string   `json:"offline,omitempty"`
	Streams   []T                 `json:"streams"`
	Consumers []T                 `json:"consumers"`
}

// pageRequest is the request of one page: the entries from offset on.
type pageRequest struct {
	Offset int `json:"offset"`
}

// listAll reads every page of the paged list at subject, each entry named by key, and returns the
// entries once offset+len reaches the answer's total. Any answer that is not the whole list is
// UNKNOWN in step: an error, a `missing` list (clustered CONSUMER.LIST answers success with one
// for a consumer it could not read, jetstream_cluster.go:12003-12180), an `offline` list, an
// offset other than the one asked for, a total that changes between pages, an entry listed twice,
// more entries than the total, and a page that adds no entry before the total is reached. The one
// exception is a `missing` list that accept answers nil for (nil accept accepts none): its names
// are returned beside the entries and count toward the total and the next page's offset, since
// the server counts them so (STREAM.LIST's total is its stream assignments,
// jetstream_cluster.go:11994), and a name listed as an entry too is UNKNOWN.
func listAll[T any](ctx context.Context, nc *nats.Conn, step, subject string, entries func(*listPage[T]) []T, key func(T) string, accept func(context.Context, []string) error) ([]T, []string, error) {
	var all []T
	var missing []string
	seen := map[string]bool{}
	total := -1
	for {
		offset := len(all) + len(missing)
		var page listPage[T]
		if err := apiRequest(ctx, nc, subject, pageRequest{Offset: offset}, &page); err != nil {
			return nil, nil, unknownf(step, err, "%s at offset %d", subject, offset)
		}
		if page.Error != nil {
			return nil, nil, unknownf(step, page.Error, "%s at offset %d", subject, offset)
		}
		if len(page.Missing) > 0 {
			if accept == nil {
				return nil, nil, unknownf(step, nil, "%s at offset %d answered missing %v: the list is not complete", subject, offset, page.Missing)
			}
			if err := accept(ctx, page.Missing); err != nil {
				return nil, nil, err
			}
		}
		if len(page.Offline) > 0 {
			return nil, nil, unknownf(step, nil, "%s at offset %d answered offline %v: the list is not complete", subject, offset, page.Offline)
		}
		if page.Offset != offset {
			return nil, nil, unknownf(step, nil, "%s answered offset %d to a request at offset %d", subject, page.Offset, offset)
		}
		if total >= 0 && page.Total != total {
			return nil, nil, unknownf(step, nil, "%s's total changed from %d to %d between pages", subject, total, page.Total)
		}
		total = page.Total
		added := entries(&page)
		for _, entry := range added {
			name := key(entry)
			if seen[name] {
				return nil, nil, unknownf(step, nil, "%s listed %q twice", subject, name)
			}
			seen[name] = true
			all = append(all, entry)
		}
		for _, name := range page.Missing {
			if seen[name] {
				return nil, nil, unknownf(step, nil, "%s listed %q twice", subject, name)
			}
			seen[name] = true
			missing = append(missing, name)
		}
		switch counted := len(all) + len(missing); {
		case counted == total:
			return all, missing, nil
		case counted > total:
			return nil, nil, unknownf(step, nil, "%s listed %d entries of a total of %d", subject, counted, total)
		case len(added) == 0 && len(page.Missing) == 0:
			return nil, nil, unknownf(step, nil, "%s at offset %d added no entry, with %d of a total of %d listed: the list is not complete", subject, offset, counted, total)
		}
	}
}

// writersInside refuses, in step, the first writer inside the server the census finds aimed at a
// protected subject or stream (TakeCensus).
func writersInside(step string, census *Census) error {
	protected := protectedSubjects(census.Streams)
	for _, info := range census.Streams {
		cfg := info.Config
		if cfg.RePublish != nil {
			if err := destination(step, protected, fmt.Sprintf("stream %s's republish of %q", cfg.Name, cfg.RePublish.Source), cfg.RePublish.Destination); err != nil {
				return err
			}
		}
		if cfg.SubjectTransform != nil {
			if err := destination(step, protected, fmt.Sprintf("stream %s's subject transform of %q", cfg.Name, cfg.SubjectTransform.Source), cfg.SubjectTransform.Destination); err != nil {
				return err
			}
		}
		if !isProtectedStream(cfg.Name) {
			continue
		}
		if err := protectedStreamWriters(step, cfg); err != nil {
			return err
		}
	}
	for _, info := range census.Streams {
		for _, consumer := range census.Consumers[info.Config.Name] {
			deliver := consumer.Config.DeliverSubject
			if deliver == "" {
				continue
			}
			if subject, ok := overlapping(deliver, protected); ok {
				return refusef(step, "push consumer %s of stream %s delivers to %q, which overlaps the protected subject %q", consumer.Name, info.Config.Name, deliver, subject)
			}
		}
	}
	return nil
}

// destination refuses, in step, what's destination dest when it is not a literal subject or when
// it overlaps a protected subject.
func destination(step string, protected []string, what, dest string) error {
	if !literalSubject(dest) {
		return refusef(step, "%s writes to %q, which is not a literal subject: a wildcard, a $<digits> token or a {{...}} function builds its output from the source subject's tokens", what, dest)
	}
	if subject, ok := overlapping(dest, protected); ok {
		return refusef(step, "%s writes to %q, which overlaps the protected subject %q", what, dest, subject)
	}
	return nil
}

// protectedSubjects are $KV.> and the subjects of every protected stream of streams, as its listed
// configuration holds them.
func protectedSubjects(streams []jetstream.StreamInfo) []string {
	subjects := []string{kvSubjects}
	for _, info := range streams {
		if isProtectedStream(info.Config.Name) {
			subjects = append(subjects, info.Config.Subjects...)
		}
	}
	return subjects
}

// overlapping is the first of protected that subject could share a literal subject with, by
// nats-server's own SubjectsCollide.
func overlapping(subject string, protected []string) (string, bool) {
	for _, p := range protected {
		if natsserver.SubjectsCollide(subject, p) {
			return p, true
		}
	}
	return "", false
}

// literalSubject reports whether dest names one subject, whatever subject the server maps to it: a
// valid subject with no wildcard token (nats-server's IsValidPublishSubject), no $<digits> token
// (the legacy transform placeholder, subject_transform.go's indexPlaceHolders) and no {{...}}
// mapping function.
func literalSubject(dest string) bool {
	if !natsserver.IsValidPublishSubject(dest) || strings.Contains(dest, "{{") {
		return false
	}
	for token := range strings.SplitSeq(dest, ".") {
		if len(token) > 1 && token[0] == '$' {
			if _, err := strconv.Atoi(token[1:]); err == nil {
				return false
			}
		}
	}
	return true
}
