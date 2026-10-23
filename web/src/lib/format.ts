import { locale, t } from '../i18n'

export const fmtNum = (n: number | undefined) => new Intl.NumberFormat(locale()).format(n ?? 0)

export function fmtCompact(n: number) {
  return new Intl.NumberFormat(locale(), { notation: 'compact', maximumFractionDigits: 1 }).format(n)
}

export function fmtBytes(b: number) {
  if (!b) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const i = Math.min(u.length - 1, Math.floor(Math.log(b) / Math.log(1024)))
  return (b / Math.pow(1024, i)).toFixed(i ? 1 : 0) + ' ' + u[i]
}

export function fmtTime(s: string | number | Date, withDate = true) {
  const d = new Date(s)
  return d.toLocaleString(locale(), withDate
    ? { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' }
    : { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

export function fmtAgo(s: string | number | Date) {
  const sec = Math.round((Date.now() - new Date(s).getTime()) / 1000)
  if (sec < 60) return t('{n} s ago', { n: Math.max(sec, 0) })
  if (sec < 3600) return t('{n} min ago', { n: Math.round(sec / 60) })
  if (sec < 86400) return t('{n} h ago', { n: Math.round(sec / 3600) })
  return t('{n} days ago', { n: Math.round(sec / 86400) })
}

export function fmtDuration(sec: number) {
  const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60)
  return [d && `${d}d`, h && `${h}h`, `${m}m`].filter(Boolean).join(' ')
}

export const severities = ['emerg', 'alert', 'crit', 'error', 'warning', 'notice', 'info', 'debug']
export const severityLabel = ['Emergency', 'Alert', 'Critical', 'Error', 'Warning', 'Notice', 'Info', 'Debug']
export function severityClass(s: number) {
  if (s <= 2) return 'bg-red-500/15 text-red-500 ring-red-500/30'
  if (s === 3) return 'bg-orange-500/15 text-orange-500 ring-orange-500/30'
  if (s === 4) return 'bg-amber-500/15 text-amber-500 ring-amber-500/30'
  if (s === 5) return 'bg-sky-500/15 text-sky-500 ring-sky-500/30'
  return 'bg-slate-500/15 text-slate-400 ring-slate-500/30'
}

// Chart colours follow the entity (category), never its rank. Eight primary
// categories get the validated categorical slots (light / dark steps);
// the rest fold into a neutral "Other" series in stacked charts.
const slot = {
  blue: ['#2a78d6', '#3987e5'], orange: ['#eb6834', '#d95926'], aqua: ['#1baf7a', '#199e70'], yellow: ['#eda100', '#c98500'],
  magenta: ['#e87ba4', '#d55181'], green: ['#008300', '#008300'], violet: ['#6250d6', '#9085e9'], red: ['#e34948', '#e66767'],
}
export const categoryMeta: Record<string, { label: string; slot?: keyof typeof slot }> = {
  firewall: { label: 'Firewall', slot: 'red' },
  ips: { label: 'IPS / Threats', slot: 'orange' },
  wifi: { label: 'Wi-Fi', slot: 'blue' },
  dhcp: { label: 'DHCP', slot: 'aqua' },
  switch: { label: 'Switch', slot: 'yellow' },
  vpn: { label: 'VPN', slot: 'green' },
  auth: { label: 'Sign-in', slot: 'violet' },
  admin: { label: 'Admin', slot: 'magenta' },
  dns: { label: 'DNS' },
  client: { label: 'Clients' },
  device: { label: 'Devices' },
  system: { label: 'System' },
  protect: { label: 'Protect' },
  access: { label: 'Access' },
}
export const primaryCategories = Object.keys(categoryMeta).filter((k) => categoryMeta[k].slot)
const isDark = () => document.documentElement.classList.contains('dark')
export const otherColor = () => (isDark() ? '#6b7385' : '#9aa3b2')
export const catColor = (c: string) => {
  const s = categoryMeta[c]?.slot
  return s ? slot[s][isDark() ? 1 : 0] : otherColor()
}
export const catLabel = (c: string) => (categoryMeta[c] ? t(categoryMeta[c].label) : c === 'other' ? t('Other') : c)
export const chartInk = () => (isDark() ? { grid: '#1e2638', axis: '#8591a8', tip: '#151b2c' } : { grid: '#e3e8f0', axis: '#64748b', tip: '#ffffff' })

export function actionClass(a: string) {
  switch (a) {
    case 'block': case 'reject': return 'bg-red-500/15 text-red-500 ring-red-500/30'
    case 'allow': return 'bg-emerald-500/15 text-emerald-500 ring-emerald-500/30'
    case 'alert': return 'bg-orange-500/15 text-orange-500 ring-orange-500/30'
    case 'fail': return 'bg-rose-500/15 text-rose-500 ring-rose-500/30'
    default: return 'bg-slate-500/15 text-slate-400 ring-slate-500/30'
  }
}
export const actionLabel: Record<string, string> = { block: 'Blocked', reject: 'Rejected', allow: 'Allowed', alert: 'Alert', fail: 'Failed', nat: 'NAT', log: 'Log' }

export interface Range { label: string; from: Date; to: Date; key: string }
export const rangePresets: { key: string; label: string; ms: number }[] = [
  { key: '15m', label: 'Last 15 min', ms: 15 * 60e3 },
  { key: '1h', label: 'Last hour', ms: 3600e3 },
  { key: '6h', label: 'Last 6 hours', ms: 6 * 3600e3 },
  { key: '24h', label: 'Last 24 hours', ms: 24 * 3600e3 },
  { key: '7d', label: 'Last 7 days', ms: 7 * 86400e3 },
  { key: '30d', label: 'Last 30 days', ms: 30 * 86400e3 },
  { key: '90d', label: 'Last 90 days', ms: 90 * 86400e3 },
]
export function presetRange(key: string) {
  const p = rangePresets.find((x) => x.key === key) ?? rangePresets[3]
  const to = new Date()
  return { from: new Date(to.getTime() - p.ms), to }
}
