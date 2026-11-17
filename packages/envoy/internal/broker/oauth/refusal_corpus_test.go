package oauth_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"authelia.com/provider/oauth2"
	"github.com/sjawhar/envoy/internal/broker/oauth"
)

// The refusal texts the TypeScript broker client prints whole (packages/broker-client,
// isKnownBrokerRefusal), as this broker's own OAuth library and its own Refusal write them on the
// wire. testdata/refusal-descriptions.json holds them, and broker-client's refusal-corpus test
// reads that file, so neither the library's sentence ahead of a hint nor a hint's words can change
// without a test that names it. Each hint's format string must also stand in the broker's source,
// so a sentence changed there fails here.
type corpusRefusal struct {
	base   *oauth2.RFC6749Error
	format string
	args   []any
	source string
}

const (
	corpusPerson  = "ada@example.com"
	corpusProject = "DSP"
	corpusSession = "ses_corpus"
)

var corpusRefusals = []corpusRefusal{
	{oauth2.ErrInvalidGrant, "%s revoked this machine's sign-in, so the key this proof was signed with trades nothing. Sign in again with `agent-secrets login`.", []any{corpusPerson}, "exchange.go"},
	{oauth2.ErrInvalidGrant, "%s's sign-in on this box key was made on another machine, and a sign-in belongs to the machine it was made on. Sign in again with `agent-secrets login`.", []any{corpusPerson}, "exchange.go"},
	{oauth2.ErrInvalidGrant, "The machine login the subject token belongs to has ended.", nil, "exchange.go"},
	{oauth2.ErrInvalidGrant, "The machine login has ended.", nil, "provider.go"},
	{oauth2.ErrInvalidGrant, "Another live machine login holds the key this proof was signed with. A machine makes a key of its own.", nil, "exchange.go"},
	{oauth2.ErrInvalidGrant, "Another live machine login holds the key this proof was signed with. A sign-in makes a box key of its own.", nil, "exchange.go"},
	{oauth2.ErrInvalidRequest, "%s is a restricted member, whose token names its projects: set AGENT_SECRETS_PROJECTS to the projects this session works on.", []any{corpusPerson}, "exchange.go"},
	{oauth2.ErrInvalidRequest, "The token names a project its person cannot see: %s.", []any{corpusProject}, "exchange.go"},
	{oauth2.ErrInvalidRequest, "The subject token was refused: its entry names the project %s, which its person %s cannot see.", []any{corpusProject, corpusPerson}, "exchange.go"},
	{oauth2.ErrInvalidRequest, "The subject token was refused: its entry names no project, and its person %s is a restricted member, who sees only the projects that name them.", []any{corpusPerson}, "exchange.go"},
	{oauth2.ErrTemporarilyUnavailable, "Dispatch could not be asked whether the token's person sees the projects it names; try again.", nil, "exchange.go"},
	{oauth2.ErrInvalidGrant, "The session name '%s' belongs to another identity: one the broker's configuration fixes it for, or one that has written under it within the last %d days. Ask under a name of your own.", []any{corpusSession, 30}, "exchange.go"},
	{oauth2.ErrInvalidGrant, "No registration says this pod may write under the session name '%s'. A pod writes under a session name its deployment registered for it.", []any{corpusSession}, "exchange.go"},
	{oauth2.ErrInvalidRequest, "The subject token was refused: %s.", []any{"its audience is not this broker's"}, "exchange.go"},
}

// The broker's own refusal, as its token endpoint writes its rate limit.
const corpusRateLimit = "too many token requests; try again later"

func corpusDescriptions(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, refusal := range corpusRefusals {
		hint := refusal.format
		if len(refusal.args) > 0 {
			hint = fmt.Sprintf(refusal.format, refusal.args...)
		}
		out = append(out, refusal.base.WithHint(hint).GetDescription())
	}
	recorder := httptest.NewRecorder()
	oauth.WriteRefusal(recorder, &oauth.Refusal{Status: 429, Code: "RATE_LIMITED", Message: corpusRateLimit})
	var body struct {
		Description string `json:"error_description"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("the rate limit's answer is not JSON: %v", err)
	}
	return append(out, body.Description)
}

func TestTheRefusalCorpusIsWhatTheBrokerWrites(t *testing.T) {
	path := filepath.Join("testdata", "refusal-descriptions.json")
	want := corpusDescriptions(t)
	if os.Getenv("UPDATE_REFUSAL_CORPUS") == "1" {
		encoded, err := json.MarshalIndent(want, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var got []string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s is not a JSON list of strings: %v", path, err)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s is not what this broker writes; run UPDATE_REFUSAL_CORPUS=1 go test -run %s ./internal/broker/oauth and check broker-client's list.\nwant:\n%s\ngot:\n%s", path, t.Name(), strings.Join(want, "\n"), strings.Join(got, "\n"))
	}
}

func TestEachCorpusHintStandsInTheBrokersSource(t *testing.T) {
	sources := map[string]string{}
	for _, refusal := range corpusRefusals {
		text, ok := sources[refusal.source]
		if !ok {
			raw, err := os.ReadFile(refusal.source)
			if err != nil {
				t.Fatalf("read %s: %v", refusal.source, err)
			}
			text = string(raw)
			sources[refusal.source] = text
		}
		// The format as Go source writes it: a backquote inside a double-quoted string is itself.
		if !strings.Contains(text, refusal.format) {
			t.Errorf("%s holds no hint %q: the broker's sentence changed, so the corpus and broker-client's list must too", refusal.source, refusal.format)
		}
	}
	raw, err := os.ReadFile(filepath.Join("..", "api", "handlers_oauth.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), corpusRateLimit) {
		t.Errorf("api/handlers_oauth.go holds no rate-limit message %q", corpusRateLimit)
	}
}
