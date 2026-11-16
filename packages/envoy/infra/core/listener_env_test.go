package core_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/core"
)

// heredocEscape is a URL that, written raw into the watchdog's env-file heredoc, would end the
// heredoc and run a command of its own in the create script, which runs as root on the host.
const heredocEscape = "nats://hub.internal.example:4222\nENVEOF\ntouch /root/injected\ncat > /dev/null << 'ENVEOF'"

// requireInputError requires err to be a core.InputError refusing input, whose message holds msg.
func requireInputError(t *testing.T, err error, input, msg string) {
	t.Helper()
	require.ErrorContains(t, err, msg)
	var refused *core.InputError
	require.True(t, errors.As(err, &refused), "%v is a core.InputError", err)
	require.Equal(t, input, refused.Input)
}

// Check refuses every value of the config the listener cannot run on, each naming its input. This
// is the one table of those refusals: a component that calls Check tests only that it does, and
// where its refusal goes.
func TestListenerConfigCheck(t *testing.T) {
	type checkCase struct {
		name  string
		in    core.ListenerConfig
		input string // the input a refusal names
		err   string
	}
	cases := []checkCase{
		{name: "the default port", in: core.ListenerConfig{MachineID: "m", Port: core.DefaultListenerPort}},
		{name: "no machine id", in: core.ListenerConfig{Port: 9020}, input: "machineId", err: "machineId is empty"},
		{name: "a blank machine id", in: core.ListenerConfig{MachineID: " ", Port: 9020}, input: "machineId", err: "machineId has leading or trailing whitespace"},
		{name: "a machine id with a newline", in: core.ListenerConfig{MachineID: "a\nENVEOF\nid", Port: 9020}, input: "machineId", err: "machineId contains a control character"},
		{name: "a machine id with a tab", in: core.ListenerConfig{MachineID: "example\thost", Port: 9020}, input: "machineId", err: "machineId contains a control character"},
		{name: "port 0, which is no port", in: core.ListenerConfig{MachineID: "m"}, input: "port", err: "port 0 is outside 1-65535"},
		{name: "a negative port", in: core.ListenerConfig{MachineID: "m", Port: -1}, input: "port", err: "port -1 is outside 1-65535"},
		{name: "a port above 65535", in: core.ListenerConfig{MachineID: "m", Port: 65536}, input: "port", err: "port 65536 is outside 1-65535"},
		{name: "a listen host", in: core.ListenerConfig{MachineID: "m", Port: 9020, ListenHost: "0.0.0.0"}},
		{name: "a listen host with a control character", in: core.ListenerConfig{MachineID: "m", Port: 9020, ListenHost: "0.0.0.0\n"}, input: "listenHost", err: `listenHost contains a control character: "0.0.0.0\n"`},
		// A seed is refused before the control-character refusal that would quote it.
		{name: "a listen host holding a seed", in: core.ListenerConfig{MachineID: "m", Port: 9020, ListenHost: readmeSeed + "\n"}, input: "listenHost", err: "listenHost holds an nkey seed (not echoed)"},
		{
			name:  "a listen host holding a seed, lowercase and split across DNS labels",
			in:    core.ListenerConfig{MachineID: "m", Port: 9020, ListenHost: strings.ToLower(readmeSeed[:29] + "." + readmeSeed[29:])},
			input: "listenHost", err: "listenHost holds an nkey seed (not echoed)",
		},
		{name: "the allowed hosts an operator writes", in: core.ListenerConfig{MachineID: "m", Port: 9020, AllowedHosts: acceptedAllowedHosts}},
		{name: "the most NATS replicas, five", in: core.ListenerConfig{MachineID: "m", Port: 9020, NATSReplicas: 5}},
		{name: "a negative NATS replicas", in: core.ListenerConfig{MachineID: "m", Port: 9020, NATSReplicas: -1}, input: "natsReplicas", err: "natsReplicas -1 is outside 0-5"},
		{name: "more NATS replicas than NATS keeps", in: core.ListenerConfig{MachineID: "m", Port: 9020, NATSReplicas: 6}, input: "natsReplicas", err: "natsReplicas 6 is outside 0-5"},
		{name: "a stream byte limit", in: core.ListenerConfig{MachineID: "m", Port: 9020, StreamMaxBytes: 1}},
		{name: "a negative stream byte limit", in: core.ListenerConfig{MachineID: "m", Port: 9020, StreamMaxBytes: -1}, input: "streamMaxBytes", err: "streamMaxBytes -1 is below 0"},
	}
	// The listener refuses each of these at boot, so a config carrying one deploys a listener
	// that never serves; the entry is refused by its index, beside an entry that is fine.
	for _, entry := range refusedAllowedHosts {
		cases = append(cases, checkCase{
			name:  "an allowed host " + entry,
			in:    core.ListenerConfig{MachineID: "m", Port: 9020, AllowedHosts: []string{"ok.internal.example", entry}},
			input: "allowedHosts",
			err:   fmt.Sprintf("allowedHosts[1] is %q: want a host name or address with no port, scheme or path", entry),
		})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.in.Check()
			env, envErr := c.in.Env([]string{"nats://127.0.0.1:4222"})
			if c.err != "" {
				requireInputError(t, err, c.input, c.err)
				require.NotContains(t, strings.ToUpper(err.Error()), readmeSeed[:12], "the refusal quotes no part of the seed")
				// Env runs Check itself, so a caller that skips Check still gets its refusal.
				require.Nil(t, env)
				requireInputError(t, envErr, c.input, c.err)
				return
			}
			require.NoError(t, err)
			require.NoError(t, envErr)
		})
	}
}

