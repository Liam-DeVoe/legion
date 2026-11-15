package main

import (
	"cmp"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/embed"
)

// writing_settings.go reads the settings of the work on agents' texts (INFRA-3231): the reviewer,
// Rewrite and Define, the records of the words people ask about, the written rules' refusal, the
// three model groups those call, the deployments' quotas and a test process's stand-in model. It
// reads them into writingSettings and builds none of them: it calls nothing and reads no file, so
// a model service that is down, or a key file not written yet, never refuses boot.

// The kinds of a model group.
const (
	// modelKindAzure is a chat deployment of an Azure AI Foundry resource, signed in to with the
	// shared Azure sign-in, as the answerer's DISPATCH_ANSWERER=azure is.
	modelKindAzure = "azure"
	// modelKindAnthropic is an Anthropic Messages service, such as the operator's model gateway,
	// which takes the key a file holds.
	modelKindAnthropic = "anthropic"
)

// modelGroups are the three model groups, each by the prefix of its settings and whose group it
// is, as a refusal names it.
var modelGroups = []struct{ prefix, whose string }{
	{"DISPATCH_REVIEWER_MODEL_", "the reviewer's"},
	{"DISPATCH_REWRITE_DEFINE_MODEL_", "Rewrite and Define's"},
	{"DISPATCH_WORD_RECORDS_MODEL_", "the records job's"},
}

// What the reviewer does with one kind of text (DISPATCH_REVIEWER_<KIND>).
const (
	// reviewOff reads no text of the kind.
	reviewOff = "off"
	// reviewAfter posts the text at once and sends the findings to the writer session.
	reviewAfter = "after"
	// reviewHold keeps the text from every person until the review ends and the writer acts.
	reviewHold = "hold"
)

// What posts when the writer does not act on a held text in time (DISPATCH_REVIEWER_UNEDITED).
const (
	uneditedRewrite   = "rewrite"
	uneditedAsWritten = "as_written"
)

var (
	// reviewerRuleSets are the rule sets DISPATCH_REVIEWER_RULES may name, and
	// defaultReviewerRules those it names unset.
	reviewerRuleSets     = []string{"noslop", "phrases", "reader_words", "ask_parts", "structure", "detector"}
	defaultReviewerRules = []string{"noslop", "phrases", "reader_words", "ask_parts"}
	// writingRulesExtraSets are the rule sets DISPATCH_WRITING_RULES_EXTRA may name.
	writingRulesExtraSets = []string{"reader_words", "ask_parts"}
)

const (
	defaultReviewerTimeout      = 120 * time.Second
	defaultReviewerWriterWindow = 5 * time.Minute
	// defaultWordRecordsFrom is DISPATCH_WORD_RECORDS_FROM unset: the first event of the event
	// log the records job's backfill reads.
	defaultWordRecordsFrom = "2026-10-16T00:00:00Z"
)

// writingSettings are the settings of the work on agents' texts, as resolveWritingSettings
// checked them. Every default keeps each change to a text off.
type writingSettings struct {
	// The reviewer's choice for each kind of text an agent writes: reviewOff, reviewAfter or,
	// for every kind but a document version, reviewHold.
	ReviewAsks           string
	ReviewIssues         string
	ReviewComments       string
	ReviewMessages       string
	ReviewDocuments      string
	ReviewDecisionBlocks string
	// ReviewerRules are the rule sets the reviewer applies (DISPATCH_REVIEWER_RULES).
	ReviewerRules []string
	// ReviewerTimeout bounds the reviewer's model call (DISPATCH_REVIEWER_TIMEOUT).
	ReviewerTimeout time.Duration
	// ReviewerWriterWindow is the time a writer session has to act on a held text
	// (DISPATCH_REVIEWER_WRITER_WINDOW).
	ReviewerWriterWindow time.Duration
	// ReviewerUnedited is uneditedRewrite or uneditedAsWritten (DISPATCH_REVIEWER_UNEDITED).
	ReviewerUnedited string
	// The three model groups. A group whose Kind is "" is unset: no model.
	ReviewerModel      modelGroup
	RewriteDefineModel modelGroup
	WordRecordsModel   modelGroup
	// WordRecords runs the records job while WordRecordsModel is set (DISPATCH_WORD_RECORDS).
	WordRecords bool
	// WordRecordsFrom is the time the records job's first run reads from
	// (DISPATCH_WORD_RECORDS_FROM).
	WordRecordsFrom time.Time
	// WritingRulesRefusal turns on the dispatch command's refusal of R6
	// (DISPATCH_WRITING_RULES_REFUSAL), and WritingRulesExtra are the rule sets it shows beside
	// the written rules (DISPATCH_WRITING_RULES_EXTRA).
	WritingRulesRefusal bool
	WritingRulesExtra   []string
	// DeploymentQuotas is each deployment's tokens-per-minute quota by its name
	// (DISPATCH_MODEL_DEPLOYMENT_QUOTAS), which the process's pacer (modelpace) takes.
	DeploymentQuotas map[string]int
	// TestModelURL is DISPATCH_TEST_MODEL_URL, read only under the test hooks: the stand-in
	// model every set group's Endpoint is.
	TestModelURL string
}

