/**
 * The opener calls `scripts/checks/db-openers.ts` lists that pass a value it cannot judge by
 * reading, each with why that value cannot print a misread setting. An entry names the call's file,
 * the function that holds it and its argument's text, so it follows the call when lines above it
 * move; it judges each call of that function with that argument. An entry that names no call the
 * search lists fails the search, so a removed or changed call takes its entry with it.
 */

/** A listed call the search cannot judge by reading, with why. */
export interface JudgedOpener {
  readonly file: string;
  readonly function: string;
  readonly argument: string;
  readonly why: string;
}

/** The why of a test harness's opener: its URL is a test server's. */
const TEST_SERVER = (variable: string) =>
  `a test harness's opener, which no package but a test or another harness imports (go list): its URL is the test Postgres ${variable} names, or a database the harness made on it, a throwaway server CI and a developer's own run start`;

/** The why of Bun's SQL, whose own errors print nothing of the URL it is handed. */
const BUN_SQL = (source: string) =>
  `Bun's SQL (1.4.4, .bun-version's), handed ${source}: its own errors name no part of the URL, since it refuses a URL it cannot read as postgres://[REDACTED] and a failed connect as "Failed to connect" with its code alone; a server's refusal is the server's words, which name the user it refused, as pgx's do`;

/** The why of the research prototype's opener, whose module cannot import pgsetting. */
const PROTOTYPE =
  "the research prototype's module, which cannot import pgsetting (internal/pgdsn/pgdsn.go says why): pgdsn.Parse refuses the DSN by its length before this call, and a failed connect prints through pgdsn.ConnectFailure, with no user and no database";

const PROTO = "docs/research/temporal-evaluation-2026-11/prototype/internal/libx/libx.go";
const DAEMON_TEMPLATE = "packages/daemon/internal/testpg/template.go";
const DAEMON_TESTDB = "packages/daemon/internal/testpg/testdb/testdb.go";
const DAEMON_DSN = TEST_SERVER("LEGION_TEST_PG_DSN");

export const JUDGED: readonly JudgedOpener[] = [
  { file: PROTO, function: "EnsureDatabase", argument: "u.String()", why: PROTOTYPE },
  { file: PROTO, function: "Open", argument: "databaseURL", why: PROTOTYPE },
  { file: DAEMON_TEMPLATE, function: "Clone", argument: "dsn", why: DAEMON_DSN },
  { file: DAEMON_TEMPLATE, function: "Clone", argument: "shared.server", why: DAEMON_DSN },
  { file: DAEMON_TEMPLATE, function: "ClonePool", argument: "Clone(t, prefix)", why: DAEMON_DSN },
  { file: DAEMON_TEMPLATE, function: "prepare", argument: "adminURL", why: DAEMON_DSN },
  {
    file: "packages/daemon/internal/testpg/testdb/proxy.go",
    function: "Delayed",
    argument: "dsn",
    why: DAEMON_DSN,
  },
  { file: DAEMON_TESTDB, function: "Database", argument: "dsn", why: DAEMON_DSN },
  { file: DAEMON_TESTDB, function: "create", argument: "admin", why: DAEMON_DSN },
  { file: DAEMON_TESTDB, function: "rewriteDSN", argument: "rewritten", why: DAEMON_DSN },
  { file: DAEMON_TESTDB, function: "RunDatabaseMain", argument: "dsn", why: DAEMON_DSN },
  { file: DAEMON_TESTDB, function: "RunDatabaseFor", argument: "dsn", why: DAEMON_DSN },
  {
    file: "packages/envoy/internal/broker/store/storetest/storetest.go",
    function: "URL",
    argument: "raw",
    why: `${TEST_SERVER("BROKER_TEST_DATABASE_URL")}; the broker's test rig (brokertest and its requestseed command) is the one other importer`,
  },
  {
    file: "packages/envoy/internal/broker/store/storetest/storetest.go",
    function: "DatabaseURL",
    argument: "serverURL(t)",
    why: `${TEST_SERVER("BROKER_TEST_DATABASE_URL")}; the broker's test rig (brokertest and its requestseed command) is the one other importer`,
  },
  {
    file: "packages/envoy/internal/dispatch/benchtime/benchtime.go",
    function: "Open",
    argument: "databaseURL",
    why: TEST_SERVER(
      "DISPATCH_BENCH_DATABASE_URL, the seeded measurement database a benchmark is pointed at,"
    ),
  },
  {
    file: "packages/envoy/internal/dispatch/horizontest/horizontest.go",
    function: "QueryRow",
    argument: "string(d)",
    why: TEST_SERVER("DISPATCH_TEST_DATABASE_URL"),
  },
  {
    file: "packages/envoy/internal/dispatch/store/storetest/storetest.go",
    function: "prepare",
    argument: 'databaseURL(shared.server, "postgres")',
    why: TEST_SERVER("DISPATCH_TEST_DATABASE_URL"),
  },
  {
    file: "packages/envoy/internal/testpg/testpg.go",
    function: "databaseOn",
    argument: "server.String()",
    why: TEST_SERVER("DISPATCH_TEST_DATABASE_URL"),
  },
  {
    file: "packages/envoy-client/test/dispatch-stack.ts",
    function: "startDispatchStack",
    argument: "postgres",
    why: BUN_SQL("the test Postgres DISPATCH_TEST_DATABASE_URL names, in a test helper"),
  },
  {
    file: "scripts/dispatch/meaning-search-check.ts",
    function: "lexemes",
    argument: "target",
    why: BUN_SQL("the lexeme database the command line names"),
  },
  {
    file: "scripts/dispatch/meaning-search-check.ts",
    function: "lexemes",
    argument: '{ path: target, username: "postgres", database: "postgres" }',
    why: BUN_SQL("a socket directory the command line names, with no password"),
  },
  {
    file: "scripts/sessions/__tests__/message-wait.test.ts",
    function: "(top level)",
    argument: "SERVER",
    why: BUN_SQL("the test Postgres LEGION_TEST_PG_DSN names, in a test"),
  },
  {
    file: "scripts/sessions/__tests__/message-wait.test.ts",
    function: "(top level)",
    argument: "storeUrl.toString()",
    why: BUN_SQL("a database the test made on the test Postgres LEGION_TEST_PG_DSN names"),
  },
  {
    file: "scripts/sessions/message-wait.ts",
    function: "liveStore",
    argument: "dsn",
    why: BUN_SQL(
      "the session store's URL, read from the file SESSION_READER_DSN_FILE or --dsn-file names; the script prints no part of it either (a read that fails names the file's error code alone)"
    ),
  },
];
