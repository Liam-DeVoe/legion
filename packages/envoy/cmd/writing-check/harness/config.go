package harness

import (
	_ "embed"
	"errors"
	"fmt"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/sjawhar/envoy/agentloop"
	"github.com/sjawhar/envoy/cmd/writing-check/harness/llm"
)

// candidatesFile is candidates.yaml, the roles and the candidate models.
//
//go:embed candidates.yaml
var candidatesFile []byte

// pricesFile is prices.yaml, what each model charges.
//
//go:embed prices.yaml
var pricesFile []byte

// ModelEntry is one model of candidates.yaml.
type ModelEntry struct {
	Family string `yaml:"family" json:"family"`
	ID     string `yaml:"id" json:"id"`
	// Effort is a Claude model's reasoning effort (low, medium or high); empty is none.
	Effort string `yaml:"effort,omitempty" json:"effort,omitempty"`
}

// PendingEntry is a candidate that has no id yet: one of INFRA-3483's deployments, by the stack
// output that will name it.
type PendingEntry struct {
	Family string `yaml:"family" json:"family"`
	Output string `yaml:"output" json:"output"`
}

// Models is candidates.yaml.
type Models struct {
	Roles struct {
		Grader ModelEntry `yaml:"grader"`
		// GraderFallbacks grade, in order, a judgement the grader refuses on every try.
		GraderFallbacks []ModelEntry `yaml:"grader_fallbacks"`
		Writer          ModelEntry   `yaml:"writer"`
		Reader          ModelEntry   `yaml:"reader"`
	} `yaml:"roles"`
	Candidates []ModelEntry   `yaml:"candidates"`
	Pending    []PendingEntry `yaml:"pending"`
}

// Price is one row of prices.yaml, in USD per million tokens.
type Price struct {
	Family     string  `yaml:"family" json:"family"`
	Input      float64 `yaml:"input" json:"input"`
	Output     float64 `yaml:"output" json:"output"`
	CacheWrite float64 `yaml:"cache_write" json:"cache_write"`
	CacheRead  float64 `yaml:"cache_read" json:"cache_read"`
}

// agentloop is the price as agentloop prices a call.
func (p Price) agentloop() agentloop.Price {
	return agentloop.Price{Input: p.Input, Output: p.Output, CacheWrite: p.CacheWrite, CacheRead: p.CacheRead}
}

// PriceSource is where a family's prices were read, and when.
type PriceSource struct {
	Source string `yaml:"source" json:"source"`
	Read   string `yaml:"read" json:"read"`
}

// Prices is prices.yaml.
type Prices struct {
	Sources map[string]PriceSource `yaml:"sources"`
	Prices  map[string]Price       `yaml:"prices"`
}

// Config is the harness's candidate list and price table, checked against each other.
type Config struct {
	Models Models
	Prices Prices
}

// role names.
const (
	RoleGrader         = "grader"
	RoleGraderFallback = "grader_fallback"
	RoleWriter         = "writer"
	RoleReader         = "reader"
	// RoleCandidate is the role of a candidate: the model a change calls.
	RoleCandidate = "candidate"
)

// LoadConfig reads the embedded candidates.yaml and prices.yaml.
func LoadConfig() (Config, error) {
	return parseConfig(candidatesFile, pricesFile)
}

func parseConfig(candidates, prices []byte) (Config, error) {
	var c Config
	if err := yaml.Unmarshal(candidates, &c.Models); err != nil {
		return c, fmt.Errorf("candidates.yaml: %w", err)
	}
	if err := yaml.Unmarshal(prices, &c.Prices); err != nil {
		return c, fmt.Errorf("prices.yaml: %w", err)
	}
	return c, c.check()
}

