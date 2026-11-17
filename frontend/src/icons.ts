// Official integration logos (from the Guance integration library), bundled into the app.
const files = import.meta.glob('./assets/icons/*.{png,svg}', { eager: true, query: '?url', import: 'default' }) as Record<string, string>

const byName: Record<string, string> = {}
for (const [path, url] of Object.entries(files)) {
  byName[path.split('/').pop()!.replace(/\.(png|svg)$/, '')] = url
}

const alias: Record<string, string> = {
  host_processes: 'process', gpu_smi: 'nvidiasmi', prom_remote_write: 'promremote', windows_event: 'winevent', host_change: 'hostobject',
  clickhouse: 'clickhousev1', flink: 'flinkv1', kubernetes: 'container', 'kubernetes-prometheus': 'kubernetesprometheus',
}

/** Logo URL for a collector id / input name / feed name (e.g. "mysql/dbm"), or undefined. */
export function iconFor(name: string): string | undefined {
  const base = name.split(/[\/:]/)[0].toLowerCase()
  return byName[base] ?? byName[alias[base] ?? ''] ?? byName[base.replace(/[-_]/g, '')]
}
