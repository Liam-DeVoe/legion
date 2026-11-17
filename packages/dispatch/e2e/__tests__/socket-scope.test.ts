import { expect, test } from "bun:test";
import { join } from "node:path";

// e2e/acceptance/ci-check.sh hands socket-scope.ts its DISPATCH_URL as `<server>`, and prints its
// stderr when it fails. That URL may carry a user and a password: a value that is no http or
// https URL with a host is refused naming its length alone, before the URL parser's error, which
// holds the value whole, can be printed. The password is made for this run.
test("a <server> that is no http or https URL with a host is refused naming its length alone", async () => {
  const password = crypto.randomUUID();
  const said: { readonly code: number; readonly output: string }[] = [];
  const values = [
    `http://made-up-user:${password}@[bad`,
    `made-up-user:${password}@dispatch.example`,
  ];
  for (const server of values) {
    const child = Bun.spawn(
      [
        process.execPath,
        join(import.meta.dir, "..", "acceptance", "socket-scope.ts"),
        server,
        "ACME-1",
        "4",
        join(import.meta.dir, "no-such-cookie-file"),
      ],
      { stdout: "pipe", stderr: "pipe" }
    );
    const [code, stdout, stderr] = await Promise.all([
      child.exited,
      new Response(child.stdout).text(),
      new Response(child.stderr).text(),
    ]);
    said.push({ code, output: stdout + stderr });
  }
  expect(said).toEqual(
    values.map((server) => ({
      code: 2,
      output: `<server> must be an http or https URL: its value is ${server.length} characters long\n`,
    }))
  );
});
