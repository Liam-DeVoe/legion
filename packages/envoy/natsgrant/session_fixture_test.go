package natsgrant_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"strings"
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

// biomeConfigPath is the repository's biome.json, whose formatter the lint job holds every JSON
// file to (`bunx biome check .`), this fixture among them.
const biomeConfigPath = "../../../biome.json"

// sessionFixtureID is the id the fixture writes the grant for. A reader swaps it for its own
// session's id token by token, so it is one subject token and appears in no other token.
const sessionFixtureID = "ses_fixture_placeholder"

// sessionFixture is the fixture's shape: the id it was written for, then the grant.
type sessionFixture struct {
	SessionID string   `json:"session_id"`
	Publish   []string `json:"publish"`
	Subscribe []string `json:"subscribe"`
}

// biomeLayout is the two settings of biome.json's formatter the fixture's layout turns on.
type biomeLayout struct {
	IndentWidth int `json:"indentWidth"`
	LineWidth   int `json:"lineWidth"`
}

func readBiomeLayout(t *testing.T) biomeLayout {
	t.Helper()
	var config struct {
		Formatter biomeLayout `json:"formatter"`
	}
	readJSON(t, biomeConfigPath, &config)
	require.Positive(t, config.Formatter.IndentWidth, "%s names no formatter.indentWidth", biomeConfigPath)
	require.Positive(t, config.Formatter.LineWidth, "%s names no formatter.lineWidth", biomeConfigPath)
	return config.Formatter
}

// renderSessionFixture writes fixture as biome's JSON formatter prints it, so that the lint job's
// `biome check` keeps the file the -update run writes. Go's encoder breaks every list onto one
// entry a line, which biome joins back onto its key's line whenever that line fits. Biome puts
// each key of an object on a line of its own, indented by layout.IndentWidth, and a list of
// strings on its key's line, entries separated by ", ", when that whole line, its trailing comma
// included, fits in layout.LineWidth columns, and otherwise one entry a line, one level deeper.
// The strings carry no HTML escaping, so `>` stays as the grant writes it rather than \u003e.
func renderSessionFixture(t *testing.T, fixture sessionFixture, layout biomeLayout) []byte {
	t.Helper()
	quote := func(value string) string {
		var buffer bytes.Buffer
		encoder := json.NewEncoder(&buffer)
		encoder.SetEscapeHTML(false)
		require.NoError(t, encoder.Encode(value))
		return strings.TrimSuffix(buffer.String(), "\n")
	}
	indent := strings.Repeat(" ", layout.IndentWidth)
	type entry struct {
		key    string
		scalar string
		list   []string
	}
	entries := []entry{
		{key: "session_id", scalar: fixture.SessionID},
		{key: "publish", list: fixture.Publish},
		{key: "subscribe", list: fixture.Subscribe},
	}
	var out strings.Builder
	out.WriteString("{\n")
	for i, field := range entries {
		comma := ","
		if i == len(entries)-1 {
			comma = ""
		}
		head := indent + quote(field.key) + ": "
		if field.list == nil {
			out.WriteString(head + quote(field.scalar) + comma + "\n")
			continue
		}
		quoted := make([]string, len(field.list))
		for j, value := range field.list {
			quoted[j] = quote(value)
		}
		if flat := head + "[" + strings.Join(quoted, ", ") + "]" + comma; len(flat) <= layout.LineWidth {
			out.WriteString(flat + "\n")
			continue
		}
		out.WriteString(head + "[\n")
		for j, value := range quoted {
			separator := ","
			if j == len(quoted)-1 {
				separator = ""
			}
			out.WriteString(indent + indent + value + separator + "\n")
		}
		out.WriteString(indent + "]" + comma + "\n")
	}
	out.WriteString("}\n")
	return []byte(out.String())
}

// The committed fixture is natsgrant.Session of its placeholder id, element for element, so the Bun
// rows that start a server from it hold a session to the grant the auth service gives one, and it
// is laid out as renderSessionFixture writes it, which is biome's layout. A change to the grant, or
// a hand edit of the file, is red here until the fixture is written again: go test -run
// TestTheSessionGrantFixtureIsNatsgrantSession ./natsgrant/ -update.
func TestTheSessionGrantFixtureIsNatsgrantSession(t *testing.T) {
	grant, err := natsgrant.Session(sessionFixtureID)
	require.NoError(t, err)
	want := sessionFixture{SessionID: sessionFixtureID, Publish: grant.Publish, Subscribe: grant.Subscribe}
	rendered := renderSessionFixture(t, want, readBiomeLayout(t))
	if *updateSessionFixture {
		require.NoError(t, os.WriteFile(sessionFixturePath, rendered, 0o644))
	}
	var committed sessionFixture
	readJSON(t, sessionFixturePath, &committed)
	require.NotEmpty(t, committed.Publish, "%s holds no publish list, so it says nothing about the grant", sessionFixturePath)
	const rewrite = "write it again with `go test -run TestTheSessionGrantFixtureIsNatsgrantSession ./natsgrant/ -update`"
	require.Equal(t, want, committed,
		"%s is not natsgrant.Session(%q): "+rewrite+", and run the Bun rows that read it (packages/envoy-client/test/nats-server.ts, sessionGrantConfig)",
		sessionFixturePath, sessionFixtureID)
	raw, err := os.ReadFile(sessionFixturePath)
	require.NoError(t, err)
	require.Equal(t, string(rendered), string(raw),
		"%s holds the grant but not in the layout the -update run writes, which is biome's (%s): "+rewrite,
		sessionFixturePath, biomeConfigPath)
}
