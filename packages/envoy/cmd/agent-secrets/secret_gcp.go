// packages/envoy/cmd/agent-secrets/secret_gcp.go
//
// The GCP side of the secret forms: the Secret Manager calls they make, under the Google credential
// of the first source this command finds present, in the order newGCPBackend gives (gcp_source.go):
// --profile, CLOUDSDK_AUTH_ACCESS_TOKEN, GOOGLE_APPLICATION_CREDENTIALS, CLOUDSDK_ACTIVE_CONFIG_NAME
// or CLOUDSDK_CORE_ACCOUNT, the well-known credentials file, the active gcloud account, the
// metadata server - whatever identity that is, a person's or a machine's: Secret Manager's own
// IAM decides what it may read and write. GCP, unlike AWS, needs no wrong-project read check:
// every call already names the project explicitly, so the API itself refuses one in another
// project.
//
// GCP Secret Manager has no soft-delete-with-recovery-window the way AWS does. Instead delete sets
// the secret's own expire_time, which GCP uses as a TTL: the secret (and every version it holds)
// stays readable and restorable - clear expire_time to bring it back - until that moment, and GCP
// deletes it for good once it passes, with no further restore. delete therefore sets expire_time
// to now plus recoveryWindowDays (the same window AWS's delete schedules), and restore clears it;
// the moment delete sets is both "when it is deleted" and "the last moment it can be restored",
// unlike AWS's separate DeletedDate and 7-day minimum recovery window, so secretView.EarliestPurge
// is left unset here (list/show print only the deletion moment).
package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"cloud.google.com/go/iam/apiv1/iampb"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/sjawhar/envoy/internal/broker/policy"
	"github.com/sjawhar/envoy/internal/broker/secrets"
	"github.com/sjawhar/envoy/internal/googleidentity"
)

// checkSettingsKMSKeyName refuses, before any GCP call, settings whose secrets_prefix is not a
// Secret Manager prefix, whose gcp_project does not match the project number secrets_prefix
// itself names (the broker derives gcp_project from secrets_prefix the same way,
// internal/broker/api handlers_secrets.go, secrets.GCPProject), or whose kms_key_name is not a
// global-location CMEK key name (secrets.ValidGCPKeyName: automatic replication, the only
// replication the store serves, takes a global key). This is the GCP counterpart of
// checkSettingsKeyARN: it ties the project a write lands in to the resource-name prefix the
// broker itself derived it from, not to a separately-sent field a spoofed or plain-http broker
// could set on its own to steer a write.
func checkSettingsKMSKeyName(settings Settings) error {
	if err := secrets.ValidGCPPrefix(settings.SecretsPrefix); err != nil {
		return fmt.Errorf("the broker's settings name an invalid secrets_prefix: %w", err)
	}
	if project := secrets.GCPProject(settings.SecretsPrefix); settings.GCPProject != project {
		return fmt.Errorf("the broker's settings name project %s, but its secrets prefix %s is in project %s: the broker's settings are inconsistent", settings.GCPProject, settings.SecretsPrefix, project)
	}
	if err := secrets.ValidGCPKeyName(settings.KMSKeyName); err != nil {
		return fmt.Errorf("the broker's settings name an invalid KMS key name: %w", err)
	}
	return nil
}

// gcpCredentialsFile loads the credentials file at path, of whatever type it names, scoped for
// Secret Manager: the file GOOGLE_APPLICATION_CREDENTIALS names and the well-known credentials
// file. A file it cannot read or parse is an error. Tests replace it.
var gcpCredentialsFile = func(ctx context.Context, path string) (*google.Credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("it is not a JSON credentials file: %w", err)
	}
	return google.CredentialsFromJSONWithType(ctx, data, google.CredentialsType(file.Type), secretmanager.DefaultAuthScopes()...)
}

// gcpMetadataCredentials answers the machine's own service account, as the metadata server names
// it (googleidentity.Probe), and credentials that read its access tokens from that server
// (googleidentity.AccessTokenSource). googleidentity.ErrNoMetadataServer is the absence of a
// metadata server, which newGCPBackend skips. Tests replace it.
var gcpMetadataCredentials = func(ctx context.Context) (*google.Credentials, string, error) {
	email, err := googleidentity.Probe(ctx)
	if err != nil {
		return nil, "", err
	}
	return &google.Credentials{TokenSource: oauth2.ReuseTokenSource(nil, googleidentity.AccessTokenSource())}, email, nil
}

