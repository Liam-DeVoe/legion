package main

import (
	"cmp"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/api"
	"github.com/sjawhar/envoy/internal/dispatch/embed"
	"github.com/sjawhar/envoy/internal/dispatch/reviewer"
	"github.com/sjawhar/envoy/internal/dispatch/wordrecords"
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

// modelGroups are the three model groups, each by the prefix of its settings, whose group it is,
// as a refusal names it, and the reply bound of its calls: the most tokens one reply may hold,
// which a minute's quota must cover (refuseQuotaUnderReplyBound).
var modelGroups = []struct {
	prefix, whose string
	replyBound    int
}{
	{"DISPATCH_REVIEWER_MODEL_", "the reviewer's", reviewer.DefaultOutputTokens},
	{"DISPATCH_REWRITE_DEFINE_MODEL_", "Rewrite and Define's", reviewer.DefaultOutputTokens},
	{"DISPATCH_WORD_RECORDS_MODEL_", "the records job's", wordrecords.CallOutputTokens},
}

// reviewerReplyBound and recordsReplyBound are the two reply bounds of modelGroups, as the
// settings table's descriptions state them.
var (
	reviewerReplyBound = strconv.Itoa(reviewer.DefaultOutputTokens)
	recordsReplyBound  = strconv.Itoa(wordrecords.CallOutputTokens)
)

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
	// reviewerRuleSets are the rule sets DISPATCH_REVIEWER_RULES may name, the reviewer's own
	// (reviewer.AllRuleSets), and defaultReviewerRules those it names unset.
	reviewerRuleSets     = ruleSetNames(reviewer.AllRuleSets...)
	defaultReviewerRules = ruleSetNames(reviewer.RuleNoslop, reviewer.RulePhrases, reviewer.RuleReaderWords, reviewer.RuleAskParts)
	// writingRulesExtraSets are the rule sets DISPATCH_WRITING_RULES_EXTRA may name, the ones the
	// server's writing rules show (api.WritingRulesConfig).
	writingRulesExtraSets = []string{api.WritingRulesReaderWords, api.WritingRulesAskParts}
)

// ruleSetNames is the names of sets, as a setting spells them.
func ruleSetNames(sets ...reviewer.RuleSet) []string {
	names := make([]string, len(sets))
	for i, set := range sets {
		names[i] = string(set)
	}
	return names
}

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
	// TestModels are the models a test request may name in its Dispatch-Test-Model header
	// (DISPATCH_TEST_MODELS), read only under the test hooks, by name: each is the group of its
	// kind's DISPATCH_TEST_MODELS_<KIND>_* settings with the name's id as its Deployment and the
	// name's effort, if it names one, as its Effort.
	TestModels map[string]modelGroup
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
	// Effort is the reasoning effort a test model's name asks for (`<id>:<effort>`,
	// DISPATCH_TEST_MODELS), one of testModelEfforts. It is "" for a model group and for a test
	// model named by its id alone, which ask for their kind's own (agentloopModel).
	Effort string
}

