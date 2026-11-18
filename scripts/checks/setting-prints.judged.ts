/**
 * The lines `scripts/checks/setting-prints.ts` finds in its scope that print a setting's value and
 * cannot print a user part of a URL, each with why. An entry names its file and a part of its
 * line's own text, so it follows the line when lines above it move; an entry that names no line the
 * search finds fails the search, so a fixed or moved line takes its entry with it.
 *
 * A judgement whose why rests on code other than its line (a guard that refuses the value before
 * the line runs, a function that names it, a table that admits it) names that code in `rests`:
 * each basis is a statement, by its file and a part of its first line, with the digest of its text
 * when it was judged (`setting-prints/bases.ts`). A basis the tree no longer holds, or one whose
 * text changed, fails the search, which prints the digest it has now, until the judgement is read
 * again and the new digest written. A judgement with no `rests` rests on its line's own text and on
 * what the setting it prints is (a path, a name, a number), which the search's own reading of the
 * data established, or on lines of the scope the search reads itself.
 */

/** What a judgement rests on: the statement whose first line holds `at` in `file`, whose text had
 *  the digest `digest` when it was judged. */
export interface Basis {
  readonly file: string;
  readonly at: string;
  readonly digest: string;
}

/** A line the search finds that cannot carry a user part, with why, and what that rests on. */
export interface Judged {
  readonly file: string;
  readonly text: string;
  readonly why: string;
  readonly rests?: readonly Basis[];
}

const PATH =
  "a path (a home, a config file, a checkout, a picture, a key command, a build or binary directory), which names no server";
const NAME =
  "a name, a key or an id (a project, a role, a session, a repository, an issue, a bucket, an image, an App), which no URL setting supplies";
const WORD = "a word of this command's own command line, quoted back to the person who typed it";
const REF =
  "a Dispatch link made from the issue key and the ids the ref parser matched, which hold no user part";
const CONFIG =
  "a refusal or notice of the config, which names a URL setting only by its length (unshownSettingUrl, through httpUrl of envoy-config.ts and dispatch-config.ts) and a file by its path";
const VARIABLES = "the names of the variables that are set, not their values";
const NUMBER =
  "the value of a port, count or duration setting, quoted only when it is not one; none of these settings is read as a URL";
const BOOT_REFUSAL =
  "a refusal of the boot, each of whose lines that writes a setting is judged here or names it through redact.URL";
const DISPATCH_BASE =
  "the Dispatch URL, which the config admits only when shownSettingUrl shows it (httpUrl, dispatch-config.ts), so it holds no user part and parses";
const ADMITTED_URL = (where: string) =>
  `a URL this file admits only when shownSettingUrl shows it (${where}), so it holds no user part and parses`;
const PLAYWRIGHT_BASE =
  "baseUrl: PLAYWRIGHT_BASE_URL, which baseUrlOf (api.ts) hands on only when shownSettingUrl shows it and it carries no user part, or the harness's own loopback origin";
/** The why of a parse of `path` against `base`, a URL admitted only when shownSettingUrl shows it.
 *  Bun prints the base in the error of a parse whose input fails, so the base's guard admits the
 *  parse only with what makes its path resolve: a / and then a word, which resolves against any
 *  URL that parses. */
const RESOLVED = (base: string, path: string) =>
  `${path}, a / and then a word, resolved against ${base}, which parses: such a path resolves against any URL that parses, so the parse cannot fail and no error of it prints the base`;
/** The same of a parse of `path` written after `base`, an http(s) URL that parses: a path of a /
 *  and percent-encoded segments, with a query of URLSearchParams, after it leaves it a URL that
 *  parses. */
const APPENDED = (base: string, path: string) =>
  `${path}, a / and then percent-encoded segments and a query URLSearchParams wrote, written after ${base}, an http(s) URL that parses: such a path after it leaves a URL that parses, so the parse cannot fail and no error of it prints the base`;

const DISPATCH = "packages/envoy/cmd/dispatch/";

