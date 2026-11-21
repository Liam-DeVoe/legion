package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sjawhar/envoy/cipherclient"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// ControllerConfigExample is the example every refusal of the operator-side file points at.
const ControllerConfigExample = "deploy/kubernetes/daemon/controller.yaml.example"

// controllerKeys is every key the operator-side file may carry: the same names as legion.yaml, only
// the ones the controller needs.
var controllerKeys = []string{
	"project", "daemon_url", "operator_token_file", "envoy_url", "nats_urls",
	"nats_nkey_seed_file", "dispatch_url", "dispatch_token_file", "dispatch_token_broker",
	"instructions", "omp_invocation", "omp_launch_prefix", "state_dir",
}

// envoyTokenFileRefusal is LoadController's refusal of `envoy_token_file`, the key whose file
// reached the controller's session as ENVOY_TOKEN_FILE until the Envoy listener stopped taking a
// shared bearer. With no such pointer the session's Envoy client presents the broker token it
// trades with this machine's own identity wherever the operator's environment names a secrets
// broker and an identity (envoySettings and brokerCredential, packages/envoy-client), and
// otherwise no credential, which a loopback listener serves and no other does. The key has a
// refusal of its own rather than the unknown key's, so an operator whose file predates the change
// reads what replaced it.
const envoyTokenFileRefusal = "envoy_token_file is not read any more: the Envoy listener takes no shared bearer, so the controller's" +
	" session presents the broker token it trades with this machine's own identity at the secrets broker its" +
	" environment names (AGENT_SECRETS_URL, AGENT_SECRETS_MACHINE_IDENTITY), or calls a loopback listener, which needs" +
	" no credential; remove envoy_token_file from the controller configuration — see " + ControllerConfigExample

// ControllerConfig is `legion controller start`'s operator-side file, settled. Every path is
// absolute; an optional key the file leaves out is "" (or nil).
type ControllerConfig struct {
	// Project is the daemon's project token (claim.ProjectToken of the value as written).
	Project string
	// DaemonURL is the daemon's API base, with no trailing slash: `--daemon-url` when given,
	// else the file's daemon_url.
	DaemonURL         string
	OperatorTokenFile string
	EnvoyURL          string
	NatsURLs          []string
	// NatsNkeySeedFile is the NATS nkey user seed's file the controller's Oh My Pi reads through
	// NATS_NKEY_SEED_FILE, "" when the file names none.
	NatsNkeySeedFile  string
	DispatchURL       string
	DispatchTokenFile string
	// DispatchTokenBroker is `dispatch_token_broker`: the controller's Dispatch credential is
	// this machine's own identity, traded at the secrets broker its environment names, rather
	// than a bearer in a file (`dispatch_token_file`), the one alternative to it. The loader
	// reads no environment, so it decides nothing about that trade; `legion controller start`
	// makes it (controllerDispatchBroker).
	DispatchTokenBroker bool
	InstructionsPath    string
	// OmpInvocation is the invocation as configured, "" when the file sets none; the command
	// resolves it as the daemon resolves its own (omplaunch.ResolveInvocation), with no pinned
	// default in Go.
	OmpInvocation   string
	OmpLaunchPrefix []string
	// StateDir is "" when the file sets none; the command then picks the default under
	// `$XDG_STATE_HOME/legion`.
	StateDir string
}

