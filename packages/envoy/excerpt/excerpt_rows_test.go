package excerpt_test

import (
	"testing"

	"github.com/sjawhar/envoy/excerpt"
	"github.com/sjawhar/envoy/internal/redact/redacttest"
)

// TestTextPrintsNoCredentialAPageEchoes: a gateway's page that holds the token its request
// carried, bare, and words quoting a URL that carries a user, a password and a token. Text's
// excerpt of each holds none of them: the first by its redaction of the tokens it is handed
// (redact.Tokens), the second by the rule for remote text (redact.Remote). Each is a designated
// assertion of the check's mutation gate: excerpt.text-tokens and excerpt.text-remote.
func TestTextPrintsNoCredentialAPageEchoes(t *testing.T) {
	redacttest.RequireNone(t, "excerpt.text-tokens", excerpt.Text("<pre>\n"+redacttest.Token+"\n</pre>", redacttest.Token))
	redacttest.RequireNone(t, "excerpt.text-remote", excerpt.Text("<p>"+redacttest.Words+"</p>"))
}
