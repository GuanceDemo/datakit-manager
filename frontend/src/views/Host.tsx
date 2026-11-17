import { useCallback, useEffect, useMemo, useState } from 'react'
import { Alert, Button, Card, Descriptions, Form, Input, Modal, Result, Segmented, Select, Space, Spin, Statistic, Tabs, Tag, Typography, message } from 'antd'
import { BugOutlined, CheckCircleFilled, CloseCircleFilled, FileTextOutlined, ReloadOutlined } from '@ant-design/icons'
import { api, errMsg } from '../api'
import type { Host, HostInfo, PodMetrics, Sample } from '../types'
import { CodeEditor, LogView, Logo } from '../components'
import { defaultableInputs } from '../envCatalog'
import { fmtBytes, fmtDuration } from '../util'
import { ConfManager, type ConfRow } from './ConfManager'
import { MetricsPanels, enabledCount } from './MetricsPanels'
import type { ApplyRequest } from './ApplyModal'
import { BugReportModal } from './BugReport'

/** Install (or upgrade) DataKit on one host with the official installer. */
export function HostInstallModal({ host, open, upgrade, onClose }: { host: Host; open: boolean; upgrade?: boolean; onClose: (installed: boolean) => void }) {
  const [dataway, setDataway] = useState('')
  const [inputs, setInputs] = useState<string[]>([])
  const [offline, setOffline] = useState(false)
  const [version, setVersion] = useState('')
  const [busy, setBusy] = useState(false)
  const [log, setLog] = useState<{ ok: boolean; text: string } | null>(null)
  useEffect(() => {
    if (!open) return
    setLog(null); setBusy(false); setOffline(false); setInputs([])
    api.LatestVersions().then((v) => setVersion(v.datakit ?? '')).catch(() => {})
  }, [open])

  const run = async () => {
    if (!upgrade && !dataway.trim()) return void message.error('请填写 DataWay 地址')
    setBusy(true)
    try {
      const text = await api.HostInstall(host.id, { dataway: dataway.trim(), defInputs: inputs.join(','), offline, version: version.trim(), upgrade: !!upgrade })
      setLog({ ok: true, text })
    } catch (e) { setLog({ ok: false, text: errMsg(e) }) } finally { setBusy(false) }
  }

  return (
    <Modal open={open} width={820} maskClosable={false} closable={!busy} title={<Space><Logo name="dk" />{upgrade ? '升级' : '安装'} DataKit — {host.name}</Space>}
      onCancel={() => !busy && onClose(!!log?.ok)}
      footer={log ? <Button type="primary" onClick={() => onClose(log.ok)}>{log.ok ? '完成' : '关闭'}</Button>
        : <Space><Button disabled={busy} onClick={() => onClose(false)}>取消</Button><Button type="primary" loading={busy} onClick={run}>{upgrade ? '开始升级' : '开始安装'}</Button></Space>}>
      {log ? (
        <Space direction="vertical" style={{ width: '100%' }}>
          <Alert type={log.ok ? 'success' : 'error'} showIcon message={log.ok ? (upgrade ? '升级完成' : '安装完成') : '安装失败'} />
          <LogView value={log.text.slice(-6000)} height="46vh" />
        </Space>
      ) : busy ? (
        <div style={{ textAlign: 'center', padding: 48 }}><Spin size="large" /><div style={{ marginTop: 16 }}>正在{offline ? '下载安装包、上传并' : ''}执行官方安装程序，通常需要 1～5 分钟，请不要关闭…</div></div>
      ) : (
        <Form layout="horizontal" labelCol={{ flex: '150px' }} labelAlign="left" colon={false}>
          {!upgrade && <>
            <Form.Item label="DataWay 地址" required extra="在观测云控制台「集成 → DataKit」中可以复制，包含工作空间 token。">
              <Input value={dataway} placeholder="https://openway.guance.com?token=tkn_xxxxxxxx" onChange={(e) => setDataway(e.target.value)} />
            </Form.Item>
            <Form.Item label="默认开启的采集器" extra="留空使用安装程序的默认值（主机基础采集器）。">
              <Select mode="tags" value={inputs} onChange={setInputs} tokenSeparators={[',', ' ']} placeholder="可选" options={defaultableInputs.map((o) => ({ value: o, label: o }))} />
            </Form.Item>
          </>}
          <Form.Item label="安装方式">
            <Segmented value={offline ? 'offline' : 'online'} onChange={(v) => setOffline(v === 'offline')}
              options={[{ value: 'online', label: '在线（主机可访问外网）' }, { value: 'offline', label: '离线上传（主机无法访问外网）' }]} />
          </Form.Item>
          {offline && <>
            <Form.Item label="DataKit 版本" extra="留空使用最新版本。"><Input value={version} style={{ width: 240 }} onChange={(e) => setVersion(e.target.value)} /></Form.Item>
            <Alert type="info" showIcon message="离线上传方式" description="安装包（约 100～200 MB）由运行本工具的这台机器从 static.guance.com 下载并缓存，再通过 SSH 上传到主机，用官方安装程序的离线模式安装。本机需要能访问外网。" />
          </>}
          {!offline && <Typography.Text type="secondary">将在主机上执行官方安装脚本（static.guance.com/datakit-v2/install.sh），主机需要有 bash 和 curl。</Typography.Text>}
        </Form>
      )}
    </Modal>
  )
}

