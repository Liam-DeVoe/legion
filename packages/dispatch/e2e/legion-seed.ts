// The Settings page's Legion daemon field, written straight into Postgres. No dashboard page draws
// a daemon's board, so the only Legion surface a browser drives is the Settings section where a
// project's daemon is named, listed and removed. These are the rows that section reads: a project
// to name a daemon for, and the `legion_settings` row a naming leaves.
//
// acme/widgets-shaped placeholders throughout (AGENTS.md: "This repository is public").
import { sql } from "./psql";

const SEED_ACTOR = `'{"kind":"user","id":"alice"}'`;

/** The project, under the row's own key `project` (`rowKey("ACME")`, e2e/test.ts), and its
 *  person, which the Settings page's `Legion project` picker lists and the daemon routes write
 *  under. No daemon is named: the spec about naming one starts from here. */
export async function seedLegionProject(project: string): Promise<void> {
  await sql(
    `INSERT INTO projects (key, name, default_assignee) VALUES ('${project}', 'Acme', 'alice') ON CONFLICT DO NOTHING`,
    `INSERT INTO people (email) VALUES ('alice') ON CONFLICT DO NOTHING`
  );
}

/** `seedLegionProject` with the project's daemon already named at `stateUrl`, so the Settings
 *  list shows it and its Remove has a row to take away. */
export async function seedLegionDaemon(project: string, stateUrl: string): Promise<void> {
  await seedLegionProject(project);
  await sql(
    `INSERT INTO legion_settings (project, state_url, created_by, updated_at)
       VALUES ('${project}', '${stateUrl}', ${SEED_ACTOR}, now())`
  );
}

/** The projects whose daemon the stack names, in the order the Settings section lists them (by
 *  key, compared as the section compares them): the store's own table, `legion_settings`, which
 *  is what each project's daemon read answers from and its delete takes a row out of. No route
 *  lists every daemon - the section reads the project list and then each project's own
 *  (`useLegionDaemons`) - and the list is the stack's whole, every row's daemons in it, so a row
 *  that asserts the list reads it here at its start. */
export async function legionDaemonsHeld(): Promise<string[]> {
  const held = await sql("SELECT project FROM legion_settings");
  return held === "" ? [] : held.split("\n").sort();
}

/** Takes `project`'s daemon out again, as its Remove does, for the `finally` of a row that named
 *  one: the next row of the worker reads the list this row started on. */
export async function removeLegionDaemon(project: string): Promise<void> {
  await sql(`DELETE FROM legion_settings WHERE project = '${project}'`);
}
