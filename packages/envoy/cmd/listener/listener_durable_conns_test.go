package main

import (
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
)

// The registry of a listener's durable connections forgets a client once the client stops, so a
// process that connects a client again after one stopped keeps no entry, and no closed connection,
// for the stopped one. The control is a client still running, whose entry stays.
func TestTheDurableConnectionRegistryForgetsAStoppedClient(t *testing.T) {
	url := testnats.URL(t)
	connect := func() *bus.Client {
		t.Helper()
		client, err := bus.Connect([]string{url}, bus.WithName("envoy-listener"))
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(client.Close)
		return client
	}
	registered := func(client *bus.Client, durable string) bool {
		_, ok := listenerDurableConns.Load(durableConnsKey{client, durable})
		return ok
	}
	stopping, running := connect(), connect()
	connsOf(stopping, "listener-registry-stopping")
	connsOf(running, "listener-registry-running")
	stopping.Close()
	deadline := time.Now().Add(10 * time.Second)
	for registered(stopping, "listener-registry-stopping") {
		if time.Now().After(deadline) {
			t.Fatal("the registry still holds the stopped client's entry")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !registered(running, "listener-registry-running") {
		t.Fatal("control: the running client's entry is gone")
	}
}
