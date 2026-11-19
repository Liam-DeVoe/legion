// Package loopback chooses the loopback address a NATS server listens on, for the servers of the
// tests (testnats.NewHost) and for the servers a script's stack starts itself: natsd
// (../cmd/natsd), and through ../cmd/loopbackhost a nats-server binary or container a script runs.
// It imports the standard library alone, so loopbackhost builds in a second, without the NATS
// server testnats links.
package loopback

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"sync"
)

// NewHost is a loopback address no earlier call of this process gave. On Linux, where every
// address of 127.0.0.0/8 is the loopback, it is one at random from 127.1.0.1 through
// 127.254.255.254, never one this process gave before (given). Elsewhere it is 127.0.0.1, the only
// loopback address such a system brings up by default, and every server there shares it.
//
// A process that asks once and exits, as loopbackhost does, has no earlier address to avoid: two
// such processes share an address only by a draw of one in 16,516,096. testnats.NewHost says why a
// server on an address of its own is out of reach of another server's clients, whatever port it
// gets.
func NewHost() string {
	if runtime.GOOS != "linux" {
		return "127.0.0.1"
	}
	givenMu.Lock()
	defer givenMu.Unlock()
	for {
		host := fmt.Sprintf("127.%d.%d.%d", 1+rand.IntN(254), rand.IntN(256), 1+rand.IntN(254))
		if !given[host] {
			given[host] = true
			return host
		}
	}
}

// given is every address NewHost has given in this process, kept for the process's life: an
// address is never given twice, so a client of an ended test never reaches a later one.
var (
	givenMu sync.Mutex
	given   = map[string]bool{}
)
