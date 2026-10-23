import { useEffect, useState } from 'react'
import { Check, Copy, Download } from 'lucide-react'
import { api, type Role } from '../api'
import { t } from '../i18n'
import { Badge, Button, cx, useToast } from './ui'

export const roleLabel = (r: string) => (r === 'admin' ? t('Admin') : r === 'analyst' ? t('Analyst') : t('Viewer'))

export const roleOptions = () => [
  { value: 'viewer', label: t('Viewer: view logs and dashboards (read-only)') },
  { value: 'analyst', label: t('Analyst: also export logs and manage categories') },
  { value: 'admin', label: t('Admin: full access to all sites') },
]

export function RoleBadge({ role }: { role: Role | string }) {
  const cls = role === 'admin' ? 'bg-indigo-500/15 text-indigo-400 ring-indigo-500/30' : role === 'analyst' ? 'bg-cyan-500/15 text-cyan-500 ring-cyan-500/30' : ''
  return <Badge className={cls}>{roleLabel(role)}</Badge>
}

export const siteName = (s: string) => (s === 'default' ? t('Default') : s)

/** Lists the sites as toggle chips; nothing selected means all sites. */
export function SitePicker({ value, onChange, disabled }: { value: string[]; onChange: (v: string[]) => void; disabled?: boolean }) {
  const [sites, setSites] = useState<string[]>([])
  useEffect(() => { api.get<string[]>('/sites').then(setSites).catch(() => {}) }, [])
  const all = [...new Set([...sites, ...value])]
  const toggle = (s: string) => onChange(value.includes(s) ? value.filter((x) => x !== s) : [...value, s])
  return (
    <div className="flex flex-wrap gap-1.5">
      <button type="button" disabled={disabled} onClick={() => onChange([])}
        className={cx('rounded-lg px-2.5 py-1 text-xs ring-1 transition cursor-pointer disabled:cursor-not-allowed disabled:opacity-60',
          value.length === 0 ? 'bg-indigo-500/15 text-indigo-400 ring-indigo-500/40' : 'ring-[var(--border)] text-muted hover:text-[var(--text)]')}>
        {t('All sites')}
      </button>
      {all.map((s) => (
        <button type="button" key={s} disabled={disabled} onClick={() => toggle(s)}
          className={cx('inline-flex items-center gap-1 rounded-lg px-2.5 py-1 text-xs ring-1 transition cursor-pointer disabled:cursor-not-allowed disabled:opacity-60',
            value.includes(s) ? 'bg-indigo-500/15 text-indigo-400 ring-indigo-500/40' : 'ring-[var(--border)] text-muted hover:text-[var(--text)]')}>
          {value.includes(s) && <Check className="size-3" />}{siteName(s)}
        </button>
      ))}
    </div>
  )
}

export const sitesSummary = (sites: string[]) => (sites.length ? sites.map(siteName).join(', ') : t('All sites'))

/** Shows freshly generated recovery codes with copy and download. */
export function RecoveryCodes({ codes }: { codes: string[] }) {
  const toast = useToast()
  const text = codes.join('\n')
  const download = () => {
    const a = document.createElement('a')
    a.href = URL.createObjectURL(new Blob([text + '\n'], { type: 'text/plain' }))
    a.download = 'recovery-codes.txt'
    a.click()
    URL.revokeObjectURL(a.href)
  }
  return (
    <div className="space-y-3">
      <p className="text-sm text-muted">{t('Store these recovery codes somewhere safe. Each code signs you in once if you lose your authenticator. They are shown only now.')}</p>
      <div className="grid grid-cols-2 gap-2 rounded-xl bg-[var(--panel-2)] p-4 font-mono text-sm">
        {codes.map((c) => <span key={c}>{c}</span>)}
      </div>
      <div className="flex gap-2">
        <Button onClick={() => { navigator.clipboard.writeText(text); toast('ok', t('Copied')) }} icon={<Copy className="size-4" />}>{t('Copy')}</Button>
        <Button onClick={download} icon={<Download className="size-4" />}>{t('Download')}</Button>
      </div>
    </div>
  )
}
