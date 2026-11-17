import { describe, expect, test } from "bun:test";
import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";
import { ANSWERS, labelOf, SKIPS, scenarioOf } from "./completion-answers";
import {
  loadRun,
  mounted,
  namesOf,
  namesSaid,
  offWorldReach,
  ownNames,
  serviceProvenance,
  unfinished,
  writtenBy,
} from "./health";
import { REMOVED_NAMES, systemPromptOf, vocabulary } from "./run-names";

/** The world of a run whose pane names only the loopback. */
const LOOPBACK = new Set(["127.0.0.1", "localhost", "::1"]);
const SESSION = "sessions/2026-11-14T00-00-00-000Z_s1.jsonl";
const API = "https://api.github.com/repos/cybermonklabs/legion";
const LINK = "https://github.com/cybermonklabs/legion/pull/2827";
/** The run's own directory, whose bin/gh is its gh stand-in. */
const RUN = "/work/runs/threads-closed-text-fixes-head-1";

/** Rule 13's reason for an agent of the run RUN that made `steps`, one tool call each, in that
 * order. */
function reach(...steps: [tool: string, args: Record<string, unknown>][]): string | undefined {
  return offWorldReach(
    steps.map(([tool, args], at) => ({ file: SESSION, tool, args, at })),
    LOOPBACK,
    [RUN]
  );
}
const bash = (command: string): [string, Record<string, unknown>] => ["bash", { command }];
const write = (path: string, content: string): [string, Record<string, unknown>] => [
  "write",
  { path, content },
];
const FETCH_PY = `import urllib.request\nprint(urllib.request.urlopen('${API}/pulls/2827').read())\n`;

// Catches: a here-document body read as text whatever reads it, so a Python or a shell program
// given on stdin reaches GitHub unread; and the body of a command that writes text (a file, a
// comment's --body-file, a commit message) or reads data (jq, `python3 -c`) read as a reach.
describe("health: rule 13, off-world network, here-documents (door 2)", () => {
  test("a here-document python3 runs as its program is off-world", () => {
    expect(
      reach(bash(`python3 - <<'EOF'\nimport urllib.request; urllib.request.urlopen('${API}')\nEOF`))
    ).toBe(
      `off-world network: a bash call in ${SESSION} reaches api.github.com (${API}) in a here-document python3 runs, no host of the run's world (127.0.0.1, localhost, ::1)`
    );
  });

  test("a here-document a bare cat pipes into node is off-world", () => {
    expect(reach(bash(`cat <<'EOF' | node\nawait fetch("${API}");\nEOF`))).toContain(
      "reaches api.github.com"
    );
  });

  test("a here-document a shell reads on stdin is read as a shell command", () => {
    expect(reach(bash(`bash -s 2827 <<'EOF'\ncurl -sS ${API}\nEOF`))).toContain(
      `reaches api.github.com (${API}) in a here-document bash runs`
    );
    expect(reach(bash(`bash <<'EOF'\ngh pr comment 1 --body 'see ${LINK}'\nEOF`))).toBeUndefined();
  });

  test("a here-document a command writes as text is no reach", () => {
    expect(reach(bash(`cat > body.md <<'EOF'\nsee ${LINK}\nEOF`))).toBeUndefined();
    expect(reach(bash(`gh pr comment 1 --body-file - <<'EOF'\nsee ${LINK}\nEOF`))).toBeUndefined();
    expect(reach(bash(`git commit -F - <<'EOF'\nAnswer ${LINK}\nEOF`))).toBeUndefined();
  });

  test("a here-document an interpreter reads as data is no reach", () => {
    expect(
      reach(bash(`python3 -c 'import sys; print(sys.stdin.read())' <<'EOF'\n${LINK}\nEOF`))
    ).toBeUndefined();
    expect(reach(bash(`jq -r .url <<'EOF'\n{"url": "${API}"}\nEOF`))).toBeUndefined();
  });
});

// Catches: a script the run wrote and then ran read as text, so its fetch of GitHub goes unread;
// and a file written and never run, or handed to a command as text, read as a reach.
describe("health: rule 13, off-world network, files written then run (door 3)", () => {
  test("a write of fetch.py that python3 then runs is off-world", () => {
    expect(reach(write("fetch.py", FETCH_PY), bash("python3 fetch.py"))).toBe(
      `off-world network: a bash call in ${SESSION} reaches api.github.com (${API}/pulls/2827) in fetch.py, which a write call in ${SESSION} wrote and it runs, no host of the run's world (127.0.0.1, localhost, ::1)`
    );
  });

  test("a written file run by its absolute path, an interpreter's subcommand or source is off-world", () => {
    expect(
      reach(
        write("/run/ws/fetch.ts", `await fetch("${API}");`),
        bash("cd /run/ws && bun run fetch.ts")
      )
    ).toContain("in /run/ws/fetch.ts, which a write call");
    expect(
      reach(write("get.sh", `curl -sS ${API}\n`), bash("chmod +x get.sh && ./get.sh"))
    ).toContain("reaches api.github.com");
    expect(reach(write("env.sh", `curl -sS ${API}\n`), bash("source env.sh"))).toContain(
      "reaches api.github.com"
    );
    expect(reach(write("fetch.py", FETCH_PY), bash("python3 /run/ws/fetch.py"))).toContain(
      "in fetch.py, which a write call"
    );
  });

  test("calls are read in the order they were made, whatever transcript holds them", () => {
    const [script, run] = [write("fetch.py", FETCH_PY), bash("python3 fetch.py")];
    const at = (step: [string, Record<string, unknown>], when: number, file: string) => ({
      file,
      tool: step[0],
      args: step[1],
      at: when,
    });
    // The subagent's transcript is read after its parent's, but its write came first.
    expect(
      offWorldReach([at(run, 2, SESSION), at(script, 1, `${SESSION}/sub.jsonl`)], LOOPBACK)
    ).toContain(`in fetch.py, which a write call in ${SESSION}/sub.jsonl`);
  });

  test("a written file a program a shell is given inline runs is off-world", () => {
    expect(
      reach(write("fetch.py", FETCH_PY), bash(`bash -lc 'cd ws && python3 fetch.py'`))
    ).toContain("in fetch.py, which a write call");
  });

  test("a written file a command's quoted text names as a command is no reach", () => {
    const script = write("fetch.sh", `curl -sS ${API}\n`);
    expect(
      reach(
        script,
        bash(`dispatch result --issue X-1 --run "bash fetch.sh; ./fetch.sh" --verdict pass`)
      )
    ).toBeUndefined();
    expect(reach(script, bash(`git commit -m "Fix it\n./fetch.sh\nmore"`))).toBeUndefined();
  });

  test("a written reply a command substitution reads is no reach", () => {
    // Round 17's reviewer-text-findings-text-only-base-5 and threads-closed-code-defect-base-3.
    const reply = write("/tmp/reply.txt", `see ${LINK}\n`);
    for (const read of ["body=$(cat /tmp/reply.txt)", "body=`cat /tmp/reply.txt`"])
      expect(
        reach(reply, bash(`${read} && gh api graphql -F id=PRRT_1 -f body="$body"`))
      ).toBeUndefined();
  });

  test("a here-document a bash command writes to a file that it then runs is off-world", () => {
    expect(
      reach(bash(`cat > fetch.py <<'EOF'\n${FETCH_PY}EOF`), bash("python3 fetch.py"))
    ).toContain("in fetch.py, which a bash call");
    expect(
      reach(bash(`tee get.sh <<'EOF' >/dev/null\ncurl -sS ${API}\nEOF`), bash("bash get.sh"))
    ).toContain("in get.sh, which a bash call");
  });

  test("an edit that puts a URL in a file the run then runs is off-world", () => {
    expect(
      reach(
        write("fetch.py", "import urllib.request\n"),
        ["edit", { input: `[fetch.py#1A2B]\nPUT >1:\n+urllib.request.urlopen('${API}')\n` }],
        bash("python3 fetch.py")
      )
    ).toContain("in fetch.py, which a edit call");
  });

  test("what a call appends to a written file keeps the text the file held", () => {
    const tail = "print('done')\n";
    for (const append of [
      bash(`cat >> fetch.py <<'EOF'\n${tail}EOF`),
      bash(`tee -a fetch.py <<'EOF'\n${tail}EOF`),
      ["edit", { input: `[fetch.py#1A2B]\nPUT >2:\n+${tail}` }] as [
        string,
        Record<string, unknown>,
      ],
    ])
      expect(reach(write("fetch.py", FETCH_PY), append, bash("python3 fetch.py"))).toContain(
        "reaches api.github.com"
      );
    expect(
      reach(
        write("fetch.py", FETCH_PY),
        bash(`cat > fetch.py <<'EOF'\n${tail}EOF`),
        bash("python3 fetch.py")
      )
    ).toBeUndefined();
    // Within one call: the append keeps what the same command wrote before it.
    expect(
      reach(
        bash(`cat > fetch.py <<'EOF'\n${FETCH_PY}EOF\ncat >> fetch.py <<'EOF'\n${tail}EOF`),
        bash("python3 fetch.py")
      )
    ).toContain("reaches api.github.com");
  });

  test("a bash command that writes a here-document to a file and runs it is off-world", () => {
    expect(reach(bash(`cat > fetch.py <<'EOF'\n${FETCH_PY}EOF\npython3 fetch.py`))).toContain(
      "in fetch.py, which a bash call"
    );
  });

  test("a written file a shell runs is read as a shell script, a link it writes as text", () => {
    const post = `gh pr comment 1 --body 'see ${LINK}'\n`;
    expect(reach(write("post", `#!/bin/sh\n${post}`), bash("./post"))).toBeUndefined();
    expect(reach(write("post.sh", post), bash("./post.sh"))).toBeUndefined();
    expect(reach(write("post", post), bash("bash post"))).toBeUndefined();
    expect(reach(write("post.py", `# ${post}`), bash("python3 post.py"))).toContain(
      "reaches github.com"
    );
  });

  test("an eval cell that loads a written file as code is off-world", () => {
    const evalOf = (language: string, code: string): [string, Record<string, unknown>] => [
      "eval",
      { language, code },
    ];
    for (const code of ['import runpy\nrunpy.run_path("fetch.py")', "import fetch"])
      expect(reach(write("fetch.py", FETCH_PY), evalOf("py", code))).toContain(
        "in fetch.py, which a write call"
      );
    expect(
      reach(write("/run/ws/fetch.js", `await fetch("${API}");`), evalOf("js", 'require("./fetch")'))
    ).toContain("in /run/ws/fetch.js, which a write call");
  });

  test("an eval cell that runs a written file through a subprocess is off-world", () => {
    expect(
      reach(write("fetch.py", FETCH_PY), [
        "eval",
        { language: "py", code: 'import subprocess\nsubprocess.run(["python3", "fetch.py"])' },
      ])
    ).toContain("in fetch.py, which a write call");
    expect(
      reach(write("fetch.ts", `await fetch("${API}");`), [
        "eval",
        {
          language: "js",
          code: 'const { execSync } = require("child_process");\nexecSync("bun fetch.ts");',
        },
      ])
    ).toContain("in fetch.ts, which a write call");
  });

  test("a written file never run, run before it was written, or handed on as text is no reach", () => {
    expect(reach(write("fetch.py", FETCH_PY))).toBeUndefined();
    expect(reach(bash("python3 fetch.py"), write("fetch.py", FETCH_PY))).toBeUndefined();
    expect(
      reach(write("reply.md", `see ${LINK}\n`), bash("gh pr comment 1 --body-file reply.md"))
    ).toBeUndefined();
  });

  test("an eval cell that names a written file without running it is no reach", () => {
    const doc = write("docs/cli.md", `See ${LINK}.\n`);
    for (const code of [
      'print(open("docs/cli.md").read())',
      'import subprocess\nsubprocess.run(["git", "diff", "docs/cli.md"])',
      'const { execSync } = require("child_process");\nexecSync("git diff docs/cli.md");',
    ])
      expect(reach(doc, ["eval", { language: "py", code }])).toBeUndefined();
  });

  test("an eval cell whose message quotes a written file's command is no reach", () => {
    const code = `await tool.envoy_publish({ topic: "t", message: "ran \`python3 fetch.py\` and it passed" });`;
    expect(reach(write("fetch.py", FETCH_PY), ["eval", { language: "js", code }])).toBeUndefined();
  });
});

