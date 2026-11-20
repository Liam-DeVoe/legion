package oauth_test

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"

	"authelia.com/provider/oauth2/handler/rfc8693"

	"github.com/sjawhar/envoy/internal/broker/oauthstore"
	"github.com/sjawhar/envoy/internal/broker/oauthwire"
)

// tradeKinds is every kind of trade the exchange mints a token for, under the subject token type
// the trade presents. The token type alone does not tell every kind apart: a pod's token is a
// pod entry's or a read entry's, and a machine's Google identity token is an entry's that names a
// person or one's that names none, which only the entry the broker's configuration lists decides.
// So the kinds are written here by hand, and TestEveryTradeKindNarrowsOnRequest ties the list to
// the code: every subject token type the exchange client accepts must be listed here, and every
// type listed here must be one the client accepts. A new type the client takes fails that test
// until its kinds are written here.
var tradeKinds = map[string][]string{
	oauthwire.SubjectTokenTypeKubernetes:  {"pod-entry", "read-entry"},
	oauthwire.SubjectTokenTypeGitHub:      {"workflow-entry"},
	oauthwire.SubjectTokenTypeGoogle:      {"google-identity-no-person", "google-identity-with-a-person"},
	oauthwire.SubjectTokenTypePerson:      {"person-sign-in"},
	oauthwire.SubjectTokenTypeAccessToken: {"machine-login"},
}

// narrowingRow is how the narrowing tests trade one kind of tradeKinds asking for the scope
// read: the subject token type its trade presents.
type narrowingRow struct {
	// subjectTokenType is the subject_token_type the row's trade presents, the type tradeKinds
	// lists the row's kind under.
	subjectTokenType string
}

// narrowingRows is the narrowing row of every kind of trade, keyed by the kind's name as
// tradeKinds spells it, which is also the key of the kind's answer in
// internal/brokertoken/testdata/narrowed-answers.json.
var narrowingRows = map[string]narrowingRow{}

// TestEveryTradeKindNarrowsOnRequest holds narrowingRows to every kind of trade the exchange
// mints a token for. The kinds are those of every subject token type the exchange client accepts,
// read from the client's own registration (oauthstore, GetClient), with the read entry beside the
// pod entry under the Kubernetes type (tradeKinds). It fails for each accepted type tradeKinds
// does not list, each type it lists that the client does not accept, each kind with no row, each
// row that names no kind, and each row whose subject token type is not its kind's. Every problem
// is collected first and the test fails once, naming each of them.
func TestEveryTradeKindNarrowsOnRequest(t *testing.T) {
	// GetClient reads nothing of the store's own state: the two clients are the code's.
	client, err := (&oauthstore.Store{}).GetClient(context.Background(), oauthwire.ExchangeClientID)
	if err != nil {
		t.Fatalf("GetClient(%s): %v", oauthwire.ExchangeClientID, err)
	}
	exchanging, ok := client.(rfc8693.Client)
	if !ok {
		t.Fatalf("the exchange client is a %T, which answers no subject token types", client)
	}
	accepted := exchanging.GetSupportedSubjectTokenTypes()
	if len(accepted) == 0 {
		t.Fatal("the exchange client accepts no subject token type, so the walk read nothing")
	}

	var problems []string
	kindOf := map[string]string{}
	for _, tokenType := range accepted {
		kinds, listed := tradeKinds[tokenType]
		if !listed {
			problems = append(problems, "the subject token type "+tokenType+": the exchange client accepts it, and tradeKinds lists no kind of trade under it")
		}
		for _, kind := range kinds {
			kindOf[kind] = tokenType
		}
	}
	for tokenType := range tradeKinds {
		if !slices.Contains(accepted, tokenType) {
			problems = append(problems, "the subject token type "+tokenType+": tradeKinds lists it, and the exchange client does not accept it")
		}
	}
	for kind := range kindOf {
		if _, rowed := narrowingRows[kind]; !rowed {
			problems = append(problems, "the kind "+kind+": no row in narrowingRows")
		}
	}
	for kind, row := range narrowingRows {
		tokenType, known := kindOf[kind]
		switch {
		case !known:
			problems = append(problems, "narrowingRows row "+kind+": it names no kind of trade tradeKinds lists under a type the exchange client accepts")
		case row.subjectTokenType != tokenType:
			problems = append(problems, "narrowingRows row "+kind+": it presents "+row.subjectTokenType+", and its kind is a trade of "+tokenType)
		}
	}
	sort.Strings(problems)
	t.Logf("the exchange client accepts %d subject token types, under which %d kinds of trade mint a token; narrowingRows holds %d rows",
		len(accepted), len(kindOf), len(narrowingRows))
	if len(problems) > 0 {
		t.Errorf("the narrowing inventory (narrowing_test.go) has %d problems:\n%s", len(problems), strings.Join(problems, "\n"))
	}
}
