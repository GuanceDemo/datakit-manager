// In-browser fake backend, only used by `npm run dev` outside of Wails. Not part of the app logic.
import { parse } from 'smol-toml'
import type { Backend } from './api'
import type { Cluster, ConfFile, EnvVar, Host, HostConfFile, Inspection, PodMetrics } from './types'

const wait = (ms = 250) => new Promise((r) => setTimeout(r, ms))
let clusters: Cluster[] = [
  { id: 'a1', name: 'prod-shanghai', context: 'prod-shanghai', server: 'https://10.10.0.1:6443', namespace: 'datakit', source: '~/.kube/config', addedAt: '' },
  { id: 'b2', name: 'test-fresh', context: 'test', server: 'https://10.20.0.1:6443', namespace: 'monitoring', source: '~/.kube/config', addedAt: '' },
]
const state: Record<string, { files: ConfFile[]; env: EnvVar[]; rolling: number }> = {}
const st = (id: string) => (state[id] ??= {
  rolling: 0,
  files: id === 'a1' ? [{ source: 'configmap', path: '/usr/local/datakit/conf.d/mysql.conf', editable: true, live: true, drift: '', pods: 3, configMap: 'datakit-conf', key: 'mysql.conf', mounted: true, mountPath: '/usr/local/datakit/conf.d/mysql.conf', inputs: ['mysql'], error: '',
    content: '# 生产库\n[[inputs.mysql]]\n  host = "mysql.prod.svc"\n  port = 3306\n  user = "datakit"\n  pass = "secret"\n  interval = "10s"\n  election = true\n  innodb = true\n\n  [inputs.mysql.tags]\n    env = "prod"\n' }] : [],
  env: [
    { name: 'ENV_K8S_NODE_NAME', value: '', fromRef: 'fieldRef: spec.nodeName', editable: false },
    { name: 'ENV_DATAWAY', value: 'https://openway.guance.com?token=tkn_demo', fromRef: '', editable: true },
    { name: 'ENV_CLUSTER_NAME_K8S', value: id === 'a1' ? 'prod-shanghai' : 'default', fromRef: '', editable: true },
    { name: 'ENV_DEFAULT_ENABLED_INPUTS', value: 'statsd,dk,cpu,disk,diskio,mem,swap,system,hostobject,net,host_processes,container,kubernetesprometheus,ddtrace', fromRef: '', editable: true },
    { name: 'ENV_ENABLE_ELECTION', value: 'enable', fromRef: '', editable: true },
    { name: 'ENV_HTTP_LISTEN', value: '0.0.0.0:9529', fromRef: '', editable: true },
    { name: 'HOST_PROC', value: '/rootfs/proc', fromRef: '', editable: true },
  ],
})
const op = { installed: false, rolling: 0, config: JSON.stringify({ server_listen: '0.0.0.0:9543', log_level: 'info', admission_inject_v2: {
  ddtraces: [{ language: 'java', namespace_selectors: ['default'], label_selectors: [], check_annotation: false, image: 'pubrepo.guance.com/datakit-operator/dd-lib-java-init:v1.60.4-ext', image_pull_policy: 'Always',
    envs: { DD_AGENT_HOST: 'datakit-service.datakit.svc.cluster.local', DD_TRACE_AGENT_PORT: '9529', POD_NAME: '{fieldRef:metadata.name}', DD_TAGS: 'pod_name:$(POD_NAME)' } }],
  otels: [], logfwds: [], flameshots: [] }, admission_mutate: { loggings: [] } }) }
let mhosts: Host[] = [
  { id: 'h1', name: '订单服务-1', address: '10.0.0.11', port: 22, user: 'root', hostKey: '', addedAt: '' },
  { id: 'h2', name: 'db-backup', address: '10.0.0.40', port: 22, user: 'ubuntu', hostKey: '', addedAt: '' },
]
const hostInstalled = new Set(['h1'])
let hostFiles: HostConfFile[] = [
  { path: '/usr/local/datakit/conf.d/db/redis.conf', rel: 'db/redis.conf', content: '# 缓存集群\n[[inputs.redis]]\n  host = "127.0.0.1"\n  port = 6379\n', inputs: ['redis'], error: '' },
  { path: '/usr/local/datakit/conf.d/host/cpu.conf', rel: 'host/cpu.conf', content: '[[inputs.cpu]]\n  interval = "10s"\n', inputs: ['cpu'], error: '' },
]
const nodes = ['node-10-10-0-11', 'node-10-10-0-12', 'node-10-10-0-13']
const pods = (rolling: number) => nodes.map((n, i) => ({ name: `datakit-${'xk2p9'.slice(i)}${i}`, node: n, ip: `10.10.0.1${i + 1}`, phase: 'Running', ready: i >= rolling, restarts: 0, reason: i >= rolling ? '' : 'ContainerCreating', image: '', startAt: '' }))

