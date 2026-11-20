package testdb

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/envoy/testdeadline"
)

// logDrop writes its measurement into the server's log: one line at LOG, which the server logs at
// its default log_min_messages, reading `testdb: drop database <name> took <ms> ms` with the name
// and the duration it was given. The names include one with a quote and a percent sign, which the
// RAISE's format would otherwise read as its own. The test reads each line back from the server,
// as the notice a connection with client_min_messages = log receives, and checks its text and its
// level: a line that is not sent, sent at a level the server does not log, or sent with other text
// would leave every CI job's log with no drop time and its sample unknown.
func TestLogDropLogsTheDropsNameAndTimeAtLOG(t *testing.T) {
	dsn := os.Getenv("LEGION_TEST_PG_DSN")
	if dsn == "" {
		t.Skip(SkipMessage)
	}
	ctx, cancel := testdeadline.Answered(t, context.Background())
	defer cancel()
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse LEGION_TEST_PG_DSN: %v", err)
	}
	config.RuntimeParams["client_min_messages"] = "log"
	var notices []*pgconn.Notice
	config.OnNotice = func(_ *pgconn.PgConn, notice *pgconn.Notice) { notices = append(notices, notice) }
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	for _, drop := range []struct {
		name string
		took time.Duration
		ms   string
	}{
		{"legion_test_0123456789abcdef", 1234567 * time.Microsecond, "1234.567"},
		{"a'quote and 100% of a name", 77243 * time.Millisecond, "77243.000"},
	} {
		notices = nil
		if err := logDrop(ctx, conn, drop.name, drop.took); err != nil {
			t.Errorf("log the drop of %q: %v", drop.name, err)
			continue
		}
		want := "testdb: drop database " + drop.name + " took " + drop.ms + " ms"
		if len(notices) != 1 || notices[0].SeverityUnlocalized != "LOG" || notices[0].Message != want {
			t.Errorf("the drop of %q, which took %s, sent the server %s; want one line at LOG: %q", drop.name, drop.took, described(notices), want)
		}
	}
}

// described is the level and text of each notice, for a failure's message.
func described(notices []*pgconn.Notice) string {
	if len(notices) == 0 {
		return "no line"
	}
	text := ""
	for i, notice := range notices {
		if i > 0 {
			text += ", "
		}
		text += fmt.Sprintf("%s %q", notice.SeverityUnlocalized, notice.Message)
	}
	return text
}
