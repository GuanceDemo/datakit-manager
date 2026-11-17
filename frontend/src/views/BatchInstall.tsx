import { useEffect, useMemo, useRef, useState } from 'react'
import { Alert, Button, Form, Input, Modal, Segmented, Space, Spin, Table, Tag, Tooltip, Typography, message } from 'antd'
import { CheckCircleFilled, CloseCircleFilled, CloudServerOutlined, DesktopOutlined } from '@ant-design/icons'
import { api, errMsg } from '../api'
import type { Cluster, Host, InstallDefaults } from '../types'
import { ImagePushHints } from '../components'

type Product = 'datakit' | 'operator'
interface Asset { key: string; kind: 'cluster' | 'host'; id: string; name: string; address: string }
interface Probe { loading: boolean; installed?: boolean; version?: string; error?: string; note?: string }
interface Run { state: 'waiting' | 'running' | 'ok' | 'failed'; message?: string }

const slug = (s: string) => s.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '') || 'default'

/** Run tasks with a small concurrency limit. */
async function pool<T>(items: T[], limit: number, fn: (item: T) => Promise<void>) {
  let next = 0
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, async () => {
    while (next < items.length) await fn(items[next++])
  }))
}

/** Pick assets, pick a product, install it on all of them. */
export function BatchInstallModal({ open, clusters, hosts, onClose }: { open: boolean; clusters: Cluster[]; hosts: Host[]; onClose: (changed: boolean) => void }) {
  const [product, setProduct] = useState<Product>('datakit')
  const [probes, setProbes] = useState<Record<string, Probe>>({})
  const [picked, setPicked] = useState<string[]>([])
  const [runs, setRuns] = useState<Record<string, Run>>({})
  const [busy, setBusy] = useState(false)
  const [finished, setFinished] = useState(false)
  const [defs, setDefs] = useState<InstallDefaults | null>(null)
  const [dataway, setDataway] = useState('')
  const [version, setVersion] = useState('')
  const [opVersion, setOpVersion] = useState('')
  const [source, setSource] = useState<'public' | 'private'>('public')
  const [registry, setRegistry] = useState('')
  const [hostOffline, setHostOffline] = useState(false)
  const session = useRef(0)

  const assets = useMemo<Asset[]>(() => [
    ...clusters.map((c) => ({ key: 'c:' + c.id, kind: 'cluster' as const, id: c.id, name: c.name, address: `${c.server.replace(/^https?:\/\//, '')} · ns ${c.namespace}` })),
    ...hosts.map((h) => ({ key: 'h:' + h.id, kind: 'host' as const, id: h.id, name: h.name, address: `${h.user}@${h.address}:${h.port}` })),
  ], [clusters, hosts])

  // probe every asset for the selected product
  useEffect(() => {
    if (!open) return
    const my = ++session.current
    setPicked([]); setRuns({}); setFinished(false)
    setProbes(Object.fromEntries(assets.map((a) => [a.key, { loading: true }])))
    const set = (key: string, p: Probe) => { if (session.current === my) setProbes((x) => ({ ...x, [key]: p })) }
    pool(assets, 4, async (a) => {
      try {
        if (a.kind === 'host') {
          if (product === 'operator') return set(a.key, { loading: false, note: 'Operator 只能装在 Kubernetes 集群' })
          const i = await api.HostInspect(a.id)
          set(a.key, { loading: false, installed: i.installed, version: i.version, note: i.root ? undefined : '没有 root 权限' })
        } else if (product === 'datakit') {
          const i = await api.Inspect(a.id)
          set(a.key, { loading: false, installed: i.found, version: i.found ? i.workload.version : undefined })
        } else {
          const s = await api.OperatorStatus(a.id)
          set(a.key, { loading: false, installed: s.installed, version: s.version })
        }
      } catch (e) { set(a.key, { loading: false, error: errMsg(e) }) }
    })
  }, [open, product, assets])

  useEffect(() => {
    if (!open) return
    setDataway(''); setSource('public'); setRegistry(''); setHostOffline(false); setProduct('datakit')
    api.InstallDefaults().then((d) => {
      setDefs(d); setOpVersion(d.operatorVersion); setVersion(d.datakitVersion)
      api.LatestVersions().then((v) => { if (v.datakit) setVersion(v.datakit) }).catch(() => {})
    }).catch(() => {})
  }, [open])

  const selectable = (a: Asset) => {
    const p = probes[a.key]
    return !!p && !p.loading && !p.error && p.installed === false && !p.note
  }
  const targets = assets.filter((a) => picked.includes(a.key) && selectable(a))
  const hasCluster = targets.some((a) => a.kind === 'cluster')
  const hasHost = targets.some((a) => a.kind === 'host')
  const reg = registry.trim().replace(/\/+$/, '')
  const priv = source === 'private'

  const installOne = async (a: Asset) => {
    if (!defs) throw new Error('默认参数未加载')
    if (a.kind === 'host') {
      await api.HostInstall(a.id, { dataway: dataway.trim(), offline: hostOffline, version: version.trim() })
      return 'DataKit 已安装'
    }
    if (product === 'datakit') {
      const image = `${priv ? reg : defs.datakitRepo}/datakit:${version.trim()}`
      const yaml = await api.RenderDataKitYAML(a.id, { image, dataway: dataway.trim(), clusterName: slug(a.name), enabledInputs: defs.enabledInputs, imagePullSecret: '' })
      const done = await api.ApplyYAML(a.id, yaml)
      return `已应用 ${done?.length ?? 0} 个资源`
    }
    const image = `${priv ? reg : defs.operatorRepo}/datakit-operator:${opVersion.trim()}`
    const yaml = await api.RenderOperatorYAML(a.id, { image, operatorRepo: priv ? reg : defs.operatorRepo, datakitRepo: priv ? reg : defs.datakitRepo, imagePullSecret: '' })
    const done = await api.ApplyYAML(a.id, yaml)
    return `已应用 ${done?.length ?? 0} 个资源`
  }

  const start = async () => {
    if (!targets.length) return void message.error('请先勾选要安装的资产')
    if (product === 'datakit' && !dataway.trim()) return void message.error('请填写 DataWay 地址')
    if (hasCluster && priv && !reg) return void message.error('请填写私有仓库地址')
    setBusy(true)
    setRuns(Object.fromEntries(targets.map((a) => [a.key, { state: 'waiting' as const }])))
    await pool(targets, 3, async (a) => {
      setRuns((r) => ({ ...r, [a.key]: { state: 'running' } }))
      try {
        const msg = await installOne(a)
        setRuns((r) => ({ ...r, [a.key]: { state: 'ok', message: msg } }))
      } catch (e) { setRuns((r) => ({ ...r, [a.key]: { state: 'failed', message: errMsg(e) } })) }
    })
    setBusy(false); setFinished(true)
  }

  const okCount = Object.values(runs).filter((r) => r.state === 'ok').length
  const failCount = Object.values(runs).filter((r) => r.state === 'failed').length
  const publicImages = defs ? (product === 'datakit' ? [`${defs.datakitRepo}/datakit:${version}`] : [`${defs.operatorRepo}/datakit-operator:${opVersion}`]) : []

  return (
    <Modal open={open} title="批量安装" width={980} maskClosable={false} closable={!busy} onCancel={() => !busy && onClose(okCount > 0)}
      styles={{ body: { maxHeight: '74vh', overflow: 'auto' } }}
      footer={finished
        ? <Button type="primary" onClick={() => onClose(okCount > 0)}>完成</Button>
        : <Space><Button disabled={busy} onClick={() => onClose(false)}>取消</Button>
            <Button type="primary" loading={busy} disabled={!targets.length} onClick={start}>一键安装{targets.length ? `（${targets.length}）` : ''}</Button></Space>}>
      <Space direction="vertical" size={14} style={{ width: '100%' }}>
        <Space size={16} wrap>
          <Typography.Text strong>安装产品</Typography.Text>
          <Segmented value={product} disabled={busy || finished} onChange={(v) => setProduct(v as Product)}
            options={[{ value: 'datakit', label: 'DataKit' }, { value: 'operator', label: 'DataKit Operator' }]} />
          <Typography.Text type="secondary">{product === 'datakit' ? '集群按官方 datakit.yaml 安装，主机用官方安装程序。' : '只适用于 Kubernetes 集群，固定安装在 datakit 命名空间。'}</Typography.Text>
        </Space>

        <Table<Asset> size="small" rowKey="key" dataSource={assets} pagination={assets.length > 12 ? { pageSize: 12 } : false}
          locale={{ emptyText: '还没有资产。先导入 kubeconfig 或 SSH 密钥。' }}
          rowSelection={{
            selectedRowKeys: picked, onChange: (keys) => setPicked(keys as string[]),
            getCheckboxProps: (a) => ({ disabled: busy || finished || !selectable(a) }),
          }}
          columns={[
            { title: '类型', width: 110, render: (_, a) => a.kind === 'cluster' ? <Tag icon={<CloudServerOutlined />} color="blue">K8s 集群</Tag> : <Tag icon={<DesktopOutlined />} color="geekblue">主机</Tag> },
            { title: '名称', dataIndex: 'name', ellipsis: true },
            { title: '地址', dataIndex: 'address', ellipsis: true, render: (t: string) => <Typography.Text type="secondary" style={{ fontSize: 12.5 }}>{t}</Typography.Text> },
            { title: '当前状态', width: 190, render: (_, a) => {
                const p = probes[a.key]
                if (!p || p.loading) return <Spin size="small" />
                if (p.error) return <Tooltip title={p.error}><Tag color="red">连接失败</Tag></Tooltip>
                if (p.installed === undefined && p.note) return <Tooltip title={p.note}><Tag>不适用</Tag></Tooltip>
                if (p.installed) return <Tag color="green">已安装 {p.version}</Tag>
                return p.note ? <Tooltip title={p.note}><Tag color="orange">未安装 · {p.note}</Tag></Tooltip> : <Tag color="orange">未安装</Tag>
              } },
            { title: '安装结果', width: 230, render: (_, a) => {
                const r = runs[a.key]
                if (!r) return null
                if (r.state === 'waiting') return <Typography.Text type="secondary">等待中</Typography.Text>
                if (r.state === 'running') return <Space size={6}><Spin size="small" />安装中…</Space>
                if (r.state === 'ok') return <Space size={6}><CheckCircleFilled style={{ color: '#0e9f6e' }} />{r.message}</Space>
                return <Tooltip title={<pre style={{ margin: 0, whiteSpace: 'pre-wrap', maxHeight: 300, overflow: 'auto' }}>{r.message}</pre>} overlayStyle={{ maxWidth: 560 }}>
                  <Space size={6}><CloseCircleFilled style={{ color: '#dc2626' }} /><Typography.Text type="danger" ellipsis style={{ maxWidth: 180 }}>{r.message}</Typography.Text></Space></Tooltip>
              } },
          ]} />
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>已经安装的资产不可选，批量安装不会覆盖它们现有的配置。</Typography.Text>

        {finished
          ? <Alert type={failCount ? 'warning' : 'success'} showIcon message={`安装结束：成功 ${okCount}，失败 ${failCount}`}
              description={failCount ? '把鼠标移到失败的行上可以看到完整原因。' : 'Pod / 服务启动需要一点时间，稍后在各资产页面查看状态。'} />
          : (
            <Form layout="horizontal" labelCol={{ flex: '150px' }} labelAlign="left" colon={false} disabled={busy}>
              {product === 'datakit' && <>
                <Form.Item label="DataWay 地址" required extra="所有选中的资产都上报到这个工作空间。集群名称（ENV_CLUSTER_NAME_K8S）取各集群的显示名称。" style={{ marginBottom: 14 }}>
                  <Input value={dataway} placeholder="https://openway.guance.com?token=tkn_xxxxxxxx" onChange={(e) => setDataway(e.target.value)} />
                </Form.Item>
                <Form.Item label="DataKit 版本" style={{ marginBottom: 14 }}><Input value={version} style={{ width: 220 }} onChange={(e) => setVersion(e.target.value.trim())} /></Form.Item>
              </>}
              {product === 'operator' && <Form.Item label="Operator 版本" style={{ marginBottom: 14 }}><Input value={opVersion} style={{ width: 220 }} onChange={(e) => setOpVersion(e.target.value.trim())} /></Form.Item>}
              {(hasCluster || product === 'operator') && <>
                <Form.Item label="集群镜像来源" style={{ marginBottom: 14 }}>
                  <Segmented value={source} onChange={(v) => setSource(v as 'public' | 'private')}
                    options={[{ value: 'public', label: '官方仓库' }, { value: 'private', label: '私有仓库（集群无法访问外网）' }]} />
                </Form.Item>
                {priv && <Form.Item label="私有仓库地址" required style={{ marginBottom: 14 }}><Input value={registry} placeholder="harbor.example.com/datakit" onChange={(e) => setRegistry(e.target.value)} /></Form.Item>}
              </>}
              {hasHost && product === 'datakit' && (
                <Form.Item label="主机安装方式" style={{ marginBottom: 14 }} extra={hostOffline ? '安装包由本机下载后通过 SSH 上传到每台主机。' : '主机自行从 static.guance.com 下载安装。'}>
                  <Segmented value={hostOffline ? 'offline' : 'online'} onChange={(v) => setHostOffline(v === 'offline')}
                    options={[{ value: 'online', label: '在线' }, { value: 'offline', label: '离线上传（主机无法访问外网）' }]} />
                </Form.Item>)}
            </Form>
          )}
        {!finished && priv && (hasCluster || product === 'operator') && <ImagePushHints images={publicImages} registry={registry} />}
        {!finished && priv && product === 'operator' && <Typography.Text type="secondary" style={{ fontSize: 12 }}>Operator 注入用到的探针、logfwd、flameshot 镜像也需要推送，完整清单见单个集群的 Operator 安装向导。</Typography.Text>}
      </Space>
    </Modal>
  )
}