const basis = (file: string, at: string, digest: string): Basis => ({ file, at, digest });
/** The bases the judgements below rest on. */
const RESTS = {
  hostAndPort: basis(
    "packages/broker-client/src/metadata-identity.ts",
    "const HOST_AND_PORT =",
    "eeebf40b3235"
  ),
  shownIssuer: basis(
    "packages/broker-client/src/person-identity.ts",
    "if (shownIssuer === undefined) {",
    "35095c58543e"
  ),
  tokenClient: basis(
    "packages/broker-client/src/token-client.ts",
    "the broker URL must be an http or https URL",
    "cb980260847d"
  ),
  baseUrlOf: basis("packages/dispatch/e2e/api.ts", "export function baseUrlOf(", "a85145f96f34"),
  liveProbe: basis(
    "packages/dispatch/e2e/delivery-live-probe.ts",
    "if (shownSettingUrl(rawUrl) === undefined) {",
    "c1d37daf73ef"
  ),
  fileStore: basis(
    "packages/dispatch/e2e/harness-pool.ts",
    "if (shownEndpoint === undefined) {",
    "9e93d32ced3c"
  ),
  databaseUrlRefusal: basis(
    "packages/dispatch/e2e/local-instance-support.ts",
    "export function databaseUrlRefusal(",
    "49bd219fcd6a"
  ),
  httpUrl: basis(
    "packages/envoy-client/src/envoy-config.ts",
    "export function httpUrl(",
    "64fac4d01852"
  ),
  httpUrlSchema: basis(
    "packages/envoy-client/src/envoy-config.ts",
    "const httpUrlSchema = z",
    "89e56807d28d"
  ),
  gatewayShown: basis(
    "packages/pi-legion/scripts/skill-scenarios/label-comments.ts",
    "const shown = shownSettingUrl(gateway);",
    "3692507804ee"
  ),
  gatewayRefused: basis(
    "packages/pi-legion/scripts/skill-scenarios/label-comments.ts",
    "--gateway must be an http or https URL",
    "849fdc0d41f4"
  ),
  daemon: basis(
    "packages/pi-shared/src/daemon-client.ts",
    "shownSettingUrl(baseUrl) === undefined",
    "7a14d8156067"
  ),
  claimantDispatch: basis(
    "scripts/e2e/dispatch-claimant-delivery.ts",
    "if (shownSettingUrl(dispatchUrl) === undefined) {",
    "29fe680e9712"
  ),
  claimantEnvoy: basis(
    "scripts/e2e/dispatch-claimant-delivery.ts",
    "if (shownSettingUrl(envoyUrl) === undefined) {",
    "f50de315082e"
  ),
  keyed: basis(
    "scripts/e2e/lib/dispatch-keyed-comment.ts",
    "if (shownSettingUrl(rawURL) === undefined) {",
    "6b3e24acb10a"
  ),
  live: basis(
    "scripts/e2e/lib/dispatch-live-events.ts",
    "if (shownSettingUrl(rawURL) === undefined) {",
    "6b3e24acb10a"
  ),
  verificationDispatch: basis(
    "scripts/e2e/verification-results.ts",
    "if (shownSettingUrl(dispatchUrl) === undefined) {",
    "29fe680e9712"
  ),
  verificationGitHub: basis(
    "scripts/e2e/verification-results.ts",
    "if (shownSettingUrl(githubUrl) === undefined) {",
    "f488e10c9657"
  ),
  bootMisread: basis(
    `${DISPATCH}main.go`,
    'if err := pgsetting.Refuse("DATABASE_URL", boot.DatabaseURL); err != nil {',
    "cba966fcef60"
  ),
  misread: basis("packages/envoy/pgsetting/pgsetting.go", "func misread(", "ff6b44be0650"),
  misreadNet: basis("packages/envoy/internal/redact/url.go", "func Misread(", "5c34564869f4"),
  loopback: basis(`${DISPATCH}main.go`, "func loopbackDatabase(", "654bd6fbf695"),
  mounted: basis(`${DISPATCH}main.go`, "func logDevSignInMounted(", "98a15d292b9a"),
  devOrigin: basis(
    "packages/envoy/internal/dispatch/routes/devsignin.go",
    "func DevSignInOrigin(",
    "6773c11bb213"
  ),
  serverURL: basis(
    "packages/envoy/internal/dispatch/config/config.go",
    "func dispatchServerURL(",
    "f83aec577c14"
  ),
  natsURLs: basis(
    "packages/envoy/internal/dispatch/config/config.go",
    "func natsURLs(",
    "452b7162809b"
  ),
  loadApp: basis(
    "packages/envoy/internal/dispatch/auth/app.go",
    "func LoadAppFromEnv(",
    "37afc265408d"
  ),
  remoteNATS: basis(
    "packages/envoy/internal/bus/reach.go",
    "func refuseRemoteNATS(",
    "0bf020039841"
  ),
  unreadableNATS: basis(
    "packages/envoy/internal/bus/reach.go",
    "func unreadableNATSURL(",
    "575e2ffe7f35"
  ),
  envoyConfigUrl: basis(
    "packages/envoy-client/src/envoy-settings.ts",
    "if (namedUrl !== undefined && httpUrl(namedUrl) === undefined) {",
    "d30dfeb0e11e"
  ),
  transportAttempt: basis(
    "packages/envoy-client/src/transport.ts",
    "const attemptOnce = async (",
    "0c71d3fde0d5"
  ),
  transportRequest: basis(
    "packages/envoy-client/src/transport.ts",
    "const request = async (",
    "feeb20898a23"
  ),
  transportRedirectError: basis(
    "packages/envoy-client/src/transport.ts",
    "export class EnvoyRedirectError extends Error {",
    "5b2eac9f6bcb"
  ),
  redirectTargetFn: basis(
    "packages/envoy-client/src/redirect.ts",
    "export function redirectTarget(",
    "5c2397e44c3b"
  ),
  transportUrlError: basis(
    "packages/envoy-client/src/transport.ts",
    "export class EnvoyUrlError extends Error {",
    "f167ef4c26e8"
  ),
  unshownSettingUrl: basis(
    "packages/contracts/src/setting-url.ts",
    "export function unshownSettingUrl(",
    "82212fe122e8"
  ),
};

