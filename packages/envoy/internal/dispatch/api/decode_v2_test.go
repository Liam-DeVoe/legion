package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/api/apicode"
	"github.com/sjawhar/envoy/internal/dispatch/api/apitest"
)

// widgetQuery is a made-up row's query, one field of each kind the decoder reads.
type widgetQuery struct {
	pageQuery
	Project []string            `query:"project"`
	Pinned  bool                `query:"pinned"`
	Issue   queryFilter[string] `query:"issue"`
	Sort    querySort           `query:"sort" enum:"priority,updated_at" default:"-updated_at"`
	Include []string            `query:"include,comma" enum:"thread,mark"`
	From    *time.Time          `query:"from"`
	Days    int                 `query:"days" default:"7" min:"1" max:"30"`
	State   string              `query:"state" enum:"open,closed" default:"open"`
}

// widgetQueryDeclares is every key widgetQuery declares, session among them, sorted: what a
// refusal of an undeclared key names, written out by hand.
var widgetQueryDeclares = []string{"days", "from", "include", "issue", "limit", "offset", "pinned", "project", "session", "sort", "state"}

// widgetBody is a made-up row's JSON body.
type widgetBody struct {
	Title string  `json:"title"`
	Owner *string `json:"owner"`
}

// widgetResolved and widgetAnswered are the two arms of a made-up state union.
type widgetResolved struct {
	State string `json:"state"`
}

type widgetAnswered struct {
	State string `json:"state"`
	Text  string `json:"text"`
}

// widgetUpload is a made-up upload's declared fields.
type widgetUpload struct {
	Name    string  `json:"name"`
	Summary *string `json:"summary"`
}

// madeUpV2Route is row mounted at /api/v2 alone, as register mounts a group's row.
func madeUpV2Route(row routeRow) apiRoute {
	row.Versions = onlyV2
	return mountRow(row)[0]
}

// widgetListRoute is a made-up list read whose query is widgetQuery.
func widgetListRoute(bounds listBounds) apiRoute {
	row := madeUpRow()
	row.Query, row.List = widgetQuery{}, bounds
	return madeUpV2Route(row)
}

// decodeQuery decodes target's query as route declares it, answering the query read or the
// refusal.
func decodeQuery(t *testing.T, route apiRoute, target string) (widgetQuery, error) {
	t.Helper()
	request, err := decodeV2Request(httptest.NewRequest(http.MethodGet, target, nil), route)
	if err != nil {
		return widgetQuery{}, err
	}
	return v2Query[widgetQuery](request), nil
}

// refusalCodeOf is the v2 name of the refusal err is, and "" for an error that is no refusal.
func refusalCodeOf(err error) string {
	var refusal *apiError
	if errors.As(err, &refusal) {
		return refusal.code.Name()
	}
	var unknown *queryKeyUnknownError
	if errors.As(err, &unknown) {
		return apicode.QueryKeyUnknown.Name()
	}
	return ""
}

// assertQueryRefused fails unless target is refused with code, its message naming each of names.
func assertQueryRefused(t *testing.T, route apiRoute, target string, code apicode.Code, names ...string) {
	t.Helper()
	_, err := decodeQuery(t, route, target)
	if refusalCodeOf(err) != code.Name() {
		t.Fatalf("%s: decoded with %v, want %s", target, err, code.Name())
	}
	for _, name := range names {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("%s: refused %q, which does not name %q", target, err.Error(), name)
		}
	}
}

