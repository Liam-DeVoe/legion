// Fixture secrets for tests, and the check that every test uses them.
//
// A secret a test hands the clients (a token in an environment variable, a capability or a boot
// token in a 0600 file, a Dispatch bearer) is held by the process, and every line the clients
// print is scrubbed of it: each held value of 10 or more characters, and each run of 10 or more of
// its characters, prints as `<withheld>` (`@legion/contracts/held-secrets`). A fixture spelled from
// words (`controller-start-capability`) takes those words out of the lines a test reads
// (`invalid controller capability` printed as `invalid <withheld> <withheld>`), and the test waits
// for words that never show. A real secret is random, so a fixture is too: `fixtureSecret`.
import { randomInt } from "node:crypto";
import { readdirSync, readFileSync, statSync } from "node:fs";
import path from "node:path";

/** Upper-case letters only, so every run of the random part holds one and no run reads as a word. */
const LETTERS = "ABCDEFGHIJKLMNOPQRSTUVWXYZ";
/** How many random letters follow the prefix. */
const RANDOM_LENGTH = 24;

/**
 * A fixture secret: `prefix`, a `-`, and 24 random upper-case letters (`boot-KQVM…`). The prefix
 * names what the secret is in a test's output and is 1 to 8 lower-case letters or digits, so with
 * its `-` it is at most 9 characters, every 10-character run of the secret holds a random
 * upper-case letter, and the scrub takes the secret out of no ordinary text.
 */
export function fixtureSecret(prefix: string): string {
  if (!/^[a-z0-9]{1,8}$/.test(prefix)) {
    throw new Error(
      `fixtureSecret's prefix is 1 to 8 lower-case letters or digits, not ${JSON.stringify(prefix)}`
    );
  }
  let random = "";
  for (let made = 0; made < RANDOM_LENGTH; made += 1) random += LETTERS[randomInt(LETTERS.length)];
  return `${prefix}-${random}`;
}

/** The shortest literal that can be held (`@legion/contracts/held-secrets`, `SCRUB_RUN`): a
 *  shorter one is never held and scrubs nothing. */
const SHORTEST_HELD = 10;

/** A line exempted from the check, with the reason after the marker. */
const EXEMPT = /\/\/ fixture-secret: \S/;

/** An upper-case name that names a secret, as an environment variable or a constant does. A claim
 *  token names a role, not a credential, and is never held. */
const SECRET_KEY = /^(?![A-Z_]*CLAIM_TOKEN$)[A-Z][A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|CAPABILITY)$/;
/** A camel-case name that names a secret. A claim token names a role, not a credential. */
const SECRET_NAME =
  /^(?!claimToken$)(?:(?:token|secret|password|capability)$|.*(?:Token|Secret|Password|Capability)$)/;

/** A quoted or template literal, captured whole. */
const LITERAL = String.raw`("(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'|\`(?:[^\`\\]|\\.)*\`)`;

/** The places a test hands the clients a secret: each pattern, and the test its first group (a
 *  name) must pass, where it has one. Its last group is the literal. */
const SINKS: readonly (readonly [RegExp, RegExp | undefined])[] = [
  // A secret file: written with mode 0o600.
  [
    new RegExp(String.raw`writeFile(?:Sync)?\(.*?,\s*${LITERAL}\s*,\s*\{[^}]*mode:\s*0o600`, "g"),
    undefined,
  ],
  // A token or secret file, written with any mode: the path names it (`tokenFile`,
  // `path.join(dir, "LEGION_BOOT_TOKEN")`, `secretFile`).
  [
    new RegExp(
      String.raw`writeFile(?:Sync)?\(([^,]*(?:[Tt]oken|[Ss]ecret|[Cc]apability|TOKEN|SECRET)[^,]*),\s*${LITERAL}`,
      "g"
    ),
    /./,
  ],
  // An environment variable, an assignment or a constant whose name is upper case, written as a
  // key, a name or a bracketed name (`process.env["DISPATCH_TOKEN"] = …`).
  [new RegExp(String.raw`\b([A-Z][A-Z0-9_]*)["']?\]?\s*[:=]\s*${LITERAL}`, "g"), SECRET_KEY],
  // A variable whose name is camel case.
  [
    new RegExp(
      String.raw`\b(?:const|let)\s+([a-z][A-Za-z0-9]*)\s*(?::\s*[A-Za-z]+\s*)?=\s*${LITERAL}`,
      "g"
    ),
    SECRET_NAME,
  ],
  // A plain bearer.
  [new RegExp(String.raw`bearerCredential\(\s*${LITERAL}`, "g"), undefined],
  // A daemon's registration secret or a boot token, as a stand-in answers or sends them, and a
  // config's token (an `envoy.json`'s `token`, or a `!` command whose output is the token).
  [new RegExp(String.raw`\b(?:secret|bootToken|token)\s*:\s*${LITERAL}`, "g"), undefined],
];

