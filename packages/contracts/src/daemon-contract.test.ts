import { afterAll, expect, test } from "bun:test";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { daemonContract } from "./daemon-contract";

// A plugin's contract in each place its code runs (c-entry-numbers): a packed plugin's manifest
// holds the field, a checkout counts its own fixtures, and an installed bundle that lacks the field
// refuses to load. Each case runs in a scratch tree of its own outside the repository, so the
// repository's own fixtures decide none of them.

const scratch = mkdtempSync(join(tmpdir(), "daemon-contract-"));
afterAll(() => rmSync(scratch, { force: true, recursive: true }));

/** A checkout's root under the scratch directory: `released` as its `released-contracts.json`,
 *  and `entries` entry fixtures beside it, or no `contracts/` directory at all for null. Answers
 *  the directory of a plugin's source in it, the `from` a test or a linked plugin passes. */
function checkout(
  name: string,
  released: readonly number[],
  entries: readonly string[] | null
): string {
  const root = join(scratch, name);
  const fixtures = join(root, "packages", "contracts", "fixtures", "daemon-api");
  mkdirSync(fixtures, { recursive: true });
  writeFileSync(join(fixtures, "released-contracts.json"), JSON.stringify(released));
  if (entries !== null) {
    mkdirSync(join(fixtures, "contracts"));
    for (const entry of entries) writeFileSync(join(fixtures, "contracts", entry), "{}");
  }
  const plugin = join(root, "packages", "pi-legion", "extensions");
  mkdirSync(plugin, { recursive: true });
  return plugin;
}

test("the manifest's field is the contract, wherever the code runs", () => {
  const inCheckout = checkout("declared", [46, 48, 232], ["20261125010101_probe.json"]);
  expect(daemonContract({ legion: { daemonApiVersion: 240 } }, inCheckout)).toBe(240);
  const installed = join(scratch, "declared-install", "pi-legion", "dist");
  mkdirSync(installed, { recursive: true });
  expect(daemonContract({ legion: { daemonApiVersion: 240 } }, installed)).toBe(240);
});

test("in a checkout a manifest with no field speaks L plus the count of entry fixtures", () => {
  // The greatest number, not the last one, is L.
  const entries = ["20261124235959_earlier.json", "20261125010101_later.json"];
  const two = checkout("two-entries", [232, 46, 48], entries);
  expect(daemonContract({ name: "@sjawhar/pi-legion" }, two)).toBe(234);
  // The cutover's tree holds no entry, and so no contracts/ directory.
  const none = checkout("no-entries", [46, 48, 232], null);
  expect(daemonContract({ name: "@sjawhar/pi-legion" }, none)).toBe(232);
});

test("a directory that is no checkout, with a manifest that holds no field, refuses", () => {
  // An installed bundle: dist/ of a plugin under a directory no checkout holds.
  const installed = join(scratch, "install", "node_modules", "@sjawhar", "pi-legion", "dist");
  mkdirSync(installed, { recursive: true });
  expect(() => daemonContract({ name: "@sjawhar/pi-legion" }, installed)).toThrow(
    `the plugin's package.json holds no legion.daemonApiVersion, which every pack writes, and ${installed} is in no checkout: no directory from it up holds packages/contracts/fixtures/daemon-api/released-contracts.json`
  );
});
