/** What the shell's chunk-recovery rows need of the page: the assets the document names, a
 *  deployment that never built one of them, the tab's record of the reloads it has spent, and
 *  what each load of the tab did with a chunk failure.
 *
 *  Two spec files share this. `shell.stub.e2e.ts` holds the rows that need nothing but the
 *  page and its own routes; `shell.e2e.ts` holds the rows whose failed asset must be the
 *  server's own 404, which the stubbed layer's file server does not give (it answers an
 *  unknown path with `index.html`, as the SPA fallback).
 */
import { expect, type Page } from "@playwright/test";

/** The path of the asset the page at `origin` names in `pattern`'s first group. */
export async function pageAssetPath(
  pattern: RegExp,
  what: string,
  origin: string
): Promise<string> {
  const html = await (await fetch(new URL("/", origin))).text();
  const asset = pattern.exec(html)?.[1];
  if (asset === undefined) {
    throw new Error(`the page names no ${what}: ${html}`);
  }
  return new URL(asset, origin).pathname;
}

/**
 * Answers the page's entry chunk 404, as a server whose deployment never built it does, whenever
 * `missing` says so for that request (numbered from 1; it may hold the request until it answers),
 * and counts the page's document loads and entry requests.
 */
export async function missEntryChunk(
  page: Page,
  missing: (request: number) => boolean | Promise<boolean>,
  origin: string
) {
  // The entry chunk: the module script Vite writes into index.html.
  const entry = await pageAssetPath(
    /<script\b[^>]*\btype="module"[^>]*\bsrc="([^"]+)"/u,
    "module script",
    origin
  );
  const counts = { documents: 0, entryRequests: 0 };
  page.on("request", (request) => {
    if (request.isNavigationRequest() && request.frame() === page.mainFrame()) {
      counts.documents += 1;
    }
  });
  await page.route(
    (url) => url.pathname === entry,
    async (route) => {
      counts.entryRequests += 1;
      if (!(await missing(counts.entryRequests))) {
        await route.continue();
        return;
      }
      await route.fulfill({
        body: JSON.stringify({ error: "not found" }),
        contentType: "application/json",
        status: 404,
      });
    }
  );
  return counts;
}

/** Records reloads `minutesAgo` by the page's clock as the tab's recovery reloads, and returns
 *  their times. */
export function recordTabReloads(page: Page, minutesAgo: number[]): Promise<number[]> {
  return page.evaluate((ages) => {
    const now = Date.now();
    const times = ages.map((age) => now - age * 60_000);
    window.sessionStorage.setItem("dispatch.chunk-reloads", JSON.stringify(times));
    return times;
  }, minutesAgo);
}

/** The tab's recorded recovery reloads. */
export function tabReloads(page: Page): Promise<number[] | null> {
  return page.evaluate(() =>
    JSON.parse(window.sessionStorage.getItem("dispatch.chunk-reloads") ?? "null")
  );
}

/** What one document load of the tab did with the chunk failures reported to it. */
export interface FailureHandling {
  /** How many of this load's `vite:preloadError` dispatches have run to their end: the bundle's
   *  recovery has judged each of them. */
  readonly preloadErrors: number;
  /** How many `error` events of this load's scripts and links every listener of the window has
   *  seen: `web/index.html`'s inline script has judged each of them. */
  readonly assetErrors: number;
  /** Whether this load began a reload. */
  readonly reloaded: boolean;
}

/** Where the page keeps that record: the tab's session storage, which outlives the reload a
 *  failure may begin, so the load that began it can still be read once the next one is up. */
const FAILURE_HANDLING_KEY = "e2e.failure-handling";

/**
 * Records what each document load of the tab does with a chunk failure, from before any script
 * of the page runs, and returns a wait for the `load`th load (from 1) to have judged one, which
 * answers every load's record as it stood then.
 *
 * A chunk the bundle imports fails twice. Vite asks for it through a
 * `<link rel="modulepreload">` beside the import, so its download fails as that link's `error`
 * event, which the inline script judges, and as the import's `vite:preloadError`, which the
 * bundle's recovery judges (`reloadForChunkFailure`,
 * `web/src/features/shell/DeploymentResilience.tsx`). Each judge decides inside the event it is
 * given: it returns, or it calls `window.location.reload()` there. The two events come in either
 * order - on the stubbed layer the link's came first for some loads and second for others - so
 * the wait is for both: once both have passed their judges, a reload either judge was going to
 * begin has begun, and a row about a failure that must reload nothing reads that at that moment
 * rather than after a margin of the wall clock.
 */
