import { useEffect, useMemo, useState } from 'react'
import { Alert, Button, Card, Descriptions, Empty, Modal, Select, Space, Spin, Statistic, Table, Tag, Tooltip, Typography } from 'antd'
import { CheckCircleFilled, CloseCircleFilled, FileTextOutlined, ReloadOutlined, SyncOutlined } from '@ant-design/icons'
import { api, errMsg } from '../api'
import type { Inspection, Pod, PodMetrics } from '../types'
import { LogView } from '../components'
import { MetricsPanels, enabledCount } from './MetricsPanels'
import { fmtBytes, fmtDuration } from '../util'

export function Overview({ clusterId, ins, onRestart }: { clusterId: string; ins: Inspection; onRestart: () => void }) {
  const [metrics, setMetrics] = useState<PodMetrics[] | null>(null)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  const [logs, setLogs] = useState<{ pod: string; text: string; loading: boolean } | null>(null)
  const w = ins.workload

  const load = () => {
    setLoading(true); setErr('')
    api.Metrics(clusterId, '').then(setMetrics).catch((e) => setErr(errMsg(e))).finally(() => setLoading(false))
  }
  useEffect(() => { setMetrics(null); load() }, [clusterId, ins]) // eslint-disable-line

  const byPod = useMemo(() => new Map((metrics ?? []).map((m) => [m.pod, m])), [metrics])
  const okPods = (metrics ?? []).filter((m) => m.ok)
  const leader = okPods.find((m) => m.electedId)?.electedId
  const versions = [...new Set(okPods.map((m) => m.version).filter(Boolean))]

  const showLogs = (pod: string) => {
    setLogs({ pod, text: '', loading: true })
    api.PodLogs(clusterId, pod, w.container, 400).then((t) => setLogs({ pod, text: t || '（没有日志输出）', loading: false }))
      .catch((e) => setLogs({ pod, text: errMsg(e), loading: false }))
  }

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(170px, 1fr))', gap: 12 }}>
        <Card size="small"><Statistic title="DataKit 版本" value={versions.length ? versions.join(' / ') : w.version || '-'} valueStyle={{ fontSize: 22 }} /></Card>
        <Card size="small"><Statistic title="就绪 Pod" value={w.ready} suffix={`/ ${w.desired}`} valueStyle={{ color: w.ready === w.desired ? '#0e9f6e' : '#d97706', fontSize: 22 }} /></Card>
        <Card size="small"><Statistic title="工作负载" value={w.rolloutDone ? '运行中' : '滚动更新中'} valueStyle={{ fontSize: 22 }} prefix={w.rolloutDone ? <CheckCircleFilled style={{ color: '#0e9f6e' }} /> : <SyncOutlined spin />} /></Card>
        <Card size="small"><Statistic title="开启的采集器" value={enabledCount(metrics) || ins.enabledInputs.length} valueStyle={{ fontSize: 22 }} /></Card>
        <Card size="small"><Statistic title="配置持久化" value={ins.persistence.mode === 'configmap' ? 'ConfigMap' : ins.persistence.mode === 'volume' ? '目录卷' : '未配置'}
          valueStyle={{ fontSize: 22, color: ins.persistence.mode === 'none' ? '#d97706' : undefined }} /></Card>
      </div>

      <Card size="small" title="部署信息" extra={<Button size="small" icon={<ReloadOutlined />} onClick={onRestart}>重启 DataKit</Button>}>
        <Descriptions size="small" column={2} items={[
          { key: 'w', label: '工作负载', children: `${w.kind}/${w.name}（命名空间 ${ins.namespace}）` },
          { key: 'i', label: '镜像', children: <Typography.Text copyable style={{ fontSize: 13 }}>{w.image}</Typography.Text> },
          { key: 'k', label: 'Kubernetes', children: ins.serverVersion || '-' },
          { key: 'h', label: 'HTTP 监听', children: `${w.httpLocal ? '127.0.0.1' : '0.0.0.0'}:${w.httpPort}${w.hostNetwork ? '（hostNetwork）' : ''}` },
          { key: 'e', label: '选举', children: leader ? <span>当选节点 <Tag color="blue">{leader}</Tag>{okPods.find((m) => m.electedId)?.electionNamespace && `命名空间 ${okPods.find((m) => m.electedId)!.electionNamespace}`}</span> : '未开启或尚未选出' },
          { key: 'p', label: '配置持久化', children: ins.persistence.summary },
        ]} />
      </Card>

      {err && <Alert type="error" showIcon message="读取 DataKit 指标失败" description={err} />}

      <Card size="small" title={`Pod（${ins.pods.length}）`} extra={<Button size="small" icon={<ReloadOutlined />} loading={loading} onClick={load}>刷新指标</Button>}>
        <Table<Pod> size="small" rowKey="name" dataSource={ins.pods} pagination={ins.pods.length > 15 ? { pageSize: 15 } : false}
          columns={[
            { title: '节点', dataIndex: 'node', render: (n: string, p) => <span>{n}{leader && byPod.get(p.name)?.hostname === leader && <Tag color="blue" style={{ marginLeft: 6 }}>选举 Leader</Tag>}</span> },
            { title: 'Pod', dataIndex: 'name' },
            { title: '状态', render: (_, p) => p.ready ? <Tag color="green">Running</Tag> : <Tooltip title={p.reason}><Tag color={p.phase === 'Running' ? 'orange' : 'red'}>{p.reason?.split(':')[0] || p.phase}</Tag></Tooltip> },
            { title: '重启', dataIndex: 'restarts', width: 60 },
            { title: '版本', render: (_, p) => byPod.get(p.name)?.version || '-' },
            { title: '运行时长', render: (_, p) => fmtDuration(byPod.get(p.name)?.uptimeSec ?? 0) },
            { title: 'CPU', render: (_, p) => { const m = byPod.get(p.name); return m?.ok ? `${m.cpuUsage.toFixed(1)}%` : '-' } },
            { title: '内存', render: (_, p) => fmtBytes(byPod.get(p.name)?.memRss ?? 0) },
            { title: '指标', render: (_, p) => { const m = byPod.get(p.name); return !m ? (loading ? <Spin size="small" /> : '-') : m.ok ? <CheckCircleFilled style={{ color: '#0e9f6e' }} /> : <Tooltip title={m.error}><CloseCircleFilled style={{ color: '#dc2626' }} /></Tooltip> } },
            { title: '', width: 70, render: (_, p) => <Button size="small" type="text" icon={<FileTextOutlined />} onClick={() => showLogs(p.name)}>日志</Button> },
          ]} />
      </Card>

      <MetricsPanels metrics={metrics} loading={loading} leader={leader}
        emptyHint={'无法读取 DataKit 指标。环境变量中声明的采集器：' + (ins.enabledInputs.join(', ') || '无')} />

      <Modal open={!!logs} title={`日志 — ${logs?.pod}`} width={1000} footer={null} onCancel={() => setLogs(null)}>
        {logs?.loading ? <Spin style={{ display: 'block', margin: 40 }} /> : <LogView value={logs?.text ?? ''} />}
      </Modal>
    </Space>
  )
}
