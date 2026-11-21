/** The split of `e2e/acceptance/restricted-sweep.sh`, against the fake Dispatch of
 *  `restricted-sweep-fake.ts`: fills mode holds the full member's half where a full member's
 *  credential may be, a runner the member's half where none may be, and a writer the live check's
 *  writes. Each fake is alike, so fills one fake answered serve a runner or a writer of another. */

import { afterAll, describe, expect, test } from "bun:test";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";

import { rowLimit } from "./bounded-subprocess";
import {
  appInstallation,
  at,
  failures,
  fullCookie,
  fullToken,
  type Leak,
  memberCookie,
  stopFakes,
  sweep,
  tgt,
  verdictOf,
  withoutTools,
} from "./restricted-sweep-fake";
import { proxyReads } from "./restricted-sweep-index";

afterAll(stopFakes);

describe.skipIf(withoutTools)("the split into fills, a runner and a writer", () => {
  const fillsDirectories = temporaryDirectories("restricted-sweep-fills");
  const someRows = ["/api/v1/inbox", "/api/v1/issues/{key}/comments", appInstallation];

  /** A run of fills mode, and the file it was to write. Its proxy reads as the real one does, a
   *  browser's person alone, so its reads can fail only with the full member's cookie, which the
   *  run is given unless `withoutCookie`. Its live socket is served, for the agent view's full
   *  half. */
  async function fills(leaks: readonly Leak[] = [], withoutCookie = false) {
    const file = join(fillsDirectories(), "fills.tsv");
    const run = await sweep(leaks, {
      env: {
        SWEEP_MODE: "fills",
        SWEEP_MEMBER: "bob@outside.example",
        SWEEP_FILLS_FILE: file,
        SWEEP_FULL_COOKIE: withoutCookie ? undefined : fullCookie,
        SWEEP_RESTRICTED_COOKIE: undefined,
        SWEEP_MEMBER_SESSION: undefined,
        SWEEP_WRITE_ISSUE: undefined,
      },
      feed: { live: true },
      proxyReadsCookies: true,
      rows: someRows,
    });
    return { ...run, file };
  }

  /** A runner's run with FILE, its sockets served, and no full member's credential. */
  function runner(file: string, leaks: readonly Leak[] = []) {
    return sweep(leaks, {
      env: {
        SWEEP_FILLS_FILE: file,
        SWEEP_FULL_TOKEN: undefined,
        SWEEP_CONVERSATION_FILE: undefined,
        SWEEP_WRITE_ISSUE: undefined,
      },
      feed: { doc: true, live: true },
      rows: someRows,
    });
  }

  /** A writer's run with FILE's fills, writing every second for WRITE_SECONDS, with the full
   *  member's cookie for its direct messages. */
  function writer(file: string, writeSeconds: string, noise?: string) {
    return sweep([], {
      env: {
        SWEEP_MODE: "writer",
        SWEEP_FILLS_FILE: file,
        SWEEP_FULL_COOKIE: fullCookie,
        SWEEP_WRITE_SECONDS: writeSeconds,
        SWEEP_WRITE_EVERY: "1",
        SWEEP_RESTRICTED_COOKIE: undefined,
        SWEEP_MEMBER_SESSION: undefined,
        SWEEP_CONVERSATION_FILE: undefined,
      },
      feed: noise === undefined ? { live: true } : { live: true, noise },
    });
  }

  test(
    "fills mode writes the full member's half, makes no request as the member, and writes no credential",
    async () => {
      const { code, file, fullRequests, memberPaths, stdout } = await fills();
      expect(verdictOf(stdout)).toStartWith("FILLS: ");
      expect(code, stdout).toBe(0);
      expect(memberPaths).toEqual([]);
      expect(fullRequests).toBeGreaterThan(0);
      const text = readFileSync(file, "utf8");
      const lines = text.trimEnd().split("\n");
      expect(lines[0]).toBe("restricted-sweep-fills\t2");
      expect(lines).toContain("project\tTGT");
      expect(lines.filter((line) => /^origin\thttp:\/\/127\.0\.0\.1:\d+$/.test(line))).toHaveLength(
        1
      );
      expect(lines).toContain("member\tbob@outside.example");
      expect(lines).toContain("own\tOWN");
      expect(lines).toContain(`going_run\t${tgt.goingRun}`);
      expect(lines).toContain(`issue_title\t${tgt.title}`);
      expect(lines).toContain(`proxy_repo\t${tgt.repository}`);
      expect(lines).toContain(`dm_session\t${tgt.session}`);
      expect(lines).toContain(`dm_message\t${tgt.directMessage}`);
      expect(lines).toContain("can_fail\tGET /api/v1/issues/{key}/comments");
      for (const shape of proxyReads)
        expect(lines).toContain(`can_fail\tGET /api/github/rest/repos/{owner}/{repo}/${shape}`);
      expect(lines).toContain(`can_fail\tGET ${appInstallation}`);
      expect(lines).toContain("can_fail\t/ws/live agent view of {session_id}");
      for (const secret of [fullToken, fullCookie, memberCookie])
        expect(text).not.toContain(secret);
      expect(/^[\t\n\x20-\x7e]*$/.test(text)).toBe(true);
    },
    rowLimit("short")
  );

  test(
    "a runner with those fills and no full member's credential passes, names where the fills were made, and prints its live window",
    async () => {
      const { file } = await fills();
      const { code, fullRequests, stdout } = await runner(file);
      expect(fullRequests).toBe(0);
      expect(stdout).toMatch(
        /^fills {3}of TGT for bob@outside\.example, made at http:\/\/127\.0\.0\.1:\d+ -?\d+ s ago: \d+ values, \d+ rows that can fail$/m
      );
      expect(stdout).toMatch(
        /^live window \d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ \d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$/m
      );
      expect(stdout).toMatch(/^ok {6}GET \/api\/v1\/issues\/\{key\}\/comments: 404 \(can fail\)$/m);
      expect(stdout).toContain(
        `\nok      GET ${appInstallation}: 200, as a missing one is answered (can fail)\n`
      );
      expect(stdout).toContain(
        "\nok      /ws/live agent view of {session_id}: no one answers for it (can fail)\n"
      );
      expect(verdictOf(stdout)).toMatch(
        /^PASS: no route and neither socket gave the member a row of TGT; the live check holds once a writer's delivered write falls inside the live window \S+ \S+, which the tailnet leg joins$/
      );
      expect(code, stdout).toBe(0);
    },
    rowLimit("short")
  );

  // The control of the row above: the same runner, with a leak planted, fails.
  test(
    "the same runner fails where the member is answered a row of the project",
    async () => {
      const { file } = await fills();
      const { code, stdout } = await runner(file, [
        at(`/api/v1/issues/${tgt.issue}/comments`, 200, [{ id: tgt.comment }]),
      ]);
      expect(failures(stdout)).toEqual([
        `GET /api/v1/issues/{key}/comments: the member is answered 200 holding ${tgt.comment}, a row of TGT`,
      ]);
      expect(code).toBe(1);
    },
    rowLimit("short")
  );

  // The same runner, where the route that answers a hidden repository as a missing one answers the
  // member that the App is installed on the project's.
  test(
    "the same runner fails where a route that answers a hidden repository as a missing one answers the member otherwise",
    async () => {
      const { file } = await fills();
      const { code, stdout } = await runner(file, [
        at(`/api/v1/github/app-installation/${tgt.repository}`, 200, { installed: true }),
      ]);
      expect(failures(stdout)).toEqual([
        `GET ${appInstallation}: the member is answered 200 for TGT's object, otherwise than a missing one answered 200`,
      ]);
      expect(code).toBe(1);
    },
    rowLimit("short")
  );

  // A rate limit answers every repository alike, the hidden one and one nobody holds: two equal
  // answers of it say nothing of what the member may see, whatever the fills said of the full
  // member's half.
  test(
    "the same runner, its member rate-limited by the GitHub proxy for both repositories, is UNKNOWN",
    async () => {
      const { file } = await fills();
      const limited = {
        code: "GITHUB_RATE_LIMITED",
        error: "GitHub rate-limited the App; retry after 1m0s",
      };
      const { code, stdout } = await runner(file, [
        at(`/api/github/rest/repos/${tgt.repository}/pulls/1`, 503, limited),
        at("/api/github/rest/repos/acme/no-such-repo/pulls/1", 503, limited),
      ]);
      expect(failures(stdout)).toEqual([]);
      expect(stdout).toContain(
        "\nUNKNOWN GET /api/github/rest/repos/{owner}/{repo}/pulls/{number}: the member is answered 503, and 503 for a repository the App is not installed on, which no rule judges\n"
      );
      expect(verdictOf(stdout)).toStartWith("UNKNOWN: ");
      expect(code).toBe(2);
    },
    rowLimit("short")
  );

  // A proxy read the full member's half could not make fail leaves no fills, and a runner whose
  // fills say a proxy read cannot fail is UNKNOWN: neither is a pass of a read that could not fail.
  test(
    "fills mode whose proxy reads cannot fail, with no full member's cookie, writes no file, and names them",
    async () => {
      const { code, file, stdout } = await fills([], true);
      expect(verdictOf(stdout)).toStartWith("UNKNOWN: no fills were written");
      for (const shape of proxyReads) {
        expect(stdout).toContain(
          `\n  GET /api/github/rest/repos/{owner}/{repo}/${shape}: the full member is answered 503 for TGT's repository as for one the App is not installed on (503), so the read could not fail: the proxy reads the browser's person alone, whom SWEEP_FULL_COOKIE names\n`
        );
      }
      expect(code).toBe(2);
      expect(await Bun.file(file).exists()).toBe(false);
    },
    rowLimit("short")
  );

  test(
    "a runner whose fills hold no proxy read that could fail is UNKNOWN, naming it",
    async () => {
      const { file } = await fills();
      const pulls = "can_fail\tGET /api/github/rest/repos/{owner}/{repo}/pulls/{number}\n";
      const text = readFileSync(file, "utf8");
      expect(text).toContain(pulls);
      writeFileSync(file, text.replace(pulls, ""));
      const { code, stdout } = await runner(file);
      expect(failures(stdout)).toEqual([]);
      expect(stdout).toContain(
        "\nUNKNOWN GET /api/github/rest/repos/{owner}/{repo}/pulls/{number}: the member is answered 503, but SWEEP_FILLS_FILE holds no full member's read of it that could fail\n"
      );
      expect(verdictOf(stdout)).toBe(
        "UNKNOWN: nothing reached the member, but this run did not prove it, for the 1 reasons above"
      );
      expect(code).toBe(2);
    },
    rowLimit("short")
  );

  // Whole fills hold every fill: a runner given fills without one would leave the rows that read
  // it out, the proxy reads or the direct message's, and pass on the rest.
  test.each(["proxy_repo", "dm_message"])(
    "a runner whose fills hold no %s line is UNKNOWN, naming it, before its first request",
    async (name) => {
      const { file } = await fills();
      const text = readFileSync(file, "utf8");
      const lines = text.split("\n").filter((line) => !line.startsWith(`${name}\t`));
      expect(lines.length).toBe(text.split("\n").length - 1);
      writeFileSync(file, lines.join("\n"));
      const { code, memberPaths, stdout } = await runner(file);
      expect(verdictOf(stdout)).toBe(
        `UNKNOWN: SWEEP_FILLS_FILE lacks fills that whole fills hold: ${name}`
      );
      expect(memberPaths).toEqual([]);
      expect(code).toBe(2);
    },
    rowLimit("short")
  );

  test(
    "fills mode that cannot prove a row writes no file, and is UNKNOWN",
    async () => {
      const { code, file, stdout } = await fills([]).then(async (first) => {
        // A second run at the same path, where the lookup finds no handoff, must remove the first.
        const run = await sweep([], {
          env: {
            SWEEP_MODE: "fills",
            SWEEP_MEMBER: "bob@outside.example",
            SWEEP_FILLS_FILE: first.file,
            SWEEP_FULL_COOKIE: fullCookie,
            SWEEP_RESTRICTED_COOKIE: undefined,
            SWEEP_MEMBER_SESSION: undefined,
            SWEEP_WRITE_ISSUE: undefined,
          },
          feed: { live: true },
          omit: new Set(["handoff"]),
          rows: someRows,
        });
        return { ...run, file: first.file };
      });
      expect(verdictOf(stdout)).toStartWith("UNKNOWN: no fills were written");
      expect(stdout).toContain("\n  handoff: no issue of the first 1 holds a handoff\n");
      expect(code).toBe(2);
      expect(await Bun.file(file).exists()).toBe(false);
    },
    rowLimit("short")
  );

  // Each write is a comment on the write issue and a direct message to the fills' session of the
  // project, and is delivered once the full member's own feed is sent both.
  test(
    "a writer prints each write delivered to the full member's own feed, and exits 0",
    async () => {
      const { file } = await fills();
      const { code, stdout } = await writer(file, "3");
      const writes = stdout.split("\n").filter((line) => line.startsWith("write "));
      expect(writes.length, stdout).toBeGreaterThan(0);
      for (const line of writes) {
        expect(line).toMatch(
          /^write \d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ [0-9a-f-]{36} [0-9a-f-]{36} delivered$/
        );
      }
      expect(stdout).toContain(
        `\nwriter  a comment on ${tgt.issue} and a direct message to ${tgt.session} every 1 s for 3 s, each held to the full member's own feed\n`
      );
      expect(verdictOf(stdout)).toBe(
        `WRITER: ${writes.length} writes, each delivered to the full member's own feed`
      );
      expect(code).toBe(0);
    },
    rowLimit("short")
  );

  // The control of the row above: a feed that is sent the project's events but not the comment.
  test(
    "a writer whose comment the full member's feed is not sent prints it not delivered, and exits 1",
    async () => {
      const { file } = await fills();
      const { code, stdout } = await writer(file, "1", "another event of the project");
      expect(stdout).toMatch(/^write \S+ [0-9a-f-]{36} [0-9a-f-]{36} not delivered$/m);
      expect(verdictOf(stdout)).toMatch(/^WRITER: \d+ writes, \d+ not delivered/);
      expect(code).toBe(1);
    },
    rowLimit("short")
  );
});