// A query key the route does not declare is 400 QUERY_KEY_UNKNOWN naming the key and every key
// the route declares, sorted, session among them, whatever else the query holds: a value the
// route would refuse beside it does not hide it.
func TestTheV2DecoderRefusesAnUndeclaredKey(t *testing.T) {
	route := widgetListRoute(listBounds{})
	_, err := decodeQuery(t, route, "/api/v2/widgets?project=A&zz_undeclared=1&pinned=perhaps")
	var unknown *queryKeyUnknownError
	if !errors.As(err, &unknown) || unknown.key != "zz_undeclared" || !slices.Equal(unknown.declared, widgetQueryDeclares) {
		t.Fatalf("decoded with %#v, want the key zz_undeclared refused, naming %v", err, widgetQueryDeclares)
	}

	request := withAPIVersion(apiV2, func(w http.ResponseWriter, r *http.Request) {
		(&server{}).writeV2DecodeError(w, r, err)
	})
	response := httptest.NewRecorder()
	request(response, httptest.NewRequest(http.MethodGet, "/api/v2/widgets?zz_undeclared=1", nil))
	body := decodeBody[queryKeyUnknownBody](t, response)
	if response.Code != http.StatusBadRequest || body.Code != "QUERY_KEY_UNKNOWN" || body.Key != "zz_undeclared" ||
		!slices.Equal(body.Declared, widgetQueryDeclares) || body.Error == "" {
		t.Fatalf("answered %d %s, want 400 {code: QUERY_KEY_UNKNOWN, error, key, declared}", response.Code, response.Body.String())
	}

	// A route that declares no query takes session alone.
	bare := madeUpRow()
	bare.List = listBounds{}
	_, err = decodeQuery(t, madeUpV2Route(bare), "/api/v2/widgets?session=s-1&limit=5")
	if !errors.As(err, &unknown) || unknown.key != "limit" || !slices.Equal(unknown.declared, []string{"session"}) {
		t.Fatalf("a route with no query read limit: %#v, want it refused, naming session alone", err)
	}
}

// Every key the row declares is read by its type, and every key the query leaves out takes its
// default or its zero value; session is read beside them on every route.
func TestTheV2DecoderReadsWhatTheRowDeclares(t *testing.T) {
	route := widgetListRoute(listBounds{Default: 20, Max: 100})
	target := "/api/v2/widgets?project=A&project=B&pinned=true&issue=ACME-7&sort=priority&include=thread,mark" +
		"&from=2026-11-17T09:00:00%2B01:00&days=30&state=closed&limit=10&offset=40&session=%20s-1%20"
	request, err := decodeV2Request(httptest.NewRequest(http.MethodGet, target, nil), route)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := v2Query[widgetQuery](request)
	from := time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)
	want := widgetQuery{
		pageQuery: pageQuery{Limit: 10, Offset: 40},
		Project:   []string{"A", "B"}, Pinned: true,
		Issue: queryFilter[string]{Set: true, Value: "ACME-7"},
		Sort:  querySort{Field: "priority"}, Include: []string{"thread", "mark"},
		From: &from, Days: 30, State: "closed",
	}
	if got.From == nil || !got.From.Equal(from) {
		t.Fatalf("from = %v, want %v", got.From, from)
	}
	got.From = want.From
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded %+v, want %+v", got, want)
	}
	if session, decoded := decodedSession(request); !decoded || session != "s-1" {
		t.Fatalf("session = %q, %v; want s-1 trimmed, read by the decoder", session, decoded)
	}

	empty, err := decodeQuery(t, route, "/api/v2/widgets")
	if err != nil {
		t.Fatalf("decode an empty query: %v", err)
	}
	if empty.Limit != 20 || empty.Offset != 0 || empty.Sort != (querySort{Field: "updated_at", Descending: true}) ||
		empty.Days != 7 || empty.State != "open" || empty.Issue.Set || empty.From != nil || empty.Project != nil || empty.Pinned {
		t.Fatalf("an empty query decoded as %+v, want every default", empty)
	}
}

// A key given twice is refused unless its field is a slice, which takes every value given.
func TestTheV2DecoderRefusesARepeatedScalar(t *testing.T) {
	route := widgetListRoute(listBounds{})
	assertQueryRefused(t, route, "/api/v2/widgets?state=open&state=closed", apicode.InvalidQuery, "state", "one value")
	assertQueryRefused(t, route, "/api/v2/widgets?session=a&session=b", apicode.InvalidQuery, "session")
	assertQueryRefused(t, route, "/api/v2/widgets?sort=priority&sort=updated_at", apicode.InvalidQuery, "sort")
	if query, err := decodeQuery(t, route, "/api/v2/widgets?project=A&project=B"); err != nil || !slices.Equal(query.Project, []string{"A", "B"}) {
		t.Fatalf("a repeated slice key decoded %v, %v; want both values", query.Project, err)
	}
}

