// Forced-order hooks for nats-server v2.15.1. packages/envoy/scripts/forced-order.sh adds this
// file to the server package of a copy of the module, beside server.patch, which puts the calls
// into the server's own code. Not part of nats-server: MeasureHook is nil unless a test of
// internal/testnats's hooks.go sets it, and every call below then returns at once.
package server

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
)

// MeasureEvent is one instrumented point inside the server.
type MeasureEvent struct {
	// Point names the instrumented point, one of the names hooks.go declares.
	Point string
	// Gen is the consumer file store's generation, one per ConsumerStore call; 0 if none.
	Gen uint64
	// Name is the consumer's name, at a file store point.
	Name string
	// Store is the stream's store directory at a file store point, and the server's own store
	// directory (Options.StoreDir) at a server point.
	Store string
	// Path is the file or directory involved at a file store point. At api.worker it says
	// whether the request was served from the queue ("queued") or in its client's read loop
	// ("inline").
	Path string
	// State is the encoded consumer state involved (the bytes being written), if any.
	State []byte
	// Seq is the sequence involved, if any: at stream.apply, the stream sequence the entry stores.
	Seq uint64
	// Err is the error result, if any.
	Err error
	// Server is the server's name, at a server point.
	Server string
	// Account is the account's name, at a server point.
	Account string
	// Subject is the request's subject at api.worker, the message's at client.publish and
	// stream.apply.
	Subject string
	// Stream is the stream's name at stream.apply.
	Stream string
	// Client is the publishing connection's name at client.publish, and Kind its kind (CLIENT,
	// JETSTREAM, SYSTEM, ACCOUNT).
	Client string
	Kind   string
	// Recovering is set at stream.apply while the server replays its log at start.
	Recovering bool
}

// MeasureHook is called synchronously at each instrumented point. A hook that blocks holds
// the calling goroutine at that point, with whatever locks the original code holds there.
var MeasureHook atomic.Pointer[func(*MeasureEvent)]

var measureGen atomic.Uint64

func measure(ev *MeasureEvent) {
	if h := MeasureHook.Load(); h != nil {
		(*h)(ev)
	}
}

// MeasureDecodeConsumerState decodes an o.dat state file with the server's own decoder.
func MeasureDecodeConsumerState(buf []byte) (*ConsumerState, error) {
	return decodeConsumerState(buf)
}

func (o *consumerFileStore) mev(point string, buf []byte, seq uint64, err error) *MeasureEvent {
	var store string
	if o.fs != nil {
		store = o.fs.fcfg.StoreDir
	}
	return &MeasureEvent{Point: point, Gen: o.mgen, Name: o.name, Store: store, Path: o.ifn, State: buf, Seq: seq, Err: err}
}

// measureAPI reports a JetStream API request the server has taken and not yet served, with the
// account that sent it, which the request's Nats-Request-Info header names as the API's handlers
// read it: the account the request reached the API in is the system account's.
func measureAPI(s *Server, c *client, rmsg []byte, subject, how, point string) {
	if MeasureHook.Load() == nil {
		return
	}
	var account string
	hdr, _ := c.msgParts(rmsg)
	if raw := sliceHeader(ClientInfoHdr, hdr); len(raw) > 0 {
		var ci ClientInfo
		if json.Unmarshal(raw, &ci) == nil {
			account = ci.Account
		}
	}
	measure(&MeasureEvent{Point: point, Server: s.Name(), Store: s.getOpts().StoreDir, Account: account, Subject: subject, Path: how})
}

// measurePublish reports a publish that passed its permission checks and is not yet matched
// against any subscription.
func measurePublish(c *client, acc *Account, point string) {
	if MeasureHook.Load() == nil {
		return
	}
	s := c.srv
	measure(&MeasureEvent{Point: point, Server: s.Name(), Store: s.getOpts().StoreDir, Account: acc.Name,
		Subject: string(c.pa.subject), Client: c.opts.Name, Kind: c.kindString()})
}

// measureApply reports one committed stream entry its server is about to store.
func measureApply(s *Server, mset *stream, subject string, seq uint64, recovering bool, point string) {
	if MeasureHook.Load() == nil {
		return
	}
	measure(&MeasureEvent{Point: point, Server: s.Name(), Store: s.getOpts().StoreDir, Account: mset.accName(),
		Stream: mset.name(), Subject: subject, Seq: seq, Recovering: recovering})
}

// measureWriteFile is v2.15.1's writeFileWithOptionalSync followed by writeAtomicallyWithTemp,
// unchanged except for the measure calls between their steps.
func (o *consumerFileStore) measureWriteFile(name string, data []byte, perm fs.FileMode) error {
	fst := o.fs
	sync := fst.syncAlways.Load() || fst.syncOnFlush.Load()
	tmp := name + ".tmp"
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if sync {
		flags = flags | os.O_SYNC
	}
	fst.dios.acquire()
	defer fst.dios.release()
	f, err := os.OpenFile(tmp, flags, perm)
	if err != nil {
		measure(o.mev("write.openErr", data, 0, err))
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	measure(o.mev("write.preRename", data, 0, nil))
	if err := os.Rename(tmp, name); err != nil {
		_ = os.Remove(tmp)
		measure(o.mev("write.renameErr", data, 0, err))
		return err
	}
	measure(o.mev("write.renamed", data, 0, nil))
	if sync && canFsyncDirectories {
		if err = syncDir(filepath.Dir(name)); err != nil {
			return err
		}
	}
	return nil
}
