import { useCallback, useEffect, useMemo, useState } from 'react'
import { Alert, Button, Card, Collapse, Descriptions, Empty, Form, Input, Modal, Result, Segmented, Select, Space, Spin, Switch, Table, Tag, Tooltip, Typography, message } from 'antd'
import { DeleteOutlined, FileTextOutlined, PlusOutlined, QuestionCircleOutlined, ReloadOutlined } from '@ant-design/icons'
import { api, errMsg } from '../api'
import type { Cluster, OperatorStatus, Pod } from '../types'
import { CodeEditor, Logo, LogView } from '../components'
import { InstallDrawer } from './Install'
import type { ApplyRequest } from './ApplyModal'

type Obj = Record<string, any>
type FType = 'string' | 'bool' | 'list' | 'select' | 'kv' | 'text' | 'resources'
interface RuleField { key: string; label: string; type: FType; options?: string[]; help?: string; placeholder?: string }
interface Section { path: string[]; title: string; logo: string; desc: string; fields: RuleField[]; blank: () => Obj; summary: (r: Obj) => string }

const selectors: RuleField[] = [
  { key: 'namespace_selectors', label: '命名空间', type: 'list', placeholder: 'default', help: '在这些命名空间里创建的 Pod 会被处理。' },
  { key: 'label_selectors', label: '标签选择器', type: 'list', placeholder: 'app=my-service', help: 'Pod 标签匹配时处理，写法同 kubectl 的 -l 参数。' },
]
const image: RuleField[] = [
  { key: 'image', label: '镜像', type: 'string', help: '集群无法访问外网时，改成你私有仓库里的镜像地址。' },
  { key: 'image_pull_policy', label: '拉取策略', type: 'select', options: ['Always', 'IfNotPresent', 'Never'] },
]
const envs: RuleField = { key: 'envs', label: '环境变量', type: 'kv', help: '注入到业务容器的环境变量，按顺序生效。值可以写 {fieldRef:metadata.name} 这样的引用。' }
const annotation: RuleField = { key: 'check_annotation', label: '仅处理带注解的 Pod', type: 'bool', help: '开启后，只有带对应注解的 Pod 才会注入。' }
const resources: RuleField = { key: 'resources', label: 'Sidecar 资源', type: 'resources' }
const sel = (r: Obj) => [...(r.namespace_selectors ?? []).map((n: string) => `ns:${n}`), ...(r.label_selectors ?? [])].join('，') || '未设置选择器'

