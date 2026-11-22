import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import path from "node:path";
import { GhRetries } from "./common";
import type { Gh, GhResult } from "./copies";
import {
  INSTALLER,
  PLUGINS_MANIFEST,
  packagePins,
  packageSkills,
  pinChanges,
  readPins,
  SetReads,
  vendorPins,
  vendorPool,
} from "./sets";

// sets.ts on F10 (testdata/CAPTURE.md, "F10"): GitHub's answers for the pins of two skill sets,
// cut to what sets.ts reads, with made-up names. acme/dotfiles pins alpha-set (acme/alpha-set) in
// omp/plugins/package.json by an annotated tag, and beta-set (acme/beta-set-plugin, a fork of
// acme-upstream/beta-set-plugin) in installers/omp.sh by an ensure_vendor line with a commit.

const F10: Readonly<Record<string, unknown>> = JSON.parse(
  readFileSync(path.join(import.meta.dir, "testdata", "F10-github.json"), "utf8")
);
const DOTFILES = "acme/dotfiles";
/** The dotfiles commit F10 reads the pins at. */
const AT = "82e8f6dcac42f7e9e6f80033755bcea084d18b0c";
const ALPHA_COMMIT = "2ce8aa15d05bd57213e6f746f953aafb7ded7a8b";
const ALPHA_TAG_OBJECT = "cfd50a286baea35041505ed068c878d389ca9f7f";
const BETA_COMMIT = "8a776c13a08eea0d6485c26c9bc96ea84d5a6cc6";
const SETS = [
  { id: "alpha-set", repo: "acme/alpha-set" },
  { id: "beta-set", repo: "acme/beta-set-plugin" },
];

/** F10's text answer of `route`; fails the test when F10 holds none. */
function f10Text(route: string): string {
  const answer = F10[route];
  if (typeof answer !== "string") throw new Error(`F10 holds no text answer of ${route}`);
  return answer;
}

/** A stand-in gh that answers each `gh api [-H <header>] <route>` from F10, each route asked kept
 *  in `calls`; a route F10 does not hold is answered as GitHub answers a missing one. */
function f10Gh(calls: string[] = []): Gh {
  return async (args): Promise<GhResult> => {
    const route = args.filter((arg, at) => at > 0 && arg !== "-H" && args[at - 1] !== "-H").at(-1);
    calls.push(route ?? "");
    const answer = route === undefined ? undefined : F10[route];
    if (answer === undefined) return { code: 1, stdout: "", stderr: "gh: Not Found (HTTP 404)\n" };
    const stdout = typeof answer === "string" ? answer : JSON.stringify(answer);
    return { code: 0, stdout, stderr: "" };
  };
}

/** sets.ts's reads through `gh`, sent again with no wait. */
const readsOf = (gh: Gh) =>
  new SetReads(gh, new GhRetries({ sleep: async () => {}, err: () => {} }, "set-count"));

// Catches: a dependency matched by its package key, by the repository's exact case, or a github:
// dependency of another repository taken for the set.
test("reads the alpha-set pin from omp/plugins/package.json", () => {
  const text = f10Text(`repos/${DOTFILES}/contents/${PLUGINS_MANIFEST}?ref=${AT}`);
  expect(packagePins(text, SETS)).toEqual([
    {
      kind: "package",
      set: "alpha-set",
      manifest: PLUGINS_MANIFEST,
      line: 7,
      text: '"alpha-set": "github:acme/alpha-set#v7.0.1"',
      package: "alpha-set",
      ref: "v7.0.1",
    },
  ]);
  // Whatever the package key, and the owner and repository in any case.
  const renamed = text.replace(
    '"alpha-set": "github:acme/alpha-set',
    '"sp": "github:ACME/Alpha-Set'
  );
  expect(packagePins(renamed, SETS)).toMatchObject([
    { set: "alpha-set", package: "sp", ref: "v7.0.1", line: 7 },
  ]);
  expect(packagePins(text, [{ id: "knives", repo: "acme/knives-two" }])).toEqual([]);
});

// Catches: the ensure_vendor function's own lines, a line of another repository, or a line with
// no commit taken for the set's pin.
test("reads the beta-set pin from an ensure_vendor line", () => {
  const text = f10Text(`repos/${DOTFILES}/contents/${INSTALLER}?ref=${AT}`);
  expect(vendorPins(text, SETS)).toEqual([
    {
      kind: "vendor",
      set: "beta-set",
      manifest: INSTALLER,
      line: 4,
      text: `ensure_vendor https://github.com/acme/beta-set-plugin.git beta-set ${BETA_COMMIT}`,
      dir: "beta-set",
      ref: BETA_COMMIT,
    },
  ]);
  const variants = [
    "ensure_vendor() {",
    '    local url="$1" name="$2" ref="$3"',
    "# ensure_vendor https://github.com/acme/beta-set-plugin.git old-dir 1111111",
    "ensure_vendor https://github.com/Acme/Beta-Set-Plugin beta-two # unpinned",
  ].join("\n");
  expect(vendorPins(variants, SETS)).toEqual([
    {
      kind: "vendor",
      set: "beta-set",
      manifest: INSTALLER,
      line: 4,
      text: "ensure_vendor https://github.com/Acme/Beta-Set-Plugin beta-two # unpinned",
      dir: "beta-two",
      ref: "",
    },
  ]);
});

