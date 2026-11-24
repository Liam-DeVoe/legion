import { writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { parseArgs } from "node:util";
import { readContract } from "./name-map.ts";
import { newestEventId, PROJECT_KEY } from "./state.ts";

/**
 * The seed of check k6 on a local Dispatch (`bun run dispatch:local`): every item k6 lists, the
 * stub daemons below, at and above the contract this checkout records and one whose state has no
 * `contract` key, and the items the fixtures of `__fixtures__/word-rewrite/capture.sh` are
 * captured from. It writes through the routes `packages/dispatch/e2e/api.ts` wraps, with plain
 * `fetch`: as a session (the local harness's agent bearer, `e2e-token`, naming a session in
 * `actor`), as the person alice (the dev sign-in's cookie and the dashboard's `Origin`), and as an
 * agent acting through alice's personal token.
 *
 * It prints one JSON object (and writes it to `--out` when named): every key, id and address the
 * runs and the capture need. Then it keeps the stub daemons serving until it is stopped.
 *
 * The seeded texts hold the word: they are the test input of the tool that rewrites it.
 *
 *   bun scripts/dispatch/word-rewrite/local-seed.ts --url http://127.0.0.1:<port> [--out <file>]
 */

const { values } = parseArgs({
  args: Bun.argv.slice(2),
  options: {
    url: { type: "string" },
    out: { type: "string" },
    token: { type: "string" },
  },
  strict: true,
});
if (values.url === undefined) {
  console.error(
    "usage: local-seed.ts --url <local Dispatch base URL> [--out <file>] [--token <t>]"
  );
  process.exit(2);
}
const base = values.url.replace(/\/+$/, "");
const agentToken = values.token ?? "e2e-token";
const session = { kind: "session", id: crypto.randomUUID() };
const contract = readContract(resolve(import.meta.dir, "../../.."));

type Caller = "agent" | "alice" | { readonly bearer: string };

let aliceCookie: string | undefined;

async function cookieOf(login: string): Promise<string> {
  const response = await fetch(`${base}/auth/_dev/signin?login=${login}`, { redirect: "manual" });
  const cookie = response.headers
    .getSetCookie()
    .find((header) => header.startsWith("dsession="))
    ?.split(";")[0];
  if (response.status !== 302 || cookie === undefined) {
    throw new Error(`dev sign-in as ${login} answered ${response.status}`);
  }
  return cookie;
}

async function call(method: string, path: string, body: object | undefined, as: Caller) {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  let payload = body;
  if (as === "alice") {
    aliceCookie ??= await cookieOf("alice");
    headers.Cookie = aliceCookie;
    headers.Origin = new URL(base).origin;
  } else {
    // Dispatch refuses a bearer's write that names no session (`ACTOR_KIND`), the shared token's
    // and a personal agent token's alike.
    headers.Authorization = `Bearer ${as === "agent" ? agentToken : as.bearer}`;
    payload = body === undefined ? undefined : { ...body, actor: session };
  }
  const response = await fetch(`${base}${path}`, {
    method,
    headers,
    body: payload === undefined ? undefined : JSON.stringify(payload),
  });
  const text = await response.text();
  if (!response.ok) throw new Error(`${method} ${path} answered ${response.status}: ${text}`);
  return text === "" ? null : JSON.parse(text);
}

async function project(key: string): Promise<void> {
  try {
    await call("POST", "/api/v1/projects", { key, name: `Word rewrite ${key}` }, "alice");
  } catch (error) {
    if (!(error instanceof Error && error.message.includes(" 409"))) throw error;
  }
}

async function issue(title: string, spec: string, parent?: string) {
  const body = { project: "SEED", title, spec, force: true, ...(parent ? { parent } : {}) };
  const created = await call("POST", "/api/v1/issues", body, "agent");
  return { key: String(created.key), spec: String(created.primary_artifact_id) };
}

async function upload(owner: string, name: string, content: string, as: Caller = "agent") {
  const path = PROJECT_KEY.test(owner)
    ? `/api/v1/projects/${owner}/artifacts`
    : `/api/v1/issues/${owner}/artifacts`;
  const uploaded = await call("POST", path, { name, content }, as);
  return String(uploaded.artifact.id);
}

/** One edit of a document, with one op, as `as`. */
async function edit(id: string, op: object, as: Caller) {
  await call("POST", `/api/v1/artifacts/${id}/edits`, { ops: [op] }, as);
}

const insert = (id: string, markdown: string, as: Caller = "agent") =>
  edit(id, { op: "insert", markdown, after: "end" }, as);

const replace = (id: string, find: string, replacement: string, as: Caller) =>
  edit(id, { op: "replace", find, with: replacement }, as);

const approve = (id: string) =>
  call("POST", `/api/v1/artifacts/${id}/reviews`, { state: "approved" }, "alice");

const close = (key: string) => call("PATCH", `/api/v1/issues/${key}`, { status: "done" }, "agent");

/** A stub daemon whose state answers `state`. */
function stub(state: object) {
  return Bun.serve({
    hostname: "127.0.0.1",
    port: 0,
    fetch(request) {
      const path = new URL(request.url).pathname;
      if (path === "/legion/v1/state") return Response.json(state);
      if (path === "/healthz") return Response.json({ status: "ok" });
      return new Response("not found", { status: 404 });
    },
  });
}

const daemonState = (daemon: number) => ({
  contract: { daemon, caller: daemon },
  admission: { held: [] },
});
const stubs = {
  at: stub(daemonState(contract)),
  below: stub(daemonState(contract - 1)),
  above: stub(daemonState(contract + 1)),
  noContract: stub({ admission: { held: [] } }),
};
const address = (server: { port?: number | undefined }) => `http://127.0.0.1:${server.port}`;

for (const key of ["SEED", "BELOW", "ABOVE", "NOKEY", "NONE", "NOADDR", "GIVEN"])
  await project(key);
const registered: Record<string, string> = {
  SEED: address(stubs.at),
  BELOW: address(stubs.below),
  ABOVE: address(stubs.above),
  NOKEY: address(stubs.noContract),
};
for (const [key, stateUrl] of Object.entries(registered)) {
  await call("PUT", `/api/v1/projects/${key}/legion/daemon`, { state_url: stateUrl }, "alice");
}

// The tree the run names (`--tree`), with a child; the root is closed once its text is written.
const tree = await issue(
  "Tree of the word rewrite",
  "The word parked is the subject of this tree."
);
const child = await issue(
  "Rename the parked names",
  "Each parked name gets its own words.",
  tree.key
);

// The open issue: an agent's title and spec that use the word for a status, and its documents.
const main = await issue(
  "Parked until the release lands",
  "This issue is parked until the release lands.\n\nThe second paragraph names nothing of it.\n"
);
const people = await upload(
  main.key,
  "people.md",
  "A session wrote this paragraph about the parked tree.\n\nA person will change this paragraph: the parked issue waits.\n"
);
await insert(people, "Alice wrote this paragraph: we parked it for now.", "alice");
await replace(people, "A person will change", "A person changed", "alice");
const mixed = await upload(
  main.key,
  "mixed.md",
  "Café ✓ — the parked tree waits for the release.\n\n" +
    ':::ask{#ask-word urgency="med" multiple="false"}\nShould the parked issue move to backlog?\n\n- Yes\n- No\n:::\n\n' +
    ':::check{#check-word surface="cli" run="go test -run TestParkedDecision ./internal/daemon/"}\nThe parked tree resumes.\n:::\n'
);
await insert(mixed, "Alice added this: the parked tree is hers.", "alice");
const personal = await call(
  "POST",
  "/api/v1/me/agent-tokens",
  { name: "word-rewrite seed" },
  "alice"
);
const personalToken = String(personal.token);
const act = await upload(main.key, "act.md", "Plain text that names nothing.\n");
await insert(act, "An agent for Alice parked this.", { bearer: personalToken });
const quotes = await upload(
  main.key,
  "quotes.md",
  'Sami wrote: "park it until the release".\n\nThe rule says never to write "parked" for a status.\n'
);
const kernel = await upload(
  main.key,
  "kernel.md",
  "The fix touches kthread_park in the scheduler.\n\nA parked kernel thread does not run until it is woken.\n"
);
const notice = await upload(
  main.key,
  "notice.md",
  "The controller reads the parked-decision notice at its next wake.\n"
);
const priorities = await upload(
  main.key,
  "priorities.md",
  "The rail reads priority_work_parked from the priorities answer.\n"
);
const verification = await upload(
  main.key,
  "verification.md",
  ':::check{#k1 surface="cli" run="go test -run TestBootReleasesParkedClaims ./internal/daemon/" when="before_merge"}\nThe parked tree resumes once its root leaves the backlog.\n:::\n'
);
const slugged = await upload(main.key, "parked-notes.md", "Notes on the release.\n");
const race = await upload(main.key, "race.md", "The parked branch waits for its review.\n");

// A spec and a Verification document a person approved at their latest version.
const approved = await issue(
  "Approved plan",
  "The approved plan keeps the parked tree as it is.\n"
);
const approvedVerification = await upload(
  approved.key,
  "verification.md",
  "The approved checks name the parked tree.\n"
);
await approve(approved.spec);
await approve(approvedVerification);

// A document a person approved at an earlier version, which now holds an open ask block.
const earlier = await issue("Approved earlier", "Specification.\n");
const earlierDocument = await upload(earlier.key, "earlier.md", "The parked plan, as approved.\n");
await approve(earlierDocument);
await insert(
  earlierDocument,
  ':::ask{#may-move urgency="med" multiple="false"}\nMay this plan move to backlog?\n\n- Yes\n- No\n:::\n'
);

// A document an open approval request names.
const requested = await issue("Requested", "The requested spec says the issue was parked.\n");
await call(
  "POST",
  `/api/v1/artifacts/${requested.spec}/approval-requests`,
  { summary: "Approve the spec." },
  "agent"
);

// A document a design gate names, with no approval: the tool changes it (spec version 18).
const gated = await issue("Gated", "The gated spec was parked by the controller.\n");
await call(
  "PUT",
  `/api/v1/issues/${gated.key}/design-gates`,
  { gates: [{ step: "spec", artifact_id: gated.spec, holds: true }] },
  "agent"
);

// A closed issue whose title and document hold the word.
const closed = await issue("Parked and closed", "The closed issue was parked.\n");
const closedDocument = await upload(closed.key, "notes.md", "The closed notes say parked.\n");

// The project documents: SEED's own, and the notice kind in each project of the daemon rows.
const seedNotes = await upload(
  "SEED",
  "notes.md",
  "The release notes say the issue was parked until Monday.\n"
);
const notices: Record<string, string> = {};
for (const key of ["BELOW", "ABOVE", "NOKEY", "NONE", "NOADDR", "GIVEN"]) {
  notices[key] = await upload(
    key,
    "notice.md",
    "The controller reads the parked-decision notice at its next wake.\n"
  );
}

// The items the capture writes to, which hold no trace of the word.
const capture = await issue("Capture", "Capture.\n");
const captureDocument = await upload(capture.key, "capture.md", "Capture text.\n");

await close(closed.key);
await close(tree.key);

// The last seed step, which the fixture event-log.json reads: an upload, an edit and a title.
const log = await issue("Log steps", "Log steps.\n");
const logDocument = await upload(log.key, "log-a.md", "First.\n");
const logAfter = await newestEventId({
  eventLog: (after, limit) =>
    call("GET", `/api/v1/events/log?after=${after}&limit=${limit}`, undefined, "agent"),
});
const logUpload = await upload(log.key, "log-b.md", "Uploaded after the cursor.\n");
await insert(logDocument, "Second.");
await call("PATCH", `/api/v1/issues/${log.key}`, { title: "Log steps, renamed" }, "agent");

const seeded = {
  base,
  contract,
  session: session.id,
  personal_token: personalToken,
  stubs: {
    at: address(stubs.at),
    below: address(stubs.below),
    above: address(stubs.above),
    no_contract: address(stubs.noContract),
  },
  issues: {
    tree: tree.key,
    child: child.key,
    main: main.key,
    approved: approved.key,
    earlier: earlier.key,
    requested: requested.key,
    gated: gated.key,
    closed: closed.key,
    capture: capture.key,
    log: log.key,
  },
  documents: {
    main_spec: main.spec,
    people,
    mixed,
    act,
    quotes,
    kernel,
    notice,
    priorities,
    verification,
    slugged,
    race,
    approved_spec: approved.spec,
    approved_verification: approvedVerification,
    earlier: earlierDocument,
    requested_spec: requested.spec,
    gated_spec: gated.spec,
    closed: closedDocument,
    seed_notes: seedNotes,
    notices,
    capture: captureDocument,
    log_a: logDocument,
    log_b: logUpload,
  },
  log_after: logAfter,
  run_flags: [
    "--tree",
    tree.key,
    "--include-closed",
    "--daemon-state",
    `GIVEN=${address(stubs.at)}`,
    "--no-daemon",
    "NONE",
    "--state-document",
    "dispatch://SEED/artifact/rewrite-state-md",
  ],
};
const printed = JSON.stringify(seeded, null, 2);
console.log(printed);
if (values.out !== undefined) writeFileSync(values.out, `${printed}\n`);

// The stub daemons serve until the seed is stopped.
for (const signal of ["SIGINT", "SIGTERM"] as const) {
  process.on(signal, () => {
    for (const server of Object.values(stubs)) server.stop(true);
    process.exit(0);
  });
}
await new Promise<never>(() => undefined);
