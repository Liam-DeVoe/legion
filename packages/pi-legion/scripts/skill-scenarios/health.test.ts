import { describe, expect, test } from "bun:test";
import { offWorldReach } from "./health";

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
