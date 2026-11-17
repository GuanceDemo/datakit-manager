import { type ReactNode, useEffect, useMemo, useState } from 'react'
import { Alert, Button, Drawer, Empty, Input, Modal, Popconfirm, Segmented, Space, Table, Tooltip, Typography, message } from 'antd'
import { DeleteOutlined, EditOutlined, EyeOutlined, PlusOutlined, SearchOutlined } from '@ant-design/icons'
import type { Sample } from '../types'
import { hiddenInGallery, schemaById, schemaByInput, schemas, titles } from '../collectors'
import { type CollectorSchema, generate, instancesOf, missingRequired, parseDoc } from '../collectors/schema'
import { addInstance, editField, removeInstance } from '../collectors/patch'
import { Logo, TomlEditor } from '../components'
import { SchemaForm } from './SchemaForm'

/** One collector config file, wherever it is stored (ConfigMap key, persisted directory, host disk). */
export interface ConfRow {
  id: string
  /** file name, or path relative to conf.d */
  name: string
  /** where it lives, shown under the name */
  location: string
  content: string
  inputs: string[]
  error: string
  editable: boolean
  /** why it is read-only */
  readonlyHint?: string
  tags: ReactNode
}

interface Editing {
  row: ConfRow | null // null = new file
  id: string // collector id (logo / sample key)
  input: string
  schema?: CollectorSchema
  name: string
  original: string
  content: string
}

export const nameOf = (id: string) => schemaById.get(id)?.title ?? titles[id] ?? id

/**
 * Lists collector config files and edits them (form or TOML). Storage-agnostic: the owner
 * decides what saving and deleting mean.
 */