const dkHost = (ns: string) => `datakit-service.${ns}.svc.cluster.local`
const sections = (ns: string): Section[] => [
  {
    path: ['admission_inject_v2', 'ddtraces'], title: 'DDTrace 探针注入', logo: 'ddtrace', desc: '给匹配的 Pod 自动注入 DDTrace 探针（initContainer + 环境变量），业务镜像无需改动。',
    fields: [{ key: 'language', label: '语言', type: 'select', options: ['java', 'python', 'js', 'php', 'dotnet'] }, ...selectors, annotation, ...image, envs],
    summary: (r) => `${r.language ?? '?'} · ${sel(r)}`,
    blank: () => ({ language: 'java', namespace_selectors: [], label_selectors: [], check_annotation: false, image: '', image_pull_policy: 'Always',
      envs: { DD_AGENT_HOST: dkHost(ns), DD_TRACE_AGENT_PORT: '9529', DD_SERVICE: "{fieldRef:metadata.labels['app']}" } }),
  },
  {
    path: ['admission_inject_v2', 'otels'], title: 'OpenTelemetry 探针注入', logo: 'opentelemetry', desc: '给匹配的 Pod 自动注入 OpenTelemetry 自动探针。',
    fields: [{ key: 'language', label: '语言', type: 'select', options: ['java', 'python', 'nodejs'] }, ...selectors, annotation, ...image, envs],
    summary: (r) => `${r.language ?? '?'} · ${sel(r)}`,
    blank: () => ({ language: 'java', namespace_selectors: [], label_selectors: [], check_annotation: false, image: '', image_pull_policy: 'Always',
      envs: { OTEL_EXPORTER_OTLP_ENDPOINT: `http://${dkHost(ns)}:9529/otel`, OTEL_SERVICE_NAME: "{fieldRef:metadata.labels['app']}" } }),
  },
  {
    path: ['admission_inject_v2', 'logfwds'], title: 'Logfwd 日志 Sidecar', logo: 'logfwdserver', desc: '给匹配的 Pod 注入 logfwd Sidecar，采集容器内的日志文件。',
    fields: [...selectors, ...image, envs, resources,
      { key: 'log_configs', label: '日志采集配置', type: 'text', help: 'JSON 数组，同 datakit/logfwd 注解的写法。留空则使用 Pod 注解里的配置。', placeholder: '[{"disable": false, "source": "app", "path": "/var/log/app/*.log"}]' },
      { key: 'log_volume_paths', label: '日志目录', type: 'list', placeholder: '/var/log/app', help: '业务容器内需要共享给 Sidecar 的日志目录。' }],
    summary: sel,
    blank: () => ({ namespace_selectors: [], label_selectors: [], image: '', image_pull_policy: 'Always',
      envs: { LOGFWD_DATAKIT_HOST: '{fieldRef:status.hostIP}', LOGFWD_DATAKIT_PORT: '9533', LOGFWD_POD_NAME: '{fieldRef:metadata.name}', LOGFWD_POD_NAMESPACE: '{fieldRef:metadata.namespace}' },
      resources: { requests: { cpu: '100m', memory: '128Mi' }, limits: { cpu: '200m', memory: '256Mi' } }, log_configs: '', log_volume_paths: [] }),
  },
  {
    path: ['admission_inject_v2', 'flameshots'], title: 'Flameshot 性能剖析', logo: 'profile', desc: '给匹配的 Pod 注入 flameshot Sidecar，在资源使用超过阈值时自动采集 Profiling。',
    fields: [...selectors, ...image, envs, resources,
      { key: 'processes', label: '进程规则', type: 'text', help: 'JSON 数组，指定要剖析的进程和触发条件。' },
      { key: 'enable_prometheus_annotations', label: '添加 Prometheus 注解', type: 'bool' }],
    summary: sel,
    blank: () => ({ namespace_selectors: [], label_selectors: [], image: '', image_pull_policy: 'Always',
      envs: { FLAMESHOT_DATAKIT_ADDR: `http://datakit-service.${ns}:9529/profiling/v1/input`, FLAMESHOT_MONITOR_INTERVAL: '10s' },
      resources: { requests: { cpu: '100m', memory: '128Mi' }, limits: { cpu: '200m', memory: '256Mi' } }, processes: '', enable_prometheus_annotations: true }),
  },
  {
    path: ['admission_inject_v2', 'profilers'], title: 'Profiler 注入', logo: 'pyroscope', desc: '给匹配的 Pod 注入持续 Profiling 采集器。',
    fields: [{ key: 'language', label: '语言', type: 'select', options: ['java', 'python', 'golang'] }, ...selectors, annotation, ...image, envs],
    summary: (r) => `${r.language ?? '?'} · ${sel(r)}`,
    blank: () => ({ language: 'java', namespace_selectors: [], label_selectors: [], check_annotation: false, image: '', image_pull_policy: 'Always', envs: {} }),
  },
  {
    path: ['admission_mutate', 'loggings'], title: '日志采集注解', logo: 'logging', desc: '给匹配的 Pod 自动加上 datakit/logs 注解，由 DataKit 按该配置采集容器日志。',
    fields: [...selectors, { key: 'config', label: 'datakit/logs 配置', type: 'text', help: 'JSON 数组，等同于手工写在 Pod 上的 datakit/logs 注解。', placeholder: '[{"disable": false, "type": "stdout", "source": "app", "service": "app"}]' }],
    summary: sel,
    blank: () => ({ namespace_selectors: [], label_selectors: [], config: '' }),
  },
]

const pretty = (o: unknown) => JSON.stringify(o, null, 4) + '\n'
const getIn = (o: Obj, path: string[]): any => path.reduce((c, k) => (c == null ? undefined : c[k]), o as any)
function setIn(o: Obj, path: string[], v: unknown): Obj {
  if (!path.length) return v as Obj
  const [k, ...rest] = path
  return { ...(o ?? {}), [k]: setIn((o ?? {})[k], rest, v) }
}

