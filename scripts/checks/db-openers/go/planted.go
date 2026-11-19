//go:build ignore

package main

import (
	"fmt"
	"regexp"
	"strings"
)

// plantedName is where the planted shapes stand, as one more file of the package the run names.
const plantedName = "scripts/checks/db-openers.planted.go"

// planted holds the planted shapes. A line marked `planted: <verdict>` must be listed with that
// verdict (judged, unjudged or wrapper), and a line marked neither must not be listed. Each blind
// spot of the program has its line: the forms a hand-kept list missed (ConnectWithOptions of pgx
// and of pgconn), a setting in a package whose other openers are judged (the plant stands in such
// a package, and only what the call passes judges it), a wrapper's caller, and each refusal that
// does not guard the opener after it (dbOpenersPlantedGuards). The file is one that only a build tag
// builds, so a run that leaves such a file out reads none of them.
const planted = `//go:build db_openers_planted

package main

import (
	"context"
	"database/sql"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/envoy/pgsetting"
)

const dbOpenersPlantedLocal = "postgres://127.0.0.1:5432/planted"

func dbOpenersPlantedOpen(ctx context.Context) {
	_, _ = pgx.Connect(ctx, os.Getenv("DATABASE_URL")) // planted: unjudged
	_, _ = pgx.ConnectWithOptions(ctx, os.Getenv("DATABASE_URL"), pgx.ParseConfigOptions{}) // planted: unjudged
	_, _ = pgconn.ConnectWithOptions(ctx, os.Getenv("DATABASE_URL"), pgconn.ParseConfigOptions{}) // planted: unjudged
	_, _ = pgconn.Connect(ctx, os.Getenv("DATABASE_URL")) // planted: unjudged
	_, _ = pgxpool.New(ctx, os.Getenv("DATABASE_URL")) // planted: unjudged
	_, _ = sql.Open("pgx", os.Getenv("DATABASE_URL")) // planted: unjudged
	parsed, _ := pgx.ParseConfig(os.Getenv("DATABASE_URL")) // planted: unjudged
	_, _ = pgx.ConnectConfig(ctx, parsed) // planted: judged
	_, _ = pgx.Connect(ctx, dbOpenersPlantedLocal) // planted: judged
	read, err := pgsetting.ParseConfig("DATABASE_URL", os.Getenv("DATABASE_URL"))
	if err == nil {
		_, _ = pgx.ConnectConfig(ctx, read) // planted: judged
	}
	raw := os.Getenv("DATABASE_URL")
	if pgsetting.Refuse("DATABASE_URL", raw) != nil {
		return
	}
	_, _ = pgconn.ParseConfig(raw) // planted: judged
	field := struct{ url string }{os.Getenv("DATABASE_URL")}
	_, _ = pgxpool.ParseConfig(field.url) // planted: unjudged
	dbOpenersPlantedWrapper(ctx, os.Getenv("DATABASE_URL")) // planted: unjudged
	dbOpenersPlantedWrapper(ctx, dbOpenersPlantedLocal) // planted: judged
	open := pgxpool.New // planted: unjudged
	_ = open
	_, _ = pgx.Connect(ctx, os.Getenv("DB_OPENERS_PLANTED_A_SETTING_WHOSE_NAME_RUNS_PAST_THE_120_BYTES_A_LINE_OF_THE_SEARCH_PRINTS")+os.Getenv("DATABASE_URL")) // planted: unjudged
}

func dbOpenersPlantedWrapper(ctx context.Context, url string) {
	_, _ = pgx.Connect(ctx, "postgres://"+url) // planted: wrapper
}

// dbOpenersPlantedGuards holds refusals of the formatter before an opener. A refusal guards the
// opener only when its error ends the function, it runs on every path to the opener, and the opener
// passes the very value it read, which no line writes between the two.
func dbOpenersPlantedGuards(ctx context.Context, check bool) error {
	byIf := os.Getenv("DATABASE_URL")
	if err := pgsetting.Refuse("DATABASE_URL", byIf); err != nil {
		return err
	}
	_, _ = pgx.Connect(ctx, byIf) // planted: judged
	byErr := os.Getenv("DATABASE_URL")
	_, err := pgsetting.ParseConfig("DATABASE_URL", byErr)
	if err != nil {
		return err
	}
	if check {
		_, _ = pgx.Connect(ctx, byErr) // planted: judged
	}
	settings := struct{ url, other string }{os.Getenv("DATABASE_URL"), os.Getenv("OTHER_DATABASE_URL")}
	if pgsetting.Refuse("DATABASE_URL", settings.url) != nil {
		return nil
	}
	settings.other = os.Getenv("OTHER_DATABASE_URL")
	_, _ = pgx.Connect(ctx, settings.url) // planted: judged
	dropped := os.Getenv("DATABASE_URL")
	_ = pgsetting.Refuse("DATABASE_URL", dropped)
	_, _ = pgx.Connect(ctx, dropped) // planted: unjudged
	logged := os.Getenv("DATABASE_URL")
	if err := pgsetting.Refuse("DATABASE_URL", logged); err != nil {
		_ = err.Error()
	}
	_, _ = pgx.Connect(ctx, logged) // planted: unjudged
	branch := os.Getenv("DATABASE_URL")
	if check {
		if err := pgsetting.Refuse("DATABASE_URL", branch); err != nil {
			return err
		}
	}
	_, _ = pgx.Connect(ctx, branch) // planted: unjudged
	again := os.Getenv("DATABASE_URL")
	if pgsetting.Refuse("DATABASE_URL", again) != nil {
		return nil
	}
	again = os.Getenv("OTHER_DATABASE_URL")
	_, _ = pgx.Connect(ctx, again) // planted: unjudged
	field := struct{ url string }{os.Getenv("DATABASE_URL")}
	if pgsetting.Refuse("DATABASE_URL", field.url) != nil {
		return nil
	}
	field = struct{ url string }{os.Getenv("OTHER_DATABASE_URL")}
	_, _ = pgx.Connect(ctx, field.url) // planted: unjudged
	looped := os.Getenv("DATABASE_URL")
	if pgsetting.Refuse("DATABASE_URL", looped) != nil {
		return nil
	}
	for range 2 {
		_, _ = pgx.Connect(ctx, looped) // planted: unjudged
		looped = os.Getenv("OTHER_DATABASE_URL")
	}
	aliased := os.Getenv("DATABASE_URL")
	alias := &aliased
	if pgsetting.Refuse("DATABASE_URL", aliased) != nil {
		return nil
	}
	*alias = os.Getenv("OTHER_DATABASE_URL")
	_, _ = pgx.Connect(ctx, aliased) // planted: unjudged
	closed := os.Getenv("DATABASE_URL")
	reread := func() { closed = os.Getenv("OTHER_DATABASE_URL") }
	if pgsetting.Refuse("DATABASE_URL", closed) != nil {
		return nil
	}
	reread()
	_, _ = pgx.Connect(ctx, closed) // planted: unjudged
	return nil
}
`