// Catches: a jj revision that names a remote-tracking bookmark with a path (`main@origin:<path>`)
// read as a git remote in scp form, so a run that read a file of main from its own repository was
// called off-world (round 23's world-health run reviewer-text-findings-defect-base-1, which ran
// this command verbatim); and a remote in scp form where a command does take one read as no reach.
describe("health: rule 13, off-world network, a remote in scp form", () => {
  test("a jj command naming a bookmark of a remote with a path reaches no host", () => {
    expect(
      reach(
        bash(
          `cd /tmp/ws && git rev-parse --short=8 HEAD 2>/dev/null; echo "---base cli.md---"; jj -R . file show main@origin:docs/cli.md 2>&1`
        )
      )
    ).toBeUndefined();
    expect(reach(bash("jj file show -r main@origin docs/cli.md"))).toBeUndefined();
    expect(reach(bash("jj diff --from main@origin --to legion/LWEVAL-1@origin"))).toBeUndefined();
  });

  test("a remote in scp form that git or jj clones, fetches or adds is off-world", () => {
    const remote = "git@github.com:cybermonklabs/legion.git";
    for (const command of [
      `git clone ${remote} src`,
      `git fetch ${remote} main`,
      `git push ${remote} HEAD:main`,
      `git remote add upstream ${remote}`,
      `jj git clone ${remote} src`,
      `jj git remote add upstream ${remote}`,
      `jj git remote set-url origin ${remote}`,
    ])
      expect(reach(bash(command))).toBe(
        `off-world network: a bash call in ${SESSION} reaches github.com (${remote}), no host of the run's world (127.0.0.1, localhost, ::1)`
      );
  });
});

// Catches: a position that takes a repository's address (git-clone(1), git-fetch(1) and the other
// subcommands health.ts's GIT_TAKERS and JJ_TAKERS cite) read as no address, past the options
// before it, so a remote in scp form there goes unread; and a word of git or jj where the command
// takes no address (a revset, a regex of URLs to delete, a value a mode matches, a `--repo` that
// git push's own repository argument overrides) read as a reach.
describe("health: rule 13, off-world network, where git and jj take a repository's address", () => {
  const remote = "git@github.com:cybermonklabs/legion.git";
  const off = (named: string) =>
    `off-world network: a bash call in ${SESSION} reaches github.com (${named}), no host of the run's world (127.0.0.1, localhost, ::1)`;

  for (const [command, named] of [
    [`git clone --depth 1 -b main --single-branch ${remote} src`, remote],
    [`git -C ws -c core.quotepath=off pull ${remote} main`, remote],
    [`git fetch --multiple origin ${remote}`, remote],
    [`git push --repo=${remote}`, remote],
    [`git ls-remote --sort version:refname ${remote}`, remote],
    [`git remote -v set-url --push origin ${remote}`, remote],
    [`git submodule add -b main ${remote} vendor/legion`, remote],
    [`git submodule set-url vendor/legion ${remote}`, remote],
    [`git archive --remote ${remote} HEAD`, remote],
    [`git config --add remote.upstream.url ${remote}`, remote],
    [`git config set --comment mirror remote.upstream.pushurl ${remote}`, remote],
    [`git -c remote.origin.url=${remote} fetch origin`, remote],
    ["git config --global url.git@github.com:.insteadOf gh:", "git@github.com:"],
    [`jj -R ws git remote add upstream ../legion.git --push-url ${remote}`, remote],
    [`jj git remote set-url origin --fetch ${remote}`, remote],
    [`jj --config ui.color=never git clone --remote upstream ${remote} src`, remote],
  ])
    test(`\`${command}\` is off-world`, () => {
      expect(reach(bash(command))).toBe(off(named));
    });

  for (const command of [
    "jj log -r 'main@origin::@'",
    `git remote set-url --delete origin ${remote}`,
    `git config --unset remote.origin.url ${remote}`,
    `git push --repo=${remote} origin main`,
  ])
    test(`\`${command}\` reaches no host`, () => {
      expect(reach(bash(command))).toBeUndefined();
    });
});

// Catches: a word of curl or wget where the program takes no host read as one (the file a value
// option takes, apart or at the end of a word of short options, or a redirection's target); one
// program's option read by the other's (wget's `-c` takes no cookie jar, curl's `-O` no file), so
// the URL after it goes unread; and the host an option of theirs connects to (`--url`, a proxy,
// wget's `--base`) read as none.
describe("health: rule 13, off-world network, where curl and wget take a host", () => {
  const api = "api.github.com/repos/cybermonklabs/legion";
  const whoami = "http://127.0.0.1:4101/api/v1/whoami";
  for (const [command, host, named] of [
    [`curl -O ${api}/tarball`, "api.github.com", `${api}/tarball`],
    [`curl -sS --url ${api}`, "api.github.com", api],
    [`curl -sS -x proxy.example.com:3128 ${whoami}`, "proxy.example.com", "proxy.example.com:3128"],
    [`wget -c ${api}/tarball`, "api.github.com", `${api}/tarball`],
    ["wget -F -i links.html -B api.github.com/", "api.github.com", "api.github.com/"],
  ])
    test(`\`${command}\` is off-world`, () => {
      expect(reach(bash(command))).toBe(
        `off-world network: a bash call in ${SESSION} reaches ${host} (${named}), no host of the run's world (127.0.0.1, localhost, ::1)`
      );
    });

  for (const command of [
    `curl -sS -D headers.txt -o report.json ${whoami}`,
    `curl -sSo report.json ${whoami}`,
    `curl -sS ${whoami} > report.json`,
    `wget -a fetch.log -O page.html ${whoami}`,
  ])
    test(`\`${command}\` reaches no host`, () => {
      expect(reach(bash(command))).toBeUndefined();
    });
});

/** Rule 13's reason for a bash call that reaches `host`, named `named`, held to the run's world. */
const offWorld = (host: string, named: string, via = "") =>
  `off-world network: a bash call in ${SESSION} reaches ${host} (${named})${via}, no host of the run's world (127.0.0.1, localhost, ::1)`;

// Catches: an address in scp form with no user (`host:path`, which git, jj, scp, rsync and sftp
// read as a remote host when no slash comes before its first colon) read as no reach where one
// of them takes an address; and that form read anywhere else, where `HEAD:docs/cli.md`,
// `main@origin:docs/cli.md` and `docs/cli.md:12` are a revision and a path, or a file and a line,
// and a review body or a commit message that names a command after a `;` or a bracket is text.
describe("health: rule 13, off-world network, a remote in scp form with no user", () => {
  const remote = "github.com:cybermonklabs/legion.git";
  for (const command of [
    `git clone ${remote} src`,
    `git -C ws remote add upstream ${remote}`,
    `jj git clone ${remote} src`,
    `scp notes.md ${remote}`,
    `rsync -az ./ ${remote}`,
    `sftp ${remote}`,
  ])
    test(`\`${command}\` is off-world`, () => {
      expect(reach(bash(command))).toBe(offWorld("github.com", remote));
    });

  for (const command of [
    "git show HEAD:docs/cli.md",
    "jj file show main@origin:docs/cli.md",
    "grep -n greet docs/cli.md:12",
    "git push origin HEAD:main",
    "git clone localhost:legion.git src",
    "scp ./notes:v1.md docs/cli.md:12 backup/",
    `gh pr comment 1001 --body "Checked; git clone ${remote} still works"`,
    'git commit -m "Fix the typo (ssh keys are unchanged)"',
  ])
    test(`\`${command}\` reaches no host`, () => {
      expect(reach(bash(command))).toBeUndefined();
    });
});