// modelGroup is one model group as resolveModelGroup checked it. Kind is "" where the group is
// unset; the rest belongs to a set group, Kind's fields alone.
type modelGroup struct {
	Kind string
	// Endpoint is the base URL the kind's route is appended to: the group's own, or the stand-in
	// model's (StandIn).
	Endpoint string
	// Deployment is what each request names as its model: the Azure deployment's name or the
	// Anthropic model's id.
	Deployment string
	// APIVersion is the api-version query value of an azure group's requests, "" for none.
	APIVersion string
	// Scope is the token scope an azure group's sign-in asks for.
	Scope string
	// KeyFile is the file holding an anthropic group's key, read on each call.
	KeyFile string
	// TokensPerMinute is the group's ceiling on its share of its deployment's quota.
	TokensPerMinute int
	// TenantID, ClientID and FederatedToken are the shared Azure sign-in an azure group signs in
	// with.
	TenantID       string
	ClientID       string
	FederatedToken string
	// StandIn says Endpoint is DISPATCH_TEST_MODEL_URL's stand-in, called with no credential.
	StandIn bool
}

// resolveWritingSettings reads the settings of the work on agents' texts and refuses, naming the
// setting, every value the work could not run on: a choice outside its values, a rule set no
// reviewer applies, a duration or a time that does not parse, a malformed quota, a reviewer that
// reviews with no model group, a model group set in part (resolveModelGroup), and
// DISPATCH_TEST_MODEL_URL without the test hooks.
func resolveWritingSettings(env settingValues, testHooksEnabled bool) (writingSettings, error) {
	var cfg writingSettings
	if err := testHookURL(env, "DISPATCH_TEST_MODEL_URL", testHooksEnabled, &cfg.TestModelURL); err != nil {
		return writingSettings{}, err
	}
	for i, group := range []*modelGroup{&cfg.ReviewerModel, &cfg.RewriteDefineModel, &cfg.WordRecordsModel} {
		resolved, err := resolveModelGroup(env, modelGroups[i].prefix, modelGroups[i].whose, cfg.TestModelURL)
		if err != nil {
			return writingSettings{}, err
		}
		*group = resolved
	}
	holdable := []string{reviewOff, reviewAfter, reviewHold}
	for _, row := range []struct {
		name    string
		target  *string
		choices []string
	}{
		{"DISPATCH_REVIEWER_ASKS", &cfg.ReviewAsks, holdable},
		{"DISPATCH_REVIEWER_ISSUES", &cfg.ReviewIssues, holdable},
		{"DISPATCH_REVIEWER_COMMENTS", &cfg.ReviewComments, holdable},
		{"DISPATCH_REVIEWER_MESSAGES", &cfg.ReviewMessages, holdable},
		{"DISPATCH_REVIEWER_DOCUMENTS", &cfg.ReviewDocuments, []string{reviewOff, reviewAfter}},
		{"DISPATCH_REVIEWER_DECISION_BLOCKS", &cfg.ReviewDecisionBlocks, holdable},
	} {
		choice, err := choiceSetting(env, row.name, row.choices, reviewOff)
		if err != nil {
			return writingSettings{}, err
		}
		if choice != reviewOff && cfg.ReviewerModel.Kind == "" {
			return writingSettings{}, fmt.Errorf("%s=%s needs the reviewer's model group (%sKIND and the settings beside it): with no model the reviewer reads nothing", row.name, choice, modelGroups[0].prefix)
		}
		*row.target = choice
	}
	var err error
	if cfg.ReviewerRules, err = listSetting(env, "DISPATCH_REVIEWER_RULES", reviewerRuleSets, defaultReviewerRules); err != nil {
		return writingSettings{}, err
	}
	if cfg.ReviewerTimeout, err = durationSetting(env, "DISPATCH_REVIEWER_TIMEOUT", defaultReviewerTimeout); err != nil {
		return writingSettings{}, err
	}
	if cfg.ReviewerWriterWindow, err = durationSetting(env, "DISPATCH_REVIEWER_WRITER_WINDOW", defaultReviewerWriterWindow); err != nil {
		return writingSettings{}, err
	}
	if cfg.ReviewerUnedited, err = choiceSetting(env, "DISPATCH_REVIEWER_UNEDITED", []string{uneditedRewrite, uneditedAsWritten}, uneditedRewrite); err != nil {
		return writingSettings{}, err
	}
	if cfg.WordRecords, err = onOffSetting(env, "DISPATCH_WORD_RECORDS", true); err != nil {
		return writingSettings{}, err
	}
	from := cmp.Or(strings.TrimSpace(env.get("DISPATCH_WORD_RECORDS_FROM")), defaultWordRecordsFrom)
	if cfg.WordRecordsFrom, err = time.Parse(time.RFC3339, from); err != nil {
		return writingSettings{}, fmt.Errorf("DISPATCH_WORD_RECORDS_FROM=%q (expected an RFC 3339 time, such as %s)", from, defaultWordRecordsFrom)
	}
	if cfg.WritingRulesRefusal, err = onOffSetting(env, "DISPATCH_WRITING_RULES_REFUSAL", false); err != nil {
		return writingSettings{}, err
	}
	if cfg.WritingRulesExtra, err = listSetting(env, "DISPATCH_WRITING_RULES_EXTRA", writingRulesExtraSets, nil); err != nil {
		return writingSettings{}, err
	}
	if cfg.DeploymentQuotas, err = deploymentQuotas(env); err != nil {
		return writingSettings{}, err
	}
	return cfg, nil
}

