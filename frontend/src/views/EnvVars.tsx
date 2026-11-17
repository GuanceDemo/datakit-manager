import { useEffect, useMemo, useState } from 'react'
import { Alert, Button, Card, Form, Input, Select, Space, Table, Tag, Tooltip, Typography, message } from 'antd'
import { DeleteOutlined, PlusOutlined, QuestionCircleOutlined, UndoOutlined } from '@ant-design/icons'
import type { Inspection } from '../types'
import { defaultableInputs, envGroups, knownEnv, type EnvItem } from '../envCatalog'
import { Logo } from '../components'
import type { ApplyRequest } from './ApplyModal'

function EnvControl({ item, value, onChange }: { item: EnvItem; value: string; onChange: (v: string) => void }) {
  switch (item.type) {
    case 'bool':
      return <Select allowClear style={{ width: 200 }} placeholder="未设置（默认）" value={value || undefined} onChange={(v) => onChange(v ?? '')}
        options={[{ value: 'true', label: 'true' }, { value: 'false', label: 'false' }]} />
    case 'select':
      return <Select allowClear style={{ width: 200 }} placeholder="未设置" value={value || undefined} onChange={(v) => onChange(v ?? '')}
        options={[...new Set([...(item.options ?? []), ...(value ? [value] : [])])].map((o) => ({ value: o, label: o }))} />
    case 'inputs': {
      const cur = value.split(',').map((s) => s.trim()).filter(Boolean)
      return <Select mode="tags" value={cur} style={{ width: '100%' }} tokenSeparators={[',', ' ']} placeholder="选择或输入采集器名"
        onChange={(v: string[]) => onChange(v.join(','))} options={defaultableInputs.map((o) => ({ value: o, label: o }))}
        optionRender={(o) => <Space><Logo name={String(o.value)} size={18} />{o.label}</Space>} />
    }
    case 'json': {
      let bad = false
      if (value) { try { JSON.parse(value) } catch { bad = true } }
      return <Input value={value} status={bad ? 'error' : undefined} placeholder={item.placeholder} onChange={(e) => onChange(e.target.value)}
        suffix={bad ? <Tooltip title="不是合法的 JSON"><Tag color="red" style={{ margin: 0 }}>JSON</Tag></Tooltip> : <span />} />
    }
    default:
      return <Input value={value} placeholder={item.placeholder} onChange={(e) => onChange(e.target.value)} />
  }
}

