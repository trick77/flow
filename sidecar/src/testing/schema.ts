// Test support: checks drafts against contract/events.schema.json, the
// subset contractgen accepts (type, required, properties, enum, $ref, items,
// additionalProperties). Stricter than a reader: a property the schema does
// not name is an error, so a writer cannot drift from the contract.

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

interface Schema {
  type?: string;
  required?: string[];
  properties?: Record<string, Schema>;
  enum?: unknown[];
  $ref?: string;
  items?: Schema;
  additionalProperties?: Schema | boolean;
}

interface Contract {
  "x-unions": { Event: { kinds: { kind: string; payload: string }[] } };
  $defs: Record<string, Schema>;
}

function repoFile(rel: string): string {
  return fileURLToPath(new URL(`../../../${rel}`, import.meta.url));
}

const contract = JSON.parse(
  readFileSync(repoFile("contract/events.schema.json"), "utf8"),
) as Contract;
const payloads = new Map(
  contract["x-unions"].Event.kinds.map((k) => [k.kind, k.payload]),
);

function check(
  value: unknown,
  schema: Schema,
  path: string,
  errs: string[],
): void {
  if (schema.$ref !== undefined) {
    const name = schema.$ref.replace("#/$defs/", "");
    const def = contract.$defs[name];
    if (def === undefined) errs.push(`${path}: unknown $ref ${schema.$ref}`);
    else check(value, def, path, errs);
    return;
  }
  if (schema.enum !== undefined && !schema.enum.includes(value)) {
    errs.push(`${path}: ${JSON.stringify(value)} not in enum`);
    return;
  }
  switch (schema.type) {
    case undefined:
      return;
    case "string":
      if (typeof value !== "string") errs.push(`${path}: want string`);
      return;
    case "boolean":
      if (typeof value !== "boolean") errs.push(`${path}: want boolean`);
      return;
    case "integer":
      if (!Number.isInteger(value))
        errs.push(`${path}: want integer, got ${JSON.stringify(value)}`);
      return;
    case "number":
      if (typeof value !== "number" || !Number.isFinite(value))
        errs.push(`${path}: want number`);
      return;
    case "array":
      if (!Array.isArray(value)) {
        errs.push(`${path}: want array`);
        return;
      }
      value.forEach(
        (v, i) => schema.items && check(v, schema.items, `${path}[${i}]`, errs),
      );
      return;
    case "object": {
      if (value === null || typeof value !== "object" || Array.isArray(value)) {
        errs.push(`${path}: want object`);
        return;
      }
      const o = value as Record<string, unknown>;
      for (const r of schema.required ?? []) {
        if (o[r] === undefined) errs.push(`${path}.${r}: required`);
      }
      for (const [k, v] of Object.entries(o)) {
        const p = schema.properties?.[k];
        if (p !== undefined) check(v, p, `${path}.${k}`, errs);
        else if (typeof schema.additionalProperties === "object") {
          check(v, schema.additionalProperties, `${path}.${k}`, errs);
        } else errs.push(`${path}.${k}: not in the schema`);
      }
      return;
    }
    default:
      errs.push(`${path}: schema type ${schema.type} not in the subset`);
  }
}

// draftErrors lists what is wrong with one stdout line's draft; empty when
// it is a valid EventDraft of a contract kind.
export function draftErrors(d: unknown): string[] {
  const errs: string[] = [];
  if (d === null || typeof d !== "object" || Array.isArray(d))
    return ["draft: want object"];
  const o = d as Record<string, unknown>;
  for (const k of Object.keys(o)) {
    if (!["kind", "data", "parentTool", "cmd"].includes(k))
      errs.push(`draft.${k}: not in EventDraft`);
  }
  for (const k of ["parentTool", "cmd"]) {
    if (o[k] !== undefined && typeof o[k] !== "string")
      errs.push(`draft.${k}: want string`);
  }
  const payload = payloads.get(String(o.kind));
  if (payload === undefined)
    return [...errs, `draft.kind: ${String(o.kind)} not in the contract`];
  check(o.data, { $ref: `#/$defs/${payload}` }, `${String(o.kind)}.data`, errs);
  return errs;
}

// fixtureLines reads a contract fixture (contract/fixtures/<name>) as
// parsed JSON lines.
export function fixtureLines<T>(name: string): T[] {
  return readFileSync(repoFile(`contract/fixtures/${name}`), "utf8")
    .split("\n")
    .filter((l) => l.trim() !== "")
    .map((l) => JSON.parse(l) as T);
}