// Env runs Check, then renders the environment, and refuses the URLs itself.
func TestListenerConfigEnv(t *testing.T) {
	cases := []struct {
		name string
		in   core.ListenerConfig
		urls []string
		want []string
		err  string // a refusal of natsUrls
	}{
		{
			name: "remote NATS, the default port",
			in:   core.ListenerConfig{MachineID: "example-host-a", Port: core.DefaultListenerPort},
			urls: []string{"nats://hub.internal.example:4222"},
			want: []string{"PORT=9020", "ENVOY_MACHINE_ID=example-host-a", "NATS_URLS=nats://hub.internal.example:4222", "ENVOY_ALLOW_REMOTE_NATS=1"},
		},
		{
			name: "loopback NATS needs no remote opt-in",
			in:   core.ListenerConfig{MachineID: "m", Port: 9021},
			urls: []string{"nats://127.0.0.1:4222"},
			want: []string{"PORT=9021", "ENVOY_MACHINE_ID=m", "NATS_URLS=nats://127.0.0.1:4222"},
		},
		{
			name: "any remote URL in the list opts in",
			in:   core.ListenerConfig{MachineID: "m", Port: 9020},
			urls: []string{"nats://localhost:4222", "nats://hub.internal.example:4222"},
			want: []string{"PORT=9020", "ENVOY_MACHINE_ID=m", "NATS_URLS=nats://localhost:4222,nats://hub.internal.example:4222", "ENVOY_ALLOW_REMOTE_NATS=1"},
		},
		{
			name: "a listen host, right after the port",
			in:   core.ListenerConfig{MachineID: "m", Port: 9020, ListenHost: "0.0.0.0"},
			urls: []string{"nats://hub.internal.example:4222"},
			want: []string{"PORT=9020", "ENVOY_LISTEN_HOST=0.0.0.0", "ENVOY_MACHINE_ID=m", "NATS_URLS=nats://hub.internal.example:4222", "ENVOY_ALLOW_REMOTE_NATS=1"},
		},
		{
			name: "allowed hosts, right after the listen host and as written",
			in:   core.ListenerConfig{MachineID: "m", Port: 9020, ListenHost: "0.0.0.0", AllowedHosts: acceptedAllowedHosts},
			urls: []string{"nats://127.0.0.1:4222"},
			want: []string{"PORT=9020", "ENVOY_LISTEN_HOST=0.0.0.0", "ENVOY_ALLOWED_HOSTS=Hub.Internal.Example.,192.0.2.7,[2001:db8::1]", "ENVOY_MACHINE_ID=m", "NATS_URLS=nats://127.0.0.1:4222"},
		},
		{
			name: "no allowed host leaves ENVOY_ALLOWED_HOSTS unset",
			in:   core.ListenerConfig{MachineID: "m", Port: 9020, ListenHost: "0.0.0.0"},
			urls: []string{"nats://127.0.0.1:4222"},
			want: []string{"PORT=9020", "ENVOY_LISTEN_HOST=0.0.0.0", "ENVOY_MACHINE_ID=m", "NATS_URLS=nats://127.0.0.1:4222"},
		},
		{
			name: "no NATS replicas and no stream byte limit render neither variable",
			in:   core.ListenerConfig{MachineID: "m", Port: 9020, NATSReplicas: 0, StreamMaxBytes: 0},
			urls: []string{"nats://127.0.0.1:4222"},
			want: []string{"PORT=9020", "ENVOY_MACHINE_ID=m", "NATS_URLS=nats://127.0.0.1:4222"},
		},
		{
			name: "NATS replicas alone renders ENVOY_NATS_REPLICAS last",
			in:   core.ListenerConfig{MachineID: "m", Port: 9020, NATSReplicas: 3},
			urls: []string{"nats://127.0.0.1:4222"},
			want: []string{"PORT=9020", "ENVOY_MACHINE_ID=m", "NATS_URLS=nats://127.0.0.1:4222", "ENVOY_NATS_REPLICAS=3"},
		},
		{
			name: "a stream byte limit alone renders ENVOY_STREAM_MAX_BYTES last",
			in:   core.ListenerConfig{MachineID: "m", Port: 9020, StreamMaxBytes: 21474836480},
			urls: []string{"nats://127.0.0.1:4222"},
			want: []string{"PORT=9020", "ENVOY_MACHINE_ID=m", "NATS_URLS=nats://127.0.0.1:4222", "ENVOY_STREAM_MAX_BYTES=21474836480"},
		},
		{
			name: "NATS replicas then the stream byte limit, after the proxy",
			in:   core.ListenerConfig{MachineID: "m", Port: 9020, ListenHost: "0.0.0.0", AllowedHosts: []string{"hub.internal.example"}, SOCKS5Proxy: "127.0.0.1:1055", NATSReplicas: 3, StreamMaxBytes: 1073741824},
			urls: []string{"nats://hub.internal.example:4222"},
			want: []string{"PORT=9020", "ENVOY_LISTEN_HOST=0.0.0.0", "ENVOY_ALLOWED_HOSTS=hub.internal.example", "ENVOY_MACHINE_ID=m", "NATS_URLS=nats://hub.internal.example:4222", "ENVOY_ALLOW_REMOTE_NATS=1", "ENVOY_NATS_SOCKS5_PROXY=127.0.0.1:1055", "ENVOY_NATS_REPLICAS=3", "ENVOY_STREAM_MAX_BYTES=1073741824"},
		},
		{name: "no URL", in: core.ListenerConfig{MachineID: "m", Port: 9020}, err: "natsUrls needs at least one URL"},
		{name: "a comma in a URL", in: core.ListenerConfig{MachineID: "m", Port: 9020}, urls: []string{"nats://a:1,nats://b:2"}, err: "natsUrls[0] contains a comma"},
		{name: "an empty URL", in: core.ListenerConfig{MachineID: "m", Port: 9020}, urls: []string{" "}, err: "natsUrls[0] is empty"},
		{name: "a refused URL is named by its index", in: core.ListenerConfig{MachineID: "m", Port: 9020}, urls: []string{"nats://127.0.0.1:4222", ""}, err: "natsUrls[1] is empty"},
		{name: "a URL with a newline", in: core.ListenerConfig{MachineID: "m", Port: 9020}, urls: []string{heredocEscape}, err: "natsUrls[0] contains a control character"},
		{name: "a URL with any control character, by its index", in: core.ListenerConfig{MachineID: "m", Port: 9020}, urls: []string{"nats://127.0.0.1:4222", "nats://hub.internal.example:4222\r"}, err: "natsUrls[1] contains a control character"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.in.Env(c.urls)
			if c.err != "" {
				requireInputError(t, err, "natsUrls", c.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, c.want, got)
		})
	}
}

