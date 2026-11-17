// priority_id_fixture_test.go holds the id a priority create derives from its title to
// packages/contracts/fixtures/priority-ids.json, the one list of the cases of that rule, which the
// priorities seed's test and the dashboard's browser row read too. TestPriorityIDFromTitle reads
// priorityIDFromTitle itself and opens no store; TestPriorityIDFromTitleThroughTheRoute reads
// what POST /api/v1/priorities writes for each title, against the database storetest.Open clones
// (newTestHandlerWithStore). Every failure names the title of its case.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/prioritystore"
)

// priorityIDFixturePath is the shared list of the rule's cases, read by relative path as the other
// shared fixtures of this repository are (text/whitespace_fixture_test.go).
const priorityIDFixturePath = "../../../../contracts/fixtures/priority-ids.json"

// priorityIDCase is one case of the shared list: a title and the id a create derives from it, or
// the kind of refusal the create meets. A refusal is a kind, never a message, because the server
// and the seed word theirs differently: no-slug for a title no slug can be written from, and none
// or any for a title whose id after the cut is that keyword of the issue listing.
type priorityIDCase struct {
	Title   string `json:"title"`
	ID      string `json:"id"`
	Refusal string `json:"refusal"`
}

// priorityIDRefusalKeywords pairs the two keyword refusals of the shared list with the id each
// one is: the filter keyword the cut leaves.
var priorityIDRefusalKeywords = map[string]string{
	"none": noAdvancesFilter,
	"any":  anyAdvancesFilter,
}

