import { Component, type ReactNode } from "react";

interface ChunkBoundaryProps {
  readonly children: ReactNode;
  /** What draws in the place of `children` once a render under them has thrown: nothing, unless
   *  a host names something. */
  readonly fallback?: ReactNode;
}

/**
 * The error boundary of a host the shell draws outside the page's own boundary (`app.tsx`'s
 * `ErrorBoundary region="this page"`), around a part whose code is a chunk of its own: the two
 * lists of writes (`features/writes/write-hosts.tsx`), drawn in the status corner of every
 * signed-in page and on the sign-in page.
 *
 * WHY EACH SUCH HOST NEEDS ONE. A chunk that fails to download while the browser is online is
 * thrown into the render that waits on it (`chunkComponent`, `chunk-component.tsx`), and the
 * nearest boundary catches it. Outside the page's boundary that is the app's top one
 * (`main.tsx`), which replaces the whole app: `AuthGate` unmounts, and with it this tab's
 * sign-in channel listener and its sign-in check, so the tab stops answering another tab's
 * sign-out and stops showing the sign-in page on a 401 while its writes go on. The recovery's
 * reload, which a failure like this asks for (`DeploymentResilience.tsx`), does not always follow
 * to bring the page back: the reader can stay at the leave prompt a write out raises, a page asks
 * for one reload only, and the tab's budget of reloads can be spent. So the failure stops here,
 * and the rest of the page stays drawn and signed in. This boundary only stops the throw: the
 * reload is still asked for, as it is for any chunk a reader waits on.
 *
 * Why not `ErrorBoundary`: that one tells the reader which region failed, with a reload button,
 * in the region's own place. A list of writes in the status corner draws nothing while it has
 * nothing to show, and one that cannot load draws nothing too; React logs the failure itself
 * (`onCaughtError`). A host whose part has something else to stand for it names it as
 * `fallback`.
 */
export class ChunkBoundary extends Component<ChunkBoundaryProps, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError(): { failed: boolean } {
    return { failed: true };
  }

  render(): ReactNode {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}
