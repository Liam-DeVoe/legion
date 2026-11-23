package api

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/access"
	"github.com/sjawhar/envoy/internal/dispatch/embed"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// TestTheRelatedStatementsNameNothingOfAProjectTheirReaderDoesNotSee runs every statement the
// related-item check runs (the keyword statement and the three meaning statements, each plain and
// excluding) over the private project of verification v16's seed, for carol, who sees it, and for
// alice, who does not, with the same terms and the same vector. carol is answered every object
// of the project, which is what makes the row able to fail, and alice nothing: no item of a
// project the reader does not see is ranked or named.
//
// The terms are the one word every object holds, and its vector is near nothing, so each
// statement answers what its keyword legs find.
//
// Every statement also holds each row to the node rule of its kinds CTE (searchKindsCTE), which
// hides these objects from alice whatever the arms do, so the whole statements cannot tell
// whether the arms' own project rule holds. The keyword legs are therefore also run alone, under
// the check's own q, where relatedProjectVisible is the only rule between alice and the rows: a
// leg that lets a row of a project its reader does not see through counts it in the matches and
// the total before that rule drops it, and runs the rank for it.
func TestTheRelatedStatementsNameNothingOfAProjectTheirReaderDoesNotSee(t *testing.T) {
	embedder := &fakeEmbedder{vectors: map[string][]float32{searchAccessMeaning: queryVector(), searchAccessWord: angledVector(0, 3)}}
	seed := seedPrivateProjectWith(t, testServerOptions{embedder: embedder})
	objects := writeSearchableObjects(t, seed, embedder)
	ctx := context.Background()
	pool := seed.database.Pool

	var lexeme, tsquery string
	if err := pool.QueryRow(ctx, `select l.lexeme, quote_literal(l.lexeme) from unnest(to_tsvector('english', $1)) l`,
		searchAccessWord).Scan(&lexeme, &tsquery); err != nil {
		t.Fatalf("read the lexeme of %q: %v", searchAccessWord, err)
	}
	picked := relatedTerms{Terms: []relatedTerm{{Lexeme: lexeme, IDF: 1}}, TSQuery: tsquery}
	readerOf := func(login string) model.Actor {
		t.Helper()
		viewer, err := access.Resolve(ctx, pool, model.BrowserActor(login))
		if err != nil {
			t.Fatalf("resolve what %s sees: %v", login, err)
		}
		return model.Actor{Viewer: &viewer}
	}
	carol, alice := readerOf(seedCarol), readerOf(seedAlice)
	meaning := []any{embed.Literal(embedder.vectors[searchAccessWord]), searchMeaningFloor, seed.server.deps.Embedder.Identity()}
	want := slices.Clone(objects[seedPrivate])
	slices.Sort(want)

	for _, read := range []struct {
		name      string
		statement relatedStatement
		meaning   []any
	}{
		{"the keyword statement", relatedKeywordStatement, nil},
		{"the unscoped meaning statement", relatedMeaningStatement, meaning},
		{"the scoped meaning statement", relatedMeaningScopedStatement, meaning},
		{"the joined meaning statement", relatedMeaningJoinedStatement, meaning},
	} {
		for _, excluded := range [][]string{nil, {seed.issue[seedPrivate]}} {
			name := read.name
			if excluded != nil {
				name += ", excluding"
			}
			t.Run(name, func(t *testing.T) {
				answer := func(who model.Actor) []string {
					t.Helper()
					response, err := seed.server.relatedSearch(ctx, pool, who, read.statement, seedPrivate, picked, excluded, read.meaning...)
					if err != nil {
						t.Fatalf("run %s: %v", name, err)
					}
					return searchResultKeys(response.Results)
				}
				if got := answer(carol); !slices.Equal(got, want) {
					t.Fatalf("%s answers carol, who sees %s, %v; want its objects %v", name, seedPrivate, got, want)
				}
				if got := answer(alice); len(got) != 0 {
					t.Errorf("%s answers alice, who does not see %s, %v; want nothing", name, seedPrivate, got)
				}
			})
		}
	}

	// The keyword legs alone: $1 the terms as a tsquery, $2 the project, $3 and $4 the lexemes and
	// their idf, $5 the reader's viewer.
	legs := `
with q as (select $1::tsquery as tsq, ''::text as term, '{}'::text[] as own_keys,
                  ''::text as status_filter, ''::text as author_filter, null::timestamptz as updated_since),
` + relatedTermWeights(3) + `legs as (` + relatedKeywordLegs(5) + `
)
select kind || '/' || id from legs order by 1`
	t.Run("the keyword legs alone", func(t *testing.T) {
		answer := func(who model.Actor) []string {
			t.Helper()
			rows, err := pool.Query(ctx, legs, picked.TSQuery, seedPrivate, []string{lexeme}, []float64{1}, relatedViewer(who))
			if err != nil {
				t.Fatalf("run the keyword legs: %v", err)
			}
			keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				t.Fatalf("read the keyword legs: %v", err)
			}
			return keys
		}
		if got := answer(carol); !slices.Equal(got, want) {
			t.Fatalf("the keyword legs answer carol, who sees %s, %v; want its objects %v", seedPrivate, got, want)
		}
		if got := answer(alice); len(got) != 0 {
			t.Errorf("the keyword legs answer alice, who does not see %s, %v; want nothing", seedPrivate, got)
		}
	})
}
