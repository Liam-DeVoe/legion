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
// ConnectOwningStream rows fail), from Dial (the Dial rows fail), or its blank-entry loop (the
// blank rows fail).
func TestEveryConnectRefusesAListNamingNoServer(t *testing.T) {
	environment := func(string) (string, bool) { return "", false }
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
	lists := map[string][]string{
		"nil":            nil,
		"empty":          {},
		"blank":          {""},
		"spaces":         {"  "},
		"a blank second": {"nats://127.0.0.1:1", " "},
	}
	for connectName, connect := range connects {
		for listName, urls := range lists {
			t.Run(connectName+"/"+listName, func(t *testing.T) {
				closeIt, err := connect(urls)
				if err == nil {
					closeIt()
					t.Fatalf("%s built a connection for %q, which nats.go dials as port 4222 on this machine", connectName, urls)
				}
				if !errors.Is(err, bus.ErrNoNATSServer) {
					t.Fatalf("%s refused %q with %v, not bus.ErrNoNATSServer", connectName, urls, err)
				}
			})
		}
	}
}
