package kvrecords_test

import (
	"crypto/ed25519"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/bus/kvrecords"
	"github.com/sjawhar/envoy/internal/bus/signedrecord"
)

// copiesRead is infra/README.md's read of the records-only copies a key signed ("Rotating the hold
// signing key", step 3), with <old> the key's id and <copies> the directory the operator keeps the
// copies under: each copy is a directory whose manifest.sig opens with its key's id and a dot, or
// holds no dot when written before signatures named their key.
const copiesRead = `grep -rlE --include=manifest.sig '^(<old>\.|[^.]+$)' <copies>`

// The hold's records-only copies are signed with the operator's hold key, so a key rotation reads
// them too: infra/README.md's read, run as written over a directory of copies, lists the copy the
// old key signed and one written before signatures named their key, and not the new key's copy;
// and the old key's copy stops reading once that key leaves the list, naming its id. Fails while
// the README's step 3 reads only the five tables, as at e481b26500 (the README does not hold the
// read), or with a read that misses copies with no key id.
func TestTheRotationReadFindsTheCopiesTheOldKeySigned(t *testing.T) {
	readme, err := os.ReadFile("../../../infra/README.md")
	require.NoError(t, err)
	require.True(t, strings.Contains(string(readme), copiesRead), "infra/README.md's rotation step 3 does not hold the copies' read word for word: %s", copiesRead)

	r := newRig(t)
	r.fillBucket(t)
	copies := t.TempDir()
	old, oldKey := newKey(t)
	write := func(name string, key ed25519.PrivateKey) (string, kvrecords.Manifest) {
		t.Helper()
		dir := filepath.Join(copies, name)
		manifest, err := kvrecords.Write(t.Context(), r.operator, bucket, dir, key)
		require.NoError(t, err)
		return dir, manifest
	}
	oldCopy, oldManifest := write("old", oldKey)
	write("new", r.key)
	legacy, _ := write("legacy", oldKey)
	sigPath := filepath.Join(legacy, kvrecords.SignatureFile)
	sig, err := os.ReadFile(sigPath)
	require.NoError(t, err)
	_, bare, named := strings.Cut(string(sig), ".")
	require.True(t, named, "the copy's signature %q names no key", sig)
	require.NoError(t, os.WriteFile(sigPath, []byte(bare), 0o600))

	command := strings.NewReplacer("<old>", signedrecord.KeyID(old), "<copies>", copies).Replace(copiesRead)
	out, err := exec.Command("sh", "-c", command).Output()
	require.NoError(t, err, "the read %q", command)
	var found []string
	for _, line := range strings.Fields(string(out)) {
		rel, err := filepath.Rel(copies, line)
		require.NoError(t, err)
		found = append(found, rel)
	}
	slices.Sort(found)
	require.Equal(t, []string{filepath.Join("legacy", kvrecords.SignatureFile), filepath.Join("old", kvrecords.SignatureFile)}, found,
		"the read by the old key's id lists the old key's copy and the copy with no key id, and not the new key's")

	_, _, err = kvrecords.Read(oldCopy, bucket, r.keys, oldManifest.ManifestSHA256)
	var unlisted *signedrecord.UnlistedKeyError
	require.ErrorAs(t, err, &unlisted, "the old key's copy, read with the new key alone")
	require.Equal(t, signedrecord.KeyID(old), unlisted.KeyID)
}