// Catches: a command a wrapper runs read as the wrapper's words, so `sudo git clone …` or
// `env GIT_SSH_COMMAND=… git fetch …` reaches GitHub unread; and a wrapper read as a reach of its
// own, or the words of one that runs nothing (`command -v`, `sudo -l`) read as a command.
describe("health: rule 13, off-world network, a command a wrapper runs", () => {
  const remote = "git@github.com:cybermonklabs/legion.git";
  for (const [command, named, via] of [
    [`sudo -u ada -E git clone ${remote} src`, remote, ""],
    [`env -i GIT_SSH_COMMAND='ssh -i key' git fetch ${remote}`, remote, ""],
    [`timeout -s KILL 30 git ls-remote ${remote}`, remote, ""],
    [
      "nice -n 10 curl -sS api.github.com/repos/cybermonklabs/legion",
      "api.github.com/repos/cybermonklabs/legion",
      "",
    ],
    ["nohup ssh git@github.com &", "git@github.com", ""],
    [`echo main | xargs -n1 git clone ${remote}`, remote, ""],
    [`command git clone ${remote}`, remote, ""],
    ["exec ssh -T git@github.com", "git@github.com", ""],
    [`time git fetch ${remote}`, remote, ""],
    [`sh -c 'git clone ${remote} src'`, remote, " in the program sh -c runs"],
    ["bash -lc 'cd ws && ssh -T git@github.com'", "git@github.com", " in the program bash -c runs"],
    [`sudo env FOO=1 timeout 5 git clone ${remote}`, remote, ""],
  ])
    test(`\`${command}\` is off-world`, () => {
      expect(reach(bash(command))).toBe(
        offWorld(named.startsWith("api.") ? "api.github.com" : "github.com", named, via)
      );
    });

  for (const command of [
    "env FOO=1 git status",
    "timeout 30 bun test",
    "sudo -u ada git log --oneline",
    "nice -n 10 bun test",
    "nohup bun run serve.ts &",
    "git ls-files | xargs -I{} ssh {}",
    "command -v ssh",
    "sudo -l ssh git@github.com",
    "exec bun test",
    "time bun test",
    "bash -c 'git show HEAD:docs/cli.md'",
  ])
    test(`\`${command}\` reaches no host`, () => {
      expect(reach(bash(command))).toBeUndefined();
    });
});

// Catches: a command that follows an assignment whose value holds a space
// (`GIT_SSH_COMMAND='ssh -i key' git clone …`) read as a program named by the rest of that value,
// so no rule that reads a command by its grammar reads it: score.ts's AT_COMMAND takes an
// assignment's value as `\S*`, and SHELL_COMMAND and DISPATCH_COMMAND are both built on it. And
// an address inside such a value, or a command after an assignment that stands alone, read as a
// reach.
describe("health: rule 13, off-world network, a command after an assignment whose value holds a space", () => {
  const remote = "git@github.com:cybermonklabs/legion.git";
  for (const [command, host, named] of [
    [`GIT_SSH_COMMAND='ssh -i key' git clone ${remote} src`, "github.com", remote],
    [`GIT_SSH_COMMAND="ssh -i key" git fetch ${remote} main`, "github.com", remote],
    [`GIT_SSH_COMMAND=ssh\\ -i\\ key git push ${remote} HEAD:main`, "github.com", remote],
    [`cd ws && NOTE='two words' LANG=C git ls-remote ${remote}`, "github.com", remote],
    [`env NOTE='two words' git clone ${remote} src`, "github.com", remote],
    [
      "NOTE='two words' curl -sS api.github.com/repos/cybermonklabs/legion",
      "api.github.com",
      "api.github.com/repos/cybermonklabs/legion",
    ],
    [`NOTE="two words" ssh -T git@github.com`, "github.com", "git@github.com"],
  ])
    test(`\`${command}\` is off-world`, () => {
      expect(reach(bash(command))).toBe(offWorld(host, named));
    });

  for (const command of [
    `NOTE='clone ${remote} later' git status`,
    `NOTE="see git@github.com for the key" bun test`,
    "NOTE='two words' git show HEAD:docs/cli.md",
    `NOTE='two words'; git status`,
  ])
    test(`\`${command}\` reaches no host`, () => {
      expect(reach(bash(command))).toBeUndefined();
    });
});

// Each reader of a command's words reads them through score.ts's one shell-word reader
// (SHELL_WORD, `shellTokens`): its first word, its words, its prefix assignments, its
// redirections' targets, a text flag's value and a list a subprocess runs. Catches, one row each:
// a quoted first word read up to its first blank (`"/opt/my tools/ssh"` named `my`), or read with
// its quotes (`'/usr/bin/ssh'` named `ssh'`); env's `-i` read with no options after it; a
// backslash-escaped blank ending a word; a word quoted in part read with its quotes; a `$'…'`
// assignment read with its quotes; a redirection's quoted target read up to its first blank, the
// rest of it read as words; a quoted file a here-document is written to read up to its first
// blank; a text flag's or a field's value read up to an escaped quote, the rest of it read as a
// reach; and a subprocess list's word holding a blank read as two.
describe("health: rule 13, off-world network, words read as the shell reads them", () => {
  const remote = "git@github.com:cybermonklabs/legion.git";
  for (const [command, host, named] of [
    ['"/opt/my tools/ssh" -T git@github.com', "github.com", "git@github.com"],
    ["'/usr/bin/ssh' -T git@github.com", "github.com", "git@github.com"],
    [`env -i -u HOME git clone ${remote} src`, "github.com", remote],
    [
      "curl -sS 'api.github.com'/repos/cybermonklabs/legion",
      "api.github.com",
      "api.github.com/repos/cybermonklabs/legion",
    ],
  ])
    test(`\`${command}\` is off-world`, () => {
      expect(reach(bash(command))).toBe(offWorld(host, named));
    });

  test("a gh whose GH_HOST is given in ANSI-C quotes, an escape among them, reaches that host", () => {
    expect(reach(bash("GH_HOST=$'ghe\\x2eexample.com' gh api user"))).toBe(
      `off-world network: a bash call in ${SESSION} reaches ghe.example.com (GH_HOST=ghe.example.com), no host the run's gh serves (github.com, 127.0.0.1, localhost, ::1)`
    );
  });

  for (const command of [
    "curl -sS http://127.0.0.1:9/x -H X-Note:\\ see\\ example.com/",
    "curl -sS http://127.0.0.1:9/x > 'notes for example.com'",
    `gh pr comment 1 --body "a \\"b c\\" ${LINK}"`,
    `gh api repos/o/r/issues/1/comments -f body="a \\"b c\\" ${LINK}"`,
  ])
    test(`\`${command}\` reaches no host`, () => {
      expect(reach(bash(command))).toBeUndefined();
    });

  test("a file a here-document is written to under a quoted name, then run, is off-world", () => {
    expect(
      reach(
        bash(`cat > '/run/ws/get it.sh' <<'EOF'\ncurl -sS ${API}\nEOF`),
        bash("bash /run/ws/get\\ it.sh")
      )
    ).toContain("in /run/ws/get it.sh, which a bash call");
  });

  test("a written file a subprocess list runs by a name holding a blank is off-world", () => {
    expect(
      reach(write("/run/ws/my fetch.py", FETCH_PY), [
        "eval",
        {
          language: "py",
          code: 'import subprocess\nsubprocess.run(["python3", "/run/ws/my fetch.py"])',
        },
      ])
    ).toContain("in /run/ws/my fetch.py, which a write call");
  });

  // An assignment's value keeps an unquoted `$(` in it, so a command substitution there is read
  // where it starts and runs the written file.
  test("a written file a command substitution in an assignment's value runs is off-world", () => {
    expect(reach(write("fetch.py", FETCH_PY), bash("out=$(python3 fetch.py)"))).toContain(
      "in fetch.py, which a write call"
    );
  });
});

