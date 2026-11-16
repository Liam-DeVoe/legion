package bus

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/internal/contracts"
)

// The servers whose scan reads every filter are the releases that read a block by subject through
// stree.IntersectGSL, read from each release's server/filestore.go; every other version,
// v2.10.29 among them, and a version this cannot read, is widened for.
func TestServerScansEveryFilter(t *testing.T) {
	for version, want := range map[string]bool{
		"":            false,
		"garbage":     false,
		"2.15":        false,
		"1.4.0":       false,
		"2.10.29":     false,
		"v2.10.29":    false,
		"2.11.0":      false,
		"2.11.11":     false,
		"2.11.12":     true,
		"2.12.0":      false,
		"2.12.3":      false,
		"2.12.4":      true,
		"2.12.4-RC.1": true,
		"2.13.0":      false,
		"2.14.0":      true,
		"2.15.1":      true,
		"v2.15.1":     true,
		"3.0.0":       true,
	} {
		if got := serverScansEveryFilter(version); got != want {
			t.Errorf("serverScansEveryFilter(%q) = %t, want %t", version, got, want)
		}
	}
}

// scanSafeFilters widens every literal beside a `*` after the same tokens, and leaves a set with
// none as it is; what it returns holds no such pair.
func TestScanSafeFilters(t *testing.T) {
	const session = "ses_a"
	own := SessionConsumerFilters([]string{contracts.LegacyAgentSubject(session)})
	for _, tc := range []struct {
		name    string
		filters []string
		want    []string
	}{
		{
			name:    "a session's own agent subject in both forms",
			filters: own,
			want:    []string{"notifications.agent.*", "notifications.agent.*.ses_a"},
		},
		{
			name:    "with an issue's topic in both forms, whose tokens are all literal",
			filters: SessionConsumerFilters([]string{contracts.LegacyAgentSubject(session), "notifications.dispatch.issue.INFRA-12.>"}),
			want:    []string{"notifications.agent.*", "notifications.agent.*.ses_a", "notifications.dispatch.issue.INFRA-12.>", "notifications.dispatch.issue.INFRA.INFRA-12.>"},
		},
		{
			name:    "no wildcard",
			filters: []string{"notifications.agent.ses_a", "notifications.team.deploy"},
			want:    []string{"notifications.agent.ses_a", "notifications.team.deploy"},
		},
		{
			name:    "a literal with tokens after it",
			filters: []string{"a.*.c", "a.b.d"},
			want:    []string{"a.*.c", "a.*.d"},
		},
		{
			name:    "a widened literal the wildcard then contains",
			filters: []string{"a.*.c", "a.b.c"},
			want:    []string{"a.*.c"},
		},
		{
			name:    "a pair deeper than another",
			filters: []string{"a.x.*.d", "a.x.y", "a.z"},
			want:    []string{"a.x.*", "a.x.*.d", "a.z"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := scanSafeFilters(slices.Clone(tc.filters))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("scanSafeFilters(%q) = %q, want %q", tc.filters, got, tc.want)
			}
			if _, widened := widenBesideWildcard(slices.Clone(got)); widened {
				t.Fatalf("scanSafeFilters(%q) = %q, which still holds a literal beside a wildcard", tc.filters, got)
			}
		})
	}
}

// A pull hands a session every message but one on another session's private stream subject.
func TestHandsOver(t *testing.T) {
	const session = "ses_a"
	for subject, want := range map[string]bool{
		contracts.LegacyAgentSubject(session):                                          true,
		contracts.AgentSubject("INFRA", session):                                       true,
		contracts.AgentSubject(contracts.NoProject, session):                           true,
		contracts.LegacyAgentSubject("ses_b"):                                          false,
		contracts.AgentSubject("INFRA", "ses_b"):                                       false,
		ExceptionTopicPrefix + contracts.LegacyAgentSubject(session):                   false,
		SessionResumeSubject(session):                                                  false,
		KeptTopicPrefix + "role.lead-reviewer":                                         false,
		contracts.LegionNoticeTopicPrefix + "infra" + contracts.ControllerNoticeSuffix: true,
		"notifications.team.deploy":                                                    true,
		"notifications.dispatch.issue.INFRA.INFRA-12.comment.created":                  true,
	} {
		if got := handsOver(session, subject); got != want {
			t.Errorf("handsOver(%q, %q) = %t, want %t", session, subject, got, want)
		}
	}
}

// A consumer carrying the filters the session's own agent subject widens to (scanSafeFilters) is
// handed every session's old-form inbox, and its pull hands the session its own messages in both
// forms, in stream order, and acknowledges the rest itself, so its ack floor passes them. The
// server in this process scans every filter, so the widened consumer is made here; the natslive
// TestASessionConsumerHandsOverItsOwnMessagesInBothFormsBehindOtherSessionsTraffic has
// nats-server v2.10.29 make it.
func TestAPullHandsTheSessionNoneOfTheOtherSessionsAWidenedConsumerDelivers(t *testing.T) {
	owner, consumers := sessionConsumersOnStream(t)
	ctx := context.Background()
	const session = "ses_widened"
	other := publishStored(t, owner, contracts.LegacyAgentSubject("ses_other"), "another session's")
	oldForm := publishStored(t, owner, contracts.LegacyAgentSubject(session), "own, old form")
	publishStored(t, owner, contracts.LegacyAgentSubject("ses_other"), "another session's")
	newForm := publishStored(t, owner, contracts.AgentSubject("INFRA", session), "own, new form")
	last := publishStored(t, owner, contracts.LegacyAgentSubject("ses_other"), "another session's")
	name, filters, err := sessionConsumerTarget(session, []string{contracts.LegacyAgentSubject(session)})
	if err != nil {
		t.Fatal(err)
	}
	config := jetstream.ConsumerConfig{Durable: name, DeliverPolicy: jetstream.DeliverByStartSequencePolicy, OptStartSeq: other}
	consumers.applySettings(&config, scanSafeFilters(filters))
	if _, err := consumers.upsert(ctx, name, config, "create"); err != nil {
		t.Fatalf("create the widened consumer: %v", err)
	}
	pull := pullSession(t, consumers, session)
	for _, want := range []uint64{oldForm, newForm} {
		got := next(t, pull)
		if got.StreamSeq != want || !strings.HasSuffix(got.Subject, "."+session) {
			t.Fatalf("handed over %d on %s, want %d, the session's own", got.StreamSeq, got.Subject, want)
		}
		if err := got.Ack(); err != nil {
			t.Fatalf("ack %d: %v", got.StreamSeq, err)
		}
	}
	requireNoMessage(t, pull, 300*time.Millisecond)
	info, err := consumers.Info(ctx, session)
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if info.NumAckPending != 0 || info.AckFloor.Stream != last {
		t.Fatalf("the widened consumer awaits %d acks with its floor at %d, want none and %d: the pull acknowledges what it does not hand over",
			info.NumAckPending, info.AckFloor.Stream, last)
	}
}