var plantedMark = regexp.MustCompile(`// planted: (judged|unjudged|wrapper)$`)

// plantedBlind is what the planted shapes gave that was not as marked, given the verdict and the
// arguments of each planted line the run listed. An argument is listed whole, as a judgement of the
// list names it: the text the line passes, which a ), a comma or the line's end follows (one planted
// line passes more than 120 bytes, the most a line of the search prints).
func plantedBlind(listed map[int]string, arguments map[int][]string) []string {
	var blind []string
	for i, line := range strings.Split(planted, "\n") {
		n := i + 1
		got, ok := listed[n]
		mark := plantedMark.FindStringSubmatch(line)
		switch {
		case mark != nil && !ok:
			blind = append(blind, fmt.Sprintf("the planted line %s:%d was not listed", plantedName, n))
		case mark != nil && got != mark[1]:
			blind = append(blind, fmt.Sprintf("the planted line %s:%d was listed %s, not %s", plantedName, n, got, mark[1]))
		case mark == nil && ok:
			blind = append(blind, fmt.Sprintf("the planted line %s:%d, which is not marked, was listed %s", plantedName, n, got))
		}
		code, _, _ := strings.Cut(line, " // planted:")
		code = strings.Join(strings.Fields(code), " ")
		for _, argument := range arguments[n] {
			at := strings.Index(code, argument)
			if argument == "" || at < 0 || (at+len(argument) < len(code) && !strings.ContainsRune("),", rune(code[at+len(argument)]))) {
				blind = append(blind, fmt.Sprintf("the planted line %s:%d was listed with the argument %q, which is not the whole text it passes", plantedName, n, argument))
			}
		}
	}
	return blind
}
