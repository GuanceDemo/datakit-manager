import { useState } from 'react'
import { Button, Form, Input, InputNumber, Select, Switch, Tabs, Tooltip, Typography } from 'antd'
import { DeleteOutlined, PlusOutlined, QuestionCircleOutlined } from '@ant-design/icons'
import { type CollectorSchema, type Field, getPath } from '../collectors/schema'

type Obj = Record<string, any>

function KV({ value, onChange }: { value: Obj; onChange: (v: Obj) => void }) {
  const rows = Object.entries(value ?? {})
  const [draft, setDraft] = useState<[string, string]>(['', ''])
  const set = (i: number, k: string, v: string) => {
    const next = rows.map((r, j) => (j === i ? [k, v] : r))
    onChange(Object.fromEntries(next.filter(([key]) => key !== '')))
  }
  return (
    <div style={{ display: 'grid', gap: 6 }}>
      {rows.map(([k, v], i) => (
        <div key={i} style={{ display: 'flex', gap: 6 }}>
          <Input value={k} style={{ width: 200 }} onChange={(e) => set(i, e.target.value, String(v))} />
          <Input value={String(v)} onChange={(e) => set(i, k, e.target.value)} />
          <Button icon={<DeleteOutlined />} onClick={() => onChange(Object.fromEntries(rows.filter((_, j) => j !== i)))} />
        </div>
      ))}
      <div style={{ display: 'flex', gap: 6 }}>
        <Input placeholder="标签名" style={{ width: 200 }} value={draft[0]} onChange={(e) => setDraft([e.target.value, draft[1]])} />
        <Input placeholder="标签值" value={draft[1]} onChange={(e) => setDraft([draft[0], e.target.value])} />
        <Button icon={<PlusOutlined />} disabled={!draft[0]} onClick={() => { onChange({ ...value, [draft[0]]: draft[1] }); setDraft(['', '']) }} />
      </div>
    </div>
  )
}

function Control({ f, value, onChange }: { f: Field; value: unknown; onChange: (v: unknown) => void }) {
  const ph = f.placeholder ?? (f.default !== undefined && typeof f.default !== 'object' ? String(f.default) : undefined)
  switch (f.type) {
    case 'bool':
      return <Switch checked={(value ?? f.default ?? false) as boolean} onChange={onChange} />
    case 'number':
      return <InputNumber value={value as number | null} placeholder={ph} style={{ width: 200 }} onChange={(v) => onChange(v ?? undefined)} />
    case 'password':
      return <Input.Password value={(value ?? '') as string} placeholder={ph} autoComplete="new-password" onChange={(e) => onChange(e.target.value)} />
    case 'select':
      return <Select allowClear value={value as string | undefined} placeholder={f.default ? `默认 ${String(f.default)}` : '使用默认值'} style={{ width: 260 }}
        options={(f.options ?? []).map((o) => ({ value: o, label: o }))} onChange={(v) => onChange(v)} />
    case 'list':
      return <Select mode="tags" open={false} suffixIcon={null} value={(value ?? []) as string[]} placeholder={ph ? `${ph}，回车添加` : '输入后回车添加'} tokenSeparators={[',', ' ']} onChange={onChange} />
    case 'numlist':
      return <Select mode="tags" open={false} suffixIcon={null} value={((value ?? []) as number[]).map(String)} placeholder="输入数字后回车添加" tokenSeparators={[',', ' ']}
        onChange={(v: string[]) => onChange(v.map(Number).filter((n) => Number.isFinite(n)))} />
    case 'kv':
      return <KV value={(value ?? {}) as Obj} onChange={onChange} />
    case 'text':
      return <Input.TextArea value={(value ?? '') as string} autoSize={{ minRows: 2, maxRows: 8 }} onChange={(e) => onChange(e.target.value)} />
    default:
      return <Input value={(value ?? '') as string} placeholder={ph} onChange={(e) => onChange(e.target.value)} />
  }
}

function InstanceForm({ schema, inst, onEdit }: { schema: CollectorSchema; inst: Obj; onEdit: (path: string, value: unknown) => void }) {
  const [adv, setAdv] = useState(false)
  const groups: string[] = []
  for (const f of schema.fields) if (!groups.includes(f.group)) groups.push(f.group)
  // an advanced field that already has a value is always shown
  const visible = (f: Field) => adv || !f.advanced || getPath(inst, f.path) !== undefined
  const hidden = schema.fields.filter((f) => !visible(f)).length

  return (
    <Form layout="horizontal" labelCol={{ flex: '190px' }} labelAlign="left" labelWrap colon={false} size="middle">
      {groups.map((g) => {
        const fs = schema.fields.filter((f) => f.group === g && visible(f))
        if (!fs.length) return null
        return (
          <div key={g} style={{ marginBottom: 8 }}>
            <Typography.Text type="secondary" style={{ fontSize: 12, letterSpacing: 1 }}>{g}</Typography.Text>
            <div style={{ borderTop: '1px solid #eef0f3', marginTop: 4, paddingTop: 12 }}>
              {fs.map((f) => (
                <Form.Item key={f.path} required={f.required} style={{ marginBottom: 14 }}
                  label={<span>{f.label}{f.help && <Tooltip title={f.help}><QuestionCircleOutlined style={{ marginLeft: 6, color: '#9ca3af' }} /></Tooltip>}</span>}
                  extra={<code style={{ fontSize: 11, color: '#9ca3af' }}>{f.path}</code>}>
                  <Control f={f} value={getPath(inst, f.path)} onChange={(v) => onEdit(f.path, v)} />
                </Form.Item>
              ))}
            </div>
          </div>
        )
      })}
      {(hidden > 0 || adv) && <Button type="link" style={{ paddingLeft: 0 }} onClick={() => setAdv(!adv)}>{adv ? '收起高级选项' : `显示高级选项（${hidden}）`}</Button>}
    </Form>
  )
}

/** Form for all [[inputs.x]] instances in one conf file. */
export function SchemaForm({ schema, instances, onEdit, onAdd, onRemove }: {
  schema: CollectorSchema; instances: Obj[]
  onEdit: (index: number, path: string, value: unknown) => void; onAdd: () => void; onRemove: (index: number) => void
}) {
  const [active, setActive] = useState('0')
  const idx = Math.min(Number(active), instances.length - 1)
  const label = (inst: Obj, i: number) => {
    const main = schema.fields.find((f) => f.required)
    const v = main ? getPath(inst, main.path) : undefined
    const s = Array.isArray(v) ? v[0] : v
    return s ? String(s).replace(/^[a-z+]+:\/\/([^@/]*@)?/, '').slice(0, 28) : `实例 ${i + 1}`
  }
  return (
    <Tabs type="editable-card" size="small" activeKey={String(idx)} onChange={setActive}
      onEdit={(key, action) => {
        if (action === 'add') { onAdd(); setActive(String(instances.length)) }
        else { const i = Number(key); onRemove(i); setActive(String(Math.max(0, i - 1))) }
      }}
      items={instances.map((inst, i) => ({
        key: String(i), label: label(inst, i), closable: instances.length > 1,
        children: <InstanceForm schema={schema} inst={inst} onEdit={(path, v) => onEdit(i, path, v)} />,
      }))} />
  )
}
