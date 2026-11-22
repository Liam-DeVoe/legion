// The INFRA-4731 tester's probe of the foot of the screen, as used live in rounds 9 and 10.
// Each export is self-contained, with no closure over this module, so Playwright can send it to
// the page as it stands: `await page.evaluate(readFootEnds)`, `await page.evaluate(readFootElements,
// selectors)`, `await page.evaluate(sampleFootRest, 2000)`. Each reads the page; only
// `readFootEnds` writes anything, and that is the scroll position of each scroller.
//
// The floats are Pending changes and the update banner, read by their accessible names, so a
// float with no box is left out. A "problem" is something a reader of the screen would see.

/**
 * Scrolls the document and every element that scrolls on the block axis (overflow-y auto or
 * scroll, scrollHeight > clientHeight + 1) to the real end of its scroll, six passes three frames
 * apart, so a virtualized list has drawn its last rows and end room has landed. Then, for each
 * scroller, finds its last content: the lowest leaf element (no child element with a box) that has
 * text or is a control, lies on screen and inside the scroller's box, and is neither inside a
 * float nor inside a fixed or sticky element between it and the scroller, nor inside an inner
 * scroller. Answers each scroller with its gap to the end, its box, its last content, its mark
 * (`data-foot-clearance` and the room it holds), and the floats that last content meets; plus the
 * top of the shell's Navigation, which a scroll to the end must leave on the screen (>= 0) where
 * the Navigation shows.
 */
export async function readFootEnds() {
  const floatEls = ['[aria-label="Pending changes"]', '[aria-label="A new version is available"]']
    .map((selector) => document.querySelector(selector))
    .filter((element) => element !== null && element.getBoundingClientRect().height > 0);
  const floats = floatEls.map((element) => ({
    name: element.getAttribute("aria-label"),
    rect: element.getBoundingClientRect(),
  }));
  const meets = (a, b) =>
    Math.min(a.right, b.right) - Math.max(a.left, b.left) > 1 &&
    Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) > 1;
  const frames = (count) =>
    new Promise((resolve) => {
      let seen = 0;
      const tick = () => (++seen >= count ? resolve() : requestAnimationFrame(tick));
      requestAnimationFrame(tick);
    });
  const scrollsOnBlockAxis = (element) => {
    const { overflowY } = getComputedStyle(element);
    return (
      (overflowY === "auto" || overflowY === "scroll") &&
      element.scrollHeight > element.clientHeight + 1 &&
      element.getBoundingClientRect().height > 0
    );
  };
  const inner = [...document.querySelectorAll("*")].filter(scrollsOnBlockAxis);
  const scrollers = [document.documentElement, ...inner];
  for (let pass = 0; pass < 6; pass++) {
    for (const element of scrollers) element.scrollTop = element.scrollHeight;
    await frames(3);
  }
  const placedBetween = (leaf, stop) => {
    for (let element = leaf; element !== null && element !== stop; element = element.parentElement) {
      const { position } = getComputedStyle(element);
      if (position === "fixed" || position === "sticky") return true;
    }
    return false;
  };
  const answers = [];
  for (const scroller of scrollers) {
    const isDocument = scroller === document.documentElement;
    const box = isDocument
      ? { top: 0, bottom: innerHeight, left: 0, right: innerWidth }
      : scroller.getBoundingClientRect();
    const root = isDocument ? document.getElementById("main-content") ?? document.body : scroller;
    const stop = isDocument ? document.body : scroller;
    const leaves = [...root.querySelectorAll("*")].filter((leaf) => {
      if (floatEls.some((float) => float.contains(leaf))) return false;
      if (inner.some((other) => other !== scroller && scroller.contains(other) && other.contains(leaf))) {
        return false;
      }
      if (placedBetween(leaf, stop)) return false;
      const rect = leaf.getBoundingClientRect();
      if (rect.height === 0 || rect.width === 0) return false;
      if ([...leaf.children].some((child) => child.getBoundingClientRect().height > 0)) return false;
      const control = /^(INPUT|BUTTON|TEXTAREA|SELECT|IMG|SVG)$/i.test(leaf.tagName);
      if (!control && !(leaf.textContent ?? "").trim()) return false;
      return rect.bottom > Math.max(box.top, 0) && rect.top < Math.min(box.bottom, innerHeight);
    });
    const mark = isDocument
      ? ["main-content", "body"]
          .map((id) => (id === "body" ? document.body : document.getElementById(id)))
          .filter((element) => element?.hasAttribute("data-foot-clearance"))
          .map((element) => `${element.tagName}:${element.style.paddingBottom}`)
          .join(",")
      : scroller.hasAttribute("data-foot-clearance")
        ? `${scroller.getAttribute("data-foot-clearance")}:${
            scroller.style.getPropertyValue("--foot-clearance") || scroller.style.paddingBottom
          }`
        : "";
    const name = isDocument
      ? "the document"
      : `${scroller.tagName}${scroller.id ? `#${scroller.id}` : ""}${
          scroller.getAttribute("aria-label") ? `[${scroller.getAttribute("aria-label")}]` : ""
        }${scroller.dataset.testid ? `(${scroller.dataset.testid})` : ""}`;
    const answer = {
      name,
      gap: Math.round(scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight),
      box: [Math.round(box.top), Math.round(box.bottom)],
      mark,
      last: null,
      meets: [],
    };
    if (leaves.length > 0) {
      const last = leaves.reduce((a, b) =>
        b.getBoundingClientRect().bottom > a.getBoundingClientRect().bottom ? b : a
      );
      const rect = last.getBoundingClientRect();
      answer.last = {
        text: (last.textContent || last.getAttribute("aria-label") || last.tagName).trim().slice(0, 40),
        y: [Math.round(rect.top), Math.round(rect.bottom)],
      };
      answer.meets = floats.filter((float) => meets(rect, float.rect)).map((float) => float.name);
    }
    answers.push(answer);
  }
  const navigation = document.querySelector('aside[aria-label="Navigation"]');
  const navRect = navigation?.getBoundingClientRect();
  const navigationTop = navRect && navRect.height > 0 ? Math.round(navRect.top) : null;
  const problems = [
    ...answers
      .filter((answer) => answer.meets.length > 0)
      .map((answer) => `${answer.name}'s last content "${answer.last.text}" at y ${answer.last.y.join("-")} meets ${answer.meets.join(" and ")}`),
    ...answers
      .filter((answer) => answer.gap > 1)
      .map((answer) => `${answer.name} did not reach its end (${answer.gap}px left)`),
    ...(navigationTop !== null && navigationTop < 0
      ? [`the Navigation's top is at y ${navigationTop} at the end of the scroll`]
      : []),
  ];
  return {
    floats: floats.map((float) => `${float.name} y ${Math.round(float.rect.top)}-${Math.round(float.rect.bottom)} x ${Math.round(float.rect.left)}-${Math.round(float.rect.right)}`),
    navigationTop,
    scrollers: answers,
    problems,
  };
}