// The four operators that trim a variable's value, `${word%p}`, `${word%%p}`, `${word#p}` and
// `${word##p}`, as rule 13 reads them where a call sets the variable to a literal (score.ts's
// `expandedWord`), each row held to bash's own answer for it, which bash prints here, so the
// reader's shortest and longest matches cannot drift from the shell's. Catches: `%%` read as `%`
// or `##` as `#` (the shortest match taken for the longest), a prefix taken off for a suffix, a
// `*` matched as itself, an empty pattern or one with no match read as taking something off, and
// a character of the pattern a regular expression reads as more than itself (`.`, `+`).
describe("health: rule 13, off-world network, a variable's value trimmed as bash trims it", () => {
  const rows: [value: string, operator: string, pattern: string][] = [
    ["a:b:c", "%", ":*"],
    ["a:b:c", "%%", ":*"],
    ["a:b:c", "#", "*:"],
    ["a:b:c", "##", "*:"],
    ["a/b/c/d", "%", "/*/*"],
    ["a/b/c/d", "%%", "/*/*"],
    ["a/b/c/d", "#", "*/*/"],
    ["a/b/c/d", "##", "*/*/"],
    ["aXbYb", "#", "a*b"],
    ["aXbYb", "##", "a*b"],
    ["aXbYb", "%", "b*b"],
    ["aXbYb", "%%", "X*"],
    ["abc", "%", "*"],
    ["abc", "%%", "*"],
    ["abc", "#", "*"],
    ["abc", "##", "*"],
    ["abc", "%", ""],
    ["abc", "#", "z*"],
    ["v7.0.1", "%", ".1"],
    ["v7.0.1", "#", "v"],
    ["a.b+c", "%", "+c"],
    ["a.b+c", "#", "a.b"],
    ["axb", "#", "a.b"],
    ["actions/checkout:v7.0.1:3d3c42e5", "%%", ":*"],
    ["v7.0.1:3d3c42e5", "#", "*:"],
  ];
  test("each row reads to what bash prints for it", () => {
    const expansion = ([, operator, pattern]: (typeof rows)[number]) =>
      `\${word${operator}${pattern}}`;
    const read = rows.map((row) => [
      ...row,
      reach(bash(`word=${row[0]}; curl -s https://example.com/${expansion(row)}`)),
    ]);
    const printed = rows.map((row) => {
      const shell = Bun.spawnSync(["bash", "-c", `word=${row[0]}; printf %s "${expansion(row)}"`]);
      expect(shell.exitCode).toBe(0);
      return [...row, offWorld("example.com", `https://example.com/${shell.stdout.toString()}`)];
    });
    expect(read).toEqual(printed);
  });
});

// The paths a `bash` command writes, read through the same shell-word reader. Catches: a write
// after an assignment whose value holds a space read as no command's (AT_COMMAND's `\S*`); a
// writer's operand with an escaped blank read as two words; a quoted redirection target read as
// none; and a `>` inside a double-quoted word that holds an escaped quote read as a redirection.
describe("health: the paths a bash command writes, words read as the shell reads them", () => {
  for (const [command, paths] of [
    ["NOTE='two words' cp a.txt /var/tmp/b.md", ["/var/tmp/b.md"]],
    ["cp a.txt /var/tmp/my\\ notes.md", ["/var/tmp/my notes.md"]],
    ["echo x > '/var/tmp/my notes.md'", ["/var/tmp/my notes.md"]],
    ['echo "a \\" > /var/tmp/x"', []],
  ] as const)
    test(`\`${command}\` writes ${paths.join(", ") || "nothing"}`, () => {
      expect(writtenBy("bash", { command })).toEqual([...paths]);
    });
});

// Catches: the host ssh, scp, sftp, rsync, nc, ncat or telnet connects to read as no reach: an
// operand that takes a host by the tool's own grammar, or an option that names one (`-J`,
// `-o ProxyJump`, `-o HostName`, a proxy); and the words of those tools that name no host (an
// option's value, a local path, a listener's port, an ssh alias `HostName` points at the
// loopback) read as one.
describe("health: rule 13, off-world network, ssh, scp, sftp, rsync, nc, ncat and telnet", () => {
  for (const [command, host, named] of [
    ["ssh -i key -p 22 git@github.com", "github.com", "git@github.com"],
    ["ssh -J bastion.example.com localhost", "bastion.example.com", "bastion.example.com"],
    [
      "ssh -o ProxyJump=bastion.example.com 127.0.0.1",
      "bastion.example.com",
      "ProxyJump=bastion.example.com",
    ],
    ["ssh -o 'HostName github.com' localhost", "github.com", "HostName github.com"],
    ["scp -P 22 notes.md git@github.com:notes.md", "github.com", "git@github.com:notes.md"],
    [
      "scp -J bastion.example.com notes.md localhost:notes.md",
      "bastion.example.com",
      "bastion.example.com",
    ],
    ["sftp -b batch git@github.com", "github.com", "git@github.com"],
    ["rsync -avz --exclude .git ./ git@github.com:legion/", "github.com", "git@github.com:legion/"],
    ["rsync -av github.com::legion/ ./mirror/", "github.com", "github.com::legion/"],
    [
      "rsync -e 'ssh -J bastion.example.com' ./ localhost:backup/",
      "bastion.example.com",
      "bastion.example.com",
    ],
    ["nc -zv github.com 22", "github.com", "github.com"],
    ["nc -x proxy.example.com:1080 localhost 4100", "proxy.example.com", "proxy.example.com:1080"],
    [
      "ncat --proxy proxy.example.com:3128 127.0.0.1 4100",
      "proxy.example.com",
      "proxy.example.com:3128",
    ],
    ["telnet github.com 22", "github.com", "github.com"],
  ])
    test(`\`${command}\` is off-world`, () => {
      expect(reach(bash(command))).toBe(offWorld(host, named));
    });

  for (const command of [
    "ssh -p 2222 -i key localhost",
    "ssh -o HostName=127.0.0.1 github",
    "ssh -G github.com",
    "ssh-keygen -t ed25519 -f key",
    "scp notes.md backup/notes.md",
    "sftp localhost",
    "rsync -e ssh -av ./ localhost:backup/",
    "rsync -av ./src/ ./dst/",
    "nc -lvp 4100",
    "nc -U /tmp/agent.sock",
    "ncat -l 127.0.0.1 4100",
    "telnet 127.0.0.1 4100",
  ])
    test(`\`${command}\` reaches no host`, () => {
      expect(reach(bash(command))).toBeUndefined();
    });
});

// Catches: a host a `gh` names (`--hostname`, `-R HOST/OWNER/REPO`, GH_HOST, set before it or on
// it) other than github.com, which the run's stand-in plays, and the loopback read as in the
// world; github.com read as off it; and a gh other than the run's own stand-in (<run>/bin/gh,
// which a bare `gh` resolves to on the pane's PATH) read as the world's, whatever it names.
describe("health: rule 13, off-world network, the host a gh names, and a gh that is not the run's", () => {
  const served = (host: string, named: string) =>
    `off-world network: a bash call in ${SESSION} reaches ${host} (${named}), no host the run's gh serves (github.com, 127.0.0.1, localhost, ::1)`;
  const other = (named: string) =>
    `off-world network: a bash call in ${SESSION} reaches github.com (${named}), a gh that is not the run's own stand-in (${RUN}/bin/gh), and no world of this rig serves another`;
  for (const [command, reason] of [
    ["gh api --hostname ghe.example.com user", served("ghe.example.com", "ghe.example.com")],
    [
      "gh pr view 1001 -R ghe.example.com/example/widgets",
      served("ghe.example.com", "ghe.example.com/example/widgets"),
    ],
    [
      "gh pr view 1001 --repo=ghe.example.com/example/widgets",
      served("ghe.example.com", "ghe.example.com/example/widgets"),
    ],
    [
      "gh pr view 1001 -R https://ghe.example.com/example/widgets",
      served("ghe.example.com", "https://ghe.example.com/example/widgets"),
    ],
    [
      "GH_HOST=ghe.example.com gh pr view 1001",
      served("ghe.example.com", "GH_HOST=ghe.example.com"),
    ],
    [
      "env GH_HOST=ghe.example.com gh api user",
      served("ghe.example.com", "GH_HOST=ghe.example.com"),
    ],
    [
      "export GH_HOST=ghe.example.com && gh api user",
      served("ghe.example.com", "GH_HOST=ghe.example.com"),
    ],
    ["GH_HOST=ghe.example.com; gh api user", served("ghe.example.com", "GH_HOST=ghe.example.com")],
    ["GH_HOST=ghe.example.com\ngh api user", served("ghe.example.com", "GH_HOST=ghe.example.com")],
    ["/usr/bin/gh pr view 1", other("/usr/bin/gh")],
    ["cd ws && /opt/legion/bin/gh pr view 1", other("/opt/legion/bin/gh")],
    ["legion gh pr view 1", other("legion gh")],
    ["./gh pr view 1", other("./gh")],
  ])
    test(`\`${command}\` is off-world`, () => {
      expect(reach(bash(command))).toBe(reason);
    });

  for (const command of [
    "gh pr view 1",
    "gh api --hostname github.com repos/example/widgets/pulls/1001",
    "gh pr view 1001 -R example/widgets",
    "gh pr view 1001 -R github.com/example/widgets",
    "gh pr view -R https://github.com/example/widgets 1001",
    "GH_HOST=github.com gh api user",
    "GH_HOST=127.0.0.1 gh api user",
    `${RUN}/bin/gh pr view 1`,
    "../bin/gh pr view 1",
    "command -v /usr/bin/gh",
  ])
    test(`\`${command}\` reaches no host`, () => {
      expect(reach(bash(command))).toBeUndefined();
    });
});