func TestListenerConfigSOCKS5Proxy(t *testing.T) {
	config := core.ListenerConfig{
		MachineID:   "example-dispatch",
		Port:        core.DefaultListenerPort,
		SOCKS5Proxy: "127.0.0.1:1055",
	}
	env, err := config.Env([]string{"nats://hub.internal.example:4222"})
	require.NoError(t, err)
	require.Equal(t, []string{
		"PORT=9020",
		"ENVOY_MACHINE_ID=example-dispatch",
		"NATS_URLS=nats://hub.internal.example:4222",
		"ENVOY_ALLOW_REMOTE_NATS=1",
		"ENVOY_NATS_SOCKS5_PROXY=127.0.0.1:1055",
	}, env)

	err = (core.ListenerConfig{
		MachineID:   "example-dispatch",
		Port:        core.DefaultListenerPort,
		SOCKS5Proxy: "socks5://127.0.0.1:1055",
	}).Check()
	requireInputError(t, err, "socks5Proxy", "ENVOY_NATS_SOCKS5_PROXY")

	_, err = config.Env([]string{"nats://127.0.0.1:4222"})
	requireInputError(t, err, "socks5Proxy", "natsUrls")
}

// serviceIdentityVars are the two variables a listener's bus connection reads a service's platform
// identity from (internal/bus, credential), written out here rather than taken from bus, so a
// rename of one is seen in the environment a listener boots on.
var serviceIdentityVars = []string{"NATS_SERVICE_TOKEN_FILE", "NATS_SERVICE_GOOGLE_AUDIENCE"}