export function ConfManager({ rows, samples, banner, subdirs, newFileHint, onSave, onDelete, resetKey, deleteHint }: {
  rows: ConfRow[]
  samples: Sample[]
  banner?: ReactNode
  /** file names may contain sub-directories (hosts, persisted directories) */
  subdirs?: boolean
  newFileHint: (name: string) => string
  onSave: (row: ConfRow | null, name: string, content: string, original: string) => void
  onDelete: (row: ConfRow) => void
  /** the editor closes whenever this changes (i.e. fresh data arrived after an apply) */
  resetKey: unknown
  deleteHint: string
}) {
  const [gallery, setGallery] = useState(false)
  const [q, setQ] = useState('')
  const [ed, setEd] = useState<Editing | null>(null)
  const [tab, setTab] = useState<'form' | 'toml'>('form')
  useEffect(() => setEd(null), [resetKey])
  const sampleById = useMemo(() => new Map(samples.map((s) => [s.id, s])), [samples])
  const sampleByInput = useMemo(() => new Map(samples.map((s) => [s.input, s])), [samples])
  const names = useMemo(() => new Set(rows.map((r) => r.name)), [rows])
  const readOnly = !!ed?.row && !ed.row.editable

  const open = (e: Editing) => { setEd(e); setTab(e.schema && !parseDoc(e.content).error ? 'form' : 'toml') }

  const startNew = (id: string) => {
    const schema = schemaById.get(id)
    const sample = sampleById.get(id)
    const base = schema?.file ?? id
    let name = `${base}.conf`
    for (let i = 2; names.has(name); i++) name = `${base}-${i}.conf`
    setGallery(false)
    open({ row: null, id, input: schema?.input ?? sample?.input ?? id, schema, name, original: '', content: schema ? generate(schema) : sample?.sample ?? '' })
  }

  const startEdit = (r: ConfRow) => {
    const schema = r.inputs.length === 1 ? schemaByInput.get(r.inputs[0]) : undefined
    const input = r.inputs[0] ?? r.name.replace(/^.*\//, '').replace(/\.conf$/, '')
    open({ row: r, id: schema?.id ?? sampleByInput.get(input)?.id ?? input, input, schema, name: r.name, original: r.content, content: r.content })
  }

  const parsed = ed ? parseDoc(ed.content) : {}
  const instances = ed && parsed.doc ? instancesOf(parsed.doc, ed.input) : []
  const nameRe = subdirs ? /^[-._a-zA-Z0-9]+(\/[-._a-zA-Z0-9]+)*\.conf$/ : /^[-._a-zA-Z0-9]+\.conf$/

  const submit = () => {
    if (!ed) return
    if (parsed.error) return void message.error('TOML 语法错误：' + parsed.error)
    if (!nameRe.test(ed.name) || ed.name.includes('..')) return void message.error(`文件名只能包含字母、数字、- _ .${subdirs ? ' 和 /' : ''}，并以 .conf 结尾`)
    if (!ed.row && names.has(ed.name)) return void message.error(`${ed.name} 已存在，请换一个文件名`)
    if (ed.schema) {
      const miss = instances.flatMap((inst, i) => missingRequired(ed.schema!, inst).map((l) => `实例 ${i + 1}：${l}`))
      if (miss.length) return void message.error('必填项未填写 — ' + miss.join('；'))
    }
    onSave(ed.row, ed.name, ed.content, ed.original)
  }

  const loadSample = () => {
    const s = ed && (sampleById.get(ed.id) ?? sampleByInput.get(ed.input))
    if (!ed || !s) return
    Modal.confirm({ title: '载入官方示例', content: '将用 DataKit 官方示例（含全部可选项和注释）替换当前内容。', onOk: () => setEd({ ...ed, content: s.sample }) })
  }

  const galleryItems = useMemo(() => {
    const kw = q.trim().toLowerCase()
    const match = (id: string, input: string) => !kw || id.includes(kw) || input.includes(kw) || nameOf(id).toLowerCase().includes(kw)
    const formIds = new Set(schemas.map((s) => s.id))
    return {
      form: schemas.filter((s) => match(s.id, s.input)),
      raw: samples.filter((s) => !formIds.has(s.id) && !hiddenInGallery.has(s.id) && match(s.id, s.input)),
    }
  }, [q, samples])

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      {banner}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <Typography.Title level={5} style={{ margin: 0 }}>配置文件（{rows.length}）</Typography.Title>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => { setQ(''); setGallery(true) }}>添加采集器</Button>
      </div>

      <Table<ConfRow> rowKey="id" dataSource={rows} pagination={rows.length > 30 ? { pageSize: 30 } : false} size="middle"
        locale={{ emptyText: <Empty description="还没有采集器配置文件，点击右上角“添加采集器”" /> }}
        columns={[
          { title: '采集器', render: (_, r) => {
              const input = r.inputs[0] ?? r.name.replace(/^.*\//, '').replace(/\.conf$/, '')
              return <Space><Logo name={sampleByInput.get(input)?.id ?? input} /><div><div style={{ fontWeight: 500 }}>{r.inputs.length ? r.inputs.map((i) => nameOf(sampleByInput.get(i)?.id ?? i)).join(' / ') : '—'}</div>
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>{r.name}</Typography.Text></div></Space>
            } },
          { title: '位置', render: (_, r) => <Typography.Text style={{ fontSize: 12.5 }} ellipsis={{ tooltip: r.location }}>{r.location}</Typography.Text>, ellipsis: true },
          { title: '状态', width: 250, render: (_, r) => <Space size={4} wrap>{r.tags}{r.error && <Tooltip title={r.error}><span className="tag-err">TOML 错误</span></Tooltip>}</Space> },
          { title: '', width: 140, align: 'right', render: (_, r) => r.editable ? (
              <Space>
                <Button size="small" icon={<EditOutlined />} onClick={() => startEdit(r)}>编辑</Button>
                <Popconfirm title={`删除 ${r.name}？`} description={deleteHint} okText="预览" onConfirm={() => onDelete(r)}>
                  <Button size="small" danger icon={<DeleteOutlined />} />
                </Popconfirm>
              </Space>) : <Button size="small" icon={<EyeOutlined />} onClick={() => startEdit(r)}>查看</Button> },
        ]} />

      <Modal open={gallery} title="添加采集器" footer={null} width={920} onCancel={() => setGallery(false)} styles={{ body: { maxHeight: '68vh', overflow: 'auto' } }}>
        <Input allowClear autoFocus prefix={<SearchOutlined />} placeholder="搜索采集器，如 mysql / kafka / 日志" value={q} onChange={(e) => setQ(e.target.value)} style={{ marginBottom: 16 }} />
        {galleryItems.form.length > 0 && <>
          <Typography.Text type="secondary">表单配置</Typography.Text>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(260px, 1fr))', gap: 10, margin: '8px 0 20px' }}>
            {galleryItems.form.map((s) => (
              <div key={s.id} className="tile" onClick={() => startNew(s.id)}>
                <Logo name={s.id} size={36} />
                <div style={{ minWidth: 0 }}><div style={{ fontWeight: 600 }}>{s.title}</div><div className="tile-desc">{s.desc}</div></div>
              </div>
            ))}
          </div>
        </>}
        {galleryItems.raw.length > 0 && <>
          <Typography.Text type="secondary">TOML 配置（基于官方示例编辑）</Typography.Text>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(190px, 1fr))', gap: 10, marginTop: 8 }}>
            {galleryItems.raw.map((s) => (
              <div key={s.id} className="tile" onClick={() => startNew(s.id)}>
                <Logo name={s.id} size={28} />
                <div style={{ minWidth: 0 }}><div style={{ fontWeight: 500 }}>{nameOf(s.id)}</div><div className="tile-desc">{s.input}</div></div>
              </div>
            ))}
          </div>
        </>}
        {!galleryItems.form.length && !galleryItems.raw.length && <Empty description="没有匹配的采集器" />}
      </Modal>

      <Drawer open={!!ed} width={900} onClose={() => setEd(null)} destroyOnHidden maskClosable={false}
        title={ed && <Space><Logo name={ed.id} /><span>{!ed.row ? '添加' : readOnly ? '查看' : '编辑'} {nameOf(ed.id)}</span></Space>}
        extra={<Space><Button onClick={() => setEd(null)}>{readOnly ? '关闭' : '取消'}</Button>{!readOnly && <Button type="primary" onClick={submit}>预览并应用</Button>}</Space>}>
        {ed && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 14, height: '100%' }}>
            <div style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
              <Input addonBefore="文件名" value={ed.name} disabled={!!ed.row} style={{ maxWidth: 380 }} onChange={(e) => setEd({ ...ed, name: e.target.value.trim() })} />
              <Typography.Text type="secondary" style={{ fontSize: 12 }} ellipsis>{ed.row ? ed.row.location : newFileHint(ed.name)}</Typography.Text>
              <div style={{ flex: 1 }} />
              {!readOnly && <Segmented value={tab} options={[{ value: 'form', label: '表单', disabled: !ed.schema }, { value: 'toml', label: 'TOML' }]}
                onChange={(v) => { if (v === 'form' && parsed.error) return void message.error('TOML 有语法错误，修正后才能切回表单'); setTab(v as 'form' | 'toml') }} />}
            </div>
            {readOnly && <Alert type="info" showIcon message="只读" description={ed.row?.readonlyHint} />}
            {!readOnly && ed.schema?.note && <Alert type="info" showIcon message={ed.schema.note} />}

            {!readOnly && tab === 'form' && ed.schema && (
              parsed.error ? <Alert type="error" showIcon message="TOML 语法错误，请在 TOML 模式下修正" description={parsed.error} />
              : instances.length === 0
                ? <Empty description={`文件中没有 [[inputs.${ed.input}]] 配置段`}><Button onClick={() => setEd({ ...ed, content: generate(ed.schema!) })}>生成默认配置</Button></Empty>
                : <SchemaForm schema={ed.schema} instances={instances}
                    onEdit={(i, path, v) => setEd({ ...ed, content: editField(ed.content, parsed.doc!, ed.input, i, path, v) })}
                    onAdd={() => setEd({ ...ed, content: addInstance(ed.content, parsed.doc!, ed.schema!) })}
                    onRemove={(i) => setEd({ ...ed, content: removeInstance(ed.content, parsed.doc!, ed.input, i) })} />
            )}

            {(readOnly || tab === 'toml') && (
              <div style={{ flex: 1, minHeight: 320, display: 'flex', flexDirection: 'column', gap: 8 }}>
                {!readOnly && <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                  {parsed.error ? <Typography.Text type="danger" style={{ fontSize: 12 }}>语法错误：{parsed.error.split('\n')[0]}</Typography.Text>
                    : <Typography.Text type="success" style={{ fontSize: 12 }}>TOML 语法正确{instances.length ? `，${instances.length} 个 ${ed.input} 实例` : ''}</Typography.Text>}
                  <Button size="small" onClick={loadSample} disabled={!sampleById.get(ed.id) && !sampleByInput.get(ed.input)}>载入官方示例</Button>
                </div>}
                <div style={{ flex: 1, minHeight: 0 }}>
                  <TomlEditor value={ed.content} readOnly={readOnly} onChange={(v) => setEd((cur) => (cur ? { ...cur, content: v } : cur))} height="calc(100vh - 290px)" />
                </div>
              </div>
            )}
          </div>
        )}
      </Drawer>
    </Space>
  )
}
