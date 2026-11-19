import type { Artifact } from "@legion/contracts";
import { ARTIFACT_NAME_EXISTS_RULE } from "@legion/contracts";
import { actorText } from "../actor-text";

/**
 * The name an upload is refused under without --force (`dispatch artifact`, documents.ts): the
 * document the owner already holds under it, and the refusal that names that document. The
 * command reads it before its upload, and again when Dispatch refuses the upload's create alone
 * (create_only) because a document took the name after that read. A Dispatch too old to read that
 * member refuses the upload instead, and the command names that likely cause
 * (OLDER_DISPATCH_REFUSES_CREATE_ONLY).
 */

/** The document an upload under `name` would write the next version of: the owner's artifact
 *  whose slug or filename that name is, as the upload itself resolves one (`$2 in (name, slug)`,
 *  the slug first). Undefined where the owner holds no such document. */
export function artifactUnderName(
  artifacts: readonly Artifact[],
  name: string
): Artifact | undefined {
  const written = name.trim();
  return (
    artifacts.find((held) => held.slug === written) ??
    artifacts.find((held) => held.name === written)
  );
}

/** The refusal of an upload under a name its owner already holds: the document found, its kind,
 *  its newest version and who wrote that version, then the rule that names --force. */
export function artifactNameExistsProblem(held: Artifact, owner: string): string {
  const versions = held.versions ?? [];
  const newest = versions[versions.length - 1];
  if (newest === undefined) {
    return `${owner} already holds ${held.name} (slug ${held.slug}, kind ${held.kind}), which has no version yet and which ${actorText(held.created_by)} created. ${ARTIFACT_NAME_EXISTS_RULE}`;
  }
  const authors = (newest.authors ?? []).map(actorText);
  const wroteIt = authors.length === 0 ? actorText(held.created_by) : authors.join(" and ");
  return `${owner} already holds ${held.name} (slug ${held.slug}, kind ${held.kind}) at version ${newest.number}, which ${wroteIt} wrote. ${ARTIFACT_NAME_EXISTS_RULE}`;
}

/** The likely cause of Dispatch's 400 INVALID_JSON answer to an upload that asks for a create
 *  alone (create_only), whose JSON body the command builds itself: a Dispatch older than
 *  legion-envoy-v30.21.0, whose JSON upload refuses each member it does not declare, create_only
 *  among them. The upload wrote nothing. The daemon's log line of the same refusal names the same
 *  cause (OlderDispatchRefusesCreateOnly, packages/daemon/internal/dispatch/client.go). */
export const OLDER_DISPATCH_REFUSES_CREATE_ONLY =
  "likely cause: a Dispatch older than legion-envoy-v30.21.0 refuses the create_only member that dispatch artifact sends without --force, so it wrote nothing: the command needs Dispatch legion-envoy-v30.21.0 or later";