// A listener behind the auth service presents a service's platform identity on its NATS
// connection: a projected ServiceAccount token's file, in a pod, or the audience of the VM's
// Google identity token, each as the bus reads it, after the broker check, and each only when it
// is set, so a config that sets neither renders what it always rendered. The listener connects as
// one identity, so both set are refused naming both; a seed in either is refused naming the
// field and never quoting it, and so is whitespace or a control character, which one docker run
// argument or a watchdog's heredoc line cannot carry.
func TestListenerEnvServiceIdentity(t *testing.T) {
	urls := []string{"nats://127.0.0.1:4222"}
	base := core.ListenerConfig{
		MachineID: "m", Port: 9020,
		AgentSecretsURL: "https://example-broker.internal.example", AgentSecretsAudience: "example-broker-audience",
		PublicURL: "http://example-hub.example-tailnet.ts.net:9020",
	}
	without, err := base.Env(urls)
	require.NoError(t, err)
	for _, name := range serviceIdentityVars {
		require.NotContains(t, strings.Join(without, "\n"), name, "a config with no service identity renders neither variable")
	}
	for _, row := range []struct {
		name string
		edit func(*core.ListenerConfig)
		line string
	}{
		{"a projected token's file", func(c *core.ListenerConfig) { c.ServiceTokenFile = "/var/run/secrets/envoy/nats-token" },
			"NATS_SERVICE_TOKEN_FILE=/var/run/secrets/envoy/nats-token"},
		{"a VM's Google audience", func(c *core.ListenerConfig) { c.ServiceGoogleAudience = "example-nats-audience" },
			"NATS_SERVICE_GOOGLE_AUDIENCE=example-nats-audience"},
	} {
		t.Run(row.name, func(t *testing.T) {
			config := base
			row.edit(&config)
			got, err := config.Env(urls)
			require.NoError(t, err)
			require.Equal(t, append(slices.Clone(without), row.line), got, "the identity follows the broker check, and nothing else changes")
		})
	}
	for _, row := range []struct {
		name, input, err string
		edit             func(*core.ListenerConfig)
	}{
		{"both identities", "serviceGoogleAudience", "serviceTokenFile and serviceGoogleAudience are both set: the listener connects to NATS as one identity",
			func(c *core.ListenerConfig) {
				c.ServiceTokenFile, c.ServiceGoogleAudience = "/var/run/secrets/envoy/nats-token", "example-nats-audience"
			}},
		{"a seed as the token's file", "serviceTokenFile", "serviceTokenFile holds an nkey seed (not echoed)",
			func(c *core.ListenerConfig) { c.ServiceTokenFile = "/var/run/" + readmeSeed }},
		{"a lowercase seed as the audience", "serviceGoogleAudience", "serviceGoogleAudience holds an nkey seed (not echoed)",
			func(c *core.ListenerConfig) { c.ServiceGoogleAudience = strings.ToLower(readmeSeed) }},
		{"a token file with a space", "serviceTokenFile", "serviceTokenFile holds whitespace or a control character, which NATS_SERVICE_TOKEN_FILE cannot carry (not echoed)",
			func(c *core.ListenerConfig) { c.ServiceTokenFile = "/var/run/token --privileged" }},
		{"an audience with a line break", "serviceGoogleAudience", "serviceGoogleAudience holds whitespace or a control character, which NATS_SERVICE_GOOGLE_AUDIENCE cannot carry (not echoed)",
			func(c *core.ListenerConfig) { c.ServiceGoogleAudience = "example-nats-audience\nENVEOF" }},
	} {
		t.Run(row.name, func(t *testing.T) {
			config := base
			row.edit(&config)
			requireInputError(t, config.Check(), row.input, row.err)
			env, err := config.Env(urls)
			require.Nil(t, env)
			requireInputError(t, err, row.input, row.err)
			require.NotContains(t, strings.ToUpper(err.Error()), readmeSeed[:12], "the refusal quotes no part of the seed")
		})
	}
}