// Catches: a tool rule 13 names no reading for read as reaching nothing, so a URL handed to a
// tool nobody listed goes unread; and the text a writing tool sends (an Envoy message, a Dispatch
// comment, a subagent's prompt) read as a reach.
describe("health: rule 13, off-world network, every other tool (class default)", () => {
  test("an unknown tool given a URL is off-world, called by name or through its xd:// device", () => {
    expect(reach(["frobnicate", { target: `${API}/pulls` }])).toBe(
      `off-world network: a frobnicate call in ${SESSION} reaches api.github.com (${API}/pulls), no host of the run's world (127.0.0.1, localhost, ::1)`
    );
    expect(reach(["frobnicate", { options: [{ url: API }] }])).toContain("reaches api.github.com");
    expect(reach(write("xd://frobnicate", JSON.stringify({ target: API })))).toContain(
      "a write xd://frobnicate call"
    );
  });

  test("an unknown tool whose only URL is its intent is no reach", () => {
    expect(reach(["frobnicate", { i: `Reading ${LINK}` }])).toBeUndefined();
  });

  test("a grep given a URL as its path is off-world, and a URL it searches for is not", () => {
    expect(reach(["grep", { pattern: "legion", path: `src;${API}/readme` }])).toContain(
      "a grep call"
    );
    expect(reach(["grep", { pattern: LINK, path: "src" }])).toBeUndefined();
  });

  for (const [tool, args] of [
    ["envoy_publish", { topic: "notifications.role.x", message: `see ${LINK}` }],
    ["envoy_send", { session_id: "s", message: `see ${LINK}` }],
    ["dispatch_comment", { issue: "LWEVAL-1", body: `see ${LINK}` }],
    ["task", { tasks: [{ id: "a", assignment: `Review ${LINK}` }] }],
    ["todo", { op: "add", items: [`answer ${LINK}`] }],
    ["legion", { op: "handoff_complete", summary: `Approved ${LINK}` }],
    ["report_issue", { content: `read: ${LINK} rendered wrong` }],
    ["edit", { input: `[reply.md#1A2B]\nPUT >1:\n+see ${LINK}\n` }],
  ] as const)
    test(`a ${tool} call whose text names a URL is no reach`, () => {
      expect(reach([tool, args])).toBeUndefined();
    });

  test("an envoy_publish through its xd:// device whose message names a URL is no reach", () => {
    expect(
      reach(write("xd://envoy_publish", JSON.stringify({ topic: "t", message: `see ${LINK}` })))
    ).toBeUndefined();
  });
});

// Catches: an eval cell's call of a session tool read as code, so a message an agent publishes
// through eval (round 17's code-defect-head-5 ran `await tool.envoy_publish(…)`) that quotes the
// world's pull request voids a correct run; and that reading made so wide that a fetch beside it,
// a bash or read call inside the cell, or a call the reader cannot balance goes unread.
describe("health: rule 13, off-world network, an eval cell's tool calls (door 1)", () => {
  const WORLD_PR = "https://github.com/example/widgets/pull/1001";
  const cell = (language: string, code: string): [string, Record<string, unknown>] => [
    "eval",
    { language, code },
  ];
  const PY_PUBLISH = `await tool.envoy_publish(topic="t", message="""see ${LINK}""")`;

  test("a text tool an eval cell calls is no reach, in either language", () => {
    expect(
      reach(cell("js", `await tool.envoy_publish({topic:"t", message:\`see ${WORLD_PR}\`})`))
    ).toBeUndefined();
    expect(reach(cell("py", PY_PUBLISH))).toBeUndefined();
    expect(
      reach(cell("py", `await tool.envoy_publish(topic="t", message="""see\n${LINK}\n(the PR)""")`))
    ).toBeUndefined();
    expect(
      reach(
        cell("js", `await tool.envoy_publish({message: \`see \${\`PR \${n}\`} at ${WORLD_PR}\`})`)
      )
    ).toBeUndefined();
  });

  test("a fetch beside a text tool's call is off-world", () => {
    expect(reach(cell("py", `${PY_PUBLISH}\nawait fetch("${API}")`))).toContain(
      `reaches api.github.com (${API})`
    );
    expect(
      reach(cell("js", `await tool.envoy_publish({message: \`\${await fetch("${API}")}\`})`))
    ).toContain("reaches api.github.com");
  });

  test("a bash, read or unknown tool an eval cell calls is read as its own call", () => {
    expect(reach(cell("js", `await tool.bash({command:"curl -s ${API}/x"})`))).toContain(
      `reaches api.github.com (${API}/x) in the cell's tool.bash call`
    );
    expect(
      reach(cell("js", `await tool.bash({command:"gh pr comment 1 --body 'see ${LINK}'"})`))
    ).toBeUndefined();
    expect(
      reach(cell("js", `await tool.read({path:"https://github.com/cybermonklabs/legion"})`))
    ).toContain("in the cell's tool.read call");
    expect(reach(cell("py", `await tool.grep(pattern="${LINK}", path="src")`))).toBeUndefined();
    expect(reach(cell("py", `await tool.frobnicate("${API}")`))).toContain(
      "in the cell's tool.frobnicate call"
    );
  });

  test("a tool call an eval cell does not balance reads the cell whole as code", () => {
    expect(reach(cell("js", `await tool.envoy_publish({message: "see ${API}"`))).toContain(
      "reaches api.github.com"
    );
    // A one-line literal that does not end on its line is no literal: two apostrophes of
    // comments would otherwise hide the fetch between them inside the text tool's call.
    const comments = [
      "await tool.envoy_publish({",
      `  message: "ok", // don't`,
      `  topic: await (await fetch("${API}")).text(), // it's`,
      "})",
    ].join("\n");
    expect(reach(cell("js", comments))).toContain("reaches api.github.com");
  });

  test("a Python argument with a string prefix is read by its key", () => {
    expect(reach(cell("py", `await tool.bash(command=f"curl -s ${API}/{n}")`))).toContain(
      "in the cell's tool.bash call"
    );
  });

  test("a Python f-string's expressions are read as code, its text and doubled braces are not", () => {
    expect(
      reach(
        cell(
          "py",
          `await tool.envoy_publish(message=f"{urllib.request.urlopen('https://api.github.com/x').read()}")`
        )
      )
    ).toContain("reaches api.github.com");
    expect(
      reach(
        cell(
          "py",
          `url = "http://127.0.0.1:4100/x"\nawait tool.envoy_publish(message=f"see {url}")`
        )
      )
    ).toBeUndefined();
    expect(
      reach(cell("py", `await tool.envoy_publish(message=f"see {{https://api.github.com/x}}")`))
    ).toBeUndefined();
  });

  test("a file an eval cell's tool.write or tool.edit writes, then runs, is off-world", () => {
    const fetchPy = `await tool.write({path:"f.py", content:"import urllib.request\\nurllib.request.urlopen('https://api.github.com/x')"})`;
    expect(reach(cell("js", fetchPy), bash("python3 f.py"))).toContain(
      "in f.py, which a eval call"
    );
    expect(reach(cell("js", fetchPy))).toBeUndefined();
    expect(
      reach(
        write("f.py", "import urllib.request\n"),
        cell(
          "js",
          `await tool.edit({input:"[f.py#1A2B]\\nPUT >1:\\n+urllib.request.urlopen('https://api.github.com/x')"})`
        ),
        bash("python3 f.py")
      )
    ).toContain("in f.py, which a eval call");
    // In one cell, in the cell's order.
    expect(reach(cell("js", `${fetchPy}\nawait tool.bash({command:"python3 f.py"})`))).toContain(
      "in f.py, which a eval call"
    );
  });
});

