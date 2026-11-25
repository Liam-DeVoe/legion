import type { ReactNode } from "react";
import { Link } from "react-router-dom";

import { buttonClassName } from "../../components/Button";
import { PageHeader } from "../../components/Layout";
import { useDocumentTitle } from "./useDocumentTitle";

/** Shared not-found view: an unrecognized top-level route, an unrecognized issue sub-route, and
 *  an issue item whose id names nothing, which passes its own `title` and `detail`. */
export function NotFoundPage({
  backTo = "/",
  backLabel = "Back to inbox",
  detail = "That link doesn't match a page in Dispatch.",
  title = "Page not found",
}: {
  backLabel?: string;
  backTo?: string;
  detail?: string;
  title?: string;
} = {}): ReactNode {
  useDocumentTitle("Not found · Dispatch");
  return (
    <section>
      <PageHeader description={detail} title={title} />
      <Link className={buttonClassName({ variant: "primary" })} to={backTo}>
        {backLabel}
      </Link>
    </section>
  );
}
