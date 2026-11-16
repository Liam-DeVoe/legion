package refs

import (
	"encoding/json"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// TestTheVisibilityArgumentCarriesTheViewer: VisibilityArg is the one conversion from a reader to
// the argument every predicate binds, "Contracts / K2, the visibility argument" of
// dispatch://INFRA-3862's plan: the acting credential as the author rule compares it, beside what
// the reader's viewer sees. A reader with no viewer is the closed viewer, which sees nothing and
// reads as though some project were private.
func TestTheVisibilityArgumentCarriesTheViewer(t *testing.T) {
	carol := "carol@example.com"
	for _, test := range []struct {
		name   string
		reader model.Actor
		want   string
	}{
		{
			name:   "a reader with no viewer",
			reader: model.Actor{Kind: "user", ID: "alice"},
			want:   `{"author":{"kind":"user","id":"alice","owner":null,"service":null},"sees":[],"open":false,"all":false,"private":true}`,
		},
		{
			name:   "the shared bearer with no viewer",
			reader: model.Actor{},
			want:   `{"author":null,"sees":[],"open":false,"all":false,"private":true}`,
		},
		{
			name: "a restricted member",
			reader: model.Actor{Kind: "user", ID: "bob@outside.example", Viewer: &model.Viewer{
				Person: "bob@outside.example", Restricted: true, Sees: []string{"PRIV"}, AnyPrivate: true,
			}},
			want: `{"author":{"kind":"user","id":"bob@outside.example","owner":null,"service":null},"person":"bob@outside.example",` +
				`"sees":["PRIV"],"open":false,"all":false,"private":true}`,
		},
		{
			name: "a personal token that lists a project",
			reader: model.Actor{Kind: "session", Owner: &carol, Viewer: &model.Viewer{
				Person: carol, Listed: true, Sees: []string{"PRIV"}, AnyPrivate: true,
			}},
			want: `{"author":{"kind":"session","id":"","owner":"carol@example.com","service":null},"person":"carol@example.com",` +
				`"sees":["PRIV"],"open":false,"all":false,"private":true}`,
		},
		{
			name: "a session of an open viewer that sees nothing yet",
			reader: model.Actor{Kind: "session", ID: "ses_1", Viewer: &model.Viewer{
				Session: "ses_1", Open: true,
			}},
			want: `{"author":{"kind":"session","id":"ses_1","owner":null,"service":null},"session":"ses_1",` +
				`"sees":[],"open":true,"all":false,"private":false}`,
		},
		{
			name:   "the server's own viewer",
			reader: model.Actor{Viewer: model.ServerViewer},
			want:   `{"author":null,"sees":[],"open":true,"all":true,"private":false}`,
		},
	} {
		got, ok := VisibilityArg(test.reader).([]byte)
		if !ok {
			t.Fatalf("%s: VisibilityArg answered %T, want the argument's JSON", test.name, VisibilityArg(test.reader))
		}
		if !json.Valid(got) || string(got) != test.want {
			t.Errorf("%s: VisibilityArg = %s\nwant %s", test.name, got, test.want)
		}
	}
}