// Rule 1, finished, read on the run's own boot record (<run>/booted) held to the scenario's one
// definition of the roles it boots (completion-answers.ts's ANSWERS' `boots` and SKIPS). Each row
// builds a run directory as rig.sh leaves it, named as cmd_run names a run.
describe("health: rule 1, finished, on the run's boot record", () => {
  const runs = temporaryDirectories("health-boot-record");
  /** A transcript with its session's header and one model turn. */
  const TRANSCRIPT = [
    { type: "session", id: "s1" },
    { type: "message", message: { role: "assistant", content: [{ type: "text", text: "Done." }] } },
  ]
    .map((entry) => `${JSON.stringify(entry)}\n`)
    .join("");
  /** The implementer's last words in the measured run, threads-closed-text-fixes-head-1 at
   * 7bf20811: its out.txt and its out-implementer.txt, byte for byte the same. */
  const IMPLEMENTER_OUT =
    "WAITING on the reviewer to read the fix, resolve PRRT_kwDOfix001 and PRRT_kwDOfix002, approve head `a0ce49ee301f9d20205337743fb0a88df6480e8c`, and notify the architect, whose acceptance is the gate on the fix head.\nexit=0\n";
  const FIRST = "sessions/2026-11-15T20-59-48-435Z_01a1e102.jsonl";
  /** A run directory named `name` holding `files`, each a path under the run and its text; a path
   * ending in `/` is a directory. */
  function runOf(name: string, files: Record<string, string>): string {
    const dir = path.join(runs(), name);
    mkdirSync(dir);
    for (const [file, text] of Object.entries(files)) {
      const full = path.join(dir, file);
      mkdirSync(file.endsWith("/") ? full : path.dirname(full), { recursive: true });
      if (!file.endsWith("/")) writeFileSync(full, text);
    }
    return dir;
  }
  const judge = (dir: string) =>
    unfinished({ dir, scenario: scenarioOf(dir) ?? "", label: labelOf(dir) });
  /** A threads-closed run both of whose agents ran to their exit, with `booted` its record. */
  const both = (name: string, booted: string) =>
    runOf(name, {
      booted,
      "out-implementer.txt": IMPLEMENTER_OUT,
      "out.txt": "Approved the fix head.\nexit=0\n",
      [FIRST]: TRANSCRIPT,
      "sessions/2026-11-15T20-59-48-435Z_01a1e102/": "",
      "sessions/reviewer/2026-11-15T21-06-10-000Z_01a1e103.jsonl": TRANSCRIPT,
    });
  const SKIPPED = `skip reviewer ${SKIPS["threads-closed"]?.["code-defect"]?.reviewer}\n`;

  // Catches: the measured run read healthy. Its reviewer's stand-in timed out, so the reviewer
  // never launched: sessions/reviewer/ is empty and out.txt is still the implementer's, its exit=
  // line included, which a reader of out.txt's last line took for the run's end.
  test("the measured run whose reviewer was booted and never ran is unfinished", () => {
    const files = {
      "out-implementer.txt": IMPLEMENTER_OUT,
      "out.txt": IMPLEMENTER_OUT,
      [FIRST]: TRANSCRIPT,
      "sessions/2026-11-15T20-59-48-435Z_01a1e102/": "",
      "sessions/reviewer/": "",
    };
    expect(
      judge(
        runOf("threads-closed-text-fixes-head-1", {
          ...files,
          booted: "boot implementer\nboot reviewer\n",
        })
      )
    ).toBe(
      "unfinished: the reviewer left no transcript: <run>/sessions/reviewer holds no .jsonl of its own with an entry"
    );
    // As it was recorded, before a run kept a boot record.
    expect(judge(runOf("threads-closed-text-fixes-head-1", files))).toBe(
      "no boot record: the run has no <run>/booted, so which roles of threads-closed it booted, and whether each finished, is unknown"
    );
    // The same run where worker_boot moves the implementer's output aside rather than copying
    // it: the reviewer, booted last, has no output of its own at all.
    const { "out.txt": _, ...moved } = files;
    expect(
      judge(
        runOf("threads-closed-text-fixes-head-1", {
          ...moved,
          booted: "boot implementer\nboot reviewer\n",
        })
      )
    ).toBe("unfinished: the reviewer's output, <run>/out.txt, is missing");
  });

  // Catches: a reader that holds a run to more than it booted, or to less.
  test("a run whose every booted role finished is finished, a relaunch's repeated line one boot", () => {
    expect(
      judge(both("threads-closed-text-fixes-head-2", "boot implementer\nboot reviewer\n"))
    ).toBe(undefined);
    expect(
      judge(
        both(
          "threads-closed-text-fixes-base-2",
          "boot implementer\nboot implementer\nboot reviewer\n"
        )
      )
    ).toBe(undefined);
    // As boot_agent writes it now: each launch it asks the boot's reader about followed by the
    // `since` it asked with, and a relaunch's repeated `boot` line, asked nothing, with none.
    expect(
      judge(
        both(
          "threads-closed-text-fixes-head-11",
          "boot implementer\nsince implementer 2026-11-16T09:00:00.000Z\nboot implementer\nboot reviewer\nsince reviewer 2026-11-16T09:10:00.000Z\n"
        )
      )
    ).toBe(undefined);
    expect(
      judge(
        runOf("reviewer-text-findings-defect-base-1", {
          booted: "boot reviewer\n",
          "out.txt": "Requested changes.\nexit=0\n",
          [FIRST]: TRANSCRIPT,
        })
      )
    ).toBe(undefined);
  });

  // Catches: the code-defect run that boots no reviewer, because its implementer sent the reviewer
  // nothing, read as unfinished against a definition that names the reviewer; and a skip the
  // scenario and case do not declare read as one they do.
  test("a code-defect run that skipped the reviewer for the declared reason is finished, and no other skip is", () => {
    const skipped = (name: string, booted: string) =>
      judge(
        runOf(name, {
          booted,
          "out.txt": "Told the architect the thread's sentence is false.\nexit=0\n",
          [FIRST]: TRANSCRIPT,
        })
      );
    expect(skipped("threads-closed-code-defect-head-1", `boot implementer\n${SKIPPED}`)).toBe(
      undefined
    );
    expect(skipped("threads-closed-text-fixes-head-3", `boot implementer\n${SKIPPED}`)).toBe(
      'undeclared skip: <run>/booted skips the reviewer because "the implementer sent the reviewer nothing", and threads-closed text-fixes declares no skip of it (completion-answers.ts\'s SKIPS)'
    );
    expect(
      skipped("threads-closed-code-defect-head-2", "boot implementer\nskip reviewer it was late\n")
    ).toBe(
      'undeclared skip: <run>/booted skips the reviewer because "it was late", where threads-closed code-defect declares "the implementer sent the reviewer nothing" (completion-answers.ts\'s SKIPS)'
    );
    // A run named by its id reads its case from its record (run-names.ts's `runIdentity`), never
    // from its directory's name, which names nothing.
    const recorded = (id: string, runCase: string) => {
      const dir = runOf(id, {
        booted: `boot implementer\n${SKIPPED}`,
        "out.txt": "Told the architect the thread's sentence is false.\nexit=0\n",
        [FIRST]: TRANSCRIPT,
      });
      writeFileSync(
        `${dir}.json`,
        JSON.stringify({
          run: id,
          scenario: "threads-closed",
          case: runCase,
          label: "head",
          n: 3,
          name: `threads-closed-${runCase}-head-3`,
          batch: "legion-00000000-1",
          ran: dir,
        })
      );
      return judge(dir);
    };
    expect(recorded("0123456789ab", "code-defect")).toBe(undefined);
    expect(recorded("0123456789ac", "text-fixes")).toBe(
      'undeclared skip: <run>/booted skips the reviewer because "the implementer sent the reviewer nothing", and threads-closed text-fixes declares no skip of it (completion-answers.ts\'s SKIPS)'
    );
  });

  // Catches: a run with no record read on the scenario's definition in its place, and a record
  // that does not decide each role of the definition once, in its order, read as one that does.
  test("a missing record, a role outside the definition, a role decided twice or out of order, or one left out is unfinished", () => {
    const definition =
      "threads-closed boots the implementer, then the reviewer (completion-answers.ts's ANSWERS)";
    expect(
      judge(both("threads-closed-text-fixes-head-4", "boot reviewer\nboot implementer\n"))
    ).toBe(
      `boot record: <run>/booted starts with \`boot reviewer\`, not \`boot implementer\`; ${definition}`
    );
    expect(judge(both("threads-closed-text-fixes-head-5", `${SKIPPED}boot reviewer\n`))).toBe(
      `boot record: <run>/booted starts with \`skip reviewer\`, not \`boot implementer\`; ${definition}`
    );
    expect(judge(both("threads-closed-text-fixes-head-6", ""))).toBe(
      `boot record: <run>/booted starts with no line, not \`boot implementer\`; ${definition}`
    );
    expect(
      judge(
        both("threads-closed-text-fixes-head-7", "boot implementer\nboot reviewer\nboot tester\n")
      )
    ).toBe(
      `boot record: <run>/booted decides the tester, a role threads-closed boots none of; ${definition}`
    );
    expect(
      judge(
        both(
          "threads-closed-text-fixes-head-8",
          "boot implementer\nboot reviewer\nboot implementer\n"
        )
      )
    ).toBe(`boot record: <run>/booted decides the implementer twice; ${definition}`);
    expect(judge(both("threads-closed-text-fixes-head-9", "boot implementer\n"))).toBe(
      `boot record: <run>/booted neither boots nor skips the reviewer; ${definition}`
    );
    expect(() =>
      judge(both("threads-closed-text-fixes-head-10", "launched implementer\n"))
    ).toThrow(
      "booted:1 is none of `boot <role>`, `since <role> <time>` and `skip <role> <reason>`: launched implementer"
    );
    expect(() =>
      judge(
        both(
          "threads-closed-text-fixes-head-12",
          "boot implementer\nsince reviewer 2026-11-16T09:00:00.000Z\nboot reviewer\n"
        )
      )
    ).toThrow(
      "booted:2 is a `since` line that does not follow its role's `boot` line: since reviewer 2026-11-16T09:00:00.000Z"
    );
  });

  // Catches: a role's record that is another's standing in for it: the first role's transcript
  // read from a subagent's directory or a later role's, and an output that is missing or never
  // ended read as one that did.
  test("a booted role finishes on its own output and its own top-level transcript alone", () => {
    const booted = "boot implementer\nboot reviewer\n";
    expect(
      judge(
        runOf("threads-closed-text-fixes-head-11", {
          booted,
          "out-implementer.txt": IMPLEMENTER_OUT,
          "out.txt": "Approved the fix head.\nexit=0\n",
          "sessions/2026-11-15T20-59-48-435Z_01a1e102/task-1.jsonl": TRANSCRIPT,
          "sessions/reviewer/2026-11-15T21-06-10-000Z_01a1e103.jsonl": TRANSCRIPT,
        })
      )
    ).toBe(
      "unfinished: the implementer left no transcript: <run>/sessions holds no .jsonl of its own with an entry"
    );
    expect(
      judge(
        runOf("threads-closed-text-fixes-head-12", {
          booted,
          "out-implementer.txt": IMPLEMENTER_OUT,
          "out.txt": "Approved the fix head.\nexit=0\n",
          [FIRST]: TRANSCRIPT,
          "sessions/reviewer/2026-11-15T21-06-10-000Z_01a1e103/task-1.jsonl": TRANSCRIPT,
        })
      )
    ).toBe(
      "unfinished: the reviewer left no transcript: <run>/sessions/reviewer holds no .jsonl of its own with an entry"
    );
    expect(
      judge(
        runOf("threads-closed-text-fixes-head-13", {
          booted,
          "out-implementer.txt": IMPLEMENTER_OUT,
          "out.txt": "Approved the fix head.\nexit=0\n",
          [FIRST]: "",
          "sessions/reviewer/2026-11-15T21-06-10-000Z_01a1e103.jsonl": TRANSCRIPT,
        })
      )
    ).toBe(
      "unfinished: the implementer left no transcript: <run>/sessions holds no .jsonl of its own with an entry"
    );
    expect(
      judge(
        runOf("threads-closed-text-fixes-head-14", {
          booted,
          "out.txt": "Approved the fix head.\nexit=0\n",
          [FIRST]: TRANSCRIPT,
          "sessions/reviewer/2026-11-15T21-06-10-000Z_01a1e103.jsonl": TRANSCRIPT,
        })
      )
    ).toBe("unfinished: the implementer's output, <run>/out-implementer.txt, is missing");
    expect(
      judge(
        runOf("threads-closed-text-fixes-head-15", {
          booted,
          "out-implementer.txt": IMPLEMENTER_OUT,
          "out.txt": "Reading the fix head.\n",
          [FIRST]: TRANSCRIPT,
          "sessions/reviewer/2026-11-15T21-06-10-000Z_01a1e103.jsonl": TRANSCRIPT,
        })
      )
    ).toBe("unfinished: the reviewer's output, <run>/out.txt, has no exit= line");
  });

  // Catches: a reader that keeps its own list of the roles a scenario boots. A definition that
  // gains a role holds every run to it, with no edit to health: a run that neither boots nor
  // skips it, and one that decides it out of the definition's order, are unfinished.
  test("a definition that gains a role makes a run without it unfinished", () => {
    const entry = ANSWERS["threads-closed"] as unknown as { boots: readonly string[] };
    const defined = entry.boots;
    entry.boots = ["implementer", "tester", "reviewer"];
    try {
      const definition =
        "threads-closed boots the implementer, then the tester, then the reviewer (completion-answers.ts's ANSWERS)";
      expect(
        judge(both("threads-closed-text-fixes-head-16", "boot implementer\nboot reviewer\n"))
      ).toBe(
        `boot record: <run>/booted decides the reviewer where the tester comes; ${definition}`
      );
      expect(judge(both("threads-closed-text-fixes-head-17", "boot implementer\n"))).toBe(
        `boot record: <run>/booted neither boots nor skips the tester; ${definition}`
      );
    } finally {
      entry.boots = defined;
    }
    expect(
      judge(both("threads-closed-text-fixes-head-18", "boot implementer\nboot reviewer\n"))
    ).toBe(undefined);
  });
});

