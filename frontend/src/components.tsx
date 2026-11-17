import { useMemo, useState } from 'react'
import { Button, Radio, Space, Typography } from 'antd'
import { CheckOutlined, CopyOutlined } from '@ant-design/icons'
import CodeMirror from '@uiw/react-codemirror'
import { StreamLanguage } from '@codemirror/language'
import { toml } from '@codemirror/legacy-modes/mode/toml'
import { shell } from '@codemirror/legacy-modes/mode/shell'
import { yaml } from '@codemirror/legacy-modes/mode/yaml'
import { json } from '@codemirror/legacy-modes/mode/javascript'
import { diffLines } from 'diff'
import { iconFor } from './icons'

const palette = ['#2f6fed', '#0e9f6e', '#d97706', '#7c3aed', '#db2777', '#0891b2', '#65a30d', '#dc2626']

/** Collector logo, falling back to a coloured initial. */
export function Logo({ name, size = 28 }: { name: string; size?: number }) {
  const url = iconFor(name)
  if (url) return <img src={url} width={size} height={size} style={{ objectFit: 'contain', flex: 'none' }} alt="" />
  let h = 0
  for (const c of name) h = (h * 31 + c.charCodeAt(0)) >>> 0
  return (
    <span style={{ width: size, height: size, borderRadius: 6, background: palette[h % palette.length], color: '#fff', flex: 'none',
      display: 'inline-flex', alignItems: 'center', justifyContent: 'center', fontSize: size * 0.5, fontWeight: 600 }}>
      {(name[0] ?? '?').toUpperCase()}
    </span>
  )
}

const tomlLang = StreamLanguage.define(toml)
const shellLang = StreamLanguage.define(shell)
const langs = { toml: tomlLang, yaml: StreamLanguage.define(yaml), json: StreamLanguage.define(json), shell: shellLang }

export function CodeEditor({ value, onChange, height = '100%', readOnly = false, lang = 'toml' }: {
  value: string; onChange?: (v: string) => void; height?: string; readOnly?: boolean; lang?: keyof typeof langs
}) {
  return (
    <CodeMirror value={value} height={height} extensions={[langs[lang]]} onChange={onChange} readOnly={readOnly} editable={!readOnly}
      basicSetup={{ foldGutter: false, highlightActiveLine: !readOnly }} style={{ fontSize: 13, border: '1px solid #e5e7eb', borderRadius: 6, overflow: 'hidden' }} />
  )
}

export const TomlEditor = CodeEditor

export function LogView({ value, height = '60vh' }: { value: string; height?: string }) {
  return <CodeMirror value={value} height={height} extensions={[shellLang]} readOnly editable={false} basicSetup={{ foldGutter: false, highlightActiveLine: false }} style={{ fontSize: 12 }} />
}