// A boolean is true or false and nothing else (R1.6): 1, yes, and an empty value are refused.
func TestTheV2DecoderRefusesABadBoolean(t *testing.T) {
	route := widgetListRoute(listBounds{})
	for _, value := range []string{"1", "TRUE", "yes", ""} {
		assertQueryRefused(t, route, "/api/v2/widgets?pinned="+value, apicode.InvalidQuery, "pinned", "true or false")
	}
	if query, err := decodeQuery(t, route, "/api/v2/widgets?pinned=false"); err != nil || query.Pinned {
		t.Fatalf("pinned=false decoded %v, %v", query.Pinned, err)
	}
}

// none is the unset value of a filter (R1.6): a filter left out is unset, one set to none selects
// the rows whose field is unset, and one set to a value selects that value.
func TestTheV2DecoderReadsNoneAsTheUnsetValue(t *testing.T) {
	route := widgetListRoute(listBounds{})
	for target, want := range map[string]queryFilter[string]{
		"/api/v2/widgets":              {},
		"/api/v2/widgets?issue=none":   {Set: true, None: true},
		"/api/v2/widgets?issue=ACME-1": {Set: true, Value: "ACME-1"},
	} {
		query, err := decodeQuery(t, route, target)
		if err != nil || query.Issue != want {
			t.Errorf("%s: issue = %+v, %v; want %+v", target, query.Issue, err, want)
		}
	}
}

// A sort names one of the row's sortable fields, ascending or after a - descending (R1.6).
func TestTheV2DecoderRefusesABadSort(t *testing.T) {
	route := widgetListRoute(listBounds{})
	for _, value := range []string{"title", "--priority", "+priority", "priority,updated_at", ""} {
		assertQueryRefused(t, route, "/api/v2/widgets?sort="+value, apicode.InvalidQuery, "sort", "priority", "updated_at")
	}
	if query, err := decodeQuery(t, route, "/api/v2/widgets?sort=-priority"); err != nil || query.Sort != (querySort{Field: "priority", Descending: true}) {
		t.Fatalf("sort=-priority decoded %+v, %v", query.Sort, err)
	}
}

// include is a comma list of the row's include members (R1.9), each of which must be one.
func TestTheV2DecoderRefusesAnUnknownIncludeMember(t *testing.T) {
	route := widgetListRoute(listBounds{})
	for _, value := range []string{"thread,bogus", "bogus", "thread,", ""} {
		assertQueryRefused(t, route, "/api/v2/widgets?include="+value, apicode.InvalidQuery, "include", "thread", "mark")
	}
	query, err := decodeQuery(t, route, "/api/v2/widgets?include=mark&include=thread")
	if err != nil || !slices.Equal(query.Include, []string{"mark", "thread"}) {
		t.Fatalf("include given twice decoded %v, %v", query.Include, err)
	}
}

// A time is RFC3339 (R1.5); a date alone, a Unix time and words are refused.
func TestTheV2DecoderRefusesABadTime(t *testing.T) {
	route := widgetListRoute(listBounds{})
	for _, value := range []string{"2026-11-17", "1763370000", "yesterday", "2026-11-17T09:00:00"} {
		assertQueryRefused(t, route, "/api/v2/widgets?from="+value, apicode.InvalidQuery, "from", "RFC3339")
	}
}

