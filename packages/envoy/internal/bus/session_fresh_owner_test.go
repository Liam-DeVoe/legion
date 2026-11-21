package bus

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/contracts"
)

// A record that names another session's consumer never hands that consumer to its key: the
// independent tester's forgery (a well-formed, self-keyed record of ses_v naming ses_a's current,
// written by a principal that can store chosen bytes in the bucket) is refused as a malformed
// record by every session call of ses_v, and ses_a's consumer keeps its own filter and is never
// deleted. The control is the forged record read by a reader that takes a record's current on its
// name alone, which updates ses_a's consumer to ses_v's filter.
func TestARecordNamingAnotherSessionsConsumerNeverHandsItToItsKey(t *testing.T) {
	owner, consumers, store := freshSessionConsumersOnStream(t)
	ctx := context.Background()
	const victim, forger = "ses_a_owner", "ses_v_forger"
	publishStored(t, owner, contracts.LegacyAgentSubject(victim), "for the victim")
	victimSubjects := []string{contracts.LegacyAgentSubject(victim)}
	start, err := consumers.Ensure(ctx, victim, victimSubjects, 0)
	if err != nil {
		t.Fatal(err)
	}
	filters := func() []string {
		t.Helper()
		info, err := consumers.info(ctx, start.Name)
		if err != nil {
			t.Fatalf("read %s: %v", start.Name, err)
		}
		return slices.Clone(info.Config.FilterSubjects)
	}
	victimFilters := filters()

	// The forgery: a record of the forger's key naming the victim's consumer, written whole as a
	// principal with a chosen-bytes write of the bucket writes it.
	if _, err := store.Write(ctx, forger, consumernames.Record{Current: start.Name, Start: 1}, 0); err != nil {
		t.Fatal(err)
	}
	forgerSubjects := []string{contracts.LegacyAgentSubject(forger)}
	for name, call := range map[string]func() error{
		"Ensure": func() error { _, err := consumers.Ensure(ctx, forger, forgerSubjects, 0); return err },
		"UpdateFilter": func() error {
			return consumers.UpdateFilter(ctx, forger, forgerSubjects)
		},
		"RestartAtFloor": func() error { _, err := consumers.RestartAtFloor(ctx, forger, forgerSubjects); return err },
		"Info":           func() error { _, err := consumers.Info(ctx, forger); return err },
		"KeepListed": func() error {
			info, err := consumers.info(ctx, start.Name)
			if err != nil {
				return err
			}
			_, _, err = consumers.KeepListed(ctx, forger, victimSubjects, info)
			return err
		},
		"Delete": func() error { _, err := consumers.Delete(ctx, forger, nil); return err },
	} {
		if err := call(); !errors.Is(err, consumernames.ErrMalformedRecord) {
			t.Errorf("%s of the forger's key = %v, want ErrMalformedRecord", name, err)
		}
	}
	if got := filters(); !slices.Equal(got, victimFilters) {
		t.Fatalf("the victim's consumer %s filters %v after the forger's calls, want its own %v", start.Name, got, victimFilters)
	}

	// Control: a reader that takes the current on its name alone hands the victim's consumer to
	// the forger, as the protocol did before it read the consumer's metadata.
	record, _, err := store.Read(ctx, forger)
	if err != nil {
		t.Fatal(err)
	}
	info, err := consumers.info(ctx, record.Current)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := consumers.update(ctx, record.Current, info.Config, SessionConsumerFilters(forgerSubjects)); err != nil {
		t.Fatal(err)
	}
	if got := filters(); slices.Equal(got, victimFilters) {
		t.Fatalf("control: the name-only reader left %s on %v, want the forger's filter", start.Name, got)
	}
}