// modelGroupsReadSignIn reports whether a model group reads the shared Azure sign-in beside an
// azure embedder and an azure answerer: a set group whose kind is not anthropic. An azure group
// reads it, and a group whose kind is missing or unknown is counted too, so resolveModelGroup's
// refusal of that kind is the one an operator reads, not a refusal of the sign-in beside it.
func modelGroupsReadSignIn(env settingValues) bool {
	for _, group := range modelGroups {
		if len(settingsSet(env, group.prefix)) > 0 && strings.TrimSpace(env.get(group.prefix+"KIND")) != modelKindAnthropic {
			return true
		}
	}
	return false
}

// modelKinds is what each kind of model group reads: the schemes its endpoint may have, the route
// its client appends to the endpoint, and the settings of a group it takes no value from, with
// why.
var modelKinds = map[string]struct {
	schemes []string
	route   string
	notRead []string
	why     string
}{
	modelKindAzure: {schemes: []string{"https"}, route: "/chat/completions", notRead: []string{"KEY_FILE"},
		why: "an azure group signs in with the shared Azure sign-in and presents no key"},
	// The model gateway serves plain HTTP inside the cluster.
	modelKindAnthropic: {schemes: []string{"http", "https"}, route: "/v1/messages", notRead: []string{"API_VERSION", "SCOPE"},
		why: "an anthropic group sends no api-version and asks for no Azure token"},
}

// resolveModelGroup reads the model group whose settings begin with prefix. A group none of whose
// settings is set is unset, which is no model. A set group is refused, naming the settings, when
// its kind is neither azure nor anthropic; when it is missing a setting it needs (its kind, its
// endpoint, its deployment, its tokens-per-minute ceiling, and an anthropic group's key file);
// when it sets one its kind does not read (an azure group's key file, an anthropic group's
// api-version or scope); when its endpoint or its ceiling is malformed; and when an azure group
// finds the shared Azure sign-in unset or set in part. Under testModelURL, the stand-in takes the
// place of the group's endpoint and credential: the group needs neither, refuses its endpoint,
// key file and scope, and reads no sign-in.
func resolveModelGroup(env settingValues, prefix, whose, testModelURL string) (modelGroup, error) {
	set := settingsSet(env, prefix)
	if len(set) == 0 {
		return modelGroup{}, nil
	}
	value := func(field string) string { return strings.TrimSpace(env.get(prefix + field)) }
	group := modelGroup{Kind: value("KIND"), Deployment: value("DEPLOYMENT"), APIVersion: value("API_VERSION")}
	kind, known := modelKinds[group.Kind]
	if group.Kind != "" && !known {
		return modelGroup{}, fmt.Errorf("%sKIND=%q (expected azure or anthropic)", prefix, group.Kind)
	}
	needed := []string{"KIND", "ENDPOINT", "DEPLOYMENT", "TOKENS_PER_MINUTE"}
	switch {
	case testModelURL != "":
		needed = slices.DeleteFunc(needed, func(field string) bool { return field == "ENDPOINT" })
	case group.Kind == modelKindAnthropic:
		needed = append(needed, "KEY_FILE")
	}
	var missing []string
	for _, field := range needed {
		if value(field) == "" {
			missing = append(missing, prefix+field)
		}
	}
	if len(missing) > 0 {
		return modelGroup{}, fmt.Errorf("%s set, but %s missing: %s model group is set whole or not at all", strings.Join(set, ", "), strings.Join(missing, ", "), whose)
	}
	for _, field := range kind.notRead {
		if value(field) != "" {
			return modelGroup{}, fmt.Errorf("%s%s set, but %sKIND is %s: %s", prefix, field, prefix, group.Kind, kind.why)
		}
	}
	tokens, err := positiveSetting(env, prefix+"TOKENS_PER_MINUTE")
	if err != nil {
		return modelGroup{}, err
	}
	group.TokensPerMinute = tokens
	if testModelURL != "" {
		for _, field := range []string{"ENDPOINT", "KEY_FILE", "SCOPE"} {
			if value(field) != "" {
				return modelGroup{}, fmt.Errorf("%s%s set beside DISPATCH_TEST_MODEL_URL: the stand-in model takes the place of the group's endpoint and credential, so set one or the other", prefix, field)
			}
		}
		group.Endpoint, group.StandIn = testModelURL, true
		return group, nil
	}
	group.Endpoint = value("ENDPOINT")
	parsed, err := url.Parse(group.Endpoint)
	if err != nil || !slices.Contains(kind.schemes, parsed.Scheme) || parsed.Host == "" || strings.ContainsAny(group.Endpoint, "?#") {
		return modelGroup{}, fmt.Errorf("%sENDPOINT=%q (expected an absolute %s URL with a host and no query or fragment, the base URL %s is appended to)",
			prefix, group.Endpoint, strings.Join(kind.schemes, " or "), kind.route)
	}
	if group.Kind == modelKindAnthropic {
		group.KeyFile = value("KEY_FILE")
		return group, nil
	}
	group.Scope = cmp.Or(value("SCOPE"), embed.DefaultEntraScope)
	signIn := readAzureSignIn(env)
	group.TenantID, group.ClientID, group.FederatedToken = signIn.TenantID, signIn.ClientID, signIn.TokenFile
	switch {
	case len(signIn.set) == 0:
		return modelGroup{}, fmt.Errorf("%sKIND=azure needs the keyless Azure sign-in (%s): %s model group takes no API key", prefix, azureKeylessNames, whose)
	case len(signIn.missing) > 0:
		return modelGroup{}, signIn.refusePartial()
	}
	return group, nil
}