export function EnvVars({ ins, onApply }: { ins: Inspection; onApply: (r: ApplyRequest) => void }) {
  const orig = useMemo(() => new Map(ins.env.map((e) => [e.name, e])), [ins])
  // edits: name -> new value; null = delete
  const [edits, setEdits] = useState<Record<string, string | null>>({})
  const [custom, setCustom] = useState({ name: '', value: '' })
  useEffect(() => setEdits({}), [ins])

  const cur = (name: string): string => (name in edits ? edits[name] ?? '' : orig.get(name)?.value ?? '')
  const set = (name: string, v: string) => setEdits((e) => {
    const next = { ...e }
    const o = orig.get(name)
    if (o ? v === o.value : v === '') delete next[name] // back to original
    else next[name] = v === '' && knownEnv.has(name) ? null : v // clearing a catalogued var removes it
    return next
  })

  const changes = Object.entries(edits)
  const others = ins.env.filter((e) => !knownEnv.has(e.name))
  const added = changes.filter(([n, v]) => v !== null && !orig.has(n) && !knownEnv.has(n))

  const submit = () => {
    const envSet = changes.filter(([, v]) => v !== null).map(([name, v]) => ({ name, value: v as string }))
    const envDelete = changes.filter(([n, v]) => v === null && orig.has(n)).map(([n]) => n)
    if (!envSet.length && !envDelete.length) return void message.info('没有修改')
    const line = (n: string, v: string) => `${n}=${v}`
    const names = [...new Set([...ins.env.map((e) => e.name), ...envSet.map((e) => e.name)])]
    const before = ins.env.filter((e) => e.editable).map((e) => line(e.name, e.value)).join('\n') + '\n'
    const after = names.filter((n) => orig.get(n)?.editable !== false && !envDelete.includes(n))
      .map((n) => line(n, n in edits ? (edits[n] as string) : orig.get(n)!.value)).join('\n') + '\n'
    onApply({ title: '修改环境变量', change: { envSet, envDelete }, diffs: [{ title: `${ins.workload.kind}/${ins.workload.name} 容器环境变量`, before, after }] })
  }

  const addCustom = () => {
    const name = custom.name.trim()
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(name)) return void message.error('变量名不合法')
    if (orig.has(name)) return void message.error(`${name} 已存在，请直接修改`)
    setEdits((e) => ({ ...e, [name]: custom.value }))
    setCustom({ name: '', value: '' })
  }

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Alert type="info" showIcon message="环境变量用于 DataKit 自身设置和无需连接信息的简单采集器（DDTrace、容器等）。MySQL、Redis 这类采集器请在“采集器配置”中通过 ConfigMap 配置。" />

      {envGroups.map((g) => (
        <Card key={g.key} size="small" title={<Space>{g.logo && <Logo name={g.logo} size={20} />}{g.title}</Space>}>
          {g.desc && <Typography.Paragraph type="secondary" style={{ marginBottom: 12 }}>{g.desc}</Typography.Paragraph>}
          <Form layout="horizontal" labelCol={{ flex: '210px' }} labelAlign="left" labelWrap colon={false}>
            {g.items.map((it) => {
              const o = orig.get(it.name)
              const dirty = it.name in edits
              return (
                <Form.Item key={it.name} style={{ marginBottom: 12 }}
                  label={<span>{it.label}{it.help && <Tooltip title={it.help}><QuestionCircleOutlined style={{ marginLeft: 6, color: '#9ca3af' }} /></Tooltip>}</span>}
                  extra={<span style={{ fontSize: 11 }}><code style={{ color: '#9ca3af' }}>{it.name}</code>
                    {dirty && <Tag color="blue" style={{ marginLeft: 8 }}>{edits[it.name] === null ? '将删除' : o ? '已修改' : '将新增'}</Tag>}</span>}>
                  {o && !o.editable ? <Input disabled value={o.fromRef} /> : <EnvControl item={it} value={cur(it.name)} onChange={(v) => set(it.name, v)} />}
                </Form.Item>
              )
            })}
          </Form>
        </Card>
      ))}

      <Card size="small" title="其它环境变量">
        <Table size="small" pagination={false} rowKey="name" dataSource={[...others, ...added.map(([name]) => ({ name, value: '', fromRef: '', editable: true }))]}
          columns={[
            { title: '变量名', dataIndex: 'name', width: 320, render: (n: string) => <code>{n}</code> },
            { title: '值', render: (_, e) => !e.editable ? <Typography.Text type="secondary">{e.fromRef}</Typography.Text>
                : edits[e.name] === null ? <Typography.Text delete type="secondary">{e.value}</Typography.Text>
                : <Input size="small" value={cur(e.name)} onChange={(ev) => setEdits((x) => {
                    const next = { ...x }
                    if (orig.has(e.name) && ev.target.value === orig.get(e.name)!.value) delete next[e.name]
                    else next[e.name] = ev.target.value
                    return next
                  })} /> },
            { title: '', width: 90, align: 'right', render: (_, e) => !e.editable ? <Tag>只读</Tag>
                : e.name in edits ? <Button size="small" icon={<UndoOutlined />} onClick={() => setEdits((x) => { const n = { ...x }; delete n[e.name]; return n })} />
                : <Button size="small" danger icon={<DeleteOutlined />} onClick={() => setEdits((x) => ({ ...x, [e.name]: null }))} /> },
          ]} />
        <Space.Compact style={{ marginTop: 12, width: '100%' }}>
          <Input placeholder="ENV_XXX" style={{ width: 320 }} value={custom.name} onChange={(e) => setCustom({ ...custom, name: e.target.value })} />
          <Input placeholder="值" value={custom.value} onChange={(e) => setCustom({ ...custom, value: e.target.value })} onPressEnter={addCustom} />
          <Button icon={<PlusOutlined />} onClick={addCustom}>添加</Button>
        </Space.Compact>
      </Card>

      <div className="sticky-bar">
        <span>{changes.length ? `${changes.length} 项修改待应用` : '没有修改'}</span>
        <Space>
          <Button disabled={!changes.length} onClick={() => setEdits({})}>放弃修改</Button>
          <Button type="primary" disabled={!changes.length} onClick={submit}>预览并应用</Button>
        </Space>
      </div>
    </Space>
  )
}