// check refuses a role that is not Claude, a role that is a candidate, a grader that plays the
// writer or the reader, a model of an unknown family, a model named twice, and a model with no
// price.
func (c Config) check() error {
	roles := c.roles()
	for _, r := range roles {
		if r.model.Family != llm.FamilyClaude {
			return fmt.Errorf("candidates.yaml: the %s is %q of family %q: every role reads session text or grades it, so it is a Claude model", r.name, r.model.ID, r.model.Family)
		}
		if r.model.ID == "" {
			return fmt.Errorf("candidates.yaml: the %s names no model", r.name)
		}
		if slices.ContainsFunc(c.Models.Candidates, func(m ModelEntry) bool { return m.ID == r.model.ID }) {
			return fmt.Errorf("candidates.yaml: the %s %q is also a candidate (R2)", r.name, r.model.ID)
		}
	}
	r := c.Models.Roles
	for _, f := range r.GraderFallbacks {
		if f.ID == r.Writer.ID {
			return errors.New("candidates.yaml: a grader plays the writer (R2)")
		}
		if f.ID == r.Grader.ID || f.ID == r.Reader.ID {
			return fmt.Errorf("candidates.yaml: the fallback grader %q plays another role: it would grade outputs it wrote, or judgements it refused", f.ID)
		}
	}
	if r.Grader.ID == r.Writer.ID {
		return errors.New("candidates.yaml: a grader plays the writer (R2)")
	}
	if r.Grader.ID == r.Reader.ID {
		return errors.New("candidates.yaml: the grader plays the fresh reader (R2)")
	}
	seen := map[string]bool{}
	for _, m := range c.AllModels() {
		switch {
		case m.Family != llm.FamilyClaude && m.Family != llm.FamilyFoundry:
			return fmt.Errorf("candidates.yaml: %q has family %q, not %s or %s", m.ID, m.Family, llm.FamilyClaude, llm.FamilyFoundry)
		case seen[m.ID] && !c.isRole(m.ID):
			return fmt.Errorf("candidates.yaml: %q is named twice", m.ID)
		}
		seen[m.ID] = true
		p, ok := c.Prices.Prices[m.ID]
		if !ok {
			return fmt.Errorf("prices.yaml: %q has no price", m.ID)
		}
		if p.Family != m.Family {
			return fmt.Errorf("prices.yaml: %q is priced as family %q and candidates.yaml names family %q", m.ID, p.Family, m.Family)
		}
		if _, ok := c.Prices.Sources[m.Family]; !ok {
			return fmt.Errorf("prices.yaml: no source for the prices of family %q", m.Family)
		}
	}
	return nil
}

// WithGrader is the config with id as its grader in place of candidates.yaml's, for one run
// (--grader), with the same fallbacks and the grader's effort. It refuses the writer, the fresh
// reader, a fallback grader, a model with no Claude price in prices.yaml, and, unless candidate
// is true, a candidate (R2): a grader grades no output it wrote. An empty id is the config as it
// is.
func (c Config) WithGrader(id string, candidate bool) (Config, error) {
	r := c.Models.Roles
	switch {
	case id == "" || id == r.Grader.ID:
		return c, nil
	case id == r.Writer.ID:
		return c, fmt.Errorf("--grader %q is the writer: a grader does not play the writer (R2)", id)
	case id == r.Reader.ID:
		return c, fmt.Errorf("--grader %q is the fresh reader: a grader plays no other role (R2)", id)
	case slices.ContainsFunc(r.GraderFallbacks, func(m ModelEntry) bool { return m.ID == id }):
		return c, fmt.Errorf("--grader %q is a fallback grader: it would grade the judgements it refused", id)
	case !candidate && c.IsCandidate(id):
		return c, fmt.Errorf("--grader %q is a candidate of candidates.yaml: a grader is no candidate (R2)", id)
	}
	if p, ok := c.Prices.Prices[id]; !ok || p.Family != llm.FamilyClaude {
		return c, fmt.Errorf("--grader %q has no Claude price in prices.yaml", id)
	}
	c.Models.Roles.Grader = ModelEntry{Family: llm.FamilyClaude, ID: id, Effort: r.Grader.Effort}
	return c, nil
}

// IsCandidate is whether id is a candidate of candidates.yaml.
func (c Config) IsCandidate(id string) bool {
	return slices.ContainsFunc(c.Models.Candidates, func(m ModelEntry) bool { return m.ID == id })
}

type namedRole struct {
	name  string
	model ModelEntry
}

func (c Config) roles() []namedRole {
	out := []namedRole{{RoleGrader, c.Models.Roles.Grader}}
	for _, f := range c.Models.Roles.GraderFallbacks {
		out = append(out, namedRole{RoleGraderFallback, f})
	}
	return append(out, namedRole{RoleWriter, c.Models.Roles.Writer}, namedRole{RoleReader, c.Models.Roles.Reader})
}

func (c Config) isRole(id string) bool {
	return slices.ContainsFunc(c.roles(), func(r namedRole) bool { return r.model.ID == id })
}

// AllModels is every role model, then every candidate, each once.
func (c Config) AllModels() []ModelEntry {
	var out []ModelEntry
	for _, r := range c.roles() {
		if !slices.Contains(out, r.model) {
			out = append(out, r.model)
		}
	}
	return append(out, c.Models.Candidates...)
}

// RoleOf is the role a model plays, RoleCandidate for a candidate.
func (c Config) RoleOf(id string) string {
	for _, r := range c.roles() {
		if r.model.ID == id {
			return r.name
		}
	}
	return RoleCandidate
}