// Catches: an annotated tag's own object id taken for the commit, a lightweight tag read as an
// annotated one, or a 40-hex ref sent to GitHub at all.
test("resolves an annotated tag to its commit", async () => {
  const calls: string[] = [];
  const reads = readsOf(f10Gh(calls));
  expect(await reads.resolve("acme/alpha-set", "v7.0.1")).toEqual({
    commit: ALPHA_COMMIT,
    by: `the annotated tag v7.0.1 (tag object ${ALPHA_TAG_OBJECT})`,
  });
  expect(calls).toEqual([
    "repos/acme/alpha-set/git/ref/tags/v7.0.1",
    `repos/acme/alpha-set/git/tags/${ALPHA_TAG_OBJECT}`,
  ]);
  expect(await reads.resolve("acme/alpha-set", "v4.1.1")).toEqual({
    commit: "469a6d81ebb8b827e284d4afb090c6c622d97747",
    by: "the lightweight tag v4.1.1",
  });
  calls.length = 0;
  expect(await reads.resolve("acme/beta-set-plugin", BETA_COMMIT)).toEqual({
    commit: BETA_COMMIT,
    by: "a commit",
  });
  expect(calls).toEqual([]);
  const missing = await reads.resolve("acme/alpha-set", "v9.9.9").then(() => "resolved", String);
  expect(missing).toContain("acme/alpha-set has no tag v9.9.9");
});

// Catches: a SKILL.md outside skills/<name>/ (a guide's own skills, a test fixture's) listed as a
// skill of the set, or a skill's other files listed as skills.
test("lists the skills at the pinned commit", async () => {
  const pins = await readPins(readsOf(f10Gh()), SETS, DOTFILES, AT);
  expect(pins.commit).toBe(AT);
  expect(pins.commitFrom).toBe("--dotfiles-commit");
  expect(pins.sets).toEqual([
    {
      id: "alpha-set",
      repo: "acme/alpha-set",
      upstreams: [],
      pin: {
        kind: "package",
        set: "alpha-set",
        manifest: PLUGINS_MANIFEST,
        line: 7,
        text: '"alpha-set": "github:acme/alpha-set#v7.0.1"',
        package: "alpha-set",
        ref: "v7.0.1",
      },
      commit: ALPHA_COMMIT,
      resolvedBy: `the annotated tag v7.0.1 (tag object ${ALPHA_TAG_OBJECT})`,
      skillsFrom: "pi.skills ./skills",
      skills: ["alpha-skill-one", "alpha-skill-three", "alpha-skill-two"],
    },
    {
      id: "beta-set",
      repo: "acme/beta-set-plugin",
      upstreams: ["acme-upstream/beta-set-plugin"],
      pin: {
        kind: "vendor",
        set: "beta-set",
        manifest: INSTALLER,
        line: 4,
        text: `ensure_vendor https://github.com/acme/beta-set-plugin.git beta-set ${BETA_COMMIT}`,
        dir: "beta-set",
        ref: BETA_COMMIT,
      },
      commit: BETA_COMMIT,
      resolvedBy: "a commit",
      skillsFrom: "skills-sources.json pool vendor/beta-set",
      skills: ["beta-skill-one", "beta-skill-three", "beta-skill-two"],
    },
  ]);
  const unpinned = await readPins(
    readsOf(f10Gh()),
    [...SETS, { id: "gamma-set", repo: "acme/gamma-set" }],
    DOTFILES,
    AT
  ).then(() => "read", String);
  expect(unpinned).toContain(`the dotfiles commit ${AT} pins no gamma-set (acme/gamma-set)`);
});

