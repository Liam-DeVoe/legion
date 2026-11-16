import type {
  ConstraintRead,
  IssueConstraints,
  LensFinding,
  LensInvoked,
  PointerInput,
  PointerWritten,
  ReasoningAuthor,
  RiskWritten,
} from "@legion/contracts";
import {
  type CommandContext,
  type DispatchToolResult,
  defined,
  optionalString,
  optionalStringArray,
  stringArg,
} from "./context";
import { refusalWithCode } from "./failures";

/**
 * The commands that write and read the nodes a piece of work is reasoned about with, on
 * Dispatch's node and tuple store: a lens invoked on a node of an issue, a finding added to one, a
 * risk under a pre-mortem, a constraint under the issue, one pointer tuple out of a node, and the
 * issue's constraints. Each write names the node it wrote by its dispatch:// address, which is how
 * every later call names it, and says when that node is the caller's alone. Dispatch refuses
 * these writes by code (LENS_KIND_REQUIRED, INVALID_POINTER, POINTER_ALREADY_ASSERTED,
 * AUTHOR_NAMES_NOBODY, NOT_FOUND), and each description names them, so the code reaches the caller.
 */

/** `visibility` as the schema parsed it: project, author, or nothing at all. */
function optionalVisibility(args: Record<string, unknown>): "project" | "author" | undefined {
  const value = optionalString(args, "visibility");
  return value === "project" || value === "author" ? value : undefined;
}

/** What a line about one written node adds when the node is the caller's alone. */
function reservedNote(visibility: string): string {
  return visibility === "author" ? ", for you alone" : "";
}

/** Who wrote a node, as a line about it names them: nobody under a credential that proves none. */
function authorText(author: ReasoningAuthor | null): string {
  return author === null
    ? "nobody (the credential proves no author)"
    : `${author.kind} ${author.id}`;
}

/** `dispatch lens`: one lens of one kind invoked on a node of the issue, with its findings. */
export async function lensCommand(context: CommandContext): Promise<DispatchToolResult> {
  const { args, client, actor, issue } = context;
  const issueKey = issue();
  let lens: LensInvoked;
  try {
    lens = await client.invokeLens(issueKey, {
      actor,
      on: stringArg(args, "on"),
      kind: stringArg(args, "kind"),
      ...defined("verdict", optionalString(args, "verdict")),
      ...defined("findings", optionalStringArray(args, "findings")),
      ...defined("triggered_by", optionalString(args, "triggered_by")),
      ...defined("visibility", optionalVisibility(args)),
    });
  } catch (error) {
    throw refusalWithCode(error);
  }
  // The document renders a Go list as nullable, and Dispatch answers an empty one.
  const findings = lens.findings ?? [];
  return {
    text: [
      `Invoked a ${lens.kind} lens on ${lens.on}: ${lens.address}${reservedNote(lens.visibility)}, written by ${authorText(lens.author)}`,
      ...(lens.triggered_by === null ? [] : [`Triggered by ${lens.triggered_by}`]),
      ...(lens.verdict === null ? [] : [`Verdict: ${lens.verdict}`]),
      ...findings.map(
        (finding) =>
          `- finding ${finding.address}${reservedNote(finding.visibility)}: ${finding.text}`
      ),
    ].join("\n"),
    details: {
      issue: issueKey,
      lens: lens.address,
      kind: lens.kind,
      on: lens.on,
      visibility: lens.visibility,
      findings: findings.map((finding) => finding.address),
    },
  };
}

/** `dispatch finding`: one finding added to a lens, after every finding it holds. */
export async function findingCommand(context: CommandContext): Promise<DispatchToolResult> {
  const { args, client, actor, issue } = context;
  const issueKey = issue();
  let finding: LensFinding;
  try {
    finding = await client.addLensFinding(issueKey, stringArg(args, "lens"), {
      actor,
      text: stringArg(args, "text"),
      ...defined("visibility", optionalVisibility(args)),
    });
  } catch (error) {
    throw refusalWithCode(error);
  }
  return {
    text: `Added a finding to ${finding.lens}: ${finding.address}${reservedNote(finding.visibility)}`,
    details: {
      issue: issueKey,
      lens: finding.lens,
      finding: finding.address,
      visibility: finding.visibility,
    },
  };
}

