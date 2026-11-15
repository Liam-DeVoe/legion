package main

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/embed"
)

const (
	reviewerModel      = "DISPATCH_REVIEWER_MODEL_"
	rewriteDefineModel = "DISPATCH_REWRITE_DEFINE_MODEL_"
	wordRecordsModel   = "DISPATCH_WORD_RECORDS_MODEL_"
)

// groupOf is each model group of a resolved boot, by its settings' prefix.
var groupOf = map[string]func(writingSettings) modelGroup{
	reviewerModel:      func(w writingSettings) modelGroup { return w.ReviewerModel },
	rewriteDefineModel: func(w writingSettings) modelGroup { return w.RewriteDefineModel },
	wordRecordsModel:   func(w writingSettings) modelGroup { return w.WordRecordsModel },
}

// azureGroup is a whole azure model group under prefix, with the shared Azure sign-in beside it,
// which the group reads with no azure embedder or answerer.
func azureGroup(prefix string, overrides map[string]string) map[string]string {
	return withOverrides(withOverrides(map[string]string{
		prefix + "KIND":              "azure",
		prefix + "ENDPOINT":          "https://models.example/models",
		prefix + "DEPLOYMENT":        "grok-4.7",
		prefix + "TOKENS_PER_MINUTE": "60000",
	}, azureFederated), overrides)
}

// anthropicGroup is a whole anthropic model group under prefix, at a gateway over plain HTTP.
func anthropicGroup(prefix string, overrides map[string]string) map[string]string {
	return withOverrides(map[string]string{
		prefix + "KIND":              "anthropic",
		prefix + "ENDPOINT":          "http://model-gateway.example:4000",
		prefix + "DEPLOYMENT":        "claude-haiku-4-5-20251001",
		prefix + "KEY_FILE":          "/var/run/model-gateway-key/key",
		prefix + "TOKENS_PER_MINUTE": "40000",
	}, overrides)
}

// standInGroup is a model group under prefix as a test process sets it beside the stand-in model:
// its kind, its deployment and its ceiling, under the test hooks.
func standInGroup(prefix, kind string, overrides map[string]string) map[string]string {
	return withOverrides(map[string]string{
		"DISPATCH_TEST_HOOKS":        "1",
		"DISPATCH_TEST_MODEL_URL":    "http://127.0.0.1:9040",
		prefix + "KIND":              kind,
		prefix + "DEPLOYMENT":        "stand-in-" + strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(prefix, "DISPATCH_"), "_MODEL_")),
		prefix + "TOKENS_PER_MINUTE": "1000000",
	}, overrides)
}

// Every default keeps each change to a text off: no kind is reviewed, the written rules refuse
// nothing, no model group is set, so neither Rewrite and Define nor the records job runs, and
// the deployments have no quota of their own.
func TestEveryWritingDefaultKeepsEachChangeOff(t *testing.T) {
	writing := resolveWith(t, nil).Writing
	for name, choice := range map[string]string{
		"DISPATCH_REVIEWER_ASKS": writing.ReviewAsks, "DISPATCH_REVIEWER_ISSUES": writing.ReviewIssues,
		"DISPATCH_REVIEWER_COMMENTS": writing.ReviewComments, "DISPATCH_REVIEWER_MESSAGES": writing.ReviewMessages,
		"DISPATCH_REVIEWER_DOCUMENTS": writing.ReviewDocuments, "DISPATCH_REVIEWER_DECISION_BLOCKS": writing.ReviewDecisionBlocks,
	} {
		if choice != reviewOff {
			t.Errorf("%s = %q, want off", name, choice)
		}
	}
	if writing.WritingRulesRefusal || len(writing.WritingRulesExtra) != 0 {
		t.Errorf("the written rules: refusal %t, extra %v, want off and none", writing.WritingRulesRefusal, writing.WritingRulesExtra)
	}
	for prefix, group := range groupOf {
		if got := group(writing); got != (modelGroup{}) {
			t.Errorf("%s group = %+v, want unset", prefix, got)
		}
	}
	if writing.DeploymentQuotas != nil || writing.TestModelURL != "" {
		t.Errorf("quotas %v, stand-in %q, want neither", writing.DeploymentQuotas, writing.TestModelURL)
	}
}

