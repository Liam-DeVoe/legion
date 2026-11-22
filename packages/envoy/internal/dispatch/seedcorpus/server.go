package seedcorpus

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/api"
	"github.com/sjawhar/envoy/internal/dispatch/auth"
	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/identity"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// personHeader is the header the in-process API reads a person's login from. The header identity
// is the one the API's own tests sign a person in with (identity.HeaderIdentity): nothing outside
// this process can present it, since no socket serves these handlers.
const personHeader = "X-Dispatch-User"

// serverURL is the origin the in-process API writes its links under. No request reaches it: it
// is the public origin the API's own tests serve under, and every address the corpus writes is a
// dispatch:// address, which names no origin.
const serverURL = "https://dispatch.example"

// apiServer is the API's own handlers, built over the database the corpus is written into and
// called in this process. Register places the shipped routes on a mux of this process's own,
// which no listener serves: every request below is handed to the mux's ServeHTTP directly, so the
// corpus is written by the same handlers a deployment runs, under the same owner locks, and no
// route, test hook or socket is added for it.
type apiServer struct {
	mux        *http.ServeMux
	documents  *docs.Service
	background api.Background
	cancel     context.CancelFunc
	// token is the shared agent bearer of this process alone, drawn at random, which an agent's
	// write presents beside the session its body names.
	token string
}

// startAPI builds the in-process API over database: the document service, the API's
// dependencies with a header identity and a shared agent bearer drawn here, and the routes on a
// mux of its own. Each person who writes is recorded as signed in (recordPeople), which a
// project's default assignee and an ask's asked_of require.
func startAPI(ctx context.Context, database *store.Store, people []string) (*apiServer, error) {
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	tokens, err := auth.ParseSharedAgentTokens(token)
	if err != nil {
		return nil, fmt.Errorf("parse the loader's agent bearer: %w", err)
	}
	keys := auth.SingleSigningKey(token)
	peopleStore := store.NewPgPeopleStore(database.Pool, keys, nil)
	for _, login := range people {
		if err := peopleStore.Record(ctx, login); err != nil {
			return nil, fmt.Errorf("record %s as a person who has signed in: %w", login, err)
		}
	}
	header := identity.HeaderIdentity{Header: personHeader, People: peopleStore}
	broker := events.NewBroker()
	documents := docs.New(docs.Deps{Store: database, Events: broker, ServerURL: serverURL, Settle: 20 * time.Millisecond, Identity: header})
	deps, err := api.NewDeps(api.DepsInput{
		Store:       database,
		Identity:    header,
		AgentTokens: tokens,
		ServerURL:   serverURL,
		Docs:        documents,
		Events:      broker,
	})
	if err != nil {
		_ = documents.Shutdown(context.Background())
		return nil, fmt.Errorf("build the API's dependencies: %w", err)
	}
	lifetime, cancel := context.WithCancel(ctx)
	mux := http.NewServeMux()
	background := api.Register(mux, lifetime, deps)
	return &apiServer{mux: mux, documents: documents, background: background, cancel: cancel, token: token}, nil
}

// close settles every document the corpus wrote (docs.Service.Quiesce), waits for the work the
// handlers left behind their answers - a delivery, a related-item check - and shuts the document
// service down.
func (s *apiServer) close(ctx context.Context) error {
	quiesced := s.documents.Quiesce(ctx)
	s.cancel()
	s.background.Wait()
	shut := s.documents.Shutdown(context.Background())
	return errors.Join(quiesced, shut)
}

// randomToken is 32 random bytes in hex.
func randomToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("draw the loader's agent bearer: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// writer is who a request is made as: a person, by login, through the header identity, or an
// agent's session, through the shared bearer with the session named in the body's actor.
type writer struct {
	person  string
	session string
}

// asPerson and asSession are the two kinds of writer.
func asPerson(login string) writer    { return writer{person: login} }
func asSession(session string) writer { return writer{session: session} }

// actor is the actor a session's write names in its body.
func (w writer) actor() *model.Actor {
	if w.session == "" {
		return nil
	}
	return &model.Actor{Kind: "session", ID: w.session}
}

// String names the writer in a failure.
func (w writer) String() string {
	if w.session != "" {
		return "session " + w.session
	}
	return w.person
}

// call sends one request to the API as who, with body encoded as JSON (an agent's carrying its
// actor), and decodes the answer into into where it is not nil. An answer whose status is none of
// want is an error naming the request and the answer, so a refusal says what the API refused.
func (s *apiServer) call(ctx context.Context, who writer, method, path string, body map[string]any, into any, want ...int) error {
	var payload io.Reader = http.NoBody
	if body != nil {
		if actor := who.actor(); actor != nil {
			if _, named := body["actor"]; !named {
				body["actor"] = actor
			}
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s %s: %w", method, path, err)
		}
		payload = bytes.NewReader(encoded)
	}
	request := httptest.NewRequestWithContext(ctx, method, path, payload)
	request.Header.Set("Content-Type", "application/json")
	if who.session != "" {
		request.Header.Set("Authorization", "Bearer "+s.token)
	} else {
		request.Header.Set(personHeader, who.person)
	}
	answer := httptest.NewRecorder()
	s.mux.ServeHTTP(answer, request)
	if len(want) == 0 {
		want = []int{http.StatusOK, http.StatusCreated}
	}
	if !slices.Contains(want, answer.Code) {
		return fmt.Errorf("%s %s as %s answered %d: %s", method, path, who, answer.Code, bytes.TrimSpace(answer.Body.Bytes()))
	}
	if into == nil {
		return nil
	}
	if err := json.Unmarshal(answer.Body.Bytes(), into); err != nil {
		return fmt.Errorf("decode the answer of %s %s: %w", method, path, err)
	}
	return nil
}
