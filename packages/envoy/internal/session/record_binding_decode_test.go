package session

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
)

// DecodeEntry reads a stored record naming a key as the registration of the key it is stored
// under only when it names that key, a machine, a registration time and its projects (the key
// present, null or a list), and refuses every other such record with ErrEntryUnbound: one naming
// another key, the empty key or null, and a session's own interest row, which names its key, its
// machine and a time but no projects. A record naming no key is read as before where the reader
// does not require the key, and refused where it does. A value that is not JSON is the decode
// error itself, as it was before records named their key.
func TestDecodeEntryReadsOnlyARecordBoundToItsKey(t *testing.T) {
	updated := strconv.FormatInt(time.Now().UnixMilli(), 10)
	const info = `{"type":"io.nats.jetstream.api.v1.stream_info_response","config":{"name":"KV_envoy_sessions"},"created":"2026-11-21T00:00:00Z"}`
	for _, tc := range []struct {
		name      string
		value     string
		requireID bool
		unbound   bool
		malformed bool
		machine   string
	}{
		{name: "a record naming its key", value: `{"session_id":"ses_k","machine_id":"m","updated_at":` + updated + `,"projects":["PRIV"]}`, machine: "m"},
		{name: "a record naming its key, where the key is required", value: `{"session_id":"ses_k","machine_id":"m","updated_at":` + updated + `,"projects":["PRIV"]}`, requireID: true, machine: "m"},
		{name: "a record naming its key and null projects", value: `{"session_id":"ses_k","machine_id":"m","updated_at":` + updated + `,"projects":null}`, machine: "m"},
		{name: "an older build's record naming no key", value: `{"machine_id":"m","updated_at":` + updated + `}`, machine: "m"},
		{name: "an older build's record naming no key, where the key is required", value: `{"machine_id":"m","updated_at":` + updated + `}`, requireID: true, unbound: true},
		{name: "a stream info answer, which names no key", value: info},
		{name: "a stream info answer, where the key is required", value: info, requireID: true, unbound: true},
		{name: "a record naming another key", value: `{"session_id":"ses_other","machine_id":"m","updated_at":` + updated + `,"projects":[]}`, unbound: true},
		{name: "a record naming the empty key", value: `{"session_id":"","machine_id":"m","updated_at":` + updated + `,"projects":[]}`, unbound: true},
		{name: "a record naming a null key", value: `{"session_id":null,"machine_id":"m","updated_at":` + updated + `,"projects":[]}`, unbound: true},
		{name: "a record naming its key and no machine", value: `{"session_id":"ses_k","updated_at":` + updated + `,"projects":[]}`, unbound: true},
		{name: "a record naming its key and no registration time", value: `{"session_id":"ses_k","machine_id":"m","projects":[]}`, unbound: true},
		{name: "a record naming its key and a negative registration time", value: `{"session_id":"ses_k","machine_id":"m","updated_at":-1,"projects":[]}`, unbound: true},
		{name: "the session's own interest row", value: `{"session_id":"ses_k","machine_id":"m","dir":"","topics":["notifications.agent.ses_k"],"updated_at":` + updated + `}`, unbound: true},
		{name: "an empty value", value: ``, malformed: true},
		{name: "a value that is not JSON", value: `not json`, malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry, err := DecodeEntry("ses_k", []byte(tc.value), tc.requireID)
			switch {
			case tc.unbound:
				if !errors.Is(err, ErrEntryUnbound) {
					t.Fatalf("DecodeEntry(ses_k, %s, %t) = %+v, %v; want ErrEntryUnbound", tc.value, tc.requireID, entry, err)
				}
			case tc.malformed:
				if err == nil || errors.Is(err, ErrEntryUnbound) {
					t.Fatalf("DecodeEntry(ses_k, %q, %t) = %+v, %v; want the JSON decode error", tc.value, tc.requireID, entry, err)
				}
			default:
				if err != nil || entry.MachineID != tc.machine {
					t.Fatalf("DecodeEntry(ses_k, %s, %t) = %+v, %v; want the record, machine %q", tc.value, tc.requireID, entry, err, tc.machine)
				}
			}
		})
	}
}

// putOlderBuildRecord stores at key the record a build that wrote no session id stores: a machine
// and a registration time, and no key.
func putOlderBuildRecord(t *testing.T, conn *natsgo.Conn, key string) {
	t.Helper()
	js, err := conn.JetStream()
	if err != nil {
		t.Fatalf("JetStream: %v", err)
	}
	kv, err := js.KeyValue(bus.SessionBucket)
	if err != nil {
		t.Fatalf("open %s: %v", bus.SessionBucket, err)
	}
	value, err := json.Marshal(map[string]any{"machine_id": "shared-a", "title": key, "updated_at": time.Now().UnixMilli(), "projects": []string{"PUB"}})
	if err != nil {
		t.Fatalf("encode %s: %v", key, err)
	}
	if _, err := kv.Put(key, value); err != nil {
		t.Fatalf("store %s: %v", key, err)
	}
}

