// Comment-preserving edits of a TOML conf file.
//
// The form changes one key at a time. Re-serialising the whole document would drop the
// user's comments and layout, so we first try a line-level patch of just that key and then
// VERIFY the result by parsing it and comparing with the document we expect. Only when the
// patch is impossible or the verification fails do we fall back to full re-serialisation.
import { stringify } from 'smol-toml'
import { type CollectorSchema, getPath, instancesOf, newInstance, parseDoc, setPath, toToml } from './schema'

type Obj = Record<string, any>

const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v) && !(v instanceof Date)

export function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true
  if (a instanceof Date && b instanceof Date) return a.getTime() === b.getTime()
  if (Array.isArray(a) && Array.isArray(b)) return a.length === b.length && a.every((x, i) => deepEqual(x, b[i]))
  if (isObj(a) && isObj(b)) {
    const ka = Object.keys(a), kb = Object.keys(b)
    return ka.length === kb.length && ka.every((k) => k in b && deepEqual(a[k], b[k]))
  }
  return false
}

const headerRe = /^\s*(\[\[?)\s*([^\]#]+?)\s*\]\]?\s*(#.*)?$/
const header = (line: string): { name: string; array: boolean } | null => {
  const m = headerRe.exec(line)
  return m ? { name: m[2].replace(/\s*\.\s*/g, '.'), array: m[1] === '[[' } : null
}
const keyLine = (line: string) => /^\s*(?:[A-Za-z0-9_-]+|"[^"]*"|'[^']*')\s*=/.test(line)
const esc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
const indentOf = (line: string) => /^\s*/.exec(line)![0]

/** [start, end) line range of the idx-th instance of inputs.<input>, header line included. */
function instanceBlock(lines: string[], input: string, idx: number): [number, number] | null {
  const root = `inputs.${input}`
  const starts: number[] = []
  lines.forEach((l, i) => { if (header(l)?.name === root) starts.push(i) })
  if (idx >= starts.length) return null
  const start = starts[idx]
  let end = lines.length
  for (let i = start + 1; i < lines.length; i++) {
    const h = header(lines[i])
    if (h && h.name !== root && !h.name.startsWith(root + '.')) { end = i; break }
    if (h && h.name === root) { end = i; break }
  }
  return [start, end]
}

/** Set (or, for an empty value, remove) one scalar/array key. Returns null when it cannot be done safely. */
function patchKey(text: string, input: string, idx: number, path: string, value: unknown): string | null {
  const lines = text.split('\n')
  const block = instanceBlock(lines, input, idx)
  if (!block) return null
  const [bStart, bEnd] = block
  const parts = path.split('.')
  const key = parts.pop()!
  if (!/^[A-Za-z0-9_-]+$/.test(key) || isObj(value)) return null
  const tablePath = parts.join('.')
  const tableName = tablePath ? `inputs.${input}.${tablePath}` : ''
  const empty = value === undefined || value === null || value === '' || (Array.isArray(value) && value.length === 0)
  const rendered = empty ? '' : stringify({ [key]: value }).trim()
  if (rendered.includes('\n')) return null

  // locate the section [sStart, sEnd) holding the key; sStart is the line after its header
  let sStart = -1
  if (!tableName) sStart = bStart + 1
  else for (let i = bStart + 1; i < bEnd; i++) { const h = header(lines[i]); if (h && !h.array && h.name === tableName) { sStart = i + 1; break } }

  if (sStart < 0) {
    if (empty) return text
    // table does not exist yet: append it at the end of the instance block
    let at = bEnd
    while (at > bStart + 1 && lines[at - 1].trim() === '') at--
    const ind = indentOf(lines[bStart]) + (lines.slice(bStart + 1, bEnd).find(keyLine) ? indentOf(lines.slice(bStart + 1, bEnd).find(keyLine)!).slice(indentOf(lines[bStart]).length) : '')
    lines.splice(at, 0, '', `${ind}[${tableName}]`, `${ind}${ind === indentOf(lines[bStart]) ? '' : '  '}${rendered}`)
    return lines.join('\n')
  }
  let sEnd = bEnd
  for (let i = sStart; i < bEnd; i++) if (header(lines[i])) { sEnd = i; break }

  const re = new RegExp(`^(\\s*)${esc(key)}\\s*=(.*)$`)
  let found = -1
  for (let i = sStart; i < sEnd; i++) if (re.test(lines[i])) { found = i; break }

  if (found >= 0) {
    const val = re.exec(lines[found])![2].trim()
    // multi-line values (arrays / ''' strings spanning lines) are not patched line-wise
    const opens = (val.match(/\[/g) ?? []).length, closes = (val.match(/\]/g) ?? []).length
    if (opens !== closes || ((val.match(/'''/g) ?? []).length % 2) || ((val.match(/"""/g) ?? []).length % 2)) return null
    if (empty) {
      lines.splice(found, 1)
      // drop a table header left without any content
      if (tableName && !lines.slice(sStart, sEnd - 1).some((l) => l.trim() !== '' && !l.trim().startsWith('#'))) lines.splice(sStart - 1, 1)
    } else lines[found] = indentOf(lines[found]) + rendered
    return lines.join('\n')
  }
  if (empty) return text
  let last = -1, sample = ''
  for (let i = sStart; i < sEnd; i++) if (keyLine(lines[i])) { last = i; sample = lines[i] }
  const ind = sample ? indentOf(sample) : indentOf(lines[sStart - 1]) + (tableName || indentOf(lines[sStart - 1]) ? '  ' : '')
  lines.splice(last >= 0 ? last + 1 : sStart, 0, ind + rendered)
  return lines.join('\n')
}

function rebuild(doc: Obj, input: string, list: Obj[]): Obj {
  const single = !Array.isArray(doc.inputs?.[input]) && list.length === 1
  return { ...doc, inputs: { ...(doc.inputs ?? {}), [input]: single ? list[0] : list } }
}

const verified = (patched: string | null, expected: Obj): string => {
  if (patched !== null) {
    const r = parseDoc(patched)
    if (r.doc && deepEqual(r.doc, expected)) return patched
  }
  return toToml(expected)
}

/** Change one field of one instance. */
export function editField(text: string, doc: Obj, input: string, idx: number, path: string, value: unknown): string {
  const list = instancesOf(doc, input)
  const old = getPath(list[idx], path)
  const expected = rebuild(doc, input, list.map((x, j) => (j === idx ? setPath(x, path, value) : x)))

  // a key/value table (tags) is patched key by key
  const ops: [string, unknown][] = []
  if (isObj(value) || isObj(old)) {
    const a = isObj(old) ? old : {}, b = isObj(value) ? value : {}
    for (const k of Object.keys(a)) if (!(k in b)) ops.push([`${path}.${k}`, undefined])
    for (const k of Object.keys(b)) if (!deepEqual(a[k], b[k])) ops.push([`${path}.${k}`, b[k]])
  } else ops.push([path, value])

  let patched: string | null = text
  for (const [p, v] of ops) patched = patched === null ? null : patchKey(patched, input, idx, p, v)
  return verified(patched, expected)
}

export function addInstance(text: string, doc: Obj, schema: CollectorSchema): string {
  const list = instancesOf(doc, schema.input)
  const inst = newInstance(schema)
  const expected = { ...doc, inputs: { ...(doc.inputs ?? {}), [schema.input]: [...list, inst] } }
  const patched = Array.isArray(doc.inputs?.[schema.input]) ? text.replace(/\s*$/, '\n\n') + toToml({ inputs: { [schema.input]: [inst] } }) : null
  return verified(patched, expected)
}

export function removeInstance(text: string, doc: Obj, input: string, idx: number): string {
  const list = instancesOf(doc, input)
  const expected = rebuild(doc, input, list.filter((_, j) => j !== idx))
  const lines = text.split('\n')
  const block = instanceBlock(lines, input, idx)
  let patched: string | null = null
  if (block && list.length > 1) {
    let start = block[0]
    // take the comment lines directly above the header with it
    while (start > 0 && lines[start - 1].trim().startsWith('#')) start--
    lines.splice(start, block[1] - start)
    patched = lines.join('\n')
  }
  return verified(patched, expected)
}