/** Unified line diff. Unchanged runs longer than 6 lines are collapsed. */
export function DiffView({ before, after }: { before: string; after: string }) {
  const rows = useMemo(() => {
    const out: { kind: '+' | '-' | ' ' | '…'; text: string }[] = []
    for (const part of diffLines(before, after)) {
      const lines = part.value.replace(/\n$/, '').split('\n')
      const kind = part.added ? '+' : part.removed ? '-' : ' '
      if (kind === ' ' && lines.length > 6) {
        lines.slice(0, 3).forEach((t) => out.push({ kind, text: t }))
        out.push({ kind: '…', text: `… ${lines.length - 6} 行未变化 …` })
        lines.slice(-3).forEach((t) => out.push({ kind, text: t }))
      } else lines.forEach((t) => out.push({ kind, text: t }))
    }
    return out
  }, [before, after])
  if (before === after) return <div style={{ color: '#6b7280', padding: 8 }}>内容没有变化</div>
  const bg = { '+': '#e6f6ec', '-': '#fdecec', ' ': 'transparent', '…': '#f3f4f6' }
  const fg = { '+': '#116329', '-': '#a4262c', ' ': '#1f2937', '…': '#6b7280' }
  return (
    <pre style={{ margin: 0, fontSize: 12.5, lineHeight: 1.55, border: '1px solid #e5e7eb', borderRadius: 6, maxHeight: 340, overflow: 'auto', fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace' }}>
      {rows.map((r, i) => (
        <div key={i} style={{ background: bg[r.kind], color: fg[r.kind], padding: '0 10px', whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
          <span style={{ userSelect: 'none', opacity: 0.6, display: 'inline-block', width: 14 }}>{r.kind === '…' ? '' : r.kind}</span>{r.text || ' '}
        </div>
      ))}
    </pre>
  )
}

export function CopyButton({ text, label = '复制' }: { text: string; label?: string }) {
  const [done, setDone] = useState(false)
  const copy = async () => {
    try { await navigator.clipboard.writeText(text) } catch {
      const ta = document.createElement('textarea'); ta.value = text; document.body.appendChild(ta); ta.select(); document.execCommand('copy'); ta.remove()
    }
    setDone(true); setTimeout(() => setDone(false), 1500)
  }
  return <Button size="small" icon={done ? <CheckOutlined /> : <CopyOutlined />} onClick={copy}>{done ? '已复制' : label}</Button>
}

/** Last path segment of an image reference, with its tag: a.b/c/datakit:1.0 -> datakit:1.0 */
export const imageBase = (img: string) => img.slice(img.lastIndexOf('/') + 1)

/** Commands to mirror public images into a private registry, for clusters without internet access. */
export function ImagePushHints({ images, registry }: { images: string[]; registry: string }) {
  const [arch, setArch] = useState('amd64')
  const reg = registry.trim().replace(/\/+$/, '') || '<你的仓库地址>/<项目>'
  const pairs = images.map((src) => ({ src, dst: `${reg}/${imageBase(src)}` }))
  const push = [
    '# 1. 在能访问外网的机器上拉取（--platform 与集群节点架构一致）',
    ...pairs.map((p) => `docker pull --platform linux/${arch} ${p.src}`),
    '',
    '# 2. 打标签并推送到你的仓库',
    ...pairs.flatMap((p) => [`docker tag ${p.src} ${p.dst}`, `docker push ${p.dst}`]),
  ].join('\n')
  const offline = [
    '# 没有镜像仓库时：导出镜像，拷贝到每个节点后导入',
    `docker save -o datakit-images.tar ${pairs.map((p) => p.dst).join(' ')}`,
    'ctr -n k8s.io images import datakit-images.tar    # containerd 节点',
    'docker load -i datakit-images.tar                 # docker 节点',
  ].join('\n')
  const box = { margin: 0, padding: 12, background: '#1f2430', color: '#e5e7eb', borderRadius: 6, fontSize: 12.5, lineHeight: 1.6, overflow: 'auto', whiteSpace: 'pre' as const }
  return (
    <div style={{ border: '1px solid #ffd8b8', background: '#fff7f0', borderRadius: 8, padding: 14 }}>
      <Space style={{ marginBottom: 8, width: '100%', justifyContent: 'space-between' }}>
        <Typography.Text strong>镜像推送提示：集群无法访问外网时，需要先把下面 {images.length} 个镜像推送到你自己的仓库</Typography.Text>
        <Space>
          <Radio.Group size="small" value={arch} onChange={(e) => setArch(e.target.value)} optionType="button" options={[{ value: 'amd64', label: 'amd64' }, { value: 'arm64', label: 'arm64' }]} />
          <CopyButton text={push} label="复制命令" />
        </Space>
      </Space>
      <pre style={box}>{push}</pre>
      <details style={{ marginTop: 8 }}>
        <summary style={{ cursor: 'pointer', fontSize: 13, color: '#6b7280' }}>没有镜像仓库？用导出 / 导入的方式</summary>
        <pre style={{ ...box, marginTop: 8 }}>{offline}</pre>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>这种方式下镜像地址仍需与导入的标签一致，并且拉取策略不能是 Always（否则节点仍会尝试联网拉取）。</Typography.Text>
      </details>
    </div>
  )
}
