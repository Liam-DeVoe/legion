// The family fence, the owner check and adoption: which consumers a record of a logical name may
// name, which consumer the server holds is that logical name's own, and which candidate an open
// operation may publish. Names applies the fence to every record it reads (Check), the owner
// check to every read and delete of a name a record supplies (info, deleteOwned), and the adoption
// check to every candidate it did not see created (adopt).

package consumernames

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nats-io/nats.go/jetstream"
)

// fixed is logical's fixed name (Config.Fixed).
func (n *Names) fixed(logical string) string {
	if n.cfg.Fixed != nil {
		return n.cfg.Fixed(logical)
	}
	return n.cfg.Prefix(logical)
}

// freshOf reports whether name is a fresh name of logical: Prefix(logical), '~', and a NUID's
// characters ([0-9A-Za-z]) alone (FreshName).
func (n *Names) freshOf(logical, name string) bool {
	nuid, ok := strings.CutPrefix(name, n.cfg.Prefix(logical)+"~")
	return ok && nuid != "" && !strings.ContainsFunc(nuid, func(r rune) bool {
		return !('0' <= r && r <= '9' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z')
	})
}

// Check refuses, as ErrMalformedRecord naming the field and the name, a record of logical that
// names a consumer outside logical's own family: its current, each retired name and an open
// operation's candidate must be fresh names of logical (freshOf), and an open operation's old name
// one of those or logical's fixed name, which the cutover's convert replaces. Each is a name the
// protocol creates, binds, updates or deletes, so a record that names another family's consumer (a
// daemon's durable, a session's of another prefix) never gets one of those calls on it. It is the
// creator's own check of every record it reads (read), so a reader of the store that judges a record
// healthy (an operator's status, census or cleanup) judges it as the creator does.
func (n *Names) Check(logical string, record Record) error {
	refuse := func(field, name string) error {
		return fmt.Errorf("%w: the key %s's %s %q is no %s~<nuid> name", ErrMalformedRecord, logical, field, name, n.cfg.Prefix(logical))
	}
	if record.Current != "" && !n.freshOf(logical, record.Current) {
		return refuse("current", record.Current)
	}
	for _, name := range record.Retired {
		if !n.freshOf(logical, name) {
			return refuse("retired name", name)
		}
	}
	if op := record.Op; op != nil {
		if op.Candidate != "" && !n.freshOf(logical, op.Candidate) {
			return refuse("candidate", op.Candidate)
		}
		if op.Old != "" && op.Old != n.fixed(logical) && !n.freshOf(logical, op.Old) {
			return fmt.Errorf("%w: the key %s's old name %q is neither a %s~<nuid> name nor its fixed name %s", ErrMalformedRecord, logical, op.Old, n.cfg.Prefix(logical), n.fixed(logical))
		}
	}
	return nil
}

// metadata is the metadata every consumer of logical carries (Config.Metadata).
func (n *Names) metadata(logical string) map[string]string {
	if n.cfg.Metadata != nil {
		return n.cfg.Metadata(logical)
	}
	return map[string]string{LogicalMetadataKey: logical}
}

// Owns is nil when info, a consumer a record of logical names, is logical's own: its metadata
// holds each pair the family's consumers carry (Config.Metadata), or it is logical's fixed name.
// Anything else is ErrMalformedRecord naming the consumer and the pair it lacks: a record that
// names another key's consumer is no record of logical's, however well formed, and nothing reads,
// binds, updates, adopts or deletes that consumer for logical. A family that reads a consumer's
// info again on its own, to correct or bind it, asks here too.
func (n *Names) Owns(logical string, info *jetstream.ConsumerInfo) error {
	if info.Name == n.fixed(logical) {
		return nil
	}
	for key, want := range n.metadata(logical) {
		if got, ok := info.Config.Metadata[key]; !ok || got != want {
			return fmt.Errorf("%w: the consumer %s that the key %s's record names carries %s=%q, not %q", ErrMalformedRecord, info.Name, logical, key, got, want)
		}
	}
	return nil
}

// info reads consumer name, which a record of logical names, and answers it only when it is
// logical's own (Owns); a consumer the server does not hold is ErrNotFound.
func (n *Names) info(ctx context.Context, logical, name string) (*jetstream.ConsumerInfo, error) {
	info, err := n.cfg.Server.Info(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := n.Owns(logical, info); err != nil {
		return nil, err
	}
	return info, nil
}

// deleteOwned deletes consumer name, which a record of logical names, once its info shows it is
// logical's own (info); one the server does not hold is no error, and one of another key is
// ErrMalformedRecord and is not deleted.
func (n *Names) deleteOwned(ctx context.Context, logical, name string) error {
	if _, err := n.info(ctx, logical, name); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	return n.cfg.Server.Delete(ctx, name)
}

// adopt publishes sel's candidate, which the server holds as info and this call did not see
// created (Step 0, or a create whose outcome was unknown), once it is as the operation reserved it
// (adoptable); one that is not is refused, naming it, and the operation stays open, as it does
// when the family's check could not read the candidate.
func (n *Names) adopt(ctx context.Context, sel Selection, info *jetstream.ConsumerInfo, operation Operation) (Selection, error) {
	if err := n.adoptable(ctx, sel, info); err != nil {
		n.observeErr(operation, err)
		return sel, stepError(sel.Logical, operation, sel.Record.Op.Candidate, err)
	}
	return n.publish(ctx, sel, info, operation, OutcomeAdopted)
}

// adoptable refuses, as ErrUnexpectedConsumer, a candidate whose start is not the one sel's
// operation reserved, which no update can change: the native policy of a native create, else a
// start by sequence no later than the saved start (a create clamps a start past the stream's end to
// an earlier one, which can only deliver again); and answers the family's own check
// (Config.Adopt), a refusal or a read that failed. Its owner is checked already (Owns), as every
// consumer a record names is.
func (n *Names) adoptable(ctx context.Context, sel Selection, info *jetstream.ConsumerInfo) error {
	op, config := sel.Record.Op, info.Config
	var why string
	switch {
	case op.Native == NativeAll && config.DeliverPolicy != jetstream.DeliverAllPolicy:
		why = fmt.Sprintf("its deliver policy is %s, not the native first create's all", config.DeliverPolicy)
	case op.Native == NativeNew && config.DeliverPolicy != jetstream.DeliverNewPolicy:
		why = fmt.Sprintf("its deliver policy is %s, not the native first create's new", config.DeliverPolicy)
	case op.Native == "" && config.DeliverPolicy != jetstream.DeliverByStartSequencePolicy:
		why = fmt.Sprintf("its deliver policy is %s, not by start sequence", config.DeliverPolicy)
	case op.Native == "" && (config.OptStartSeq == 0 || sel.Record.Start > 0 && config.OptStartSeq > sel.Record.Start):
		why = fmt.Sprintf("it starts at %d, past the saved start %d", config.OptStartSeq, sel.Record.Start)
	}
	if why != "" {
		return fmt.Errorf("%w: %s %s", ErrUnexpectedConsumer, info.Name, why)
	}
	if n.cfg.Adopt != nil {
		return n.cfg.Adopt(ctx, sel.Logical, info)
	}
	return nil
}