// readPriorityIDCases reads the shared list and holds it to its own shape: each case carries an id
// or a refusal of a kind the list names, and no two cases carry one id, because the route test
// writes every accepted id into one store.
func readPriorityIDCases(t *testing.T) []priorityIDCase {
	t.Helper()
	raw, err := os.ReadFile(priorityIDFixturePath)
	if err != nil {
		t.Fatalf("read the shared list of priority ids: %v", err)
	}
	var fixture struct {
		Description string           `json:"description"`
		Cases       []priorityIDCase `json:"cases"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatalf("parse %s: %v", priorityIDFixturePath, err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatalf("%s holds no case, so it says nothing about the id a title derives", priorityIDFixturePath)
	}
	titleOf := make(map[string]string, len(fixture.Cases))
	for _, priorityCase := range fixture.Cases {
		_, keyword := priorityIDRefusalKeywords[priorityCase.Refusal]
		switch {
		case priorityCase.ID == "" && priorityCase.Refusal == "":
			t.Fatalf("the case of the title %q names neither an id nor a refusal", priorityCase.Title)
		case priorityCase.ID != "" && priorityCase.Refusal != "":
			t.Fatalf("the case of the title %q names both the id %q and the refusal %q",
				priorityCase.Title, priorityCase.ID, priorityCase.Refusal)
		case priorityCase.Refusal != "" && priorityCase.Refusal != "no-slug" && !keyword:
			t.Fatalf("the case of the title %q names the refusal %q, want no-slug, none or any",
				priorityCase.Title, priorityCase.Refusal)
		case priorityCase.ID != "":
			if other, taken := titleOf[priorityCase.ID]; taken {
				t.Fatalf("the titles %q and %q both give the id %q, and one store holds one priority of an id",
					other, priorityCase.Title, priorityCase.ID)
			}
			titleOf[priorityCase.ID] = priorityCase.Title
		}
	}
	return fixture.Cases
}

// priorityIDCutProblem says what is wrong with id as the cut of slug, or "" when nothing is. The
// id has at most priorityIDMax characters and ends at the end of a word: it is the slug whole, or
// a start of it that the slug continues past with a hyphen. The one exception is a slug whose
// first word is longer than the bound, where no start ends a word and the id is the first
// priorityIDMax characters.
func priorityIDCutProblem(slug, id string) string {
	switch {
	case len(id) > priorityIDMax:
		return fmt.Sprintf("the id %q has %d characters, past the bound of %d", id, len(id), priorityIDMax)
	case !strings.HasPrefix(slug, id):
		return fmt.Sprintf("the id %q is no start of the title's slug %q", id, slug)
	case len(id) == len(slug) || slug[len(id)] == '-':
		return ""
	}
	if firstWord, _, _ := strings.Cut(slug, "-"); len(firstWord) > priorityIDMax && len(id) == priorityIDMax {
		return ""
	}
	return fmt.Sprintf("the id %q stops inside a word of the title's slug %q", id, slug)
}

// TestPriorityIDFromTitle holds priorityIDFromTitle to every case of the shared list: an accepted
// case derives its id, which priorityIDRefusal lets through and which ends at the end of a word
// within priorityIDMax; a no-slug case derives nothing; and a keyword case derives the keyword,
// which priorityIDRefusal refuses after the cut.
func TestPriorityIDFromTitle(t *testing.T) {
	for _, priorityCase := range readPriorityIDCases(t) {
		title := priorityCase.Title
		id := priorityIDFromTitle(title)
		switch keyword, isKeyword := priorityIDRefusalKeywords[priorityCase.Refusal]; {
		case priorityCase.Refusal == "no-slug":
			if id != "" {
				t.Errorf("the title %q derives the id %q, want none: it holds no ASCII letter or digit", title, id)
			}
		case isKeyword:
			if id != keyword {
				t.Errorf("the title %q derives the id %q, want the keyword %q", title, id, keyword)
			}
			if refusal := priorityIDRefusal(id, true); refusal == "" {
				t.Errorf("the title %q derives the id %q, which priorityIDRefusal lets through, want it refused", title, id)
			}
		default:
			if id != priorityCase.ID {
				t.Errorf("the title %q derives the id %q, want %q", title, id, priorityCase.ID)
			}
			if refusal := priorityIDRefusal(id, true); refusal != "" {
				t.Errorf("the title %q derives the id %q, which priorityIDRefusal refuses: %s", title, id, refusal)
			}
			if problem := priorityIDCutProblem(artifactSlug(title), id); problem != "" {
				t.Errorf("the title %q: %s", title, problem)
			}
		}
	}
}

// TestPriorityIDFromTitleThroughTheRoute sends each title of the shared list to
// POST /api/v1/priorities with no id. An accepted case is 201 under its id, with the title written
// whole and an id that ends at the end of a word within priorityIDMax; a refusal is
// 400 PRIORITY_INPUT. A create that names an id one past the bound is 400 PRIORITY_INPUT naming
// the bound, since only a derived id is cut. The refused writes leave the store as it was: it
// holds the accepted cases' priorities and nothing else.
func TestPriorityIDFromTitleThroughTheRoute(t *testing.T) {
	handler, database := newTestHandlerWithStore(t)
	var accepted []string
	for _, priorityCase := range readPriorityIDCases(t) {
		title := priorityCase.Title
		response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/priorities",
			map[string]any{"title": title}, "alice")
		if priorityCase.Refusal != "" {
			if code, text := refusalOf(t, response); response.Code != http.StatusBadRequest || code != codePriorityInput {
				t.Errorf("the title %q: status=%d code=%s error=%.300q, want 400 %s for the refusal %s",
					title, response.Code, code, text, codePriorityInput, priorityCase.Refusal)
			}
			continue
		}
		if response.Code != http.StatusCreated {
			t.Errorf("the title %q: status=%d body=%.300s, want 201 under the id %q",
				title, response.Code, response.Body.String(), priorityCase.ID)
			continue
		}
		made := decodeBody[model.Priority](t, response)
		if made.ID != priorityCase.ID {
			t.Errorf("the title %q is written under the id %q, want %q", title, made.ID, priorityCase.ID)
		}
		if made.Title != strings.TrimSpace(title) {
			t.Errorf("the title %q is written as %q, want it whole", title, made.Title)
		}
		if problem := priorityIDCutProblem(artifactSlug(title), made.ID); problem != "" {
			t.Errorf("the title %q: %s", title, problem)
		}
		accepted = append(accepted, made.ID)
	}

	named := strings.Repeat("n", priorityIDMax+1)
	response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/priorities",
		map[string]any{"id": named, "title": "A named id one past the bound"}, "alice")
	code, text := refusalOf(t, response)
	if response.Code != http.StatusBadRequest || code != codePriorityInput {
		t.Errorf("a create naming an id of %d characters: status=%d code=%s error=%.300q, want 400 %s",
			len(named), response.Code, code, text, codePriorityInput)
	}
	if !strings.Contains(text, fmt.Sprint(priorityIDMax)) {
		t.Errorf("a create naming an id of %d characters is refused with %.300q, want it to name the bound %d",
			len(named), text, priorityIDMax)
	}

	var written int
	if err := database.Pool.QueryRow(context.Background(), `select count(*) from priority_nodes where kind = $1`,
		prioritystore.KindPriority).Scan(&written); err != nil {
		t.Fatalf("count the priorities: %v", err)
	}
	if written != len(accepted) {
		t.Errorf("the store holds %d priorities, want the %d the accepted titles wrote", written, len(accepted))
	}
	listed := listedPriorityIDs(t, handler)
	slices.Sort(listed)
	slices.Sort(accepted)
	if !slices.Equal(listed, accepted) {
		t.Errorf("the live priorities are %q, want only the ids the accepted titles wrote, %q", listed, accepted)
	}
}