// gcpUserinfo reads the email of the account behind creds from Google's userinfo endpoint, which
// answers only a token that carries an email scope. Only --owner me asks for it
// (gcpBackend.personEmail). Tests replace it so no form makes a network call for it.
var gcpUserinfo = func(ctx context.Context, creds *google.Credentials) (string, error) {
	httpClient := oauth2.NewClient(ctx, creds.TokenSource)
	resp, err := httpClient.Get("https://www.googleapis.com/oauth2/v2/userinfo")
	if err != nil {
		return "", fmt.Errorf("read the Google sign-in's email: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("read the Google sign-in's email: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var userinfo struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&userinfo); err != nil {
		return "", fmt.Errorf("decode the Google sign-in's email: %w", err)
	}
	return userinfo.Email, nil
}

// gcpClientDial builds the Secret Manager client for creds. Tests replace it to dial a local fake
// Secret Manager server instead of the real API. option.WithLogger discards the SDK's own debug
// log (GOOGLE_SDK_GO_LOGGING_LEVEL): at debug level the SDK logs every request as protojson,
// which for AddSecretVersion includes the secret's value, base64-encoded, in Payload.Data (the
// broker's own GCP store takes the same precaution, internal/broker/secrets/gcp.go).
var gcpClientDial = func(ctx context.Context, creds *google.Credentials) (*secretmanager.Client, error) {
	return secretmanager.NewClient(ctx, option.WithCredentials(creds), option.WithLogger(slog.New(slog.DiscardHandler)))
}

// gcloudTokenLife is how long this command takes an access token `gcloud auth print-access-token`
// prints to last, counted from the moment it starts gcloud: gcloud prints the token alone, never
// its expiry, and refreshes a token that would expire within 300 seconds before it prints one
// (_CREDENTIALS_EXPIRY_WINDOW in the Cloud SDK's googlecloudsdk/core/credentials/store.py), so
// every token it prints lasts at least this long. oauth2.ReuseTokenSource asks gcloud again once
// it has passed, and gcloud answers the token it holds until that one nears its own expiry.
const gcloudTokenLife = 5 * time.Minute

// gcloudCommand is the gcloud a source runs: path, the first gcloud on PATH, and configuration,
// the gcloud configuration --profile names, which every call then names with --configuration
// ("" for every other source, whose gcloud runs under the configuration gcloud itself picks).
type gcloudCommand struct {
	path          string
	configuration string
}

// findGcloud is the first gcloud on PATH, under configuration.
func findGcloud(configuration string) (gcloudCommand, error) {
	path, err := exec.LookPath("gcloud")
	if err != nil {
		return gcloudCommand{}, errors.New("no gcloud is on PATH: install the Google Cloud CLI, then sign in once with `gcloud auth login`")
	}
	return gcloudCommand{path: path, configuration: configuration}, nil
}

// gcloudLogins is every login `gcloud auth list` names, and the one it marks active (its
// core/account property, under gcloud's configuration): the first of a gcloud source's three
// calls. A list gcloud cannot make is an error.
func gcloudLogins(ctx context.Context, gcloud gcloudCommand) (accounts []string, active string, err error) {
	out, err := runGcloud(ctx, gcloud, "auth", "list", "--format=json")
	if err != nil {
		return nil, "", fmt.Errorf("`gcloud auth list` failed: %w", err)
	}
	var listed []struct {
		Account string `json:"account"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return nil, "", fmt.Errorf("`gcloud auth list --format=json` answered something other than a list of accounts: %w", err)
	}
	for _, entry := range listed {
		accounts = append(accounts, entry.Account)
		if entry.Status == "ACTIVE" {
			active = entry.Account
		}
	}
	return accounts, active, nil
}

// gcloudSettings are the properties of gcloud's effective configuration a gcloud source reads, as
// `gcloud config list --format=json` shows them, environment overrides included: the second of a
// gcloud source's three calls. Core.Account is the account the configuration names; the auth
// properties are those that change whose token `gcloud auth print-access-token` prints -
// impersonation, an access token file and a credential file override.
type gcloudSettings struct {
	Core struct {
		Account string `json:"account"`
	} `json:"core"`
	Auth struct {
		ImpersonateServiceAccount string `json:"impersonate_service_account"`
		AccessTokenFile           string `json:"access_token_file"`
		CredentialFileOverride    string `json:"credential_file_override"`
	} `json:"auth"`
}

func readGcloudSettings(ctx context.Context, gcloud gcloudCommand) (gcloudSettings, error) {
	var settings gcloudSettings
	out, err := runGcloud(ctx, gcloud, "config", "list", "--format=json")
	if err != nil {
		return settings, fmt.Errorf("`gcloud config list` failed: %w", err)
	}
	if err := json.Unmarshal(out, &settings); err != nil {
		return settings, fmt.Errorf("`gcloud config list --format=json` answered something other than gcloud's properties: %w", err)
	}
	return settings, nil
}

// principalUnknown is the principal a refusal and the "using" line name where the credential
// does not say whose it is: a lent access token, an authorized_user credentials file, and a gcloud
// login whose token comes from an access token file or a credential file override. The CLI asks
// for no email scope to learn it.
const principalUnknown = "principal unknown"

// principal is who account's gcloud token acts as, by settings: account, or account impersonating
// the service account gcloud is set to impersonate, or principalUnknown where an access token
// file or a credential file override replaces account's own token.
func (settings gcloudSettings) principal(account string) string {
	switch {
	case settings.Auth.AccessTokenFile != "" || settings.Auth.CredentialFileOverride != "":
		return principalUnknown
	case settings.Auth.ImpersonateServiceAccount != "":
		return account + " impersonating " + settings.Auth.ImpersonateServiceAccount
	}
	return account
}

// personAccount reports whether email, as Google's userinfo endpoint names an account, is a
// person's: a plain email the broker accepts as a person owner (policy.ValidPersonOwner,
// lowercased), and no service account's (one ending in .gserviceaccount.com, such as a machine's
// own). --owner me takes only a person's (gcpBackend.personEmail).
func personAccount(email string) bool {
	lower := strings.ToLower(email)
	return policy.ValidPersonOwner(lower) && !strings.HasSuffix(lower, ".gserviceaccount.com")
}

// chooseGcloudAccount picks, of accounts, the login gcloud itself would use, whatever kind of
// account it is, as `gcloud auth list` names it (an email, or a principal:// identifier for a
// workload or workforce identity federation login): the one gcloud's own CLOUDSDK_CORE_ACCOUNT
// names, outside --profile; else, where the caller chose a configuration (chosen, by --profile or
// CLOUDSDK_ACTIVE_CONFIG_NAME), the account that configuration names (configured); else the one
// gcloud marks active, else the only one. An account or a configuration the caller chose is the
// caller's choice, so where it names no login gcloud holds the command is refused, naming it and
// each login gcloud holds, rather than run as another login - a configuration that names no
// account at all included, which gcloud's only other login would otherwise stand in for. It also
// refuses no login at all, and several of which none is active, naming each.
func chooseGcloudAccount(gcloud gcloudCommand, chosen string, accounts []string, active, configured string) (string, error) {
	held := "none"
	if len(accounts) > 0 {
		held = strings.Join(accounts, ", ")
	}
	if named := os.Getenv("CLOUDSDK_CORE_ACCOUNT"); named != "" && gcloud.configuration == "" {
		if !slices.Contains(accounts, named) {
			return "", fmt.Errorf("CLOUDSDK_CORE_ACCOUNT names %s, and gcloud holds no login for it (it holds %s): sign in as %s with `gcloud auth login`, or name another login gcloud holds", named, held, named)
		}
		return named, nil
	}
	if chosen != "" {
		switch {
		case configured == "":
			return "", fmt.Errorf("the gcloud configuration %s names no account (gcloud holds logins for %s): sign in to it with `gcloud auth login --configuration=%s`, or name a login gcloud holds with `gcloud config set account ACCOUNT --configuration=%s`", chosen, held, chosen, chosen)
		case !slices.Contains(accounts, configured):
			return "", fmt.Errorf("the gcloud configuration %s names %s, and gcloud holds no login for it (it holds %s): sign in to it with `gcloud auth login --configuration=%s`", chosen, configured, held, chosen)
		}
		return configured, nil
	}
	switch {
	case active != "":
		return active, nil
	case len(accounts) == 1:
		return accounts[0], nil
	case len(accounts) == 0:
		return "", errors.New("gcloud holds no login: sign in once with `gcloud auth login`")
	}
	return "", fmt.Errorf("gcloud holds several logins (%s) and none is active: run the command again with CLOUDSDK_CORE_ACCOUNT set to the one to use", strings.Join(accounts, ", "))
}

// gcloudToken is the oauth2.TokenSource of one gcloud login: each Token runs
// `gcloud auth print-access-token` for its account and takes the token to last gcloudTokenLife.
// No error carries the token: a failure names the account and what gcloud wrote on its standard
// error.
type gcloudToken struct {
	ctx     context.Context
	gcloud  gcloudCommand
	account string
}

// gcloudTokenArgs is the `gcloud auth print-access-token` command line for account, less gcloud:
// the third of a gcloud source's three calls. gcloud prints a command's output through its logger,
// whose file handler keeps a copy in its log file (calliope/display.py's printer writes to
// log.out); the format's private attribute writes the token to the console stream alone
// (core/resource/resource_printer_base.py). gcloud's own impersonation, access token file and
// credential file override apply as its settings say, as they do for gcloud itself, and
// gcloudSettings.principal names what they make the token act as.
func gcloudTokenArgs(account string) []string {
	return []string{"auth", "print-access-token", "--format=value[private](token)", account}
}

// Token runs `gcloud auth print-access-token` for the account under a deadline of its own,
// storeCallDeadline: a refresh runs inside a store call, under that call's context only where
// oauth2 passes one, which it does not, so without its own bound a gcloud that never answered would
// hold the call past its deadline.
func (s gcloudToken) Token() (*oauth2.Token, error) {
	started := time.Now()
	ctx, cancel := withStoreDeadline(s.ctx)
	defer cancel()
	out, err := runGcloud(ctx, s.gcloud, gcloudTokenArgs(s.account)...)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("`gcloud auth print-access-token` did not answer within %s for %s", storeCallDeadlineText(), s.account)
	}
	if err != nil {
		return nil, fmt.Errorf("`gcloud auth print-access-token` failed for %s: %w", s.account, err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, fmt.Errorf("`gcloud auth print-access-token` answered something other than one access token for %s", s.account)
	}
	return &oauth2.Token{AccessToken: token, TokenType: "Bearer", Expiry: started.Add(gcloudTokenLife)}, nil
}

// gcloudEnv is the environment gcloud runs in: this command's, less CLOUDSDK_AUTH_ACCESS_TOKEN,
// which would make gcloud print that token rather than the login's, and, under --profile, less
// CLOUDSDK_CORE_ACCOUNT, which would name another login than the configuration's; with prompts
// off, so gcloud fails rather than waits on one, file logging off (core/disable_file_logging:
// log.py's AddLogsDir adds no file handler), so no run leaves a log file behind, and HTTP logging
// off (core/log_http), whatever this command's environment or gcloud's configuration sets: with
// it on, and its token redaction off, a failed refresh prints its request body, the refresh token
// in it, on the standard error runGcloud quotes in its errors. A variable outranks the
// configuration file, and given twice takes its last value.
func gcloudEnv(gcloud gcloudCommand) []string {
	dropped := []string{"CLOUDSDK_AUTH_ACCESS_TOKEN"}
	if gcloud.configuration != "" {
		dropped = append(dropped, "CLOUDSDK_CORE_ACCOUNT")
	}
	env := slices.DeleteFunc(os.Environ(), func(variable string) bool {
		name, _, _ := strings.Cut(variable, "=")
		return slices.Contains(dropped, name)
	})
	return append(env, "CLOUDSDK_CORE_DISABLE_PROMPTS=1", "CLOUDSDK_CORE_DISABLE_FILE_LOGGING=1", "CLOUDSDK_CORE_LOG_HTTP=false")
}

// gcloudWaitDelay is how long runGcloud waits for a killed gcloud's output to close before it gives
// the call up: a second, the standard error kept and nothing else.
const gcloudWaitDelay = time.Second

// runGcloud runs gcloud with args in gcloudEnv, and --configuration=<its configuration> under
// --profile, and answers what it wrote on its standard output. The flag is explicit rather than
// CLOUDSDK_ACTIVE_CONFIG_NAME because a flag outranks the environment, and because a gcloud
// wrapper may admit a person's login in an agent session only under an explicit --configuration.
// gcloud reads /dev/null, never this command's standard input, which can be a secret's value on
// its way in; what it writes on its standard error is kept only to name a failure. Once ctx ends,
// gcloud is killed and its output waited for no more than gcloudWaitDelay: gcloud is a shell
// script whose Python child can hold the output open after the script is killed.
func runGcloud(ctx context.Context, gcloud gcloudCommand, args ...string) ([]byte, error) {
	if gcloud.configuration != "" {
		args = append(slices.Clone(args), "--configuration="+gcloud.configuration)
	}
	cmd := exec.CommandContext(ctx, gcloud.path, args...)
	cmd.Env = gcloudEnv(gcloud)
	cmd.WaitDelay = gcloudWaitDelay
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if said := strings.TrimSpace(stderr.String()); said != "" {
			return nil, fmt.Errorf("%w: %s", err, said)
		}
		return nil, err
	}
	return out, nil
}

// newGCPBackend signs every form, a read and a write alike, in with the credential of the first
// source present, in this order (gcpSource), and dials the Secret Manager client under it
// (gcpClientDial):
//
//  1. --profile NAME, the gcloud configuration NAME; a configuration gcloudConfigDir does not
//     hold is a usage error naming those it holds;
//  2. CLOUDSDK_AUTH_ACCESS_TOKEN, a lent access token;
//  3. GOOGLE_APPLICATION_CREDENTIALS, the credentials file it names;
//  4. CLOUDSDK_ACTIVE_CONFIG_NAME or CLOUDSDK_CORE_ACCOUNT, the gcloud configuration or login they
//     name;
//  5. the well-known credentials file (wellKnownCredentialsFile);
//  6. the active gcloud account, where gcloud is on PATH and holds a login;
//  7. the metadata server, where one answers (googleidentity.Probe).
//
// A source that is absent is skipped. One that is present but fails - a file it cannot parse, a
// refresh that fails, an account gcloud does not hold - stops the command with its error, and no
// later source is tried; with none present the error names each and why it had nothing. A
// gcloud source runs gcloud with gcloud's own settings for its configuration and account
// (gcloudEnv). It refuses no credential for the identity it is: Secret Manager's IAM decides what
// each may read and write, and a refusal of the store's names the credential (identity). It makes
// no userinfo or tokeninfo call: only --owner me needs an email (personEmail).
func newGCPBackend(ctx context.Context, settings Settings, profile string) (*gcpBackend, error) {
	b := &gcpBackend{settings: settings, profile: profile}
	if err := b.signIn(ctx, profile); err != nil {
		return nil, err
	}
	client, err := gcpClientDial(ctx, b.creds)
	if err != nil {
		return nil, fmt.Errorf("create the Secret Manager client: %w", err)
	}
	b.client = client
	return b, nil
}

// signIn sets b's credential from the first source present (newGCPBackend).
func (b *gcpBackend) signIn(ctx context.Context, profile string) error {
	var absent []string
	skip := func(source gcpSource, why string) { absent = append(absent, source.name()+": "+why) }

	if profile != "" {
		if !slices.Contains(gcloudConfigurations(), profile) {
			return usageErr{fmt.Errorf("--profile names the gcloud configuration %s, which gcloud's configuration directory %s does not hold (it holds %s): name one of those, or create it with `gcloud config configurations create %s`", profile, cmp.Or(gcloudConfigDir(), "(none: CLOUDSDK_CONFIG and HOME are unset)"), gcloudConfigurationList(), profile)}
		}
		gcloud, err := findGcloud(profile)
		if err != nil {
			return fmt.Errorf("--profile names the gcloud configuration %s, but %w", profile, err)
		}
		return b.signInWithGcloud(ctx, sourceProfileFlag, fmt.Sprintf("the gcloud configuration %s (--profile)", profile), gcloud, profile, nil)
	}
	skip(sourceProfileFlag, "not given")

	if token := os.Getenv("CLOUDSDK_AUTH_ACCESS_TOKEN"); token != "" {
		b.source, b.sourcePhrase, b.principal = sourceLentToken, "the access token in CLOUDSDK_AUTH_ACCESS_TOKEN", principalUnknown
		b.creds = &google.Credentials{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token, TokenType: "Bearer"})}
		return nil
	}
	skip(sourceLentToken, "unset")

	if path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); path != "" {
		creds, err := gcpCredentialsFile(ctx, path)
		if err != nil {
			return fmt.Errorf("load the credentials file GOOGLE_APPLICATION_CREDENTIALS names (%s): %w", path, err)
		}
		b.source, b.sourcePhrase, b.principal, b.creds = sourceCredentialsFile, fmt.Sprintf("the credentials file GOOGLE_APPLICATION_CREDENTIALS names (%s)", path), credentialsFilePrincipal(creds), creds
		return nil
	}
	skip(sourceCredentialsFile, "unset")

	if config, account := os.Getenv("CLOUDSDK_ACTIVE_CONFIG_NAME"), os.Getenv("CLOUDSDK_CORE_ACCOUNT"); config != "" || account != "" {
		phrase := fmt.Sprintf("the gcloud configuration %s (CLOUDSDK_ACTIVE_CONFIG_NAME)", config)
		if account != "" {
			phrase = "the gcloud account (CLOUDSDK_CORE_ACCOUNT)"
		}
		gcloud, err := findGcloud("")
		if err != nil {
			return fmt.Errorf("%s chooses a gcloud login, but %w", sourceChosenGcloud.name(), err)
		}
		return b.signInWithGcloud(ctx, sourceChosenGcloud, phrase, gcloud, config, nil)
	}
	skip(sourceChosenGcloud, "both unset")

	if path := wellKnownCredentialsFile(); path == "" {
		skip(sourceWellKnownFile, "no gcloud configuration directory (CLOUDSDK_CONFIG and HOME are unset)")
	} else if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		skip(sourceWellKnownFile, "absent")
	} else {
		creds, err := gcpCredentialsFile(ctx, path)
		if err != nil {
			return fmt.Errorf("load the well-known credentials file %s: %w", path, err)
		}
		b.source, b.sourcePhrase, b.principal, b.creds = sourceWellKnownFile, "the well-known credentials file "+path, credentialsFilePrincipal(creds), creds
		return nil
	}

	if gcloud, err := findGcloud(""); err != nil {
		skip(sourceActiveGcloud, "no gcloud on PATH")
	} else {
		accounts, active, err := gcloudLogins(ctx, gcloud)
		if err != nil {
			return fmt.Errorf("%s: %w", sourceActiveGcloud.name(), err)
		}
		if len(accounts) == 0 {
			skip(sourceActiveGcloud, "gcloud holds no login")
		} else {
			return b.signInWithGcloud(ctx, sourceActiveGcloud, fmt.Sprintf("the active gcloud account (gcloud configuration %s)", activeGcloudConfiguration()), gcloud, "", &gcloudLoginList{accounts, active})
		}
	}

	creds, email, err := gcpMetadataCredentials(ctx)
	switch {
	case errors.Is(err, googleidentity.ErrNoMetadataServer):
		skip(sourceMetadata, err.Error())
	case err != nil:
		return fmt.Errorf("ask the metadata server which service account this machine runs as: %w", err)
	default:
		b.source, b.sourcePhrase, b.principal, b.creds = sourceMetadata, "the metadata server", email, creds
		return nil
	}
	return fmt.Errorf("found no Google credential: this command takes the first of these that is present, and none is: %s. Name a gcloud configuration with --profile, or sign in with `gcloud auth login`", strings.Join(absent, "; "))
}

// gcloudLoginList is what `gcloud auth list` answered, for a source that read it to learn
// whether gcloud holds any login at all.
type gcloudLoginList struct {
	accounts []string
	active   string
}

// signInWithGcloud sets b's credential from gcloud's login, as source names it in phrase, under
// the configuration the caller chose (chosen; "" for gcloud's own): it reads which logins gcloud
// holds (gcloudLogins, unless listed already holds that answer), gcloud's settings
// (readGcloudSettings), which login to use (chooseGcloudAccount), and asks for the first token at
// once, so a login gcloud cannot lend is refused here, before any call is made under it;
// gcloudToken then runs `gcloud auth print-access-token` again as each token nears its end,
// gcloud refreshing the login as it does for itself, through the interface gcloud offers for that,
// never its credential files.
func (b *gcpBackend) signInWithGcloud(ctx context.Context, source gcpSource, phrase string, gcloud gcloudCommand, chosen string, listed *gcloudLoginList) error {
	if listed == nil {
		accounts, active, err := gcloudLogins(ctx, gcloud)
		if err != nil {
			return fmt.Errorf("%s: %w", phrase, err)
		}
		listed = &gcloudLoginList{accounts, active}
	}
	settings, err := readGcloudSettings(ctx, gcloud)
	if err != nil {
		return fmt.Errorf("%s: %w", phrase, err)
	}
	account, err := chooseGcloudAccount(gcloud, chosen, listed.accounts, listed.active, settings.Core.Account)
	if err != nil {
		return fmt.Errorf("%s: %w", phrase, err)
	}
	tokens := gcloudToken{ctx: ctx, gcloud: gcloud, account: account}
	first, err := tokens.Token()
	if err != nil {
		return fmt.Errorf("%s: %w", phrase, err)
	}
	b.source, b.sourcePhrase, b.principal, b.gcloudAccount = source, phrase, settings.principal(account), account
	b.creds = &google.Credentials{TokenSource: oauth2.ReuseTokenSource(first, tokens)}
	return nil
}

// credentialsFilePrincipal is who a credentials file's credentials act as, by what the file says,
// never by a call: a key file's client_email, the service account an impersonating file names in
// its service_account_impersonation_url (an external_account workload identity federation
// credential, such as a dev box's, or an impersonated_service_account file), else
// principalUnknown (an authorized_user file, a federation credential that impersonates no one).
func credentialsFilePrincipal(creds *google.Credentials) string {
	var parsed struct {
		ClientEmail      string `json:"client_email"`
		ImpersonationURL string `json:"service_account_impersonation_url"`
	}
	if err := json.Unmarshal(creds.JSON, &parsed); err != nil {
		return principalUnknown
	}
	if parsed.ClientEmail != "" {
		return parsed.ClientEmail
	}
	if _, rest, ok := strings.Cut(parsed.ImpersonationURL, "/serviceAccounts/"); ok {
		if account, _, _ := strings.Cut(rest, ":"); account != "" {
			return account
		}
	}
	return principalUnknown
}

// gcpBackend is the secretBackend for store "gcp-secret-manager": GCP Secret Manager under the
// credential of the first Google credential source present (newGCPBackend), pinned to
// settings.GCPProject.
type gcpBackend struct {
	settings Settings
	client   *secretmanager.Client
	creds    *google.Credentials
	// source is the source creds came from, sourcePhrase names it, and principal is who creds act
	// as, or principalUnknown (identity).
	source       gcpSource
	sourcePhrase string
	principal    string
	// profile is the --profile the form was given, "" for none, and gcloudAccount the gcloud login
	// a gcloud source signed in with: a command the form prints for running again carries them
	// (agentSecrets, gcloudPin), so it signs in as this one did.
	profile       string
	gcloudAccount string
}

// id is the Secret Manager resource name of the secret whose name under the prefix is slug.
func (b *gcpBackend) id(slug string) string { return b.settings.SecretsPrefix + slug }

func (b *gcpBackend) parent() string { return "projects/" + b.settings.GCPProject }

// secretID is id(slug) with the parent's "projects/<project>/secrets/" stripped, the bare id
// CreateSecretRequest.SecretId takes.
func (b *gcpBackend) secretID(slug string) string {
	return strings.TrimPrefix(b.id(slug), b.parent()+"/secrets/")
}

func (b *gcpBackend) displayName(id string) string { return displayName(b.settings.SecretsPrefix, id) }

// requireAccount is a no-op for GCP, unlike AWS's: every call this backend makes already names
// projects/<gcp_project>/... explicitly in the resource, so a wrong-project sign-in gets a real
// PermissionDenied or NotFound from the API itself rather than silently reading another
// project's secrets - AWS's ListSecrets takes no project parameter at all, which is the hazard
// requireAccount guards there. A sign-in's own project (its quota project, set from a credentials
// file's project_id, which a person's sign-in does not carry) is in any case not reliably
// comparable with the broker's gcp_project without a Resource Manager call this CLI does not
// make.
func (b *gcpBackend) requireAccount(context.Context) error { return nil }

// personEmail answers, for --owner me, the lowercased email of the person this sign-in is, as
// Google's userinfo endpoint (gcpUserinfo) names the account behind its token. It is the one
// userinfo call any form makes. It refuses a token userinfo does not answer, which is any token
// without an email scope (an access token minted for cloud-platform alone, as a lent token or a
// machine's commonly is), and an account that is no person's (personAccount): a service account's
// email names no person.
func (b *gcpBackend) personEmail(ctx context.Context) (string, error) {
	email, err := gcpUserinfo(ctx, b.creds)
	switch {
	case err != nil:
		return "", fmt.Errorf("Google's userinfo endpoint does not name the account behind %s, as it names none for a token without an email scope: %w", b.sourcePhrase, err)
	case email == "":
		return "", fmt.Errorf("Google's userinfo endpoint names no email for %s", b.sourcePhrase)
	case !personAccount(email):
		return "", fmt.Errorf("Google's userinfo endpoint names %s as %s, which is no person's email", b.sourcePhrase, email)
	}
	return strings.ToLower(email), nil
}

// identity names the credential this backend signs in with, as `<source>: <principal>`: the
// source newGCPBackend took it from, and who it acts as, or that the principal is unknown.
func (b *gcpBackend) identity() string { return b.sourcePhrase + ": " + b.principal }

// chooseAnother is how a refusal of the store's says to sign in with another credential: the
// flag, and the gcloud configurations it can name.
func (b *gcpBackend) chooseAnother() string {
	return "--profile <gcloud configuration> (configurations here: " + gcloudConfigurationList() + ")"
}

// signInPrefix is the shell assignments a command line a form prints for running again
// (pipeCommand) needs to sign in as this one did, each shell-quoted, where a variable chose the
// source and may have been set for this one command: GOOGLE_APPLICATION_CREDENTIALS for the file
// it names, and CLOUDSDK_ACTIVE_CONFIG_NAME and CLOUDSDK_CORE_ACCOUNT, as set, for the gcloud login
// they chose; "" for every other source. It never writes CLOUDSDK_AUTH_ACCESS_TOKEN: a printed
// command carries no token. --profile is pipeCommand's to write.
func (b *gcpBackend) signInPrefix() string {
	prefix := ""
	switch b.source {
	case sourceCredentialsFile:
		prefix = "GOOGLE_APPLICATION_CREDENTIALS=" + shellWord(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")) + " "
	case sourceChosenGcloud:
		for _, name := range []string{"CLOUDSDK_ACTIVE_CONFIG_NAME", "CLOUDSDK_CORE_ACCOUNT"} {
			if value := os.Getenv(name); value != "" {
				prefix += name + "=" + shellWord(value) + " "
			}
		}
	}
	return prefix
}

// hasCurrentVersion reports whether id is served while versions/latest is ENABLED.
func (b *gcpBackend) hasCurrentVersion(ctx context.Context, id string) (bool, error) {
	version, err := b.client.GetSecretVersion(ctx, &secretmanagerpb.GetSecretVersionRequest{Name: id + "/versions/latest"})
	if status.Code(err) == codes.NotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return version.GetState() == secretmanagerpb.SecretVersion_ENABLED, nil
}

// viewOf is secret as a secretView, less its versions, which only show reads.
func (b *gcpBackend) viewOf(ctx context.Context, secret *secretmanagerpb.Secret) (secretView, error) {
	hasValue, err := b.hasCurrentVersion(ctx, secret.GetName())
	if err != nil {
		return secretView{}, err
	}
	view := secretView{
		Name: b.displayName(secret.GetName()), SecretName: secret.GetName(),
		HasValue: hasValue,
	}.withTags(secret.GetAnnotations())
	if t := secret.GetCreateTime(); t != nil {
		created := t.AsTime()
		view.Created = &created
	}
	if t := secret.GetExpireTime(); t != nil {
		deleted := t.AsTime()
		view.Deleted = &deleted
	}
	return view, nil
}

func (b *gcpBackend) list(ctx context.Context) ([]secretView, error) {
	views := []secretView{}
	it := b.client.ListSecrets(ctx, &secretmanagerpb.ListSecretsRequest{Parent: b.parent()})
	for {
		secret, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("list the agent secrets: %w", err)
		}
		if !strings.HasPrefix(secret.GetName(), b.settings.SecretsPrefix) {
			continue // the parent held more than the namespace
		}
		view, err := b.viewOf(ctx, secret)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", secret.GetName(), err)
		}
		views = append(views, view)
	}
	return views, nil
}

// versionsOf maps each of id's version numbers to its one-element state, the GCP counterpart of
// AWS's version-to-stages map.
func (b *gcpBackend) versionsOf(ctx context.Context, id string) (map[string][]string, error) {
	versions := map[string][]string{}
	it := b.client.ListSecretVersions(ctx, &secretmanagerpb.ListSecretVersionsRequest{Parent: id})
	for {
		v, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}
		number := v.GetName()[strings.LastIndex(v.GetName(), "/")+1:]
		versions[number] = []string{v.GetState().String()}
	}
	return versions, nil
}

func (b *gcpBackend) show(ctx context.Context, slug string) (secretView, error) {
	id := b.id(slug)
	secret, err := b.client.GetSecret(ctx, &secretmanagerpb.GetSecretRequest{Name: id})
	if err != nil {
		return secretView{}, err
	}
	view, err := b.viewOf(ctx, secret)
	if err != nil {
		return secretView{}, err
	}
	versions, err := b.versionsOf(ctx, id)
	if err != nil {
		return secretView{}, err
	}
	view.Versions = versions
	return view, nil
}

// createLabel is the label create sets on the secret it makes, to a value of its own
// (newCreateToken), so that a secret it finds after a CreateSecret whose answer was lost is its own
// exactly when that label holds its value: one that held the name before, or that another create of
// the name made in between, carries no label or another's. It is a label and not an annotation,
// since the policy's tags are the annotations. The broker reads it for one thing only: a secret
// that carries it and holds no version is a create waiting for its value (secrets.Listing.Created
// and policy.ReasonPendingValue). It stays on the secret once its value is added, since removing it
// would cost a create secretmanager.secrets.update.
const createLabel = secrets.CreateLabel

// newCreateToken is a value for createLabel: 128 random bits as 32 lowercase hex digits, which a
// Secret Manager label value admits. crypto/rand.Read never returns an error.
func newCreateToken() string {
	var token [16]byte
	_, _ = rand.Read(token[:])
	return hex.EncodeToString(token[:])
}

// prepareCreate builds the state, including the ownership label, before the form installs its
// signal cleanup. begin then makes the secret, so a signal during CreateSecret waits for its
// answer and cleans up as it does during the later store step.
func (b *gcpBackend) prepareCreate(slug string, tags map[string]string) newSecret {
	return &gcpNewSecret{b: b, name: b.id(slug), secretID: b.secretID(slug), display: b.displayName(b.id(slug)), tags: tags, token: newCreateToken()}
}

// addPermission is the permission store needs on the secret begin makes.
const addPermission = "secretmanager.versions.add"

// begin makes the secret, empty and labelled with this create's token, under storeCallDeadline,
// then asks IAM whether this sign-in may add its value (addPermission), so a sign-in that may
// create but not add is told before it is asked for the value: begin then discards the empty
// secret through discard and answers that refusal. Where IAM cannot be asked, begin goes on, and
// store's add is the check. A CreateSecret whose answer leaves open whether the store made the
// secret is settled by settleLostCreate.
func (n *gcpNewSecret) begin(ctx context.Context) (discardOutcome, error) {
	b := n.b
	callCtx, cancel := withStoreDeadline(ctx)
	_, err := b.client.CreateSecret(callCtx, &secretmanagerpb.CreateSecretRequest{
		Parent:   b.parent(),
		SecretId: n.secretID,
		Secret: &secretmanagerpb.Secret{
			Replication: &secretmanagerpb.Replication{
				Replication: &secretmanagerpb.Replication_Automatic_{
					Automatic: &secretmanagerpb.Replication_Automatic{
						CustomerManagedEncryption: &secretmanagerpb.CustomerManagedEncryption{
							KmsKeyName: b.settings.KMSKeyName,
						},
					},
				},
			},
			Annotations: n.tags,
			Labels:      map[string]string{createLabel: n.token},
		},
	})
	cancel()
	if err != nil {
		if gcpDefiniteRefusal(err) {
			return madeNothing, err
		}
		outcome, left := n.settleLostCreate(ctx)
		return outcome, fmt.Errorf("%w; %s", err, left)
	}
	if b.mayNotAdd(ctx, n.name) {
		refusal := fmt.Errorf("%s may create %s but not add its value: IAM grants it no %s on the secret, so no value was asked for", b.identity(), n.name, addPermission)
		outcome, left := n.discard(ctx)
		if left != nil {
			return outcome, fmt.Errorf("%w; %w", refusal, left)
		}
		return outcome, refusal
	}
	return madeNothing, nil
}

// mayNotAdd reports whether IAM answers, under storeCallDeadline, that this sign-in does not hold
// addPermission on the secret named id. A failed ask answers false: the add is then the check.
func (b *gcpBackend) mayNotAdd(ctx context.Context, id string) bool {
	ctx, cancel := withStoreDeadline(ctx)
	defer cancel()
	held, err := b.client.TestIamPermissions(ctx, &iampb.TestIamPermissionsRequest{Resource: id, Permissions: []string{addPermission}})
	return err == nil && !slices.Contains(held.GetPermissions(), addPermission)
}

// settleLostCreate answers what a CreateSecret whose answer leaves open whether the store acted
// left in the store, and settles it. It reads the secret back: none shown means the store did not
// show it then, though a create it has not finished may still land; one whose createLabel is not
// this create's token is another write's - one that held the name before, or another create's -
// and is left as it is; one carrying the token goes through discard, which deletes it only where it
// holds no version. A read that fails names the token and the gcloud commands that show a secret's
// labels and clear it. Each call runs under storeCallDeadline.
func (n *gcpNewSecret) settleLostCreate(ctx context.Context) (discardOutcome, string) {
	lost := "the store's answer leaves open whether it made " + n.name
	getCtx, cancel := withStoreDeadline(ctx)
	secret, err := n.b.client.GetSecret(getCtx, &secretmanagerpb.GetSecretRequest{Name: n.name})
	cancel()
	switch {
	case status.Code(err) == codes.NotFound:
		return keptUnknown, fmt.Sprintf("%s, and reading it back did not find it then, but a create the store has not finished may still land: `%s` shows whether it is there, and one whose %s label is %s, with `versions: none`, is this create's empty secret, which %s clears",
			lost, n.agentSecrets("secret", "show", n.display), createLabel, n.token, n.clearCommand())
	case err != nil:
		return keptUnknown, fmt.Sprintf("%s, and reading it back failed, so whether this create made it is unknown: %s shows its labels, and one whose %s label is %s is this create's, and with no version it is an empty secret, which %s clears. Reading it failed: %v",
			lost, n.b.gcloud("secrets", "describe", n.secretID, "--project", n.b.settings.GCPProject), createLabel, n.token, n.clearCommand(), err)
	case secret.GetLabels()[createLabel] != n.token:
		return keptOthers, lost + "; reading it back finds a secret this create did not make (its " + createLabel + " label is not this create's), so it is left as it is"
	}
	outcome, left := n.discardRead(ctx, secret, lost+"; reading it back finds it made by this create")
	if left != nil {
		return outcome, left.Error()
	}
	return outcome, lost + "; reading it back finds it, made by this create and empty, so it is deleted again and nothing stays"
}

// countVersions is how many versions the secret named id holds, listed under storeCallDeadline.
func (b *gcpBackend) countVersions(ctx context.Context, id string) (int, error) {
	ctx, cancel := withStoreDeadline(ctx)
	defer cancel()
	versions, err := b.versionsOf(ctx, id)
	return len(versions), err
}

// deleteOutright deletes the observed secret outright, under storeCallDeadline, conditional on
// the etag read before listing its versions, so a replacement cannot be deleted by name alone.
func (b *gcpBackend) deleteOutright(ctx context.Context, id, etag string) error {
	ctx, cancel := withStoreDeadline(ctx)
	defer cancel()
	return b.client.DeleteSecret(ctx, &secretmanagerpb.DeleteSecretRequest{Name: id, Etag: etag})
}

// gcloud is a gcloud command line for printing, in backticks, pinned to the sign-in this command
// used where gcloud can be told it (gcloudPin), and followed by that sign-in's name, so a person
// runs it as the identity that made what it settles.
func (b *gcpBackend) gcloud(args ...string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellWord(arg)
	}
	return fmt.Sprintf("`%sgcloud %s` (as %s)", b.gcloudPin(), strings.Join(quoted, " "), b.identity())
}

// gcloudPin is the shell assignments that make gcloud sign in as this command did, each
// shell-quoted, by the source the credential came from: CLOUDSDK_ACTIVE_CONFIG_NAME for the
// configuration --profile named, the variables that chose a gcloud configuration or login as they
// were set, CLOUDSDK_CORE_ACCOUNT for the active gcloud account's login, and
// CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE for the key file GOOGLE_APPLICATION_CREDENTIALS named;
// "" for a token CLOUDSDK_AUTH_ACCESS_TOKEN lent, which gcloud reads from the same environment,
// and for credentials gcloud cannot be pointed at (the metadata server's, or the well-known
// file's), which the name after the command (gcloud) says to use.
func (b *gcpBackend) gcloudPin() string {
	switch b.source {
	case sourceProfileFlag:
		return "CLOUDSDK_ACTIVE_CONFIG_NAME=" + shellWord(b.profile) + " "
	case sourceChosenGcloud:
		return b.signInPrefix()
	case sourceActiveGcloud:
		return "CLOUDSDK_CORE_ACCOUNT=" + shellWord(b.gcloudAccount) + " "
	case sourceCredentialsFile:
		return "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE=" + shellWord(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")) + " "
	}
	return ""
}

// agentSecrets is an agent-secrets command line for printing, with the sign-in prefix and the
// --profile that make it sign in as this command did (signInPrefix, pipeCommand).
func (b *gcpBackend) agentSecrets(words ...string) string {
	return pipeCommand(b.signInPrefix(), b.profile, words...)
}

// gcpNewSecret is the empty secret create made: name its whole resource name, secretID the id
// under the project, display the NAME a session asks for it as, token its createLabel value.
// mayHoldValue and deleted are what unsettled reads while a call of begin, store or discard may be
// in flight: mayHoldValue is set from the moment store begins the add until a refusal the store
// answered before acting on it, and from the moment discard finds a version; deleted once the
// delete is done. An add whose answer was lost leaves mayHoldValue set, and discard then never
// deletes the secret: the add may still land.
type gcpNewSecret struct {
	b                       *gcpBackend
	name, secretID, display string
	token                   string
	tags                    map[string]string
	mayHoldValue, deleted   atomic.Bool
}

// store adds value as the secret's first version, under storeCallDeadline.
func (n *gcpNewSecret) store(ctx context.Context, value string) error {
	n.mayHoldValue.Store(true)
	ctx, cancel := withStoreDeadline(ctx)
	defer cancel()
	err := n.b.addVersion(ctx, n.name, value)
	if err != nil && gcpDefiniteRefusal(err) {
		n.mayHoldValue.Store(false)
	}
	return err
}

// discard reads the secret back and goes on through discardRead, which deletes it only where it is
// this create's, empty, unchanged, and no add of this create's may still land. A secret already gone
// is deleted, for this create.
func (n *gcpNewSecret) discard(ctx context.Context) (discardOutcome, error) {
	getCtx, cancel := withStoreDeadline(ctx)
	secret, err := n.b.client.GetSecret(getCtx, &secretmanagerpb.GetSecretRequest{Name: n.name})
	cancel()
	switch {
	case status.Code(err) == codes.NotFound:
		n.deleted.Store(true)
		return deletedEmpty, nil
	case err != nil:
		return keptUnknown, fmt.Errorf("reading %s before cleanup failed, so it is left as it is: %s. Reading it failed: %w", n.name, n.unsettled(), err)
	}
	return n.discardRead(ctx, secret, "")
}

// discardRead deletes secret, as discard read it, only where it carries this create's token, holds
// no version, no add of this create's may still land (mayHoldValue), and its etag is unchanged at
// the delete; otherwise it leaves it, saying why. Even when this form never added a value, another
// writer may have filled or replaced the secret while its prompt was open. why, when set, opens
// each refusal. Each call runs under storeCallDeadline. The one case it cannot see: a version
// another writer adds between the list and the delete, which the delete then takes with the secret.
func (n *gcpNewSecret) discardRead(ctx context.Context, secret *secretmanagerpb.Secret, why string) (discardOutcome, error) {
	if why != "" {
		why += "; "
	}
	if secret.GetLabels()[createLabel] != n.token {
		return keptOthers, fmt.Errorf("%sanother writer now holds %s (its %s label is not this create's), so it is left as it is: `%s` shows it", why, n.name, createLabel, n.agentSecrets("secret", "show", n.display))
	}
	addLost := n.mayHoldValue.Load()
	versions, err := n.b.countVersions(ctx, n.name)
	switch {
	case err != nil:
		return keptUnknown, fmt.Errorf("%slisting the secret's versions failed, so it stays: whether %s holds the value is unknown. `%s` settles it, under a sign-in that may list its versions: `versions: none` is an empty secret, which %s clears, and a version is the value stored. Listing the versions failed: %w",
			why, n.name, n.agentSecrets("secret", "show", n.display), n.clearCommand(), err)
	case versions > 0:
		n.mayHoldValue.Store(true)
		return keptMayHoldValue, fmt.Errorf("%s%s holds %d version(s) now, so it stays: the value may have been stored by this create or another writer. Check it with `%s`, and run `%s` to store it again",
			why, n.name, versions, n.agentSecrets("secret", "show", n.display), n.agentSecrets("secret", "set", n.display))
	case addLost:
		return keptMayHoldValue, fmt.Errorf("%sthe store's answer to the add leaves open whether it stored the value, and %s showed no version when listed, but a late add may still land, so it stays: `%s` shows whether the value is there, `versions: none` being an empty secret, which %s clears",
			why, n.name, n.agentSecrets("secret", "show", n.display), n.clearCommand())
	}
	if err := n.b.deleteOutright(ctx, n.name, secret.GetEtag()); err != nil {
		if status.Code(err) == codes.FailedPrecondition || status.Code(err) == codes.Aborted {
			return keptOthers, fmt.Errorf("%s%s changed before cleanup could delete it, so it is left as it is: `%s` shows it. Deleting it failed: %w", why, n.name, n.agentSecrets("secret", "show", n.display), err)
		}
		return keptEmpty, fmt.Errorf("%san empty secret named %s stays in the store: clear it with %s. Deleting it failed: %w", why, n.name, n.clearCommand(), err)
	}
	n.deleted.Store(true)
	return deletedEmpty, nil
}

// unsettled says what the store may hold should the form end now: nothing once the empty secret
// is deleted; the secret, empty or holding the value, once the add has begun and nothing has shown
// it empty, which `secret show` tells apart; else the empty secret, or another writer's secret of
// that name. Each names the command that clears the empty secret.
func (n *gcpNewSecret) unsettled() string {
	switch {
	case n.deleted.Load():
		return fmt.Sprintf("the empty secret %s is deleted, so nothing stays in the store", n.name)
	case n.mayHoldValue.Load():
		return fmt.Sprintf("%s may stay in the store, empty or holding the value: `%s` says which, `versions: none` being an empty secret, which %s clears, and a version the value stored",
			n.name, n.agentSecrets("secret", "show", n.display), n.clearCommand())
	}
	return fmt.Sprintf("the empty secret %s may stay in the store, or another writer may now hold that name. %s shows its labels: only one whose %s label is %s is this create's, and only with `versions: none` from `%s` is it empty and safe to clear with %s",
		n.name, n.b.gcloud("secrets", "describe", n.secretID, "--project", n.b.settings.GCPProject), createLabel, n.token, n.agentSecrets("secret", "show", n.display), n.clearCommand())
}

// clearCommand is the gcloud command that deletes the secret outright, as gcloud prints it.
func (n *gcpNewSecret) clearCommand() string {
	return n.b.gcloud("secrets", "delete", n.secretID, "--project", n.b.settings.GCPProject)
}

// agentSecrets is an agent-secrets command line signing in as this create did.
func (n *gcpNewSecret) agentSecrets(words ...string) string { return n.b.agentSecrets(words...) }

// madeBeforeValue is true: begin makes the empty secret, which waits in the store for its value.
func (*gcpNewSecret) madeBeforeValue() bool { return true }

// gcpDefiniteRefusal reports whether err is an answer Secret Manager gives before it acts on a
// call: the request refused as malformed, unauthorized, out of quota or against the secret's state.
// Every other answer - a lost one (Unknown, DeadlineExceeded, Canceled, Unavailable), a fault
// (Internal, DataLoss, Aborted), an error with no gRPC status at all - may follow a call the store
// carried out. It reads a gRPC status alone, so it is GCP's: an AWS error has none and answers
// false here whatever it says (awsDefiniteRefusal reads those).
func gcpDefiniteRefusal(err error) bool {
	switch status.Code(err) {
	case codes.InvalidArgument, codes.NotFound, codes.AlreadyExists, codes.PermissionDenied, codes.Unauthenticated,
		codes.ResourceExhausted, codes.FailedPrecondition, codes.OutOfRange, codes.Unimplemented:
		return true
	}
	return false
}

func (b *gcpBackend) set(ctx context.Context, slug, value string) error {
	return b.addVersion(ctx, b.id(slug), value)
}

func (b *gcpBackend) addVersion(ctx context.Context, parent, value string) error {
	_, err := b.client.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
		Parent:  parent,
		Payload: &secretmanagerpb.SecretPayload{Data: []byte(value)},
	})
	return err
}

func (b *gcpBackend) currentTags(ctx context.Context, slug string) (heldTags, error) {
	secret, err := b.client.GetSecret(ctx, &secretmanagerpb.GetSecretRequest{Name: b.id(slug)})
	if err != nil {
		return heldTags{}, err
	}
	return heldTags{tags: secret.GetAnnotations(), etag: secret.GetEtag()}, nil
}

// readValue reads the secret's value with AccessSecretVersion on versions/latest, the version the
// broker serves, and refuses one that is empty or not valid UTF-8, as the broker's own read does
// (secrets.GCP.Read). Its error never holds the value's bytes.
func (b *gcpBackend) readValue(ctx context.Context, slug string) (string, error) {
	latest := b.id(slug) + "/versions/latest"
	out, err := b.client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{Name: latest})
	if err != nil {
		return "", err
	}
	data := out.GetPayload().GetData()
	if len(data) == 0 || !utf8.Valid(data) {
		return "", errors.New("its value is not a non-empty UTF-8 string")
	}
	return string(data), nil
}

// valueReadPermission is what AccessSecretVersion needs of a person's sign-in. The roles people
// hold on the broker's secrets commonly lack it: they add versions and change annotations, and
// read none.
func (b *gcpBackend) valueReadPermission() string {
	return "secretmanager.versions.access on the secret"
}

// setTags writes tags into the annotations held holds, and removeTags deletes keys from them, each
// in one UpdateSecret that keeps every annotation it does not name. An UpdateSecret whose mask names
// "annotations" replaces the whole map, so each sends held's annotations, changed, with held's
// etag: the write lands only on the annotations the retag checked, and a write another writer made
// since held was read refuses it with FAILED_PRECONDITION (errTagsChanged), naming the change,
// rather than being merged into annotations no one checked.
func (b *gcpBackend) setTags(ctx context.Context, slug string, held heldTags, tags map[string]string) error {
	return b.updateAnnotations(ctx, slug, held, func(annotations map[string]string) { maps.Copy(annotations, tags) })
}

func (b *gcpBackend) removeTags(ctx context.Context, slug string, held heldTags, keys []string) error {
	return b.updateAnnotations(ctx, slug, held, func(annotations map[string]string) {
		for _, key := range keys {
			delete(annotations, key)
		}
	})
}

// updateAnnotations sends held's annotations, as change leaves a copy of them, with held's etag.
func (b *gcpBackend) updateAnnotations(ctx context.Context, slug string, held heldTags, change func(map[string]string)) error {
	annotations := maps.Clone(held.tags)
	if annotations == nil {
		annotations = map[string]string{}
	}
	change(annotations)
	_, err := b.client.UpdateSecret(ctx, &secretmanagerpb.UpdateSecretRequest{
		Secret: &secretmanagerpb.Secret{
			Name:        b.id(slug),
			Annotations: annotations,
			Etag:        held.etag,
		},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"annotations"}},
	})
	if status.Code(err) == codes.FailedPrecondition {
		return fmt.Errorf("%w, so the store refused this write: %w", errTagsChanged, err)
	}
	return err
}

func (b *gcpBackend) delete(ctx context.Context, slug string) (*time.Time, error) {
	expire := time.Now().UTC().Add(time.Duration(recoveryWindowDays) * 24 * time.Hour)
	_, err := b.client.UpdateSecret(ctx, &secretmanagerpb.UpdateSecretRequest{
		Secret: &secretmanagerpb.Secret{
			Name:       b.id(slug),
			Expiration: &secretmanagerpb.Secret_ExpireTime{ExpireTime: timestamppb.New(expire)},
		},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"expire_time"}},
	})
	if err != nil {
		return nil, err
	}
	return &expire, nil
}

// restore clears expire_time: the field mask names it with no value in the body, which Secret
// Manager reads as unsetting it.
func (b *gcpBackend) restore(ctx context.Context, slug string) error {
	_, err := b.client.UpdateSecret(ctx, &secretmanagerpb.UpdateSecretRequest{
		Secret:     &secretmanagerpb.Secret{Name: b.id(slug)},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"expire_time"}},
	})
	return err
}

func (b *gcpBackend) isAccessDenied(err error) bool {
	return status.Code(err) == codes.PermissionDenied
}

// close releases the Secret Manager client's underlying gRPC connection.
func (b *gcpBackend) close() error { return b.client.Close() }

var _ secretBackend = (*gcpBackend)(nil)
