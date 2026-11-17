import { spyOn } from "bun:test";

import { api } from "../../api/client";
import type { ArtifactV2, Version } from "../../api/types";

/** A document as a test writes it: the fields of its own read, with every version it holds. The
 *  read carries only what its newest version is (`latest_version`, `latest_version_at`,
 *  `latest_version_size`) and how many it holds (`version_count`); the versions are their own
 *  list (`GET /api/v2/artifacts/{id}/versions`), which `serveVersions` answers from these. */
export type DocumentFixture = Omit<
  ArtifactV2,
  "latest_version" | "latest_version_at" | "latest_version_size" | "version_count"
> & { readonly versions: Version[] };

/** `document`'s own read: its newest version's number, time and stored size, and how many
 *  versions it holds, in place of its versions. */
export function documentRead({ versions, ...read }: DocumentFixture): ArtifactV2 {
  const latest = versions.reduce<Version | undefined>(
    (newest, version) =>
      newest === undefined || version.number > newest.number ? version : newest,
    undefined
  );
  return {
    ...read,
    latest_version: latest?.number ?? null,
    latest_version_at: latest?.created_at ?? null,
    latest_version_size: latest?.size ?? null,
    version_count: versions.length,
  };
}

/** Answers `api.listArtifactVersions` with each document's versions, by its id, and a document no
 *  fixture names with none. The caller restores the spy. */
export function serveVersions(documents: readonly DocumentFixture[]) {
  return spyOn(api, "listArtifactVersions").mockImplementation(async (id) => [
    ...(documents.find((document) => document.id === id)?.versions ?? []),
  ]);
}
