import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Collapse, Modal, Progress, Result, Space, Spin, Steps, Tag, Typography } from 'antd'
import { api, errMsg } from '../api'
import type { ApplyResult, ChangeSet, PlanView, RolloutStatus } from '../types'
import { DiffView } from '../components'

export interface ApplyRequest {
  title: string
  /** DataKit change; ignored when `custom` is given */
  change: ChangeSet
  /** other targets (e.g. the operator) bring their own preview / apply / progress calls */
  custom?: {
    preview: () => Promise<PlanView>
    apply: () => Promise<ApplyResult>
    rollout: () => Promise<RolloutStatus>
    applyLabel?: string
  }
  /** content diffs to show (TOML files, env list...) */
  diffs: { title: string; before: string; after: string }[]
}

type Phase = 'preview' | 'applying' | 'rolling' | 'done' | 'error'

/** Preview → apply → watch the rolling restart. */
export function ApplyModal({ clusterId, request, onClose }: { clusterId: string; request: ApplyRequest | null; onClose: (applied: boolean) => void }) {
  const [phase, setPhase] = useState<Phase>('preview')
  const [plan, setPlan] = useState<PlanView | null>(null)
  const [error, setError] = useState('')
  const [result, setResult] = useState<ApplyResult | null>(null)
  const [rollout, setRollout] = useState<RolloutStatus | null>(null)
  const timer = useRef<number>()
  const applied = useRef(false)

  useEffect(() => {
    if (!request) return
    setPhase('preview'); setPlan(null); setError(''); setResult(null); setRollout(null); applied.current = false
    const preview = request.custom ? request.custom.preview() : api.PreviewChanges(clusterId, request.change)
    preview.then(setPlan).catch((e) => { setError(errMsg(e)); setPhase('error') })
    return () => window.clearTimeout(timer.current)
  }, [request, clusterId])

  const poll = () => {
    const status = request?.custom ? request.custom.rollout() : api.RolloutStatus(clusterId)
    status.then((rs) => {
      setRollout(rs)
      if (rs.done) setPhase('done')
      else timer.current = window.setTimeout(poll, 2000)
    }).catch(() => { timer.current = window.setTimeout(poll, 3000) })
  }

  const apply = async () => {
    if (!request) return
    setPhase('applying')
    try {
      const res = await (request.custom ? request.custom.apply() : api.ApplyChanges(clusterId, request.change))
      applied.current = true
      setResult(res); setPhase('rolling')
      timer.current = window.setTimeout(poll, 1500)
    } catch (e) {
      setError(errMsg(e)); setPhase('error')
    }
  }

  const close = () => { window.clearTimeout(timer.current); onClose(applied.current) }
  const step = phase === 'preview' || phase === 'error' ? 0 : phase === 'applying' ? 1 : phase === 'rolling' ? 2 : 3
  const badPods = rollout?.pods.filter((p) => !p.ready && p.reason) ?? []

  return (
    <Modal open={!!request} title={request?.title} width={860} onCancel={close} maskClosable={false} destroyOnHidden
      footer={
        phase === 'preview' ? (
          <Space>
            <Button onClick={close}>取消</Button>
            <Button type="primary" disabled={!plan} onClick={apply}>{request?.custom?.applyLabel ?? '应用并重启 DataKit'}</Button>
          </Space>
        ) : phase === 'applying' ? null : <Button type={phase === 'done' ? 'primary' : 'default'} onClick={close}>{phase === 'rolling' ? '后台继续' : '关闭'}</Button>
      }>
      <Steps size="small" current={step} status={phase === 'error' ? 'error' : undefined} style={{ margin: '4px 0 16px' }}
        items={[{ title: '预览' }, { title: '写入' }, { title: '重启' }, { title: '完成' }]} />

      {phase === 'error' && <Alert type="error" showIcon message="操作失败" description={<pre style={{ margin: 0, whiteSpace: 'pre-wrap' }}>{error}</pre>} />}

      {phase === 'preview' && (!plan ? <Spin style={{ display: 'block', margin: '40px auto' }} /> : (
        <Space direction="vertical" style={{ width: '100%' }} size={12}>
          {plan.warnings.map((w, i) => <Alert key={i} type="warning" showIcon message={w} />)}
          <div>
            <Typography.Text strong>将执行的操作</Typography.Text>
            <ol style={{ margin: '6px 0 0', paddingLeft: 22 }}>{plan.actions.map((a, i) => <li key={i}>{a}</li>)}</ol>
          </div>
          {request && request.diffs.length > 0 && (
            <Collapse size="small" defaultActiveKey={request.diffs.map((_, i) => i)}
              items={request.diffs.map((d, i) => ({ key: i, label: d.title, children: <DiffView before={d.before} after={d.after} /> }))} />
          )}
        </Space>
      ))}

      {phase === 'applying' && <div style={{ textAlign: 'center', padding: 40 }}><Spin /><div style={{ marginTop: 12 }}>正在写入配置并更新工作负载…</div></div>}

      {(phase === 'rolling' || phase === 'done') && (
        <Space direction="vertical" style={{ width: '100%' }} size={12}>
          {phase === 'done'
            ? <Result status="success" title="已生效" subTitle={rollout?.message} style={{ padding: '8px 0' }} />
            : <>
                <Progress percent={rollout && rollout.desired ? Math.round((Math.min(rollout.updated, rollout.available) / rollout.desired) * 100) : 0} status="active" />
                <div>{rollout?.message ?? '等待控制器处理变更…'}</div>
              </>}
          {badPods.length > 0 && <Alert type="warning" showIcon message="部分 Pod 尚未就绪"
            description={badPods.slice(0, 5).map((p) => <div key={p.name}><Tag>{p.node}</Tag>{p.name}：{p.reason}</div>)} />}
          {result?.warnings.map((w, i) => <Alert key={i} type="warning" showIcon message={w} />)}
          {result?.backupFile && <Typography.Text type="secondary" style={{ fontSize: 12 }}>变更前的 YAML 已备份到 {result.backupFile}</Typography.Text>}
        </Space>
      )}
    </Modal>
  )
}