// Catches: a package whose skills are elsewhere (a built dist/skills no git tree holds) listed
// from skills/, pi.skills read before omp.skills, or a vendor pool whose skills sit deeper taken
// for skills/.
test("reads where a package or a vendor pool puts its skills, and refuses any place but skills/", async () => {
  const alpha = f10Text(`repos/acme/alpha-set/contents/package.json?ref=${ALPHA_COMMIT}`);
  expect(packageSkills(alpha)).toBe("pi.skills ./skills");
  expect(packageSkills('{"omp": {"skills": ["skills/"]}, "pi": {"skills": ["other"]}}')).toBe(
    "omp.skills skills/"
  );
  expect(packageSkills('{"name": "x", "omp": {"extensions": ["a.ts"]}}')).toBe(
    "its package.json declares no omp.skills or pi.skills, so skills/"
  );
  // A tag whose commit holds no package.json (F10's v4.1.1): Oh My Pi reads skills/.
  const reads = readsOf(f10Gh());
  const none = await reads.text(
    "acme/alpha-set",
    "469a6d81ebb8b827e284d4afb090c6c622d97747",
    "package.json"
  );
  expect(none).toBeUndefined();
  expect(packageSkills(none)).toBe("no package.json, so skills/");
  const gamma = f10Text(
    "repos/acme/gamma-set/contents/package.json?ref=dc7e6687840aefce14c3af2977f00487fd736bcd"
  );
  expect(() => packageSkills(gamma)).toThrow(
    "its package.json declares its skills at dist/skills (omp.skills), not skills/"
  );
  const pools = f10Text(`repos/${DOTFILES}/contents/skills-sources.json?ref=${AT}`);
  expect(vendorPool(pools, "beta-set")).toBe("skills-sources.json pool vendor/beta-set");
  expect(() => vendorPool(pools, "delta")).toThrow(
    "skills-sources.json gives vendor/delta the pools vendor/delta/plugins/delta"
  );
  expect(() => vendorPool(pools, "gamma")).toThrow(
    "skills-sources.json gives vendor/gamma no pool"
  );
  expect(() => vendorPool(undefined, "beta-set")).toThrow("holds no skills-sources.json");
});

// Catches: a commit that changed another dependency or line named as a pin change, a change
// compared with the commit before it in the list rather than with its parent, or the changes
// left in GitHub's newest-first order.
test("names each commit in the window at which a pin changed", async () => {
  const changes = await pinChanges(
    readsOf(f10Gh()),
    SETS,
    DOTFILES,
    "2026-10-23T00:00:00Z",
    "2026-11-20T00:00:00Z"
  );
  const vendor = (pin: string) =>
    `ensure_vendor https://github.com/acme/beta-set-plugin.git beta-set ${pin}`;
  expect(changes).toEqual([
    {
      commit: "1e99dfa8eeefb6e7e7ae128cd17ed723cc7c616b",
      date: "2026-10-24T22:40:26Z",
      manifest: INSTALLER,
      set: "beta-set",
      before: vendor("b4adb2f0312d"),
      after: vendor("a1c1209cf1e9"),
    },
    {
      commit: "c5f487920f96a5889db9273a48bc7aae6723b40f",
      date: "2026-10-24T22:47:21Z",
      manifest: INSTALLER,
      set: "beta-set",
      before: vendor("a1c1209cf1e9"),
      after: vendor(BETA_COMMIT),
    },
    {
      commit: "76135b6e9e25d5038aa39aa81f24eecef4ccc4cc",
      date: "2026-10-24T23:24:57Z",
      manifest: INSTALLER,
      set: "beta-set",
      before: vendor(BETA_COMMIT),
      after: vendor("b4adb2f0312d"),
    },
    {
      commit: "371649f517018dd6d7a4cb235a557b8b98032115",
      date: "2026-10-25T00:31:32Z",
      manifest: INSTALLER,
      set: "beta-set",
      before: vendor("b4adb2f0312d"),
      after: vendor(BETA_COMMIT),
    },
    {
      commit: "dc0a7398282a43ba3b9a49b91927e4d44a28ddb9",
      date: "2026-10-27T04:29:04Z",
      manifest: PLUGINS_MANIFEST,
      set: "alpha-set",
      before: '"alpha-set": "github:acme/alpha-set#v6.3.0"',
      after: '"alpha-set": "github:acme/alpha-set#v7.0.1"',
    },
  ]);
});

// Catches: the default commit taken from anything but the default branch's ref now.
test("with no --dotfiles-commit, reads the pins at the default branch's commit now", async () => {
  const head = "1111111111111111111111111111111111111111";
  const calls: string[] = [];
  const f10 = f10Gh();
  const gh: Gh = async (args) => {
    const route = args.at(-1) ?? "";
    if (route === `repos/${DOTFILES}`) {
      calls.push(route);
      return { code: 0, stdout: JSON.stringify({ default_branch: "trunk" }), stderr: "" };
    }
    if (route === `repos/${DOTFILES}/git/ref/heads/trunk`) {
      calls.push(route);
      const ref = { ref: "refs/heads/trunk", object: { sha: head, type: "commit" } };
      return { code: 0, stdout: JSON.stringify(ref), stderr: "" };
    }
    // The pins at that commit are F10's at AT.
    return f10(args.map((arg) => arg.replace(`ref=${head}`, `ref=${AT}`)));
  };
  const pins = await readPins(readsOf(gh), SETS, DOTFILES, undefined);
  expect(calls).toEqual([`repos/${DOTFILES}`, `repos/${DOTFILES}/git/ref/heads/trunk`]);
  expect(pins.commit).toBe(head);
  expect(pins.commitFrom).toBe("the default branch trunk of acme/dotfiles when the count ran");
  expect(pins.sets.map((set) => set.commit)).toEqual([ALPHA_COMMIT, BETA_COMMIT]);
});