// A limit is held to the row's bounds, naming the bound (R1.4): a row that names none takes 50 and
// 200, and a catalogue read whole takes any limit of 1 or more, answering every item without one.
func TestTheV2DecoderRefusesALimitAboveTheMaximum(t *testing.T) {
	bounded := widgetListRoute(listBounds{Default: 20, Max: 100})
	assertQueryRefused(t, bounded, "/api/v2/widgets?limit=101", apicode.InvalidLimit, "100")
	for _, value := range []string{"0", "-1", "ten", "1.5", ""} {
		assertQueryRefused(t, bounded, "/api/v2/widgets?limit="+value, apicode.InvalidLimit, "100")
	}
	assertQueryRefused(t, bounded, "/api/v2/widgets?limit=5&limit=6", apicode.InvalidLimit, "100")
	if query, err := decodeQuery(t, bounded, "/api/v2/widgets?limit=100"); err != nil || query.Limit != 100 {
		t.Fatalf("limit=100 decoded %d, %v", query.Limit, err)
	}

	unbounded := widgetListRoute(listBounds{})
	assertQueryRefused(t, unbounded, "/api/v2/widgets?limit=201", apicode.InvalidLimit, "200")
	if query, err := decodeQuery(t, unbounded, "/api/v2/widgets"); err != nil || query.Limit != defaultListLimit {
		t.Fatalf("a row with no bounds answered limit %d, %v; want %d", query.Limit, err, defaultListLimit)
	}

	whole := widgetListRoute(listBounds{Whole: true})
	assertQueryRefused(t, whole, "/api/v2/widgets?limit=0", apicode.InvalidLimit, "1 or more")
	for target, want := range map[string]int{"/api/v2/widgets": 0, "/api/v2/widgets?limit=100000": 100000} {
		if query, err := decodeQuery(t, whole, target); err != nil || query.Limit != want {
			t.Errorf("%s on a catalogue read whole decoded limit %d, %v; want %d", target, query.Limit, err, want)
		}
	}
}

// An offset is an integer of 0 or more (R1.4).
func TestTheV2DecoderRefusesANegativeOffset(t *testing.T) {
	route := widgetListRoute(listBounds{})
	for _, value := range []string{"-1", "first", ""} {
		assertQueryRefused(t, route, "/api/v2/widgets?offset="+value, apicode.InvalidOffset, "offset", "0 or more")
	}
	if query, err := decodeQuery(t, route, "/api/v2/widgets?offset=0"); err != nil || query.Offset != 0 {
		t.Fatalf("offset=0 decoded %d, %v", query.Offset, err)
	}
}

// madeUpStateRoute is a made-up transition whose body is a union keyed by state.
func madeUpStateRoute() apiRoute {
	row := madeUpRow()
	row.Method, row.Pattern, row.Kind = http.MethodPut, "/widgets/{id}/state", kindState
	row.Body = requestBody{Kind: bodyStateUnion, Arms: map[string]any{"resolved": widgetResolved{}, "answered": widgetAnswered{}}}
	row.Scope = scopeDeployment
	return madeUpV2Route(row)
}

