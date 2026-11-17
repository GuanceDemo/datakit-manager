import { useCallback, useEffect, useRef, useState } from 'react'
import { App as AntApp, Button, Checkbox, Dropdown, Empty, Form, Input, Modal, Result, Spin, Table, Tabs, Tag, Tooltip, Typography } from 'antd'
import { AppstoreAddOutlined, CloudServerOutlined, DesktopOutlined, EditOutlined, FolderOpenOutlined, KeyOutlined, MoreOutlined, PlusOutlined, ReloadOutlined, SettingOutlined } from '@ant-design/icons'
import { api, errMsg } from './api'
import type { AppInfo, Cluster, ContextInfo, Host, Inspection, Sample } from './types'
import { Logo } from './components'
import { Overview } from './views/Overview'
import { Collectors } from './views/Collectors'
import { EnvVars } from './views/EnvVars'
import { ApplyModal, type ApplyRequest } from './views/ApplyModal'
import { Operator } from './views/Operator'
import { InstallDrawer } from './views/Install'
import { HostView, ImportHostsModal } from './views/Host'
import { BatchInstallModal } from './views/BatchInstall'

function ImportModal({ path, contexts, onClose }: { path: string; contexts: ContextInfo[]; onClose: (added?: Cluster[]) => void }) {
  const { message } = AntApp.useApp()
  const [picked, setPicked] = useState<string[]>([])
  const [ns, setNs] = useState('datakit')
  const [busy, setBusy] = useState(false)
  useEffect(() => { setPicked(contexts.map((c) => c.name)); setNs('datakit') }, [contexts])
  const submit = async () => {
    setBusy(true)
    try { onClose(await api.ImportClusters(path, picked, ns.trim() || 'datakit')) }
    catch (e) { message.error(errMsg(e)) }
    finally { setBusy(false) }
  }
  return (
    <Modal open={contexts.length > 0} title="导入集群" width={760} onCancel={() => onClose()} okText={`导入 ${picked.length} 个集群`} okButtonProps={{ disabled: !picked.length, loading: busy }} onOk={submit}>
      <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>{path}</Typography.Paragraph>
      <Table<ContextInfo> size="small" rowKey="name" pagination={false} dataSource={contexts} scroll={{ y: 300 }}
        columns={[
          { title: <Checkbox checked={picked.length === contexts.length} indeterminate={picked.length > 0 && picked.length < contexts.length}
              onChange={(e) => setPicked(e.target.checked ? contexts.map((c) => c.name) : [])} />, width: 44,
            render: (_, c) => <Checkbox checked={picked.includes(c.name)} onChange={(e) => setPicked(e.target.checked ? [...picked, c.name] : picked.filter((n) => n !== c.name))} /> },
          { title: 'Context', dataIndex: 'name', render: (n: string, c) => <span>{n}{c.current && <Tag style={{ marginLeft: 6 }}>current</Tag>}</span> },
          { title: 'API Server', dataIndex: 'server', ellipsis: true },
          { title: '用户', dataIndex: 'user', ellipsis: true, width: 160 },
        ]} />
      <Form layout="vertical" style={{ marginTop: 16 }}>
        <Form.Item label="DataKit 所在命名空间" extra="导入后可以针对每个集群单独修改。" style={{ marginBottom: 0 }}>
          <Input value={ns} onChange={(e) => setNs(e.target.value)} style={{ width: 260 }} />
        </Form.Item>
      </Form>
    </Modal>
  )
}

function SettingsModal({ cluster, onClose }: { cluster: Cluster | null; onClose: (saved: boolean) => void }) {
  const { message } = AntApp.useApp()
  const [form] = Form.useForm()
  useEffect(() => { if (cluster) form.setFieldsValue({ name: cluster.name, namespace: cluster.namespace }) }, [cluster, form])
  return (
    <Modal open={!!cluster} title="集群设置" onCancel={() => onClose(false)} onOk={async () => {
      const v = await form.validateFields()
      try { await api.UpdateCluster(cluster!.id, v.name.trim(), v.namespace.trim()); onClose(true) } catch (e) { message.error(errMsg(e)) }
    }}>
      <Form form={form} layout="vertical">
        <Form.Item name="name" label="显示名称" rules={[{ required: true, whitespace: true }]}><Input /></Form.Item>
        <Form.Item name="namespace" label="DataKit 所在命名空间" rules={[{ required: true, pattern: /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/, message: '不是合法的命名空间名' }]}><Input /></Form.Item>
      </Form>
      {cluster && <Typography.Text type="secondary" style={{ fontSize: 12 }}>Context：{cluster.context}<br />API Server：{cluster.server}</Typography.Text>}
    </Modal>
  )
}

