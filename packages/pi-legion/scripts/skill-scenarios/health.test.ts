import { describe, expect, test } from "bun:test";
import { offWorldReach } from "./health";

/** The world of a run whose pane names only the loopback. */
const LOOPBACK = new Set(["127.0.0.1", "localhost", "::1"]);
const SESSION = "sessions/2026-11-14T00-00-00-000Z_s1.jsonl";
const API = "https://api.github.com/repos/cybermonklabs/legion";
const LINK = "https://github.com/cybermonklabs/legion/pull/2827";

/** Rule 13's reason for an agent that made `steps`, one tool call each, in that order. */
function reach(...steps: [tool: string, args: Record<string, unknown>][]): string | undefined {
  return offWorldReach(
    steps.map(([tool, args], at) => ({ file: SESSION, tool, args, at })),
    LOOPBACK
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