// A whole group of either kind parses into the group the server's model clients are built from.
func TestAWholeModelGroupParses(t *testing.T) {
	for prefix, group := range groupOf {
		t.Run(prefix, func(t *testing.T) {
			if got, want := group(resolveWith(t, azureGroup(prefix, nil)).Writing), (modelGroup{
				Kind: "azure", Endpoint: "https://models.example/models", Deployment: "grok-4.7",
				Scope: embed.DefaultEntraScope, TokensPerMinute: 60000,
				TenantID: "tenant-id", ClientID: "client-id", FederatedToken: "/var/run/secrets/azure-federation/token",
			}); got != want {
				t.Errorf("azure group = %+v, want %+v", got, want)
			}
			if got, want := group(resolveWith(t, anthropicGroup(prefix, nil)).Writing), (modelGroup{
				Kind: "anthropic", Endpoint: "http://model-gateway.example:4000", Deployment: "claude-haiku-4-5-20251001",
				KeyFile: "/var/run/model-gateway-key/key", TokensPerMinute: 40000,
			}); got != want {
				t.Errorf("anthropic group = %+v, want %+v", got, want)
			}
		})
	}
}

// A group set in part refuses the boot, naming each setting it is missing: its kind, its endpoint,
// its deployment and its ceiling, and an anthropic group's key file.
func TestAModelGroupSetInPartRefusesBootNamingTheMissingSetting(t *testing.T) {
	for prefix := range groupOf {
		t.Run(prefix, func(t *testing.T) {
			for _, field := range []string{"KIND", "ENDPOINT", "DEPLOYMENT", "TOKENS_PER_MINUTE"} {
				refusedWith(t, azureGroup(prefix, map[string]string{prefix + field: ""}), prefix+field+" missing")
				refusedWith(t, anthropicGroup(prefix, map[string]string{prefix + field: " "}), prefix+field+" missing")
			}
			refusedWith(t, anthropicGroup(prefix, map[string]string{prefix + "KEY_FILE": ""}), prefix+"KEY_FILE missing")
			// One setting alone names every one the group still needs.
			refusedWith(t, map[string]string{prefix + "DEPLOYMENT": "grok-4.7"},
				prefix+"DEPLOYMENT set, but "+prefix+"KIND, "+prefix+"ENDPOINT, "+prefix+"TOKENS_PER_MINUTE missing")
		})
	}
}

// DISPATCH_TEST_MODEL_URL is a test process's alone: without DISPATCH_TEST_HOOKS=1 it refuses the
// boot. Under the hooks every set group's calls go to it, each naming the group's deployment,
// with no credential: the group needs no endpoint, key file or sign-in, and refuses them.
func TestTheStandInModelURLNeedsTheTestHooks(t *testing.T) {
	refusedWith(t, map[string]string{"DISPATCH_TEST_MODEL_URL": "http://127.0.0.1:9040"}, "DISPATCH_TEST_HOOKS=1 required with DISPATCH_TEST_MODEL_URL")
	refusedWith(t, standInGroup(reviewerModel, "anthropic", map[string]string{"DISPATCH_TEST_HOOKS": ""}), "DISPATCH_TEST_HOOKS=1 required with DISPATCH_TEST_MODEL_URL")
	env := withOverrides(standInGroup(reviewerModel, "anthropic", nil), standInGroup(wordRecordsModel, "azure", map[string]string{wordRecordsModel + "API_VERSION": "2024-05-01-preview"}))
	writing := resolveWith(t, env).Writing
	if got, want := writing.ReviewerModel, (modelGroup{Kind: "anthropic", Endpoint: "http://127.0.0.1:9040", Deployment: "stand-in-reviewer", TokensPerMinute: 1000000, StandIn: true}); got != want {
		t.Errorf("reviewer's group = %+v, want %+v", got, want)
	}
	if got, want := writing.WordRecordsModel, (modelGroup{Kind: "azure", Endpoint: "http://127.0.0.1:9040", Deployment: "stand-in-word_records", APIVersion: "2024-05-01-preview", TokensPerMinute: 1000000, StandIn: true}); got != want {
		t.Errorf("records job's group = %+v, want %+v", got, want)
	}
	if writing.RewriteDefineModel != (modelGroup{}) {
		t.Errorf("Rewrite and Define's group = %+v, want unset: the stand-in sets no group", writing.RewriteDefineModel)
	}
	for _, field := range []string{"ENDPOINT", "KEY_FILE", "SCOPE"} {
		refusedWith(t, standInGroup(rewriteDefineModel, "azure", map[string]string{rewriteDefineModel + field: "x"}), rewriteDefineModel+field)
	}
}

