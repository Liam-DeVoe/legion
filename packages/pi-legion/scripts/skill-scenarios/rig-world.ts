/**
 * The repository and default branch of every phase worker's world, from rig-world.json, the one
 * place they are written: the pane's addressing (daemon-pane.go, through `workerPane`), the stand-in
 * daemon's state, the GitHub stand-in's seed and events (heads-world.ts) and rig.sh's git fixture and
 * gh routes all read them, so the text an agent gets and the world it reads name one repository.
 */
import { readFileSync } from "node:fs";
import path from "node:path";
import { z } from "zod";

const World = z.strictObject({
  repository: z.string().regex(/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/),
  defaultBranch: z.string().min(1),
});

/** rig-world.json, beside this file. */
export const RIG_WORLD_FILE = path.join(import.meta.dir, "rig-world.json");

/** The world's repository (`owner/name`) and its default branch. */
export const RIG_WORLD = World.parse(JSON.parse(readFileSync(RIG_WORLD_FILE, "utf8")));

/** The owner of the world's repository. */
export const RIG_OWNER = RIG_WORLD.repository.split("/")[0] ?? "";
