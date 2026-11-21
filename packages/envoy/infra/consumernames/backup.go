package consumernames

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nats-io/nats.go"

	selection "github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/bus/kvrecords"
	"github.com/sjawhar/envoy/natsgrant"
)

// backup is `consumer-names backup --out DIR`: the selection bucket's walk, the records-only copy
// of its stream in kvrecords.Format (kvrecords.Write), written into DIR, a new directory, for
// create-bucket --after-loss --backup DIR to load after the bucket is lost (createbucket_backup.go
// createBucketReadWalk). It runs outside a hold: it reads the hold row and refuses unless the row
// is released (requirePhase, as status does), so its connection holds natsgrant.Operator(released),
// whose STREAM.INFO and STREAM.MSG.GET of the bucket's stream are the walk's only calls. It
// changes nothing on the hub and writes nothing to a hold's journal.
//
// It refuses an --out that exists, a file or a directory, empty or not, before it reads the row
// or reaches the hub, and writes nothing then. A walk that fails writes nothing either:
// kvrecords.Write stages its files in a directory of its own beside DIR, moves it to DIR only once
// every file is written (os.Rename, which refuses a DIR that appeared meanwhile), and removes it
// otherwise. A refused request's error names the subject in the server's words
// (connRefusal).
//
// It prints the walk's sha256: the sha256 of its SHA256SUMS, which names the sums of its manifest
// and its records file, as createBucketReadWalk computes it and create-bucket --after-loss records
// it, the recovery's backup_sha256 in its hold's selection record and its sentinel.
func (r *run) backup(ctx context.Context) error {
	if _, err := os.Lstat(r.o.out); !errors.Is(err, fs.ErrNotExist) {
		if err != nil {
			return fmt.Errorf("--out %s cannot be read (%v): backup writes its copy into a new directory", r.o.out, err)
		}
		return fmt.Errorf("--out %s exists already: backup writes its copy into a new directory, and changes nothing that is there", r.o.out)
	}
	if _, err := r.requirePhase(ctx, natsgrant.OperatorPhaseReleased); err != nil {
		return err
	}
	nc, _, err := r.conn(ctx)
	if err != nil {
		return err
	}
	manifest, err := r.backupWrite(ctx, nc, r.o.out)
	if err != nil {
		return connRefusal(nc, err)
	}
	sums, err := os.ReadFile(filepath.Join(r.o.out, kvrecords.SumsFile))
	if err != nil {
		return fmt.Errorf("the walk is written to %s, and its %s does not read: %w", r.o.out, kvrecords.SumsFile, err)
	}
	sum := sha256.Sum256(sums)
	fmt.Fprintf(r.stdout, "%s: the walk of %s, %d records through sequence %d, written to %s; backup sha256 %s\n",
		selection.SessionBucket, manifest.Stream, manifest.Records, manifest.LastSeq, r.o.out, hex.EncodeToString(sum[:]))
	return nil
}

// backupWrite takes the walk of the selection bucket's stream through nc into dir, the backup's
// one call of kvrecords.Write, which signs nothing: the copy's manifest names its records file's
// SHA-256, and its SHA256SUMS names the manifest's and the records file's.
func (r *run) backupWrite(ctx context.Context, nc *nats.Conn, dir string) (kvrecords.Manifest, error) {
	return kvrecords.Write(ctx, nc, selection.SessionBucket, dir)
}
