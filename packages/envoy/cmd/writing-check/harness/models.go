package harness

import (
	"flag"
	"fmt"
	"os"

	"github.com/sjawhar/envoy/cmd/writing-check/harness/claude"
	"github.com/sjawhar/envoy/cmd/writing-check/harness/llm"
	"github.com/sjawhar/envoy/internal/dispatch/embed"
)

// Where the pod's model access lives where a flag names none.
const (
	// defaultKeyFile is the model gateway's key in a Legion pod.
	defaultKeyFile = "/var/run/model-gateway-key/key"
	// defaultTokenFile is the pod's ServiceAccount token, which the Foundry account's dev
	// identity admits.
	defaultTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	// defaultAPIVersion is the api-version the Foundry chat route takes.
	defaultAPIVersion = "2024-05-01-preview"
	// DefaultConcurrency is the model calls a run has in flight at once.
	DefaultConcurrency = 6
)

// modelFlags are the flags that say how a command reaches the models: the gateway for Claude,
// the Foundry account and its sign-in, and the calls in flight.
type modelFlags struct {
	gateway, keyFile                   string
	foundry, apiVersion                string
	tenant, clientID, tokenFile, scope string
	concurrency                        int
}

func addModelFlags(fs *flag.FlagSet) *modelFlags {
	m := &modelFlags{}
	fs.StringVar(&m.gateway, "gateway", os.Getenv("ANTHROPIC_BASE_URL"), "the model gateway's base URL, which serves Anthropic Messages (default $ANTHROPIC_BASE_URL)")
	fs.StringVar(&m.keyFile, "gateway-key-file", defaultKeyFile, "the file holding the gateway's key (read on each call, never printed)")
	fs.StringVar(&m.foundry, "foundry-endpoint", "", "the Foundry account's models inference endpoint, its base URL with /models on the end (for a foundry model)")
	fs.StringVar(&m.apiVersion, "foundry-api-version", defaultAPIVersion, "the api-version the Foundry chat route takes")
	fs.StringVar(&m.tenant, "azure-tenant", "", "the Entra tenant id of the Foundry account's keyless sign-in")
	fs.StringVar(&m.clientID, "azure-client-id", "", "the Entra client id the pod's token is exchanged for")
	fs.StringVar(&m.tokenFile, "federated-token-file", defaultTokenFile, "the federated token Entra trusts for the client id")
	fs.StringVar(&m.scope, "scope", embed.DefaultEntraScope, "the Entra token scope")
	fs.IntVar(&m.concurrency, "concurrency", DefaultConcurrency, "the model calls in flight at once")
	return m
}

// runner is the one Runner of a command: every client it builds shares its bound.
func (m *modelFlags) runner() (*llm.Runner, error) {
	if m.concurrency < 1 {
		return nil, fmt.Errorf("--concurrency must be at least 1")
	}
	return &llm.Runner{Limit: llm.NewLimit(m.concurrency)}, nil
}

// foundryConfig is the Foundry account the flags name.
func (m *modelFlags) foundryConfig() llm.FoundryConfig {
	return llm.FoundryConfig{
		Endpoint: m.foundry, APIVersion: m.apiVersion,
		Entra: embed.EntraConfig{TenantID: m.tenant, ClientID: m.clientID, TokenFile: m.tokenFile, Scope: m.scope},
	}
}

// claudeClient is the client of a Claude model of the config.
func (m *modelFlags) claudeClient(cfg Config, model ModelEntry, runner *llm.Runner) (*claude.Client, error) {
	price, ok := cfg.Prices.Prices[model.ID]
	if !ok {
		return nil, fmt.Errorf("prices.yaml has no price for %q", model.ID)
	}
	return claude.New(m.gateway, m.keyFile, model.ID, model.Effort, price.agentloop(), runner)
}

// grader is the grader of the config, with its fallbacks, reading the context inputs give.
func (m *modelFlags) grader(cfg Config, inputs GraderInputs, runner *llm.Runner) (*Grader, error) {
	model, err := m.claudeClient(cfg, cfg.Models.Roles.Grader, runner)
	if err != nil {
		return nil, err
	}
	var fallbacks []*claude.Client
	for _, f := range cfg.Models.Roles.GraderFallbacks {
		c, err := m.claudeClient(cfg, f, runner)
		if err != nil {
			return nil, err
		}
		fallbacks = append(fallbacks, c)
	}
	return NewGrader(model, fallbacks, inputs), nil
}