export const mockBackend: Backend = {
  async ListClusters() { return clusters },
  async PickKubeconfig() { return '/Users/demo/.kube/config' },
  async ParseKubeconfig() { return [{ name: 'staging', cluster: 'staging', server: 'https://10.30.0.1:6443', user: 'admin', namespace: '', current: true }, { name: 'dev', cluster: 'dev', server: 'https://10.40.0.1:6443', user: 'dev', namespace: '', current: false }] },
  async ImportClusters(path, contexts, namespace) {
    const added = contexts.map((c) => ({ id: Math.random().toString(36).slice(2), name: c, context: c, server: 'https://10.30.0.1:6443', namespace, source: path, addedAt: '' }))
    clusters = [...clusters, ...added]; return added
  },
  async UpdateCluster(id, name, namespace) { clusters = clusters.map((c) => (c.id === id ? { ...c, name, namespace } : c)); return clusters.find((c) => c.id === id)! },
  async RemoveCluster(id) { clusters = clusters.filter((c) => c.id !== id) },
  async Inspect(id): Promise<Inspection> {
    await wait()
    const c = clusters.find((x) => x.id === id)!
    const s = st(id)
    if (c.namespace !== 'datakit') return { namespace: c.namespace, found: false, message: `命名空间 ${c.namespace} 中没有找到 DataKit（DaemonSet / StatefulSet / Deployment）。请确认命名空间是否正确。`, serverVersion: 'v1.30.4' } as Inspection
    const has = s.files.length > 0
    return {
      namespace: c.namespace, found: true, message: '', serverVersion: 'v1.30.4',
      workload: { kind: 'DaemonSet', name: 'datakit', container: 'datakit', image: 'pubrepo.guance.com/datakit/datakit:1.80.1', version: '1.80.1', desired: 3, updated: 3, ready: 3, available: 3, rolloutDone: true, hostNetwork: true, httpPort: 9529, httpLocal: false, restartedAt: '' },
      pods: pods(0),
      persistence: { mode: has ? 'configmap' : 'none', volumeDir: '', volumeDesc: '', volumePerNode: false, configMap: 'datakit-conf', configMapExists: has, volumeName: 'datakit-conf', volumeDeclared: true, mounts: [], dirVolumes: [],
        summary: has ? '采集器配置通过 ConfigMap datakit-conf 挂载到 conf.d，重启后不会丢失。' : '已声明卷但 ConfigMap datakit-conf 不存在，conf.d 下也没有挂载。保存配置时会自动创建 ConfigMap 并挂载。' },
      liveSynced: true, liveError: '', livePods: 3,
      confFiles: [...s.files, { source: 'container', path: '/usr/local/datakit/conf.d/host/cpu.conf', editable: false, live: true, drift: '', pods: 3, configMap: '', key: 'cpu.conf', mounted: true, mountPath: '', inputs: ['cpu'], error: '', content: '[[inputs.cpu]]\n  interval = "10s"\n' } as ConfFile], env: s.env, enabledInputs: s.env.find((e) => e.name === 'ENV_DEFAULT_ENABLED_INPUTS')!.value.split(','),
    }
  },
  async PreviewChanges(id, cs) {
    await wait()
    const s = st(id)
    const a: string[] = []
    for (const u of cs.confUpserts ?? []) {
      parse(u.content)
      if (!s.files.length) a.push('创建 ConfigMap datakit-conf')
      const ex = s.files.find((f) => f.key === u.key)
      a.push(ex ? `ConfigMap datakit-conf 更新 ${u.key}` : `ConfigMap datakit-conf 新增 ${u.key}`)
      if (!ex) a.push(`DaemonSet/datakit 新增挂载 /usr/local/datakit/conf.d/${u.key}（subPath: ${u.key}）`)
    }
    for (const d of cs.confDeletes ?? []) a.push(`DaemonSet/datakit 移除挂载 /usr/local/datakit/conf.d/${d.key}`, `ConfigMap datakit-conf 删除 ${d.key}`)
    for (const e of cs.envSet ?? []) a.push(`环境变量 ${e.name} → "${e.value}"`)
    for (const e of cs.envDelete ?? []) a.push(`删除环境变量 ${e}`)
    a.push('滚动重启 DaemonSet/datakit')
    return { actions: a, warnings: a.length === 1 ? ['配置内容没有变化，仅执行重启'] : [] }
  },
  async ApplyChanges(id, cs) {
    const pv = await this.PreviewChanges(id, cs)
    const s = st(id)
    for (const u of cs.confUpserts ?? []) {
      const doc = parse(u.content) as any
      const f: ConfFile = { source: 'configmap', path: '/usr/local/datakit/conf.d/' + u.key, editable: true, live: false, drift: '', pods: 0, configMap: 'datakit-conf', key: u.key!, content: u.content, mounted: true, mountPath: '/usr/local/datakit/conf.d/' + u.key, inputs: Object.keys(doc.inputs ?? {}), error: '' }
      s.files = [...s.files.filter((x) => x.key !== u.key), f]
    }
    for (const d of cs.confDeletes ?? []) s.files = s.files.filter((x) => x.key !== d.key)
    for (const e of cs.envSet ?? []) s.env = s.env.some((x) => x.name === e.name) ? s.env.map((x) => (x.name === e.name ? { ...x, value: e.value } : x)) : [...s.env, { name: e.name, value: e.value, fromRef: '', editable: true }]
    for (const e of cs.envDelete ?? []) s.env = s.env.filter((x) => x.name !== e)
    s.rolling = 3
    return { ...pv, backupFile: '/Users/demo/Library/Application Support/datakit-manager/backups/' + id + '/20261116-221500.yaml' }
  },
  async RolloutStatus(id) {
    const s = st(id)
    if (s.rolling > 0) s.rolling--
    const up = 3 - s.rolling
    return { desired: 3, updated: up, ready: up, available: up, done: s.rolling === 0, message: s.rolling ? `滚动重启中：已更新 ${up}/3，可用 ${up}/3` : '重启完成，3/3 个 Pod 已就绪', pods: pods(s.rolling) }
  },
  async Metrics(id): Promise<PodMetrics[]> {
    await wait(400)
    const now = Date.now() / 1000
    const hasMysql = st(id).files.some((f) => f.inputs.includes('mysql'))
    return pods(0).map((p, i) => ({
      pod: p.name, node: p.node, ok: true, error: '', via: 'proxy', version: '1.80.1', hostname: p.node, osArch: 'linux/amd64', buildAt: '', uptimeSec: 86400 * 3 + i * 500,
      cpuUsage: 2.1 + i, cpuCores: 8, memRss: (210 + i * 30) * 1024 * 1024, goroutines: 400, openFiles: 120,
      electionStatus: 'success', electedId: nodes[0], electionNamespace: 'default',
      inputs: [{ name: 'cpu', instances: 1, crashes: 0 }, { name: 'container', instances: 1, crashes: 0 }, { name: 'ddtrace', instances: 1, crashes: 0 }, ...(hasMysql && i === 0 ? [{ name: 'mysql', instances: 1, crashes: 0 }] : [])],
      feeds: [
        { name: 'cpu', category: 'metric', feeds: 25920, points: 25920, filtered: 0, errors: 0, lastFeed: now - 4, avgCollectMs: 1.2 },
        { name: 'container', category: 'metric', feeds: 8000, points: 640000, filtered: 0, errors: 0, lastFeed: now - 12, avgCollectMs: 85 },
        { name: 'container', category: 'object', feeds: 900, points: 81000, filtered: 0, errors: 0, lastFeed: now - 40, avgCollectMs: 85 },
        { name: 'ddtrace', category: 'tracing', feeds: 120000, points: 2400000, filtered: 0, errors: 0, lastFeed: now - 1, avgCollectMs: 0 },
        ...(hasMysql && i === 0 ? [{ name: 'mysql', category: 'metric', feeds: 2600, points: 130000, filtered: 0, errors: 3, lastFeed: now - 900, avgCollectMs: 42 }] : []),
      ],
      uploads: [{ category: 'metric', total: 800000, ok: 799200, failed: 800 }, { category: 'tracing', total: 2400000, ok: 2400000, failed: 0 }, { category: 'object', total: 81000, ok: 81000, failed: 0 }],
      errors: hasMysql && i === 0 ? [{ source: 'mysql', category: 'metric', error: 'dial tcp 10.0.0.1:3306: connect: connection refused' }] : [],
    }))
  },
  async PodLogs() { await wait(); return '2026-11-16T22:10:01.123+0800\tINFO\tio\tio/feed.go:120\tfeed 12 points from cpu\n2026-11-16T22:10:02.456+0800\tWARN\tmysql\tmysql/input.go:310\tconnect failed: connection refused\n' },
  async Samples() {
    const m = (await import('../../internal/catalog/samples.json')).default as Record<string, { input: string; sample: string }>
    return Object.entries(m).map(([id, v]) => ({ id, input: v.input, sample: v.sample }))
  },
  async Info() { return { version: 'dev', mode: (new URLSearchParams(location.search).get('mode') as any) ?? 'desktop', dataDir: '/Users/demo/Library/Application Support/datakit-manager' } },
  async InstallDefaults() { return { datakitVersion: '2.12.0', datakitRepo: 'pubrepo.guance.com/datakit', operatorVersion: 'v1.9.2', operatorRepo: 'pubrepo.guance.com/datakit-operator', operatorNamespace: 'datakit', enabledInputs: 'statsd,dk,cpu,disk,diskio,mem,swap,system,hostobject,net,host_processes,container,kubernetesprometheus' } },
  async UploadKubeconfig() { return '/data/uploads/abc.yaml' },
  async RenderDataKitYAML(_id, o) { return `apiVersion: v1\nkind: Namespace\nmetadata:\n  name: datakit\n---\napiVersion: apps/v1\nkind: DaemonSet\nmetadata:\n  name: datakit\n  namespace: datakit\nspec:\n  template:\n    spec:\n      containers:\n      - name: datakit\n        image: ${o.image}\n        env:\n        - name: ENV_DATAWAY\n          value: ${o.dataway}\n` },
  async RenderOperatorYAML(_id, o) { return `apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: datakit-operator\n  namespace: datakit\nspec:\n  template:\n    spec:\n      containers:\n      - name: operator\n        image: ${o.image}\n---\ndata:\n  jsonconfig: |-\n    "image": "${o.operatorRepo}/dd-lib-java-init:v1.60.4-ext",\n    "image": "${o.datakitRepo}/logfwd:1.93.0",\n    "image": "${o.datakitRepo}/flameshot:0.2.4",\n` },
  async ApplyYAML(id, yaml) {
    await wait(600)
    if (yaml.includes('datakit-operator')) { op.installed = true; return ['已应用 Namespace/datakit', '已应用 ClusterRole/datakit-operator', '已应用 Deployment/datakit-operator（datakit）', '已应用 MutatingWebhookConfiguration/datakit-operator-mutation'] }
    clusters = clusters.map((c) => (c.id === id ? { ...c, namespace: 'datakit' } : c))
    return ['已应用 Namespace/datakit', '已应用 ClusterRole/datakit', '已应用 DaemonSet/datakit（datakit）', '已应用 ConfigMap/datakit-conf（datakit）']
  },
  async OperatorStatus() {
    await wait()
    return { installed: op.installed, namespace: 'datakit', name: 'datakit-operator', image: 'pubrepo.guance.com/datakit-operator/datakit-operator:v1.9.2', version: 'v1.9.2', desired: 1, ready: 1,
      pods: [{ name: 'datakit-operator-7c9d8-abcde', node: nodes[0], ip: '', phase: 'Running', ready: true, restarts: 0, reason: '', image: '', startAt: '' }],
      configMap: 'datakit-operator-config', configKey: 'jsonconfig', config: op.config, configError: '', webhook: true, message: '' }
  },
  async ApplyOperatorConfig(_id, config) { await wait(500); op.config = config; op.rolling = 2; return { actions: [], warnings: [], backupFile: '' } },
  async OperatorRollout() { if (op.rolling > 0) op.rolling--; return { desired: 1, updated: 1, ready: op.rolling ? 0 : 1, available: op.rolling ? 0 : 1, done: op.rolling === 0, message: op.rolling ? 'Operator 重启中' : 'Operator 已就绪（1/1）', pods: [] } },
  async LatestVersions() { return { datakit: '2.19.0', error: '' } },
  async PickSSHKey() { return '/Users/demo/.ssh/id_ed25519' },
  async ListHosts() { return mhosts },
  async ImportHosts(req) {
    const added = req.hosts.filter((l) => l.trim()).map((l) => ({ id: Math.random().toString(36).slice(2), name: l.split(/\s+/).slice(1).join(' ') || l.split('@').pop()!.split(':')[0], address: l.split('@').pop()!.split(':')[0].split(' ')[0], port: 22, user: l.includes('@') ? l.split('@')[0] : 'root', hostKey: '', addedAt: '' }))
    mhosts = [...mhosts, ...added]; return { added, errors: [] }
  },
  async UpdateHost(id, name) { mhosts = mhosts.map((h) => (h.id === id ? { ...h, name } : h)); return mhosts.find((h) => h.id === id)! },
  async RemoveHost(id) { mhosts = mhosts.filter((h) => h.id !== id) },
  async HostInspect(id) {
    await wait(400)
    const installed = hostInstalled.has(id)
    return { hostname: 'web-01', os: 'Ubuntu 22.04.4 LTS', arch: 'x86_64', kernel: '5.15.0-105-generic', user: 'root', root: true, installed, version: installed ? '2.19.0' : '', running: installed, service: installed ? 'active' : 'inactive',
      installDir: '/usr/local/datakit', confDir: '/usr/local/datakit/conf.d', mainConfPath: '/usr/local/datakit/conf.d/datakit.conf', httpListen: 'localhost:9529', defaultInputs: installed ? ['cpu', 'disk', 'mem', 'net', 'hostobject'] : [],
      mainConf: installed ? 'default_enabled_inputs = ["cpu", "disk", "mem", "net", "hostobject"]\n\n[dataway]\n  urls = ["https://openway.guance.com?token=tkn_demo"]\n\n[http_api]\n  listen = "localhost:9529"\n' : '',
      confFiles: installed ? hostFiles : [] }
  },
  async HostPreview(_id, ch) { await wait(); return { actions: [...(ch.upserts ?? []).map((u) => '写入 ' + u.path), ...(ch.deletes ?? []).map((d) => '删除 ' + d), '重启 datakit 服务'], warnings: [] } },
  async HostApply(_id, ch) {
    await wait(500)
    for (const u of ch.upserts ?? []) { if (u.path.endsWith('datakit.conf')) continue; const doc = parse(u.content) as any; hostFiles = [...hostFiles.filter((f) => f.path !== u.path), { path: u.path, rel: u.path.replace('/usr/local/datakit/conf.d/', ''), content: u.content, inputs: Object.keys(doc.inputs ?? {}), error: '' }] }
    for (const d of ch.deletes ?? []) hostFiles = hostFiles.filter((f) => f.path !== d)
    return { actions: [], warnings: [], backupFile: '/Users/demo/Library/Application Support/datakit-manager/backups/h1' }
  },
  async HostStatus() { return { desired: 1, updated: 1, ready: 1, available: 1, done: true, message: 'datakit 服务运行中（2.19.0）', pods: [] } },
  async HostMetrics(id) { return (await this.Metrics(id, '')).slice(0, 1).map((m) => ({ ...m, pod: 'web-01', node: 'web-01', electedId: '' })) },
  async HostLogs() { return '2026-11-17T00:10:01 INFO io feed 12 points from cpu\n' },
  async HostInstall(id) { await wait(1800); hostInstalled.add(id); return 'Installing DataKit 2.19.0 ...\nInstall Success!\n' },
  async OpenDataDir() {},
}
