package bus_test

import (
	"errors"
	"testing"

	"github.com/sjawhar/envoy/internal/bus"
)

// Every constructor refuses, before any dial, a server list that names no server or holds a blank
// entry: nats.go reads either as its default server, port 4222 on this machine. Each row connects
// in the background, so with the refusal gone it returns the client or connection it built and
// the row fails naming it, rather than waiting out a dial.
//
// Controls, each on its own: drop refuseNoServer from newClient (the Connect and
// ConnectOwningStream rows fail), from Dial (the Dial rows fail), or its blank-entry check (the
// blank rows fail).
func TestEveryConnectRefusesAListNamingNoServer(t *testing.T) {
	requireEveryConnectRefuses(t, func(string) (string, bool) { return "", false }, map[string][]string{
		"nil":            nil,
		"empty":          {},
		"blank":          {""},
		"spaces":         {"  "},
		"a blank second": {"nats://127.0.0.1:1", " "},
	})
}

// Every constructor refuses, before any dial, an entry that names no host, even with
// ENVOY_ALLOW_REMOTE_NATS=1: nats.go dials such an entry on this machine (nats:// as port 4222),
// and the opt-in, which admits another machine's server, names none here.
//
// Control: drop refuseNoServer's host check; with the opt-in, each row builds its client or
// connection, and fails naming it.
func TestEveryConnectRefusesAServerNamingNoHost(t *testing.T) {
	requireEveryConnectRefuses(t, allowRemote, map[string][]string{
		"nats://":                 {"nats://"},
		"nats://:4222":            {"nats://:4222"},
		"a user and no host":      {"nats://example-user@:4222"},
		"a host-less second":      {"nats://127.0.0.1:1", "nats://:4222"},
		"no scheme, a port alone": {":4222"},
	})
}

// Every constructor refuses, before any dial, an entry that names the unspecified address, even
// with ENVOY_ALLOW_REMOTE_NATS=1: a connect to 0.0.0.0 or :: reaches this machine, since its
// network stack takes either for its own, so such an entry names no server the opt-in could
// allow. The first two rows are the tester's probe, which showed a client built for each reaching
// a server on this machine's loopback.
//
// Control: drop refuseNoServer's unspecified-address check; with the opt-in, each row builds its
// client or connection, which dials this machine, and fails naming it.
func TestEveryConnectRefusesTheUnspecifiedAddress(t *testing.T) {
	requireEveryConnectRefuses(t, allowRemote, map[string][]string{
		"0.0.0.0":                 {"nats://0.0.0.0:4222"},
		"[::]":                    {"nats://[::]:4222"},
		"IPv4-mapped 0.0.0.0":     {"nats://[::ffff:0.0.0.0]:4222"},
		"[::] with a zone":        {"nats://[::%25lo]:4222"},
		"no scheme, 0.0.0.0":      {"0.0.0.0:4222"},
		"an unspecified second":   {"nats://127.0.0.1:1", "nats://0.0.0.0:4222"},
		"a user and 0.0.0.0 only": {"nats://example-user@0.0.0.0"},
	})
}

// allowRemote is a connect's environment with ENVOY_ALLOW_REMOTE_NATS=1 and nothing else set.
func allowRemote(name string) (string, bool) {
	if name == bus.AllowRemoteEnvVar {
		return "1", true
	}
	return "", false
}

// requireEveryConnectRefuses connects with Connect, ConnectOwningStream and Dial, each in the
// background and with environment, to each list of lists, and fails a row whose connect does not
// refuse it with bus.ErrNoNATSServer.
func requireEveryConnectRefuses(t *testing.T, environment func(string) (string, bool), lists map[string][]string) {
	t.Helper()
	connects := map[string]func(urls []string) (func(), error){
		"Connect": func(urls []string) (func(), error) {
			client, err := bus.Connect(urls, bus.WithEnvironment(environment), bus.WithoutStreamSubjects(), bus.WithConnectInBackground())
			if err != nil {
				return nil, err
			}
			return client.Close, nil
		},
		"ConnectOwningStream": func(urls []string) (func(), error) {
			client, err := bus.ConnectOwningStream(urls, bus.WithEnvironment(environment), bus.WithConnectInBackground())
			if err != nil {
				return nil, err
			}
			return client.Close, nil
		},
		"Dial": func(urls []string) (func(), error) {
			conn, err := bus.Dial("no-server-test", urls, environment, bus.WithConnectInBackground())
			if err != nil {
				return nil, err
			}
			return conn.Close, nil
		},
	}
	for connectName, connect := range connects {
		for listName, urls := range lists {
			t.Run(connectName+"/"+listName, func(t *testing.T) {
				closeIt, err := connect(urls)
				if err == nil {
					closeIt()
					t.Fatalf("%s built a connection for %q, which nats.go dials on this machine", connectName, urls)
				}
				if !errors.Is(err, bus.ErrNoNATSServer) {
					t.Fatalf("%s refused %q with %v, not bus.ErrNoNATSServer", connectName, urls, err)
				}
			})
		}
	}
}
