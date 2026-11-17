import { useMemo, useState } from 'react'
import { Card, Empty, Select, Space, Spin, Table, Tag, Tooltip, Typography } from 'antd'
import type { FeedStat, PodMetrics } from '../types'
import { Logo } from '../components'
import { ago, categoryName, fmtNum } from '../util'

interface InputRow {
  name: string
  instances: number
  pods: { pod: string; node: string; feeds: FeedStat[] }[]
  categories: string[]
  feeds: number
  points: number
  errors: number
  lastFeed: number
  avgMs: number
  lastError?: string
}

function aggregate(metrics: PodMetrics[]): InputRow[] {
  const rows = new Map<string, InputRow>()
  const row = (name: string) => {
    let r = rows.get(name)
    if (!r) { r = { name, instances: 0, pods: [], categories: [], feeds: 0, points: 0, errors: 0, lastFeed: 0, avgMs: 0 }; rows.set(name, r) }
    return r
  }
  for (const pm of metrics) {
    if (!pm.ok) continue
    for (const i of pm.inputs) row(i.name).instances += i.instances
    const byName = new Map<string, FeedStat[]>()
    for (const f of pm.feeds) byName.set(f.name, [...(byName.get(f.name) ?? []), f])
    for (const [name, fs] of byName) {
      const r = row(name)
      r.pods.push({ pod: pm.pod, node: pm.node, feeds: fs })
      for (const f of fs) {
        if (f.category && !r.categories.includes(f.category)) r.categories.push(f.category)
        r.feeds += f.feeds; r.points += f.points; r.errors += f.errors
        r.lastFeed = Math.max(r.lastFeed, f.lastFeed)
        r.avgMs = Math.max(r.avgMs, f.avgCollectMs)
      }
    }
    for (const e of pm.errors) if (e.source && rows.has(e.source)) rows.get(e.source)!.lastError = e.error
  }
  return [...rows.values()].sort((a, b) => a.name.localeCompare(b.name))
}

