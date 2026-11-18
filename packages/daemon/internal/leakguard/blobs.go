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
			// git's own account of a failure is quoted as it wrote it, less the user and password
			// of a URL in it: a lazy fetch of a partial clone that fails names its promisor
			// remote, whose URL can carry a token (withoutUserinfo).
			err = fmt.Errorf("git cat-file exited %d: %s", status, withoutUserinfo(strings.TrimSpace(stderr.String())))
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

// passOver passes over the next blob unread.
func (b *blobBatch) passOver() error {
	rest, _, err := b.head()
	if err != nil {
		return err
	}
	return b.skip(rest)
}

// text reads the next blob as a text file of at most bound bytes and answers its content. A
// binary blob (binary) it passes over, answering no content, as of a file that holds no line. A
// text blob above bound it leaves unread and answers its size, over, which the caller refuses the
// read with, naming the file in its own words; over is 0 for every other blob. The batch reads
// nothing after a blob left unread, and the caller ends it there (close).
func (b *blobBatch) text(bound int64) (file string, over int64, err error) {
	rest, head, err := b.head()
	if err != nil {
		return "", 0, err
	}
	if binary(head) {
		return "", 0, b.skip(rest)
	}
	if size := int64(len(head)) + rest; size > bound {
		return "", size, nil
	}
	file, err = b.whole(head, rest)
	return file, 0, err
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
