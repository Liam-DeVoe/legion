package writerhold

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/internal/testnats"
)

// `envoy-infra streams update|census` parse their flags as `envoy-infra hold` and `buckets` do
// (flagerr.Parse): a flag value that holds an nkey seed is refused naming the flag, never the
// value (infra/AGENTS.md: a seed is refused where it enters), and every line the words write names
// the command that wrote it, `envoy-infra streams <word>`. The control is the words as they stood:
// a seed in --reason passed the parse and reached the hold's records, and a failure printed
// `envoy-infra hold streams <word>`, the name of no command.
func TestTheStreamsWordsRefuseASeedAndNameThemselves(t *testing.T) {
	seed, _ := testnats.User(t)
	unreachable := streamsEnv{env: env{
		records: func(context.Context, options, bool) (*holdrecord.Client, error) { return nil, errors.New("no broker here") },
		hub:     func(context.Context, options) (*Operator, error) { return nil, errors.New("no hub here") },
	}}
	run := func(args ...string) (int, string) {
		full := append(append([]string{}, args...), commonArgs...)
		full = append(full, "--nats", "nats://127.0.0.1:1", "--minters-report", "/dev/null", "--probe-token-file", "/dev/null", "--replicas", "3")
		var stdout, stderr bytes.Buffer
		code := runStreams(context.Background(), full, &stdout, &stderr, unreachable)
		return code, stderr.String()
	}

	for _, word := range []string{"update", "census"} {
		code, stderr := run(word, "--family", "session", "--reason", "for "+seed)
		require.Equal(t, 2, code, "streams %s with a seed in --reason", word)
		require.Contains(t, stderr, "envoy-infra streams "+word+": --reason: an nkey seed, which is never an argument (not echoed)")
		require.NotContains(t, stderr, seed)

		code, stderr = run(word, "--family", "session", "--reason", "a reason")
		require.Equal(t, 1, code, "streams %s with no broker", word)
		require.True(t, strings.HasPrefix(stderr, "envoy-infra streams "+word+": "), "the failure names %q", stderr)
		require.Contains(t, stderr, "no broker here")
	}
}
