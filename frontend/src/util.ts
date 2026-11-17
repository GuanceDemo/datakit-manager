export function fmtBytes(b: number): string {
  if (!b) return '-'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (b >= 1024 && i < u.length - 1) { b /= 1024; i++ }
  return `${b.toFixed(b >= 100 || i === 0 ? 0 : 1)} ${u[i]}`
}

export function fmtNum(n: number): string {
  if (!n) return '0'
  if (n >= 1e9) return (n / 1e9).toFixed(2) + ' B'
  if (n >= 1e6) return (n / 1e6).toFixed(2) + ' M'
  if (n >= 1e4) return (n / 1e3).toFixed(1) + ' K'
  return String(Math.round(n))
}

export function fmtDuration(sec: number): string {
  if (!sec || sec < 0) return '-'
  const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d} 天 ${h} 小时`
  if (h > 0) return `${h} 小时 ${m} 分`
  if (m > 0) return `${m} 分 ${Math.floor(sec % 60)} 秒`
  return `${Math.floor(sec)} 秒`
}

/** "3 分钟前" for a unix-seconds timestamp. */
export function ago(unixSec: number, now = Date.now() / 1000): string {
  if (!unixSec) return '-'
  const d = Math.max(0, now - unixSec)
  if (d < 60) return `${Math.floor(d)} 秒前`
  if (d < 3600) return `${Math.floor(d / 60)} 分钟前`
  if (d < 86400) return `${Math.floor(d / 3600)} 小时前`
  return `${Math.floor(d / 86400)} 天前`
}

export const categoryName: Record<string, string> = {
  metric: '指标', logging: '日志', tracing: '链路', object: '对象', custom_object: '自定义对象', network: '网络', rum: 'RUM',
  security: '安全巡检', profiling: 'Profiling', keyevent: '事件', dialtesting: '拨测', 'dynamic_dw': '动态', unknown: '未知',
}
