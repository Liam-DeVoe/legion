import { Component, type ReactNode } from "react";

interface ChunkBoundaryProps {
  readonly children: ReactNode;
  /** What draws in the place of `children` once a render under them has thrown: nothing, unless
   *  a host names something. */
  readonly fallback?: ReactNode;
  /** For a host whose part the reader opened (`askedSurface`): called once a render under
   *  `children` has thrown, to close that part, and the boundary then draws `children` again,
   *  closed, so the reader's next open draws it afresh rather than meeting a boundary that stays
   *  failed for the life of the page. */
  readonly onError?: () => void;
  /** For a host whose part draws with no reader action and stays mounted (the two lists of
   *  writes, the reference-preview layer): a value that changes when the reader does something
   *  that wants the part again - a new write, a move to another address. A boundary that has
   *  caught a throw draws `children` again once it changes, so the part mounts afresh and asks
   *  for its chunk again (`chunkComponent` keeps no failed download), rather than drawing nothing
   *  for the life of the page: the reader can stay on a page that has spent its reloads, or at
   *  the leave prompt a write out raises, for as long as they like. */
  readonly reset?: unknown;
}

/**
 * The error boundary of a host the shell draws outside the page's own boundary (`app.tsx`'s
 * `ErrorBoundary region="this page"`), around a part whose code is a chunk of its own: the two
 * lists of writes (`features/writes/write-hosts.tsx`), drawn in the status corner of every
 * signed-in page and on the sign-in page; the reference-preview layer
 * (`features/refs/RefPreviewLayer.tsx`); and the surfaces a reader opens with a key - the Create
 * issue dialog, the shortcut sheet, the search bar and the Inbox peek (`askedSurface`), whose
 * bodies draw decision cards from a chunk of their own too (`features/doc/TextAskCardHost.tsx`).
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
 * `fallback`, and a dialog or a drawer closes (`onError`).
 */
export class ChunkBoundary extends Component<ChunkBoundaryProps, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError(): { failed: boolean } {
    return { failed: true };
  }

  componentDidCatch(): void {
    const { onError } = this.props;
    if (onError === undefined) return;
    // The host's close and this reset are both updates of the commit that caught the throw, so
    // React draws them in one render: the children come back with their part closed.
    onError();
    this.setState({ failed: false });
  }

  componentDidUpdate({ reset }: ChunkBoundaryProps): void {
    // A boundary that has not failed draws `children` already: drawing them again is a render
    // that changes nothing.
    if (reset !== this.props.reset) this.setState({ failed: false });
  }

  render(): ReactNode {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}
