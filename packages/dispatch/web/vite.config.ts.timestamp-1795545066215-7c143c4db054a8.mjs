// web/vite.config.ts
import { createRequire as createRequire2 } from "node:module";
import { fileURLToPath } from "node:url";
import tailwindcss from "file:///legion/workspaces/cybermonklabs/legion/infra-1109/node_modules/.bun/@tailwindcss+vite@4.3.3+65512b89bd1830ee/node_modules/@tailwindcss/vite/dist/index.mjs";
import react from "file:///legion/workspaces/cybermonklabs/legion/infra-1109/node_modules/.bun/@vitejs+plugin-react@4.7.0+65512b89bd1830ee/node_modules/@vitejs/plugin-react/dist/index.js";
import { defineConfig } from "file:///legion/workspaces/cybermonklabs/legion/infra-1109/node_modules/.bun/vite@5.4.21+979a5299fa474f04/node_modules/vite/dist/node/index.js";

// ../../scripts/third-party-notices.ts
import { existsSync, realpathSync } from "node:fs";
import { readdir, readFile, realpath, stat, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, isAbsolute, join, resolve, sep } from "node:path";
import parseSpdxExpression from "file:///legion/workspaces/cybermonklabs/legion/infra-1109/node_modules/.bun/spdx-expression-parse@3.0.1/node_modules/spdx-expression-parse/index.js";
import spdxLicenses from "file:///legion/workspaces/cybermonklabs/legion/infra-1109/node_modules/.bun/spdx-license-list@6.12.0/node_modules/spdx-license-list/full.js";
import { z } from "file:///legion/workspaces/cybermonklabs/legion/infra-1109/node_modules/.bun/zod@4.3.6/node_modules/zod/index.js";
var LICENSE_FILE = /^(licen[cs]e|copying)([.-].*)?$/i;
var NOTICE_FILE = /^notice([.-].*)?$/i;
var NODE_MODULES = `${sep}node_modules${sep}`;
function installedRootOf(modulePath) {
  const at = modulePath.lastIndexOf(NODE_MODULES);
  if (at < 0) return null;
  const rest = modulePath.slice(at + NODE_MODULES.length).split(sep);
  const depth = rest[0]?.startsWith("@") ? 2 : 1;
  return modulePath.slice(0, at + NODE_MODULES.length) + rest.slice(0, depth).join(sep);
}
async function readManifest(directory) {
  try {
    return JSON.parse(await readFile(join(directory, "package.json"), "utf8"));
  } catch (error) {
    if (error.code === "ENOENT") return null;
    throw error;
  }
}
async function licensedWorkspaceRootOf(modulePath) {
  let directory = dirname(modulePath);
  while (directory !== dirname(directory)) {
    const manifest = await readManifest(directory);
    if (manifest !== null) {
      if ("workspaces" in manifest) return null;
      const names = await readdir(directory);
      return names.some((name) => LICENSE_FILE.test(name)) ? directory : null;
    }
    directory = dirname(directory);
  }
  return null;
}
var PackageIdentity = z.object({ name: z.string().min(1), version: z.string().min(1) });
var LicenseObject = z.object({ type: z.string() });
var PackageLicense = z.object({
  license: z.union([z.string(), LicenseObject]).nullable().optional(),
  licenses: z.array(LicenseObject).min(1).optional(),
  repository: z.union([z.string(), z.object({ url: z.string() })]).optional().catch(void 0),
  homepage: z.string().optional().catch(void 0)
});
function licenseLeaves(expression) {
  if ("license" in expression) return [expression];
  return [...licenseLeaves(expression.left), ...licenseLeaves(expression.right)];
}
function standardTextsFor(expression, license, label) {
  return licenseLeaves(expression).map((leaf) => {
    const text = spdxLicenses[leaf.license]?.licenseText;
    if ("exception" in leaf || text === void 0) {
      throw new Error(
        `${label} declares ${license}, whose terms are not on the SPDX License List, and ships no license file; find its terms before bundling it`
      );
    }
    return { id: leaf.license, text: text.trim() };
  });
}
async function readPackage(root) {
  const manifest = JSON.parse(await readFile(join(root, "package.json"), "utf8"));
  const identity = PackageIdentity.safeParse(manifest);
  if (!identity.success) {
    throw new Error(`${root}/package.json names no package name and version`);
  }
  const { name, version } = identity.data;
  const label = `${name}@${version}`;
  const fields = PackageLicense.safeParse(manifest);
  if (!fields.success) {
    throw new Error(
      `${label} declares its license in a shape npm does not define (${fields.error.issues.map((issue) => issue.path.join(".")).join(", ")}); find its terms before bundling it`
    );
  }
  const { license: declared, licenses, repository, homepage } = fields.data;
  const license = typeof declared === "string" ? declared : declared?.type ?? licenses?.map((entry) => entry.type).join(" OR ") ?? null;
  const entries = (await readdir(root, { withFileTypes: true })).filter((entry) => entry.isFile()).map((entry) => entry.name).sort();
  const licenseNames = entries.filter((entry) => LICENSE_FILE.test(entry));
  const names = entries.filter((entry) => LICENSE_FILE.test(entry) || NOTICE_FILE.test(entry));
  const files = await Promise.all(
    names.map(async (file) => ({
      name: file,
      text: (await readFile(join(root, file), "utf8")).trim()
    }))
  );
  const standardTexts = [];
  if (license === null) {
    if (licenseNames.length === 0) {
      throw new Error(
        `${label} declares no license and ships no license file; find its terms before bundling it`
      );
    }
  } else {
    let expression;
    try {
      expression = parseSpdxExpression(license);
    } catch {
      throw new Error(
        `${label} declares an unrecognized license ${license}; find its terms before bundling it`
      );
    }
    if (licenseNames.length === 0) {
      standardTexts.push(...standardTextsFor(expression, license, label));
    }
  }
  const source = (typeof repository === "string" ? repository : repository?.url) ?? homepage ?? null;
  return { name, version, license, source, files, standardTexts };
}
async function thirdPartyNotices(inputs, base) {
  const roots = /* @__PURE__ */ new Set();
  for (const input of inputs) {
    const modulePath = await realpath(resolve(base, input));
    const root = installedRootOf(modulePath) ?? await licensedWorkspaceRootOf(modulePath);
    if (root !== null) roots.add(root);
  }
  const packages = /* @__PURE__ */ new Map();
  for (const root of roots) {
    const bundled = await readPackage(root);
    packages.set(`${bundled.name}@${bundled.version}`, bundled);
  }
  const sorted = [...packages].sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0);
  const sections = sorted.map(([key, bundled]) => {
    const lines = [key, `License: ${bundled.license ?? "(not declared in package.json)"}`];
    if (bundled.source !== null) lines.push(`Source: ${bundled.source}`);
    for (const standard of bundled.standardTexts) {
      lines.push(
        "",
        `--- ${standard.id}: the SPDX License List's standard text (the package ships no license file) ---`,
        "",
        standard.text
      );
    }
    for (const file of bundled.files) {
      lines.push("", `--- ${file.name} ---`, "", file.text);
    }
    return lines.join("\n");
  });
  const rule = `

${"=".repeat(80)}

`;
  return "Third-party software inlined into the bundles in this directory, with each package's license.\n" + rule.trimStart() + sections.join(rule) + "\n";
}
function packageRoot(name, from) {
  for (let directory = resolve(from); ; directory = dirname(directory)) {
    const candidate = join(directory, "node_modules", name);
    if (existsSync(join(candidate, "package.json"))) return realpathSync(candidate);
    if (directory === dirname(directory)) {
      throw new Error(`no package ${name} is installed above ${from}`);
    }
  }
}
function viteRuntimeGeneratedCode(viteRoot) {
  return { id: /^\0vite\//, packages: [viteRoot] };
}
var TAILWIND_JS_DIRECTIVE = /@(?:plugin|config)\s+["']([^"']+)["']/g;
async function viteBundleInputs(build, bundle, root, generated) {
  const file = (id) => resolve(root, id.replace(/\?.*$/, ""));
  const inputs = /* @__PURE__ */ new Set();
  const unaccounted = /* @__PURE__ */ new Set();
  const generatedBy = (id) => {
    const rule = generated.find((candidate) => candidate.id.test(id));
    if (rule === void 0) unaccounted.add(id);
    else for (const directory of rule.packages) inputs.add(join(directory, "package.json"));
  };
  for (const output of Object.values(bundle)) {
    if (output.type === "chunk") {
      for (const id of Object.keys(output.modules)) {
        if (isAbsolute(id)) inputs.add(file(id));
        else generatedBy(id);
      }
    } else if (output.originalFileNames.length === 0) {
      generatedBy(output.fileName);
    } else {
      for (const id of output.originalFileNames) inputs.add(file(id));
    }
  }
  if (unaccounted.size > 0) {
    throw new Error(
      `the build emits ${JSON.stringify([...unaccounted])}, which no source file or known generator accounts for; name the package that generates each`
    );
  }
  const modules = new Set([...build.getModuleIds()].filter((id) => isAbsolute(id)).map(file));
  for (const id of build.getWatchFiles?.() ?? []) {
    const path = file(id);
    if (!modules.has(path) && (await stat(path).catch(() => null))?.isFile()) inputs.add(path);
  }
  for (const input of [...inputs].filter((path) => path.endsWith(".css"))) {
    const resolveFrom = createRequire(input).resolve;
    for (const [, specifier] of (await readFile(input, "utf8")).matchAll(TAILWIND_JS_DIRECTIVE)) {
      inputs.add(resolveFrom(specifier));
    }
  }
  return inputs;
}
if (import.meta.main) {
  const args = process.argv.slice(2);
  const out = args.pop();
  if (out === void 0 || args.length === 0) {
    process.stderr.write("usage: bun scripts/third-party-notices.ts <metafile>... <out>\n");
    process.exit(2);
  }
  const inputs = [];
  for (const metafile of args) {
    const parsed = JSON.parse(await readFile(metafile, "utf8"));
    inputs.push(...Object.keys(parsed.inputs));
  }
  await writeFile(out, await thirdPartyNotices(inputs, process.cwd()));
}

// web/vite.config.ts
var __vite_injected_original_import_meta_url = "file:///legion/workspaces/cybermonklabs/legion/infra-1109/packages/dispatch/web/vite.config.ts";
var proofEditor = createRequire2(
  fileURLToPath(new URL("../../proof-editor/src/lib-headless.ts", __vite_injected_original_import_meta_url))
);
var characterReferenceDecoder = createRequire2(
  proofEditor.resolve("micromark-core-commonmark")
).resolve("decode-named-character-reference");
var vite = packageRoot("vite", fileURLToPath(new URL(".", __vite_injected_original_import_meta_url)));
var GENERATED_CODE = [
  viteRuntimeGeneratedCode(vite),
  { id: /^\0(commonjsHelpers\.js|commonjs-dynamic-modules)$/, packages: [vite] },
  { id: /^\0\/.*\?commonjs-(module|exports)$/, packages: [] }
];
function thirdPartyNoticesFile() {
  let root = "";
  const workerFiles = /* @__PURE__ */ new Set();
  const workerInputs = /* @__PURE__ */ new Set();
  return {
    page: {
      name: "third-party-notices",
      apply: "build",
      configResolved(config) {
        root = config.root;
      },
      async generateBundle(_options, bundle) {
        const pageBundle = Object.fromEntries(
          Object.entries(bundle).filter(([fileName]) => !workerFiles.has(fileName))
        );
        const inputs = await viteBundleInputs(this, pageBundle, root, GENERATED_CODE);
        for (const input of workerInputs) inputs.add(input);
        this.emitFile({
          type: "asset",
          fileName: "THIRD_PARTY_NOTICES.txt",
          source: await thirdPartyNotices(inputs, root)
        });
      }
    },
    worker: () => ({
      name: "third-party-notices-worker",
      apply: "build",
      async generateBundle(_options, bundle) {
        for (const fileName of Object.keys(bundle)) workerFiles.add(fileName);
        for (const input of await viteBundleInputs(this, bundle, root, GENERATED_CODE)) {
          workerInputs.add(input);
        }
      }
    })
  };
}
var notices = thirdPartyNoticesFile();
var vite_config_default = defineConfig({
  root: "web",
  plugins: [react(), tailwindcss(), notices.page],
  // The Mermaid worker (features/refs/mermaid-render.worker.ts) is created with
  // `{ type: "module" }`, so it is bundled as an ES module.
  worker: { format: "es", plugins: () => [notices.worker()] },
  define: {
    // Keep the entry chunk distinct across deployments, including no-source-change image rebuilds.
    // DISPATCH_BUILD wins over GITHUB_SHA, which CI sets on every step, so one job can build two
    // distinct dashboards of one commit (`bun run e2e:multiprocess`'s second build).
    __DISPATCH_BUILD__: JSON.stringify(
      process.env.DISPATCH_BUILD ?? process.env.GITHUB_SHA ?? Date.now().toString()
    )
  },
  resolve: {
    // The source-only editor and Dispatch both reach this constructor. Historical-version
    // rendering passes a Proof document to Dispatch's DOMSerializer, so Vite must emit one
    // module identity rather than one per workspace import path.
    dedupe: ["prosemirror-model"],
    alias: {
      "decode-named-character-reference": characterReferenceDecoder,
      // Every subpath of `@legion/contracts` this tree reads, named before the package itself:
      // Vite's alias matches a prefix, so `@legion/contracts` alone would resolve every subpath
      // to a path inside `index.ts`. The subpaths are what keeps the barrel out of this bundle
      // - one import of it reaches every module of the package, and rollup may drop none whose
      // top level builds anything, which is how the whole `DISPATCH_TEXT_REFERENCES` table and
      // zod's schema modules were in the entry chunk of a page that reads neither
      // (dispatch://INFRA-1109, R13). Each is published by that package's own `exports`, which
      // is what Bun, tsc and every other consumer read. Three keys are read by no shipped module
      // of this bundle today - `dispatch-api` by nothing in the repository,
      // `dispatch-text-references` by three Bun tests, which resolve through `exports` and never
      // see this config, and `dispatch-tools` by a type import alone (`theme/classes.ts`, which
      // the build erases) and by browser rows, which Playwright resolves through `exports` too -
      // and the three are kept so that a value import of one resolves to its module rather than
      // being rewritten into `index.ts` by the bare key below it.
      "@legion/contracts/actor-label": fileURLToPath(
        new URL("../../contracts/src/actor-label.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/contracts/agent-stream": fileURLToPath(
        new URL("../../contracts/src/agent-stream.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/contracts/claim-holds": fileURLToPath(
        new URL("../../contracts/src/claim-holds.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/contracts/dispatch-api": fileURLToPath(
        new URL("../../contracts/src/dispatch-api.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/contracts/dispatch-api-constants": fileURLToPath(
        new URL("../../contracts/src/dispatch-api-constants.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/contracts/dispatch-api-graph": fileURLToPath(
        new URL("../../contracts/src/dispatch-api-graph.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/contracts/dispatch-block-ref": fileURLToPath(
        new URL("../../contracts/src/dispatch-block-ref.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/contracts/dispatch-href": fileURLToPath(
        new URL("../../contracts/src/dispatch-href.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/contracts/dispatch-text-references": fileURLToPath(
        new URL("../../contracts/src/dispatch-text-references.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/contracts/dispatch-text-rules": fileURLToPath(
        new URL("../../contracts/src/dispatch-text-rules.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/contracts/legion-roles": fileURLToPath(
        new URL("../../contracts/src/legion-roles.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/contracts/related-forms": fileURLToPath(
        new URL("../../contracts/src/related-forms.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/contracts/dispatch-snippet": fileURLToPath(
        new URL("../../contracts/src/dispatch-snippet.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/contracts/dispatch-tools": fileURLToPath(
        new URL("../../contracts/src/dispatch-tools.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/contracts/dispatch-tools-constants": fileURLToPath(
        new URL("../../contracts/src/dispatch-tools-constants.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/contracts": fileURLToPath(new URL("../../contracts/src/index.ts", __vite_injected_original_import_meta_url)),
      "@legion/proof-editor/document-json": fileURLToPath(
        new URL("../../proof-editor/src/document-json.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/proof-editor/headless": fileURLToPath(
        new URL("../../proof-editor/src/lib-headless.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/proof-editor/mermaid": fileURLToPath(
        new URL("../../proof-editor/src/mermaid-svg.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/proof-editor/mermaid-render": fileURLToPath(
        new URL("../../proof-editor/src/mermaid-render.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/proof-editor/style.css": fileURLToPath(
        new URL("../../proof-editor/src/lib.css", __vite_injected_original_import_meta_url)
      ),
      "@legion/proof-editor/block-nesting": fileURLToPath(
        new URL("../../proof-editor/src/block-nesting.ts", __vite_injected_original_import_meta_url)
      ),
      "@legion/proof-editor": fileURLToPath(
        new URL("../../proof-editor/src/lib.ts", __vite_injected_original_import_meta_url)
      ),
      // The upstream package declares no main, no module, no types, no exports and no files,
      // so it publishes no entry point at all and the editor's upstream imports reach its
      // src/ by path. Same mapping as packages/proof-editor/tsconfig.json, which Bun and tsc read.
      "proof-sdk-upstream/src": fileURLToPath(
        new URL("../../proof-editor/node_modules/proof-sdk-upstream/src", __vite_injected_original_import_meta_url)
      )
    }
  },
  build: {
    outDir: "dist",
    // The entry - the one `assets/index-*.js` that dist/index.html names - read 499,485 bytes,
    // measured with `bun run build:web` and a 39-character `DISPATCH_BUILD` at `4935c4af` (the
    // same at the merges 4548096d and e15ee5e4) on 2026-11-23; the build id is in the entry, so
    // CI's 40-character commit writes one byte more. On that day the default branch's own build
    // at `d795b25c` writes 1,144,188, before dispatch://INFRA-1109
    // split the shell: the Inbox and its peek, the ask card, the search bar, the Create issue
    // dialog, the shortcut sheet, the reference-preview layer, the event-refresh map, the headless
    // Markdown engine the shell warms at boot, the new issue page's frame shell, the pending-writes
    // indicator, the kept writes and the kinds of write are each a chunk of their own now, and
    // `@legion/contracts`' subpaths keep that package's reference table and zod's schema modules
    // out of a page that reads neither - in the editor's chunks too, which import that package
    // by subpath alone (`packages/proof-editor/src/block-pointers.ts`).
    // The merge of 4b22ece4 read 500,672 bytes, over the budget; the cut in `01fc0e41` brought it
    // to 499,380, and `97c0cde6`'s focus helper added 105, so `4935c4af` reads those 499,485 bytes,
    // 515 under the budget. That cut made two moves. The Inbox's `Blocked on you` strip and the
    // project page's pill left the entry for a file of
    // their own, `features/inbox/BlockedOnYouLink.tsx`, a chunk of 866 bytes the Inbox's and the
    // project page's chunks share, and without that move the entry at `01fc0e41` measured 500,104
    // bytes. The modulepreload polyfill, 710 bytes of the entry, is off, for the reason beside
    // `modulePreload` below.
    // The other sizes here were measured the same way at `53286717` the same day: the largest
    // chunk is ECharts (an `index-*.js` of 1,134,772 bytes raw), which only the Delivery page
    // loads.
    // The loading box and the alert of the surfaces a key opens
    // (`features/shell/asked-surface.tsx`, review 6034817973) then took the entry to 500,530
    // bytes, over the budget again. The rail's pinned agents answered it: they draw a session
    // from `AgentName.tsx`'s session half alone (`features/refs/SessionName.tsx`), so the rest of
    // that module is a chunk the pages' chunks share, and the entry read 497,401 bytes with a
    // 39-character `DISPATCH_BUILD` on 2026-11-24. After the changes since, Details' one Escape
    // rule among them (`escapeClosesDrawer`, `features/shell/useDialog.ts`), the entry read
    // 497,933 bytes with a 39-character `DISPATCH_BUILD` the same day, and the two lists' alert
    // for a render error (`features/writes/write-hosts.tsx`) takes it to 498,553. The sign-in
    // page's list of kept writes, in the entry so that its warning depends on no chunk
    // (`features/writes/KeptWrites.tsx`), takes it to 499,583. The merge of the default branch's
    // correction and deletion of a post (#3512) took it to 500,975, over the budget. Three moves
    // then took code that no first paint draws out of the entry, none of them a new lazy load, and
    // the entry reads 498,776 bytes with a 39-character `DISPATCH_BUILD` on 2026-11-24: a held
    // send refreshes a comment list under `ownerCommentsQuery`'s key, so
    // `features/conversation/composer-model.ts` is out of the entry (411 bytes); the
    // rail's state mark reads its relative time from `features/refs/relative-time.ts`, so
    // `<Timestamp>` is a chunk the pages' chunks share (646 bytes); and the recent items read a
    // document's route from `features/refs/routes.ts`, so the search bar's chip
    // (`features/search/bar-scope.ts`) is a chunk that the bar and the issue list share
    // (1,142 bytes). That left 498,776 bytes; the correction's mark then joined the event map's
    // chunk (`api/event-refresh-load.ts`), and the entry reads 498,590.
    // `web/src/asset-sizes-in-build.ts` holds the entry to 500,000 bytes and keeps its history,
    // and `bun run check:asset-sizes` reads it again; this `chunkSizeWarningLimit` is Vite's own
    // warning and is over the ECharts chunk alone.
    // beautiful-mermaid and ELK live only in the Mermaid worker's bundle
    // (features/refs/mermaid-render.worker.ts, 1,565,608 bytes), fetched once a body or a
    // document holds a mermaid block; Vite emits it as an asset and holds it to no chunk limit.
    // The editor's lazy chunk (`lib-*.js`, 188,972 bytes) carries none of the renderer.
    chunkSizeWarningLimit: 1200,
    // No modulepreload polyfill. Vite's copy of it is 710 bytes of the entry chunk, and it does
    // nothing in a browser that has `<link rel=modulepreload>`: Chrome 66+, Safari 17+ and
    // Firefox 115+. Every browser without that link also lacks `Promise.withResolvers`, which
    // only Chrome 119+, Safari 17.4+ and Firefox 121+ have, and the app's own code calls it: each
    // write the page makes (`run` in `features/writes/write-store.ts`) and each upload
    // (`xhrUpload` in `api/client.ts`). So in those browsers every write fails as the reader
    // makes it, with the polyfill or without it: they do not run the app, and the polyfill acted
    // only in browsers the app already excludes. No browser that runs the app changes without it.
    // Turned off for the entry's budget (dispatch://INFRA-1109, 2026-11-23).
    modulePreload: { polyfill: false },
    emptyOutDir: true
  },
  server: {
    proxy: {
      "/api": "http://localhost:8766",
      "/auth": "http://localhost:8766",
      "/ws": {
        target: "ws://localhost:8766",
        ws: true
      }
    }
  }
});
export {
  vite_config_default as default
};
//# sourceMappingURL=data:application/json;base64,ewogICJ2ZXJzaW9uIjogMywKICAic291cmNlcyI6IFsid2ViL3ZpdGUuY29uZmlnLnRzIiwgIi4uLy4uL3NjcmlwdHMvdGhpcmQtcGFydHktbm90aWNlcy50cyJdLAogICJzb3VyY2VzQ29udGVudCI6IFsiY29uc3QgX192aXRlX2luamVjdGVkX29yaWdpbmFsX2Rpcm5hbWUgPSBcIi9sZWdpb24vd29ya3NwYWNlcy9jeWJlcm1vbmtsYWJzL2xlZ2lvbi9pbmZyYS0xMTA5L3BhY2thZ2VzL2Rpc3BhdGNoL3dlYlwiO2NvbnN0IF9fdml0ZV9pbmplY3RlZF9vcmlnaW5hbF9maWxlbmFtZSA9IFwiL2xlZ2lvbi93b3Jrc3BhY2VzL2N5YmVybW9ua2xhYnMvbGVnaW9uL2luZnJhLTExMDkvcGFja2FnZXMvZGlzcGF0Y2gvd2ViL3ZpdGUuY29uZmlnLnRzXCI7Y29uc3QgX192aXRlX2luamVjdGVkX29yaWdpbmFsX2ltcG9ydF9tZXRhX3VybCA9IFwiZmlsZTovLy9sZWdpb24vd29ya3NwYWNlcy9jeWJlcm1vbmtsYWJzL2xlZ2lvbi9pbmZyYS0xMTA5L3BhY2thZ2VzL2Rpc3BhdGNoL3dlYi92aXRlLmNvbmZpZy50c1wiO2ltcG9ydCB7IGNyZWF0ZVJlcXVpcmUgfSBmcm9tIFwibm9kZTptb2R1bGVcIjtcbmltcG9ydCB7IGZpbGVVUkxUb1BhdGggfSBmcm9tIFwibm9kZTp1cmxcIjtcbmltcG9ydCB0YWlsd2luZGNzcyBmcm9tIFwiQHRhaWx3aW5kY3NzL3ZpdGVcIjtcbmltcG9ydCByZWFjdCBmcm9tIFwiQHZpdGVqcy9wbHVnaW4tcmVhY3RcIjtcbmltcG9ydCB7IGRlZmluZUNvbmZpZywgdHlwZSBQbHVnaW4gfSBmcm9tIFwidml0ZVwiO1xuaW1wb3J0IHtcbiAgdHlwZSBHZW5lcmF0ZWRDb2RlLFxuICBwYWNrYWdlUm9vdCxcbiAgdGhpcmRQYXJ0eU5vdGljZXMsXG4gIHZpdGVCdW5kbGVJbnB1dHMsXG4gIHZpdGVSdW50aW1lR2VuZXJhdGVkQ29kZSxcbn0gZnJvbSBcIi4uLy4uLy4uL3NjcmlwdHMvdGhpcmQtcGFydHktbm90aWNlc1wiO1xuXG4vKipcbiAqIGBkZWNvZGUtbmFtZWQtY2hhcmFjdGVyLXJlZmVyZW5jZWAncyBET00tZnJlZSBpbXBsZW1lbnRhdGlvbiwgd2hpY2ggbWljcm9tYXJrIGRlY29kZXMgZXZlcnlcbiAqIG5hbWVkIGNoYXJhY3RlciByZWZlcmVuY2UgdGhyb3VnaCBpbiBldmVyeSBidW5kbGUgdGhpcyBjb25maWcgYnVpbGRzLiBUaGUgcGFja2FnZSdzIGBicm93c2VyYFxuICogZXhwb3J0IGNvbmRpdGlvbiBpcyBhIG1vZHVsZSB3aG9zZSBib2R5IGlzIGBkb2N1bWVudC5jcmVhdGVFbGVtZW50KCdpJylgLCBhbmQgVml0ZSByZXNvbHZlcyBhXG4gKiBXZWIgV29ya2VyJ3MgYnVuZGxlIHdpdGggdGhlIGNsaWVudCdzIGNvbmRpdGlvbnMsIHNvIHRoZSBwYXJzZSB3b3JrZXJcbiAqIChmZWF0dXJlcy9yZWZzL21hcmtkb3duLXBhcnNlLndvcmtlci50cykgd291bGQgdGhyb3cgYmVmb3JlIGl0cyBmaXJzdCBtZXNzYWdlLiBJdHMgZGVmYXVsdFxuICogY29uZGl0aW9uIGFuc3dlcnMgZnJvbSB0aGUgYGNoYXJhY3Rlci1lbnRpdGllc2AgdGFibGUgaW5zdGVhZCAtIHRoZSB0YWJsZSBCdW4sIE5vZGUsIHRoZSB1bml0XG4gKiBzdWl0ZXMgYW5kIHRoZSBwbWRvYyBmaXh0dXJlIGdlbmVyYXRvciBhbHJlYWR5IHJ1biAtIHNvIGEgYm9keSByZWFkcyB0aGUgc2FtZSBhYm92ZSBhbmQgYmVsb3dcbiAqIHRoZSBwYXJzZSB3b3JrZXIncyB0aHJlc2hvbGQsIGFuZCB0aGUgc2FtZSBhcyB0aGUgZ2VuZXJhdG9yIHRoYXQgcGlucyBHbydzIGZpeHR1cmVzXG4gKiAoc2phd2hhci9sZWdpb24jMjUxOSksIGZvciAxMS42IEtCIGd6aXBwZWQgb24gdGhlIHBhZ2UncyBidW5kbGUuXG4gKlxuICogVGhlIHBhY2thZ2UgcHVibGlzaGVzIG5vIGAuL2luZGV4LmpzYCBzdWJwYXRoLCBzbyB0aGUgZmlsZSBpcyByZXNvbHZlZCByYXRoZXIgdGhhbiBuYW1lZDogZnJvbVxuICogdGhlIGVkaXRvciwgd2hpY2ggYG1pY3JvbWFyay1jb3JlLWNvbW1vbm1hcmtgIGlzIGEgZGVwZW5kZW5jeSBvZiwgYW5kIGZyb20gdGhhdCBwYWNrYWdlLCB3aGljaFxuICogZGVjb2RlcyByZWZlcmVuY2VzIHdpdGggaXQuIGByZXF1aXJlYCdzIG93biBjb25kaXRpb25zIGhvbGQgbm8gYGJyb3dzZXJgLCBzbyB0aGlzIGlzIGBpbmRleC5qc2AuXG4gKi9cbmNvbnN0IHByb29mRWRpdG9yID0gY3JlYXRlUmVxdWlyZShcbiAgZmlsZVVSTFRvUGF0aChuZXcgVVJMKFwiLi4vLi4vcHJvb2YtZWRpdG9yL3NyYy9saWItaGVhZGxlc3MudHNcIiwgaW1wb3J0Lm1ldGEudXJsKSlcbik7XG5jb25zdCBjaGFyYWN0ZXJSZWZlcmVuY2VEZWNvZGVyID0gY3JlYXRlUmVxdWlyZShcbiAgcHJvb2ZFZGl0b3IucmVzb2x2ZShcIm1pY3JvbWFyay1jb3JlLWNvbW1vbm1hcmtcIilcbikucmVzb2x2ZShcImRlY29kZS1uYW1lZC1jaGFyYWN0ZXItcmVmZXJlbmNlXCIpO1xuY29uc3Qgdml0ZSA9IHBhY2thZ2VSb290KFwidml0ZVwiLCBmaWxlVVJMVG9QYXRoKG5ldyBVUkwoXCIuXCIsIGltcG9ydC5tZXRhLnVybCkpKTtcbi8qKlxuICogVGhlIGNvZGUgdGhlIGJ1aWxkIGVtaXRzIGZyb20gbm8gc291cmNlIGZpbGUgaXQgcmVjb3JkczogVml0ZSdzIG93biBoZWxwZXJzIChtb2R1bGUgcHJlbG9hZCxcbiAqIGFuZCB0aGUgQ29tbW9uSlMgaW50ZXJvcCBvZiB0aGUgQHJvbGx1cC9wbHVnaW4tY29tbW9uanMgYnVpbGQgVml0ZSA1IGNhcnJpZXMgaW5zaWRlIGl0KSwgYW5kXG4gKiB0aGUgd3JhcHBlcnMgdGhhdCBpbnRlcm9wIHB1dHMgYXJvdW5kIGEgQ29tbW9uSlMgbW9kdWxlLCB3aG9zZSBvd24gZmlsZSB0aGUgYnVpbGQgcmVjb3Jkcy5cbiAqL1xuY29uc3QgR0VORVJBVEVEX0NPREU6IEdlbmVyYXRlZENvZGVbXSA9IFtcbiAgdml0ZVJ1bnRpbWVHZW5lcmF0ZWRDb2RlKHZpdGUpLFxuICB7IGlkOiAvXlxcMChjb21tb25qc0hlbHBlcnNcXC5qc3xjb21tb25qcy1keW5hbWljLW1vZHVsZXMpJC8sIHBhY2thZ2VzOiBbdml0ZV0gfSxcbiAgeyBpZDogL15cXDBcXC8uKlxcP2NvbW1vbmpzLShtb2R1bGV8ZXhwb3J0cykkLywgcGFja2FnZXM6IFtdIH0sXG5dO1xuXG4vKipcbiAqIFdyaXRlcyBkaXN0L1RISVJEX1BBUlRZX05PVElDRVMudHh0LCB3aGljaCBEaXNwYXRjaCBzZXJ2ZXMgYXQgL1RISVJEX1BBUlRZX05PVElDRVMudHh0IGFzIHRleHQ6XG4gKiB0aGUgbGljZW5zZSBvZiBldmVyeSB0aGlyZC1wYXJ0eSBwYWNrYWdlIHRoZSBidWlsZCBjb3BpZXMgaW50byBkaXN0LlxuICpcbiAqIEEgV2ViIFdvcmtlciAoYG5ldyBXb3JrZXIobmV3IFVSTChcdTIwMjYpLCBcdTIwMjYpYCkgaXMgYSBidW5kbGUgb2YgaXRzIG93biwgYnVpbHQgd2hpbGUgdGhlIHBhZ2UncyBidWlsZFxuICogdHJhbnNmb3JtcyB0aGUgbW9kdWxlIHRoYXQgY3JlYXRlcyBpdCwgc28gYmVmb3JlIHRoZSBwYWdlJ3MgYnVuZGxlIGlzIGdlbmVyYXRlZDsgdGhlIHBhZ2Unc1xuICogYnVpbGQgdGhlbiBjb3BpZXMgdGhlIHdvcmtlcidzIGZpbGVzIGluIGFzIGFzc2V0cyB0aGF0IG5hbWUgbm8gc291cmNlLiBTbyBgd29ya2VyYCwgdGhlIHBsdWdpblxuICogb2YgZWFjaCB3b3JrZXIgYnVuZGxlLCByZWNvcmRzIHRoYXQgYnVuZGxlJ3MgZmlsZXMgYW5kIGlucHV0cywgYW5kIGBwYWdlYCBsZWF2ZXMgdGhvc2UgZmlsZXNcbiAqIG91dCBvZiB0aGUgcGFnZSdzIGJ1bmRsZSBhbmQgYWRkcyB0aG9zZSBpbnB1dHMgdG8gaXRzIG93bi5cbiAqL1xuZnVuY3Rpb24gdGhpcmRQYXJ0eU5vdGljZXNGaWxlKCk6IHsgcGFnZTogUGx1Z2luOyB3b3JrZXI6ICgpID0+IFBsdWdpbiB9IHtcbiAgbGV0IHJvb3QgPSBcIlwiO1xuICBjb25zdCB3b3JrZXJGaWxlcyA9IG5ldyBTZXQ8c3RyaW5nPigpO1xuICBjb25zdCB3b3JrZXJJbnB1dHMgPSBuZXcgU2V0PHN0cmluZz4oKTtcbiAgcmV0dXJuIHtcbiAgICBwYWdlOiB7XG4gICAgICBuYW1lOiBcInRoaXJkLXBhcnR5LW5vdGljZXNcIixcbiAgICAgIGFwcGx5OiBcImJ1aWxkXCIsXG4gICAgICBjb25maWdSZXNvbHZlZChjb25maWcpIHtcbiAgICAgICAgcm9vdCA9IGNvbmZpZy5yb290O1xuICAgICAgfSxcbiAgICAgIGFzeW5jIGdlbmVyYXRlQnVuZGxlKF9vcHRpb25zLCBidW5kbGUpIHtcbiAgICAgICAgY29uc3QgcGFnZUJ1bmRsZSA9IE9iamVjdC5mcm9tRW50cmllcyhcbiAgICAgICAgICBPYmplY3QuZW50cmllcyhidW5kbGUpLmZpbHRlcigoW2ZpbGVOYW1lXSkgPT4gIXdvcmtlckZpbGVzLmhhcyhmaWxlTmFtZSkpXG4gICAgICAgICk7XG4gICAgICAgIGNvbnN0IGlucHV0cyA9IGF3YWl0IHZpdGVCdW5kbGVJbnB1dHModGhpcywgcGFnZUJ1bmRsZSwgcm9vdCwgR0VORVJBVEVEX0NPREUpO1xuICAgICAgICBmb3IgKGNvbnN0IGlucHV0IG9mIHdvcmtlcklucHV0cykgaW5wdXRzLmFkZChpbnB1dCk7XG4gICAgICAgIHRoaXMuZW1pdEZpbGUoe1xuICAgICAgICAgIHR5cGU6IFwiYXNzZXRcIixcbiAgICAgICAgICBmaWxlTmFtZTogXCJUSElSRF9QQVJUWV9OT1RJQ0VTLnR4dFwiLFxuICAgICAgICAgIHNvdXJjZTogYXdhaXQgdGhpcmRQYXJ0eU5vdGljZXMoaW5wdXRzLCByb290KSxcbiAgICAgICAgfSk7XG4gICAgICB9LFxuICAgIH0sXG4gICAgd29ya2VyOiAoKSA9PiAoe1xuICAgICAgbmFtZTogXCJ0aGlyZC1wYXJ0eS1ub3RpY2VzLXdvcmtlclwiLFxuICAgICAgYXBwbHk6IFwiYnVpbGRcIixcbiAgICAgIGFzeW5jIGdlbmVyYXRlQnVuZGxlKF9vcHRpb25zLCBidW5kbGUpIHtcbiAgICAgICAgZm9yIChjb25zdCBmaWxlTmFtZSBvZiBPYmplY3Qua2V5cyhidW5kbGUpKSB3b3JrZXJGaWxlcy5hZGQoZmlsZU5hbWUpO1xuICAgICAgICBmb3IgKGNvbnN0IGlucHV0IG9mIGF3YWl0IHZpdGVCdW5kbGVJbnB1dHModGhpcywgYnVuZGxlLCByb290LCBHRU5FUkFURURfQ09ERSkpIHtcbiAgICAgICAgICB3b3JrZXJJbnB1dHMuYWRkKGlucHV0KTtcbiAgICAgICAgfVxuICAgICAgfSxcbiAgICB9KSxcbiAgfTtcbn1cblxuY29uc3Qgbm90aWNlcyA9IHRoaXJkUGFydHlOb3RpY2VzRmlsZSgpO1xuXG5leHBvcnQgZGVmYXVsdCBkZWZpbmVDb25maWcoe1xuICByb290OiBcIndlYlwiLFxuICBwbHVnaW5zOiBbcmVhY3QoKSwgdGFpbHdpbmRjc3MoKSwgbm90aWNlcy5wYWdlXSxcbiAgLy8gVGhlIE1lcm1haWQgd29ya2VyIChmZWF0dXJlcy9yZWZzL21lcm1haWQtcmVuZGVyLndvcmtlci50cykgaXMgY3JlYXRlZCB3aXRoXG4gIC8vIGB7IHR5cGU6IFwibW9kdWxlXCIgfWAsIHNvIGl0IGlzIGJ1bmRsZWQgYXMgYW4gRVMgbW9kdWxlLlxuICB3b3JrZXI6IHsgZm9ybWF0OiBcImVzXCIsIHBsdWdpbnM6ICgpID0+IFtub3RpY2VzLndvcmtlcigpXSB9LFxuICBkZWZpbmU6IHtcbiAgICAvLyBLZWVwIHRoZSBlbnRyeSBjaHVuayBkaXN0aW5jdCBhY3Jvc3MgZGVwbG95bWVudHMsIGluY2x1ZGluZyBuby1zb3VyY2UtY2hhbmdlIGltYWdlIHJlYnVpbGRzLlxuICAgIC8vIERJU1BBVENIX0JVSUxEIHdpbnMgb3ZlciBHSVRIVUJfU0hBLCB3aGljaCBDSSBzZXRzIG9uIGV2ZXJ5IHN0ZXAsIHNvIG9uZSBqb2IgY2FuIGJ1aWxkIHR3b1xuICAgIC8vIGRpc3RpbmN0IGRhc2hib2FyZHMgb2Ygb25lIGNvbW1pdCAoYGJ1biBydW4gZTJlOm11bHRpcHJvY2Vzc2AncyBzZWNvbmQgYnVpbGQpLlxuICAgIF9fRElTUEFUQ0hfQlVJTERfXzogSlNPTi5zdHJpbmdpZnkoXG4gICAgICBwcm9jZXNzLmVudi5ESVNQQVRDSF9CVUlMRCA/PyBwcm9jZXNzLmVudi5HSVRIVUJfU0hBID8/IERhdGUubm93KCkudG9TdHJpbmcoKVxuICAgICksXG4gIH0sXG4gIHJlc29sdmU6IHtcbiAgICAvLyBUaGUgc291cmNlLW9ubHkgZWRpdG9yIGFuZCBEaXNwYXRjaCBib3RoIHJlYWNoIHRoaXMgY29uc3RydWN0b3IuIEhpc3RvcmljYWwtdmVyc2lvblxuICAgIC8vIHJlbmRlcmluZyBwYXNzZXMgYSBQcm9vZiBkb2N1bWVudCB0byBEaXNwYXRjaCdzIERPTVNlcmlhbGl6ZXIsIHNvIFZpdGUgbXVzdCBlbWl0IG9uZVxuICAgIC8vIG1vZHVsZSBpZGVudGl0eSByYXRoZXIgdGhhbiBvbmUgcGVyIHdvcmtzcGFjZSBpbXBvcnQgcGF0aC5cbiAgICBkZWR1cGU6IFtcInByb3NlbWlycm9yLW1vZGVsXCJdLFxuICAgIGFsaWFzOiB7XG4gICAgICBcImRlY29kZS1uYW1lZC1jaGFyYWN0ZXItcmVmZXJlbmNlXCI6IGNoYXJhY3RlclJlZmVyZW5jZURlY29kZXIsXG4gICAgICAvLyBFdmVyeSBzdWJwYXRoIG9mIGBAbGVnaW9uL2NvbnRyYWN0c2AgdGhpcyB0cmVlIHJlYWRzLCBuYW1lZCBiZWZvcmUgdGhlIHBhY2thZ2UgaXRzZWxmOlxuICAgICAgLy8gVml0ZSdzIGFsaWFzIG1hdGNoZXMgYSBwcmVmaXgsIHNvIGBAbGVnaW9uL2NvbnRyYWN0c2AgYWxvbmUgd291bGQgcmVzb2x2ZSBldmVyeSBzdWJwYXRoXG4gICAgICAvLyB0byBhIHBhdGggaW5zaWRlIGBpbmRleC50c2AuIFRoZSBzdWJwYXRocyBhcmUgd2hhdCBrZWVwcyB0aGUgYmFycmVsIG91dCBvZiB0aGlzIGJ1bmRsZVxuICAgICAgLy8gLSBvbmUgaW1wb3J0IG9mIGl0IHJlYWNoZXMgZXZlcnkgbW9kdWxlIG9mIHRoZSBwYWNrYWdlLCBhbmQgcm9sbHVwIG1heSBkcm9wIG5vbmUgd2hvc2VcbiAgICAgIC8vIHRvcCBsZXZlbCBidWlsZHMgYW55dGhpbmcsIHdoaWNoIGlzIGhvdyB0aGUgd2hvbGUgYERJU1BBVENIX1RFWFRfUkVGRVJFTkNFU2AgdGFibGUgYW5kXG4gICAgICAvLyB6b2QncyBzY2hlbWEgbW9kdWxlcyB3ZXJlIGluIHRoZSBlbnRyeSBjaHVuayBvZiBhIHBhZ2UgdGhhdCByZWFkcyBuZWl0aGVyXG4gICAgICAvLyAoZGlzcGF0Y2g6Ly9JTkZSQS0xMTA5LCBSMTMpLiBFYWNoIGlzIHB1Ymxpc2hlZCBieSB0aGF0IHBhY2thZ2UncyBvd24gYGV4cG9ydHNgLCB3aGljaFxuICAgICAgLy8gaXMgd2hhdCBCdW4sIHRzYyBhbmQgZXZlcnkgb3RoZXIgY29uc3VtZXIgcmVhZC4gVGhyZWUga2V5cyBhcmUgcmVhZCBieSBubyBzaGlwcGVkIG1vZHVsZVxuICAgICAgLy8gb2YgdGhpcyBidW5kbGUgdG9kYXkgLSBgZGlzcGF0Y2gtYXBpYCBieSBub3RoaW5nIGluIHRoZSByZXBvc2l0b3J5LFxuICAgICAgLy8gYGRpc3BhdGNoLXRleHQtcmVmZXJlbmNlc2AgYnkgdGhyZWUgQnVuIHRlc3RzLCB3aGljaCByZXNvbHZlIHRocm91Z2ggYGV4cG9ydHNgIGFuZCBuZXZlclxuICAgICAgLy8gc2VlIHRoaXMgY29uZmlnLCBhbmQgYGRpc3BhdGNoLXRvb2xzYCBieSBhIHR5cGUgaW1wb3J0IGFsb25lIChgdGhlbWUvY2xhc3Nlcy50c2AsIHdoaWNoXG4gICAgICAvLyB0aGUgYnVpbGQgZXJhc2VzKSBhbmQgYnkgYnJvd3NlciByb3dzLCB3aGljaCBQbGF5d3JpZ2h0IHJlc29sdmVzIHRocm91Z2ggYGV4cG9ydHNgIHRvbyAtXG4gICAgICAvLyBhbmQgdGhlIHRocmVlIGFyZSBrZXB0IHNvIHRoYXQgYSB2YWx1ZSBpbXBvcnQgb2Ygb25lIHJlc29sdmVzIHRvIGl0cyBtb2R1bGUgcmF0aGVyIHRoYW5cbiAgICAgIC8vIGJlaW5nIHJld3JpdHRlbiBpbnRvIGBpbmRleC50c2AgYnkgdGhlIGJhcmUga2V5IGJlbG93IGl0LlxuICAgICAgXCJAbGVnaW9uL2NvbnRyYWN0cy9hY3Rvci1sYWJlbFwiOiBmaWxlVVJMVG9QYXRoKFxuICAgICAgICBuZXcgVVJMKFwiLi4vLi4vY29udHJhY3RzL3NyYy9hY3Rvci1sYWJlbC50c1wiLCBpbXBvcnQubWV0YS51cmwpXG4gICAgICApLFxuICAgICAgXCJAbGVnaW9uL2NvbnRyYWN0cy9hZ2VudC1zdHJlYW1cIjogZmlsZVVSTFRvUGF0aChcbiAgICAgICAgbmV3IFVSTChcIi4uLy4uL2NvbnRyYWN0cy9zcmMvYWdlbnQtc3RyZWFtLnRzXCIsIGltcG9ydC5tZXRhLnVybClcbiAgICAgICksXG4gICAgICBcIkBsZWdpb24vY29udHJhY3RzL2NsYWltLWhvbGRzXCI6IGZpbGVVUkxUb1BhdGgoXG4gICAgICAgIG5ldyBVUkwoXCIuLi8uLi9jb250cmFjdHMvc3JjL2NsYWltLWhvbGRzLnRzXCIsIGltcG9ydC5tZXRhLnVybClcbiAgICAgICksXG4gICAgICBcIkBsZWdpb24vY29udHJhY3RzL2Rpc3BhdGNoLWFwaVwiOiBmaWxlVVJMVG9QYXRoKFxuICAgICAgICBuZXcgVVJMKFwiLi4vLi4vY29udHJhY3RzL3NyYy9kaXNwYXRjaC1hcGkudHNcIiwgaW1wb3J0Lm1ldGEudXJsKVxuICAgICAgKSxcbiAgICAgIFwiQGxlZ2lvbi9jb250cmFjdHMvZGlzcGF0Y2gtYXBpLWNvbnN0YW50c1wiOiBmaWxlVVJMVG9QYXRoKFxuICAgICAgICBuZXcgVVJMKFwiLi4vLi4vY29udHJhY3RzL3NyYy9kaXNwYXRjaC1hcGktY29uc3RhbnRzLnRzXCIsIGltcG9ydC5tZXRhLnVybClcbiAgICAgICksXG4gICAgICBcIkBsZWdpb24vY29udHJhY3RzL2Rpc3BhdGNoLWFwaS1ncmFwaFwiOiBmaWxlVVJMVG9QYXRoKFxuICAgICAgICBuZXcgVVJMKFwiLi4vLi4vY29udHJhY3RzL3NyYy9kaXNwYXRjaC1hcGktZ3JhcGgudHNcIiwgaW1wb3J0Lm1ldGEudXJsKVxuICAgICAgKSxcbiAgICAgIFwiQGxlZ2lvbi9jb250cmFjdHMvZGlzcGF0Y2gtYmxvY2stcmVmXCI6IGZpbGVVUkxUb1BhdGgoXG4gICAgICAgIG5ldyBVUkwoXCIuLi8uLi9jb250cmFjdHMvc3JjL2Rpc3BhdGNoLWJsb2NrLXJlZi50c1wiLCBpbXBvcnQubWV0YS51cmwpXG4gICAgICApLFxuICAgICAgXCJAbGVnaW9uL2NvbnRyYWN0cy9kaXNwYXRjaC1ocmVmXCI6IGZpbGVVUkxUb1BhdGgoXG4gICAgICAgIG5ldyBVUkwoXCIuLi8uLi9jb250cmFjdHMvc3JjL2Rpc3BhdGNoLWhyZWYudHNcIiwgaW1wb3J0Lm1ldGEudXJsKVxuICAgICAgKSxcbiAgICAgIFwiQGxlZ2lvbi9jb250cmFjdHMvZGlzcGF0Y2gtdGV4dC1yZWZlcmVuY2VzXCI6IGZpbGVVUkxUb1BhdGgoXG4gICAgICAgIG5ldyBVUkwoXCIuLi8uLi9jb250cmFjdHMvc3JjL2Rpc3BhdGNoLXRleHQtcmVmZXJlbmNlcy50c1wiLCBpbXBvcnQubWV0YS51cmwpXG4gICAgICApLFxuICAgICAgXCJAbGVnaW9uL2NvbnRyYWN0cy9kaXNwYXRjaC10ZXh0LXJ1bGVzXCI6IGZpbGVVUkxUb1BhdGgoXG4gICAgICAgIG5ldyBVUkwoXCIuLi8uLi9jb250cmFjdHMvc3JjL2Rpc3BhdGNoLXRleHQtcnVsZXMudHNcIiwgaW1wb3J0Lm1ldGEudXJsKVxuICAgICAgKSxcbiAgICAgIFwiQGxlZ2lvbi9jb250cmFjdHMvbGVnaW9uLXJvbGVzXCI6IGZpbGVVUkxUb1BhdGgoXG4gICAgICAgIG5ldyBVUkwoXCIuLi8uLi9jb250cmFjdHMvc3JjL2xlZ2lvbi1yb2xlcy50c1wiLCBpbXBvcnQubWV0YS51cmwpXG4gICAgICApLFxuICAgICAgXCJAbGVnaW9uL2NvbnRyYWN0cy9yZWxhdGVkLWZvcm1zXCI6IGZpbGVVUkxUb1BhdGgoXG4gICAgICAgIG5ldyBVUkwoXCIuLi8uLi9jb250cmFjdHMvc3JjL3JlbGF0ZWQtZm9ybXMudHNcIiwgaW1wb3J0Lm1ldGEudXJsKVxuICAgICAgKSxcbiAgICAgIFwiQGxlZ2lvbi9jb250cmFjdHMvZGlzcGF0Y2gtc25pcHBldFwiOiBmaWxlVVJMVG9QYXRoKFxuICAgICAgICBuZXcgVVJMKFwiLi4vLi4vY29udHJhY3RzL3NyYy9kaXNwYXRjaC1zbmlwcGV0LnRzXCIsIGltcG9ydC5tZXRhLnVybClcbiAgICAgICksXG4gICAgICBcIkBsZWdpb24vY29udHJhY3RzL2Rpc3BhdGNoLXRvb2xzXCI6IGZpbGVVUkxUb1BhdGgoXG4gICAgICAgIG5ldyBVUkwoXCIuLi8uLi9jb250cmFjdHMvc3JjL2Rpc3BhdGNoLXRvb2xzLnRzXCIsIGltcG9ydC5tZXRhLnVybClcbiAgICAgICksXG4gICAgICBcIkBsZWdpb24vY29udHJhY3RzL2Rpc3BhdGNoLXRvb2xzLWNvbnN0YW50c1wiOiBmaWxlVVJMVG9QYXRoKFxuICAgICAgICBuZXcgVVJMKFwiLi4vLi4vY29udHJhY3RzL3NyYy9kaXNwYXRjaC10b29scy1jb25zdGFudHMudHNcIiwgaW1wb3J0Lm1ldGEudXJsKVxuICAgICAgKSxcbiAgICAgIFwiQGxlZ2lvbi9jb250cmFjdHNcIjogZmlsZVVSTFRvUGF0aChuZXcgVVJMKFwiLi4vLi4vY29udHJhY3RzL3NyYy9pbmRleC50c1wiLCBpbXBvcnQubWV0YS51cmwpKSxcbiAgICAgIFwiQGxlZ2lvbi9wcm9vZi1lZGl0b3IvZG9jdW1lbnQtanNvblwiOiBmaWxlVVJMVG9QYXRoKFxuICAgICAgICBuZXcgVVJMKFwiLi4vLi4vcHJvb2YtZWRpdG9yL3NyYy9kb2N1bWVudC1qc29uLnRzXCIsIGltcG9ydC5tZXRhLnVybClcbiAgICAgICksXG4gICAgICBcIkBsZWdpb24vcHJvb2YtZWRpdG9yL2hlYWRsZXNzXCI6IGZpbGVVUkxUb1BhdGgoXG4gICAgICAgIG5ldyBVUkwoXCIuLi8uLi9wcm9vZi1lZGl0b3Ivc3JjL2xpYi1oZWFkbGVzcy50c1wiLCBpbXBvcnQubWV0YS51cmwpXG4gICAgICApLFxuICAgICAgXCJAbGVnaW9uL3Byb29mLWVkaXRvci9tZXJtYWlkXCI6IGZpbGVVUkxUb1BhdGgoXG4gICAgICAgIG5ldyBVUkwoXCIuLi8uLi9wcm9vZi1lZGl0b3Ivc3JjL21lcm1haWQtc3ZnLnRzXCIsIGltcG9ydC5tZXRhLnVybClcbiAgICAgICksXG4gICAgICBcIkBsZWdpb24vcHJvb2YtZWRpdG9yL21lcm1haWQtcmVuZGVyXCI6IGZpbGVVUkxUb1BhdGgoXG4gICAgICAgIG5ldyBVUkwoXCIuLi8uLi9wcm9vZi1lZGl0b3Ivc3JjL21lcm1haWQtcmVuZGVyLnRzXCIsIGltcG9ydC5tZXRhLnVybClcbiAgICAgICksXG4gICAgICBcIkBsZWdpb24vcHJvb2YtZWRpdG9yL3N0eWxlLmNzc1wiOiBmaWxlVVJMVG9QYXRoKFxuICAgICAgICBuZXcgVVJMKFwiLi4vLi4vcHJvb2YtZWRpdG9yL3NyYy9saWIuY3NzXCIsIGltcG9ydC5tZXRhLnVybClcbiAgICAgICksXG4gICAgICBcIkBsZWdpb24vcHJvb2YtZWRpdG9yL2Jsb2NrLW5lc3RpbmdcIjogZmlsZVVSTFRvUGF0aChcbiAgICAgICAgbmV3IFVSTChcIi4uLy4uL3Byb29mLWVkaXRvci9zcmMvYmxvY2stbmVzdGluZy50c1wiLCBpbXBvcnQubWV0YS51cmwpXG4gICAgICApLFxuICAgICAgXCJAbGVnaW9uL3Byb29mLWVkaXRvclwiOiBmaWxlVVJMVG9QYXRoKFxuICAgICAgICBuZXcgVVJMKFwiLi4vLi4vcHJvb2YtZWRpdG9yL3NyYy9saWIudHNcIiwgaW1wb3J0Lm1ldGEudXJsKVxuICAgICAgKSxcbiAgICAgIC8vIFRoZSB1cHN0cmVhbSBwYWNrYWdlIGRlY2xhcmVzIG5vIG1haW4sIG5vIG1vZHVsZSwgbm8gdHlwZXMsIG5vIGV4cG9ydHMgYW5kIG5vIGZpbGVzLFxuICAgICAgLy8gc28gaXQgcHVibGlzaGVzIG5vIGVudHJ5IHBvaW50IGF0IGFsbCBhbmQgdGhlIGVkaXRvcidzIHVwc3RyZWFtIGltcG9ydHMgcmVhY2ggaXRzXG4gICAgICAvLyBzcmMvIGJ5IHBhdGguIFNhbWUgbWFwcGluZyBhcyBwYWNrYWdlcy9wcm9vZi1lZGl0b3IvdHNjb25maWcuanNvbiwgd2hpY2ggQnVuIGFuZCB0c2MgcmVhZC5cbiAgICAgIFwicHJvb2Ytc2RrLXVwc3RyZWFtL3NyY1wiOiBmaWxlVVJMVG9QYXRoKFxuICAgICAgICBuZXcgVVJMKFwiLi4vLi4vcHJvb2YtZWRpdG9yL25vZGVfbW9kdWxlcy9wcm9vZi1zZGstdXBzdHJlYW0vc3JjXCIsIGltcG9ydC5tZXRhLnVybClcbiAgICAgICksXG4gICAgfSxcbiAgfSxcbiAgYnVpbGQ6IHtcbiAgICBvdXREaXI6IFwiZGlzdFwiLFxuICAgIC8vIFRoZSBlbnRyeSAtIHRoZSBvbmUgYGFzc2V0cy9pbmRleC0qLmpzYCB0aGF0IGRpc3QvaW5kZXguaHRtbCBuYW1lcyAtIHJlYWQgNDk5LDQ4NSBieXRlcyxcbiAgICAvLyBtZWFzdXJlZCB3aXRoIGBidW4gcnVuIGJ1aWxkOndlYmAgYW5kIGEgMzktY2hhcmFjdGVyIGBESVNQQVRDSF9CVUlMRGAgYXQgYDQ5MzVjNGFmYCAodGhlXG4gICAgLy8gc2FtZSBhdCB0aGUgbWVyZ2VzIDQ1NDgwOTZkIGFuZCBlMTVlZTVlNCkgb24gMjAyNi0xMS0yMzsgdGhlIGJ1aWxkIGlkIGlzIGluIHRoZSBlbnRyeSwgc29cbiAgICAvLyBDSSdzIDQwLWNoYXJhY3RlciBjb21taXQgd3JpdGVzIG9uZSBieXRlIG1vcmUuIE9uIHRoYXQgZGF5IHRoZSBkZWZhdWx0IGJyYW5jaCdzIG93biBidWlsZFxuICAgIC8vIGF0IGBkNzk1YjI1Y2Agd3JpdGVzIDEsMTQ0LDE4OCwgYmVmb3JlIGRpc3BhdGNoOi8vSU5GUkEtMTEwOVxuICAgIC8vIHNwbGl0IHRoZSBzaGVsbDogdGhlIEluYm94IGFuZCBpdHMgcGVlaywgdGhlIGFzayBjYXJkLCB0aGUgc2VhcmNoIGJhciwgdGhlIENyZWF0ZSBpc3N1ZVxuICAgIC8vIGRpYWxvZywgdGhlIHNob3J0Y3V0IHNoZWV0LCB0aGUgcmVmZXJlbmNlLXByZXZpZXcgbGF5ZXIsIHRoZSBldmVudC1yZWZyZXNoIG1hcCwgdGhlIGhlYWRsZXNzXG4gICAgLy8gTWFya2Rvd24gZW5naW5lIHRoZSBzaGVsbCB3YXJtcyBhdCBib290LCB0aGUgbmV3IGlzc3VlIHBhZ2UncyBmcmFtZSBzaGVsbCwgdGhlIHBlbmRpbmctd3JpdGVzXG4gICAgLy8gaW5kaWNhdG9yLCB0aGUga2VwdCB3cml0ZXMgYW5kIHRoZSBraW5kcyBvZiB3cml0ZSBhcmUgZWFjaCBhIGNodW5rIG9mIHRoZWlyIG93biBub3csIGFuZFxuICAgIC8vIGBAbGVnaW9uL2NvbnRyYWN0c2AnIHN1YnBhdGhzIGtlZXAgdGhhdCBwYWNrYWdlJ3MgcmVmZXJlbmNlIHRhYmxlIGFuZCB6b2QncyBzY2hlbWEgbW9kdWxlc1xuICAgIC8vIG91dCBvZiBhIHBhZ2UgdGhhdCByZWFkcyBuZWl0aGVyIC0gaW4gdGhlIGVkaXRvcidzIGNodW5rcyB0b28sIHdoaWNoIGltcG9ydCB0aGF0IHBhY2thZ2VcbiAgICAvLyBieSBzdWJwYXRoIGFsb25lIChgcGFja2FnZXMvcHJvb2YtZWRpdG9yL3NyYy9ibG9jay1wb2ludGVycy50c2ApLlxuICAgIC8vIFRoZSBtZXJnZSBvZiA0YjIyZWNlNCByZWFkIDUwMCw2NzIgYnl0ZXMsIG92ZXIgdGhlIGJ1ZGdldDsgdGhlIGN1dCBpbiBgMDFmYzBlNDFgIGJyb3VnaHQgaXRcbiAgICAvLyB0byA0OTksMzgwLCBhbmQgYDk3YzBjZGU2YCdzIGZvY3VzIGhlbHBlciBhZGRlZCAxMDUsIHNvIGA0OTM1YzRhZmAgcmVhZHMgdGhvc2UgNDk5LDQ4NSBieXRlcyxcbiAgICAvLyA1MTUgdW5kZXIgdGhlIGJ1ZGdldC4gVGhhdCBjdXQgbWFkZSB0d28gbW92ZXMuIFRoZSBJbmJveCdzIGBCbG9ja2VkIG9uIHlvdWAgc3RyaXAgYW5kIHRoZVxuICAgIC8vIHByb2plY3QgcGFnZSdzIHBpbGwgbGVmdCB0aGUgZW50cnkgZm9yIGEgZmlsZSBvZlxuICAgIC8vIHRoZWlyIG93biwgYGZlYXR1cmVzL2luYm94L0Jsb2NrZWRPbllvdUxpbmsudHN4YCwgYSBjaHVuayBvZiA4NjYgYnl0ZXMgdGhlIEluYm94J3MgYW5kIHRoZVxuICAgIC8vIHByb2plY3QgcGFnZSdzIGNodW5rcyBzaGFyZSwgYW5kIHdpdGhvdXQgdGhhdCBtb3ZlIHRoZSBlbnRyeSBhdCBgMDFmYzBlNDFgIG1lYXN1cmVkIDUwMCwxMDRcbiAgICAvLyBieXRlcy4gVGhlIG1vZHVsZXByZWxvYWQgcG9seWZpbGwsIDcxMCBieXRlcyBvZiB0aGUgZW50cnksIGlzIG9mZiwgZm9yIHRoZSByZWFzb24gYmVzaWRlXG4gICAgLy8gYG1vZHVsZVByZWxvYWRgIGJlbG93LlxuICAgIC8vIFRoZSBvdGhlciBzaXplcyBoZXJlIHdlcmUgbWVhc3VyZWQgdGhlIHNhbWUgd2F5IGF0IGA1MzI4NjcxN2AgdGhlIHNhbWUgZGF5OiB0aGUgbGFyZ2VzdFxuICAgIC8vIGNodW5rIGlzIEVDaGFydHMgKGFuIGBpbmRleC0qLmpzYCBvZiAxLDEzNCw3NzIgYnl0ZXMgcmF3KSwgd2hpY2ggb25seSB0aGUgRGVsaXZlcnkgcGFnZVxuICAgIC8vIGxvYWRzLlxuICAgIC8vIFRoZSBsb2FkaW5nIGJveCBhbmQgdGhlIGFsZXJ0IG9mIHRoZSBzdXJmYWNlcyBhIGtleSBvcGVuc1xuICAgIC8vIChgZmVhdHVyZXMvc2hlbGwvYXNrZWQtc3VyZmFjZS50c3hgLCByZXZpZXcgNjAzNDgxNzk3MykgdGhlbiB0b29rIHRoZSBlbnRyeSB0byA1MDAsNTMwXG4gICAgLy8gYnl0ZXMsIG92ZXIgdGhlIGJ1ZGdldCBhZ2Fpbi4gVGhlIHJhaWwncyBwaW5uZWQgYWdlbnRzIGFuc3dlcmVkIGl0OiB0aGV5IGRyYXcgYSBzZXNzaW9uXG4gICAgLy8gZnJvbSBgQWdlbnROYW1lLnRzeGAncyBzZXNzaW9uIGhhbGYgYWxvbmUgKGBmZWF0dXJlcy9yZWZzL1Nlc3Npb25OYW1lLnRzeGApLCBzbyB0aGUgcmVzdCBvZlxuICAgIC8vIHRoYXQgbW9kdWxlIGlzIGEgY2h1bmsgdGhlIHBhZ2VzJyBjaHVua3Mgc2hhcmUsIGFuZCB0aGUgZW50cnkgcmVhZCA0OTcsNDAxIGJ5dGVzIHdpdGggYVxuICAgIC8vIDM5LWNoYXJhY3RlciBgRElTUEFUQ0hfQlVJTERgIG9uIDIwMjYtMTEtMjQuIEFmdGVyIHRoZSBjaGFuZ2VzIHNpbmNlLCBEZXRhaWxzJyBvbmUgRXNjYXBlXG4gICAgLy8gcnVsZSBhbW9uZyB0aGVtIChgZXNjYXBlQ2xvc2VzRHJhd2VyYCwgYGZlYXR1cmVzL3NoZWxsL3VzZURpYWxvZy50c2ApLCB0aGUgZW50cnkgcmVhZFxuICAgIC8vIDQ5Nyw5MzMgYnl0ZXMgd2l0aCBhIDM5LWNoYXJhY3RlciBgRElTUEFUQ0hfQlVJTERgIHRoZSBzYW1lIGRheSwgYW5kIHRoZSB0d28gbGlzdHMnIGFsZXJ0XG4gICAgLy8gZm9yIGEgcmVuZGVyIGVycm9yIChgZmVhdHVyZXMvd3JpdGVzL3dyaXRlLWhvc3RzLnRzeGApIHRha2VzIGl0IHRvIDQ5OCw1NTMuIFRoZSBzaWduLWluXG4gICAgLy8gcGFnZSdzIGxpc3Qgb2Yga2VwdCB3cml0ZXMsIGluIHRoZSBlbnRyeSBzbyB0aGF0IGl0cyB3YXJuaW5nIGRlcGVuZHMgb24gbm8gY2h1bmtcbiAgICAvLyAoYGZlYXR1cmVzL3dyaXRlcy9LZXB0V3JpdGVzLnRzeGApLCB0YWtlcyBpdCB0byA0OTksNTgzLiBUaGUgbWVyZ2Ugb2YgdGhlIGRlZmF1bHQgYnJhbmNoJ3NcbiAgICAvLyBjb3JyZWN0aW9uIGFuZCBkZWxldGlvbiBvZiBhIHBvc3QgKCMzNTEyKSB0b29rIGl0IHRvIDUwMCw5NzUsIG92ZXIgdGhlIGJ1ZGdldC4gVGhyZWUgbW92ZXNcbiAgICAvLyB0aGVuIHRvb2sgY29kZSB0aGF0IG5vIGZpcnN0IHBhaW50IGRyYXdzIG91dCBvZiB0aGUgZW50cnksIG5vbmUgb2YgdGhlbSBhIG5ldyBsYXp5IGxvYWQsIGFuZFxuICAgIC8vIHRoZSBlbnRyeSByZWFkcyA0OTgsNzc2IGJ5dGVzIHdpdGggYSAzOS1jaGFyYWN0ZXIgYERJU1BBVENIX0JVSUxEYCBvbiAyMDI2LTExLTI0OiBhIGhlbGRcbiAgICAvLyBzZW5kIHJlZnJlc2hlcyBhIGNvbW1lbnQgbGlzdCB1bmRlciBgb3duZXJDb21tZW50c1F1ZXJ5YCdzIGtleSwgc29cbiAgICAvLyBgZmVhdHVyZXMvY29udmVyc2F0aW9uL2NvbXBvc2VyLW1vZGVsLnRzYCBpcyBvdXQgb2YgdGhlIGVudHJ5ICg0MTEgYnl0ZXMpOyB0aGVcbiAgICAvLyByYWlsJ3Mgc3RhdGUgbWFyayByZWFkcyBpdHMgcmVsYXRpdmUgdGltZSBmcm9tIGBmZWF0dXJlcy9yZWZzL3JlbGF0aXZlLXRpbWUudHNgLCBzb1xuICAgIC8vIGA8VGltZXN0YW1wPmAgaXMgYSBjaHVuayB0aGUgcGFnZXMnIGNodW5rcyBzaGFyZSAoNjQ2IGJ5dGVzKTsgYW5kIHRoZSByZWNlbnQgaXRlbXMgcmVhZCBhXG4gICAgLy8gZG9jdW1lbnQncyByb3V0ZSBmcm9tIGBmZWF0dXJlcy9yZWZzL3JvdXRlcy50c2AsIHNvIHRoZSBzZWFyY2ggYmFyJ3MgY2hpcFxuICAgIC8vIChgZmVhdHVyZXMvc2VhcmNoL2Jhci1zY29wZS50c2ApIGlzIGEgY2h1bmsgdGhhdCB0aGUgYmFyIGFuZCB0aGUgaXNzdWUgbGlzdCBzaGFyZVxuICAgIC8vICgxLDE0MiBieXRlcykuIFRoYXQgbGVmdCA0OTgsNzc2IGJ5dGVzOyB0aGUgY29ycmVjdGlvbidzIG1hcmsgdGhlbiBqb2luZWQgdGhlIGV2ZW50IG1hcCdzXG4gICAgLy8gY2h1bmsgKGBhcGkvZXZlbnQtcmVmcmVzaC1sb2FkLnRzYCksIGFuZCB0aGUgZW50cnkgcmVhZHMgNDk4LDU5MC5cbiAgICAvLyBgd2ViL3NyYy9hc3NldC1zaXplcy1pbi1idWlsZC50c2AgaG9sZHMgdGhlIGVudHJ5IHRvIDUwMCwwMDAgYnl0ZXMgYW5kIGtlZXBzIGl0cyBoaXN0b3J5LFxuICAgIC8vIGFuZCBgYnVuIHJ1biBjaGVjazphc3NldC1zaXplc2AgcmVhZHMgaXQgYWdhaW47IHRoaXMgYGNodW5rU2l6ZVdhcm5pbmdMaW1pdGAgaXMgVml0ZSdzIG93blxuICAgIC8vIHdhcm5pbmcgYW5kIGlzIG92ZXIgdGhlIEVDaGFydHMgY2h1bmsgYWxvbmUuXG4gICAgLy8gYmVhdXRpZnVsLW1lcm1haWQgYW5kIEVMSyBsaXZlIG9ubHkgaW4gdGhlIE1lcm1haWQgd29ya2VyJ3MgYnVuZGxlXG4gICAgLy8gKGZlYXR1cmVzL3JlZnMvbWVybWFpZC1yZW5kZXIud29ya2VyLnRzLCAxLDU2NSw2MDggYnl0ZXMpLCBmZXRjaGVkIG9uY2UgYSBib2R5IG9yIGFcbiAgICAvLyBkb2N1bWVudCBob2xkcyBhIG1lcm1haWQgYmxvY2s7IFZpdGUgZW1pdHMgaXQgYXMgYW4gYXNzZXQgYW5kIGhvbGRzIGl0IHRvIG5vIGNodW5rIGxpbWl0LlxuICAgIC8vIFRoZSBlZGl0b3IncyBsYXp5IGNodW5rIChgbGliLSouanNgLCAxODgsOTcyIGJ5dGVzKSBjYXJyaWVzIG5vbmUgb2YgdGhlIHJlbmRlcmVyLlxuICAgIGNodW5rU2l6ZVdhcm5pbmdMaW1pdDogMTIwMCxcbiAgICAvLyBObyBtb2R1bGVwcmVsb2FkIHBvbHlmaWxsLiBWaXRlJ3MgY29weSBvZiBpdCBpcyA3MTAgYnl0ZXMgb2YgdGhlIGVudHJ5IGNodW5rLCBhbmQgaXQgZG9lc1xuICAgIC8vIG5vdGhpbmcgaW4gYSBicm93c2VyIHRoYXQgaGFzIGA8bGluayByZWw9bW9kdWxlcHJlbG9hZD5gOiBDaHJvbWUgNjYrLCBTYWZhcmkgMTcrIGFuZFxuICAgIC8vIEZpcmVmb3ggMTE1Ky4gRXZlcnkgYnJvd3NlciB3aXRob3V0IHRoYXQgbGluayBhbHNvIGxhY2tzIGBQcm9taXNlLndpdGhSZXNvbHZlcnNgLCB3aGljaFxuICAgIC8vIG9ubHkgQ2hyb21lIDExOSssIFNhZmFyaSAxNy40KyBhbmQgRmlyZWZveCAxMjErIGhhdmUsIGFuZCB0aGUgYXBwJ3Mgb3duIGNvZGUgY2FsbHMgaXQ6IGVhY2hcbiAgICAvLyB3cml0ZSB0aGUgcGFnZSBtYWtlcyAoYHJ1bmAgaW4gYGZlYXR1cmVzL3dyaXRlcy93cml0ZS1zdG9yZS50c2ApIGFuZCBlYWNoIHVwbG9hZFxuICAgIC8vIChgeGhyVXBsb2FkYCBpbiBgYXBpL2NsaWVudC50c2ApLiBTbyBpbiB0aG9zZSBicm93c2VycyBldmVyeSB3cml0ZSBmYWlscyBhcyB0aGUgcmVhZGVyXG4gICAgLy8gbWFrZXMgaXQsIHdpdGggdGhlIHBvbHlmaWxsIG9yIHdpdGhvdXQgaXQ6IHRoZXkgZG8gbm90IHJ1biB0aGUgYXBwLCBhbmQgdGhlIHBvbHlmaWxsIGFjdGVkXG4gICAgLy8gb25seSBpbiBicm93c2VycyB0aGUgYXBwIGFscmVhZHkgZXhjbHVkZXMuIE5vIGJyb3dzZXIgdGhhdCBydW5zIHRoZSBhcHAgY2hhbmdlcyB3aXRob3V0IGl0LlxuICAgIC8vIFR1cm5lZCBvZmYgZm9yIHRoZSBlbnRyeSdzIGJ1ZGdldCAoZGlzcGF0Y2g6Ly9JTkZSQS0xMTA5LCAyMDI2LTExLTIzKS5cbiAgICBtb2R1bGVQcmVsb2FkOiB7IHBvbHlmaWxsOiBmYWxzZSB9LFxuICAgIGVtcHR5T3V0RGlyOiB0cnVlLFxuICB9LFxuICBzZXJ2ZXI6IHtcbiAgICBwcm94eToge1xuICAgICAgXCIvYXBpXCI6IFwiaHR0cDovL2xvY2FsaG9zdDo4NzY2XCIsXG4gICAgICBcIi9hdXRoXCI6IFwiaHR0cDovL2xvY2FsaG9zdDo4NzY2XCIsXG4gICAgICBcIi93c1wiOiB7XG4gICAgICAgIHRhcmdldDogXCJ3czovL2xvY2FsaG9zdDo4NzY2XCIsXG4gICAgICAgIHdzOiB0cnVlLFxuICAgICAgfSxcbiAgICB9LFxuICB9LFxufSk7XG4iLCAiY29uc3QgX192aXRlX2luamVjdGVkX29yaWdpbmFsX2Rpcm5hbWUgPSBcIi9sZWdpb24vd29ya3NwYWNlcy9jeWJlcm1vbmtsYWJzL2xlZ2lvbi9pbmZyYS0xMTA5L3NjcmlwdHNcIjtjb25zdCBfX3ZpdGVfaW5qZWN0ZWRfb3JpZ2luYWxfZmlsZW5hbWUgPSBcIi9sZWdpb24vd29ya3NwYWNlcy9jeWJlcm1vbmtsYWJzL2xlZ2lvbi9pbmZyYS0xMTA5L3NjcmlwdHMvdGhpcmQtcGFydHktbm90aWNlcy50c1wiO2NvbnN0IF9fdml0ZV9pbmplY3RlZF9vcmlnaW5hbF9pbXBvcnRfbWV0YV91cmwgPSBcImZpbGU6Ly8vbGVnaW9uL3dvcmtzcGFjZXMvY3liZXJtb25rbGFicy9sZWdpb24vaW5mcmEtMTEwOS9zY3JpcHRzL3RoaXJkLXBhcnR5LW5vdGljZXMudHNcIjsvLyBUaGUgbGljZW5zZSBub3RpY2VzIG9mIHRoZSB0aGlyZC1wYXJ0eSBwYWNrYWdlcyBhIGJ1bmRsZSBpbmxpbmVzLiBBIGJ1bmRsZSBpcyBhIGNvcHkgb2YgZXZlcnlcbi8vIHBhY2thZ2UgaXQgaW5saW5lcywgYW5kIG1vc3Qgb2YgdGhlaXIgbGljZW5zZXMgKE1JVCwgQlNELCBJU0MsIEFwYWNoZS0yLjApIHJlcXVpcmUgc2hpcHBpbmcgdGhlaXJcbi8vIG5vdGljZXMgd2l0aCBldmVyeSBjb3B5LCBzbyBhIGJ1aWxkIHRoYXQgd3JpdGVzIGEgYnVuZGxlIHdyaXRlcyB0aGlzIGJlc2lkZSBpdC5cbmltcG9ydCB7IGV4aXN0c1N5bmMsIHJlYWxwYXRoU3luYyB9IGZyb20gXCJub2RlOmZzXCI7XG5pbXBvcnQgeyByZWFkZGlyLCByZWFkRmlsZSwgcmVhbHBhdGgsIHN0YXQsIHdyaXRlRmlsZSB9IGZyb20gXCJub2RlOmZzL3Byb21pc2VzXCI7XG5pbXBvcnQgeyBjcmVhdGVSZXF1aXJlIH0gZnJvbSBcIm5vZGU6bW9kdWxlXCI7XG5pbXBvcnQgeyBkaXJuYW1lLCBpc0Fic29sdXRlLCBqb2luLCByZXNvbHZlLCBzZXAgfSBmcm9tIFwibm9kZTpwYXRoXCI7XG5pbXBvcnQgcGFyc2VTcGR4RXhwcmVzc2lvbiBmcm9tIFwic3BkeC1leHByZXNzaW9uLXBhcnNlXCI7XG5pbXBvcnQgc3BkeExpY2Vuc2VzIGZyb20gXCJzcGR4LWxpY2Vuc2UtbGlzdC9mdWxsXCI7XG5pbXBvcnQgeyB6IH0gZnJvbSBcInpvZFwiO1xuXG4vKiogQSBmaWxlIHRoYXQgaG9sZHMgYSBwYWNrYWdlJ3MgbGljZW5zZSB0ZXJtcy4gQSBOT1RJQ0UgYWxvbmUgZG9lcyBub3QuICovXG5jb25zdCBMSUNFTlNFX0ZJTEUgPSAvXihsaWNlbltjc11lfGNvcHlpbmcpKFsuLV0uKik/JC9pO1xuLyoqIEEgTk9USUNFIGZpbGUsIHdoaWNoIEFwYWNoZS0yLjAgcmVxdWlyZXMgcGFzc2luZyBvbiBiZXNpZGUgdGhlIGxpY2Vuc2UuICovXG5jb25zdCBOT1RJQ0VfRklMRSA9IC9ebm90aWNlKFsuLV0uKik/JC9pO1xuY29uc3QgTk9ERV9NT0RVTEVTID0gYCR7c2VwfW5vZGVfbW9kdWxlcyR7c2VwfWA7XG5cbmludGVyZmFjZSBCdW5kbGVkUGFja2FnZSB7XG4gIG5hbWU6IHN0cmluZztcbiAgdmVyc2lvbjogc3RyaW5nO1xuICBsaWNlbnNlOiBzdHJpbmcgfCBudWxsO1xuICBzb3VyY2U6IHN0cmluZyB8IG51bGw7XG4gIGZpbGVzOiB7IG5hbWU6IHN0cmluZzsgdGV4dDogc3RyaW5nIH1bXTtcbiAgLyoqIFRoZSBTUERYIExpY2Vuc2UgTGlzdCdzIHN0YW5kYXJkIHRleHQgb2YgZWFjaCBkZWNsYXJlZCBsaWNlbnNlLCBmb3IgYSBwYWNrYWdlIHRoYXQgc2hpcHMgbm9uZS4gKi9cbiAgc3RhbmRhcmRUZXh0czogeyBpZDogc3RyaW5nOyB0ZXh0OiBzdHJpbmcgfVtdO1xufVxuXG4vKiogVGhlIGluc3RhbGxlZCBwYWNrYWdlIHJvb3QgYSBtb2R1bGUgcGF0aCBzaXRzIGluLCBvciBudWxsIGZvciBhIHBhdGggb3V0c2lkZSBub2RlX21vZHVsZXMuICovXG5mdW5jdGlvbiBpbnN0YWxsZWRSb290T2YobW9kdWxlUGF0aDogc3RyaW5nKTogc3RyaW5nIHwgbnVsbCB7XG4gIGNvbnN0IGF0ID0gbW9kdWxlUGF0aC5sYXN0SW5kZXhPZihOT0RFX01PRFVMRVMpO1xuICBpZiAoYXQgPCAwKSByZXR1cm4gbnVsbDtcbiAgY29uc3QgcmVzdCA9IG1vZHVsZVBhdGguc2xpY2UoYXQgKyBOT0RFX01PRFVMRVMubGVuZ3RoKS5zcGxpdChzZXApO1xuICBjb25zdCBkZXB0aCA9IHJlc3RbMF0/LnN0YXJ0c1dpdGgoXCJAXCIpID8gMiA6IDE7XG4gIHJldHVybiBtb2R1bGVQYXRoLnNsaWNlKDAsIGF0ICsgTk9ERV9NT0RVTEVTLmxlbmd0aCkgKyByZXN0LnNsaWNlKDAsIGRlcHRoKS5qb2luKHNlcCk7XG59XG5cbmFzeW5jIGZ1bmN0aW9uIHJlYWRNYW5pZmVzdChkaXJlY3Rvcnk6IHN0cmluZyk6IFByb21pc2U8UmVjb3JkPHN0cmluZywgdW5rbm93bj4gfCBudWxsPiB7XG4gIHRyeSB7XG4gICAgcmV0dXJuIEpTT04ucGFyc2UoYXdhaXQgcmVhZEZpbGUoam9pbihkaXJlY3RvcnksIFwicGFja2FnZS5qc29uXCIpLCBcInV0ZjhcIikpO1xuICB9IGNhdGNoIChlcnJvcikge1xuICAgIGlmICgoZXJyb3IgYXMgTm9kZUpTLkVycm5vRXhjZXB0aW9uKS5jb2RlID09PSBcIkVOT0VOVFwiKSByZXR1cm4gbnVsbDtcbiAgICB0aHJvdyBlcnJvcjtcbiAgfVxufVxuXG4vKipcbiAqIFRoZSB3b3Jrc3BhY2UgcGFja2FnZSBhIG1vZHVsZSBvZiB0aGlzIHJlcG9zaXRvcnkgc2l0cyBpbiwgd2hlbiB0aGF0IHBhY2thZ2UgY2FycmllcyBhIGxpY2Vuc2VcbiAqIG9mIGl0cyBvd24gKGEgY29weSBvZiBzb21lb25lIGVsc2UncyBjb2RlLCBzdWNoIGFzIHBhY2thZ2VzL3Byb29mLWVkaXRvciksIGVsc2UgbnVsbC4gVGhlIHdhbGtcbiAqIHN0b3BzIGF0IHRoZSB3b3Jrc3BhY2Ugcm9vdCwgd2hvc2UgbGljZW5zZSBpcyB0aGUgcmVwb3NpdG9yeSdzLlxuICovXG5hc3luYyBmdW5jdGlvbiBsaWNlbnNlZFdvcmtzcGFjZVJvb3RPZihtb2R1bGVQYXRoOiBzdHJpbmcpOiBQcm9taXNlPHN0cmluZyB8IG51bGw+IHtcbiAgbGV0IGRpcmVjdG9yeSA9IGRpcm5hbWUobW9kdWxlUGF0aCk7XG4gIHdoaWxlIChkaXJlY3RvcnkgIT09IGRpcm5hbWUoZGlyZWN0b3J5KSkge1xuICAgIGNvbnN0IG1hbmlmZXN0ID0gYXdhaXQgcmVhZE1hbmlmZXN0KGRpcmVjdG9yeSk7XG4gICAgaWYgKG1hbmlmZXN0ICE9PSBudWxsKSB7XG4gICAgICBpZiAoXCJ3b3Jrc3BhY2VzXCIgaW4gbWFuaWZlc3QpIHJldHVybiBudWxsO1xuICAgICAgY29uc3QgbmFtZXMgPSBhd2FpdCByZWFkZGlyKGRpcmVjdG9yeSk7XG4gICAgICByZXR1cm4gbmFtZXMuc29tZSgobmFtZSkgPT4gTElDRU5TRV9GSUxFLnRlc3QobmFtZSkpID8gZGlyZWN0b3J5IDogbnVsbDtcbiAgICB9XG4gICAgZGlyZWN0b3J5ID0gZGlybmFtZShkaXJlY3RvcnkpO1xuICB9XG4gIHJldHVybiBudWxsO1xufVxuXG5jb25zdCBQYWNrYWdlSWRlbnRpdHkgPSB6Lm9iamVjdCh7IG5hbWU6IHouc3RyaW5nKCkubWluKDEpLCB2ZXJzaW9uOiB6LnN0cmluZygpLm1pbigxKSB9KTtcbi8qKiBucG0ncyBkZXByZWNhdGVkIGxpY2Vuc2Ugb2JqZWN0LCBhbHNvIHRoZSBlbnRyeSBvZiBpdHMgb2xkZXIgYGxpY2Vuc2VzYCBhcnJheS4gKi9cbmNvbnN0IExpY2Vuc2VPYmplY3QgPSB6Lm9iamVjdCh7IHR5cGU6IHouc3RyaW5nKCkgfSk7XG4vKipcbiAqIFRoZSBsaWNlbnNlIGZpZWxkcyBvZiBhIG1hbmlmZXN0LCBpbiBldmVyeSBzaGFwZSBucG0gZGVmaW5lczogYGxpY2Vuc2VgIGFzIGEgc3RyaW5nLCB0aGVcbiAqIGRlcHJlY2F0ZWQgYHsgdHlwZSwgdXJsIH1gIG9iamVjdCwgYG51bGxgIChzb21lIHRvb2xpbmcgd3JpdGVzIHRoaXMgZm9yIFwibm9uZSBkZWNsYXJlZFwiLCB0aGVcbiAqIHNhbWUgYXMgdGhlIGZpZWxkIGJlaW5nIGFic2VudCksIG9yIHRoZSBvbGRlciBgbGljZW5zZXNgIGFycmF5IG9mIHRob3NlIG9iamVjdHMgKGZvcm1hdEAwLjIuMlxuICogc3RpbGwgdXNlcyBpdCksIHdob3NlIGVudHJpZXMgb2ZmZXIgYSBjaG9pY2Ugb2YgbGljZW5zZXMuIEFueSBvdGhlciBzaGFwZSBpcyByZWZ1c2VkLlxuICogYHJlcG9zaXRvcnlgIGFuZCBgaG9tZXBhZ2VgIG9ubHkgcG9pbnQgYXQgdGhlIHNvdXJjZSwgc28gYSBzaGFwZSB0aGlzIGRvZXMgbm90IHJlYWQgZHJvcHMgdGhlbS5cbiAqL1xuY29uc3QgUGFja2FnZUxpY2Vuc2UgPSB6Lm9iamVjdCh7XG4gIGxpY2Vuc2U6IHoudW5pb24oW3ouc3RyaW5nKCksIExpY2Vuc2VPYmplY3RdKS5udWxsYWJsZSgpLm9wdGlvbmFsKCksXG4gIGxpY2Vuc2VzOiB6LmFycmF5KExpY2Vuc2VPYmplY3QpLm1pbigxKS5vcHRpb25hbCgpLFxuICByZXBvc2l0b3J5OiB6XG4gICAgLnVuaW9uKFt6LnN0cmluZygpLCB6Lm9iamVjdCh7IHVybDogei5zdHJpbmcoKSB9KV0pXG4gICAgLm9wdGlvbmFsKClcbiAgICAuY2F0Y2godW5kZWZpbmVkKSxcbiAgaG9tZXBhZ2U6IHouc3RyaW5nKCkub3B0aW9uYWwoKS5jYXRjaCh1bmRlZmluZWQpLFxufSk7XG5cbi8qKiBFdmVyeSBsaWNlbnNlIGFuIFNQRFggZXhwcmVzc2lvbiBuYW1lcy4gKi9cbmZ1bmN0aW9uIGxpY2Vuc2VMZWF2ZXMoZXhwcmVzc2lvbjogcGFyc2VTcGR4RXhwcmVzc2lvbi5JbmZvKTogcGFyc2VTcGR4RXhwcmVzc2lvbi5MaWNlbnNlSW5mb1tdIHtcbiAgaWYgKFwibGljZW5zZVwiIGluIGV4cHJlc3Npb24pIHJldHVybiBbZXhwcmVzc2lvbl07XG4gIHJldHVybiBbLi4ubGljZW5zZUxlYXZlcyhleHByZXNzaW9uLmxlZnQpLCAuLi5saWNlbnNlTGVhdmVzKGV4cHJlc3Npb24ucmlnaHQpXTtcbn1cblxuLyoqXG4gKiBUaGUgU1BEWCBMaWNlbnNlIExpc3QncyBzdGFuZGFyZCB0ZXh0IGZvciBldmVyeSBsZWFmIG9mIGEgcGFyc2VkIGxpY2Vuc2UgZXhwcmVzc2lvbiwgcmVxdWlyZWRcbiAqIHdoZW4gdGhlIHBhY2thZ2Ugc2hpcHMgbm8gbGljZW5zZSBmaWxlIG9mIGl0cyBvd246IGEgYExpY2Vuc2VSZWYtYC9gRG9jdW1lbnRSZWYtYCBsZWFmLCBvciBhbnlcbiAqIGlkIHRoZSBsaXN0IGRvZXNuJ3QgY2FycnksIG9yIG9uZSB3aXRoIGFuIGV4Y2VwdGlvbiAoYFdJVEhgKSwgc3RpbGwgbGVhdmVzIG5vdGhpbmcgdG8gc2hpcCwgc29cbiAqIGl0IHRocm93cywgbmFtaW5nIHRoZSBwYWNrYWdlLlxuICovXG5mdW5jdGlvbiBzdGFuZGFyZFRleHRzRm9yKFxuICBleHByZXNzaW9uOiBwYXJzZVNwZHhFeHByZXNzaW9uLkluZm8sXG4gIGxpY2Vuc2U6IHN0cmluZyxcbiAgbGFiZWw6IHN0cmluZ1xuKTogeyBpZDogc3RyaW5nOyB0ZXh0OiBzdHJpbmcgfVtdIHtcbiAgcmV0dXJuIGxpY2Vuc2VMZWF2ZXMoZXhwcmVzc2lvbikubWFwKChsZWFmKSA9PiB7XG4gICAgY29uc3QgdGV4dCA9IHNwZHhMaWNlbnNlc1tsZWFmLmxpY2Vuc2VdPy5saWNlbnNlVGV4dDtcbiAgICBpZiAoXCJleGNlcHRpb25cIiBpbiBsZWFmIHx8IHRleHQgPT09IHVuZGVmaW5lZCkge1xuICAgICAgdGhyb3cgbmV3IEVycm9yKFxuICAgICAgICBgJHtsYWJlbH0gZGVjbGFyZXMgJHtsaWNlbnNlfSwgd2hvc2UgdGVybXMgYXJlIG5vdCBvbiB0aGUgU1BEWCBMaWNlbnNlIExpc3QsIGFuZCBgICtcbiAgICAgICAgICBcInNoaXBzIG5vIGxpY2Vuc2UgZmlsZTsgZmluZCBpdHMgdGVybXMgYmVmb3JlIGJ1bmRsaW5nIGl0XCJcbiAgICAgICk7XG4gICAgfVxuICAgIHJldHVybiB7IGlkOiBsZWFmLmxpY2Vuc2UsIHRleHQ6IHRleHQudHJpbSgpIH07XG4gIH0pO1xufVxuXG5hc3luYyBmdW5jdGlvbiByZWFkUGFja2FnZShyb290OiBzdHJpbmcpOiBQcm9taXNlPEJ1bmRsZWRQYWNrYWdlPiB7XG4gIGNvbnN0IG1hbmlmZXN0OiB1bmtub3duID0gSlNPTi5wYXJzZShhd2FpdCByZWFkRmlsZShqb2luKHJvb3QsIFwicGFja2FnZS5qc29uXCIpLCBcInV0ZjhcIikpO1xuICBjb25zdCBpZGVudGl0eSA9IFBhY2thZ2VJZGVudGl0eS5zYWZlUGFyc2UobWFuaWZlc3QpO1xuICBpZiAoIWlkZW50aXR5LnN1Y2Nlc3MpIHtcbiAgICB0aHJvdyBuZXcgRXJyb3IoYCR7cm9vdH0vcGFja2FnZS5qc29uIG5hbWVzIG5vIHBhY2thZ2UgbmFtZSBhbmQgdmVyc2lvbmApO1xuICB9XG4gIGNvbnN0IHsgbmFtZSwgdmVyc2lvbiB9ID0gaWRlbnRpdHkuZGF0YTtcbiAgY29uc3QgbGFiZWwgPSBgJHtuYW1lfUAke3ZlcnNpb259YDtcbiAgY29uc3QgZmllbGRzID0gUGFja2FnZUxpY2Vuc2Uuc2FmZVBhcnNlKG1hbmlmZXN0KTtcbiAgaWYgKCFmaWVsZHMuc3VjY2Vzcykge1xuICAgIHRocm93IG5ldyBFcnJvcihcbiAgICAgIGAke2xhYmVsfSBkZWNsYXJlcyBpdHMgbGljZW5zZSBpbiBhIHNoYXBlIG5wbSBkb2VzIG5vdCBkZWZpbmUgYCArXG4gICAgICAgIGAoJHtmaWVsZHMuZXJyb3IuaXNzdWVzLm1hcCgoaXNzdWUpID0+IGlzc3VlLnBhdGguam9pbihcIi5cIikpLmpvaW4oXCIsIFwiKX0pOyBgICtcbiAgICAgICAgXCJmaW5kIGl0cyB0ZXJtcyBiZWZvcmUgYnVuZGxpbmcgaXRcIlxuICAgICk7XG4gIH1cbiAgY29uc3QgeyBsaWNlbnNlOiBkZWNsYXJlZCwgbGljZW5zZXMsIHJlcG9zaXRvcnksIGhvbWVwYWdlIH0gPSBmaWVsZHMuZGF0YTtcbiAgY29uc3QgbGljZW5zZSA9XG4gICAgdHlwZW9mIGRlY2xhcmVkID09PSBcInN0cmluZ1wiXG4gICAgICA/IGRlY2xhcmVkXG4gICAgICA6IChkZWNsYXJlZD8udHlwZSA/PyBsaWNlbnNlcz8ubWFwKChlbnRyeSkgPT4gZW50cnkudHlwZSkuam9pbihcIiBPUiBcIikgPz8gbnVsbCk7XG4gIGNvbnN0IGVudHJpZXMgPSAoYXdhaXQgcmVhZGRpcihyb290LCB7IHdpdGhGaWxlVHlwZXM6IHRydWUgfSkpXG4gICAgLmZpbHRlcigoZW50cnkpID0+IGVudHJ5LmlzRmlsZSgpKVxuICAgIC5tYXAoKGVudHJ5KSA9PiBlbnRyeS5uYW1lKVxuICAgIC5zb3J0KCk7XG4gIGNvbnN0IGxpY2Vuc2VOYW1lcyA9IGVudHJpZXMuZmlsdGVyKChlbnRyeSkgPT4gTElDRU5TRV9GSUxFLnRlc3QoZW50cnkpKTtcbiAgY29uc3QgbmFtZXMgPSBlbnRyaWVzLmZpbHRlcigoZW50cnkpID0+IExJQ0VOU0VfRklMRS50ZXN0KGVudHJ5KSB8fCBOT1RJQ0VfRklMRS50ZXN0KGVudHJ5KSk7XG4gIGNvbnN0IGZpbGVzID0gYXdhaXQgUHJvbWlzZS5hbGwoXG4gICAgbmFtZXMubWFwKGFzeW5jIChmaWxlKSA9PiAoe1xuICAgICAgbmFtZTogZmlsZSxcbiAgICAgIHRleHQ6IChhd2FpdCByZWFkRmlsZShqb2luKHJvb3QsIGZpbGUpLCBcInV0ZjhcIikpLnRyaW0oKSxcbiAgICB9KSlcbiAgKTtcbiAgLy8gVGhlIHRlcm1zIG11c3Qgc2hpcCBlaXRoZXIgd2F5OiBpbiB0aGUgcGFja2FnZSdzIG93biBsaWNlbnNlIGZpbGUsIG9yLCBmb3IgbGljZW5zZXMgb24gdGhlXG4gIC8vIFNQRFggbGlzdCwgYXMgdGhhdCBsaXN0J3Mgc3RhbmRhcmQgdGV4dC4gQSBsaWNlbnNlIHRoZSBsaXN0IGRvZXMgbm90IGhvbGQgKExpY2Vuc2VSZWYtLFxuICAvLyBEb2N1bWVudFJlZi0pLCBhbiBleGNlcHRpb24sIG9yIG5vIGRlY2xhcmF0aW9uIGF0IGFsbCBsZWF2ZXMgdGhlIGxpY2Vuc2UgZmlsZSBhcyB0aGUgb25seVxuICAvLyBzb3VyY2Ugb2YgaXRzIHRlcm1zLlxuICBjb25zdCBzdGFuZGFyZFRleHRzOiB7IGlkOiBzdHJpbmc7IHRleHQ6IHN0cmluZyB9W10gPSBbXTtcbiAgaWYgKGxpY2Vuc2UgPT09IG51bGwpIHtcbiAgICBpZiAobGljZW5zZU5hbWVzLmxlbmd0aCA9PT0gMCkge1xuICAgICAgdGhyb3cgbmV3IEVycm9yKFxuICAgICAgICBgJHtsYWJlbH0gZGVjbGFyZXMgbm8gbGljZW5zZSBhbmQgc2hpcHMgbm8gbGljZW5zZSBmaWxlOyBmaW5kIGl0cyB0ZXJtcyBiZWZvcmUgYnVuZGxpbmcgaXRgXG4gICAgICApO1xuICAgIH1cbiAgfSBlbHNlIHtcbiAgICBsZXQgZXhwcmVzc2lvbjogcGFyc2VTcGR4RXhwcmVzc2lvbi5JbmZvO1xuICAgIHRyeSB7XG4gICAgICBleHByZXNzaW9uID0gcGFyc2VTcGR4RXhwcmVzc2lvbihsaWNlbnNlKTtcbiAgICB9IGNhdGNoIHtcbiAgICAgIHRocm93IG5ldyBFcnJvcihcbiAgICAgICAgYCR7bGFiZWx9IGRlY2xhcmVzIGFuIHVucmVjb2duaXplZCBsaWNlbnNlICR7bGljZW5zZX07IGZpbmQgaXRzIHRlcm1zIGJlZm9yZSBidW5kbGluZyBpdGBcbiAgICAgICk7XG4gICAgfVxuICAgIGlmIChsaWNlbnNlTmFtZXMubGVuZ3RoID09PSAwKSB7XG4gICAgICBzdGFuZGFyZFRleHRzLnB1c2goLi4uc3RhbmRhcmRUZXh0c0ZvcihleHByZXNzaW9uLCBsaWNlbnNlLCBsYWJlbCkpO1xuICAgIH1cbiAgfVxuICAvLyBXaGVyZSBhIHJlY2lwaWVudCBnZXRzIHRoZSBwYWNrYWdlJ3Mgc291cmNlLCB3aGljaCBhIGNvcHlsZWZ0IGxpY2Vuc2Ugc3VjaCBhcyBFUEwtMi4wIChlbGtqcywgaW5cbiAgLy8gdGhlIERpc3BhdGNoIHdlYiBidW5kbGUpIHJlcXVpcmVzIHRoZSBub3RpY2VzIHRvIHNheS5cbiAgY29uc3Qgc291cmNlID1cbiAgICAodHlwZW9mIHJlcG9zaXRvcnkgPT09IFwic3RyaW5nXCIgPyByZXBvc2l0b3J5IDogcmVwb3NpdG9yeT8udXJsKSA/PyBob21lcGFnZSA/PyBudWxsO1xuICByZXR1cm4geyBuYW1lLCB2ZXJzaW9uLCBsaWNlbnNlLCBzb3VyY2UsIGZpbGVzLCBzdGFuZGFyZFRleHRzIH07XG59XG5cbi8qKlxuICogVGhlIG5vdGljZXMgdGV4dCBmb3IgYSBidW5kbGUgYnVpbHQgZnJvbSBgaW5wdXRzYDogdGhlIG1vZHVsZSBmaWxlIHBhdGhzIGEgYnVuZGxlciBsaXN0cyAoYSBCdW5cbiAqIG1ldGFmaWxlJ3MgYE9iamVjdC5rZXlzKHJlc3VsdC5tZXRhZmlsZS5pbnB1dHMpYCksIGFic29sdXRlIG9yIHJlbGF0aXZlIHRvIGBiYXNlYC4gQSBtb2R1bGVcbiAqIGluc3RhbGxlZCB1bmRlciBub2RlX21vZHVsZXMgY291bnRzIGFzIGl0cyBwYWNrYWdlJ3M7IGEgbW9kdWxlIG9mIHRoaXMgcmVwb3NpdG9yeSBjb3VudHMgb25seVxuICogd2hlbiBpdHMgd29ya3NwYWNlIHBhY2thZ2UgY2FycmllcyBhIGxpY2Vuc2UgZmlsZSBvZiBpdHMgb3duLiBQYWNrYWdlcyBhcmUgc29ydGVkIGJ5IG5hbWUgYW5kXG4gKiB2ZXJzaW9uLCBzbyB0aGUgc2FtZSBpbnB1dHMgYWx3YXlzIGdpdmUgdGhlIHNhbWUgdGV4dC5cbiAqL1xuZXhwb3J0IGFzeW5jIGZ1bmN0aW9uIHRoaXJkUGFydHlOb3RpY2VzKGlucHV0czogSXRlcmFibGU8c3RyaW5nPiwgYmFzZTogc3RyaW5nKTogUHJvbWlzZTxzdHJpbmc+IHtcbiAgY29uc3Qgcm9vdHMgPSBuZXcgU2V0PHN0cmluZz4oKTtcbiAgZm9yIChjb25zdCBpbnB1dCBvZiBpbnB1dHMpIHtcbiAgICBjb25zdCBtb2R1bGVQYXRoID0gYXdhaXQgcmVhbHBhdGgocmVzb2x2ZShiYXNlLCBpbnB1dCkpO1xuICAgIGNvbnN0IHJvb3QgPSBpbnN0YWxsZWRSb290T2YobW9kdWxlUGF0aCkgPz8gKGF3YWl0IGxpY2Vuc2VkV29ya3NwYWNlUm9vdE9mKG1vZHVsZVBhdGgpKTtcbiAgICBpZiAocm9vdCAhPT0gbnVsbCkgcm9vdHMuYWRkKHJvb3QpO1xuICB9XG4gIGNvbnN0IHBhY2thZ2VzID0gbmV3IE1hcDxzdHJpbmcsIEJ1bmRsZWRQYWNrYWdlPigpO1xuICBmb3IgKGNvbnN0IHJvb3Qgb2Ygcm9vdHMpIHtcbiAgICBjb25zdCBidW5kbGVkID0gYXdhaXQgcmVhZFBhY2thZ2Uocm9vdCk7XG4gICAgcGFja2FnZXMuc2V0KGAke2J1bmRsZWQubmFtZX1AJHtidW5kbGVkLnZlcnNpb259YCwgYnVuZGxlZCk7XG4gIH1cbiAgY29uc3Qgc29ydGVkID0gWy4uLnBhY2thZ2VzXS5zb3J0KChbYV0sIFtiXSkgPT4gKGEgPCBiID8gLTEgOiBhID4gYiA/IDEgOiAwKSk7XG4gIGNvbnN0IHNlY3Rpb25zID0gc29ydGVkLm1hcCgoW2tleSwgYnVuZGxlZF0pID0+IHtcbiAgICBjb25zdCBsaW5lcyA9IFtrZXksIGBMaWNlbnNlOiAke2J1bmRsZWQubGljZW5zZSA/PyBcIihub3QgZGVjbGFyZWQgaW4gcGFja2FnZS5qc29uKVwifWBdO1xuICAgIGlmIChidW5kbGVkLnNvdXJjZSAhPT0gbnVsbCkgbGluZXMucHVzaChgU291cmNlOiAke2J1bmRsZWQuc291cmNlfWApO1xuICAgIGZvciAoY29uc3Qgc3RhbmRhcmQgb2YgYnVuZGxlZC5zdGFuZGFyZFRleHRzKSB7XG4gICAgICBsaW5lcy5wdXNoKFxuICAgICAgICBcIlwiLFxuICAgICAgICBgLS0tICR7c3RhbmRhcmQuaWR9OiB0aGUgU1BEWCBMaWNlbnNlIExpc3QncyBzdGFuZGFyZCB0ZXh0ICh0aGUgcGFja2FnZSBzaGlwcyBubyBsaWNlbnNlIGZpbGUpIC0tLWAsXG4gICAgICAgIFwiXCIsXG4gICAgICAgIHN0YW5kYXJkLnRleHRcbiAgICAgICk7XG4gICAgfVxuICAgIGZvciAoY29uc3QgZmlsZSBvZiBidW5kbGVkLmZpbGVzKSB7XG4gICAgICBsaW5lcy5wdXNoKFwiXCIsIGAtLS0gJHtmaWxlLm5hbWV9IC0tLWAsIFwiXCIsIGZpbGUudGV4dCk7XG4gICAgfVxuICAgIHJldHVybiBsaW5lcy5qb2luKFwiXFxuXCIpO1xuICB9KTtcbiAgY29uc3QgcnVsZSA9IGBcXG5cXG4ke1wiPVwiLnJlcGVhdCg4MCl9XFxuXFxuYDtcbiAgcmV0dXJuIChcbiAgICBcIlRoaXJkLXBhcnR5IHNvZnR3YXJlIGlubGluZWQgaW50byB0aGUgYnVuZGxlcyBpbiB0aGlzIGRpcmVjdG9yeSwgd2l0aCBlYWNoIHBhY2thZ2UncyBsaWNlbnNlLlxcblwiICtcbiAgICBydWxlLnRyaW1TdGFydCgpICtcbiAgICBzZWN0aW9ucy5qb2luKHJ1bGUpICtcbiAgICBcIlxcblwiXG4gICk7XG59XG5cbi8qKlxuICogVGhlIGRpcmVjdG9yeSBvZiB0aGUgcGFja2FnZSBgbmFtZWAgYXMgTm9kZSBmaW5kcyBpdCBmcm9tIGBmcm9tYDogdGhlIGZpcnN0IGFuY2VzdG9yJ3NcbiAqIGBub2RlX21vZHVsZXMvPG5hbWU+YCwgcmVhZCByZWdhcmRsZXNzIG9mIHRoZSBwYWNrYWdlJ3MgYGV4cG9ydHNgLCBzaW5jZSBvbmx5IGl0cyByb290IGlzIHdhbnRlZC5cbiAqL1xuZXhwb3J0IGZ1bmN0aW9uIHBhY2thZ2VSb290KG5hbWU6IHN0cmluZywgZnJvbTogc3RyaW5nKTogc3RyaW5nIHtcbiAgZm9yIChsZXQgZGlyZWN0b3J5ID0gcmVzb2x2ZShmcm9tKTsgOyBkaXJlY3RvcnkgPSBkaXJuYW1lKGRpcmVjdG9yeSkpIHtcbiAgICBjb25zdCBjYW5kaWRhdGUgPSBqb2luKGRpcmVjdG9yeSwgXCJub2RlX21vZHVsZXNcIiwgbmFtZSk7XG4gICAgaWYgKGV4aXN0c1N5bmMoam9pbihjYW5kaWRhdGUsIFwicGFja2FnZS5qc29uXCIpKSkgcmV0dXJuIHJlYWxwYXRoU3luYyhjYW5kaWRhdGUpO1xuICAgIGlmIChkaXJlY3RvcnkgPT09IGRpcm5hbWUoZGlyZWN0b3J5KSkge1xuICAgICAgdGhyb3cgbmV3IEVycm9yKGBubyBwYWNrYWdlICR7bmFtZX0gaXMgaW5zdGFsbGVkIGFib3ZlICR7ZnJvbX1gKTtcbiAgICB9XG4gIH1cbn1cblxuLyoqXG4gKiBDb2RlIGEgVml0ZSBidWlsZCBlbWl0cyBmcm9tIG5vIHNvdXJjZSBmaWxlIGl0IHJlY29yZHM6IGEgYnVuZGxlcidzIHJ1bnRpbWUgaGVscGVyLCBvciBhIHNjcmlwdFxuICogb3Igc3R5bGVzaGVldCBhIGZyYW1ld29yayBnZW5lcmF0ZXMuIGBpZGAgbWF0Y2hlcyB0aGUgbW9kdWxlIGlkLCBvciB0aGUgYXNzZXQncyBmaWxlIG5hbWU7XG4gKiBgcGFja2FnZXNgIGFyZSB0aGUgZGlyZWN0b3JpZXMgb2YgdGhlIHBhY2thZ2VzIHdob3NlIGNvZGUgaXQgaXMgKGBwYWNrYWdlUm9vdGApLCBhbmQgbm9uZSBmb3IgYVxuICogd3JhcHBlciBhcm91bmQgYSBtb2R1bGUgd2hvc2Ugb3duIGZpbGUgdGhlIGJ1bmRsZSByZWNvcmRzLlxuICovXG5leHBvcnQgaW50ZXJmYWNlIEdlbmVyYXRlZENvZGUge1xuICBpZDogUmVnRXhwO1xuICBwYWNrYWdlczogc3RyaW5nW107XG59XG5cbi8qKlxuICogVml0ZSdzIG93biBydW50aW1lIGhlbHBlciAoaXRzIG1vZHVsZS1wcmVsb2FkIHNoaW0pLCBnZW5lcmF0ZWQgY29kZSBldmVyeSBWaXRlLWJhc2VkIGJ1aWxkXG4gKiBlbWl0cyB3aXRoIG5vIHNvdXJjZSBmaWxlIHJlY29yZGluZyBpdC4gRXZlcnkgVml0ZSBidWlsZCBuZWVkcyB0aGlzIGV4YWN0IGVudHJ5IGFsb25nc2lkZSBpdHNcbiAqIG93biBidW5kbGVyLXNwZWNpZmljIG9uZXMgKERpc3BhdGNoJ3MgU1BBLCB0aGUgZG9jcyBzaXRlJ3MgQXN0cm8gYnVpbGQpLlxuICovXG5leHBvcnQgZnVuY3Rpb24gdml0ZVJ1bnRpbWVHZW5lcmF0ZWRDb2RlKHZpdGVSb290OiBzdHJpbmcpOiBHZW5lcmF0ZWRDb2RlIHtcbiAgcmV0dXJuIHsgaWQ6IC9eXFwwdml0ZVxcLy8sIHBhY2thZ2VzOiBbdml0ZVJvb3RdIH07XG59XG5cbi8qKiBXaGF0IGB2aXRlQnVuZGxlSW5wdXRzYCByZWFkcyBmcm9tIGEgVml0ZSBidWlsZDogaXRzIHBsdWdpbiBjb250ZXh0IGFuZCBpdHMgb3V0cHV0IGJ1bmRsZS4gKi9cbmludGVyZmFjZSBWaXRlQnVpbGQge1xuICBnZXRNb2R1bGVJZHMoKTogSXRlcmFibGU8c3RyaW5nPjtcbiAgLyoqIEFic2VudCBmcm9tIFJvbGxkb3duLCBWaXRlIDgncyBidW5kbGVyLiAqL1xuICBnZXRXYXRjaEZpbGVzPygpOiBzdHJpbmdbXTtcbn1cbnR5cGUgVml0ZU91dHB1dCA9XG4gIHwgeyB0eXBlOiBcImNodW5rXCI7IG1vZHVsZXM6IFJlY29yZDxzdHJpbmcsIHVua25vd24+IH1cbiAgfCB7IHR5cGU6IFwiYXNzZXRcIjsgZmlsZU5hbWU6IHN0cmluZzsgb3JpZ2luYWxGaWxlTmFtZXM6IHJlYWRvbmx5IHN0cmluZ1tdIH07XG5cbi8qKiBUYWlsd2luZCdzIGBAcGx1Z2luYCBhbmQgYEBjb25maWdgOiBKYXZhU2NyaXB0IGl0IHJ1bnMgd2hpbGUgY29tcGlsaW5nIGEgc3R5bGVzaGVldCwgd2hvc2VcbiAqICBvdXRwdXQgbGFuZHMgaW4gdGhlIENTUyBhbHRob3VnaCB0aGUgcGFja2FnZSBuZXZlciBlbnRlcnMgdGhlIG1vZHVsZSBncmFwaC4gKi9cbmNvbnN0IFRBSUxXSU5EX0pTX0RJUkVDVElWRSA9IC9AKD86cGx1Z2lufGNvbmZpZylcXHMrW1wiJ10oW15cIiddKylbXCInXS9nO1xuXG4vKipcbiAqIFRoZSBmaWxlcyBhIFZpdGUgYnVpbGQgY29waWVzIGludG8gaXRzIG91dHB1dCwgYXMgYHRoaXJkUGFydHlOb3RpY2VzYCBpbnB1dHM6IHRoZSBtb2R1bGVzIHRoZVxuICogY2h1bmtzIHJlbmRlciAodHJlZS1zaGFrZW4gbW9kdWxlcyBhcmUgbGVmdCBvdXQpLCB0aGUgc291cmNlIGZpbGUgb2YgZXZlcnkgZW1pdHRlZCBhc3NldCwgdGhlXG4gKiBmaWxlcyBhIHBsdWdpbiByZWFkIHRoYXQgYXJlIG5vIG1vZHVsZSBhdCBhbGwgKHRoZSBzdHlsZXNoZWV0cyBhIENTUyBgQGltcG9ydGAgcHVsbHMgaW4pLCB0aGVcbiAqIHBhY2thZ2VzIGEgc3R5bGVzaGVldCBuYW1lcyB0byBUYWlsd2luZCBhcyBwbHVnaW5zLCBhbmQgdGhlIHBhY2thZ2UgYGdlbmVyYXRlZGAgbmFtZXMgZm9yIGVhY2hcbiAqIG1vZHVsZSBvciBhc3NldCB0aGF0IGhhcyBubyBzb3VyY2UgZmlsZS4gQSBtb2R1bGUgaWQgb3IgYXNzZXQgb2YgdGhhdCBraW5kIHdoaWNoIGBnZW5lcmF0ZWRgXG4gKiBkb2VzIG5vdCBtYXRjaCBmYWlscyB0aGUgYnVpbGQsIG5hbWluZyBpdC4gQSBgP3F1ZXJ5YCBzdWZmaXggbmFtZXMgdGhlIGZpbGUgYmVmb3JlIGl0LCBhbmQgYVxuICogcmVsYXRpdmUgaWQgaXMgcmVsYXRpdmUgdG8gYHJvb3RgLCBWaXRlJ3Mgcm9vdC5cbiAqL1xuZXhwb3J0IGFzeW5jIGZ1bmN0aW9uIHZpdGVCdW5kbGVJbnB1dHMoXG4gIGJ1aWxkOiBWaXRlQnVpbGQsXG4gIGJ1bmRsZTogUmVjb3JkPHN0cmluZywgVml0ZU91dHB1dD4sXG4gIHJvb3Q6IHN0cmluZyxcbiAgZ2VuZXJhdGVkOiBHZW5lcmF0ZWRDb2RlW11cbik6IFByb21pc2U8U2V0PHN0cmluZz4+IHtcbiAgY29uc3QgZmlsZSA9IChpZDogc3RyaW5nKSA9PiByZXNvbHZlKHJvb3QsIGlkLnJlcGxhY2UoL1xcPy4qJC8sIFwiXCIpKTtcbiAgY29uc3QgaW5wdXRzID0gbmV3IFNldDxzdHJpbmc+KCk7XG4gIGNvbnN0IHVuYWNjb3VudGVkID0gbmV3IFNldDxzdHJpbmc+KCk7XG4gIGNvbnN0IGdlbmVyYXRlZEJ5ID0gKGlkOiBzdHJpbmcpID0+IHtcbiAgICBjb25zdCBydWxlID0gZ2VuZXJhdGVkLmZpbmQoKGNhbmRpZGF0ZSkgPT4gY2FuZGlkYXRlLmlkLnRlc3QoaWQpKTtcbiAgICBpZiAocnVsZSA9PT0gdW5kZWZpbmVkKSB1bmFjY291bnRlZC5hZGQoaWQpO1xuICAgIGVsc2UgZm9yIChjb25zdCBkaXJlY3Rvcnkgb2YgcnVsZS5wYWNrYWdlcykgaW5wdXRzLmFkZChqb2luKGRpcmVjdG9yeSwgXCJwYWNrYWdlLmpzb25cIikpO1xuICB9O1xuICBmb3IgKGNvbnN0IG91dHB1dCBvZiBPYmplY3QudmFsdWVzKGJ1bmRsZSkpIHtcbiAgICBpZiAob3V0cHV0LnR5cGUgPT09IFwiY2h1bmtcIikge1xuICAgICAgZm9yIChjb25zdCBpZCBvZiBPYmplY3Qua2V5cyhvdXRwdXQubW9kdWxlcykpIHtcbiAgICAgICAgaWYgKGlzQWJzb2x1dGUoaWQpKSBpbnB1dHMuYWRkKGZpbGUoaWQpKTtcbiAgICAgICAgZWxzZSBnZW5lcmF0ZWRCeShpZCk7XG4gICAgICB9XG4gICAgfSBlbHNlIGlmIChvdXRwdXQub3JpZ2luYWxGaWxlTmFtZXMubGVuZ3RoID09PSAwKSB7XG4gICAgICBnZW5lcmF0ZWRCeShvdXRwdXQuZmlsZU5hbWUpO1xuICAgIH0gZWxzZSB7XG4gICAgICBmb3IgKGNvbnN0IGlkIG9mIG91dHB1dC5vcmlnaW5hbEZpbGVOYW1lcykgaW5wdXRzLmFkZChmaWxlKGlkKSk7XG4gICAgfVxuICB9XG4gIGlmICh1bmFjY291bnRlZC5zaXplID4gMCkge1xuICAgIHRocm93IG5ldyBFcnJvcihcbiAgICAgIGB0aGUgYnVpbGQgZW1pdHMgJHtKU09OLnN0cmluZ2lmeShbLi4udW5hY2NvdW50ZWRdKX0sIHdoaWNoIG5vIHNvdXJjZSBmaWxlIG9yIGtub3duIGAgK1xuICAgICAgICBcImdlbmVyYXRvciBhY2NvdW50cyBmb3I7IG5hbWUgdGhlIHBhY2thZ2UgdGhhdCBnZW5lcmF0ZXMgZWFjaFwiXG4gICAgKTtcbiAgfVxuICBjb25zdCBtb2R1bGVzID0gbmV3IFNldChbLi4uYnVpbGQuZ2V0TW9kdWxlSWRzKCldLmZpbHRlcigoaWQpID0+IGlzQWJzb2x1dGUoaWQpKS5tYXAoZmlsZSkpO1xuICBmb3IgKGNvbnN0IGlkIG9mIGJ1aWxkLmdldFdhdGNoRmlsZXM/LigpID8/IFtdKSB7XG4gICAgLy8gQSB3YXRjaCBlbnRyeSBtYXkgYWxzbyBiZSBhIGRpcmVjdG9yeSBvciBhIGdsb2IgVGFpbHdpbmQgc2NhbnMgZm9yIGNsYXNzIG5hbWVzLlxuICAgIGNvbnN0IHBhdGggPSBmaWxlKGlkKTtcbiAgICBpZiAoIW1vZHVsZXMuaGFzKHBhdGgpICYmIChhd2FpdCBzdGF0KHBhdGgpLmNhdGNoKCgpID0+IG51bGwpKT8uaXNGaWxlKCkpIGlucHV0cy5hZGQocGF0aCk7XG4gIH1cbiAgZm9yIChjb25zdCBpbnB1dCBvZiBbLi4uaW5wdXRzXS5maWx0ZXIoKHBhdGgpID0+IHBhdGguZW5kc1dpdGgoXCIuY3NzXCIpKSkge1xuICAgIGNvbnN0IHJlc29sdmVGcm9tID0gY3JlYXRlUmVxdWlyZShpbnB1dCkucmVzb2x2ZTtcbiAgICBmb3IgKGNvbnN0IFssIHNwZWNpZmllcl0gb2YgKGF3YWl0IHJlYWRGaWxlKGlucHV0LCBcInV0ZjhcIikpLm1hdGNoQWxsKFRBSUxXSU5EX0pTX0RJUkVDVElWRSkpIHtcbiAgICAgIC8vIFRoZSBwYXR0ZXJuJ3Mgb25lIGdyb3VwIG1hdGNoZXMgd2hlbmV2ZXIgdGhlIHBhdHRlcm4gZG9lcy5cbiAgICAgIGlucHV0cy5hZGQocmVzb2x2ZUZyb20oc3BlY2lmaWVyIGFzIHN0cmluZykpO1xuICAgIH1cbiAgfVxuICByZXR1cm4gaW5wdXRzO1xufVxuXG4vLyBgYnVuIHNjcmlwdHMvdGhpcmQtcGFydHktbm90aWNlcy50cyA8bWV0YWZpbGU+Li4uIDxvdXQ+YCB3cml0ZXMgdGhlIG5vdGljZXMgZm9yIG9uZSBvciBtb3JlIGBidW5cbi8vIGJ1aWxkIC0tbWV0YWZpbGU9PG1ldGFmaWxlPmAgcnVucyBpbiB0aGUgY3VycmVudCBkaXJlY3RvcnksIHdoaWNoIHRoZSBtZXRhZmlsZXMnIGlucHV0IHBhdGhzIGFyZVxuLy8gcmVsYXRpdmUgdG86IGEgcGFja2FnZSB0aGF0IHNoaXBzIHNldmVyYWwgYnVuZGxlcyBsaXN0cyBldmVyeSBwYWNrYWdlIGFueSBvZiB0aGVtIGlubGluZXMuXG5pZiAoaW1wb3J0Lm1ldGEubWFpbikge1xuICBjb25zdCBhcmdzID0gcHJvY2Vzcy5hcmd2LnNsaWNlKDIpO1xuICBjb25zdCBvdXQgPSBhcmdzLnBvcCgpO1xuICBpZiAob3V0ID09PSB1bmRlZmluZWQgfHwgYXJncy5sZW5ndGggPT09IDApIHtcbiAgICBwcm9jZXNzLnN0ZGVyci53cml0ZShcInVzYWdlOiBidW4gc2NyaXB0cy90aGlyZC1wYXJ0eS1ub3RpY2VzLnRzIDxtZXRhZmlsZT4uLi4gPG91dD5cXG5cIik7XG4gICAgcHJvY2Vzcy5leGl0KDIpO1xuICB9XG4gIGNvbnN0IGlucHV0czogc3RyaW5nW10gPSBbXTtcbiAgZm9yIChjb25zdCBtZXRhZmlsZSBvZiBhcmdzKSB7XG4gICAgY29uc3QgcGFyc2VkID0gSlNPTi5wYXJzZShhd2FpdCByZWFkRmlsZShtZXRhZmlsZSwgXCJ1dGY4XCIpKSBhcyB7XG4gICAgICBpbnB1dHM6IFJlY29yZDxzdHJpbmcsIHVua25vd24+O1xuICAgIH07XG4gICAgaW5wdXRzLnB1c2goLi4uT2JqZWN0LmtleXMocGFyc2VkLmlucHV0cykpO1xuICB9XG4gIGF3YWl0IHdyaXRlRmlsZShvdXQsIGF3YWl0IHRoaXJkUGFydHlOb3RpY2VzKGlucHV0cywgcHJvY2Vzcy5jd2QoKSkpO1xufVxuIl0sCiAgIm1hcHBpbmdzIjogIjtBQUEwWSxTQUFTLGlCQUFBQSxzQkFBcUI7QUFDeGEsU0FBUyxxQkFBcUI7QUFDOUIsT0FBTyxpQkFBaUI7QUFDeEIsT0FBTyxXQUFXO0FBQ2xCLFNBQVMsb0JBQWlDOzs7QUNEMUMsU0FBUyxZQUFZLG9CQUFvQjtBQUN6QyxTQUFTLFNBQVMsVUFBVSxVQUFVLE1BQU0saUJBQWlCO0FBQzdELFNBQVMscUJBQXFCO0FBQzlCLFNBQVMsU0FBUyxZQUFZLE1BQU0s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QztBQUFBLE1BQzNGLHNDQUFzQztBQUFBLFFBQ3BDLElBQUksSUFBSSwyQ0FBMkMsd0NBQWU7QUFBQSxNQUNwRTtBQUFBLE1BQ0EsaUNBQWlDO0FBQUEsUUFDL0IsSUFBSSxJQUFJLDBDQUEwQyx3Q0FBZTtBQUFBLE1BQ25FO0FBQUEsTUFDQSxnQ0FBZ0M7QUFBQSxRQUM5QixJQUFJLElBQUkseUNBQXlDLHdDQUFlO0FBQUEsTUFDbEU7QUFBQSxNQUNBLHVDQUF1QztBQUFBLFFBQ3JDLElBQUksSUFBSSw0Q0FBNEMsd0NBQWU7QUFBQSxNQUNyRTtBQUFBLE1BQ0Esa0NBQWtDO0FBQUEsUUFDaEMsSUFBSSxJQUFJLGtDQUFrQyx3Q0FBZTtBQUFBLE1BQzNEO0FBQUEsTUFDQSxzQ0FBc0M7QUFBQSxRQUNwQyxJQUFJLElBQUksMkNBQTJDLHdDQUFlO0FBQUEsTUFDcEU7QUFBQSxNQUNBLHdCQUF3QjtBQUFBLFFBQ3RCLElBQUksSUFBSSxpQ0FBaUMsd0NBQWU7QUFBQSxNQUMxRDtBQUFBO0FBQUE7QUFBQTtBQUFBLE1BSUEsMEJBQTBCO0FBQUEsUUFDeEIsSUFBSSxJQUFJLDBEQUEwRCx3Q0FBZTtBQUFBLE1BQ25GO0FBQUEsSUFDRjtBQUFBLEVBQ0Y7QUFBQSxFQUNBLE9BQU87QUFBQSxJQUNMLFFBQVE7QUFBQTtBQUFBO0FBQUE7QUFBQTtBQUFBO0FBQUE7QUFBQTtBQUFBO0FBQUE7QUFBQTtBQUFBO0FBQUE7QUFBQTtBQUFBO0FBQUE7QUFBQTtBQUFBO0FBQUE7QUFBQTtBQUFBO0FBQUE7QUFBQTtBQUFBO0FBQUE7QUFBQTtBQUFBO0FBQUE7QUFBQTtBQUFBO0FBQUE7QUFBQTtBQUFBO0FBQUE7QUFBQTtBQUFBO0FBQUE7QUFBQTtBQUFBO0FBQUE7QUFBQTtBQUFBO0FBQUE7QUFBQTtBQUFBO0FBQUE7QUFBQTtBQUFBO0FBQUE7QUFBQTtBQUFBO0FBQUE7QUFBQTtBQUFBLElBcURSLHVCQUF1QjtBQUFBO0FBQUE7QUFBQTtBQUFBO0FBQUE7QUFBQTtBQUFBO0FBQUE7QUFBQTtBQUFBLElBVXZCLGVBQWUsRUFBRSxVQUFVLE1BQU07QUFBQSxJQUNqQyxhQUFhO0FBQUEsRUFDZjtBQUFBLEVBQ0EsUUFBUTtBQUFBLElBQ04sT0FBTztBQUFBLE1BQ0wsUUFBUTtBQUFBLE1BQ1IsU0FBUztBQUFBLE1BQ1QsT0FBTztBQUFBLFFBQ0wsUUFBUTtBQUFBLFFBQ1IsSUFBSTtBQUFBLE1BQ047QUFBQSxJQUNGO0FBQUEsRUFDRjtBQUNGLENBQUM7IiwKICAibmFtZXMiOiBbImNyZWF0ZVJlcXVpcmUiLCAiY3JlYXRlUmVxdWlyZSJdCn0K
