/**
 * The case file of the behaviour test (plan, R5 `cases.ts`): one JSON object per line, each one
 * step of a real Oh My Pi session that a line of one skill's text governs. Its writer is
 * `skill-cuts read --prompt case` with the case builder; arms.ts, capture.ts, judge.ts and `report`
 * read it through `loadCases`. A case file holds the cases of one skill: the runner is run once
 * per skill, with that skill's old and new directories.
 *
 * A read of a skill file is a `read` tool call of `skill://<skill>` (its SKILL.md) or
 * `skill://<skill>/<path>`, as `skillReadFile` reads one. Oh My Pi answers a read of a skill file
 * by its path on disk in another form (a `[<path>#<hash>]` line, then numbered lines), which the
 * arms do not rebuild: a skillReads index that answers one is refused, and a session that read
 * the skill that way gives no case, since that read's base text would stay in every arm.
 */
import { createHash } from "node:crypto";
import { readFileSync, statSync } from "node:fs";
import path from "node:path";
import { messageFor } from "@legion/envoy-client/errors";
import { z } from "zod";
import { readCalls, SessionMessage } from "../skill-scenarios/transcript";

/** A file of a repository at a commit: the repository (`owner/name`), the commit, and the path
 * inside it. */
const RepoPath = {
  repo: z.string().regex(/^[^/\s]+\/[^/\s]+$/),
  commit: z.string().min(1),
  path: z.string().min(1),
};
/** An installed skill directory of a case's profile, and the name it is installed under. */
export const ProfileSkill = z.strictObject({ ...RepoPath, name: z.string().min(1) });
export type ProfileSkill = z.infer<typeof ProfileSkill>;

/** What the capture installs for a case, the same in every arm but the case's skill (plan, "The
 * case context"): the installed skills, the plugins the session's context installed at its
 * start, the context files (the repository's `AGENTS.md`, a Legion project's instructions file),
 * and, for a Legion context, the role prompt that goes to `--append-system-prompt`. */
export const Profile = z.strictObject({
  skills: z.array(ProfileSkill),
  plugins: z.array(z.strictObject({ ...RepoPath, name: z.string().min(1) })),
  contextFiles: z.array(z.strictObject(RepoPath)),
  appendSystemPrompt: z.string().min(1).optional(),
});
export type Profile = z.infer<typeof Profile>;

/** The thinking levels `omp --thinking` takes. */
export const Thinking = z.enum(["off", "minimal", "low", "medium", "high", "xhigh", "max", "auto"]);

/** A path inside a skill directory: relative, with no `..` part. */
const InsidePath = z
  .string()
  .min(1)
  .refine(
    (file) => !path.isAbsolute(file) && !file.split(/[\\/]/).includes(".."),
    "is not a path inside the skill directory"
  );

/** One case. `line` is the line of the cut's base text that the step exercises: its text,
 * `lineNumber` (from 1) and `lineCount` (the lines of `baseFile`; a final newline ends the last
 * line, it does not start one). `criterion` says what the line requires at this step, when a reply
 * passes and when it fails; it is written before any run. `model` and `thinking` are the
 * transcript's last `model_change` and `thinking_level_change` before the step. `messages` are the
 * session's Oh My Pi messages up to the step, `skillReads` the indices in `messages` of the read
 * results of this skill's files, and `realStep` the real agent's next assistant message. `source`
 * names the store and the SHA-256 of the transcript's path (`pathHash`), never the path.
 * `secretsRemoved` counts what the secret scan took out of the case before it left the pod, and
 * `sessionDigest` is the session's last `system-prompt-digest` before the step, or null. */
export const Case = z.strictObject({
  id: z.string().min(1),
  cut: z.string().min(1),
  skill: z.string().min(1),
  baseFile: InsidePath,
  line: z.strictObject({
    text: z
      .string()
      .refine((text) => text.trim() !== "" && !text.includes("\n"), "is not one non-blank line"),
    lineNumber: z.int().positive(),
    lineCount: z.int().positive(),
  }),
  criterion: z.strictObject({ pass: z.string().min(1), fail: z.string().min(1) }),
  model: z.string().min(1),
  thinking: Thinking,
  context: z.string().regex(/^(legion:[^:\s]+:[^:\s]+|devbox)$/),
  profile: Profile,
  messages: z.array(SessionMessage),
  skillReads: z.array(z.int().nonnegative()),
  realStep: SessionMessage.refine(
    (message) => message.role === "assistant",
    "is not an assistant message"
  ),
  source: z.strictObject({
    store: z.string().min(1),
    pathHash: z.string().regex(/^[0-9a-f]{64}$/),
  }),
  sessionStart: z.iso.datetime({ offset: true }),
  secretsRemoved: z.int().nonnegative(),
  sessionDigest: z.string().min(1).nullable(),
});
export type Case = z.infer<typeof Case>;

/** The `source.pathHash` of a transcript path. */
export function pathHash(transcriptPath: string): string {
  return createHash("sha256").update(transcriptPath).digest("hex");
}

/** The file of skill `skill` that a `read` of `target` reads, as a path inside the skill
 * directory (`SKILL.md` for `skill://<skill>` itself), or undefined when `target` is no
 * `skill://` URL of that skill, carries a selector (`:`), or leaves the skill directory. */
