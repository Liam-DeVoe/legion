// The schema of a node of The Spec, and the YAML of the spec files, read the same in TypeScript as
// in Go (R5 of dispatch://INFRA-4083). ajv reads the one schema of the repository and the one table
// of its cases that the Go test TestSchemaCases reads; the yaml library reads the YAML nodes of
// specgraph's testdata/nodes, each as the data of the .decoded.json file beside it, which a person
// wrote and which the Go test TestNodesDecodeToTheirData holds yaml.v3 to.

import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { Ajv2020 } from "ajv/dist/2020";
import { parseAllDocuments } from "yaml";

/** The text of path under packages/envoy/internal/dispatch/specgraph. */
function specgraph(path: string): string {
  return readFileSync(
    fileURLToPath(new URL(`../../../envoy/internal/dispatch/specgraph/${path}`, import.meta.url)),
    "utf8"
  );
}

interface SchemaCase {
  name: string;
  node: unknown;
  valid: boolean;
  at: string[];
}

const validate = new Ajv2020({ allErrors: true, strict: true }).compile(
  JSON.parse(specgraph("schema/spec-node.schema.json"))
);
const cases = JSON.parse(specgraph("testdata/schema-cases.json")) as SchemaCase[];

describe("the schema of a node", () => {
  test("the table of cases holds rows", () => {
    expect(cases.length).toBeGreaterThan(0);
  });
  for (const row of cases) {
    test(row.name, () => {
      expect(validate(row.node), row.name).toBe(row.valid);
      if (row.valid) return;
      // ajv names each `if` whose `then` or `else` failed, beside the failure itself; the Go
      // validator names the failure alone.
      const at = new Set(
        (validate.errors ?? []).filter((e) => e.keyword !== "if").map((e) => e.instancePath)
      );
      expect([...at].sort(), row.name).toEqual([...new Set(row.at)].sort());
    });
  }
});

describe("the YAML of the spec files", () => {
  for (const name of ["all-kinds", "quoted", "separators"]) {
    test(`${name}.yaml reads as ${name}.decoded.json, and the schema takes each node`, () => {
      const documents = parseAllDocuments(specgraph(`testdata/nodes/${name}.yaml`));
      if (!Array.isArray(documents)) throw new Error(`${name}.yaml holds no document`);
      const want = JSON.parse(specgraph(`testdata/nodes/${name}.decoded.json`)) as unknown[];
      expect(documents.length).toBe(want.length);
      documents.forEach((document, i) => {
        expect(document.errors, `${name}.yaml, document ${i + 1}`).toEqual([]);
        expect(document.warnings, `${name}.yaml, document ${i + 1}`).toEqual([]);
        const data: unknown = document.toJS();
        expect(data, `${name}.yaml, document ${i + 1}`).toEqual(want[i]);
        expect(validate(data), `${name}.yaml, document ${i + 1}`).toBe(true);
      });
    });
  }
});
