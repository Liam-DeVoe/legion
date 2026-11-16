import { expect, test } from "bun:test";
import { act, render, screen } from "@testing-library/react";
import { type ReactNode, Suspense } from "react";

import { settle } from "../../__tests__/fake-clock";
import { importWhenOnline, lazyChunk } from "./chunk-loads";
import { ErrorBoundary } from "./ErrorBoundary";

function withOnLine<T>(onLine: boolean, run: () => Promise<T>): Promise<T> {
  const descriptor = Object.getOwnPropertyDescriptor(Navigator.prototype, "onLine");
  Object.defineProperty(navigator, "onLine", { configurable: true, value: onLine });
  return run().finally(() => {
    Reflect.deleteProperty(navigator, "onLine");
    if (descriptor !== undefined) {
      Object.defineProperty(Navigator.prototype, "onLine", descriptor);
    }
  });
}

function Greeting({ name }: { name: string }): ReactNode {
  return <p>Hello {name}</p>;
}

test("a chunk that fails offline loads once the browser is back online", async () => {
  let attempts = 0;
  const load = () => {
    attempts += 1;
    return attempts === 1
      ? Promise.reject(new TypeError("Failed to fetch"))
      : Promise.resolve("ok");
  };

  await withOnLine(false, async () => {
    const loading = importWhenOnline(load);
    await Promise.resolve();
    expect(attempts).toBe(1);
    window.dispatchEvent(new Event("online"));
    expect(await loading).toBe("ok");
    expect(attempts).toBe(2);
  });
});

test("a chunk that fails while online is a real failure, not a network blip", async () => {
  await withOnLine(true, async () => {
    await expect(
      importWhenOnline(() => Promise.reject(new TypeError("Failed to fetch")))
    ).rejects.toThrow("Failed to fetch");
  });
});

// React's `lazy` keeps the promise its loader returned for the life of the page and never calls
// the loader again. Offline the chunk-failure recovery reloads nothing, so a download that
// failed there, kept, would leave the component broken after the network returned: its error
// boundary would hold the failure, and nothing would ask for the chunk again. The boundary's
// fallback stands instead while the browser is offline, and the component draws once the window
// fires `online` and the second download is in.
test("a lazy component whose download fails offline draws once the browser is back online", async () => {
  let loads = 0;
  const Lazy = lazyChunk(() => {
    loads += 1;
    return loads === 1
      ? Promise.reject(new TypeError("Failed to fetch dynamically imported module"))
      : Promise.resolve({ default: Greeting });
  });

  await withOnLine(false, async () => {
    const view = render(
      <ErrorBoundary region="this panel">
        <Suspense fallback={<p>Loading</p>}>
          <Lazy name="Ada" />
        </Suspense>
      </ErrorBoundary>
    );
    try {
      await act(settle);
      expect(loads).toBe(1);
      expect(screen.queryByRole("alert")).toBeNull();
      expect(screen.getByText("Loading")).toBeDefined();

      await act(async () => {
        window.dispatchEvent(new Event("online"));
        await settle();
      });
      expect(await screen.findByText("Hello Ada")).toBeDefined();
      expect(loads).toBe(2);
    } finally {
      view.unmount();
    }
  });
});