/** Ordered key/value editor: env order matters to the operator ($(VAR) references). */
function KVList({ value, onChange }: { value: Obj; onChange: (v: Obj) => void }) {
  const rows = Object.entries(value ?? {})
  const [draft, setDraft] = useState(['', ''])
  const put = (i: number, k: string, v: string) => onChange(Object.fromEntries(rows.map((r, j) => (j === i ? [k, v] : r)).filter(([key]) => key !== '')))
  return (
    <div style={{ display: 'grid', gap: 6 }}>
      {rows.map(([k, v], i) => (
        <div key={i} style={{ display: 'flex', gap: 6 }}>
          <Input value={k} style={{ width: 280, fontFamily: 'ui-monospace, Menlo, monospace', fontSize: 12.5 }} onChange={(e) => put(i, e.target.value, String(v))} />
          <Input value={String(v)} style={{ fontFamily: 'ui-monospace, Menlo, monospace', fontSize: 12.5 }} onChange={(e) => put(i, k, e.target.value)} />
          <Button icon={<DeleteOutlined />} onClick={() => onChange(Object.fromEntries(rows.filter((_, j) => j !== i)))} />
        </div>
      ))}
      <div style={{ display: 'flex', gap: 6 }}>
        <Input placeholder="变量名" style={{ width: 280 }} value={draft[0]} onChange={(e) => setDraft([e.target.value.trim(), draft[1]])} />
        <Input placeholder="值" value={draft[1]} onChange={(e) => setDraft([draft[0], e.target.value])} />
        <Button icon={<PlusOutlined />} disabled={!draft[0]} onClick={() => { onChange({ ...(value ?? {}), [draft[0]]: draft[1] }); setDraft(['', '']) }} />
      </div>
    </div>
  )
}

function RuleControl({ f, value, onChange }: { f: RuleField; value: any; onChange: (v: unknown) => void }) {
  switch (f.type) {
    case 'bool': return <Switch checked={!!value} onChange={onChange} />
    case 'select': return <Select value={value} style={{ width: 240 }} options={[...new Set([...(f.options ?? []), ...(value ? [value] : [])])].map((o) => ({ value: o, label: o }))} onChange={onChange} />
    case 'list': return <Select mode="tags" open={false} suffixIcon={null} value={value ?? []} tokenSeparators={[',', ' ']} placeholder={f.placeholder ? `${f.placeholder}，回车添加` : '输入后回车添加'} onChange={onChange} />
    case 'kv': return <KVList value={value ?? {}} onChange={onChange} />
    case 'text': {
      let bad = false
      if (typeof value === 'string' && value.trim()) { try { JSON.parse(value) } catch { bad = true } }
      return <>
        <Input.TextArea value={value ?? ''} status={bad ? 'error' : undefined} placeholder={f.placeholder} autoSize={{ minRows: 2, maxRows: 10 }} style={{ fontFamily: 'ui-monospace, Menlo, monospace', fontSize: 12.5 }} onChange={(e) => onChange(e.target.value)} />
        {bad && <Typography.Text type="danger" style={{ fontSize: 12 }}>不是合法的 JSON</Typography.Text>}
      </>
    }
    case 'resources': {
      const r = value ?? {}
      const cell = (kind: 'requests' | 'limits', res: 'cpu' | 'memory', ph: string) =>
        <Input addonBefore={`${kind === 'requests' ? '请求' : '上限'} ${res === 'cpu' ? 'CPU' : '内存'}`} placeholder={ph} value={r[kind]?.[res] ?? ''} style={{ width: 230 }}
          onChange={(e) => onChange({ ...r, [kind]: { ...(r[kind] ?? {}), [res]: e.target.value } })} />
      return <Space wrap>{cell('requests', 'cpu', '100m')}{cell('requests', 'memory', '128Mi')}{cell('limits', 'cpu', '200m')}{cell('limits', 'memory', '256Mi')}</Space>
    }
    default: return <Input value={value ?? ''} placeholder={f.placeholder} onChange={(e) => onChange(e.target.value)} />
  }
}

function SectionCard({ s, rules, onChange }: { s: Section; rules: Obj[]; onChange: (rules: Obj[]) => void }) {
  return (
    <Card size="small" title={<Space><Logo name={s.logo} size={20} />{s.title}<Tag>{rules.length} 条规则</Tag></Space>}
      extra={<Button size="small" icon={<PlusOutlined />} onClick={() => onChange([...rules, s.blank()])}>添加规则</Button>}>
      <Typography.Paragraph type="secondary" style={{ marginBottom: rules.length ? 12 : 0 }}>{s.desc}</Typography.Paragraph>
      {rules.length > 0 && (
        <Collapse size="small" items={rules.map((r, i) => ({
          key: i, label: <span>规则 {i + 1}　<Typography.Text type="secondary">{s.summary(r)}</Typography.Text></span>,
          extra: <Button size="small" type="text" danger icon={<DeleteOutlined />} onClick={(e) => { e.stopPropagation(); onChange(rules.filter((_, j) => j !== i)) }} />,
          children: (
            <Form layout="horizontal" labelCol={{ flex: '170px' }} labelAlign="left" colon={false}>
              {s.fields.map((f) => (
                <Form.Item key={f.key} style={{ marginBottom: 12 }} extra={<code style={{ fontSize: 11, color: '#9ca3af' }}>{f.key}</code>}
                  label={<span>{f.label}{f.help && <Tooltip title={f.help}><QuestionCircleOutlined style={{ marginLeft: 6, color: '#9ca3af' }} /></Tooltip>}</span>}>
                  <RuleControl f={f} value={r[f.key]} onChange={(v) => onChange(rules.map((x, j) => (j === i ? { ...x, [f.key]: v } : x)))} />
                </Form.Item>
              ))}
            </Form>),
        }))} />
      )}
    </Card>
  )
}