// LoadController reads the operator-side file strictly: a YAML mapping whose every key is one of
// controllerKeys, so a typo fails on the operator's machine rather than in the controller session.
// It is never the daemon's loader, which runs both GitHub Apps' private_key_command and demands
// keys the controller never uses. Relative paths resolve against the file's directory; `~` is not
// expanded. daemonURL, when not "", is `--daemon-url`, which replaces the file's daemon_url and is
// validated the same way. A Dispatch credential is `dispatch_token_file` or `dispatch_token_broker`
// and never both, and either needs `dispatch_url`; that a configured `dispatch_url` names one at
// all is the start's rule, since only the start can prove the broker route.
func LoadController(path, daemonURL string) (ControllerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ControllerConfig{}, fmt.Errorf("controller configuration %s could not be read: %w", path, err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return ControllerConfig{}, fmt.Errorf("Invalid controller configuration: %w", err)
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return ControllerConfig{}, errors.New("controller.yaml must be a mapping")
	}
	root := document.Content[0]
	values := map[string]*yaml.Node{}
	var refused []string
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		if key == "envoy_token_file" {
			refused = append(refused, envoyTokenFileRefusal)
			continue
		}
		if !slices.Contains(controllerKeys, key) {
			refused = append(refused, fmt.Sprintf("unknown key %q in the controller configuration; legion controller start reads only %s — see %s",
				key, strings.Join(controllerKeys, ", "), ControllerConfigExample))
			continue
		}
		values[key] = root.Content[i+1]
	}
	if len(refused) > 0 {
		return ControllerConfig{}, errors.New(strings.Join(refused, "; "))
	}

	configDir := filepath.Dir(path)
	const inController = " in the controller configuration"
	required := func(key string) (string, error) { return requiredString(values[key], key, inController) }
	optionalPath := func(key string) (string, error) {
		value, err := optionalString(values[key], key)
		if err != nil || value == "" {
			return "", err
		}
		return underConfig(value, configDir), nil
	}

	var cfg ControllerConfig
	project, err := required("project")
	if err != nil {
		return ControllerConfig{}, err
	}
	if cfg.Project, err = claim.ProjectToken(project); err != nil {
		return ControllerConfig{}, err
	}
	fileDaemonURL, err := required("daemon_url")
	if err != nil {
		return ControllerConfig{}, err
	}
	if cfg.DaemonURL, err = daemonBase(fileDaemonURL, "daemon_url"); err != nil {
		return ControllerConfig{}, err
	}
	operatorTokenFile, err := required("operator_token_file")
	if err != nil {
		return ControllerConfig{}, err
	}
	cfg.OperatorTokenFile = underConfig(operatorTokenFile, configDir)
	if cfg.EnvoyURL, err = required("envoy_url"); err != nil {
		return ControllerConfig{}, err
	}
	if _, err := endpointURL(cfg.EnvoyURL, "envoy_url"); err != nil {
		return ControllerConfig{}, err
	}
	if node, set := values["nats_urls"]; set {
		if cfg.NatsURLs, err = readNatsURLs(node, "nats_urls"); err != nil {
			return ControllerConfig{}, err
		}
	}
	if len(cfg.NatsURLs) == 0 {
		return ControllerConfig{}, errors.New("nats_urls is required in the controller configuration")
	}
	if cfg.NatsNkeySeedFile, err = optionalPath("nats_nkey_seed_file"); err != nil {
		return ControllerConfig{}, err
	}
	dispatchURL, err := optionalString(values["dispatch_url"], "dispatch_url")
	if err != nil {
		return ControllerConfig{}, err
	}
	if cfg.DispatchTokenFile, err = optionalPath("dispatch_token_file"); err != nil {
		return ControllerConfig{}, err
	}
	if node, set := values["dispatch_token_broker"]; set {
		if node.Tag != "!!bool" || node.Decode(&cfg.DispatchTokenBroker) != nil {
			return ControllerConfig{}, errors.New("dispatch_token_broker must be true or false")
		}
	}
	// Which credential a configured Dispatch is written with is a file-shape question only so
	// far: the two sources exclude each other, and either names a Dispatch to spend it on. That
	// a configured dispatch_url has one of them at all is `legion controller start`'s rule, not
	// this loader's (controllerStart): a machine's broker route is proved by trading at the
	// broker, and this loader reads no environment, so a file naming neither is a configuration
	// it takes and the start decides on.
	switch {
	case cfg.DispatchTokenFile != "" && cfg.DispatchTokenBroker:
		return ControllerConfig{}, errors.New("dispatch_token_file and dispatch_token_broker are both set: the controller's Dispatch credential is the bearer a file holds or this machine's own identity traded at the secrets broker, never both")
	case dispatchURL == "" && cfg.DispatchTokenFile != "":
		return ControllerConfig{}, errors.New("dispatch_token_file is set but dispatch_url is not")
	case dispatchURL == "" && cfg.DispatchTokenBroker:
		return ControllerConfig{}, errors.New("dispatch_token_broker is set but dispatch_url is not")
	case dispatchURL != "":
		if cfg.DispatchURL, err = dispatchBase(dispatchURL, "dispatch_url"); err != nil {
			return ControllerConfig{}, err
		}
	}
	if cfg.InstructionsPath, err = optionalPath("instructions"); err != nil {
		return ControllerConfig{}, err
	}
	if cfg.OmpInvocation, err = optionalString(values["omp_invocation"], "omp_invocation"); err != nil {
		return ControllerConfig{}, err
	}
	if node, set := values["omp_launch_prefix"]; set {
		// The controller runs under the operator's own environment, not a pane's isolated XDG
		// home, so a `secrets` prefix is theirs to use (readLaunchPrefix refuses it for panes).
		if cfg.OmpLaunchPrefix, err = readStrings(node, "omp_launch_prefix"); err != nil {
			return ControllerConfig{}, err
		}
	}
	if cfg.StateDir, err = optionalPath("state_dir"); err != nil {
		return ControllerConfig{}, err
	}
	if daemonURL != "" {
		if cfg.DaemonURL, err = daemonBase(daemonURL, "--daemon-url"); err != nil {
			return ControllerConfig{}, err
		}
	}
	return cfg, nil
}