/** Why the URL of an Envoy request prints no user part, and what that rests on. */
const TRANSPORT_URL =
  "the URL of a request to the Envoy listener, which every line of the transport names through shownSettingUrl (EnvoyApiError, EnvoyRedirectError, the refusal of a credential that could not be minted), whose base it refuses before any request when shownSettingUrl would not show it (EnvoyUrlError, named by unshownSettingUrl), and of which a refused redirect reads only the origin of a Location resolved against it, which holds no user part, naming none of it for the same origin (redirectTarget)";
const TRANSPORT_URL_RESTS = [
  RESTS.transportAttempt,
  RESTS.transportRequest,
  RESTS.transportRedirectError,
  RESTS.redirectTargetFn,
];

// biome-ignore-start lint/suspicious/noTemplateCurlyInString: each entry quotes a part of a source line, placeholders included
export const JUDGED: readonly Judged[] = [
  {
    file: "packages/broker-client/src/metadata-identity.ts",
    text: "return `http://${host}${IDENTITY_PATH}",
    why: "GCE_METADATA_HOST, admitted only as a host or an IP with an optional port (HOST_AND_PORT above), so it holds no user part",
    rests: [RESTS.hostAndPort],
  },
  {
    file: "packages/broker-client/src/person-identity.ts",
    text: "const config = await discovery(new URL(flow.issuer)",
    why: "the sign-in issuer, which discover parses only when read() calls it, after read() refused an issuer that shownSettingUrl does not show (shownIssuer) and one with a user part",
    rests: [RESTS.shownIssuer],
  },
  {
    file: "packages/broker-client/src/pool-identity.ts",
    text: "const mintPath = ",
    why: "the service account's email, which names no server",
  },
  {
    file: "packages/broker-client/src/token-client.ts",
    text: "new RegistrationPendingError(this.#session, pending)",
    why: NAME,
  },
  {
    file: "packages/claude-envoy/hooks/session-hook.ts",
    text: "for (const warning of warnings)",
    why: CONFIG,
    rests: [RESTS.httpUrl],
  },
  {
    file: "packages/claude-envoy/hooks/session-hook.ts",
    text: "summary unavailable: ${dispatch.error}",
    why: CONFIG,
    rests: [RESTS.httpUrl],
  },
  {
    file: "packages/claude-envoy/hooks/session-hook.ts",
    text: '"answers-refresh", session, cwd',
    why: PATH,
  },
  {
    file: "packages/claude-envoy/hooks/session-hook.ts",
    text: "dispatch = offWithError(refreshed.reason)",
    why: "a failed read of the answers, whose errors name the Dispatch URL through shownSettingUrl (dispatch-http.ts)",
  },
  {
    file: "packages/claude-envoy/hooks/session-hook.ts",
    text: "process.stderr.write(`envoy: ${notice}",
    why: CONFIG,
    rests: [RESTS.httpUrl],
  },
  {
    file: "packages/claude-envoy/hooks/session-hook.ts",
    text: "JSON.stringify({",
    why: "the session's answers as Dispatch wrote them, or a failed read of them, whose errors name the Dispatch URL through shownSettingUrl",
  },
  {
    file: "packages/contracts/src/legion-roles.ts",
    text: "return `legion-${project}-controller`",
    why: NAME,
  },
  {
    file: "packages/contracts/src/repo.ts",
    text: "return `${owner.trim().toLowerCase()}/${repo",
    why: NAME,
  },
  {
    file: "packages/dispatch/e2e/api.ts",
    text: "Origin: dashboardOrigin",
    why: "the origin of PLAYWRIGHT_BASE_URL, which api.ts admits only when shownSettingUrl shows it and it carries no user part (baseUrlOf), or the harness's loopback origin",
  },
  {
    file: "packages/dispatch/e2e/api.ts",
    text: "const url = new URL(path, options.baseUrl ?? baseUrl)",
    why: RESOLVED(
      "PLAYWRIGHT_BASE_URL, admitted by baseUrlOf only when shownSettingUrl shows it with no user part, or a stack's own loopback origin",
      "the route each caller of request writes in code (/api/…, /auth/…)"
    ),
    rests: [RESTS.baseUrlOf],
  },
  {
    file: "packages/dispatch/e2e/api.ts",
    text: "export const dashboardOrigin = new URL(baseUrl).origin",
    why: PLAYWRIGHT_BASE,
    rests: [RESTS.baseUrlOf],
  },
  {
    file: "packages/dispatch/e2e/api.ts",
    text: "const mint = new URL(devSignInPath(login), baseUrl)",
    why: RESOLVED(
      PLAYWRIGHT_BASE,
      "/auth/_dev/signin?login=<the login, percent-encoded> (devSignInPath)"
    ),
    rests: [RESTS.baseUrlOf],
  },
  ...['const liveUrl = new URL("/ws/live", options.url)', "fetch(new URL(route.route, base)"].map(
    (text) => ({
      file: "packages/dispatch/e2e/delivery-live-probe.ts",
      text,
      why: RESOLVED(
        "--url, which parseOptions admits only when shownSettingUrl shows it, and hands on as the URL it parsed then",
        "/ws/live, or a route of the probe's own reads (/api/v1/delivery/…)"
      ),
      rests: [RESTS.liveProbe],
    })
  ),
  {
    file: "packages/dispatch/e2e/harness-pool.ts",
    text: "const own = `${bucket}-worker-${index}`",
    why: NAME,
  },
  {
    file: "packages/dispatch/e2e/harness-pool.ts",
    text: "`the file store at ${endpoint} refused",
    why: "DISPATCH_E2E_FILE_STORE_ENDPOINT, which this file admits only when shownSettingUrl shows it as written, before any stack's bucket is made",
    rests: [RESTS.fileStore],
  },
  {
    file: "packages/dispatch/e2e/harness-postgres.ts",
    text: "name: `the Postgres binaries in ${bin}`",
    why: PATH,
  },
  {
    file: "packages/dispatch/e2e/harness-postgres.ts",
    text: "name: `the Postgres image ${image}`",
    why: NAME,
  },
  {
    file: "packages/dispatch/e2e/local-instance-support.ts",
    text: '`socket, not ${remote.join(", ")}',
    why: "the hosts of a DATABASE_URL, named only when no raw @ is left once its user part is taken out (the misread branch names the value by its length), so they are no part of a user part",
    rests: [RESTS.databaseUrlRefusal],
  },
  {
    file: "packages/dispatch/e2e/local-instance-support.ts",
    text: "return /^[A-Za-z0-9_@%+=:,./-]+$/.test(value) ? value",
    why: "shellQuote, which the banner hands only the dev container's URL that the source holds: a DATABASE_URL the person set and the broker switches are read from the shell",
  },
  {
    file: "packages/dispatch/e2e/local-instance-support.ts",
    text: ": `DATABASE_URL=${shellQuote(banner.databaseUrl)}`",
    why: "the dev container's URL, which the source holds, written only when DATABASE_URL did not name the database",
  },
  ...[
    "`  Open (signed in as alice): ${baseUrl}",
    "`  Sign in as bob:            ${baseUrl}",
    "`  Seeded broadcast:          ${baseUrl}",
    "`  Planner's live view:       ${baseUrl}",
    "DISPATCH_URL=${baseUrl} `",
  ].map((text) => ({
    file: "packages/dispatch/e2e/local-instance-support.ts",
    text,
    why: "the instance's own base URL: PLAYWRIGHT_BASE_URL, admitted by api.ts only when shownSettingUrl shows it with no user part, else http://127.0.0.1:<port>",
  })),
  {
    file: "packages/dispatch/e2e/local-instance-support.ts",
    text: "`  Database (truncated and reseeded on every start): ${database}",
    why: "a DATABASE_URL the person set through shownSettingUrl or by its length, or the dev container's URL the source holds",
  },
  ...["`  Fake Envoy: live sessions GET ${fakeEnvoy}", "`reseed them with PUT ${fakeEnvoy}"].map(
    (text) => ({
      file: "packages/dispatch/e2e/local-instance-support.ts",
      text,
      why: "the fake Envoy's origin, http://127.0.0.1:<port> with the port the harness reported",
    })
  ),
  ...['join(brokerDir, "read-token")', 'join(brokerDir, "exchange-token")'].map((text) => ({
    file: "packages/dispatch/e2e/read-token-live.ts",
    text,
    why: "the broker's URL, which the broker client admits only when shownSettingUrl shows it, and the token files' paths",
    rests: [RESTS.tokenClient],
  })),
  ...[
    "problems: [`unknown command ${quoteWord(command)}",
    'const current = quoteWord(String(args["issue"]))',
    "let reason = `a bare issue key is the --issue",
    "problems.push(`unexpected argument ${quoteWord(token)}: ${reason}`)",
    "problems.push(`unexpected argument ${quoteWord(token)}: every value follows its flag`)",
    "problems.push(`unknown flag ${flag}`)",
    "problems.push(`${flag} takes no value`);",
    "problems.push(`${flag} needs a value`)",
    '`${flag} - sends the one character "-".',
    "return BARE_WORD.test(value) ? value",
  ].map((text) => ({ file: "packages/envoy-client/src/dispatch-command.ts", text, why: WORD })),
  ...["return match ? `${canonicalRepo(", "const issue = `${repo}#${legionIssue}`"].map((text) => ({
    file: "packages/envoy-client/src/dispatch-commands/check.ts",
    text,
    why: NAME,
  })),
  ...[
    "return `${compact.slice(0, 8)}-",
    '? `@v${value}` : ""',
    "${key}/${item.kind}/${item.id}`",
    "${key}/artifact/spec${version}`",
    '${key}/spec${blockOf("v")}`',
    "${key}/artifact/decisions-md`",
    '${key}/${page === "conversation"',
    "${key}/artifact/${slug}${version}",
    "${key}/ask/${ask}",
    "${key}/comment/${comment}",
    "${key}/message/${message}",
    "${key}`;",
    "agent/${decodeURIComponent(session",
    "${project}/artifact/${decodedSlug}`",
    ": `${document}/${item.kind}/${item.id}`",
  ].map((text) => ({
    file: "packages/envoy-client/src/dispatch-commands/refs.ts",
    text,
    why: REF,
  })),
  ...[
    "`${checkout.path}: dispatch.serverUrl requires dispatch.token",
    "`${checkout.path}: dispatch.token requires dispatch.serverUrl",
    "`${config.path} is a link to ${link}, which does not exist`",
    'if (config.kind === "failed") return offWithError(config.error)',
    'if (checkoutFile?.kind === "invalid") return offWithError(checkoutFile.error)',
    "`${config.path} sets dispatch.enabled to false",
    "`${checkout.path} sets dispatch.enabled to false",
    "`${config.path} names no Dispatch server, and DISPATCH_URL is not set",
    "`${config.path}: dispatch is enabled and no token is",
    "`DISPATCH_URL is set and no token is",
    "throw new Error(`dispatch config: ${resolution.error}`)",
  ].map((text) => ({
    file: "packages/envoy-client/src/dispatch-config.ts",
    text,
    why: CONFIG,
    rests: [RESTS.httpUrl],
  })),
  {
    file: "packages/envoy-client/src/dispatch-http.ts",
    text: "`GET /api/v1/issues/${issueKey}/events?after=${after}",
    why: "an issue key and an event number, which name no server",
  },
  {
    file: "packages/envoy-client/src/dispatch-http.ts",
    text: "`repository must be owner/name, got ${JSON.stringify(repo)}`",
    why: NAME,
  },
  {
    file: "packages/envoy-client/src/dispatch-http.ts",
    text: '`${path.map((segment) => encodeURIComponent(segment)).join("/")}`',
    why: "the path of a request, each segment percent-encoded; the base it is parsed against is the admitted Dispatch URL",
  },
  {
    file: "packages/envoy-client/src/dispatch-http.ts",
    text: "`${this.#baseUrl}/`",
    why: DISPATCH_BASE,
    rests: [RESTS.httpUrl],
  },
  ...[
    "return `images: ${path} is not a file`",
    "return `images: ${path} cannot be read",
    "return `images: ${path} is ${size.toLocaleString",
    "return `images: ${path} is not ${PICTURE_TYPE_NAMES} picture",
  ].map((text) => ({
    file: "packages/envoy-client/src/dispatch-picture-tools.ts",
    text,
    why: PATH,
  })),
  ...[
    "throw new Error(`${name} is ${value}, which is not an absolute path`)",
    "`XDG_CONFIG_HOME is ${xdgConfigHome}, which is not an absolute path`",
    "if (!issue) return `${filePath}: invalid envoy.json`",
    "return `${filePath}: unrecognized ${section} key(s): ${keys}`",
    "reason: `${filePath}: could not be read (",
    "reason: `${filePath}: invalid JSON",
    "? `${filePath}: the top-level natsUrls is not read, and",
    ": `${filePath}: the top-level natsUrls is not read; move it",
    "throw new Error(`${filePath}: ${field} holds whitespace",
    "`${filePath}: ${field}'s command has not run in this process yet",
    "throw new Error(`${filePath}: ${field}'s command ${outcome.failure}`)",
    "return `${oldFile} is not read, and is on another filesystem",
    "return `${oldFile} is not read: ${move}`",
    "return `${old} is not read for this profile",
    "return `no envoy.json at ${filePath}${link}",
    "? `${checkoutPath}: envoy is not read from a checkout's envoy.json",
    "? `askgate in ${checkoutPath} is not read",
    "`mkdir -p ${path.dirname(checkoutPath)} && mv ${oldCheckout}",
  ].map((text) => ({ file: "packages/envoy-client/src/envoy-config.ts", text, why: PATH })),
  {
    file: "packages/envoy-client/src/envoy-config.ts",
    text: 'return `${filePath}: ${issue.path.join(".")}: ${issue.message}`',
    why: "a schema refusal of envoy.json: the file's path, the key, and the schema's words, which name a URL only by its length (httpUrlSchema's refine, unshownSettingUrl)",
    rests: [RESTS.httpUrlSchema],
  },
  {
    file: "packages/envoy-client/src/envoy-config.ts",
    text: 'result = Bun.spawnSync(["/bin/sh", "-c", command]',
    why: "the command envoy.json names for a secret, run as the person wrote it; it is not a URL and nothing prints it",
  },
  ...[
    "source: `NATS_NKEY_SEED_FILE (${file})`",
    "`ENVOY_NATS_URL names other servers than ${filePath}'s envoy.natsUrls",
    "source: `envoy.natsSeed in ${filePath}`",
  ].map((text) => ({ file: "packages/envoy-client/src/envoy-settings.ts", text, why: PATH })),
  ...[
    'if (config.kind === "failed") return offWithError(config.error)',
    "`${config.path} sets envoy.enabled to false",
    "? `${config.path} names no Envoy listener or NATS server",
  ].map((text) => ({
    file: "packages/envoy-client/src/envoy-settings.ts",
    text,
    why: CONFIG,
    rests: [RESTS.httpUrl],
  })),
  {
    file: "packages/envoy-client/src/redirect.ts",
    text: ": `redirecting to ${target.origin}`",
    why: "the origin a redirect's Location names, on another origin than the request's: the answering server chose it, and a Location on the setting's own origin is the same-origin case, which names none",
  },
  ...[
    "`${SESSION_ANSWERS_MARKER}\\nAs read at ${options.cached.readAt}",
    "return /[.?!]$/.test(text) ? text : `${text}.`",
    "return `${FAILURE_MARKER}${reason.replace(",
  ].map((text) => ({
    file: "packages/envoy-client/src/session-answers.ts",
    text,
    why: "a failed read of the answers, whose errors name the Dispatch URL through shownSettingUrl",
  })),
  {
    file: "packages/envoy-client/src/transport.ts",
    text: "const url = `${baseUrl}${path}`;",
    why: TRANSPORT_URL,
    rests: TRANSPORT_URL_RESTS,
  },
  {
    file: "packages/envoy-client/src/transport.ts",
    text: "throw new EnvoyUrlError(baseUrl)",
    why: "the refusal of an Envoy URL shownSettingUrl does not show, before any request: EnvoyUrlError names the URL by its length and the cause its check found (unshownSettingUrl), never a part of it",
    rests: [RESTS.transportUrlError, RESTS.unshownSettingUrl],
  },
  {
    file: "packages/envoy-client/src/transport.ts",
    text: "`/v1/sessions/${encodeURIComponent(input.sessionID)}${query}`",
    why: `a session id, percent-encoded as a path segment of a request's URL: ${TRANSPORT_URL}`,
    rests: TRANSPORT_URL_RESTS,
  },
  {
    file: "packages/claude-envoy/bin/envoy-send.ts",
    text: "throw new Error(`Envoy is off: ${envoy.error ?? envoy.notice}`)",
    why: CONFIG,
    rests: [RESTS.envoyConfigUrl, RESTS.httpUrlSchema],
  },
  {
    file: "packages/envoy-client/src/transport.ts",
    text: "body: JSON.stringify(body),",
    why: "the body of a request to the Envoy listener (a machine, a session, a project directory), which no URL setting supplies",
  },
  {
    file: "packages/pi-envoy/extensions/askgate/askgate-core.ts",
    text: 'if (file.flag === "--path") uploadText = bytes.toString("utf8")',
    why: "the text of a file the person named to upload, which is its own content and no setting",
  },
  ...[
    'const child = Bun.spawn([file], { stdout: "pipe"',
    "`the key command ${file} exited ${code}`",
    "`the key command ${file} printed no key`",
  ].map((text) => ({
    file: "packages/pi-legion/scripts/skill-scenarios/label-comments.ts",
    text,
    why: PATH,
  })),
  {
    file: "packages/pi-legion/scripts/skill-scenarios/label-comments.ts",
    text: "redirectTarget(response, `${route.url}/v1/messages`)",
    why: ADMITTED_URL("--gateway, label-comments.ts's parse of its arguments"),
    rests: [RESTS.gatewayShown, RESTS.gatewayRefused],
  },
  ...[
    "return `unknown argument ${flag}. ${USAGE}`",
    "if (value === undefined",
    "return `${flag} is given twice`",
  ].map((text) => ({
    file: "packages/pi-legion/scripts/skill-scenarios/label-comments.ts",
    text,
    why: WORD,
  })),
  {
    file: "packages/pi-legion/scripts/skill-scenarios/production-dispatch.ts",
    text: "`Dispatch is not configured: ${resolution.error ?? resolution.notice}`",
    why: CONFIG,
    rests: [RESTS.httpUrl],
  },
  {
    file: "packages/pi-legion/scripts/skill-scenarios/production-dispatch.ts",
    text: "const url = new URL(`${this.#base}${shown}`)",
    why: APPENDED(
      "the Dispatch URL, which the config admits only when shownSettingUrl shows it (httpUrl, dispatch-config.ts)",
      "the path and query describe wrote"
    ),
    rests: [RESTS.httpUrl],
  },
  {
    file: "packages/pi-legion/scripts/skill-scenarios/seed.ts",
    text: "const url = new URL(route, baseUrl)",
    why: RESOLVED(PLAYWRIGHT_BASE, "the route each call of agentFetch writes (/api/v1/…)"),
    rests: [RESTS.baseUrlOf],
  },
  ...[
    "`LEGION_PROJECT ${project} names controller role ${controllerToken(project)}, but the daemon at",
    "but the daemon registered this c",
  ].map((text) => ({
    file: "packages/pi-legion/src/controller-session.ts",
    text,
    why: "the project's name and the daemon's URL through shownSettingUrl",
  })),
  {
    file: "packages/pi-shared/src/daemon-client.ts",
    text: "redirectTarget(response, `${endpoint}${path}`)",
    why: ADMITTED_URL(
      "LEGION_DAEMON_URL, refused before any call when shownSettingUrl does not show it"
    ),
    rests: [RESTS.daemon],
  },
  ...["`./${row.file}`,", "const shown = `${row.site}: ${row.directory}/${row.file}`"].map(
    (text) => ({
      file: "scripts/checks/redirect-refusals.ts",
      text,
      why: "a row of the check's own list: a site's file and symbol and its test file's path",
    })
  ),
  {
    file: "scripts/checks/setting-prints/program.ts",
    text: 'Bun.spawnSync(["git", ...args]',
    why: "the git commands this search runs to read its scope, handed the revision --base names; nothing prints them",
  },
  ...[
    "fetch(new URL(path, dispatchUrl)",
    "new URL(`/__fixture/sessions/",
    'new URL("/__fixture/sends", envoyUrl)',
    'new URL("/__fixture/sessions", envoyUrl)',
  ].map((text) => ({
    file: "scripts/e2e/dispatch-claimant-delivery.ts",
    text,
    why: RESOLVED(
      "<dispatch-url> or <fake-envoy-url>, each refused at the driver's top level, before any of its functions runs, when shownSettingUrl does not show it",
      "a route this driver writes (/api/…, /auth/…, /__fixture/…)"
    ),
    rests: [RESTS.claimantDispatch, RESTS.claimantEnvoy],
  })),
  ...[
    'await fetch(new URL("/healthz", base))',
    "new URL(`/api/v1/issues/${encodeURIComponent(options.issue)}/comments`, options.url),",
  ].map((text) => ({
    file: "scripts/e2e/lib/dispatch-keyed-comment.ts",
    text,
    why: RESOLVED(
      "--url, refused at its parse when shownSettingUrl does not show it",
      "/healthz, or the comments route of the issue, its key percent-encoded"
    ),
    rests: [RESTS.keyed],
  })),
  {
    file: "scripts/e2e/lib/dispatch-keyed-comment.ts",
    text: '[process.execPath, CLI, "comment", "--issue", options.issue, "--body", body]',
    why: "the issue key and the comment this script hands the dispatch command, no URL",
  },
  ...[
    'await fetch(new URL("/auth/whoami", options.url)',
    'const socketURL = new URL("/ws/live", options.url)',
  ].map((text) => ({
    file: "scripts/e2e/lib/dispatch-live-events.ts",
    text,
    why: RESOLVED(
      "--url, refused at its parse when shownSettingUrl does not show it",
      "/auth/whoami or /ws/live"
    ),
    rests: [RESTS.live],
  })),
  {
    file: "scripts/e2e/verification-results.ts",
    text: "const child = Bun.spawn([process.execPath, dispatchCommand, ...argv]",
    why: "the dispatch command's path and its arguments, which this driver writes, no URL",
  },
  ...["fetch(new URL(path, dispatchUrl)", 'new URL("/__fixture/repos", githubUrl)'].map((text) => ({
    file: "scripts/e2e/verification-results.ts",
    text,
    why: RESOLVED(
      "<dispatch-url> or <fake-github-url>, each refused at the driver's top level, before any of its functions runs, when shownSettingUrl does not show it",
      "a route this driver writes (/api/…, /auth/…, /__fixture/repos)"
    ),
    rests: [RESTS.verificationDispatch, RESTS.verificationGitHub],
  })),
  {
    file: "scripts/e2e/verification-results.ts",
    text: '["psql", databaseUrl, "-X"',
    why: "the rig's own throwaway database, handed to psql as the connection it opens; nothing prints it",
  },
  {
    file: "scripts/e2e/verification-results.ts",
    text: "`setup: person ${person}; session ${session}",
    why: NAME,
  },
  {
    file: `${DISPATCH}answerer.go`,
    text: "set, but neither DISPATCH_EMBEDDER nor DISPATCH_ANSWERER is azure",
    why: VARIABLES,
  },
  {
    file: `${DISPATCH}embedder.go`,
    text: "the keyless Azure sign-in needs %s: %s set, %s missing",
    why: VARIABLES,
  },
  {
    file: `${DISPATCH}embedder.go`,
    text: "DISPATCH_EMBEDDER=azure takes one credential, and both are set",
    why: VARIABLES,
  },
  {
    file: `${DISPATCH}main.go`,
    text: 'slog.Error("dispatch: resolve boot config", "error", err)',
    why: BOOT_REFUSAL,
  },
  {
    file: `${DISPATCH}main.go`,
    text: 'slog.Error("dispatch: load envoy config", "error", err)',
    why: "config.Load's refusals (internal/dispatch/config): a file by its path, a key, DISPATCH_SERVER_URL through redact.URL and NATS_URLS by its length",
    rests: [RESTS.serverURL, RESTS.natsURLs],
  },
  {
    file: `${DISPATCH}main.go`,
    text: 'slog.Error("dispatch: load app credentials", "error", err)',
    why: "the App's refusals (internal/dispatch/auth, LoadAppFromEnv), which name the variable and quote at most an App ID that is no integer, never the key",
    rests: [RESTS.loadApp],
  },
  {
    file: `${DISPATCH}main.go`,
    text: 'slog.Error("dispatch: dev sign-in", "error", err)',
    why: BOOT_REFUSAL,
  },
  {
    file: `${DISPATCH}main.go`,
    text: 'slog.Info("dispatch: loaded github app", "slug"',
    why: NAME,
  },
  {
    file: `${DISPATCH}main.go`,
    text: 'slog.Error("dispatch: connect the agent conversation relay", "error", err)',
    why: "bus.Dial's refusals, which name the variables, a NATS URL with an @ only by its length, and one with no @, which has no user part, in net/url's own error (internal/bus reach.go, refuseRemoteNATS and unreadableNATSURL, and socks5.go, held by their tests)",
    rests: [RESTS.remoteNATS, RESTS.unreadableNATS],
  },
  {
    file: `${DISPATCH}main.go`,
    text: 'slog.Error("dispatch: resolve web dist dir", "error", err)',
    why: PATH,
  },
  {
    file: `${DISPATCH}main.go`,
    text: 'slog.Info("dispatch: storing uploaded files in a bucket"',
    why: NAME,
  },
  {
    file: `${DISPATCH}main.go`,
    text: 'slog.Warn("dispatch: using test/local header identity"',
    why: "the name of the header identity reads",
  },
  {
    file: `${DISPATCH}main.go`,
    text: 'slog.Error("dispatch: build app context", "error", err)',
    why: "routes.BuildAppContext's refusals, which name the dashboard origin through redact.URL (routes.DevSignInOrigin)",
    rests: [RESTS.devOrigin],
  },
  ...[
    'slog.Info("dispatch: redelivering the GitHub App',
    'slog.Info("dispatch: reconciling the CI records',
  ].map((text) => ({
    file: `${DISPATCH}main.go`,
    text,
    why: "the GitHub App's client ID, which names no server",
  })),
  {
    file: `${DISPATCH}main.go`,
    text: 'slog.Error("dispatch: listen", "addr", boot.ListenAddr',
    why: "the address this server listens on, a host and a port that listenAddress joined",
  },
  {
    file: `${DISPATCH}main.go`,
    text: 'fmt.Errorf("load app from env: %w", err)',
    why: "auth.LoadAppFromEnv's refusal, which names the variable and quotes at most an App ID that is no integer, never the key",
    rests: [RESTS.loadApp],
  },
  {
    file: `${DISPATCH}main.go`,
    text: "DISPATCH_IDENTITY=%q (expected cookie or header:<Header-Name>)",
    why: "the identity mode, quoted only when it is neither cookie nor header:<name>; it is not read as a URL",
  },
  ...[
    'is for a loopback server only: %s, must be on 127.0.0.1 or [::1]"',
    "is for a loopback server only: DISPATCH_LISTEN_HOST=%q",
  ].map((text) => ({
    file: `${DISPATCH}main.go`,
    text,
    why: "the listen host and the address it is bound to, a host and a port",
  })),
  {
    file: `${DISPATCH}main.go`,
    text: '"issuer", redact.URL(boot.OIDCIssuer), "audience"',
    why: "the audience beside the issuer, which the line names through redact.URL; the search names the audience by the issuer's variable since one call answers both",
  },
  {
    file: `${DISPATCH}main.go`,
    text: 'slog.Warn("dispatch: dev sign-in mounted',
    why: "the host and port routes.DevSignInOrigin reads, after the dev sign-in fence admitted a loopback origin, and none of an origin net/url misreads, an @ left once it takes out its user part among them (redact.Misread)",
    rests: [RESTS.mounted, RESTS.misreadNet, RESTS.devOrigin],
  },
  {
    file: `${DISPATCH}main.go`,
    text: "serves a loopback database only: DATABASE_URL names host %q",
    why: "a host pgx read from DATABASE_URL, named only after resolveBootConfig refused a value whose user part pgx reads apart (pgsetting.Refuse: more than one @ before the first /, an @ after the first / with none before it, or an @ before the first / and another in the database name), so pgx ends the user part at its one @ before the first /, before that host, and keeps a ?, # or \\ before that @ in the user part; a password that holds an @ and, after it, a / is refused, since the host pgx would read from it can be a piece of that password",
    rests: [RESTS.bootMisread, RESTS.misread, RESTS.loopback],
  },
  {
    file: `${DISPATCH}main.go`,
    text: 'slog.Info("dispatch: no dashboard build to publish',
    why: PATH,
  },
  {
    file: `${DISPATCH}main.go`,
    text: 'slog.Warn(warning.Message, "path", warning.Path, "fix", warning.Fix)',
    why: "a warning about envoy.json, which names the file, the key and what to change, never a value",
  },
  ...[
    'fmt.Errorf("query DISPATCH_DEFAULT_PROJECT %q: %w"',
    'fmt.Errorf("DISPATCH_DEFAULT_PROJECT %q does not exist"',
  ].map((text) => ({
    file: `${DISPATCH}main.go`,
    text,
    why: "a project's key, which names no server",
  })),
  ...[
    "want a port number 1-65535",
    "(expected a positive integer)",
    "(expected a positive Go duration)",
  ].map((text) => ({
    file: `${DISPATCH}main.go`,
    text,
    why: NUMBER,
  })),
  ...[
    "which could not be read, and %s is not read in its place",
    "which is empty, and %s is not read in its place",
  ].map((text) => ({
    file: `${DISPATCH}settings.go`,
    text,
    why: "the path a _FILE variable names, and the variables' names",
  })),
  ...[
    'return strings.Join(names, ", ") + " and one of "',
    "must be unset because header identity and Google sign-in cannot share a deployment",
    "set, DISPATCH_SIGNIN_GROUP and DISPATCH_SIGNIN_DOMAIN missing",
    "Google sign-in needs %s: %s set, %s missing",
  ].map((text) => ({ file: `${DISPATCH}signin.go`, text, why: VARIABLES })),
  {
    file: "packages/envoy/internal/dispatch/routes/router.go",
    text: 'fmt.Errorf("BuildAppContext: %w", err)',
    why: "routes.DevSignInOrigin's refusal, which names the origin through redact.URL",
    rests: [RESTS.devOrigin],
  },
];
// biome-ignore-end lint/suspicious/noTemplateCurlyInString: the quoted source lines end here.