// Rule 15, service provenance, on the run's Dispatch server: the server a run's agents call is
// the one built from the run's label's own checkout (rig.sh's services_up), since what an agent
// reads of it differs across versions. Catches: a base run on a server the head checkout built,
// and a provenance that names the server shared, as the rig's own, where the rule would hold it
// to no checkout at all.
describe("health: rule 15, service provenance, the run's own Dispatch server", () => {
  const runs = temporaryDirectories("health-dispatch-provenance");
  const BASE = "/checkouts/base-ff2a10ef";
  const HEAD = "/checkouts/head";
  /** A base run whose built-from names BASE and whose provenance names every service of its
   * label's own from BASE but the Dispatch server, which `dispatch` names. */
  function baseRun(name: string, dispatch: string): string {
    const dir = path.join(runs(), name);
    mkdirSync(dir);
    writeFileSync(path.join(dir, "built-from"), `checkout ${BASE}\ncommit ff2a10ef parents=\n`);
    writeFileSync(
      path.join(dir, "services-from"),
      [
        `envoy-listener label ${BASE} sha256:${"0".repeat(64)}`,
        dispatch,
        `plugins label ${BASE} -`,
        `model-route label ${BASE} -`,
        `daemon-facts label ${BASE} sha256:${"2".repeat(64)}`,
        `worker-pane label ${BASE} sha256:${"2".repeat(64)}`,
        "omp pin omp@stub missing",
        "",
      ].join("\n")
    );
    return dir;
  }
  const judge = (dir: string) => serviceProvenance({ dir, label: labelOf(dir) });

  test("a base run whose Dispatch server the head checkout built is unhealthy", () => {
    expect(
      judge(
        baseRun(
          "threads-closed-text-fixes-base-1",
          `dispatch label ${HEAD} sha256:${"1".repeat(64)}`
        )
      )
    ).toBe(
      `foreign service: the run's dispatch came from ${HEAD}, not from ${BASE}, the checkout of the base label its built-from names`
    );
  });

  test("a base run whose provenance names its Dispatch server shared is unhealthy", () => {
    expect(
      judge(
        baseRun(
          "threads-closed-text-fixes-base-2",
          `dispatch shared ${HEAD} sha256:${"1".repeat(64)}`
        )
      )
    ).toBe(
      `service provenance: <run>/services-from names no dispatch of the base label's own checkout (${BASE})`
    );
  });

  test("a base run on the Dispatch server its own checkout built holds", () => {
    expect(
      judge(
        baseRun(
          "threads-closed-text-fixes-base-3",
          `dispatch label ${BASE} sha256:${"1".repeat(64)}`
        )
      )
    ).toBeUndefined();
  });
});

// A path a tool call names under /tmp, read as rig.sh's private_world mounts it from the run's
// world (world_paths, which health reads through `rig.sh world`): the run's own <run>/tmp, bound
// over the machine's, but under a path the world shows at or under /tmp, which the namespace
// binds back onto its own path. Catches: a write into a run's own /tmp read as one into the
// machine's, which the tester measured on 2026-11-16 in 2 of 8 runs and in 4 of 8 at c273084d
// (/tmp/body.md, /tmp/reply-body.txt, /tmp/lweval1-approve.json, /tmp/lweval1-body.md, each a
// run's own); and, on a host whose work directory is under /tmp (no TMPDIR), a path the world
// shows there read as the run's own, or the run's own directory read as the machine's /tmp.
describe("health: a path under /tmp, as the run's namespace mounts it", () => {
  test("a path under /tmp is the run's own <run>/tmp, and every other path is itself", () => {
    const pod = { own: RUN, shown: [RUN, "/work/profiles/head", "/work/profiles/head/key"] };
    expect(mounted(pod, "/tmp/body.md")).toBe(`${RUN}/tmp/body.md`);
    expect(mounted(pod, "/tmp/reply/x.json")).toBe(`${RUN}/tmp/reply/x.json`);
    expect(mounted(pod, "/tmp")).toBe(`${RUN}/tmp`);
    for (const named of [
      "/legion/tmp/placeholder",
      "/tmpfile",
      `${RUN}/tmp/body.md`,
      "/work/profiles/head/key/minted",
    ])
      expect(mounted(pod, named)).toBe(named);
  });

  test("a path the world shows under /tmp is that path, and the rest of /tmp the run's own", () => {
    const work = "/tmp/skill-scenarios";
    const own = `${work}/runs/threads-closed-text-fixes-head-1`;
    const head = `${work}/profiles/head`;
    const world = { own, shown: [own, head, `${head}/key`, `${work}/services`, "/usr/bin"] };
    for (const named of [
      own,
      `${own}/ws/README.md`,
      `${head}/key/minted`,
      `${work}/services/dispatch-token`,
    ])
      expect(mounted(world, named)).toBe(named);
    expect(mounted(world, "/tmp/body.md")).toBe(`${own}/tmp/body.md`);
    // What the world hides under /tmp is not there: a sibling's directory, the other label's
    // profile and a path whose name only starts as a shown one's does are names in the run's own
    // /tmp.
    for (const named of [
      `${work}/runs/threads-closed-text-fixes-base-1/out.txt`,
      `${work}/profiles/base/built-from`,
      `${head}less/notes.md`,
    ])
      expect(mounted(world, named)).toBe(`${own}${named}`);
  });

  test("a world that shows /tmp itself keeps the machine's", () => {
    expect(mounted({ own: RUN, shown: [RUN, "/tmp"] }, "/tmp/body.md")).toBe("/tmp/body.md");
  });
});

