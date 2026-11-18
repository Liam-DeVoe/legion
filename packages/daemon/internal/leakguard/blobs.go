package leakguard

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// binaryHead is how much of a file git's diff reads to tell whether it is binary
// (xdiff-interface.c's FIRST_FEW_BYTES), and so how much of a blob blobBatch.head reads first.
const binaryHead = 8000

// binary is whether a file is binary as git's diff tells one with no attribute to say, from head,
// the file's first bytes: a zero byte in its first binaryHead bytes.
func binary(head []byte) bool {
	return bytes.IndexByte(head[:min(len(head), binaryHead)], 0) >= 0
}

// fileBytes is the largest text file the read of a commit's files takes, a merge's (mergeFiles)
// or a commit of one parent's (commitFiles): 32 MiB. A merge's read holds at most two such files
// at once, the merge's and one parent's, and a set of the merge's distinct lines, which is under
// 100 MiB at the bound for lines of ordinary length and some hundreds of MiB for a file of nothing
// but short distinct lines: a small part of the 19 GiB an issue pod's roles share at the default
// issue reservation (docs/kubernetes.md). It is over eight times the largest text file this
// repository tracks, a test fixture of 3.7 MiB
// (packages/envoy/internal/dispatch/pmdoc/testdata/fixtures.json, measured 2026-11-17), so that a
// lock file or a fixture a commit changes is far below it, and under the 50 MiB above which
// GitHub warns of a file a push adds.
const fileBytes = 32 << 20

// blobBound is fileBytes, held in a variable so that a test can lower it.
var blobBound int64 = fileBytes

// aboveBound is the failure of a text file above blobBound: the file at path, of size bytes, that
// whose names the owner of ("merge 1a2b3c4's"), in the read of a commit of the kind named
// ("merge", "commit"). It names the file and its size, and the file is not read (mergeFiles says
// why).
func aboveBound(whose, path string, size int64, kind string) error {
	return fmt.Errorf("%s file %s is %d bytes, more than the %d bytes a %s's file is read up to", whose, maskPath(path), size, blobBound, kind)
}

// blobBatch is one git cat-file --batch over the blobs ids names, read a blob at a time as git
// writes them, so that what is held is the blob being read and never the batch: one git answers a
// commit's hundreds of blobs, where a git apiece would be hundreds of processes. The blobs are
// read in the order of ids, each one's first binaryHead bytes first (head). A blob cat-file does
// not answer whole is an error, never a file read as empty, and the batch ends with close, which
// stops git where the reader stopped early.
type blobBatch struct {
	// ids are the blobs asked for, and read is how many of them have been read.
	ids  []string
	read int
	// out is git's standard output, through pipe, whose reading end close shuts.
	out  *bufio.Reader
	pipe *io.PipeReader
	// stop ends the context git runs under, and ran answers how git ended.
	stop context.CancelFunc
	ran  chan error
	// first holds the first bytes of the blob being read (head), one buffer for every blob: each
	// reader is done with one blob's head before it reads the next.
	first [binaryHead]byte
}

// readBlobs starts the batch of ids, read in dir. Where ids is empty it starts no git.
func (g *Guard) readBlobs(ctx context.Context, dir string, ids []string) *blobBatch {
	ctx, stop := context.WithCancel(ctx)
	pipe, into := io.Pipe()
	batch := &blobBatch{ids: ids, out: bufio.NewReader(pipe), pipe: pipe, stop: stop, ran: make(chan error, 1)}
	if len(ids) == 0 {
		into.Close()
		batch.ran <- nil
		return batch
	}
	go func() {
		var stderr strings.Builder
		status, _, err := g.run(ctx, child{
			argv:   gitArgv("cat-file", "--batch"),
			dir:    dir,
			stdin:  strings.NewReader(strings.Join(ids, "\n") + "\n"),
			stdout: into,
			stderr: &stderr,
		})
		if err == nil && status != 0 {
			err = fmt.Errorf("git cat-file exited %d: %s", status, strings.TrimSpace(stderr.String()))
		}
		// A read past the last thing git wrote meets git's own failure, where it failed.
		into.CloseWithError(err)
		batch.ran <- err
	}()
	return batch
}