// ControllerIdentityVariables are the variables that name the secrets broker and this machine's
// identity at it, which the controller's session trades for the broker token its Envoy client
// presents the listener (brokerCredential, packages/envoy-client).
var ControllerIdentityVariables = []string{cipherclient.BrokerURLVar, cipherclient.IdentityVar}

// CheckListener refuses envoy_url when the controller's session, whose environment is env, would
// present the listener no credential and the listener is off loopback. The listener takes no
// shared bearer, so the session presents the broker token it trades with this machine's identity
// where env names both ControllerIdentityVariables and an identity the broker client can prove,
// and otherwise nothing, which only a listener bound to loopback serves, as the daemon under
// runtime: tmux is held to (checkLoopbackListener). The identity is read by the client's own
// cipherclient.MachineIdentity, as the controller's Dispatch broker check reads it
// (controllerDispatchBroker, cmd/legion), so a kind with one of its variables missing, or a kind
// the client does not know, is refused in the client's words, and a broker or identity variable
// holding only blanks is refused as an unset one is. An identity that holds a NEL (U+0085) or a
// BOM (U+FEFF) anywhere is refused naming the variable and never its value: those are the two
// characters on which Go's strings.TrimSpace, which this check and MachineIdentity trim with, and
// the session's TypeScript client, which trims with String.prototype.trim, differ (Go takes a NEL
// off the kind and leaves a BOM on it; JavaScript does the opposite), so the two could read
// different kinds. Every other space both strip alike, a no-break space and an ideographic space
// among them, so a kind padded with one is trimmed and taken. The host is judged as written, and
// the refusal names it alone, never the URL.
func (c ControllerConfig) CheckListener(env map[string]string) error {
	noIdentity := slices.ContainsFunc(ControllerIdentityVariables, func(name string) bool { return strings.TrimSpace(env[name]) == "" })
	trimsDiffer := false
	var unproven error
	if !noIdentity {
		trimsDiffer = strings.ContainsAny(env[cipherclient.IdentityVar], "\u0085\ufeff")
	}
	if !noIdentity && !trimsDiffer {
		var identity cipherclient.Source
		identity, unproven = cipherclient.MachineIdentity(func(name string) string { return env[name] })
		noIdentity = unproven == nil && identity == nil
	}
	if !noIdentity && !trimsDiffer && unproven == nil {
		return nil
	}
	parsed, err := endpointURL(c.EnvoyURL, "envoy_url")
	if err != nil {
		return err
	}
	host := parsed.Hostname()
	if err := refuseNonASCIIHost("envoy_url host", host); err != nil {
		return err
	}
	if isLoopbackHost(host) {
		return nil
	}
	if trimsDiffer {
		return fmt.Errorf("%s holds a NEL (U+0085) or a BOM (U+FEFF), which this check and the session's broker client trim differently, so the session may read another kind, trade nothing and present the Envoy listener %s no credential, and a listener off loopback answers a caller that presents none 401; remove it from the kind, or name a loopback listener",
			cipherclient.IdentityVar, host)
	}
	if unproven != nil {
		return fmt.Errorf("%w: the controller's session cannot prove this machine's identity, so it trades nothing and presents the Envoy listener %s no credential, and a listener off loopback answers a caller that presents none 401; give the identity what it needs, or name a loopback listener",
			unproven, host)
	}
	return fmt.Errorf("envoy_url names %s, which is not a loopback host, and the environment names no secrets broker and machine identity (%s): the controller's session would present the Envoy listener no credential, which takes no shared bearer, and a listener off loopback answers a caller that presents none 401; name a loopback listener, or start the controller where %s are set",
		host, strings.Join(ControllerIdentityVariables, ", "), strings.Join(ControllerIdentityVariables, " and "))
}

// daemonBase is a daemon URL the command appends a route to: baseURL keeps an operator's path but
// refuses a query or fragment, which would otherwise turn that route into request data.
func daemonBase(value, key string) (string, error) {
	return baseURL(value, key)
}