export function Operator({ cluster, onApply }: { cluster: Cluster; onApply: (r: ApplyRequest) => void }) {
  const [st, setSt] = useState<OperatorStatus | null>(null)
  const [err, setErr] = useState('')
  const [text, setText] = useState('')
  const [mode, setMode] = useState<'form' | 'json'>('form')
  const [install, setInstall] = useState(false)
  const [logs, setLogs] = useState<{ pod: string; text: string; loading: boolean } | null>(null)

  const load = useCallback(() => {
    setErr('')
    api.OperatorStatus(cluster.id).then((s) => {
      setSt(s)
      let t = s.config ?? ''
      try { t = pretty(JSON.parse(t)) } catch { /* keep raw text, JSON mode will show the error */ }
      setText(t); setMode(s.configError ? 'json' : 'form')
    }).catch((e) => setErr(errMsg(e)))
  }, [cluster.id])
  useEffect(() => { setSt(null); load() }, [load, cluster.namespace])

  const parsed = useMemo<{ doc?: Obj; error?: string }>(() => {
    try { const d = JSON.parse(text); return d && typeof d === 'object' && !Array.isArray(d) ? { doc: d } : { error: '配置必须是一个 JSON 对象' } }
    catch (e: any) { return { error: String(e?.message ?? e) } }
  }, [text])
  const original = useMemo(() => { try { return pretty(JSON.parse(st?.config ?? '')) } catch { return st?.config ?? '' } }, [st])
  const dirty = text !== original
  const secs = useMemo(() => sections(cluster.namespace), [cluster.namespace])
  const legacy = parsed.doc?.admission_inject && Object.keys(parsed.doc.admission_inject).length > 0

  const submit = () => {
    if (parsed.error) return void message.error('JSON 语法错误：' + parsed.error)
    const next = pretty(parsed.doc)
    onApply({
      title: '修改 DataKit Operator 配置', change: {},
      diffs: [{ title: `ConfigMap ${st?.configMap} / ${st?.configKey}`, before: original, after: next }],
      custom: {
        applyLabel: '应用并重启 Operator',
        preview: async () => ({ actions: [`ConfigMap ${st?.configMap} 更新 ${st?.configKey}`, `重启 Deployment/${st?.name}（Operator 只在启动时读取配置）`],
          warnings: ['新配置只对之后创建的 Pod 生效；已经在运行的业务 Pod 需要重建才会注入。'] }),
        apply: () => api.ApplyOperatorConfig(cluster.id, next),
        rollout: () => api.OperatorRollout(cluster.id),
      },
    })
  }

  const showLogs = (p: Pod) => {
    setLogs({ pod: p.name, text: '', loading: true })
    api.PodLogs(cluster.id, p.name, '', 400).then((t) => setLogs({ pod: p.name, text: t || '（没有日志输出）', loading: false })).catch((e) => setLogs({ pod: p.name, text: errMsg(e), loading: false }))
  }

  if (err) return <Result status="error" title="读取 Operator 状态失败" subTitle={err} extra={<Button onClick={load}>重试</Button>} />
  if (!st) return <Spin style={{ display: 'block', margin: 60 }} />

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      {!st.installed ? (
        <Result icon={<Logo name="container" size={64} />} title="集群中没有安装 DataKit Operator"
          subTitle="Operator 通过 Admission Webhook 给业务 Pod 自动注入 APM 探针、日志 Sidecar 和 Profiling 采集器，业务镜像和 YAML 都不用改。"
          extra={<Button type="primary" onClick={() => setInstall(true)}>安装 DataKit Operator</Button>} />
      ) : (
        <>
          <Card size="small" title="Operator 状态" extra={<Space><Button size="small" onClick={() => setInstall(true)}>重新安装 / 升级</Button><Button size="small" icon={<ReloadOutlined />} onClick={load}>刷新</Button></Space>}>
            <Descriptions size="small" column={2} items={[
              { key: 'v', label: '版本', children: st.version || '-' },
              { key: 'r', label: '副本', children: <Tag color={st.ready >= st.desired ? 'green' : 'orange'}>{st.ready} / {st.desired} 就绪</Tag> },
              { key: 'i', label: '镜像', children: <Typography.Text copyable style={{ fontSize: 13 }}>{st.image}</Typography.Text> },
              { key: 'w', label: 'Webhook', children: st.webhook ? <Tag color="green">已注册</Tag> : <Tag color="red">未找到 MutatingWebhookConfiguration</Tag> },
              { key: 'n', label: '命名空间', children: st.namespace },
              { key: 'c', label: '配置来源', children: `ConfigMap ${st.configMap} / ${st.configKey}` },
            ]} />
            {st.pods.length > 0 && <Table<Pod> size="small" rowKey="name" pagination={false} dataSource={st.pods} style={{ marginTop: 8 }}
              columns={[
                { title: 'Pod', dataIndex: 'name' }, { title: '节点', dataIndex: 'node' },
                { title: '状态', render: (_, p) => p.ready ? <Tag color="green">Running</Tag> : <Tooltip title={p.reason}><Tag color="red">{p.reason?.split(':')[0] || p.phase}</Tag></Tooltip> },
                { title: '重启', dataIndex: 'restarts', width: 70 },
                { title: '', width: 80, render: (_, p) => <Button size="small" type="text" icon={<FileTextOutlined />} onClick={() => showLogs(p)}>日志</Button> },
              ]} />}
          </Card>

          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
            <Typography.Title level={5} style={{ margin: 0 }}>注入配置</Typography.Title>
            <Segmented value={mode} options={[{ value: 'form', label: '表单' }, { value: 'json', label: 'JSON' }]}
              onChange={(v) => { if (v === 'form' && parsed.error) return void message.error('JSON 有语法错误，修正后才能切回表单'); setMode(v as 'form' | 'json') }} />
          </div>
          {st.configError && <Alert type="error" showIcon message={st.configError} />}
          {legacy && <Alert type="warning" showIcon message="配置里还有旧版 admission_inject 段" description="表单只编辑新版 admission_inject_v2；旧段会原样保留，可在 JSON 模式下查看或迁移。" />}

          {mode === 'form' && parsed.doc && <>
            <Card size="small" title="基础">
              <Form layout="inline">
                <Form.Item label="日志级别"><Select style={{ width: 160 }} value={parsed.doc.log_level ?? 'info'} options={['info', 'debug'].map((o) => ({ value: o, label: o }))}
                  onChange={(v) => setText(pretty({ ...parsed.doc, log_level: v }))} /></Form.Item>
                <Form.Item label="监听地址"><Input style={{ width: 200 }} value={parsed.doc.server_listen ?? ''} onChange={(e) => setText(pretty({ ...parsed.doc, server_listen: e.target.value }))} /></Form.Item>
              </Form>
            </Card>
            {secs.map((s) => <SectionCard key={s.title} s={s} rules={Array.isArray(getIn(parsed.doc!, s.path)) ? getIn(parsed.doc!, s.path) : []}
              onChange={(rules) => setText(pretty(setIn(parsed.doc!, s.path, rules)))} />)}
          </>}
          {mode === 'json' && <>
            {parsed.error ? <Typography.Text type="danger" style={{ fontSize: 12 }}>语法错误：{parsed.error}</Typography.Text> : <Typography.Text type="success" style={{ fontSize: 12 }}>JSON 语法正确</Typography.Text>}
            <CodeEditor lang="json" value={text} onChange={setText} height="60vh" />
          </>}

          <div className="sticky-bar">
            <span>{dirty ? '配置已修改，尚未应用' : '没有修改'}</span>
            <Space>
              <Button disabled={!dirty} onClick={() => setText(original)}>放弃修改</Button>
              <Button type="primary" disabled={!dirty} onClick={submit}>预览并应用</Button>
            </Space>
          </div>
        </>
      )}

      <InstallDrawer kind="operator" cluster={cluster} open={install} onClose={(done) => { setInstall(false); if (done) load() }} />
      <Modal open={!!logs} title={`日志 — ${logs?.pod}`} width={1000} footer={null} onCancel={() => setLogs(null)}>
        {logs?.loading ? <Spin style={{ display: 'block', margin: 40 }} /> : <LogView value={logs?.text ?? ''} />}
      </Modal>
    </Space>
  )
}
