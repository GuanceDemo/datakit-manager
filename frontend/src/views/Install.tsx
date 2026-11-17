import { useEffect, useMemo, useState } from 'react'
import { Alert, Button, Drawer, Form, Input, Result, Segmented, Select, Space, Spin, Steps, Typography, message } from 'antd'
import { api, errMsg } from '../api'
import type { Cluster, InstallDefaults } from '../types'
import { CodeEditor, ImagePushHints, Logo } from '../components'
import { defaultableInputs } from '../envCatalog'

type Kind = 'datakit' | 'operator'

/** Images the official operator config references, besides the operator itself. */
function imagesIn(yaml: string): string[] {
  const out = new Set<string>()
  for (const m of yaml.matchAll(/"?image"?\s*:\s*\\?"?([A-Za-z0-9][^\s"\\,]+)/g)) out.add(m[1])
  return [...out]
}

/**
 * One-click install of DataKit or the DataKit Operator from the official YAML.
 * Form -> rendered YAML (editable) -> apply.
 */
export function InstallDrawer({ kind, cluster, open, onClose }: { kind: Kind; cluster: Cluster; open: boolean; onClose: (installed: boolean) => void }) {
  const [defs, setDefs] = useState<InstallDefaults | null>(null)
  const [version, setVersion] = useState('')
  const [source, setSource] = useState<'public' | 'private'>('public')
  const [registry, setRegistry] = useState('')
  const [pullSecret, setPullSecret] = useState('')
  const [dataway, setDataway] = useState('')
  const [clusterName, setClusterName] = useState('')
  const [inputs, setInputs] = useState<string[]>([])
  const [step, setStep] = useState(0)
  const [yaml, setYaml] = useState('')
  const [officialImages, setOfficialImages] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState<string[] | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    if (!open) return
    setStep(0); setYaml(''); setDone(null); setError(''); setSource('public'); setRegistry(''); setPullSecret(''); setDataway('')
    setClusterName(cluster.name.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '') || 'default')
    api.InstallDefaults().then((d) => {
      setDefs(d); setVersion(kind === 'datakit' ? d.datakitVersion : d.operatorVersion); setInputs(d.enabledInputs.split(','))
      // prefer the newest published DataKit version when the download site is reachable
      if (kind === 'datakit') api.LatestVersions().then((v) => { if (v.datakit) setVersion(v.datakit) }).catch(() => {})
    }).catch((e) => setError(errMsg(e)))
  }, [open, kind, cluster.id]) // eslint-disable-line

  const reg = registry.trim().replace(/\/+$/, '')
  const publicImage = defs ? (kind === 'datakit' ? `${defs.datakitRepo}/datakit:${version}` : `${defs.operatorRepo}/datakit-operator:${version}`) : ''
  const image = source === 'private' && reg ? `${reg}/${kind === 'datakit' ? 'datakit' : 'datakit-operator'}:${version}` : publicImage

  // the operator config references more images (ddtrace agent, logfwd, flameshot): list them from the official manifest
  useEffect(() => {
    if (!open || !defs || kind !== 'operator' || !version) return
    api.RenderOperatorYAML(cluster.id, { image: publicImage, operatorRepo: defs.operatorRepo, datakitRepo: defs.datakitRepo, imagePullSecret: '' })
      .then((y) => setOfficialImages(imagesIn(y))).catch(() => setOfficialImages([publicImage]))
  }, [open, defs, kind, version]) // eslint-disable-line
  const images = useMemo(() => (kind === 'datakit' ? [publicImage] : officialImages.length ? officialImages : [publicImage]), [kind, publicImage, officialImages])

  const render = async () => {
    if (!defs) return
    if (!version.trim()) return void message.error('请填写版本')
    if (source === 'private' && !reg) return void message.error('请填写私有仓库地址')
    if (kind === 'datakit' && !dataway.trim()) return void message.error('请填写 DataWay 地址')
    setBusy(true)
    try {
      const priv = source === 'private'
      const y = kind === 'datakit'
        ? await api.RenderDataKitYAML(cluster.id, { image, dataway: dataway.trim(), clusterName: clusterName.trim(), enabledInputs: inputs.join(','), imagePullSecret: pullSecret.trim() })
        : await api.RenderOperatorYAML(cluster.id, { image, operatorRepo: priv ? reg : defs.operatorRepo, datakitRepo: priv ? reg : defs.datakitRepo, imagePullSecret: pullSecret.trim() })
      setYaml(y); setStep(1)
    } catch (e) { message.error(errMsg(e)) } finally { setBusy(false) }
  }

  const install = async () => {
    setBusy(true); setError('')
    try { setDone((await api.ApplyYAML(cluster.id, yaml)) ?? []); setStep(2) }
    catch (e) { setError(errMsg(e)) } finally { setBusy(false) }
  }

  const title = kind === 'datakit' ? 'DataKit' : 'DataKit Operator'
  return (
    <Drawer open={open} width={920} onClose={() => onClose(!!done)} maskClosable={false} destroyOnHidden
      title={<Space><Logo name={kind === 'datakit' ? 'dk' : 'container'} />安装 {title}</Space>}
      extra={step === 0 ? <Button type="primary" loading={busy} disabled={!defs} onClick={render}>生成 YAML</Button>
        : step === 1 ? <Space><Button onClick={() => setStep(0)}>上一步</Button><Button type="primary" loading={busy} onClick={install}>安装到集群</Button></Space>
        : <Button type="primary" onClick={() => onClose(true)}>完成</Button>}>
      <Steps size="small" current={step} style={{ marginBottom: 20 }} items={[{ title: '填写参数' }, { title: '确认 YAML' }, { title: '完成' }]} />

      {!defs && !error && <Spin style={{ display: 'block', margin: 40 }} />}
      {error && <Alert type="error" showIcon style={{ marginBottom: 16 }} message="操作失败" description={<pre style={{ margin: 0, whiteSpace: 'pre-wrap' }}>{error}</pre>} />}

      {defs && step === 0 && (
        <Space direction="vertical" size={16} style={{ width: '100%' }}>
          {kind === 'operator' && <Alert type="info" showIcon message={`Operator 固定安装在 ${defs.operatorNamespace} 命名空间`}
            description={`Operator 镜像内置的 Webhook 证书签发给 datakit-operator.${defs.operatorNamespace}.svc，装到其它命名空间 Webhook 无法工作。注入的探针会上报到 ${cluster.namespace} 命名空间里的 DataKit。`} />}
          <Form layout="horizontal" labelCol={{ flex: '170px' }} labelAlign="left" colon={false}>
            {kind === 'datakit' && <>
              <Form.Item label="安装到命名空间"><Input value={cluster.namespace} disabled style={{ width: 260 }} /><Typography.Text type="secondary" style={{ marginLeft: 12, fontSize: 12 }}>在集群设置里修改</Typography.Text></Form.Item>
              <Form.Item label="DataWay 地址" required extra="在观测云控制台「集成 → DataKit」中可以复制，包含工作空间 token。">
                <Input value={dataway} placeholder="https://openway.guance.com?token=tkn_xxxxxxxx" onChange={(e) => setDataway(e.target.value)} />
              </Form.Item>
              <Form.Item label="集群名称" extra="写入 ENV_CLUSTER_NAME_K8S，多集群时务必各不相同。"><Input value={clusterName} style={{ width: 360 }} onChange={(e) => setClusterName(e.target.value)} /></Form.Item>
              <Form.Item label="默认开启的采集器">
                <Select mode="tags" value={inputs} onChange={setInputs} tokenSeparators={[',', ' ']} options={defaultableInputs.map((o) => ({ value: o, label: o }))} />
              </Form.Item>
            </>}
            <Form.Item label={`${title} 版本`} extra={kind === 'datakit' ? "默认取官方最新版本（获取不到时用内置清单对应的版本），可以修改。" : "默认是本工具内置清单对应的版本，可以改成更新的版本号。"}><Input value={version} style={{ width: 260 }} onChange={(e) => setVersion(e.target.value.trim())} /></Form.Item>
            <Form.Item label="镜像来源">
              <Segmented value={source} onChange={(v) => setSource(v as 'public' | 'private')}
                options={[{ value: 'public', label: '官方仓库（集群可访问外网）' }, { value: 'private', label: '私有仓库（集群无法访问外网）' }]} />
            </Form.Item>
            {source === 'private' && <>
              <Form.Item label="私有仓库地址" required extra="仓库地址加项目路径，不带镜像名和标签。">
                <Input value={registry} placeholder="harbor.example.com/datakit" onChange={(e) => setRegistry(e.target.value)} />
              </Form.Item>
              <Form.Item label="镜像拉取 Secret" extra="仓库需要认证时填写，Secret 须已存在于目标命名空间。">
                <Input value={pullSecret} placeholder="可选，如 regcred" style={{ width: 360 }} onChange={(e) => setPullSecret(e.target.value)} />
              </Form.Item>
            </>}
            <Form.Item label="将使用的镜像"><Typography.Text code copyable>{image}</Typography.Text></Form.Item>
          </Form>
          {source === 'private' && <ImagePushHints images={images} registry={registry} />}
        </Space>
      )}

      {step === 1 && (
        <Space direction="vertical" size={10} style={{ width: '100%' }}>
          <Typography.Text type="secondary">这是将要应用到集群的 YAML，可以直接修改（例如调整资源限制、容忍度）。应用方式等同于 kubectl apply。</Typography.Text>
          {source === 'private' && <Alert type="warning" showIcon message="请确认镜像已经推送到私有仓库，否则 Pod 会停在 ImagePullBackOff。" />}
          <CodeEditor lang="yaml" value={yaml} onChange={setYaml} height="calc(100vh - 300px)" />
        </Space>
      )}

      {step === 2 && done && (
        <Result status="success" title={`${title} 已提交安装`} subTitle="资源已创建，Pod 拉取镜像并启动需要一点时间。关闭后可在页面上查看状态。">
          <div style={{ textAlign: 'left', maxHeight: 280, overflow: 'auto' }}>{done.map((d, i) => <div key={i} style={{ fontSize: 13, lineHeight: 1.9 }}>✓ {d}</div>)}</div>
        </Result>
      )}
    </Drawer>
  )
}