// rawJSONRequest is a request carrying body, JSON text as written.
func rawJSONRequest(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

// A state body's state picks the arm it is decoded into; a state the union does not take, or none
// at all, is 400 STATE_INVALID naming the states it takes.
func TestTheV2DecoderRefusesAStateUnionWithAnUnknownState(t *testing.T) {
	route := madeUpStateRoute()
	for _, body := range []string{`{"state":"bogus"}`, `{"text":"no state"}`, `{"state":7}`} {
		_, err := decodeV2Request(rawJSONRequest(http.MethodPut, "/api/v2/widgets/w1/state", body), route)
		if refusalCodeOf(err) != "STATE_INVALID" || !strings.Contains(err.Error(), "answered, resolved") {
			t.Errorf("%s: decoded with %v, want STATE_INVALID naming answered, resolved", body, err)
		}
	}
	request, err := decodeV2Request(rawJSONRequest(http.MethodPut, "/api/v2/widgets/w1/state", `{"state":"answered","text":"Yes."}`), route)
	if err != nil {
		t.Fatalf("decode an answered body: %v", err)
	}
	if arm, ok := v2BodyArm(request).(widgetAnswered); !ok || arm.Text != "Yes." {
		t.Fatalf("the answered body decoded as %#v, want its own arm", v2BodyArm(request))
	}
}

// An upload's declared fields are read from its multipart body, and a field it does not declare
// is ignored, as an undeclared JSON member is; its file is read whole.
func TestTheV2DecoderIgnoresAnUndeclaredUploadField(t *testing.T) {
	row := madeUpRow()
	row.Method, row.Pattern = http.MethodPost, "/widgets/{id}/files"
	row.Body = requestBody{Kind: bodyUpload, Type: widgetUpload{}}
	row.Scope = scopeDeployment
	route := madeUpV2Route(row)
	body, contentType, err := apitest.MultipartUpload(map[string]string{
		"name": "notes.txt", "summary": "Two lines", "zz_undeclared": "ignored",
	}, "notes.txt", "text/plain", []byte("first\nsecond\n"))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v2/widgets/w1/files", body)
	request.Header.Set("Content-Type", contentType)
	decoded, err := decodeV2Request(request, route)
	if err != nil {
		t.Fatalf("an upload with an undeclared field was refused: %v", err)
	}
	fields := v2Body[widgetUpload](decoded)
	if fields.Name != "notes.txt" || fields.Summary == nil || *fields.Summary != "Two lines" {
		t.Fatalf("the upload's fields decoded as %+v", fields)
	}
	file := v2Upload(decoded)
	if file == nil || file.Name != "notes.txt" || file.ContentType != "text/plain" || string(file.Content) != "first\nsecond\n" {
		t.Fatalf("the upload's file decoded as %+v", file)
	}
}

// A JSON body's undeclared member is ignored (R4), and the body is put back as the bytes that
// were read, so the keyed layer behind the decoder fingerprints what was decoded.
func TestTheV2DecoderIgnoresAnUndeclaredBodyMember(t *testing.T) {
	row := madeUpRow()
	row.Method, row.Pattern = http.MethodPost, "/widgets"
	row.Body = requestBody{Kind: bodyJSON, Type: widgetBody{}}
	row.Scope = scopeDeployment
	route := madeUpV2Route(row)
	const sent = `{"title":"A widget","zz_undeclared":{"deep":[1,2]}}`
	request, err := decodeV2Request(rawJSONRequest(http.MethodPost, "/api/v2/widgets", sent), route)
	if err != nil {
		t.Fatalf("a body with an undeclared member was refused: %v", err)
	}
	if body := v2Body[widgetBody](request); body.Title != "A widget" || body.Owner != nil {
		t.Fatalf("the body decoded as %+v", body)
	}
	again, err := io.ReadAll(request.Body)
	if err != nil || !bytes.Equal(again, []byte(sent)) {
		t.Fatalf("the body read again is %q, %v; want the bytes sent", again, err)
	}
	var members map[string]json.RawMessage
	if decoded, _ := decodedV2(request); json.Unmarshal([]byte(sent), &members) != nil || len(decoded.members) != len(members) {
		t.Fatalf("the decoder kept members %v, want every member sent", decoded.members)
	}
}

// A row whose query or body the decoder cannot read, or whose authority rule could never hold,
// is refused at mount (undecodableRoutes), naming what is wrong; so is a row served at /api/v1 too
// that declares authorities, which /api/v1 reads no decoded request to hold.
func TestARowTheV2DecoderCannotReadRefusesToMount(t *testing.T) {
	if faults := undecodableRoutes(slices.Concat(mountRow(madeUpRow()), []apiRoute{widgetListRoute(listBounds{}), madeUpStateRoute()})); len(faults) > 0 {
		t.Fatalf("well-formed rows were refused: %q", faults)
	}
	type untagged struct {
		Project string
	}
	type sortWithoutDefault struct {
		Sort querySort `query:"sort" enum:"priority"`
	}
	type enumOnABoolean struct {
		Pinned bool `query:"pinned" enum:"true"`
	}
	type limitWithItsOwnBound struct {
		Limit int `query:"limit" max:"10"`
	}
	type sessionOfItsOwn struct {
		Session string `query:"session"`
	}
	type commaScalar struct {
		Include string `query:"include,comma"`
	}
	type badDefault struct {
		Days int `query:"days" default:"week"`
	}
	type unreadType struct {
		Ratio float64 `query:"ratio"`
	}
	person := routeScopeSet{"person"}
	for name, change := range map[string]func(*routeRow){
		"an untagged field":                func(row *routeRow) { row.Query = untagged{} },
		"a sort with no default":           func(row *routeRow) { row.Query = sortWithoutDefault{} },
		"an enum on a boolean":             func(row *routeRow) { row.Query = enumOnABoolean{} },
		"a limit with a bound of its own":  func(row *routeRow) { row.Query = limitWithItsOwnBound{} },
		"a session of its own":             func(row *routeRow) { row.Query = sessionOfItsOwn{} },
		"a comma list that is no slice":    func(row *routeRow) { row.Query = commaScalar{} },
		"a default no value of its own":    func(row *routeRow) { row.Query = badDefault{} },
		"a type the decoder does not read": func(row *routeRow) { row.Query = unreadType{} },
		"a query that is a pointer":        func(row *routeRow) { row.Query = &widgetQuery{} },
		"bounds with no default":           func(row *routeRow) { row.Query, row.List = widgetQuery{}, listBounds{Max: 10} },
		"a union with no arms":             func(row *routeRow) { row.Body = requestBody{Kind: bodyStateUnion} },
		"a body that is a pointer":         func(row *routeRow) { row.Body = requestBody{Kind: bodyJSON, Type: &widgetBody{}} },
		"a rule on an undeclared key": func(row *routeRow) {
			row.Query = widgetQuery{}
			row.Authorities = []authorityRule{{Where: authorityWhere{In: partQuery, Name: "waiting_on"}, Value: "me", Scopes: person}}
		},
		"a rule on a value the enum does not list": func(row *routeRow) {
			row.Query = widgetQuery{}
			row.Authorities = []authorityRule{{Where: authorityWhere{In: partQuery, Name: "state"}, Value: "opne", Scopes: person}}
		},
		"a rule on a member no type declares": func(row *routeRow) {
			row.Body = requestBody{Kind: bodyJSON, Type: widgetBody{}}
			row.Authorities = []authorityRule{{Where: authorityWhere{In: partBody, Name: "colour"}, Scopes: person}}
		},
		"a rule on a state no arm is": func(row *routeRow) {
			row.Body = requestBody{Kind: bodyStateUnion, Arms: map[string]any{"resolved": widgetResolved{}}}
			row.Authorities = []authorityRule{{Where: authorityWhere{In: partBody, Name: "state"}, Value: "accepted", Scopes: person}}
		},
		"a rule with no scope": func(row *routeRow) {
			row.Body = requestBody{Kind: bodyJSON, Type: widgetBody{}}
			row.Authorities = []authorityRule{{Where: authorityWhere{In: partBody, Name: "owner"}}}
		},
	} {
		row := madeUpRow()
		change(&row)
		if faults := undecodableRoutes([]apiRoute{madeUpV2Route(row)}); len(faults) != 1 {
			t.Errorf("%s: refused %q, want the row refused once", name, faults)
		}
	}

	both := madeUpRow()
	both.Body = requestBody{Kind: bodyJSON, Type: widgetBody{}}
	both.Authorities = []authorityRule{{Where: authorityWhere{In: partBody, Name: "owner"}, Scopes: person}}
	faults := undecodableRoutes(mountRow(both))
	if len(faults) != 1 || !strings.HasPrefix(faults[0], "GET /api/v1/widgets: declares Authorities") {
		t.Fatalf("a row served at /api/v1 too that declares authorities was answered %q, want its /api/v1 mount refused", faults)
	}
}
