// The connections the listener's own durable's fresh generations are granted on. A listener beside
// Dispatch holds Dispatch's credential, whose grant reaches its durable's fixed name alone; the auth
// service grants each fresh generation's calls to a connection named for that generation
// (natsgrant.ParseDispatchConnection): listener-intake/<machine id>/<generation> creates it, reads
// it, binds it and acknowledges its messages, and listener-cleanup/<machine id>/<generation> reads
// and deletes a retired one. So in the fresh mode every call on a generation goes on such a
// connection, opened beside the listener's client (bus.Client.Sibling) with its credential. A
// listener with a grant of its own reaches every consumer on any connection, so for it the names
// change nothing.

package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/natsgrant"
)

// generationAdmittedWithin bounds the wait for a generation's connection to be admitted. The auth
// service answers within its two-second budget; a connection not admitted by then is refused, and
// the call that needed it fails with nothing changed.
const generationAdmittedWithin = 5 * time.Second

// durableConns are the connections of one listener client's durable's generations, by physical
// name: the intake connection of each generation the listener reserved, created or binds.
type durableConns struct {
	client  *bus.Client
	durable string

	mu     sync.Mutex
	byName map[string]*bus.Client
}

// durableConnsKey keys the registry by the listener's client and its durable's logical name.
type durableConnsKey struct {
	client  *bus.Client
	durable string
}

// listenerDurableConns is every listener client's durableConns.
var listenerDurableConns sync.Map

// connsOf is client's durableConns for durable.
func connsOf(client *bus.Client, durable string) *durableConns {
	conns, _ := listenerDurableConns.LoadOrStore(durableConnsKey{client, durable}, &durableConns{
		client: client, durable: durable, byName: map[string]*bus.Client{},
	})
	return conns.(*durableConns)
}

// machine is the machine id the durable is the listener durable of (bus.ListenerDurable), as the
// auth service's entry names it: the durable's name without its "listener-" prefix. A machine id
// holding a space, which the durable's name writes as a hyphen, is granted no generation.
func (d *durableConns) machine() string {
	return strings.TrimPrefix(d.durable, "listener-")
}

// generation is physical's generation, or "" for a name that is no generation of the durable.
func (d *durableConns) generation(physical string) string {
	generation, ok := strings.CutPrefix(physical, d.durable+"~")
	if !ok || generation == "" || strings.Contains(generation, "~") {
		return ""
	}
	return generation
}

// open opens the connection called name beside the listener's client and waits, up to
// generationAdmittedWithin or ctx's end, for the hub to admit it.
func (d *durableConns) open(ctx context.Context, name string) (*bus.Client, error) {
	conn, err := d.client.Sibling(name)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", name, err)
	}
	admitted, cancel := context.WithTimeout(ctx, generationAdmittedWithin)
	defer cancel()
	for {
		changed := conn.ConnectionChanged()
		if conn.Connected() {
			return conn, nil
		}
		select {
		case <-changed:
		case <-time.After(50 * time.Millisecond):
		case <-admitted.Done():
			conn.Close()
			return nil, fmt.Errorf("connect %s: not admitted within %s: %w", name, generationAdmittedWithin, admitted.Err())
		}
	}
}

// intake is the intake connection of the generation physical names, opened when there is none.
func (d *durableConns) intake(ctx context.Context, physical string) (*bus.Client, error) {
	d.mu.Lock()
	conn := d.byName[physical]
	d.mu.Unlock()
	if conn != nil {
		return conn, nil
	}
	generation := d.generation(physical)
	if generation == "" {
		return nil, fmt.Errorf("consumer names: %s is no generation of %s", physical, d.durable)
	}
	conn, err := d.open(ctx, natsgrant.ListenerIntakeConnection(d.machine(), generation, ""))
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if held := d.byName[physical]; held != nil {
		conn.Close()
		return held, nil
	}
	d.byName[physical] = conn
	return conn, nil
}

// held is the intake connection the listener holds for physical, nil with none.
func (d *durableConns) held(physical string) *bus.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.byName[physical]
}

// cleanup opens the connection that reads and deletes the retired generation physical names; its
// caller closes it.
func (d *durableConns) cleanup(ctx context.Context, physical string) (*bus.Client, error) {
	generation := d.generation(physical)
	if generation == "" {
		return nil, fmt.Errorf("consumer names: %s is no generation of %s", physical, d.durable)
	}
	return d.open(ctx, natsgrant.ListenerCleanupConnection(d.machine(), generation))
}

// keepOnly closes every intake connection but physical's: once the listener binds a generation,
// no other is its own.
func (d *durableConns) keepOnly(physical string) {
	d.mu.Lock()
	var closing []*bus.Client
	for name, conn := range d.byName {
		if name != physical {
			closing = append(closing, conn)
			delete(d.byName, name)
		}
	}
	d.mu.Unlock()
	for _, conn := range closing {
		conn.Close()
	}
}

// reserved is consumernames.Config.Reserved: a candidate is reserved, so its intake connection
// opens now, before the protocol creates it; an earlier candidate of this call is dropped, since
// the protocol retires a candidate before it reserves the next. A connection the hub does not
// admit ends the call with nothing created.
func (d *durableConns) reserved(ctx context.Context, _ string, op consumernames.Op) error {
	d.mu.Lock()
	var earlier []string
	for name := range d.byName {
		if name != op.Candidate && name != op.Old {
			earlier = append(earlier, name)
		}
	}
	d.mu.Unlock()
	for _, name := range earlier {
		d.drop(name)
	}
	_, err := d.intake(ctx, op.Candidate)
	return err
}

// drop closes the intake connection of physical, if the listener holds one.
func (d *durableConns) drop(physical string) {
	d.mu.Lock()
	conn := d.byName[physical]
	delete(d.byName, physical)
	d.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}