// head reads the header of the next blob and its first binaryHead bytes, or all of it where it is
// shorter, which say whether it is binary (binary), and answers how many bytes of it are left.
// The bytes are the batch's own, good until the next head.
func (b *blobBatch) head() (rest int64, head []byte, err error) {
	if b.read == len(b.ids) {
		return 0, nil, errors.New("git cat-file was asked for fewer blobs than were read")
	}
	id := b.ids[b.read]
	b.read++
	// "<id> blob <size>", and "<id> missing" for no such object.
	header, err := b.out.ReadSlice('\n')
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return 0, nil, err
	}
	fields := strings.Fields(string(header))
	if len(fields) != 3 || fields[0] != id || fields[1] != "blob" {
		return 0, nil, fmt.Errorf("git cat-file read no blob %s", id)
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return 0, nil, fmt.Errorf("git cat-file read no blob %s", id)
	}
	head = b.first[:min(size, binaryHead)]
	if _, err := io.ReadFull(b.out, head); err != nil {
		return 0, nil, b.cut(err)
	}
	return size - int64(len(head)), head, nil
}

// passOver passes over the next blob, and answers whether it is binary (binary) and its size.
func (b *blobBatch) passOver() (bool, int64, error) {
	rest, head, err := b.head()
	if err != nil {
		return false, 0, err
	}
	isBinary, size := binary(head), int64(len(head))+rest
	return isBinary, size, b.skip(rest)
}

// text reads the next blob, which is the file at path that whose names the owner of ("merge
// 1a2b3c4's") in the read of a commit of the kind named ("merge"), and answers its content and
// true where it is text; a binary one it passes over, answering false. A text file above
// blobBound is an error that names it, its owner and its size, and is not read (aboveBound).
func (b *blobBatch) text(path, whose, kind string) (string, bool, error) {
	rest, head, err := b.head()
	if err != nil {
		return "", false, err
	}
	if binary(head) {
		return "", false, b.skip(rest)
	}
	if size := int64(len(head)) + rest; size > blobBound {
		return "", false, aboveBound(whose, path, size, kind)
	}
	file, err := b.whole(head, rest)
	return file, err == nil, err
}

// whole reads the rest of the blob head read, rest bytes of it, and answers the blob whole.
func (b *blobBatch) whole(head []byte, rest int64) (string, error) {
	var content strings.Builder
	content.Grow(len(head) + int(rest))
	content.Write(head)
	if _, err := io.CopyN(&content, b.out, rest); err != nil {
		return "", b.cut(err)
	}
	return content.String(), b.end()
}

// skip passes over the rest of the blob head read, rest bytes of it, unread.
func (b *blobBatch) skip(rest int64) error {
	if _, err := io.CopyN(io.Discard, b.out, rest); err != nil {
		return b.cut(err)
	}
	return b.end()
}

// end reads the newline git writes after each blob's content, which is where its size says the
// content ends.
func (b *blobBatch) end() error {
	end, err := b.out.ReadByte()
	if err == nil && end != '\n' {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		return b.cut(err)
	}
	return nil
}

// cut is the error of a blob whose content git did not write whole: git's own failure where git
// failed, and else the blob cut short.
func (b *blobBatch) cut(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("git cat-file answered blob %s cut short", b.ids[b.read-1])
	}
	return err
}

// close ends the batch and answers how it ended: with failed, the error the reader stopped at,
// where it stopped at one, after it stops git; and else with git's own failure, where git failed,
// once what git wrote after the last blob read is passed over.
func (b *blobBatch) close(failed error) error {
	defer b.stop()
	if failed == nil {
		_, failed = io.Copy(io.Discard, b.out)
	} else {
		b.stop()
		b.pipe.CloseWithError(failed)
	}
	if ran := <-b.ran; failed == nil {
		return ran
	}
	return failed
}