/** One Linux host: DataKit status, collector configs, main config. */
export function HostView({ host, samples, refreshKey, onApply }: { host: Host; samples: Sample[]; refreshKey: number; onApply: (r: ApplyRequest) => void }) {
  const [info, setInfo] = useState<HostInfo | null>(null)
  const [err, setErr] = useState('')
  const [metrics, setMetrics] = useState<PodMetrics[] | null>(null)
  const [mLoading, setMLoading] = useState(false)
  const [tab, setTab] = useState('overview')
  const [install, setInstall] = useState<'install' | 'upgrade' | null>(null)
  const [logs, setLogs] = useState<{ text: string; loading: boolean } | null>(null)
  const [bugReport, setBugReport] = useState(false)
  const [main, setMain] = useState('')

  const loadMetrics = useCallback(() => {
    setMLoading(true)
    api.HostMetrics(host.id).then(setMetrics).catch(() => setMetrics([])).finally(() => setMLoading(false))
  }, [host.id])
  const load = useCallback(() => {
    setErr('')
    api.HostInspect(host.id).then((i) => { setInfo(i); setMain(i.mainConf ?? ''); if (i.installed) loadMetrics() }).catch((e) => setErr(errMsg(e)))
  }, [host.id, loadMetrics])
  useEffect(() => { setInfo(null); setMetrics(null); setTab('overview'); load() }, [host.id]) // eslint-disable-line
  useEffect(() => { if (refreshKey) load() }, [refreshKey]) // eslint-disable-line

  const hostApply = (title: string, upserts: { path: string; content: string }[], deletes: string[], diffs: ApplyRequest['diffs']) => onApply({
    title, change: {}, diffs,
    custom: {
      applyLabel: '应用并重启 DataKit',
      preview: () => api.HostPreview(host.id, { upserts, deletes }),
      apply: () => api.HostApply(host.id, { upserts, deletes }),
      rollout: () => api.HostStatus(host.id),
    },
  })

  const rows = useMemo<ConfRow[]>(() => (info?.confFiles ?? []).map((f) => ({
    id: f.path, name: f.rel, location: f.path, content: f.content, inputs: f.inputs ?? [], error: f.error, editable: true,
    tags: <Tag color="green">已加载</Tag>,
  })), [info])

  if (err) return <Result status="error" title="无法连接主机" subTitle={<pre style={{ whiteSpace: 'pre-wrap', textAlign: 'left', maxWidth: 760, margin: '0 auto' }}>{err}</pre>} extra={<Button type="primary" onClick={load}>重试</Button>} />
  if (!info) return <div className="center"><Spin tip="正在连接主机…"><div style={{ width: 200, height: 80 }} /></Spin></div>

  const pm = metrics?.[0]
  const noRoot = !info.root && <Alert type="warning" showIcon style={{ marginBottom: 16 }} message={`用户 ${info.user} 不是 root，也没有免密 sudo`} description="可以查看状态，但安装和修改配置会失败。请换用 root，或给该用户配置免密 sudo（NOPASSWD）。" />

  if (!info.installed) return (
    <>
      {noRoot}
      <Result status="warning" title="这台主机还没有安装 DataKit" subTitle={`${info.hostname} · ${info.os || 'Linux'} · ${info.arch}`}
        extra={<Button type="primary" onClick={() => setInstall('install')}>安装 DataKit</Button>} />
      <HostInstallModal host={host} open={!!install} onClose={(ok) => { setInstall(null); if (ok) load() }} />
    </>
  )

  const showLogs = () => {
    setLogs({ text: '', loading: true })
    api.HostLogs(host.id, 400).then((t) => setLogs({ text: t || '（没有日志输出）', loading: false })).catch((e) => setLogs({ text: errMsg(e), loading: false }))
  }

  const overview = (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(170px, 1fr))', gap: 12 }}>
        <Card size="small"><Statistic title="DataKit 版本" value={info.version || '-'} valueStyle={{ fontSize: 22 }} /></Card>
        <Card size="small"><Statistic title="服务状态" value={info.running ? '运行中' : '未运行'} valueStyle={{ fontSize: 22, color: info.running ? '#0e9f6e' : '#dc2626' }}
          prefix={info.running ? <CheckCircleFilled /> : <CloseCircleFilled />} /></Card>
        <Card size="small"><Statistic title="运行时长" value={fmtDuration(pm?.uptimeSec ?? 0)} valueStyle={{ fontSize: 22 }} /></Card>
        <Card size="small"><Statistic title="CPU / 内存" value={pm?.ok ? `${pm.cpuUsage.toFixed(1)}% / ${fmtBytes(pm.memRss)}` : '-'} valueStyle={{ fontSize: 22 }} /></Card>
        <Card size="small"><Statistic title="开启的采集器" value={enabledCount(metrics) || info.defaultInputs.length} valueStyle={{ fontSize: 22 }} /></Card>
      </div>
      <Card size="small" title="主机信息" extra={<Space>
        <Button size="small" icon={<FileTextOutlined />} onClick={showLogs}>日志</Button>
        <Button size="small" icon={<BugOutlined />} onClick={() => setBugReport(true)}>导出 Bug Report</Button>
        <Button size="small" onClick={() => setInstall('upgrade')}>升级</Button>
        <Button size="small" icon={<ReloadOutlined />} onClick={() => hostApply('重启 DataKit', [], [], [])}>重启 DataKit</Button>
      </Space>}>
        <Descriptions size="small" column={2} items={[
          { key: 'h', label: '主机名', children: info.hostname },
          { key: 'a', label: 'SSH', children: `${host.user}@${host.address}:${host.port}${info.root ? '' : '（无 root 权限）'}` },
          { key: 'o', label: '系统', children: `${info.os || 'Linux'} · ${info.arch} · ${info.kernel}` },
          { key: 'l', label: 'HTTP 监听', children: info.httpListen },
          { key: 'd', label: '安装目录', children: info.installDir },
          { key: 'i', label: '默认开启的采集器', children: info.defaultInputs.join(', ') || '-' },
        ]} />
      </Card>
      {pm && !pm.ok && <Alert type="warning" showIcon message="读取 DataKit 指标失败" description={pm.error} />}
      <MetricsPanels metrics={metrics} loading={mLoading} emptyHint="无法读取 DataKit 指标" />
    </Space>
  )

  const dirtyMain = main !== (info.mainConf ?? '')
  return (
    <>
      {noRoot}
      <Tabs activeKey={tab} onChange={setTab} items={[
        { key: 'overview', label: '概览', children: overview },
        { key: 'collectors', label: `采集器配置（${info.confFiles.length}）`, children: (
          <ConfManager rows={rows} samples={samples} resetKey={info} subdirs
            banner={<Alert showIcon type="info" message={`下表是主机上 ${info.confDir} 目录里实际生效的全部采集器配置。修改后会重启 datakit 服务。`} />}
            newFileHint={(name) => `保存到 ${info.confDir}/${name}`}
            deleteHint="删除后会重启 datakit 服务。"
            onSave={(row, name, content, original) => hostApply(`${row ? '修改' : '新增'}采集器配置 ${name}`, [{ path: row ? row.id : `${info.confDir}/${name}`, content }], [],
              [{ title: row ? row.id : `${info.confDir}/${name}`, before: original, after: content }])}
            onDelete={(row) => hostApply(`删除采集器配置 ${row.name}`, [], [row.id], [{ title: row.id, before: row.content, after: '' }])} />
        ) },
        { key: 'main', label: '主配置', children: (
          <Space direction="vertical" size={12} style={{ width: '100%' }}>
            <Alert showIcon type="info" message={`${info.mainConfPath}：DataWay 地址、全局标签、默认开启的采集器、HTTP 监听等 DataKit 自身设置。`} />
            <CodeEditor lang="toml" value={main} onChange={setMain} height="calc(100vh - 330px)" />
            <div className="sticky-bar">
              <span>{dirtyMain ? '主配置已修改，尚未应用' : '没有修改'}</span>
              <Space>
                <Button disabled={!dirtyMain} onClick={() => setMain(info.mainConf ?? '')}>放弃修改</Button>
                <Button type="primary" disabled={!dirtyMain} onClick={() => hostApply('修改主配置 datakit.conf', [{ path: info.mainConfPath, content: main }], [], [{ title: info.mainConfPath, before: info.mainConf ?? '', after: main }])}>预览并应用</Button>
              </Space>
            </div>
          </Space>
        ) },
      ]} />
      <HostInstallModal host={host} open={!!install} upgrade={install === 'upgrade'} onClose={(ok) => { setInstall(null); if (ok) load() }} />
      <BugReportModal target={bugReport ? { kind: 'host', id: host.id, name: host.name } : null} onClose={() => setBugReport(false)} />
      <Modal open={!!logs} title={`DataKit 日志 — ${host.name}`} width={1000} footer={null} onCancel={() => setLogs(null)}>
        {logs?.loading ? <Spin style={{ display: 'block', margin: 40 }} /> : <LogView value={logs?.text ?? ''} />}
      </Modal>
    </>
  )
}