// PrivateProjects renders ENVOY_PRIVATE_PROJECTS=1, last, which the listener reads to decide a
// project frame that carries no stamp as private (dispatch://INFRA-3862, PlanC's rule 3), and
// false renders no such line: every config written before the field existed renders what it always
// rendered (R9), so the goldens every component that renders a listener pins (gcp/testdata,
// aws/testdata, host's) hold no such line. Negative control, run by hand: render the line whatever
// the field says; this test's false row and those goldens then fail, naming the line.
func TestListenerEnvPrivateProjects(t *testing.T) {
	urls := []string{"nats://hub.internal.example:4222"}
	config := core.ListenerConfig{
		MachineID: "m", Port: 9020, ListenHost: "0.0.0.0", AllowedHosts: []string{"hub.internal.example"},
		NATSReplicas: 3, StreamMaxBytes: 1073741824,
		AgentSecretsURL: "https://example-broker.internal.example", AgentSecretsAudience: "example-broker-audience",
		PublicURL:        "http://example-hub.example-tailnet.ts.net:9020",
		ServiceTokenFile: "/var/run/secrets/envoy/nats-token",
	}
	off, err := config.Env(urls)
	require.NoError(t, err)
	require.NotContains(t, strings.Join(off, "\n"), "ENVOY_PRIVATE_PROJECTS", "false renders no line")

	config.PrivateProjects = true
	on, err := config.Env(urls)
	require.NoError(t, err)
	require.Equal(t, append(slices.Clone(off), "ENVOY_PRIVATE_PROJECTS=1"), on, "true adds the one line, last")
}