// writingSettingCases are the cases TestEverySettingReachesItsReader runs for the rows of the work
// on agents' texts: each hands its row to resolveWritingSettings through the table.
func writingSettingCases() map[string]func(t *testing.T) {
	cases := map[string]func(t *testing.T){
		"DISPATCH_REVIEWER_RULES": func(t *testing.T) {
			if got := resolveWith(t, nil).Writing.ReviewerRules; !slices.Equal(got, []string{"noslop", "phrases", "reader_words", "ask_parts"}) {
				t.Errorf("unset: ReviewerRules = %v", got)
			}
			if got := resolveWith(t, map[string]string{"DISPATCH_REVIEWER_RULES": " detector, structure,,detector "}).Writing.ReviewerRules; !slices.Equal(got, []string{"detector", "structure"}) {
				t.Errorf("ReviewerRules = %v, want detector and structure, each once", got)
			}
			refusedWith(t, map[string]string{"DISPATCH_REVIEWER_RULES": "noslop,passive-voice"}, `DISPATCH_REVIEWER_RULES names "passive-voice"`)
		},
		"DISPATCH_REVIEWER_TIMEOUT":       durationCase("DISPATCH_REVIEWER_TIMEOUT", 120*time.Second, func(w writingSettings) time.Duration { return w.ReviewerTimeout }),
		"DISPATCH_REVIEWER_WRITER_WINDOW": durationCase("DISPATCH_REVIEWER_WRITER_WINDOW", 5*time.Minute, func(w writingSettings) time.Duration { return w.ReviewerWriterWindow }),
		"DISPATCH_REVIEWER_UNEDITED": func(t *testing.T) {
			if got := resolveWith(t, nil).Writing.ReviewerUnedited; got != uneditedRewrite {
				t.Errorf("unset: ReviewerUnedited = %q, want rewrite", got)
			}
			if got := resolveWith(t, map[string]string{"DISPATCH_REVIEWER_UNEDITED": " as_written "}).Writing.ReviewerUnedited; got != uneditedAsWritten {
				t.Errorf("ReviewerUnedited = %q, want as_written", got)
			}
			refusedWith(t, map[string]string{"DISPATCH_REVIEWER_UNEDITED": "post"}, `DISPATCH_REVIEWER_UNEDITED="post" (expected rewrite or as_written)`)
		},
		"DISPATCH_WORD_RECORDS": func(t *testing.T) {
			if !resolveWith(t, nil).Writing.WordRecords {
				t.Error("unset: WordRecords off, want on")
			}
			if resolveWith(t, map[string]string{"DISPATCH_WORD_RECORDS": "off"}).Writing.WordRecords {
				t.Error("off: WordRecords on")
			}
			refusedWith(t, map[string]string{"DISPATCH_WORD_RECORDS": "1"}, `DISPATCH_WORD_RECORDS="1" (expected on or off)`)
		},
		"DISPATCH_WORD_RECORDS_FROM": func(t *testing.T) {
			if got := resolveWith(t, nil).Writing.WordRecordsFrom; !got.Equal(time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)) {
				t.Errorf("unset: WordRecordsFrom = %s", got)
			}
			if got := resolveWith(t, map[string]string{"DISPATCH_WORD_RECORDS_FROM": " 2026-11-01T12:00:00+01:00 "}).Writing.WordRecordsFrom; !got.Equal(time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC)) {
				t.Errorf("WordRecordsFrom = %s", got)
			}
			refusedWith(t, map[string]string{"DISPATCH_WORD_RECORDS_FROM": "2026-11-01"}, `DISPATCH_WORD_RECORDS_FROM="2026-11-01" (expected an RFC 3339 time`)
		},
		"DISPATCH_WRITING_RULES_REFUSAL": func(t *testing.T) {
			if resolveWith(t, nil).Writing.WritingRulesRefusal {
				t.Error("unset: the refusal is on, want off")
			}
			if !resolveWith(t, map[string]string{"DISPATCH_WRITING_RULES_REFUSAL": " on "}).Writing.WritingRulesRefusal {
				t.Error("on: the refusal is off")
			}
			refusedWith(t, map[string]string{"DISPATCH_WRITING_RULES_REFUSAL": "yes"}, `DISPATCH_WRITING_RULES_REFUSAL="yes" (expected on or off)`)
		},
		"DISPATCH_WRITING_RULES_EXTRA": func(t *testing.T) {
			if got := resolveWith(t, map[string]string{"DISPATCH_WRITING_RULES_EXTRA": "ask_parts, reader_words"}).Writing.WritingRulesExtra; !slices.Equal(got, []string{"ask_parts", "reader_words"}) {
				t.Errorf("WritingRulesExtra = %v", got)
			}
			refusedWith(t, map[string]string{"DISPATCH_WRITING_RULES_EXTRA": "noslop"}, `DISPATCH_WRITING_RULES_EXTRA names "noslop", which is not one of reader_words, ask_parts`)
		},
		"DISPATCH_MODEL_DEPLOYMENT_QUOTAS": func(t *testing.T) {
			got := resolveWith(t, map[string]string{"DISPATCH_MODEL_DEPLOYMENT_QUOTAS": " grok-4.7=600000 , claude-haiku-4-5-20251001 = 400000,"}).Writing.DeploymentQuotas
			if want := map[string]int{"grok-4.7": 600000, "claude-haiku-4-5-20251001": 400000}; !maps.Equal(got, want) {
				t.Errorf("DeploymentQuotas = %v, want %v", got, want)
			}
			for _, bad := range []string{"grok-4.7", "grok-4.7=", "=600000", "grok-4.7=0", "grok-4.7=+5", "grok-4.7=six"} {
				refusedWith(t, map[string]string{"DISPATCH_MODEL_DEPLOYMENT_QUOTAS": bad}, "DISPATCH_MODEL_DEPLOYMENT_QUOTAS names "+`"`+bad+`"`)
			}
			refusedWith(t, map[string]string{"DISPATCH_MODEL_DEPLOYMENT_QUOTAS": "grok-4.7=1,grok-4.7=2"}, "DISPATCH_MODEL_DEPLOYMENT_QUOTAS names grok-4.7 twice")
		},
		"DISPATCH_TEST_MODEL_URL": func(t *testing.T) {
			if got := resolveWith(t, map[string]string{"DISPATCH_TEST_HOOKS": "1", "DISPATCH_TEST_MODEL_URL": " http://127.0.0.1:9040 "}).Writing.TestModelURL; got != "http://127.0.0.1:9040" {
				t.Errorf("TestModelURL = %q", got)
			}
			refusedWith(t, map[string]string{"DISPATCH_TEST_MODEL_URL": "http://127.0.0.1:9040"}, "DISPATCH_TEST_HOOKS=1 required with DISPATCH_TEST_MODEL_URL")
			for _, bad := range []string{"127.0.0.1:9040", "ftp://127.0.0.1:9040", "http://127.0.0.1:9040/?x=1", "http:///v1"} {
				refusedWith(t, map[string]string{"DISPATCH_TEST_HOOKS": "1", "DISPATCH_TEST_MODEL_URL": bad}, "DISPATCH_TEST_MODEL_URL="+`"`+bad+`"`)
			}
		},
	}
	holdable := map[string]func(writingSettings) string{
		"DISPATCH_REVIEWER_ASKS":            func(w writingSettings) string { return w.ReviewAsks },
		"DISPATCH_REVIEWER_ISSUES":          func(w writingSettings) string { return w.ReviewIssues },
		"DISPATCH_REVIEWER_COMMENTS":        func(w writingSettings) string { return w.ReviewComments },
		"DISPATCH_REVIEWER_MESSAGES":        func(w writingSettings) string { return w.ReviewMessages },
		"DISPATCH_REVIEWER_DECISION_BLOCKS": func(w writingSettings) string { return w.ReviewDecisionBlocks },
	}
	for name, choiceOf := range holdable {
		cases[name] = reviewChoiceCase(name, []string{reviewAfter, reviewHold}, "off, after or hold", choiceOf)
	}
	cases["DISPATCH_REVIEWER_DOCUMENTS"] = func(t *testing.T) {
		reviewChoiceCase("DISPATCH_REVIEWER_DOCUMENTS", []string{reviewAfter}, "off or after", func(w writingSettings) string { return w.ReviewDocuments })(t)
		refusedWith(t, anthropicGroup(reviewerModel, map[string]string{"DISPATCH_REVIEWER_DOCUMENTS": "hold"}), `DISPATCH_REVIEWER_DOCUMENTS="hold" (expected off or after)`)
	}
	for prefix, group := range groupOf {
		maps.Copy(cases, modelGroupCases(prefix, group))
	}
	return cases
}