// resolveWritingSettings reads the settings of the work on agents' texts and refuses, naming the
// setting, every value the work could not run on: a choice outside its values, a rule set no
// reviewer applies, a duration or a time that does not parse, a malformed quota, a reviewer that
// reviews with no model group, a model group set in part (resolveModelGroup), a minute's quota
// under the reply bound of the calls it paces (refuseQuotaUnderReplyBound), and
// DISPATCH_TEST_MODEL_URL and DISPATCH_TEST_MODELS without the test hooks. answerer is the
// answerer the boot resolved (resolveAnswerer), whose quota paces a group on its deployment.
func resolveWritingSettings(env settingValues, testHooksEnabled bool, answerer answererConfig) (writingSettings, error) {
	var cfg writingSettings
	if err := testHookURL(env, "DISPATCH_TEST_MODEL_URL", testHooksEnabled, &cfg.TestModelURL); err != nil {
		return writingSettings{}, err
	}
	var err error
	if cfg.TestModels, err = resolveTestModels(env, testHooksEnabled, cfg.TestModelURL); err != nil {
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
	if err := refuseQuotaUnderReplyBound(cfg, answerer); err != nil {
		return writingSettings{}, err
	}
	return cfg, nil
}

// refuseQuotaUnderReplyBound refuses a minute's quota that could serve no call of a set model
// group or of a test model, because it is under the reply bound of the group's calls. The pacer
// (modelpace) charges a call its prompt's estimated tokens and its whole reply bound before the
// call is sent, and refuses for good a call that costs more than a minute's whole quota. Under
// such a quota every call would fail with nothing at boot saying why, and each reviewed text
// would post as written. Three settings can be that quota, and each is weighed as the pacer
// reads it, from the shares main joins the pacer with (modelGroup.share, answererConfig.share):
//
//   - the group's own _TOKENS_PER_MINUTE ceiling;
//   - the quota DISPATCH_MODEL_DEPLOYMENT_QUOTAS names for the group's deployment, which is then
//     the deployment's rate;
//   - where it names none, the quota answerer states, if the group's deployment is the
//     answerer's as the pacer keys it (pacedDeployment). An answerer of another kind, on another
//     endpoint host or of another deployment is another limiter, and is not weighed.
//
// A test model serves the reviewer's calls and Rewrite and Define's, which have one reply bound,
// so it is held to that bound. A quota for a deployment that no group and no test model names is
// not weighed.
func refuseQuotaUnderReplyBound(cfg writingSettings, answerer answererConfig) error {
	const why = "each call is charged its whole reply bound before it is sent, so a smaller minute's quota could serve no call"
	// stated is what the answerer asks of its deployment. A share of no tokens states no quota,
	// as the pacer reads it: the endpoint form's, and that of no answerer.
	stated, err := answerer.share()
	if err != nil {
		return err
	}
	// refuse weighs a set group's ceiling, then the quota that paces its deployment, against
	// bound. ceiling and deployment are the settings that name the group's two.
	refuse := func(group modelGroup, ceiling, deployment, whose string, bound int) error {
		if group.Kind == "" {
			return nil
		}
		asked, err := group.share()
		if err != nil {
			return err
		}
		if asked.TokensPerMinute < bound {
			return fmt.Errorf("%s=%d is under %d, the reply bound of %s calls: %s", ceiling, asked.TokensPerMinute, bound, whose, why)
		}
		if quota, named := cfg.DeploymentQuotas[asked.Deployment.Name]; named {
			if quota < bound {
				return fmt.Errorf("%s names %s=%d, under %d, the reply bound of %s calls, which go to that deployment (%s): %s",
					deploymentQuotasSetting, asked.Deployment.Name, quota, bound, whose, deployment, why)
			}
			return nil
		}
		if stated.StatesQuota && stated.Deployment == asked.Deployment && 0 < stated.TokensPerMinute && stated.TokensPerMinute < bound {
			return fmt.Errorf("%s=%d is under %d, the reply bound of %s calls, which go to the answerer's deployment (%s) and are paced at its quota, since %s names none for it: %s",
				answererQuotaSetting, stated.TokensPerMinute, bound, whose, deployment, deploymentQuotasSetting, why)
		}
		return nil
	}
	for i, group := range []modelGroup{cfg.ReviewerModel, cfg.RewriteDefineModel, cfg.WordRecordsModel} {
		row := modelGroups[i]
		if err := refuse(group, row.prefix+"TOKENS_PER_MINUTE", row.prefix+"DEPLOYMENT", row.whose, row.replyBound); err != nil {
			return err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.TestModels)) {
		group := cfg.TestModels[name]
		if err := refuse(group, testModelPrefixes[group.Kind]+"TOKENS_PER_MINUTE", testModelsSetting, testModelsWhose(group.Kind), reviewer.DefaultOutputTokens); err != nil {
			return err
		}
	}
	return nil
}

// answererQuotaSetting states the quota of the answerer's deployment under DISPATCH_ANSWERER=azure.
const answererQuotaSetting = azureChatSettingPrefix + "TOKENS_PER_MINUTE"

// modelGroupsReadSignIn reports whether a model group reads the shared Azure sign-in beside an
// azure embedder and an azure answerer: a set group whose kind is not anthropic, or a test model
// of DISPATCH_TEST_MODELS whose kind is not. An azure group reads it, and a group whose kind is
// missing or unknown is counted too, so resolveModelGroup's refusal of that kind is the one an
// operator reads, not a refusal of the sign-in beside it.
func modelGroupsReadSignIn(env settingValues) bool {
	for _, group := range modelGroups {
		if len(settingsSet(env, group.prefix)) > 0 && strings.TrimSpace(env.get(group.prefix+"KIND")) != modelKindAnthropic {
			return true
		}
	}
	for _, entry := range strings.Split(env.get(testModelsSetting), ",") {
		if _, kind, found := strings.Cut(entry, "="); found && strings.TrimSpace(kind) != modelKindAnthropic {
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

// testModelsSetting lists the models a test request may name in its Dispatch-Test-Model header.
const testModelsSetting = "DISPATCH_TEST_MODELS"

// testModelPrefixes are the prefixes of the settings of the test models of each kind
// (DISPATCH_TEST_MODELS_<KIND>_*): where a test model of the kind is called, with what credential,
// and the ceiling of its share of its deployment.
var testModelPrefixes = map[string]string{
	modelKindAzure:     "DISPATCH_TEST_MODELS_AZURE_",
	modelKindAnthropic: "DISPATCH_TEST_MODELS_ANTHROPIC_",
}

// testModelsWhose is whose group the test models of kind are, as a refusal names it.
func testModelsWhose(kind string) string {
	return "the " + kind + " test models'"
}

// testModelEfforts are the reasoning efforts a test model's name may ask for: the words agentloop
// reads on each protocol it speaks. off asks for no reasoning (OpenAI's protocols send it as
// `none`, Anthropic's turn thinking off), minimal is Anthropic's low, and low, medium and high are
// each provider's own.
var testModelEfforts = []string{"off", "minimal", "low", "medium", "high"}

// resolveTestModels reads DISPATCH_TEST_MODELS: a comma list of name=kind, each name once, kind
// azure or anthropic, refused without the test hooks. A name is a model's id, or `<id>:<effort>`
// with effort one of testModelEfforts, which asks that model for that effort; an effort not in
// that list is refused, naming it. The test models of one kind are that kind's
// DISPATCH_TEST_MODELS_<KIND>_* settings read as a model group of the kind (resolveTestModelKind),
// each with its name's id as the deployment its requests name. The settings of a kind that no
// listed model has are refused. Unset, it is nil.
func resolveTestModels(env settingValues, testHooksEnabled bool, testModelURL string) (map[string]modelGroup, error) {
	raw := strings.TrimSpace(env.get(testModelsSetting))
	if raw != "" && !testHooksEnabled {
		return nil, fmt.Errorf("DISPATCH_TEST_HOOKS=1 required with %s", testModelsSetting)
	}
	models := map[string]modelGroup{}
	kinds := map[string]modelGroup{}
	for _, entry := range strings.Split(raw, ",") {
		if entry = strings.TrimSpace(entry); entry == "" {
			continue
		}
		name, kind, found := strings.Cut(entry, "=")
		name, kind = strings.TrimSpace(name), strings.TrimSpace(kind)
		id, effort, named := strings.Cut(name, ":")
		prefix, known := testModelPrefixes[kind]
		if !found || id == "" || !known {
			return nil, fmt.Errorf("%s names %q (expected a comma list of name=kind, each a model's name and its kind, azure or anthropic)", testModelsSetting, entry)
		}
		if named && !slices.Contains(testModelEfforts, effort) {
			return nil, fmt.Errorf("%s names %s, whose effort %q is not one agentloop takes (expected <id> or <id>:<effort>, the effort one of %s)",
				testModelsSetting, name, effort, strings.Join(testModelEfforts, ", "))
		}
		if _, twice := models[name]; twice {
			return nil, fmt.Errorf("%s names %s twice", testModelsSetting, name)
		}
		group, resolved := kinds[kind]
		if !resolved {
			var err error
			if group, err = resolveTestModelKind(env, prefix, kind, testModelURL); err != nil {
				return nil, err
			}
			kinds[kind] = group
		}
		group.Deployment, group.Effort = id, effort
		models[name] = group
	}
	for kind, prefix := range testModelPrefixes {
		if _, listed := kinds[kind]; !listed {
			if set := settingsSet(env, prefix); len(set) > 0 {
				return nil, fmt.Errorf("%s set, but %s names no %s model", strings.Join(set, ", "), testModelsSetting, kind)
			}
		}
	}
	if len(models) == 0 {
		return nil, nil
	}
	return models, nil
}

// resolveTestModelKind reads the settings of the test models of kind under prefix as
// resolveModelGroup reads a model group of that kind, so it refuses each shape a group is refused
// for: its endpoint, an anthropic model's key file and the ceiling are needed, and beside the
// stand-in model (testModelURL) the ceiling alone, every call then going to the stand-in. A kind
// has no row for a field its group would not read (an azure key file, an anthropic api-version or
// scope), so each such field reads as unset.
func resolveTestModelKind(env settingValues, prefix, kind, testModelURL string) (modelGroup, error) {
	whose := testModelsWhose(kind)
	if len(settingsSet(env, prefix)) == 0 {
		return modelGroup{}, fmt.Errorf("%s names an %s model, but no %s* setting is set: %s group is set whole or not at all", testModelsSetting, kind, prefix, whose)
	}
	asGroup := maps.Clone(env)
	for _, field := range []string{"ENDPOINT", "API_VERSION", "SCOPE", "KEY_FILE", "TOKENS_PER_MINUTE"} {
		if _, listed := asGroup[prefix+field]; !listed {
			asGroup[prefix+field] = settingValue{}
		}
	}
	asGroup[prefix+"KIND"] = settingValue{value: kind, set: true}
	asGroup[prefix+"DEPLOYMENT"] = settingValue{value: testModelsSetting, set: true}
	return resolveModelGroup(asGroup, prefix, whose, testModelURL)
}

// deploymentQuotasSetting gives each deployment's tokens-per-minute quota, by the deployment's
// name.
const deploymentQuotasSetting = "DISPATCH_MODEL_DEPLOYMENT_QUOTAS"

// deploymentQuotas reads DISPATCH_MODEL_DEPLOYMENT_QUOTAS: a comma list of name=tokens_per_minute,
// each name once, each quota a positive integer. Unset, it is nil.
func deploymentQuotas(env settingValues) (map[string]int, error) {
	raw := strings.TrimSpace(env.get(deploymentQuotasSetting))
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
			return nil, fmt.Errorf("%s names %q (expected a comma list of name=tokens_per_minute, each a deployment's name and its quota, a positive integer)", deploymentQuotasSetting, entry)
		}
		if _, twice := quotas[deployment]; twice {
			return nil, fmt.Errorf("%s names %s twice", deploymentQuotasSetting, deployment)
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