export default function App() {
  const { message, modal } = AntApp.useApp()
  const [clusters, setClusters] = useState<Cluster[]>([])
  const [sel, setSel] = useState<string>()
  const [ins, setIns] = useState<Inspection | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [samples, setSamples] = useState<Sample[]>([])
  const [imp, setImp] = useState<{ path: string; contexts: ContextInfo[] }>({ path: '', contexts: [] })
  const [settings, setSettings] = useState<Cluster | null>(null)
  const [request, setRequest] = useState<ApplyRequest | null>(null)
  const [tab, setTab] = useState('overview')
  const [info, setInfo] = useState<AppInfo | null>(null)
  const [installDk, setInstallDk] = useState(false)
  const [opKey, setOpKey] = useState(0)
  const fileInput = useRef<HTMLInputElement>(null)
  const [hosts, setHosts] = useState<Host[]>([])
  const [importHosts, setImportHosts] = useState(false)
  const [batch, setBatch] = useState(false)
  const [hostKey, setHostKey] = useState(0)
  const cluster = clusters.find((c) => c.id === sel)
  const host = hosts.find((h) => h.id === sel)
  const loadHosts = useCallback(async () => {
    const list = (await api.ListHosts()) ?? []
    setHosts(list)
    return list
  }, [])

  const loadClusters = useCallback(async () => {
    const list = (await api.ListClusters()) ?? []
    setClusters(list)
    return list
  }, [])

  useEffect(() => {
    Promise.all([loadClusters(), loadHosts()]).then(([cs, hs]) => setSel(cs[0]?.id ?? hs[0]?.id)).catch((e) => message.error(errMsg(e)))
    api.Samples().then((s) => setSamples(s ?? [])).catch(() => {})
    api.Info().then(setInfo).catch(() => {})
  }, []) // eslint-disable-line

  const inspect = useCallback((id: string, quiet = false) => {
    if (!quiet) { setIns(null); setError('') }
    setLoading(true)
    api.Inspect(id).then((r) => { setIns(r); setError('') }).catch((e) => setError(errMsg(e))).finally(() => setLoading(false))
  }, [])
  useEffect(() => { if (cluster) inspect(cluster.id) }, [sel, cluster?.namespace]) // eslint-disable-line

  const web = info?.mode === 'web'
  // desktop: native file dialog; web: upload the file content to the server
  const startImport = async () => {
    if (web) return void fileInput.current?.click()
    try {
      const path = await api.PickKubeconfig()
      if (!path) return
      setImp({ path, contexts: await api.ParseKubeconfig(path) })
    } catch (e) { message.error(errMsg(e)) }
  }
  const onFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0]
    e.target.value = ''
    if (!f) return
    try {
      const path = await api.UploadKubeconfig(await f.text())
      setImp({ path, contexts: await api.ParseKubeconfig(path) })
    } catch (err) { message.error(errMsg(err)) }
  }

  const remove = (c: Cluster) => modal.confirm({
    title: `移除集群 ${c.name}？`, content: '只会从本工具中移除，不会改动集群里的任何资源。', okButtonProps: { danger: true }, okText: '移除',
    onOk: async () => { await api.RemoveCluster(c.id); const l = await loadClusters(); if (sel === c.id) setSel(l[0]?.id ?? hosts[0]?.id) },
  })
  const removeHost = (h: Host) => modal.confirm({
    title: `移除主机 ${h.name}？`, content: '只会从本工具中移除并删除保存的密钥，不会改动主机上的 DataKit。', okButtonProps: { danger: true }, okText: '移除',
    onOk: async () => { await api.RemoveHost(h.id); const l = await loadHosts(); if (sel === h.id) setSel(clusters[0]?.id ?? l[0]?.id) },
  })
  const renameHost = (h: Host) => {
    let name = h.name
    modal.confirm({
      title: '重命名主机', icon: null, content: <Input defaultValue={h.name} onChange={(e) => { name = e.target.value }} />,
      onOk: async () => { if (name.trim()) { await api.UpdateHost(h.id, name.trim()); await loadHosts() } },
    })
  }

  return (
    <div className="shell">
      <aside className="sider">
        <div className="brand"><Logo name="dk" size={30} /><div>DataKit Manager<small>集群与主机采集配置管理{info?.version && ` · ${info.version}`}</small></div></div>
        <div className="cluster-list">
          {clusters.length + hosts.length === 0 && <div style={{ padding: 16, color: '#8b93a5', fontSize: 13 }}>还没有资产。导入 kubeconfig 或 SSH 密钥开始使用。</div>}
          {clusters.map((c) => (
            <div key={c.id} className={'cluster' + (c.id === sel ? ' active' : '')} onClick={() => { setSel(c.id); setTab('overview') }}>
              <CloudServerOutlined />
              <div className="meta"><div className="name">{c.name}</div><div className="server">{c.server.replace(/^https?:\/\//, '')} · {c.namespace}</div></div>
              <Dropdown trigger={['click']} menu={{ items: [
                { key: 's', label: '设置', icon: <SettingOutlined />, onClick: () => setSettings(c) },
                { key: 'r', label: '移除', danger: true, onClick: () => remove(c) },
              ] }}><MoreOutlined className="more" onClick={(e) => e.stopPropagation()} /></Dropdown>
            </div>
          ))}
          {hosts.map((h) => (
            <div key={h.id} className={'cluster' + (h.id === sel ? ' active' : '')} onClick={() => setSel(h.id)}>
              <DesktopOutlined />
              <div className="meta"><div className="name">{h.name}</div><div className="server">{h.user}@{h.address}{h.port !== 22 ? ':' + h.port : ''} · 主机</div></div>
              <Dropdown trigger={['click']} menu={{ items: [
                { key: 's', label: '重命名', icon: <EditOutlined />, onClick: () => renameHost(h) },
                { key: 'r', label: '移除', danger: true, onClick: () => removeHost(h) },
              ] }}><MoreOutlined className="more" onClick={(e) => e.stopPropagation()} /></Dropdown>
            </div>
          ))}
        </div>
        <div className="sider-foot">
          <Button type="primary" icon={<PlusOutlined />} onClick={startImport} block>导入 kubeconfig</Button>
          <Button icon={<KeyOutlined />} onClick={() => setImportHosts(true)} block ghost>导入 SSH 密钥（主机）</Button>
          <Button icon={<AppstoreAddOutlined />} onClick={() => setBatch(true)} block ghost>批量安装</Button>
          <input ref={fileInput} type="file" style={{ display: 'none' }} onChange={onFile} />
          {info && !web && <Tooltip title={info.dataDir}><div className="dir" onClick={() => api.OpenDataDir()}><FolderOpenOutlined /> 打开数据目录</div></Tooltip>}
          {web && <div className="dir" style={{ cursor: 'default' }}>Web 模式 · 凭据加密保存在服务端</div>}
        </div>
      </aside>

      <main className="main">
        {host ? (
          <>
            <div className="topbar">
              <h1>{host.name}</h1>
              <Tag>主机</Tag>
              <Typography.Text type="secondary" style={{ fontSize: 12 }} ellipsis>{host.user}@{host.address}:{host.port}</Typography.Text>
              <div style={{ flex: 1 }} />
              <Button icon={<ReloadOutlined />} onClick={() => setHostKey((k) => k + 1)}>刷新</Button>
            </div>
            <div className="content" style={{ paddingTop: 16 }}>
              <HostView key={host.id} host={host} samples={samples} refreshKey={hostKey} onApply={setRequest} />
            </div>
            <ApplyModal clusterId="" request={request} onClose={(applied) => { setRequest(null); if (applied) setHostKey((k) => k + 1) }} />
          </>
        ) : !cluster ? (
          <div className="center"><Empty description="导入 kubeconfig 管理集群里的 DataKit，或导入 SSH 密钥管理 Linux 主机上的 DataKit">
            <Button type="primary" icon={<PlusOutlined />} onClick={startImport}>导入 kubeconfig</Button>
            <Button icon={<KeyOutlined />} style={{ marginLeft: 12 }} onClick={() => setImportHosts(true)}>导入 SSH 密钥</Button>
          </Empty></div>
        ) : (
          <>
            <div className="topbar">
              <h1>{cluster.name}</h1>
              <Tooltip title="DataKit 所在命名空间，点击修改"><Tag style={{ cursor: 'pointer' }} onClick={() => setSettings(cluster)}>ns: {cluster.namespace}</Tag></Tooltip>
              <Typography.Text type="secondary" style={{ fontSize: 12 }} ellipsis>{cluster.server}</Typography.Text>
              <div style={{ flex: 1 }} />
              <Button icon={<ReloadOutlined />} loading={loading} onClick={() => inspect(cluster.id, !!ins)}>刷新</Button>
            </div>
            <div className="content">
              {error ? (
                <Result status="error" title="无法连接集群" subTitle={<pre style={{ whiteSpace: 'pre-wrap', textAlign: 'left', maxWidth: 760, margin: '0 auto' }}>{error}</pre>}
                  extra={<Button type="primary" onClick={() => inspect(cluster.id)}>重试</Button>} />
              ) : !ins ? (
                <div className="center"><Spin tip="正在检查 DataKit…"><div style={{ width: 200, height: 80 }} /></Spin></div>
              ) : !ins.found ? (
                <Result status="warning" title="没有找到 DataKit" subTitle={ins.message}
                  extra={[
                    <Button key="i" type="primary" onClick={() => setInstallDk(true)}>一键安装 DataKit</Button>,
                    <Button key="n" onClick={() => setSettings(cluster)}>修改命名空间</Button>,
                  ]} />
              ) : (
                <Tabs activeKey={tab} onChange={setTab} items={[
                  { key: 'overview', label: '概览', children: <Overview clusterId={cluster.id} clusterName={cluster.name} ins={ins} onRestart={() => setRequest({ title: '重启 DataKit', change: {}, diffs: [] })} /> },
                  { key: 'collectors', label: `采集器配置${ins.confFiles.length ? `（${ins.confFiles.length}）` : ''}`, children: <Collectors ins={ins} samples={samples} onApply={setRequest} /> },
                  { key: 'env', label: '环境变量', children: <EnvVars ins={ins} onApply={setRequest} /> },
                  { key: 'operator', label: 'Operator', children: <Operator key={opKey} cluster={cluster} onApply={setRequest} /> },
                ]} />
              )}
            </div>
            <ApplyModal clusterId={cluster.id} request={request} onClose={(applied) => { const custom = !!request?.custom; setRequest(null); if (applied) { if (custom) setOpKey((k) => k + 1); else inspect(cluster.id, true) } }} />
            <InstallDrawer kind="datakit" cluster={cluster} open={installDk} onClose={(done) => { setInstallDk(false); if (done) inspect(cluster.id) }} />
          </>
        )}
      </main>

      <ImportModal path={imp.path} contexts={imp.contexts} onClose={async (added) => {
        setImp({ path: '', contexts: [] })
        if (added?.length) { await loadClusters(); setSel(added[0].id); message.success(`已导入 ${added.length} 个集群`) }
      }} />
      <SettingsModal cluster={settings} onClose={async (saved) => { setSettings(null); if (saved) await loadClusters() }} />
      <ImportHostsModal open={importHosts} web={web} onClose={async (added) => {
        setImportHosts(false)
        if (added) { await loadHosts(); if (added.length) { setSel(added[0].id); message.success(`已导入 ${added.length} 台主机`) } }
      }} />
      <BatchInstallModal open={batch} clusters={clusters} hosts={hosts} onClose={(changed) => {
        setBatch(false)
        if (changed) { if (cluster) inspect(cluster.id); setHostKey((k) => k + 1) }
      }} />
    </div>
  )
}