/** A call that returns random characters, which a template may build a secret around. */
const RANDOM_CALL =
  /^(?:crypto\.)?(?:randomUUID\(\)|randomBytes\([^)]*\)\.toString\([^)]*\)|fixtureSecret\([^)]*\))$/;

/** An identifier, which is checked where it is declared. */
const IDENTIFIER = /^[A-Za-z_][A-Za-z0-9_.]*$/;
/** A 10-character run of text that can be ordinary prose: lower-case letters, digits, `-`, `_`
 *  and spaces. A run with an upper-case letter or other punctuation reads as no word. */
const PROSE_WINDOW = /[a-z0-9 _-]{10}/;

/** What a `!` command prints, which is what the clients hold: each word it `echo`es and each
 *  string it `printf`s, `${…}` values left as they are. */
function commandOutput(command: string): string[] {
  const echoed = [...command.matchAll(/\becho\s+([^\s;#&|`'"]+)/g)].map((match) => match[1] ?? "");
  const printed = [...command.matchAll(/\bprintf\s+'([^']*)'/g)].map((match) =>
    (match[1] ?? "").replace(/\\\\n|\\n|\s/g, "")
  );
  return [...echoed, ...printed];
}

/**
 * Whether `literal`, as written in source, can be held and is not the helper's output or random.
 * A quoted literal of 10 characters or more can be held; a content token (`sha256:…`) names a
 * document's version and is no credential. A `!` command is held as its output (`echo
 * kept-secret` holds `kept-secret`). A template whose values are identifiers (checked where they
 * are declared) is that value where its own text is only whitespace; a template whose values are
 * all random calls is random where no run of its own text could be prose (`PROSE_WINDOW`). Any
 * other template, such as `${name}-secret`, builds a secret of words.
 */
function mayBeHeld(literal: string): boolean {
  const body = literal.slice(1, -1);
  if (body.startsWith("sha256:")) return false;
  if (body.startsWith("!")) {
    return commandOutput(body).some(
      (output) => !output.startsWith("${") && output.length >= SHORTEST_HELD
    );
  }
  if (!literal.startsWith("`") || !body.includes("${")) {
    return body.replace(/\\n$/, "").length >= SHORTEST_HELD;
  }
  const values = [...body.matchAll(/\$\{([^}]*)\}/g)].map((match) => (match[1] ?? "").trim());
  const text = body.split(/\$\{[^}]*\}/);
  if (values.every((value) => RANDOM_CALL.test(value))) {
    return text.some((part) => PROSE_WINDOW.test(part));
  }
  const onlyWhitespace = text.every((part) => part.replace(/\\n|\s/g, "") === "");
  return !(
    onlyWhitespace && values.every((value) => IDENTIFIER.test(value) || RANDOM_CALL.test(value))
  );
}

/** Every `.ts` file under `dir` that holds tests or their fixtures: a `.test.ts` file, or any file
 *  under a `test`, `__tests__` or `scripts` directory, where the stand-ins and rigs live. It skips
 *  `node_modules`, `dist` and dot directories. */
function testSources(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir)) {
    if (entry === "node_modules" || entry === "dist" || entry.startsWith(".")) continue;
    const at = path.join(dir, entry);
    if (statSync(at).isDirectory()) out.push(...testSources(at));
    else if (/\.test\.ts$/.test(entry) || /[\\/](?:test|__tests__|scripts)[\\/].*\.ts$/.test(at)) {
      out.push(at);
    }
  }
  return out;
}

/**
 * Each place under `root` where a test hands the clients a secret spelled as a literal that can be
 * held and is not random (`mayBeHeld`), as `<file>:<line>: <the literal>`. A secret there is to
 * be a `fixtureSecret`; a line that hands a value the clients refuse, and so never hold, says why
 * in a `// fixture-secret: <reason>` comment on it. An empty list is the pass; a root that holds no
 * test source is an error, since a check that read nothing has not passed.
 */
export function wordBuiltFixtureSecrets(root: string): string[] {
  const files = testSources(root);
  if (files.length === 0) throw new Error(`no test source under ${root}: the check read nothing`);
  // One entry per site, however many sinks match it.
  const found = new Set<string>();
  for (const file of files) {
    const lines = readFileSync(file, "utf8").split("\n");
    lines.forEach((line, at) => {
      // A comment hands the clients nothing, and an exempted line says why it is none.
      if (EXEMPT.test(line) || /^\s*(?:\/\/|\/\*|\*)/.test(line)) return;
      for (const [sink, names] of SINKS) {
        for (const match of line.matchAll(sink)) {
          const literal = match[match.length - 1] ?? "";
          if (names !== undefined && !names.test(match[1] ?? "")) continue;
          if (mayBeHeld(literal)) found.add(`${path.relative(root, file)}:${at + 1}: ${literal}`);
        }
      }
    });
  }
  return [...found];
}