export function skillReadFile(skill: string, target: string): string | undefined {
  const url = `skill://${skill}`;
  if (target !== url && !target.startsWith(`${url}/`)) return undefined;
  let file: string;
  try {
    file = decodeURIComponent(target.slice(url.length + 1));
  } catch {
    return undefined;
  }
  if (file === "") return "SKILL.md";
  return file.includes(":") || !InsidePath.safeParse(file).success ? undefined : file;
}

/** The lines of a file's text: a final newline ends the last line, it does not start one. */
export function textLines(text: string): string[] {
  return text.replace(/\n$/, "").split("\n");
}

/** Why `c` cannot run against the base text in `baseDir`, or undefined when it can. */
function refusal(c: Case, baseDir: string): string | undefined {
  const file = path.join(baseDir, c.baseFile);
  let text: string;
  try {
    text = readFileSync(file, "utf8");
  } catch {
    return `its baseFile ${c.baseFile} is not a file of ${baseDir}`;
  }
  const lines = textLines(text);
  const wanted = c.line.text.trim();
  if (!lines.some((line) => line.trim() === wanted)) {
    return `its line is not a whole line of ${file}: ${JSON.stringify(c.line.text)}`;
  }
  if (lines.length !== c.line.lineCount) {
    return `its line.lineCount is ${c.line.lineCount}, and ${file} has ${lines.length} lines`;
  }
  if (lines[c.line.lineNumber - 1]?.trim() !== wanted) {
    return `line ${c.line.lineNumber} of ${file} is not its line`;
  }
  if (!c.profile.skills.some((skill) => skill.name === c.skill)) {
    return `its profile does not install the skill ${c.skill}`;
  }
  const targets = new Map(
    readCalls(c.messages.map((message) => ({ message }))).map((call) => [call.id, call.target])
  );
  for (const index of c.skillReads) {
    const message = c.messages[index];
    if (message?.role !== "toolResult" || message.toolName !== "read") {
      return `skillReads index ${index} is not a read result`;
    }
    const target = targets.get(message.toolCallId ?? "");
    if (target === undefined || skillReadFile(c.skill, target) === undefined) {
      return `skillReads index ${index} answers ${target === undefined ? "no read call before it" : `a read of ${target}`}, not a read of a skill://${c.skill} file`;
    }
  }
  // A read of the skill's files left out of skillReads would keep the base text in every arm.
  for (const [index, message] of c.messages.entries()) {
    if (message.role !== "toolResult" || c.skillReads.includes(index)) continue;
    const target = targets.get(message.toolCallId ?? "");
    if (target !== undefined && skillReadFile(c.skill, target) !== undefined) {
      return `the read result at index ${index} (${target}) is not in skillReads`;
    }
  }
  return undefined;
}

/** The cases of `file`, each checked against the base text of its skill, the directory `baseDir`
 * (the skill's own directory, SKILL.md at its top). Throws one error that names each case it
 * refuses and why: a line that is no case, a missing realStep, a line.text that is not a whole
 * line (trimmed) of `baseDir/<baseFile>` or is not at its lineNumber, a skillReads index that is
 * not a read result of a `skill://<skill>` file, a read result of one that skillReads leaves out,
 * a skill its profile does not install, an id given twice, or cases of more than one skill. */
export function loadCases(file: string, baseDir: string): Case[] {
  if (!statSync(baseDir, { throwIfNoEntry: false })?.isDirectory()) {
    throw new Error(`the base directory ${baseDir} is not a directory`);
  }
  const cases: Case[] = [];
  const refusals: string[] = [];
  const lines = readFileSync(file, "utf8").split("\n");
  for (const [index, text] of lines.entries()) {
    if (text.trim() === "") continue;
    const where = `${file}:${index + 1}`;
    let raw: unknown;
    try {
      raw = JSON.parse(text);
    } catch (error) {
      refusals.push(`${where} is not JSON: ${messageFor(error)}`);
      continue;
    }
    const id =
      typeof raw === "object" && raw !== null && "id" in raw && typeof raw.id === "string"
        ? raw.id
        : undefined;
    const name = id === undefined ? `the case at ${where}` : `case ${id} (${where})`;
    if (typeof raw === "object" && raw !== null && !("realStep" in raw)) {
      refusals.push(`${name} has no realStep (the real agent's next assistant message)`);
      continue;
    }
    const parsed = Case.safeParse(raw);
    if (!parsed.success) {
      const issues = parsed.error.issues.map(
        (issue) => `${issue.path.join(".") || "the case"}: ${issue.message}`
      );
      refusals.push(`${name} is not a case: ${issues.join("; ")}`);
      continue;
    }
    const why = refusal(parsed.data, baseDir);
    if (why !== undefined) {
      refusals.push(`${name}: ${why}`);
      continue;
    }
    if (cases.some((c) => c.id === parsed.data.id)) {
      refusals.push(`${name}: its id is given twice`);
      continue;
    }
    cases.push(parsed.data);
  }
  const other = cases.find((c) => c.skill !== cases[0]?.skill);
  if (other !== undefined) {
    refusals.push(
      `${file} holds cases of more than one skill: ${cases[0]?.skill} and ${other.skill} (one skill per case file)`
    );
  }
  if (refusals.length > 0) throw new Error(`refused cases:\n${refusals.join("\n")}`);
  return cases;
}
