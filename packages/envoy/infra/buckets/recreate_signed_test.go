package buckets

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/jsm.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/internal/bus"
)

// state is bucket's stream as the operator reads it: its created time and its message count.
func (h *heldBuckets) state(bucket string) (time.Time, uint64) {
	h.t.Helper()
	ctx := context.Background()
	nc, err := h.s.ConnectOperator(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		h.t.Fatal(err)
	}
	s, err := js.Stream(ctx, "KV_"+bucket)
	if err != nil {
		h.t.Fatalf("read KV_%s: %v", bucket, err)
	}
	info := s.CachedInfo()
	return info.Created, info.State.Msgs
}

// `buckets recreate` takes what its hold's backup covered from the backup's signed record, never
// from the manifest file alone: the latest done `buckets backup` entry of the hold, the manifest's
// sha256 that entry records, and each bucket's archive present, readable and hashing to the sha256
// the entry records for it. Anything less refuses, naming the bucket, and deletes nothing. Two
// backups that no longer cover envoy_interests, which holds 4 messages a listener wrote:
//   - the archive is gone: the backup took the bucket, then its data file was removed (a cleaned
//     directory), so the backup can no longer restore it;
//   - the manifest was edited to name the bucket, which the backup never took: its signed done entry
//     names envoy_roles alone.
//
// The control is the check as it stood: it read the manifest file alone, so each recreate deleted
// envoy_interests and its 4 messages (exit 0).
func TestHoldRecreateTakesItsCoverageFromTheSignedBackup(t *testing.T) {
	for _, row := range []struct {
		name  string
		taken []string
		spoil func(t *testing.T, dir string)
	}{
		{"the archive is gone", []string{bus.RoleClaimBucket, bus.InterestBucket}, func(t *testing.T, dir string) {
			data, err := jsm.SnapshotDataPath(filepath.Join(dir, bus.InterestBucket))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(data); err != nil {
				t.Fatal(err)
			}
		}},
		{"the manifest names a bucket the backup never took", []string{bus.RoleClaimBucket}, func(t *testing.T, dir string) {
			manifest := filepath.Join(dir, ManifestFile)
			raw, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			edited := strings.Replace(string(raw), `"buckets": {`, `"buckets": {
    "`+bus.InterestBucket+`": {"stream": "KV_`+bus.InterestBucket+`", "sha256": "00", "meta_sha256": "00", "last_seq": 4, "messages": 4},`, 1)
			if edited == string(raw) {
				t.Fatalf("setup: the manifest's shape is not the expected one:\n%s", raw)
			}
			if err := os.WriteFile(manifest, []byte(edited), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			h := newHeldBuckets(t)
			h.bigBucket(bus.InterestBucket, 4)
			created, msgs := h.state(bus.InterestBucket)
			if msgs != 4 {
				t.Fatalf("setup: KV_%s holds %d messages, want 4", bus.InterestBucket, msgs)
			}
			dir := h.backupInto("recreate", row.taken...)
			id := h.row().HoldID
			row.spoil(t, dir)
			code, stdout, stderr := h.run("recreate", "--hold", id, "--bucket", bus.InterestBucket)
			if code != 1 || !strings.Contains(stderr, bus.InterestBucket) {
				t.Fatalf("a recreate whose signed backup does not cover %s = %d, want 1 naming it\n%s%s", bus.InterestBucket, code, stdout, stderr)
			}
			if c, m := h.state(bus.InterestBucket); !c.Equal(created) || m != msgs {
				t.Fatalf("the refused recreate changed KV_%s: created %s with %d messages, was %s with %d", bus.InterestBucket, c, m, created, msgs)
			}
		})
	}
}