// deploymentQuotas reads DISPATCH_MODEL_DEPLOYMENT_QUOTAS: a comma list of name=tokens_per_minute,
// each name once, each quota a positive integer. Unset, it is nil.
func deploymentQuotas(env settingValues) (map[string]int, error) {
	const name = "DISPATCH_MODEL_DEPLOYMENT_QUOTAS"
	raw := strings.TrimSpace(env.get(name))
	if raw == "" {
		return nil, nil
	}
	quotas := map[string]int{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		deployment, tokens, found := strings.Cut(entry, "=")
		deployment, tokens = strings.TrimSpace(deployment), strings.TrimSpace(tokens)
		quota, err := strconv.Atoi(tokens)
		if !found || deployment == "" || err != nil || strings.TrimLeft(tokens, "0123456789") != "" || quota < 1 {
			return nil, fmt.Errorf("%s names %q (expected a comma list of name=tokens_per_minute, each a deployment's name and its quota, a positive integer)", name, entry)
		}
		if _, twice := quotas[deployment]; twice {
			return nil, fmt.Errorf("%s names %s twice", name, deployment)
		}
		quotas[deployment] = quota
	}
	return quotas, nil
}

// onOffSetting reads a setting that is on or off, unset or empty being fallback.
func onOffSetting(env settingValues, name string, fallback bool) (bool, error) {
	switch value := strings.TrimSpace(env.get(name)); value {
	case "":
		return fallback, nil
	case "on":
		return true, nil
	case "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s=%q (expected on or off)", name, value)
	}
}

// choiceSetting reads a setting that is one of choices, unset or empty being fallback.
func choiceSetting(env settingValues, name string, choices []string, fallback string) (string, error) {
	value := strings.TrimSpace(env.get(name))
	switch {
	case value == "":
		return fallback, nil
	case slices.Contains(choices, value):
		return value, nil
	}
	return "", fmt.Errorf("%s=%q (expected %s or %s)", name, value, strings.Join(choices[:len(choices)-1], ", "), choices[len(choices)-1])
}

// listSetting reads a comma list of names from known, each once in the order given; unset or
// empty, it is fallback.
func listSetting(env settingValues, name string, known, fallback []string) ([]string, error) {
	raw := strings.TrimSpace(env.get(name))
	if raw == "" {
		return slices.Clone(fallback), nil
	}
	list := []string{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" || slices.Contains(list, item) {
			continue
		}
		if !slices.Contains(known, item) {
			return nil, fmt.Errorf("%s names %q, which is not one of %s", name, item, strings.Join(known, ", "))
		}
		list = append(list, item)
	}
	return list, nil
}

// durationSetting reads a positive Go duration, unset or empty being fallback.
func durationSetting(env settingValues, name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(env.get(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s=%q (expected a positive Go duration)", name, raw)
	}
	return value, nil
}