// reviewChoiceCase is the case of a DISPATCH_REVIEWER_<KIND> row: off unset, each of choices
// with the reviewer's model group set, refused with no group naming that group, and any other
// value refused naming the values it takes.
func reviewChoiceCase(name string, choices []string, expected string, choiceOf func(writingSettings) string) func(t *testing.T) {
	return func(t *testing.T) {
		if got := choiceOf(resolveWith(t, nil).Writing); got != reviewOff {
			t.Errorf("unset: %s = %q, want off", name, got)
		}
		for _, choice := range choices {
			if got := choiceOf(resolveWith(t, anthropicGroup(reviewerModel, map[string]string{name: " " + choice + " "})).Writing); got != choice {
				t.Errorf("%s = %q, want %s", name, got, choice)
			}
			refusedWith(t, map[string]string{name: choice}, name+"="+choice+" needs the reviewer's model group (DISPATCH_REVIEWER_MODEL_KIND")
		}
		refusedWith(t, anthropicGroup(reviewerModel, map[string]string{name: "on"}), name+`="on" (expected `+expected+")")
	}
}

// durationCase is the case of a duration row: fallback unset, the value set, and a value that is
// not a positive Go duration refused.
func durationCase(name string, fallback time.Duration, valueOf func(writingSettings) time.Duration) func(t *testing.T) {
	return func(t *testing.T) {
		if got := valueOf(resolveWith(t, nil).Writing); got != fallback {
			t.Errorf("unset: %s = %s, want %s", name, got, fallback)
		}
		if got := valueOf(resolveWith(t, map[string]string{name: " 45s "}).Writing); got != 45*time.Second {
			t.Errorf("%s = %s, want 45s", name, got)
		}
		for _, bad := range []string{"0s", "-1m", "45"} {
			refusedWith(t, map[string]string{name: bad}, name+`="`+bad+`" (expected a positive Go duration)`)
		}
	}
}

