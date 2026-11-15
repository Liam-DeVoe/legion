import type { HeldEditInput, HeldRead } from "@legion/contracts";
import { type CommandContext, type DispatchToolResult, optionalString, stringArg } from "./context";

/**
 * The commands of a text Dispatch's reviewer holds (INFRA-3231's R5): `dispatch held` reads it,
 * `dispatch held-edit` changes it, which the reviewer reads again, and `dispatch held-post` posts
 * it as written with a reason. The frame that brings the reviewer's findings names the last two.
 */

/** The wording of a held text as its reader would see it. */
function wordingLines(wording: HeldRead["wording"]): string[] {
  const lines: string[] = [];
  if (wording.title !== undefined && wording.title !== "") lines.push(`Title: ${wording.title}`);
  lines.push(wording.body);
  for (const option of wording.options ?? []) {
    lines.push(
      `- ${option.label}${option.description === undefined || option.description === "" ? "" : `: ${option.description}`}`
    );
  }
  return lines;
}

/** What a held text's read prints: its state, its wording, the findings and the rewrite. */
function heldText(held: HeldRead): string {
  const lines = [`Held ${held.kind} ${held.id}: ${held.state}.`];
  if (held.deadline !== null && held.state === "awaiting_writer") {
    lines.push(`You have until ${held.deadline} to edit it or post it.`);
  }
  if (held.waits_on !== undefined && held.waits_on !== "") {
    lines.push(`It posts after held text ${held.waits_on}.`);
  }
  if (held.outcome !== undefined && held.outcome !== "") lines.push(`Outcome: ${held.outcome}.`);
  lines.push("", "The text:", ...wordingLines(held.wording));
  const findings = held.findings ?? [];
  if (findings.length > 0) {
    lines.push("", "The reviewer's findings:");
    findings.forEach((finding, index) => {
      lines.push(
        `${index + 1}. "${finding.quote}" (${finding.rule_set}): ${finding.why} Fix: ${finding.fix}`
      );
    });
  }
  if (held.rewrite !== null) {
    lines.push("", "The reviewer's rewrite:", ...wordingLines(held.rewrite));
    if (held.not_applied !== undefined && held.not_applied !== "") {
      lines.push(`Dispatch cannot post this rewrite as it is: ${held.not_applied}.`);
    }
  }
  return lines.join("\n");
}

/** `dispatch held`: read a text the reviewer holds. */
export async function heldCommand(context: CommandContext): Promise<DispatchToolResult> {
  const held = await context.client.getHeld(stringArg(context.args, "held"));
  return { text: heldText(held), details: { held } };
}

/** `dispatch held-edit`: change a held text, which the reviewer reads again. */
export async function heldEditCommand(context: CommandContext): Promise<DispatchToolResult> {
  const { args, client, actor } = context;
  const options = args.options;
  const input: HeldEditInput = {
    ...(optionalString(args, "question") === undefined
      ? {}
      : { question: optionalString(args, "question") }),
    ...(Array.isArray(options)
      ? { options: options as NonNullable<HeldEditInput["options"]> }
      : {}),
    ...(optionalString(args, "title") === undefined
      ? {}
      : { title: optionalString(args, "title") }),
    ...(optionalString(args, "spec") === undefined ? {} : { spec: optionalString(args, "spec") }),
    ...(optionalString(args, "body") === undefined ? {} : { body: optionalString(args, "body") }),
    actor,
  };
  const held = await client.editHeld(stringArg(args, "held"), input);
  return {
    text: `Edited held ${held.kind} ${held.id}; the reviewer reads it again.\n\n${heldText(held)}`,
    details: { held },
  };
}

/** `dispatch held-post`: post a held text as written, with the reason it is right as written. */
export async function heldPostCommand(context: CommandContext): Promise<DispatchToolResult> {
  const { args, client, actor } = context;
  const held = await client.postHeld(stringArg(args, "held"), {
    reason: stringArg(args, "reason"),
    actor,
  });
  return {
    text:
      held.state === "posted"
        ? `Posted ${held.kind} ${held.id} as written.`
        : `Held ${held.kind} ${held.id} is ${held.state}${held.waits_on ? `: it posts after held text ${held.waits_on}` : ""}.`,
    details: { held },
  };
}
