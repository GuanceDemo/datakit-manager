// Typed access to the Go backend. Three transports, one interface:
//   - desktop: Wails bindings (window.go.app.Service + window.go.main.Desktop)
//   - web:     POST /api/<Method> with a JSON array of arguments
//   - dev:     an in-browser mock (`npm run dev` only)
import type {
  AppInfo, ApplyResult, ChangeSet, Cluster, ContextInfo, DataKitInstall, Host, HostChanges, HostImport, HostImportResult, HostInfo, HostInstallOptions,
  Inspection, InstallDefaults, OperatorInstall, OperatorStatus, PlanView, PodMetrics, RolloutStatus, Sample, Versions, BugReportFile, BugReportOptions,
} from './types'

export interface Backend {
  Info(): Promise<AppInfo>
  Samples(): Promise<Sample[]>
  InstallDefaults(): Promise<InstallDefaults>

  ListClusters(): Promise<Cluster[]>
  UploadKubeconfig(content: string): Promise<string>
  ParseKubeconfig(path: string): Promise<ContextInfo[]>
  ImportClusters(path: string, contexts: string[], namespace: string): Promise<Cluster[]>
  UpdateCluster(id: string, name: string, namespace: string): Promise<Cluster>
  RemoveCluster(id: string): Promise<void>

  Inspect(id: string): Promise<Inspection>
  PreviewChanges(id: string, cs: ChangeSet): Promise<PlanView>
  ApplyChanges(id: string, cs: ChangeSet): Promise<ApplyResult>
  RolloutStatus(id: string): Promise<RolloutStatus>
  Metrics(id: string, pod: string): Promise<PodMetrics[]>
  PodLogs(id: string, pod: string, container: string, tail: number): Promise<string>

  RenderDataKitYAML(id: string, o: DataKitInstall): Promise<string>
  RenderOperatorYAML(id: string, o: OperatorInstall): Promise<string>
  ApplyYAML(id: string, yaml: string): Promise<string[]>

  OperatorStatus(id: string): Promise<OperatorStatus>
  ApplyOperatorConfig(id: string, config: string): Promise<ApplyResult>
  OperatorRollout(id: string): Promise<RolloutStatus>

  LatestVersions(): Promise<Versions>

  ListHosts(): Promise<Host[]>
  ImportHosts(req: HostImport): Promise<HostImportResult>
  UpdateHost(id: string, name: string): Promise<Host>
  RemoveHost(id: string): Promise<void>
  HostInspect(id: string): Promise<HostInfo>
  HostPreview(id: string, ch: HostChanges): Promise<PlanView>
  HostApply(id: string, ch: HostChanges): Promise<ApplyResult>
  HostStatus(id: string): Promise<RolloutStatus>
  HostMetrics(id: string): Promise<PodMetrics[]>
  HostLogs(id: string, lines: number): Promise<string>
  HostInstall(id: string, o: HostInstallOptions): Promise<string>

  /** desktop only */
  PickKubeconfig(): Promise<string>
  /** desktop only */
  PickSSHKey(): Promise<string>
  /** desktop only */
  BugReport(id: string, pod: string, node: string, container: string, o: BugReportOptions): Promise<BugReportFile>
  HostBugReport(id: string, o: BugReportOptions): Promise<BugReportFile>
  /** desktop: save dialog, resolves to the saved path ('' = cancelled); web: browser download, resolves to 'download' */
  SaveBugReport(file: string): Promise<string>
  OpenDataDir(): Promise<void>
}

let backend: Backend | null = null

/** web: fetch the exported zip with the API's credentials and hand it to the browser as a download */
async function downloadBugReport(file: string): Promise<string> {
  const res = await fetch('/download/bugreport/' + encodeURIComponent(file), { credentials: 'same-origin', headers: { 'X-Requested-With': 'datakit-manager' } })
  if (res.status === 401) throw new Error('登录已失效，请刷新页面重新登录')
  if (!res.ok) throw new Error((await res.text().catch(() => '')) || `下载失败（${res.status}）`)
  const url = URL.createObjectURL(await res.blob())
  const a = document.createElement('a')
  a.href = url; a.download = file
  document.body.appendChild(a); a.click(); a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 60_000)
  return 'download'
}

function httpBackend(): Backend {
  return new Proxy({} as Backend, {
    get: (_t, method: string) => async (...args: unknown[]) => {
      if (method === 'SaveBugReport') return downloadBugReport(String(args[0]))
      const res = await fetch('/api/' + method, {
        method: 'POST', credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'datakit-manager' },
        body: JSON.stringify(args),
      })
      if (res.status === 401) throw new Error('登录已失效，请刷新页面重新登录')
      const body = await res.json().catch(() => ({ error: `服务器返回 ${res.status}` }))
      if (body.error) throw new Error(body.error)
      return body.result
    },
  })
}

export async function initBackend(): Promise<void> {
  const go = (window as any).go
  if (go?.app?.Service) {
    backend = new Proxy({} as Backend, { get: (_t, name: string) => go.app.Service[name] ?? go.main?.Desktop?.[name] })
  } else if (import.meta.env.DEV && !import.meta.env.VITE_API) {
    backend = (await import('./mock')).mockBackend
  } else {
    backend = httpBackend()
  }
}

export const api = new Proxy({} as Backend, {
  get: (_t, name: string) => (...args: unknown[]) => (backend as any)[name](...args),
})

/** Wails rejects with a plain string; normalise to a message. */
export function errMsg(e: unknown): string {
  if (typeof e === 'string') return e
  if (e instanceof Error) return e.message
  try { return JSON.stringify(e) } catch { return String(e) }
}
