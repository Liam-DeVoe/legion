package api

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// recordField is one field of a query's record as docs' IssueTree.QueryRecord writes it: its byte
// length, one space, then its bytes.
func recordField(field string) string { return strconv.Itoa(len(field)) + " " + field }

// The spec query takes the component and interface blocks an issue proposes (R22), in the record's
// one layout, and QueryDefinitionVersion stays 1: a store that holds no component block reads the
// record TestTheRecordOfOneNodeIsItsLayoutByteForByte pins, byte for byte, as every record an
// approval before this change covered was built; and a component block written beside the
// requirement stands after it in the walk's order as one more node of the same seven fields. No
// approval of a version written under one definition reads as one of another, so the version does
// not rise.
func TestTheSpecQueryTakesComponentsUnderOneDefinition(t *testing.T) {
	if graphstore.QueryDefinitionVersion != 1 {
		t.Fatalf("QueryDefinitionVersion is %d, want 1: adding the component to the spec query changes no record's layout", graphstore.QueryDefinitionVersion)
	}
	database := storetest.Open(t)
	rig := &digestRig{database: database}
	const requirement, component = "PIN-1/spec#r1", "PIN-1/spec#billing"
	rig.state(t,
		blockTuple(requirement, graphstore.TuplePartOf, graphstore.KindIssue, "PIN-1", true),
		blockValue(requirement, refs.AttributeRank, "1V"),
		blockValue(requirement, graphstore.AttributeText, ":::requirement{#r1}\nThe words.\n:::\n"),
		blockValue(requirement, "size", "1.5"),
		blockValue(requirement, "probe", "a b"),
		blockValue(requirement, refs.AttributePlacement, refs.PlacementAuthored),
		blockValue(requirement, graphstore.AttributeState, graphstore.StateLive),
		blockValue(requirement, graphstore.AttributeVisibility, graphstore.VisibilityProject),
		blockTuple(requirement, "serves", refs.BlockNodeKind, "PIN-1/spec#k1", true),
	)
	read := func() docs.QueryRecord {
		t.Helper()
		record, err := docs.ReadQueryRecord(context.Background(), database.Pool, "PIN-1", graphstore.QuerySpec, refs.TreeCut{})
		if err != nil {
			t.Fatalf("read the spec record of PIN-1: %v", err)
		}
		return record
	}
	if before := read(); string(before.Record) != pinnedRecord || before.Digest != pinnedDigest {
		t.Fatalf("with no component block the spec record of PIN-1 reads %q (%s), want the pinned record %q (%s)", before.Record, before.Digest, pinnedRecord, pinnedDigest)
	}

	const text = ":::component{#billing name=\"Billing\" kind=\"interface\" joins=\"bank shop\"}\nIt bills.\n:::\n"
	rig.state(t,
		blockTuple(component, graphstore.TuplePartOf, graphstore.KindIssue, "PIN-1", true),
		blockValue(component, refs.AttributeRank, "1W"),
		blockValue(component, graphstore.AttributeText, text),
		blockValue(component, "joins", "bank shop"),
		blockValue(component, graphstore.AttributeState, graphstore.StateLive),
		blockValue(component, graphstore.AttributeVisibility, graphstore.VisibilityProject),
	)
	values := recordField("joins") + recordField("bank shop") + recordField(graphstore.AttributeState) + recordField(graphstore.StateLive) +
		recordField(graphstore.AttributeVisibility) + recordField(graphstore.VisibilityProject)
	want := pinnedRecord + recordField(refs.BlockNodeKind+":"+component) + recordField("component") + recordField(graphstore.KindIssue+":PIN-1") +
		recordField("1W") + recordField(values) + recordField(text) + recordField(recordField(graphstore.TuplePartOf)+recordField(graphstore.KindIssue+":PIN-1"))
	after := read()
	if string(after.Record) != want {
		t.Errorf("with a component block the spec record of PIN-1 reads\n%q\nwant\n%q", after.Record, want)
	}
	if after.Digest == pinnedDigest || !strings.Contains(string(after.Record), "component") {
		t.Errorf("the component block leaves the spec digest at %s", after.Digest)
	}
}
