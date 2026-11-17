package natsgrant_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/natsgrant"
)

var updateSessionFixture = flag.Bool("update", false, "rewrite packages/contracts/fixtures/session-grant.json from natsgrant.Session")

// sessionFixturePath is the session grant as the auth service answers it, for the placeholder id
// sessionFixtureID, read by relative path as the other shared fixtures of this repository are.
// Its reader is packages/envoy-client/test/nats-server.ts (sessionGrantConfig), which renders it
// with each Bun row's own session id for the users of the NATS servers those rows start: the
// consumer-delivery row of envoy-client and the replay rows of pi-envoy and claude-envoy.
const sessionFixturePath = "../../contracts/fixtures/session-grant.json"

// sessionFixtureID is the id the fixture writes the grant for. A reader swaps it for its own
// session's id token by token, so it is one subject token and appears in no other token.
const sessionFixtureID = "ses_fixture_placeholder"

// sessionFixture is the fixture's shape: the id it was written for, then the grant.
type sessionFixture struct {
	SessionID string   `json:"session_id"`
	Publish   []string `json:"publish"`
	Subscribe []string `json:"subscribe"`
}

// The committed fixture is natsgrant.Session of its placeholder id, element for element, so the Bun
// rows that start a server from it hold a session to the grant the auth service gives one. A change
// to the grant is red here until the fixture is written again: go test -run
// TestTheSessionGrantFixtureIsNatsgrantSession ./natsgrant/ -update.
func TestTheSessionGrantFixtureIsNatsgrantSession(t *testing.T) {
	grant, err := natsgrant.Session(sessionFixtureID)
	require.NoError(t, err)
	want := sessionFixture{SessionID: sessionFixtureID, Publish: grant.Publish, Subscribe: grant.Subscribe}
	if *updateSessionFixture {
		// No HTML escaping, so the fixture reads `>` as the grant writes it rather than \u003e.
		var written bytes.Buffer
		encoder := json.NewEncoder(&written)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		require.NoError(t, encoder.Encode(want))
		require.NoError(t, os.WriteFile(sessionFixturePath, written.Bytes(), 0o644))
	}
	var committed sessionFixture
	readJSON(t, sessionFixturePath, &committed)
	require.NotEmpty(t, committed.Publish, "%s holds no publish list, so it says nothing about the grant", sessionFixturePath)
	require.Equal(t, want, committed,
		"%s is not natsgrant.Session(%q): write it again with `go test -run TestTheSessionGrantFixtureIsNatsgrantSession ./natsgrant/ -update`, "+
			"and run the Bun rows that read it (packages/envoy-client/test/nats-server.ts, sessionGrantConfig)",
		sessionFixturePath, sessionFixtureID)
}
