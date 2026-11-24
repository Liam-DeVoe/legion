import { describe, expect, test } from "bun:test";
import { resolve } from "node:path";
import ts from "typescript";

const output = resolve(import.meta.dir, "../src/dispatch-api.generated.ts");
const generator = resolve(import.meta.dir, "gen-api.ts");
const schemaFile = resolve(import.meta.dir, "../schemas/dispatch-openapi.json");
const repoRoot = resolve(import.meta.dir, "../../..");

type Document = {
  paths: Record<string, unknown>;
  components: { schemas: Record<string, { properties?: Record<string, unknown> }> };
};

/** What one module declares, read from its syntax tree: every path of `paths`, every schema of
 * `components.schemas` with the names of its own fields, every schema exported as an alias, and
 * the schemas `SplitOwnedSchemaName` says the hand-written split exports instead. */
function declaredBy(source: string) {
  const file = ts.createSourceFile("dispatch-api.generated.ts", source, ts.ScriptTarget.Latest);
  const paths = new Set<string>();
  const schemas = new Map<string, string[]>();
  const aliases = new Set<string>();
  const owned = new Set<string>();
  const nameOf = (name: ts.PropertyName) =>
    ts.isIdentifier(name) || ts.isStringLiteral(name) ? name.text : name.getText(file);
  // A schema's own fields are the property members of its object type, or of each object type
  // an intersection joins, which is how an object with a union beside it is rendered.
  const fieldsOf = (type: ts.TypeNode | undefined): string[] => {
    if (type === undefined) return [];
    if (ts.isTypeLiteralNode(type)) {
      return type.members
        .filter(ts.isPropertySignature)
        .map((member) => nameOf(member.name))
        .sort();
    }
    if (ts.isIntersectionTypeNode(type)) return type.types.flatMap(fieldsOf).sort();
    if (ts.isParenthesizedTypeNode(type)) return fieldsOf(type.type);
    return [];
  };
  for (const statement of file.statements) {
    // What a consumer can import is what the module exports, so a declaration without `export`
    // declares nothing here.
    const exported =
      ts.canHaveModifiers(statement) &&
      (ts.getModifiers(statement) ?? []).some(
        (modifier) => modifier.kind === ts.SyntaxKind.ExportKeyword
      );
    if (!exported) continue;
    if (ts.isInterfaceDeclaration(statement) && statement.name.text === "paths") {
      for (const member of statement.members) {
        if (member.name !== undefined) paths.add(nameOf(member.name));
      }
    }
    if (ts.isInterfaceDeclaration(statement) && statement.name.text === "components") {
      const block = statement.members.find(
        (member) => member.name !== undefined && nameOf(member.name) === "schemas"
      );
      if (
        block !== undefined &&
        ts.isPropertySignature(block) &&
        block.type !== undefined &&
        ts.isTypeLiteralNode(block.type)
      ) {
        for (const schema of block.type.members.filter(ts.isPropertySignature)) {
          schemas.set(nameOf(schema.name), fieldsOf(schema.type));
        }
      }
    }
    if (ts.isTypeAliasDeclaration(statement)) {
      const name = statement.name.text;
      if (name === "SplitOwnedSchemaName") {
        const members = ts.isUnionTypeNode(statement.type)
          ? statement.type.types
          : [statement.type];
        for (const member of members) {
          if (ts.isLiteralTypeNode(member) && ts.isStringLiteral(member.literal))
            owned.add(member.literal.text);
        }
      } else if (statement.type.getText(file) === `components["schemas"]["${name}"]`) {
        aliases.add(name);
      }
    }
  }
  return { paths, schemas, aliases, owned };
}

/** Every way the module falls short of the document: a path it does not declare, a schema it
 * does not declare or declares with other fields, and a schema it neither exports as an alias
 * nor names as the split's. Empty when the module declares the document whole. */
function shortfalls(document: Document, source: string): string[] {
  const module = declaredBy(source);
  const problems: string[] = [];
  for (const path of Object.keys(document.paths)) {
    if (!module.paths.has(path)) problems.push(`the module declares no path ${path}`);
  }
  for (const [name, schema] of Object.entries(document.components.schemas)) {
    const fields = module.schemas.get(name);
    if (fields === undefined) {
      problems.push(`the module declares no schema ${name}`);
      continue;
    }
    const want = Object.keys(schema.properties ?? {}).sort();
    if (fields.join(",") !== want.join(",")) {
      problems.push(
        `the module declares ${name} as [${fields.join(", ")}], and the document as [${want.join(", ")}]`
      );
    }
    if (!module.aliases.has(name) && !module.owned.has(name)) {
      problems.push(`the module exports no alias for ${name} and does not name it as the split's`);
    }
  }
  return problems;
}

async function render(): Promise<string> {
  const spawned = Bun.spawn(["bun", generator], { cwd: repoRoot, stderr: "pipe", stdout: "pipe" });
  const stderr = await new Response(spawned.stderr).text();
  expect(stderr).toBe("");
  expect(await spawned.exited).toBe(0);
  return Bun.file(output).text();
}

describe("gen-api", () => {
  // The module is rendered from the document on every install and committed nowhere, so the link
  // this holds is the generator's own: what it writes declares every route and every schema of
  // the document, field for field, and exports each schema under its own name or leaves it to the
  // split by name. The generator renders with openapi-typescript, so a slow first import on a cold
  // runner is why the bound is 120 s.
  test("the module declares every path and every schema of the document, field for field", async () => {
    const source = await render();
    const document = (await Bun.file(schemaFile).json()) as Document;
    expect(source).toStartWith(
      "// Code generated by scripts/gen-api.ts from schemas/dispatch-openapi.json"
    );
    expect(Object.keys(document.paths).length).toBeGreaterThan(100);
    expect(Object.keys(document.components.schemas).length).toBeGreaterThan(100);
    expect(shortfalls(document, source)).toEqual([]);
  }, 120_000);

  // The control: a document holding a path, a schema and a field the module does not declare is
  // three shortfalls, so the check above can see each kind it passes.
  test("a path, a schema and a field the module leaves out are each a shortfall", async () => {
    const source = await render();
    const document = (await Bun.file(schemaFile).json()) as Document;
    document.paths["/api/v1/_planted"] = {};
    document.components.schemas.PlantedSchema = { properties: { a: {} } };
    const issue = document.components.schemas.Issue;
    if (issue === undefined)
      throw new Error("the document declares no Issue schema to plant a field on");
    issue.properties = { ...issue.properties, planted_field: {} };
    expect(shortfalls(document, source)).toEqual([
      "the module declares no path /api/v1/_planted",
      expect.stringContaining("the module declares Issue as ["),
      "the module declares no schema PlantedSchema",
    ]);
  }, 120_000);

  // The control for exports: an alias written without `export` is one no consumer imports, so it
  // is a shortfall by its name.
  test("an alias the module declares without exporting it is a shortfall", async () => {
    const source = (await render()).replace(
      'export type Issue = components["schemas"]["Issue"];',
      'type Issue = components["schemas"]["Issue"];'
    );
    const document = (await Bun.file(schemaFile).json()) as Document;
    expect(source).toContain('\ntype Issue = components["schemas"]["Issue"];');
    expect(shortfalls(document, source)).toEqual([
      "the module exports no alias for Issue and does not name it as the split's",
    ]);
  }, 120_000);
});