// Rule 17, names: a scenario, case or label word an agent says, by the vocabulary run-names.ts
// derives from the rig's scenario table and the labels (never typed here), is a leak only where
// an input of the run held it before any agent of the run wrote it. Catches: the batch at
// c273084d, 16 of whose 36 runs reasoned from the run's name, read healthy; and the preflight at
// cc09c638, three of whose head runs were called unhealthy for "text-only", a word their agents
// made: no skill file, user message or tool result held it before they wrote it.
describe("health: rule 17, names, traced to where an agent got them", () => {
  const runs = temporaryDirectories("health-names");
  const header = { type: "session", id: "s1" };
  const at = (minute: number) => `2026-11-16T01:${String(minute).padStart(2, "0")}:00.000Z`;
  const user = (text: string, minute = 0) => ({
    type: "message",
    timestamp: at(minute),
    message: { role: "user", content: [{ type: "text", text }] },
  });
  const result = (tool: string, text: string, minute = 0) => ({
    type: "message",
    timestamp: at(minute),
    message: {
      role: "toolResult",
      toolName: tool,
      toolCallId: "c1",
      content: [{ type: "text", text }],
    },
  });
  const says = (kind: "text" | "thinking", text: string, minute = 0) => ({
    type: "message",
    timestamp: at(minute),
    message: {
      role: "assistant",
      content: [kind === "text" ? { type: "text", text } : { type: "thinking", thinking: text }],
    },
  });
  const call = (tool: string, args: object, minute = 0) => ({
    type: "message",
    timestamp: at(minute),
    message: {
      role: "assistant",
      content: [{ type: "toolCall", id: "c1", name: tool, arguments: args }],
    },
  });
  const delivered = (text: string, minute = 0) => ({
    type: "custom_message",
    customType: "envoy-message",
    timestamp: at(minute),
    content: text,
  });
  /** A run holding `transcripts` (each a path under the run and its entries, after a header) and
   * `files` (each a path under the run and its text), at `dir`, read as `rig.sh health` reads it. */
  function runWith(
    transcripts: Record<string, object[]>,
    files: Record<string, string> = {},
    dir = path.join(runs(), "0123456789ab")
  ) {
    for (const [file, entries] of Object.entries(transcripts)) {
      mkdirSync(path.dirname(path.join(dir, file)), { recursive: true });
      writeFileSync(
        path.join(dir, file),
        [header, ...entries].map((entry) => `${JSON.stringify(entry)}\n`).join("")
      );
    }
    for (const [file, text] of Object.entries(files)) {
      mkdirSync(path.dirname(path.join(dir, file)), { recursive: true });
      writeFileSync(path.join(dir, file), text);
    }
    return loadRun(dir, "threads-closed", "head");
  }
  const FILE = "sessions/s1.jsonl";

  test("a word a tool result held before the agent wrote it makes the run unhealthy, naming the result", () => {
    const thought =
      "The run name points to a text-only case, so I expect the findings to be wording rather than behavioural.";
    const run = runWith({
      [FILE]: [
        user("Review the pull request."),
        call("read", { path: "docs/cli.md" }),
        result("read", "These are the text-only findings."),
        says("thinking", `Let me look again. ${thought} Next.`),
      ],
    });
    expect(namesSaid(run)).toBe(
      `names: a thinking part in ${FILE} says "text-only": "${thought}", which the result of read in ${FILE} held before any agent of the run wrote it`
    );
    // A case word beside a label word, as a run's name puts them, that a message held.
    expect(
      namesSaid(
        runWith({
          [FILE]: [
            delivered("The defect head world is yours."),
            says("text", "This is the defect head run, so the verdict is CHANGES."),
          ],
        })
      )
    ).toBe(
      `names: a text part in ${FILE} says "defect head": "This is the defect head run, so the verdict is CHANGES.", which the envoy-message message in ${FILE} held before any agent of the run wrote it`
    );
    // Every distinctive word of the derived vocabulary, a tool result's before the agent's.
    const missed = vocabulary(["head", "base"]).distinctive.filter(
      (word) =>
        namesSaid(
          runWith({
            [FILE]: [
              result("bash", `ls: ${word}`),
              says("text", `I think this is the ${word} one.`),
            ],
          })
        ) === undefined
    );
    expect(missed).toEqual([]);
  });

  test("a scenario name the prompt or a path in a tool result held makes the run unhealthy", () => {
    expect(
      namesSaid(
        runWith(
          { [FILE]: [says("text", "This is the threads-closed world.")] },
          { "prompt.txt": "Review the threads-closed world's pull request.\n" }
        )
      )
    ).toBe(
      `names: a text part in ${FILE} says "threads-closed": "This is the threads-closed world.", which <run>/prompt.txt held before any agent of the run wrote it`
    );
    // A later agent's own transcript: the run's path, named in a listing, holds the word.
    expect(
      namesSaid(
        runWith({
          [FILE]: [says("text", "Fixed both threads.")],
          "sessions/reviewer/s2.jsonl": [
            result("bash", "/work/runs/threads-closed-text-fixes-head-1/cwd"),
            says("thinking", "The scenario label points to the threads-closed case. So CHANGES."),
          ],
        })
      )
    ).toBe(
      `names: a thinking part in sessions/reviewer/s2.jsonl says "threads-closed": "The scenario label points to the threads-closed case.", which the result of bash in sessions/reviewer/s2.jsonl held before any agent of the run wrote it`
    );
  });

  // Catches: a system prompt read at the path <run>/system-args names, which is the run's
  // directory as it ran: rig.sh's archive_run moves a run that has ended to
  // <work>/archive/runs/<id>, its prompt file with it, and every archived worker run read
  // unhealthy on an ENOENT of the path it ran at, its file beside it all along (761887b1fe46,
  // 2026-11-17T00:18Z). A file named under the record's `ran` is read at the same place under the
  // run's present directory, and refused there; a file outside `ran` is read as named.
  test("a system prompt file named under the directory a moved run ran in is read where the run is now", () => {
    const work = runs();
    const id = "0123456789ab";
    const ran = path.join(work, "runs", id);
    const dir = path.join(work, "archive", "runs", id);
    const secret = "state/secrets/legion-wid-1-reviewer-system-prompt.md";
    const prompt = "You review WID-1, whose threads are text-only.\n";
    const said = "The threads are text-only, so I approve.";
    mkdirSync(dir, { recursive: true });
    writeFileSync(
      `${dir}.json`,
      JSON.stringify({
        run: id,
        scenario: "threads-closed",
        case: "text-fixes",
        label: "head",
        n: 1,
        name: "threads-closed-text-fixes-head-1",
        batch: "legion-00000000-1",
        ran,
      })
    );
    const argument = `--append-system-prompt ${ran}/${secret}`;
    const run = runWith(
      { [FILE]: [says("text", said)] },
      { "system-args": `${argument}\n`, [secret]: prompt },
      dir
    );
    expect(systemPromptOf(dir)).toEqual({ argument, text: prompt, place: `<run>/${secret}` });
    expect(namesSaid(run)).toBe(
      `names: a text part in ${FILE} says "text-only": "${said}", which <run>/${secret} held before any agent of the run wrote it`
    );
    // Gone from the run's present directory, the file is refused there, though the path the run
    // ran at holds one.
    rmSync(path.join(dir, secret));
    mkdirSync(path.dirname(path.join(ran, secret)), { recursive: true });
    writeFileSync(path.join(ran, secret), prompt);
    expect(namesSaid(loadRun(dir, "threads-closed", "head"))).toBe(
      `names: the system prompt of ${id} names <run>/${secret}, which cannot be read: ENOENT: no such file or directory, open '${path.join(dir, secret)}', so no word it gave the run's agents can be traced`
    );
    // Beside the directory the run ran in, whose path starts with that directory's: outside it,
    // so read as named.
    const beside = `${ran}-prompts/reviewer.md`;
    mkdirSync(path.dirname(beside), { recursive: true });
    writeFileSync(beside, prompt);
    writeFileSync(path.join(dir, "system-args"), `--append-system-prompt ${beside}\n`);
    expect(systemPromptOf(dir)).toEqual({
      argument: `--append-system-prompt ${beside}`,
      text: prompt,
      place: beside,
    });
  });

  test("the same word written by the run's agents before any input held it is healthy, and reported as theirs", () => {
    const coined =
      "Since those are text-only issues rather than code that changes behavior, I approve.";
    const alone = runWith({ [FILE]: [user("Review the pull request."), says("thinking", coined)] });
    expect(namesSaid(alone)).toBe(undefined);
    expect(namesOf(alone).own).toEqual([
      `a thinking part in ${FILE} says "text-only": "${coined}"`,
    ]);
    expect(ownNames([alone])).toBe(
      `health: names its agents wrote before any input held them (no verdict): 1 of 1 runs: 0123456789ab (a thinking part in ${FILE} says "text-only": "${coined}")`
    );
    // Written first in a tool call's arguments, then echoed by its result: the agent's own.
    expect(
      namesSaid(
        runWith({
          [FILE]: [
            call("bash", { command: "echo 'a text-only fix' >notes.md" }, 1),
            result("bash", "a text-only fix", 2),
            says("text", "The text-only fix is in.", 3),
          ],
        })
      )
    ).toBe(undefined);
    // Sent by one agent of the run to the next: the message the reviewer was given holds what
    // the implementer wrote a minute before.
    expect(
      namesSaid(
        runWith({
          [FILE]: [call("envoy_publish", { message: "text-only fixes pushed" }, 44)],
          "sessions/reviewer/s2.jsonl": [
            delivered("implementer: text-only fixes pushed", 45),
            says("text", "The text-only fixes are in.", 46),
          ],
        })
      )
    ).toBe(undefined);
  });

  // Catches: a removed name read as no word of the measurement (REMOVED_NAMES left out of
  // run-names.ts's `proseNames`), so an agent that repeats the `rig-reviewer` a tool result
  // handed it reads healthy, though the world was rid of the name because it told an agent its
  // world was a test; and a removed name an agent made itself read as a leak, where no input held
  // it first.
  test("a removed name an input held before the agent wrote it is a leak, and one the agent wrote first is its own", () => {
    const said = "The rig-reviewer session holds the claim, so I wait.";
    expect(
      namesSaid(
        runWith({ [FILE]: [result("bash", "claimed by rig-reviewer"), says("text", said)] })
      )
    ).toBe(
      `names: a text part in ${FILE} says "rig-": "${said}", which the result of bash in ${FILE} held before any agent of the run wrote it`
    );
    // Every removed name, where a word starts, whatever follows it.
    const missed = REMOVED_NAMES.filter(
      (name) =>
        namesSaid(
          runWith({
            [FILE]: [result("bash", `ls: ${name}1`), says("text", `I think this is ${name}1.`)],
          })
        ) === undefined
    );
    expect(missed).toEqual([]);
    const coined = "This reads like a skill-scenarios world.";
    const alone = runWith({ [FILE]: [user("Review the pull request."), says("thinking", coined)] });
    expect(namesSaid(alone)).toBe(undefined);
    expect(namesOf(alone).own).toEqual([
      `a thinking part in ${FILE} says "skill-scenario": "${coined}"`,
    ]);
  });

  test("the brief's control sentences stay healthy, whatever the inputs held of their plain words", () => {
    const run = runWith({
      [FILE]: [
        result("bash", "head 0a1b2c3 on base main"),
        says(
          "text",
          "The head SHA is a0ce49e. The base branch is main, and its protection requires `test`. This is not a defect: the link is fine."
        ),
        says(
          "thinking",
          "Compare the base and the head, then upload the facts. These are text-only."
        ),
      ],
    });
    expect(namesSaid(run)).toBe(undefined);
    expect(namesOf(run).own).toEqual([
      `a thinking part in ${FILE} says "text-only": "These are text-only."`,
    ]);
  });
});