export async function recordFailureHandling(
  page: Page
): Promise<(load: number) => Promise<FailureHandling[]>> {
  await page.addInitScript((key) => {
    const navigation = window.navigation;
    // A frame inside the page shares the tab's session storage and is no load of the tab. An
    // engine without the Navigation API keeps no record at all, so the wait below fails rather
    // than reading a reload it could not see as none.
    if (window !== window.top || navigation === undefined) return;
    const loads: { assetErrors: number; preloadErrors: number; reloaded: boolean }[] = JSON.parse(
      window.sessionStorage.getItem(key) ?? "[]"
    );
    const load = { assetErrors: 0, preloadErrors: 0, reloaded: false };
    loads.push(load);
    const keep = () => window.sessionStorage.setItem(key, JSON.stringify(loads));
    keep();
    // `location.reload()` fires `navigate` before it returns, so a reload either judge begins is
    // on the record inside the event that began it. The app declares only the part of the event
    // it reads (`web/src/features/shell/DeploymentResilience.tsx`), so the type is read here.
    navigation.addEventListener("navigate", (event) => {
      if (!("navigationType" in event) || event.navigationType !== "reload") return;
      load.reloaded = true;
      keep();
    });
    // Vite dispatches this event from script, so a microtask queued here runs once every listener
    // of that dispatch has returned, the bundle's included.
    window.addEventListener("vite:preloadError", () => {
      queueMicrotask(() => {
        load.preloadErrors += 1;
        keep();
      });
    });
    // An element's `error` event reaches the document's capturing listeners after every
    // capturing listener of the window, which is where the inline script listens.
    document.addEventListener(
      "error",
      (event) => {
        if (event.target instanceof HTMLLinkElement || event.target instanceof HTMLScriptElement) {
          load.assetErrors += 1;
          keep();
        }
      },
      true
    );
  }, FAILURE_HANDLING_KEY);
  return async (load) => {
    let loads: FailureHandling[] = [];
    await expect
      .poll(
        async () => {
          // A read made while a reload takes the document away has no page to run in; the last
          // record read stands, and the next poll reads the new one.
          loads = await page
            .evaluate(
              (key): FailureHandling[] => JSON.parse(window.sessionStorage.getItem(key) ?? "[]"),
              FAILURE_HANDLING_KEY
            )
            .catch(() => loads);
          const record = loads[load - 1];
          return record !== undefined && record.assetErrors > 0 && record.preloadErrors > 0;
        },
        {
          message: `both judges of a chunk failure judged one in load ${load} of the tab`,
        }
      )
      .toBe(true);
    return loads;
  };
}

/** Adds a script of this build that the server never had, as a replaced deployment's would fail. */
export async function failBuildScript(page: Page): Promise<void> {
  await page.evaluate(() => {
    const script = document.createElement("script");
    script.src = "/assets/index-missing.js";
    document.head.append(script);
  });
}

/** The path of the margin's own chunk (`web/src/features/margin/MarginColumn.tsx`), which the
 *  shell asks for once its hold on a landing has ended (`web/src/features/shell/
 *  content-drawn.ts`). */
export const MARGIN_CHUNK = /\/assets\/Margin-[^/]+\.js$/u;

/** The path of the reference-preview layer's chunk (`web/src/features/refs/RefPreviewLayer.tsx`),
 *  which the shell asks for at the same moment. */
const REF_PREVIEW_CHUNK = /\/assets\/RefPreview-[^/]+\.js$/u;

/** One node of a tree React commits, as far as `lazyComponentsRendered` reads it. */
interface Fiber {
  readonly child: Fiber | null;
  readonly sibling: Fiber | null;
  /** What the element named: for a component behind `lazy`, the lazy object itself, whose
   *  `$$typeof` is `Symbol.for("react.lazy")`, still there once it has resolved. */
  readonly elementType: unknown;
  /** The component React rendered: for a lazy one, the component its chunk resolved to. */
  readonly type: unknown;
}

declare global {
  interface Window {
    /** Every root React has committed, kept by the DevTools hook `shellLoads` installs. */
    __shellReactRoots?: Set<{ readonly current: Fiber }>;
  }
}

/** Whether, for each chunk at `urls`, the component it carries behind `lazy` is in a tree React
 *  has committed: the component rendered, so the import that loaded it settled. */