// A record an older build wrote names no key, so a reader cannot tell it from a copy of another
// session's record. A registry that does not require the key, every listener of a fleet still
// rolling onto this build, reads it as its session's. A registry that requires it, the edge's,
// reads it as no record, so a copy written as an older build writes reaches no edge reader.
func TestAnEdgeRegistryReadsARecordNamingNoKeyAbsent(t *testing.T) {
	client := setupNATS(t)
	shared := openBindingRegistry(t, client)
	edge := openBindingRegistry(t, client, WithSessionRequireID(true))
	if err := shared.Put("ses_bound", SessionEntry{MachineID: "edge-a", Title: "bound", Projects: []string{"PRIV"}}); err != nil {
		t.Fatalf("register ses_bound: %v", err)
	}
	putOlderBuildRecord(t, client.Conn, "ses_older")
	ctx := context.Background()

	entry, err := shared.Refresh(ctx, "ses_older")
	if err != nil || entry.MachineID != "shared-a" {
		t.Fatalf("the shared registry's Refresh(ses_older) = %+v, %v; want the older build's record", entry, err)
	}
	if _, state := shared.Lookup("ses_older"); state != Live {
		t.Fatalf("the shared registry's Lookup(ses_older) = %s, want live", state)
	}

	waitFor(t, 5*time.Second, func() bool {
		_, err := edge.Refresh(ctx, "ses_bound")
		return err == nil
	})
	requireAbsent(t, edge, "ses_older", "an older build's record naming no key")
	// The control: the edge reads a record naming its key as every registry does.
	if entry, err := edge.Refresh(ctx, "ses_bound"); err != nil || entry.MachineID != "edge-a" || !slices.Equal(entry.Projects, []string{"PRIV"}) {
		t.Fatalf("the edge registry's Refresh(ses_bound) = %+v, %v; want its record", entry, err)
	}
	if ids := listed(t, edge); !slices.Equal(ids, []string{"ses_bound"}) {
		t.Fatalf("the edge registry lists %v, want only ses_bound", ids)
	}
}

// A1 and N1 at the store: the server answers a JetStream API request (a stream's info) and a pull
// request (a 404 status with no data) onto two sessions' keys. The status answer is no JSON at
// all, which every registry reads as no record. The info answer names no key, as an older
// build's record does: the edge's registry, which requires the key, reads it as no record, and a
// shared one reads it as it always has, a record naming no registration time, whose session is
// gone.
func TestAJetStreamAnswerAndAnEmptyRecordReadAbsent(t *testing.T) {
	client := setupNATS(t)
	shared := openBindingRegistry(t, client)
	edge := openBindingRegistry(t, client, WithSessionRequireID(true))
	for _, key := range []string{"ses_info", "ses_status"} {
		if err := shared.Put(key, SessionEntry{MachineID: "edge-a", Title: key, Projects: []string{"PRIV"}}); err != nil {
			t.Fatalf("register %s: %v", key, err)
		}
		// The control: each reads live at the edge before the server writes over it.
		if entry, err := edge.Refresh(context.Background(), key); err != nil || entry.MachineID != "edge-a" {
			t.Fatalf("the edge's Refresh(%s) before the write = %+v, %v; want its record", key, entry, err)
		}
	}

	info := testnats.ServerWritesKey(t, client.Conn, bus.SessionBucket, "ses_info", testnats.StreamInfo("KV_"+bus.SessionBucket), nil)
	request, data := testnats.NoWaitPull(t, client.Conn, bus.SessionBucket)
	status := testnats.ServerWritesKey(t, client.Conn, bus.SessionBucket, "ses_status", request, data)
	if value := storedValue(t, client.Conn, "ses_status"); len(value) != 0 {
		t.Fatalf("the pull's status answer stored %q under ses_status, want an empty record", value)
	}
	for _, r := range []*SessionRegistry{shared, edge} {
		awaitApplied(t, r, "ses_info", info)
		awaitApplied(t, r, "ses_status", status)
	}

	requireAbsent(t, edge, "ses_info", "a stream info answer onto its key")
	requireAbsent(t, edge, "ses_status", "a pull status answer onto its key")
	requireAbsent(t, shared, "ses_status", "a pull status answer onto its key")
	if entry, state := shared.Lookup("ses_info"); state != Gone || entry.MachineID != "" || entry.Projects != nil {
		t.Errorf("the shared registry's Lookup(ses_info) = %+v, %s; want a record naming nothing, gone, as before", entry, state)
	}
}

// A newer build may add a field to the record. A reader that refused fields it does not know would
// read every session that build registers as gone for the length of a rolling deploy, so a
// record naming its key decodes live whatever else it carries.
func TestANewerBuildsFieldDoesNotHideARecord(t *testing.T) {
	client := setupNATS(t)
	edge := openBindingRegistry(t, client, WithSessionRequireID(true))
	js, err := client.Conn.JetStream()
	if err != nil {
		t.Fatalf("JetStream: %v", err)
	}
	kv, err := js.KeyValue(bus.SessionBucket)
	if err != nil {
		t.Fatalf("open %s: %v", bus.SessionBucket, err)
	}
	value := []byte(`{"session_id":"ses_newer","machine_id":"edge-a","updated_at":` + strconv.FormatInt(time.Now().UnixMilli(), 10) +
		`,"projects":["PRIV"],"a_field_a_newer_build_writes":{"nested":[1,2]}}`)
	if _, err := kv.Put("ses_newer", value); err != nil {
		t.Fatalf("store ses_newer: %v", err)
	}
	if decoded, err := DecodeEntry("ses_newer", value, true); err != nil || decoded.MachineID != "edge-a" {
		t.Fatalf("DecodeEntry(ses_newer) = %+v, %v; want the record", decoded, err)
	}
	entry, err := edge.Refresh(context.Background(), "ses_newer")
	if err != nil || entry.MachineID != "edge-a" || !slices.Equal(entry.Projects, []string{"PRIV"}) || entry.SessionID != "ses_newer" {
		t.Fatalf("Refresh(ses_newer) = %+v, %v; want its record", entry, err)
	}
	if _, state := edge.Lookup("ses_newer"); state != Live {
		t.Fatalf("Lookup(ses_newer) = %s, want live", state)
	}
}