/** `dispatch risk`: one risk under a pre-mortem lens of the issue. */
export async function riskCommand(context: CommandContext): Promise<DispatchToolResult> {
  const { args, client, actor, issue } = context;
  const issueKey = issue();
  let risk: RiskWritten;
  try {
    risk = await client.writeRisk(issueKey, {
      actor,
      lens: stringArg(args, "lens"),
      heading: stringArg(args, "heading"),
      early_warning: stringArg(args, "early_warning"),
      prevention: stringArg(args, "prevention"),
      ...defined("prevented_by", optionalString(args, "prevented_by")),
      ...defined("visibility", optionalVisibility(args)),
    });
  } catch (error) {
    throw refusalWithCode(error);
  }
  const preventedBy = risk.prevented_by ?? [];
  return {
    text: [
      `Wrote a risk under ${risk.lens}: ${risk.address}${reservedNote(risk.visibility)}`,
      preventedBy.length === 0
        ? "No check prevents it yet."
        : `Prevented by ${preventedBy.join(", ")}`,
    ].join("\n"),
    details: {
      issue: issueKey,
      lens: risk.lens,
      risk: risk.address,
      prevented_by: preventedBy,
      visibility: risk.visibility,
    },
  };
}

/** A constraint's fields as one line names them: its standard and reference, or its measurement. */
function constraintFields(constraint: ConstraintRead): string {
  if (constraint.standard !== null) {
    return `standard ${JSON.stringify(constraint.standard)}, reference ${JSON.stringify(constraint.reference ?? "")}`;
  }
  return `measurement ${JSON.stringify(constraint.measurement ?? "")}, value ${JSON.stringify(constraint.value ?? "")}, taken ${JSON.stringify(constraint.taken_where ?? "")}`;
}

/** `dispatch constraint`: one constraint under the issue. */
export async function constraintCommand(context: CommandContext): Promise<DispatchToolResult> {
  const { args, client, actor, issue } = context;
  const issueKey = issue();
  let constraint: ConstraintRead;
  try {
    constraint = await client.writeConstraint(issueKey, {
      actor,
      ...defined("standard", optionalString(args, "standard")),
      ...defined("reference", optionalString(args, "reference")),
      ...defined("measurement", optionalString(args, "measurement")),
      ...defined("value", optionalString(args, "value")),
      ...defined("taken_where", optionalString(args, "taken_where")),
      ...defined("visibility", optionalVisibility(args)),
    });
  } catch (error) {
    throw refusalWithCode(error);
  }
  return {
    text: `Wrote a constraint under ${issueKey}: ${constraint.address}${reservedNote(constraint.visibility)} (${constraintFields(constraint)})`,
    details: { issue: issueKey, constraint: constraint.address, visibility: constraint.visibility },
  };
}

/** `dispatch constraints`: the constraints under the issue the caller may see. */
export async function constraintsCommand(context: CommandContext): Promise<DispatchToolResult> {
  const { client, issue } = context;
  const issueKey = issue();
  let listed: IssueConstraints;
  try {
    listed = await client.issueConstraints(issueKey);
  } catch (error) {
    throw refusalWithCode(error);
  }
  const constraints = listed.constraints ?? [];
  const lines =
    constraints.length === 0
      ? [`${listed.issue} holds no constraint you may see.`]
      : [
          `${listed.issue} holds ${constraints.length} constraint${constraints.length === 1 ? "" : "s"} you may see:`,
          ...constraints.map(
            (constraint) =>
              `- ${constraint.address}${reservedNote(constraint.visibility)}: ${constraintFields(constraint)}; written by ${authorText(constraint.author)}; ${constraint.state}`
          ),
        ];
  return {
    text: lines.join("\n"),
    details: {
      issue: listed.issue,
      constraints: constraints.map((constraint) => constraint.address),
    },
  };
}

/** `dispatch pointer`: one pointer tuple out of a node of the issue, appended, retracted or
 *  retargeted. */
export async function pointerCommand(context: CommandContext): Promise<DispatchToolResult> {
  const { args, client, actor, issue } = context;
  const issueKey = issue();
  let written: PointerWritten;
  try {
    written = await client.writePointer(issueKey, stringArg(args, "node"), {
      actor,
      kind: stringArg(args, "kind") as PointerInput["kind"],
      action: stringArg(args, "action") as PointerInput["action"],
      to: stringArg(args, "to"),
      ...defined("from", optionalString(args, "from")),
    });
  } catch (error) {
    throw refusalWithCode(error);
  }
  const done =
    written.action === "retarget"
      ? `Retargeted the ${written.kind} pointer of ${written.node.address} from ${written.from?.address ?? ""} to ${written.to.address}`
      : written.action === "retract"
        ? `Retracted the ${written.kind} pointer of ${written.node.address} at ${written.to.address}`
        : `Appended a ${written.kind} pointer from ${written.node.address} to ${written.to.address}`;
  return {
    text: done,
    details: {
      issue: issueKey,
      node: written.node.address,
      kind: written.kind,
      action: written.action,
      to: written.to.address,
      ...(written.from === null ? {} : { from: written.from.address }),
    },
  };
}