function lazyComponentsRendered(page: Page, urls: readonly string[]): Promise<boolean> {
  return page.evaluate(async (chunks) => {
    const lazy = Symbol.for("react.lazy");
    const rendered = new Set<unknown>();
    for (const root of window.__shellReactRoots ?? []) {
      const stack = [root.current];
      for (let fiber = stack.pop(); fiber !== undefined; fiber = stack.pop()) {
        const element = fiber.elementType;
        const isLazy =
          typeof element === "object" &&
          element !== null &&
          "$$typeof" in element &&
          element.$$typeof === lazy;
        if (isLazy) rendered.add(fiber.type);
        if (fiber.child !== null) stack.push(fiber.child);
        if (fiber.sibling !== null) stack.push(fiber.sibling);
      }
    }
    for (const chunk of chunks) {
      // The page's own module of that chunk, whose exports are the components it carries: an
      // import of a URL the page has already loaded is answered from its module map, with no
      // request.
      const module: Record<string, unknown> = await import(chunk);
      if (!Object.values(module).some((value) => rendered.has(value))) return false;
    }
    return true;
  }, urls);
}

/** One document load of a page, as the rows about a prefetched chunk read it. */
export interface ShellLoad {
  /** Settles once the two components the shell renders on every page from a chunk of their own -
   *  the margin and the reference-preview layer - have rendered. A chunk failure while either
   *  chunk is out is a failure a reader is waiting behind, whatever chunk it is
   *  (`web/src/features/shell/DeploymentResilience.tsx`), so a row about a chunk nobody waits on
   *  fails its chunk only after this. */
  readonly settled: () => Promise<void>;
  /** Settles once the Inbox peek's prefetch has gone out (`web/src/features/inbox/InboxHost.tsx`,
   *  `usePeekChunkAfterFirstPaint`), which a page the landing reads as the Inbox starts after its
   *  first paint. */
  readonly peekAsked: Promise<void>;
}

function shellLoad(page: Page) {
  const margin = Promise.withResolvers<string>();
  const preview = Promise.withResolvers<string>();
  const peek = Promise.withResolvers<void>();
  const settled = async () => {
    const chunks = await Promise.all([margin.promise, preview.promise]);
    // A chunk's response is in before the import it answers has settled - the margin's chunk
    // imports chunks of its own - and before `renderChunk`'s count of it has dropped. React
    // renders a lazy component only once its import has settled, and the count drops in the
    // first callbacks of that import, which `renderChunk` registers before React registers its
    // own (`web/src/features/shell/chunk-loads.ts`), so a rendered component is a count already
    // down. Neither draws anything on the pages these rows open - the margin is null on a route
    // that has none, and the layer is null until a card opens - so the tree React committed is
    // where they are read.
    await expect
      .poll(() => lazyComponentsRendered(page, chunks), {
        message: "the margin and the reference-preview layer rendered",
      })
      .toBe(true);
  };
  return { load: { peekAsked: peek.promise, settled }, margin, peek, preview };
}

/** The current document load of `page`, a new one from each navigation of its main frame. */
export async function shellLoads(page: Page): Promise<() => ShellLoad> {
  // React looks for its DevTools hook once, as its bundle runs, so this one is in the page before
  // any of the page's scripts. It keeps each root React commits, which is what DevTools reads a
  // tree from, and nothing else.
  await page.addInitScript(() => {
    const roots = new Set<{ readonly current: Fiber }>();
    window.__shellReactRoots = roots;
    Object.assign(window, {
      __REACT_DEVTOOLS_GLOBAL_HOOK__: {
        inject: () => 1,
        onCommitFiberRoot: (_renderer: number, root: { readonly current: Fiber }) => {
          roots.add(root);
        },
        supportsFiber: true,
      },
    });
  });
  let current = shellLoad(page);
  page.on("request", (request) => {
    if (request.isNavigationRequest() && request.frame() === page.mainFrame()) {
      current = shellLoad(page);
    } else if (/\/assets\/inbox-chunk-[^/]+\.js$/u.test(new URL(request.url()).pathname)) {
      current.peek.resolve();
    }
  });
  page.on("requestfinished", (request) => {
    const url = request.url();
    const path = new URL(url).pathname;
    if (MARGIN_CHUNK.test(path)) current.margin.resolve(url);
    if (REF_PREVIEW_CHUNK.test(path)) current.preview.resolve(url);
  });
  return () => current.load;
}

/** Holds every request for the asset at `pattern` on the wire for the rest of the row: a
 *  download that is out and never answers. */
export async function holdAsset(page: Page, pattern: RegExp): Promise<void> {
  await page.route(pattern, () => undefined);
}

/** Fails every request for the asset at `pattern`, each once `ready` has settled for the load it
 *  belongs to, and returns how many it failed. */
export async function failAssetWhen(
  page: Page,
  pattern: RegExp,
  ready: () => Promise<unknown>
): Promise<() => number> {
  let failed = 0;
  await page.route(pattern, async (route) => {
    await ready();
    await route.abort();
    failed += 1;
  });
  return () => failed;
}