/** Import Linux hosts: one SSH private key, one or more hosts. */
export function ImportHostsModal({ open, web, onClose }: { open: boolean; web: boolean; onClose: (added?: Host[]) => void }) {
  const [keyPath, setKeyPath] = useState('')
  const [keyContent, setKeyContent] = useState('')
  const [passphrase, setPassphrase] = useState('')
  const [hosts, setHosts] = useState('')
  const [busy, setBusy] = useState(false)
  const [errors, setErrors] = useState<string[]>([])
  useEffect(() => { if (open) { setKeyPath(''); setKeyContent(''); setPassphrase(''); setHosts(''); setErrors([]) } }, [open])

  const pick = async () => {
    try { const p = await api.PickSSHKey(); if (p) { setKeyPath(p); setKeyContent('') } } catch (e) { message.error(errMsg(e)) }
  }
  const onFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0]; e.target.value = ''
    if (f) { setKeyContent(await f.text()); setKeyPath(f.name) }
  }
  const submit = async () => {
    if (!keyPath && !keyContent.trim()) return void message.error('请选择或粘贴 SSH 私钥')
    if (!hosts.trim()) return void message.error('请至少填写一台主机')
    setBusy(true); setErrors([])
    try {
      const res = await api.ImportHosts({ keyPath: keyContent ? '' : keyPath, keyContent, passphrase, hosts: hosts.split('\n') })
      const added = res.added ?? []
      if ((res.errors ?? []).length) { setErrors(res.errors); if (added.length) message.success(`已导入 ${added.length} 台主机`) }
      if (!(res.errors ?? []).length) onClose(added)
      else if (added.length) setHosts(res.errors.map((x) => x.split('：')[0]).join('\n'))
    } catch (e) { message.error(errMsg(e)) } finally { setBusy(false) }
  }

  return (
    <Modal open={open} title="导入 SSH 密钥（Linux 主机）" width={720} maskClosable={false} onCancel={() => onClose(errors.length ? [] : undefined)}
      okText="连接并导入" okButtonProps={{ loading: busy }} onOk={submit}>
      <Form layout="vertical">
        <Form.Item label="SSH 私钥" required extra="私钥保存到磁盘时使用 AES-256 加密，不会以明文落盘。">
          <Space.Compact style={{ width: '100%' }}>
            <Input readOnly value={keyPath} placeholder={web ? '选择私钥文件，或在下面粘贴' : '例如 ~/.ssh/id_rsa'} />
            {web ? <Button onClick={() => document.getElementById('ssh-key-file')?.click()}>选择文件</Button> : <Button onClick={pick}>选择文件</Button>}
          </Space.Compact>
          <input id="ssh-key-file" type="file" style={{ display: 'none' }} onChange={onFile} />
          <Input.TextArea value={keyContent} onChange={(e) => { setKeyContent(e.target.value); if (e.target.value) setKeyPath('') }} autoSize={{ minRows: 2, maxRows: 5 }}
            placeholder={'或直接粘贴私钥内容\n-----BEGIN OPENSSH PRIVATE KEY-----'} style={{ marginTop: 8, fontFamily: 'ui-monospace, Menlo, monospace', fontSize: 12 }} />
        </Form.Item>
        <Form.Item label="私钥口令" extra="私钥没有口令保护时留空。">
          <Input.Password value={passphrase} autoComplete="new-password" onChange={(e) => setPassphrase(e.target.value)} style={{ width: 320 }} />
        </Form.Item>
        <Form.Item label="主机列表" required extra="每行一台：[用户@]地址[:端口] [显示名称]。用户默认 root，端口默认 22。需要 root 或免密 sudo 才能安装和修改 DataKit。">
          <Input.TextArea value={hosts} onChange={(e) => setHosts(e.target.value)} autoSize={{ minRows: 4, maxRows: 10 }}
            placeholder={'10.0.0.11\nubuntu@10.0.0.12:2222 订单服务-1\nroot@db1.example.com 生产 MySQL 主库'} style={{ fontFamily: 'ui-monospace, Menlo, monospace', fontSize: 13 }} />
        </Form.Item>
      </Form>
      {errors.length > 0 && <Alert type="error" showIcon message={`${errors.length} 台主机导入失败`} description={errors.map((e, i) => <div key={i}>{e}</div>)} />}
    </Modal>
  )
}
