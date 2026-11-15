package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sjawhar/envoy/agentloop"
	"github.com/sjawhar/envoy/internal/dispatch/embed"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/modelpace"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/wordrecords"
	"github.com/sjawhar/envoy/internal/dispatch/writingcheck"
)

// standInToken is the credential every call to the stand-in model presents (DISPATCH_TEST_MODEL_URL),
// which reads none.
const standInToken = "stand-in"

// agentloopModel is the model a set group calls through agentloop: an azure group's chat
// deployment (agentloop.OpenAIChat), its api-version on every request and a token of the shared
// Azure sign-in at its scope; an anthropic group's Messages service (agentloop.AnthropicMessages),
// its key file read on each call, so a key rewritten under the process is the next call's. A group
// beside the stand-in model presents no credential of its own. An anthropic group asks for no
// thinking (effort "off"). An azure group sends no reasoning effort at all: agentloop sends "off"
// as `none`, which a Foundry chat deployment refuses (measured on grok-4.7, as the detector's
// reader found: writingcheck.ReaderNoEffort). It reads no file and calls nothing.
func (g modelGroup) agentloopModel() (agentloop.Model, error) {
	var token func(context.Context) (string, error)
	switch {
	case g.StandIn:
		token = func(context.Context) (string, error) { return standInToken, nil }
	case g.Kind == modelKindAzure:
		source, err := embed.NewEntraTokenSource(embed.EntraConfig{
			TenantID: g.TenantID, ClientID: g.ClientID, TokenFile: g.FederatedToken, Scope: g.Scope,
		})
		if err != nil {
			return agentloop.Model{}, err
		}
		token = source.Token
	case g.Kind == modelKindAnthropic:
		keyFile := g.KeyFile
		token = func(context.Context) (string, error) {
			key, err := os.ReadFile(keyFile)
			if err != nil {
				return "", fmt.Errorf("read the model group's key file: %w", err)
			}
			return strings.TrimSpace(string(key)), nil
		}
	}
	endpoint := agentloop.Endpoint{BaseURL: strings.TrimRight(g.Endpoint, "/"), Token: token}
	effort := "off"
	switch g.Kind {
	case modelKindAzure:
		endpoint.Kind = agentloop.OpenAIChat
		if g.APIVersion != "" {
			endpoint.Query = url.Values{"api-version": {g.APIVersion}}
		}
		if writingcheck.ReaderNoEffort {
			effort = ""
		}
	case modelKindAnthropic:
		endpoint.Kind = agentloop.AnthropicMessages
	default:
		return agentloop.Model{}, fmt.Errorf("a model group of kind %q calls no model", g.Kind)
	}
	return agentloop.Model{Endpoint: endpoint, ID: g.Deployment, Effort: effort}, nil
}

// pace is the group's share of its deployment on pacer, the process's one pacer (modelpace): its
// tokens-per-minute setting is the ceiling on that share.
func (g modelGroup) pace(pacer *modelpace.Pacer) (*modelpace.Caller, error) {
	endpoint, err := url.Parse(g.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("the model group's endpoint %q is not a URL", g.Endpoint)
	}
	return pacer.Join(modelpace.Share{
		Deployment:      modelpace.Deployment{Kind: g.Kind, Host: endpoint.Host, Name: g.Deployment},
		TokensPerMinute: g.TokensPerMinute,
	}), nil
}

// newWordRecords builds the records job of INFRA-3231's R8 (wordrecords.Job), or nil where it is
// off: DISPATCH_WORD_RECORDS=off, or its model group (DISPATCH_WORD_RECORDS_MODEL_*) unset, each of
// which it logs. The job reads the event log up to horizon's safe horizon from
// DISPATCH_WORD_RECORDS_FROM, and its calls are charged to its group's share of its deployment on
// pacer. It is built from settings resolveWritingSettings has already checked, so an error here is
// a shape that check missed; it calls nothing and reads no file.
func newWordRecords(cfg writingSettings, pacer *modelpace.Pacer, database *store.Store, horizon *events.Horizon) (*wordrecords.Job, error) {
	switch {
	case !cfg.WordRecords:
		slog.Info("dispatch: the records of the words people ask about are off", "setting", "DISPATCH_WORD_RECORDS")
		return nil, nil
	case cfg.WordRecordsModel.Kind == "":
		slog.Info("dispatch: the records of the words people ask about are off: the records job's model group is unset", "setting", modelGroups[2].prefix+"KIND")
		return nil, nil
	}
	model, err := cfg.WordRecordsModel.agentloopModel()
	if err != nil {
		return nil, fmt.Errorf("the records job's model: %w", err)
	}
	pace, err := cfg.WordRecordsModel.pace(pacer)
	if err != nil {
		return nil, fmt.Errorf("the records job's model: %w", err)
	}
	slog.Info("dispatch: records the words people ask agents about", "kind", cfg.WordRecordsModel.Kind,
		"deployment", cfg.WordRecordsModel.Deployment, "stand_in", cfg.WordRecordsModel.StandIn,
		"from", cfg.WordRecordsFrom.Format(time.RFC3339))
	return wordrecords.New(wordrecords.Config{
		Store:      database,
		Horizon:    horizon,
		Classifier: wordrecords.Classifier{Model: model, Pace: pace},
		From:       cfg.WordRecordsFrom,
	}), nil
}
