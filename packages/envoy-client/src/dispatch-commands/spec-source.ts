import type { SpecLevelImport, SpecSource } from "@legion/contracts";
import { type CommandContext, type DispatchToolResult, optionalString, stringArg } from "./context";
import { refusalWithCode } from "./failures";

/** One spec source as `dispatch spec-source` prints it. */
function sourceLine(source: SpecSource): string {
  return `${source.repo}@${source.branch}${source.enabled ? "" : " (off)"}`;
}

/** `dispatch spec-source`: name, list or remove the repositories whose approved spec a project
 *  holds. */
export async function specSourceCommand(context: CommandContext): Promise<DispatchToolResult> {
  const { args, client, actor } = context;
  const project = stringArg(args, "project");
  const action = stringArg(args, "action");
  try {
    if (action === "list") {
      const sources = await client.listSpecSources(project);
      return {
        text:
          sources.length === 0
            ? `${project} names no spec source.`
            : [`Spec sources of ${project}:`, ...sources.map((source) => `- ${sourceLine(source)}`)].join("\n"),
        details: { project, sources: sources.map(({ repo, branch, enabled }) => ({ repo, branch, enabled })) },
      };
    }
    const repo = stringArg(args, "repo");
    if (action === "remove") {
      await client.deleteSpecSource(project, repo, actor);
      return {
        text: `Removed ${repo} from the spec sources of ${project}. Its approved spec stays, and its issues hold no change request from now on.`,
        details: { project, repo, removed: true },
      };
    }
    const branch = optionalString(args, "branch") ?? "";
    const source = await client.putSpecSource(project, repo, branch, actor);
    return {
      text: `${project} reads The Spec of ${sourceLine(source)}: an approval of the spec of an issue of ${source.repo} writes its change request into that repository's approved spec.`,
      details: { project, repo: source.repo, branch: source.branch, enabled: source.enabled },
    };
  } catch (error) {
    throw refusalWithCode(error);
  }
}

/** `dispatch spec-import-level`: import an issue's approved level into the approved spec of its
 *  repository. */
export async function specImportLevelCommand(context: CommandContext): Promise<DispatchToolResult> {
  const { args, client, actor } = context;
  const issue = stringArg(args, "issue");
  let imported: SpecLevelImport;
  try {
    imported = await client.importSpecLevel(issue, stringArg(args, "notes"), actor);
  } catch (error) {
    throw refusalWithCode(error);
  }
  // The answer's lists are arrays, but its generated type allows null as every Go slice does.
  const written = imported.written ?? [];
  const held = (imported.held ?? []).map(({ id, lacks }) => ({ id, lacks: lacks ?? [] }));
  const lines = [
    written.length === 0
      ? `The import of ${imported.issue} wrote nothing into the approved spec of ${imported.repo}.`
      : `The import of ${imported.issue} wrote ${written.length} ${written.length === 1 ? "node" : "nodes"} into the approved spec of ${imported.repo}: ${written.join(", ")}.`,
  ];
  if (held.length > 0) {
    lines.push(`It holds ${held.length} ${held.length === 1 ? "statement" : "statements"} until a change request supplies what each lacks:`);
    for (const hold of held) lines.push(`- ${hold.id} lacks ${hold.lacks.join(", ")}`);
    if (imported.repair_issue !== null) lines.push(`Their repair issue is ${imported.repair_issue}.`);
  }
  return {
    text: lines.join("\n"),
    details: { issue: imported.issue, repo: imported.repo, written, held, repair_issue: imported.repair_issue },
  };
}
