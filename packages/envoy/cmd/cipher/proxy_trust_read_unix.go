//go:build unix

// packages/envoy/cmd/cipher/proxy_trust_read_unix.go
package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"syscall"
)

// errTrustStreamTooLong is readTrustSource's answer for a named pipe or a device that sends more
// than proxyCertFileMaxBytes.
var errTrustStreamTooLong = errors.New("sends more than the bound")

// readTrustSource is what the file at path holds, for the bundle writer: the files the bundle
// variables and WGETRC name, which the command's own clients read whatever kind of file they are.
// It opens the path without waiting (O_NONBLOCK), so a named pipe with no writer cannot hang
// agent-secrets before the command runs, and then looks at what it opened, so the kind it reads is
// the kind it holds. A regular file is read whole, as os.ReadFile reads it. Anything else is read
// as a stream, once the open no longer waits: a named pipe until its writer closes, or at once as
// empty where it has no writer, and a device such as /dev/null as it gives; a stream that sends
// more than proxyCertFileMaxBytes is read no further and answers errTrustStreamTooLong.
func readTrustSource(path string) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		syscall.Close(fd)
		return nil, &os.PathError{Op: "fstat", Path: path, Err: err}
	}
	if err := syscall.SetNonblock(fd, false); err != nil {
		syscall.Close(fd)
		return nil, &os.PathError{Op: "setnonblock", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var data bytes.Buffer
	if st.Mode&syscall.S_IFMT == syscall.S_IFREG {
		data.Grow(int(st.Size) + bytes.MinRead)
		_, err := data.ReadFrom(f)
		return data.Bytes(), err
	}
	if _, err := data.ReadFrom(io.LimitReader(f, proxyCertFileMaxBytes+1)); err != nil {
		return nil, err
	}
	if data.Len() > proxyCertFileMaxBytes {
		return nil, errTrustStreamTooLong
	}
	return data.Bytes(), nil
}
