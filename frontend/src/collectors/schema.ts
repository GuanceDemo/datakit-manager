import { parse, stringify } from 'smol-toml'

export type FieldType = 'string' | 'password' | 'number' | 'bool' | 'list' | 'numlist' | 'select' | 'kv' | 'text'

export interface Field {
  /** dotted path inside one [[inputs.x]] instance, e.g. "object.enabled" */
  path: string
  label: string
  type: FieldType
  /** value shown when the key is absent (DataKit's own default) */
  default?: unknown
  /** written into a newly created config */
  emit?: boolean
  required?: boolean
  help?: string
  placeholder?: string
  options?: string[]
  group: string
  advanced?: boolean
}

export interface CollectorSchema {
  /** collector id = logo name = key in the official samples */
  id: string
  /** name in [[inputs.<input>]] */
  input: string
  title: string
  desc: string
  /** default file name without .conf */
  file: string
  note?: string
  fields: Field[]
}

type Obj = Record<string, any>

export function parseDoc(text: string): { doc?: Obj; error?: string } {
  try {
    return { doc: parse(text) as Obj }
  } catch (e: any) {
    return { error: String(e?.message ?? e) }
  }
}

export function toToml(doc: Obj): string {
  return stringify(doc) + '\n'
}

/** [[inputs.x]] (array of tables) and [inputs.x] (single table) are both accepted by DataKit. */
export function instancesOf(doc: Obj, input: string): Obj[] {
  const v = doc?.inputs?.[input]
  if (Array.isArray(v)) return v
  if (v && typeof v === 'object') return [v]
  return []
}

export function withInstances(doc: Obj, input: string, list: Obj[]): Obj {
  const next = { ...doc, inputs: { ...(doc.inputs ?? {}) } }
  next.inputs[input] = list
  return next
}

export function getPath(obj: Obj, path: string): unknown {
  let cur: any = obj
  for (const k of path.split('.')) {
    if (cur == null || typeof cur !== 'object') return undefined
    cur = cur[k]
  }
  return cur
}

function isEmpty(v: unknown): boolean {
  if (v === undefined || v === null || v === '') return true
  if (Array.isArray(v)) return v.length === 0
  if (typeof v === 'object') return Object.keys(v as object).length === 0
  return false
}

/** Immutable set; empty values delete the key and prune now-empty parent tables. */
export function setPath(obj: Obj, path: string, value: unknown): Obj {
  const keys = path.split('.')
  const rec = (node: Obj | undefined, i: number): Obj | undefined => {
    const out: Obj = { ...(node ?? {}) }
    const k = keys[i]
    if (i === keys.length - 1) {
      if (isEmpty(value)) delete out[k]
      else out[k] = value
    } else {
      const child = rec(typeof out[k] === 'object' && !Array.isArray(out[k]) ? out[k] : undefined, i + 1)
      if (child === undefined) delete out[k]
      else out[k] = child
    }
    return i > 0 && Object.keys(out).length === 0 ? undefined : out
  }
  return rec(obj, 0) ?? {}
}

export function newInstance(schema: CollectorSchema): Obj {
  let inst: Obj = {}
  for (const f of schema.fields) {
    if ((f.emit || f.required) && f.default !== undefined) inst = setPath(inst, f.path, f.default)
  }
  return inst
}

export function generate(schema: CollectorSchema): string {
  return toToml({ inputs: { [schema.input]: [newInstance(schema)] } })
}

/** Required fields that are missing, as labels. */
export function missingRequired(schema: CollectorSchema, inst: Obj): string[] {
  return schema.fields.filter((f) => f.required && isEmpty(getPath(inst, f.path))).map((f) => f.label)
}
