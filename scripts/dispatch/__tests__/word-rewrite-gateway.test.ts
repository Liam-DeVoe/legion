import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { gatewayModel } from "../word-rewrite/gateway.ts";

/** The gateway's own client, given a fetch, a key file and a wait of the test's own. */
describe("the model gateway's client", () => {
  let directory = "";
  let keyFile = "";
  const base = process.env.ANTHROPIC_BASE_URL;

  beforeEach(() => {
    directory = mkdtempSync(join(process.env.TMPDIR ?? tmpdir(), "word-rewrite-gateway-"));
    keyFile = join(directory, "key");
    writeFileSync(keyFile, "test-key\n");
    process.env.ANTHROPIC_BASE_URL = "http://gateway.test";
  });

  afterEach(() => {
    rmSync(directory, { recursive: true, force: true });
    if (base === undefined) delete process.env.ANTHROPIC_BASE_URL;
    else process.env.ANTHROPIC_BASE_URL = base;
  });

  /** A client whose fetch answers `answers` in turn, and what it sent and reported. */
  function client(answers: readonly Response[]) {
    const sent: string[] = [];
    const faults: string[] = [];
    const waits: number[] = [];
    const model = gatewayModel("fake", (detail) => faults.push(detail), {
      fetch: async (url) => {
        sent.push(url);
        const answer = answers[sent.length - 1];
        if (answer === undefined) throw new Error("no more answers");
        return answer;
      },
      keyFile,
      sleep: async (ms) => {
        waits.push(ms);
      },
    });
    return { model, sent, faults, waits };
  }

  const prompt = { system: "s", user: "u" };

  test("a 200 answer that is not JSON is reported once and not tried again (simplify defect 5)", async () => {
    const run = client([new Response("<html>a proxy page</html>", { status: 200 })]);
    expect(await run.model.ask(prompt)).toBeNull();
    expect(run.sent).toEqual(["http://gateway.test/v1/messages"]);
    expect(run.waits).toEqual([]);
    expect(run.faults).toEqual(["200 the answer is not JSON: <html>a proxy page</html>"]);
  });

  test("a 5xx is tried again, and the text of the answer that follows is the answer", async () => {
    const run = client([
      new Response("busy", { status: 503 }),
      Response.json({ content: [{ type: "text", text: '{"allowed": "quote"}' }] }),
    ]);
    expect(await run.model.ask(prompt)).toBe('{"allowed": "quote"}');
    expect(run.sent).toHaveLength(2);
    expect(run.waits).toHaveLength(1);
    expect(run.faults).toEqual([]);
  });
});
