import { useEffect, useState, type ReactNode } from 'react'
import { Settings as SettingsIcon, Save, Timer } from 'lucide-react'
import { api } from '../api'
import { useAuth } from '../App'
import { Badge, Button, Card, Field, Input, PageHeader, Select, Spinner, useAsync, useToast } from '../components/ui'
import { catColor, catLabel, categoryMeta, fmtAgo, fmtNum } from '../lib/format'
import { languages, t } from '../i18n'

interface General { timezone: string; retention_days: number; category_retention: Record<string, number>; base_url: string; instance_name: string; language: string }
interface Usage { category: string; rows: number; oldest: string }
const zones = ['Europe/Amsterdam', 'Europe/Brussels', 'Europe/London', 'Europe/Berlin', 'UTC', 'America/New_York', 'Asia/Singapore']

export default function SettingsPage() {
  const toast = useToast()
  const { reloadStatus } = useAuth()
  const [g, setG] = useState<General>()
  const [busy, setBusy] = useState(false)
  useEffect(() => { api.get<General>('/settings/general').then((g) => setG({ ...g, category_retention: g.category_retention ?? {} })) }, [])
  const usage = useAsync(() => api.get<Usage[]>('/settings/retention/usage'))
  if (!g) return <div className="grid place-items-center py-24"><Spinner /></div>
  const save = async () => {
    setBusy(true)
    try { const saved = await api.put<General>('/settings/general', g); setG(saved); toast('ok', t('Settings saved')); reloadStatus(); usage.reload() } catch (e) { toast('err', (e as Error).message) } finally { setBusy(false) }
  }
  return (
    <>
      <PageHeader title={t('Settings')} subtitle={t('General server settings')} icon={<SettingsIcon className="size-5" />}
        actions={<Button variant="primary" loading={busy} onClick={save} icon={<Save className="size-4" />}>{t('Save')}</Button>} />
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-2">
        <Card title={t('General')}>
          <div className="grid gap-4 md:grid-cols-2">
            <Field label={t('Server name')}><Input value={g.instance_name} onChange={(e) => setG({ ...g, instance_name: e.target.value })} /></Field>
            <Field label={t('Device time zone')} hint={t('UniFi sends timestamps without a zone (RFC 3164)')}>
              <Input list="tz" value={g.timezone} onChange={(e) => setG({ ...g, timezone: e.target.value })} />
              <datalist id="tz">{zones.map((z) => <option key={z} value={z} />)}</datalist>
            </Field>
            <Field label={t('Log retention (days)')} hint={t('Older logs are deleted automatically, one day at a time')}><Input type="number" min={1} max={3650} value={g.retention_days} onChange={(e) => setG({ ...g, retention_days: Number(e.target.value) })} /></Field>
            <Field label={t('Public base URL')} hint={t('Required for SSO behind a reverse proxy, e.g. {example}', { example: 'https://syslog.example.com' })}><Input value={g.base_url} onChange={(e) => setG({ ...g, base_url: e.target.value.trim() })} placeholder="http://server:8080" /></Field>
            <Field label={t('Default language')} hint={t('For the interface and e-mails, unless a user picks their own language')}>
              <Select value={g.language || 'en'} onChange={(language) => setG({ ...g, language })} options={languages.map(([value, label]) => ({ value, label }))} />
            </Field>
          </div>
        </Card>
        <RetentionCard g={g} setG={setG} usage={usage.data} />
        <Card title={t('Connect UniFi')} subtitle={t('How to send logs to this server')}>
          <ol className="list-decimal space-y-2 pl-4 text-sm text-muted">
            <li>{rich(t('Open the UniFi Network app → {path} (depending on version: {alt}).'), { path: 'Settings → CyberSecure / System → Traffic & Firewall logging', alt: 'Settings → System → Logging' })}</li>
            <li>{rich(t('Choose {siem} or {remote} and enter the IP address of this server, port {port}.'), { siem: 'SIEM Server', remote: 'Remote Syslog', port: <span className="font-mono">514</span> })}</li>
            <li>{t('Set the log level to Info or Debug and enable firewall logging on the rules you want.')}</li>
            <li>{rich(t('For sites behind NAT: map the WAN IP to the site under {sites}.'), { sites: t('Sites') })}</li>
          </ol>
        </Card>
      </div>
    </>
  )
}

function RetentionCard({ g, setG, usage }: { g: General; setG: (g: General) => void; usage?: Usage[] }) {
  const per = g.category_retention
  const cats = [...new Set([...Object.keys(categoryMeta), ...(usage ?? []).map((u) => u.category), ...Object.keys(per)])]
    .filter((c) => c !== '')
    .sort((a, b) => (usage?.find((u) => u.category === b)?.rows ?? 0) - (usage?.find((u) => u.category === a)?.rows ?? 0) || catLabel(a).localeCompare(catLabel(b)))
  const setDays = (c: string, v: string) => {
    const next = { ...per }
    if (v === '') delete next[c]
    else next[c] = Number(v)
    setG({ ...g, category_retention: next })
  }
  return (
    <Card className="xl:col-span-2" title={<span className="flex items-center gap-2"><Timer className="size-4 text-indigo-400" />{t('Retention per category')}</span>}
      subtitle={t('Keep some categories shorter or longer than the default of {n} days. Empty = default.', { n: g.retention_days })}>
      <div className="overflow-x-auto scroll-thin">
        <table className="w-full text-sm">
          <thead className="text-left text-[11px] uppercase tracking-wide text-muted">
            <tr className="border-b border-app">
              <th className="py-2 pr-3 font-medium">{t('Category')}</th>
              <th className="px-3 py-2 text-right font-medium">{t('Stored')}</th>
              <th className="px-3 py-2 font-medium">{t('Oldest')}</th>
              <th className="py-2 pl-3 font-medium">{t('Keep (days)')}</th>
            </tr>
          </thead>
          <tbody>
            {cats.map((c) => {
              const u = usage?.find((x) => x.category === c)
              const days = per[c]
              return (
                <tr key={c} className="border-b border-app/60">
                  <td className="py-2 pr-3"><Badge color={catColor(c)}>{catLabel(c)}</Badge></td>
                  <td className="px-3 py-2 text-right tabular-nums text-muted">{u ? fmtNum(u.rows) : '–'}</td>
                  <td className="px-3 py-2 text-muted">{u ? fmtAgo(u.oldest) : '–'}</td>
                  <td className="py-2 pl-3">
                    <div className="flex items-center gap-2">
                      <Input type="number" min={1} max={3650} className="!w-28" value={days ?? ''} placeholder={String(g.retention_days)} onChange={(e) => setDays(c, e.target.value)} />
                      {days !== undefined && <span className="text-xs text-muted">{days > g.retention_days ? t('longer than default') : days < g.retention_days ? t('shorter than default') : ''}</span>}
                    </div>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
      <p className="mt-3 text-xs text-muted">{t('ClickHouse removes expired logs in the background, usually within a few hours of saving. Dashboards keep their hourly totals for 400 days.')}</p>
    </Card>
  )
}

/** Fills {name} placeholders in a translated sentence with highlighted parts. */
function rich(text: string, parts: Record<string, ReactNode>) {
  return text.split(/(\{\w+\})/).map((p, i) => {
    const k = p.match(/^\{(\w+)\}$/)?.[1]
    return k && k in parts ? <span key={i} className="text-[var(--text)]">{parts[k]}</span> : p
  })
}