/**
 * Reads the floats against the foot elements `selectors` names ({name: css selector}), as the
 * page stands, with no scroll: a float meeting a foot element, the two floats meeting each other,
 * a float off the screen, and every control of a float or a foot element whose centre another
 * element covers (`elementFromPoint`). A trial click is no proof of this: it passed in round 8
 * over a control a float covered. Pass e.g. {bar: '[role=toolbar]', broadcast:
 * 'section[aria-label="Broadcast"]', composer: 'form[aria-label="Comment composer"]', sheet:
 * 'aside[aria-label="Review panel"]'}.
 */
export function readFootElements(selectors) {
  const box = (element) => {
    const rect = element.getBoundingClientRect();
    return { top: rect.top, bottom: rect.bottom, left: rect.left, right: rect.right };
  };
  const meets = (a, b) =>
    Math.min(a.right, b.right) - Math.max(a.left, b.left) > 1 &&
    Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) > 1;
  const shown = (element) => element !== null && element.getBoundingClientRect().height > 0;
  const floats = Object.entries({
    "Pending changes": '[aria-label="Pending changes"]',
    "the update banner": '[aria-label="A new version is available"]',
  })
    .map(([name, selector]) => [name, document.querySelector(selector)])
    .filter(([, element]) => shown(element));
  const feet = Object.entries(selectors ?? {})
    .map(([name, selector]) => [name, document.querySelector(selector)])
    .filter(([, element]) => shown(element));
  const problems = [];
  for (const [name, element] of floats) {
    const rect = box(element);
    if (rect.top < 0 || rect.bottom > innerHeight) problems.push(`${name} is off the screen`);
    for (const [foot, footElement] of feet) {
      if (meets(rect, box(footElement))) problems.push(`${name} meets ${foot}`);
    }
  }
  if (floats.length === 2 && meets(box(floats[0][1]), box(floats[1][1]))) {
    problems.push("Pending changes meets the update banner");
  }
  for (const [name, root] of [...floats, ...feet]) {
    for (const control of root.querySelectorAll("button, select, textarea, input:not([type=hidden]), a[href]")) {
      const rect = control.getBoundingClientRect();
      if (rect.width === 0 || rect.height === 0) continue;
      const x = rect.left + rect.width / 2;
      const y = rect.top + rect.height / 2;
      if (y < 0 || y >= innerHeight || x < 0 || x >= innerWidth) continue;
      const hit = document.elementFromPoint(x, y);
      if (hit && !(control === hit || control.contains(hit) || hit.contains(control))) {
        const label = (control.getAttribute("aria-label") || control.textContent || control.tagName).trim().slice(0, 30);
        const by = hit.closest("[aria-label]")?.getAttribute("aria-label") || hit.tagName;
        problems.push(`${name}'s control "${label}" is covered by ${by}`);
      }
    }
  }
  const named = (entries) =>
    Object.fromEntries(entries.map(([name, element]) => {
      const rect = box(element);
      return [name, [Math.round(rect.top), Math.round(rect.bottom)]];
    }));
  return { floats: named(floats), feet: named(feet), problems };
}

/**
 * Samples the foot every 100 ms for `ms` milliseconds: the three layer properties on the document
 * element, the document's height, the scroll, and every mark with the room it holds. Answers the
 * distinct states seen; a page at rest answers one.
 */
export async function sampleFootRest(ms) {
  const seen = new Set();
  const start = performance.now();
  while (performance.now() - start < ms) {
    const css = getComputedStyle(document.documentElement);
    const marks = [...document.querySelectorAll("[data-foot-clearance]")]
      .map((element) => `${element.tagName}:${element.getAttribute("data-foot-clearance")}:${
        element.style.getPropertyValue("--foot-clearance") || element.style.paddingBottom
      }`)
      .join(",");
    seen.add(
      [
        css.getPropertyValue("--foot-page-inset"),
        css.getPropertyValue("--foot-banner-inset"),
        css.getPropertyValue("--foot-status-inset"),
        document.documentElement.scrollHeight,
        Math.round(scrollY),
        marks,
      ].join("|")
    );
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  return [...seen];
}
