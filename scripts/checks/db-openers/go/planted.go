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
// a package, and only what the call passes judges it), and a wrapper's caller.
const planted = `package main

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
}

func dbOpenersPlantedWrapper(ctx context.Context, url string) {
	_, _ = pgx.Connect(ctx, "postgres://"+url) // planted: wrapper
}
`

var plantedMark = regexp.MustCompile(`// planted: (judged|unjudged|wrapper)$`)

// plantedBlind is what the planted shapes gave that was not as marked, given the verdict of each
// planted line the run listed.
func plantedBlind(listed map[int]string) []string {
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
	}
	return blind
}
