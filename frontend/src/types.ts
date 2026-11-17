export interface Cluster { id: string; name: string; context: string; server: string; namespace: string; source: string; addedAt: string }
export interface ContextInfo { name: string; cluster: string; server: string; user: string; namespace: string; current: boolean }

export interface Workload {
  kind: string; name: string; container: string; image: string; version: string
  desired: number; updated: number; ready: number; available: number; rolloutDone: boolean
  hostNetwork: boolean; httpPort: number; httpLocal: boolean; restartedAt: string
}
export interface Pod { name: string; node: string; ip: string; phase: string; ready: boolean; restarts: number; reason: string; image: string; startAt: string }
export interface ConfMount { mountPath: string; subPath: string; volume: string; configMap: string; kind: string }
export interface Persistence {
  mode: 'configmap' | 'volume' | 'none'; volumeDir: string; volumeDesc: string; volumePerNode: boolean
  configMap: string; configMapExists: boolean
  volumeName: string; volumeDeclared: boolean; mounts: ConfMount[]; dirVolumes: string[]; summary: string
}
export interface ConfFile {
  /** configmap | volume (persisted conf directory) | container (generated in the container, not persisted) */
  source: 'configmap' | 'volume' | 'container'
  path: string; editable: boolean; live: boolean; drift: '' | 'pending' | 'inconsistent'; pods: number
  configMap: string; key: string; content: string; mounted: boolean; mountPath: string; inputs: string[]; error: string
}
export interface EnvVar { name: string; value: string; fromRef: string; editable: boolean }
export interface Inspection {
  namespace: string; found: boolean; message: string; serverVersion: string
  workload: Workload; pods: Pod[]; persistence: Persistence; confFiles: ConfFile[]; env: EnvVar[]; enabledInputs: string[]
  /** confFiles was reconciled with the files really present in running pods */
  liveSynced: boolean; liveError: string; livePods: number
}

export interface ChangeSet {
  /** path = file in the persisted conf directory (absolute, in the container); otherwise a ConfigMap key */
  confUpserts?: { path?: string; configMap?: string; key?: string; content: string }[]
  confDeletes?: { path?: string; configMap?: string; key?: string }[]
  envSet?: { name: string; value: string }[]
  envDelete?: string[]
}
export interface PlanView { actions: string[]; warnings: string[] }
export interface ApplyResult extends PlanView { backupFile: string }
export interface RolloutStatus { desired: number; updated: number; ready: number; available: number; done: boolean; message: string; pods: Pod[] }

export interface InputInstance { name: string; instances: number; crashes: number }
export interface FeedStat { name: string; category: string; feeds: number; points: number; filtered: number; errors: number; lastFeed: number; avgCollectMs: number }
export interface UploadStat { category: string; total: number; ok: number; failed: number }
export interface LastError { source: string; category: string; error: string }
export interface PodMetrics {
  pod: string; node: string; ok: boolean; error: string; via: string
  version: string; hostname: string; osArch: string; buildAt: string; uptimeSec: number
  cpuUsage: number; cpuCores: number; memRss: number; goroutines: number; openFiles: number
  electionStatus: string; electedId: string; electionNamespace: string
  inputs: InputInstance[]; feeds: FeedStat[]; uploads: UploadStat[]; errors: LastError[]
}
export interface Sample { id: string; input: string; sample: string }

export interface AppInfo { version: string; mode: 'desktop' | 'web'; dataDir: string }
export interface InstallDefaults { datakitVersion: string; datakitRepo: string; operatorVersion: string; operatorRepo: string; operatorNamespace: string; enabledInputs: string }
export interface DataKitInstall { image: string; dataway: string; clusterName: string; enabledInputs: string; globalHostTags?: string; imagePullSecret: string }
export interface OperatorInstall { image: string; operatorRepo: string; datakitRepo: string; imagePullSecret: string }
export interface OperatorStatus {
  installed: boolean; namespace: string; name: string; image: string; version: string; desired: number; ready: number; pods: Pod[]
  configMap: string; configKey: string; config: string; configError: string; webhook: boolean; message: string
}

export interface Host { id: string; name: string; address: string; port: number; user: string; hostKey: string; addedAt: string }
export interface HostConfFile { path: string; rel: string; content: string; inputs: string[]; error: string }
export interface HostInfo {
  hostname: string; os: string; arch: string; kernel: string; user: string; root: boolean
  installed: boolean; version: string; running: boolean; service: string
  installDir: string; confDir: string; mainConfPath: string; mainConf: string; httpListen: string
  defaultInputs: string[]; confFiles: HostConfFile[]
}
export interface HostChanges { upserts?: { path: string; content: string }[]; deletes?: string[] }
export interface HostInstallOptions { dataway: string; defInputs?: string; globalHostTags?: string; offline: boolean; version?: string; upgrade?: boolean }
export interface HostImport { keyPath?: string; keyContent?: string; passphrase?: string; hosts: string[] }
export interface HostImportResult { added: Host[]; errors: string[] }
export interface Versions { datakit: string; error: string }

export interface BugReportOptions { disableProfile: boolean; nmetrics: number }
/** an exported bug report kept (temporarily) in the backend's data directory */
export interface BugReportFile { file: string; path: string; size: number; output: string }
