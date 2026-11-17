import { useEffect, useMemo, useState } from 'react'
import { Alert, Button, Checkbox, Collapse, Input, InputNumber, Modal, Space, Spin, Table, Tag, Typography } from 'antd'
import { BugOutlined, DownloadOutlined, SearchOutlined } from '@ant-design/icons'
import { api, errMsg } from '../api'
import type { BugReportFile, Pod } from '../types'
import { LogView } from '../components'
import { fmtBytes } from '../util'

export type BugReportTarget =
  | { kind: 'cluster'; id: string; name: string; container: string; pods: Pod[] }
  | { kind: 'host'; id: string; name: string }

/**
 * Export a DataKit bug report (`datakit debug --bug-report`). A report belongs to one DataKit
 * instance: for a cluster the user picks the pod (the node column helps to find it), a host
 * has exactly one.
 */
export function BugReportModal({ target, onClose }: { target: BugReportTarget | null; onClose: () => void }) {
  const [pod, setPod] = useState('')
  const [q, setQ] = useState('')
  const [noProfile, setNoProfile] = useState(false)
  const [nmetrics, setNmetrics] = useState<number | null>(3)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [file, setFile] = useState<BugReportFile | null>(null)
  const [saved, setSaved] = useState('')

  const pods = target?.kind === 'cluster' ? target.pods : []
  const usable = (p: Pod) => p.phase === 'Running'
  const openKey = target ? `${target.kind}:${target.id}` : ''
  useEffect(() => {
    if (!target) return
    setQ(''); setErr(''); setFile(null); setSaved(''); setBusy(false)
    const ok = target.kind === 'cluster' ? target.pods.filter(usable) : []
    setPod(ok.length === 1 ? ok[0].name : '')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [openKey])

  const shown = useMemo(() => {
    const k = q.trim().toLowerCase()
    return k ? pods.filter((p) => p.node.toLowerCase().includes(k) || p.name.toLowerCase().includes(k) || p.ip.includes(k)) : pods
  }, [pods, q])
  const nodes = useMemo(() => [...new Set(pods.map((p) => p.node).filter(Boolean))].sort(), [pods])

  const save = async (f: BugReportFile) => {
    try {
      const r = await api.SaveBugReport(f.file)
      setSaved(r === 'download' ? '已开始下载，请在浏览器的下载列表里查看。' : r ? `已保存到 ${r}` : '')
    } catch (e) { setErr(errMsg(e)) }
  }
  const run = async () => {
    if (!target) return
    setBusy(true); setErr(''); setFile(null); setSaved('')
    const o = { disableProfile: noProfile, nmetrics: nmetrics && nmetrics !== 3 ? nmetrics : 0 }
    try {
      const f = target.kind === 'cluster'
        ? await api.BugReport(target.id, pod, pods.find((p) => p.name === pod)?.node ?? '', target.container, o)
        : await api.HostBugReport(target.id, o)
      setFile(f); setBusy(false)
      await save(f)
    } catch (e) { setErr(errMsg(e)); setBusy(false) }
  }

  const chosen = pods.find((p) => p.name === pod)
  const ready = target?.kind === 'host' || !!pod
  return (
    <Modal open={!!target} width={860} maskClosable={false} closable={!busy} onCancel={() => !busy && onClose()}
      title={<Space><BugOutlined />导出 Bug Report — {target?.name}</Space>}
      footer={<Space>
        <Button disabled={busy} onClick={onClose}>{file ? '关闭' : '取消'}</Button>
        {file && <Button icon={<DownloadOutlined />} onClick={() => save(file)}>{saved ? '再次保存' : '保存文件'}</Button>}
        <Button type="primary" loading={busy} disabled={!ready} onClick={run}>{file ? '重新导出' : '一键导出'}</Button>
      </Space>}>
      <Typography.Paragraph type="secondary" style={{ marginBottom: 12 }}>
        在所选 DataKit 实例上执行 <Typography.Text code>datakit debug --bug-report</Typography.Text>，收集配置、日志、自身指标和 profile，打包成 zip 取回。
        {target?.kind === 'cluster' && ' Bug Report 是按实例收集的：请选择出问题的那个 Pod（可按节点筛选）。'}
      </Typography.Paragraph>

      {target?.kind === 'cluster' && <>
        <Input allowClear prefix={<SearchOutlined />} placeholder="按节点名 / Pod 名 / IP 筛选" value={q} onChange={(e) => setQ(e.target.value)} style={{ marginBottom: 8 }} disabled={busy} />
        <Table<Pod> size="small" rowKey="name" dataSource={shown} pagination={shown.length > 8 ? { pageSize: 8, showSizeChanger: false } : false}
          rowSelection={{ type: 'radio', selectedRowKeys: pod ? [pod] : [], onChange: (k) => setPod(String(k[0] ?? '')), getCheckboxProps: (p) => ({ disabled: busy || !usable(p) }) }}
          onRow={(p) => ({ onClick: () => { if (!busy && usable(p)) setPod(p.name) }, style: { cursor: usable(p) ? 'pointer' : 'not-allowed' } })}
          locale={{ emptyText: pods.length ? '没有匹配的 Pod' : '没有 DataKit Pod' }}
          columns={[
            { title: '节点', dataIndex: 'node', sorter: (a, b) => a.node.localeCompare(b.node), defaultSortOrder: 'ascend',
              filters: nodes.length > 1 && nodes.length <= 50 ? nodes.map((n) => ({ text: n, value: n })) : undefined, filterSearch: true, onFilter: (v, p) => p.node === v,
              render: (v: string) => <b>{v || '-'}</b> },
            { title: 'Pod', dataIndex: 'name', sorter: (a, b) => a.name.localeCompare(b.name) },
            { title: 'Pod IP', dataIndex: 'ip', width: 130, render: (v: string) => v || '-' },
            { title: '状态', dataIndex: 'phase', width: 150, render: (v: string, p) => <Tag color={usable(p) ? 'green' : 'orange'}>{p.reason || v}</Tag> },
            { title: '重启', dataIndex: 'restarts', width: 70 },
          ]} />
      </>}

      <Space size={24} wrap style={{ margin: '12px 0 4px' }}>
        <Checkbox checked={noProfile} disabled={busy} onChange={(e) => setNoProfile(e.target.checked)}>不采集 profile（降低对 DataKit 的影响）</Checkbox>
        <Space>指标采样次数<InputNumber size="small" min={1} max={60} value={nmetrics} disabled={busy} onChange={setNmetrics} style={{ width: 70 }} /></Space>
      </Space>
      <div style={{ color: '#8a8f98', fontSize: 12 }}>建议在问题出现时（如 CPU / 内存偏高）导出，指标和 profile 才有参考价值。DataKit 会自动遮盖 token、密码等，但文件里仍可能有敏感信息，外发前请检查。</div>

      {busy && <div style={{ marginTop: 16 }}><Spin /> <span style={{ marginLeft: 8 }}>正在{chosen ? ` ${chosen.node} / ${chosen.name} 上` : '主机上'}收集，通常需要 1 分钟左右，请不要关闭窗口…</span></div>}
      {err && <Alert style={{ marginTop: 16 }} type="error" showIcon message="导出失败" description={<pre style={{ margin: 0, whiteSpace: 'pre-wrap', fontSize: 12 }}>{err}</pre>} />}
      {file && <>
        <Alert style={{ marginTop: 16 }} type="success" showIcon message={`已导出 ${file.file}（${fmtBytes(file.size)}）`}
          description={saved || <>尚未保存到你选择的位置，点击“保存文件”。{file.path && <> 临时副本：<Typography.Text code copyable>{file.path}</Typography.Text>（7 天后自动清理）</>}</>} />
        {file.output && <Collapse size="small" style={{ marginTop: 8 }} items={[{ key: 'o', label: 'DataKit 输出', children: <LogView value={file.output} height="160px" /> }]} />}
      </>}
    </Modal>
  )
}