// modelGroupCases are the cases of one model group's seven rows.
func modelGroupCases(prefix string, groupOf func(writingSettings) modelGroup) map[string]func(t *testing.T) {
	group := func(t *testing.T, env map[string]string) modelGroup {
		t.Helper()
		return groupOf(resolveWith(t, env).Writing)
	}
	return map[string]func(t *testing.T){
		prefix + "KIND": func(t *testing.T) {
			if got := group(t, azureGroup(prefix, map[string]string{prefix + "KIND": " azure "})).Kind; got != "azure" {
				t.Errorf("Kind = %q, want azure", got)
			}
			if got := group(t, anthropicGroup(prefix, nil)).Kind; got != "anthropic" {
				t.Errorf("Kind = %q, want anthropic", got)
			}
			if got := group(t, nil); got != (modelGroup{}) {
				t.Errorf("unset: %+v, want no model", got)
			}
			refusedWith(t, azureGroup(prefix, map[string]string{prefix + "KIND": "openai"}), prefix+`KIND="openai" (expected azure or anthropic)`)
			// An azure group alone reads the shared sign-in; an anthropic group does not.
			refusedWith(t, anthropicGroup(prefix, azureFederated), "DISPATCH_AZURE_TENANT_ID, DISPATCH_AZURE_CLIENT_ID, DISPATCH_AZURE_FEDERATED_TOKEN_FILE set, but neither DISPATCH_EMBEDDER nor DISPATCH_ANSWERER is azure and no model group's kind is azure")
			refusedWith(t, azureGroup(prefix, map[string]string{"DISPATCH_AZURE_CLIENT_ID": ""}), "DISPATCH_AZURE_CLIENT_ID missing")
			refusedWith(t, azureGroup(prefix, map[string]string{"DISPATCH_AZURE_TENANT_ID": "", "DISPATCH_AZURE_CLIENT_ID": "", "DISPATCH_AZURE_FEDERATED_TOKEN_FILE": ""}), prefix+"KIND=azure needs the keyless Azure sign-in")
		},
		prefix + "ENDPOINT": func(t *testing.T) {
			if got := group(t, azureGroup(prefix, map[string]string{prefix + "ENDPOINT": " https://other.example/openai/v1 "})).Endpoint; got != "https://other.example/openai/v1" {
				t.Errorf("Endpoint = %q", got)
			}
			if got := group(t, anthropicGroup(prefix, map[string]string{prefix + "ENDPOINT": "https://gateway.example"})).Endpoint; got != "https://gateway.example" {
				t.Errorf("Endpoint = %q", got)
			}
			// Plain HTTP is an anthropic gateway's alone.
			refusedWith(t, azureGroup(prefix, map[string]string{prefix + "ENDPOINT": "http://models.example/models"}), prefix+`ENDPOINT="http://models.example/models" (expected an absolute https URL`)
			refusedWith(t, anthropicGroup(prefix, map[string]string{prefix + "ENDPOINT": "http://gateway.example?x=1"}), prefix+`ENDPOINT="http://gateway.example?x=1" (expected an absolute http or https URL`)
		},
		prefix + "DEPLOYMENT": func(t *testing.T) {
			if got := group(t, azureGroup(prefix, map[string]string{prefix + "DEPLOYMENT": " Kimi-K3.1-Pro "})).Deployment; got != "Kimi-K3.1-Pro" {
				t.Errorf("Deployment = %q", got)
			}
		},
		prefix + "API_VERSION": func(t *testing.T) {
			if got := group(t, azureGroup(prefix, map[string]string{prefix + "API_VERSION": " 2024-05-01-preview "})).APIVersion; got != "2024-05-01-preview" {
				t.Errorf("APIVersion = %q", got)
			}
			refusedWith(t, anthropicGroup(prefix, map[string]string{prefix + "API_VERSION": "2024-05-01-preview"}), prefix+"API_VERSION set, but "+prefix+"KIND is anthropic")
		},
		prefix + "SCOPE": func(t *testing.T) {
			if got := group(t, azureGroup(prefix, map[string]string{prefix + "SCOPE": " https://ai.azure.com/.default "})).Scope; got != "https://ai.azure.com/.default" {
				t.Errorf("Scope = %q", got)
			}
			if got := group(t, azureGroup(prefix, nil)).Scope; got != embed.DefaultEntraScope {
				t.Errorf("unset: Scope = %q, want the embedder's", got)
			}
			refusedWith(t, anthropicGroup(prefix, map[string]string{prefix + "SCOPE": "https://ai.azure.com/.default"}), prefix+"SCOPE set, but "+prefix+"KIND is anthropic")
		},
		prefix + "KEY_FILE": func(t *testing.T) {
			if got := group(t, anthropicGroup(prefix, map[string]string{prefix + "KEY_FILE": " /keys/gateway "})).KeyFile; got != "/keys/gateway" {
				t.Errorf("KeyFile = %q", got)
			}
			refusedWith(t, azureGroup(prefix, map[string]string{prefix + "KEY_FILE": "/keys/gateway"}), prefix+"KEY_FILE set, but "+prefix+"KIND is azure")
		},
		prefix + "TOKENS_PER_MINUTE": func(t *testing.T) {
			if got := group(t, anthropicGroup(prefix, map[string]string{prefix + "TOKENS_PER_MINUTE": " 90000 "})).TokensPerMinute; got != 90000 {
				t.Errorf("TokensPerMinute = %d, want 90000", got)
			}
			refusedWith(t, anthropicGroup(prefix, map[string]string{prefix + "TOKENS_PER_MINUTE": "0"}), prefix+`TOKENS_PER_MINUTE="0" (expected a positive integer)`)
		},
	}
}