/** Collectors with their write status, DataWay upload and last errors, from DataKit's own /metrics. */
export function MetricsPanels({ metrics, loading, emptyHint, leader }: { metrics: PodMetrics[] | null; loading: boolean; emptyHint: string; leader?: string }) {
  const [podFilter, setPodFilter] = useState<string>()
  const okPods = (metrics ?? []).filter((m) => m.ok)
  const shown = useMemo(() => (metrics ?? []).filter((m) => !podFilter || m.pod === podFilter), [metrics, podFilter])
  const rows = useMemo(() => aggregate(shown), [shown])
  const now = Date.now() / 1000
  const uploads = useMemo(() => {
    const m = new Map<string, { category: string; total: number; ok: number; failed: number }>()
    for (const pm of shown) for (const u of pm.uploads) {
      const x = m.get(u.category) ?? { category: u.category, total: 0, ok: 0, failed: 0 }
      x.total += u.total; x.ok += u.ok; x.failed += u.failed; m.set(u.category, x)
    }
    return [...m.values()]
  }, [shown])
  const errors = shown.flatMap((pm) => pm.errors.map((e) => ({ ...e, pod: pm.pod, node: pm.node })))
  const status = (r: InputRow) => {
    if (!r.feeds) return <Tag>暂无数据</Tag>
    const stale = r.lastFeed > 0 && now - r.lastFeed > 600
    return <Space size={4}>
      <Tag color={stale ? 'orange' : 'green'}>{stale ? '写入停滞' : '写入正常'}</Tag>
      {r.errors > 0 && <Tooltip title={r.lastError ?? `累计 ${r.errors} 次错误`}><Tag color="red">错误 {fmtNum(r.errors)}</Tag></Tooltip>}
    </Space>
  }
  const multi = (metrics ?? []).length > 1
  return (
    <>
      <Card size="small" title="采集器与写入状态"
        extra={multi && <Select allowClear size="small" style={{ width: 260 }} placeholder="全部 Pod 汇总" value={podFilter} onChange={setPodFilter}
          options={okPods.map((m) => ({ value: m.pod, label: `${m.node}${m.hostname === leader ? '（Leader）' : ''}` }))} />}>
        {loading && !metrics ? <Spin style={{ display: 'block', margin: 24 }} /> : rows.length === 0
          ? <Empty description={okPods.length ? '没有采集器数据' : emptyHint} />
          : <Table<InputRow> size="small" rowKey="name" dataSource={rows} pagination={false}
              expandable={{ rowExpandable: (r) => r.pods.length > 0, expandedRowRender: (r) => (
                <Table size="small" pagination={false} rowKey={(x) => x.pod + x.category}
                  dataSource={r.pods.flatMap((p) => p.feeds.map((f) => ({ ...f, pod: p.pod, node: p.node })))}
                  columns={[
                    { title: '节点', dataIndex: 'node' },
                    { title: '数据类型', dataIndex: 'category', render: (c: string) => categoryName[c] ?? c },
                    { title: '写入次数', dataIndex: 'feeds', render: fmtNum },
                    { title: '数据点', dataIndex: 'points', render: fmtNum },
                    { title: '错误', dataIndex: 'errors', render: fmtNum },
                    { title: '最近写入', dataIndex: 'lastFeed', render: (t: number) => ago(t, now) },
                  ]} />) }}
              columns={[
                { title: '采集器', render: (_, r) => <Space><Logo name={r.name} size={22} /><span style={{ fontWeight: 500 }}>{r.name}</span></Space> },
                { title: '实例', dataIndex: 'instances', width: 60, render: (n: number) => n || '-' },
                { title: '运行 Pod', width: 90, render: (_, r) => r.pods.length || '-' },
                { title: '数据类型', render: (_, r) => r.categories.map((c) => <Tag key={c}>{categoryName[c] ?? c}</Tag>) },
                { title: '写入次数', dataIndex: 'feeds', render: fmtNum, sorter: (a, b) => a.feeds - b.feeds },
                { title: '数据点', dataIndex: 'points', render: fmtNum, sorter: (a, b) => a.points - b.points },
                { title: '最近写入', dataIndex: 'lastFeed', render: (t: number) => ago(t, now) },
                { title: '采集耗时', dataIndex: 'avgMs', render: (v: number) => (v ? (v >= 1000 ? `${(v / 1000).toFixed(2)} s` : `${v.toFixed(0)} ms`) : '-') },
                { title: '状态', render: (_, r) => status(r) },
              ]} />}
      </Card>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16 }}>
        <Card size="small" title="上报 DataWay">
          <Table size="small" rowKey="category" pagination={false} dataSource={uploads} locale={{ emptyText: '暂无上报数据' }}
            columns={[
              { title: '数据类型', dataIndex: 'category', render: (c: string) => categoryName[c] ?? c },
              { title: '总数据点', dataIndex: 'total', render: fmtNum },
              { title: '成功', dataIndex: 'ok', render: fmtNum },
              { title: '成功率', render: (_, u) => { const r = u.total ? (u.ok / u.total) * 100 : 0; return <Tag color={r >= 99 ? 'green' : r >= 90 ? 'orange' : 'red'}>{r.toFixed(1)}%</Tag> } },
            ]} />
        </Card>
        <Card size="small" title="最近错误">
          <Table size="small" rowKey={(e) => e.pod + e.source + e.category} pagination={errors.length > 6 ? { pageSize: 6 } : false} dataSource={errors} locale={{ emptyText: '没有错误' }}
            columns={[
              { title: '来源', dataIndex: 'source', width: 130 },
              { title: '节点', dataIndex: 'node', width: 130, ellipsis: true },
              { title: '错误', dataIndex: 'error', render: (t: string) => <Typography.Text style={{ fontSize: 12 }} ellipsis={{ tooltip: t }}>{t}</Typography.Text> },
            ]} />
        </Card>
      </div>

    </>
  )
}

export function enabledCount(metrics: PodMetrics[] | null): number {
  return aggregate((metrics ?? []).filter((m) => m.ok)).filter((r) => r.instances > 0).length
}
