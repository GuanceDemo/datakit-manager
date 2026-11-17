import { useMemo } from 'react'
import { Alert, Tag, Tooltip } from 'antd'
import type { ConfFile, Inspection, Sample } from '../types'
import { ConfManager, type ConfRow } from './ConfManager'
import type { ApplyRequest } from './ApplyModal'

const CONF_DIR = '/usr/local/datakit/conf.d'
const rel = (p: string) => (p.startsWith(CONF_DIR + '/') ? p.slice(CONF_DIR.length + 1) : p)

/** Collector configs of a DataKit running in Kubernetes: what is saved AND what the pods really run. */
export function Collectors({ ins, samples, onApply }: { ins: Inspection; samples: Sample[]; onApply: (r: ApplyRequest) => void }) {
  const p = ins.persistence
  const toVolume = p.mode === 'volume'
  const byId = useMemo(() => new Map<string, ConfFile>(), [ins])

  const rows = useMemo<ConfRow[]>(() => ins.confFiles.map((f) => {
    const source = f.source || 'configmap'
    const id = `${source}:${f.configMap}:${f.path || f.key}`
    byId.set(id, f)
    const tags = [
      source === 'configmap' ? <Tag key="s" color="blue">ConfigMap</Tag>
        : source === 'volume' ? <Tag key="s" color="purple">持久化目录</Tag>
        : <Tooltip key="s" title="容器启动时生成，没有持久化。通常来自“默认开启的采集器”。"><Tag>容器内生成</Tag></Tooltip>,
    ]
    if (source === 'configmap' && !f.mounted) {
      tags.push(<Tooltip key="m" title="这个 key 没有挂载进容器，DataKit 不会加载。编辑并应用即可自动挂载。"><Tag color="orange">未挂载</Tag></Tooltip>)
    } else if (ins.liveSynced) {
      if (f.drift === 'pending') tags.push(<Tooltip key="l" title="已保存的内容与 Pod 里正在运行的不同，重启后生效。"><Tag color="orange">待重启生效</Tag></Tooltip>)
      else if (f.drift === 'inconsistent') tags.push(<Tooltip key="l" title={`${ins.livePods} 个 Pod 中只有 ${f.pods} 个有这个文件，或内容不一致。重新应用一次可以对齐。`}><Tag color="red">各 Pod 不一致</Tag></Tooltip>)
      else if (f.live) tags.push(<Tag key="l" color="green">生效中</Tag>)
      else tags.push(<Tooltip key="l" title="Pod 里还没有这个文件，重启后生效。"><Tag color="orange">未生效</Tag></Tooltip>)
    }
    return {
      id, name: source === 'configmap' ? f.key : rel(f.path), content: f.content, inputs: f.inputs ?? [], error: f.error,
      location: source === 'configmap' ? `ConfigMap ${f.configMap}${f.mountPath ? ' → ' + f.mountPath : ''}` : f.path,
      editable: f.editable ?? true, tags,
      readonlyHint: '这个文件是 DataKit 在容器启动时生成的（通常来自环境变量 ENV_DEFAULT_ENABLED_INPUTS），容器重启会重新生成，在这里修改不会保留。要调整它，请到“环境变量”页修改对应的 ENV_INPUT_* 变量，或用“添加采集器”新建一份持久化配置。',
    }
  }), [ins]) // eslint-disable-line

  const banner = (
    <>
      <Alert showIcon type={p.mode === 'none' ? 'warning' : 'success'}
        message={p.mode === 'volume' ? 'conf 目录已持久化：新配置直接写入该目录' : p.mode === 'configmap' ? '配置通过 ConfigMap 持久化' : 'conf.d 尚未持久化'}
        description={p.summary} />
      {ins.liveSynced
        ? <Alert showIcon type="info" message={`已与 ${ins.livePods} 个运行中的 Pod 同步：下表包含 Pod 里实际生效的全部配置文件。`} />
        : ins.liveError && <Alert showIcon type="warning" message="没有同步到运行中的配置" description={ins.liveError + ' 下表只显示 ConfigMap 里保存的配置。'} />}
    </>
  )

  return (
    <ConfManager rows={rows} samples={samples} banner={banner} resetKey={ins} subdirs={toVolume}
      newFileHint={(name) => (toVolume ? `写入持久化目录 ${p.volumeDir}/${name}` : `保存到 ConfigMap ${p.configMap}，挂载为 ${CONF_DIR}/${name}`)}
      deleteHint="删除后会重启 DataKit。"
      onSave={(row, name, content, original) => {
        const f = row ? byId.get(row.id) : undefined
        const inVolume = f ? f.source === 'volume' : toVolume
        const path = f ? f.path : `${p.volumeDir}/${name}`
        onApply({
          title: `${row ? '修改' : '新增'}采集器配置 ${name}`,
          change: { confUpserts: [inVolume ? { path, content } : { configMap: f?.configMap ?? p.configMap, key: f?.key ?? name, content }] },
          diffs: [{ title: inVolume ? path : `${name}（ConfigMap ${f?.configMap ?? p.configMap}）`, before: original, after: content }],
        })
      }}
      onDelete={(row) => {
        const f = byId.get(row.id)!
        onApply({
          title: `删除采集器配置 ${row.name}`,
          change: { confDeletes: [f.source === 'volume' ? { path: f.path } : { configMap: f.configMap, key: f.key }] },
          diffs: [{ title: row.name, before: f.content, after: '' }],
        })
      }} />
  )
}
