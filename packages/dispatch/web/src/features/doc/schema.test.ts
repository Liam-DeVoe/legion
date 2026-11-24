import { expect, test } from "bun:test";

import type { BlockSchema } from "../../api/types";
import { compiledBlockSchema } from "./schema";
import { schemaCreatable } from "./schema-creatable";

/** The compiled manifest as the build that introduces one more typed block, "future", declares
 *  it: the version raised by one, and the block read from that version and created from it
 *  (`creatable`) or from the version after it. */
function withFutureBlock(creatable: boolean): BlockSchema {
  const version = compiledBlockSchema.version + 1;
  return {
    ...compiledBlockSchema,
    types: [
      ...compiledBlockSchema.types,
      {
        attributes: {},
        content: "block+",
        creatable_since: creatable ? version : version + 1,
        name: "future",
        render: "host",
        since: version,
      },
    ],
    version,
  };
}

/** R for `schema` at `version`, read off the manifest element by element as the page's rule
 *  states it: the highest `since` among the elements whose `creatable_since` is at most
 *  `version`, each number 1 where the manifest names none. */
function creatableAt(schema: BlockSchema, version: number): number {
  const elements = [
    ...(schema.nodes ?? []),
    ...(schema.marks ?? []),
    ...schema.types.flatMap((type) => [
      type,
      ...Object.values(type.attributes).flatMap((attribute) => [
        attribute,
        ...(attribute.choices ?? []).map((choice) => attribute.choice_versions?.[choice]),
      ]),
    ]),
  ];
  return Math.max(
    0,
    ...elements
      .filter((element) => (element?.creatable_since ?? 1) <= version)
      .map((element) => element?.since ?? 1)
  );
}

// The page presents the manifest it was built with: its version V, and R, the highest `since`
// among the elements it may create (`creatable_since` V or less), as Go's `pmdoc.SchemaCreatable`
// computes it. The manifest stages the task block's `parent`: read from V and created from V+1.
// So R is below V, and `parent` counts toward R only in a manifest at V+1. The numbers come from
// the manifest, never from a literal here, so raising the manifest does not break this row.
test("the compiled manifest is the page's own, and an element it only reads does not count", () => {
  const version = compiledBlockSchema.version;
  expect(compiledBlockSchema.nodes?.map(({ name }) => name)).toContain("paragraph");
  expect(compiledBlockSchema.types.map(({ name }) => name)).toEqual([
    "callout",
    "ask",
    "requirement",
    "plan_task",
    "check",
  ]);
  const parent = compiledBlockSchema.types.find(({ name }) => name === "plan_task")?.attributes
    .parent;
  expect(parent).toMatchObject({ creatable_since: version + 1, kind: "block[]", since: version });
  expect(schemaCreatable(compiledBlockSchema)).toBe(creatableAt(compiledBlockSchema, version));
  expect(schemaCreatable(compiledBlockSchema)).toBeLessThan(version);
  expect(schemaCreatable({ ...compiledBlockSchema, version: version + 1 })).toBe(version);
});

test("an element the page creates counts toward its creatable version, one it only reads does not", () => {
  const version = compiledBlockSchema.version + 1;
  expect(schemaCreatable(withFutureBlock(true))).toBe(version);
  expect(schemaCreatable(withFutureBlock(false))).toBe(creatableAt(compiledBlockSchema, version));
});

test("nodes, marks, attributes and enum choices each count toward the creatable version", () => {
  const version = compiledBlockSchema.version + 1;
  const shipped = creatableAt(compiledBlockSchema, version);
  const ask = compiledBlockSchema.types.find(({ name }) => name === "ask");
  const urgency = ask?.attributes.urgency;
  if (ask === undefined || urgency === undefined) {
    throw new Error("the compiled manifest declares the ask block's urgency");
  }
  const withAsk = (attributes: BlockSchema["types"][number]["attributes"]): BlockSchema => ({
    ...compiledBlockSchema,
    types: compiledBlockSchema.types.map((type) =>
      type.name === "ask" ? { ...ask, attributes: { ...ask.attributes, ...attributes } } : type
    ),
    version,
  });

  expect(
    schemaCreatable({
      ...compiledBlockSchema,
      nodes: [
        ...(compiledBlockSchema.nodes ?? []),
        { creatable_since: version, name: "aside", since: version },
      ],
      version,
    })
  ).toBe(version);
  expect(
    schemaCreatable({
      ...compiledBlockSchema,
      marks: [
        ...(compiledBlockSchema.marks ?? []),
        { creatable_since: version + 1, name: "glow", since: version },
      ],
      version,
    })
  ).toBe(shipped);
  expect(
    schemaCreatable(
      withAsk({ due: { creatable_since: version, kind: "timestamp", since: version } })
    )
  ).toBe(version);
  expect(
    schemaCreatable(
      withAsk({
        urgency: {
          ...urgency,
          choice_versions: { critical: { creatable_since: version, since: version } },
          choices: [...(urgency.choices ?? []), "critical"],
        },
      })
    )
  ).toBe(version);
});
