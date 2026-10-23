import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { MapPin, Plus, Trash2, Save, Router as RouterIcon, ShieldAlert, Ban, AlertTriangle } from 'lucide-react'
import { api } from '../api'
import { useAuth } from '../App'
import { Button, Card, Empty, Field, Input, PageHeader, Spinner, cx, useAsync, useToast } from '../components/ui'
import { fmtAgo, fmtCompact } from '../lib/format'
import { t } from '../i18n'

interface Source { ip: string; name: string; site: string; hostname: string; model: string; last_seen: number; first_seen: number }
interface Range { id: number; site: string; cidr: string }
interface SiteView { site: string; ranges: string[]; devices: number; active: number; silent: number; senders: number; last_seen: number; total: number; blocked: number; threats: number; errors: number }

const siteLabel = (s: string) => (s === 'default' ? t('Default') : s)

function SiteCard({ s }: { s: SiteView }) {
  const nav = useNavigate()
  const state = s.silent > 0 ? 'warn' : s.last_seen && Date.now() / 1000 - s.last_seen < 3600 ? 'ok' : 'idle'
  const dot = { ok: 'bg-emerald-500', warn: 'bg-amber-500', idle: 'bg-slate-500' }[state]
  return (
    <div className="panel flex flex-col rounded-2xl p-5 fade-in">
      <div className="flex items-start justify-between gap-3">
        <button onClick={() => nav('/?' + new URLSearchParams({ site: s.site }))} className="min-w-0 cursor-pointer text-left">
          <div className="flex items-center gap-2">
            <span className={cx('size-2.5 shrink-0 rounded-full', dot, state === 'ok' && 'shadow-[0_0_8px] shadow-emerald-500/60')} />
            <span className="truncate font-semibold hover:text-indigo-400">{siteLabel(s.site)}</span>
          </div>
          <div className="mt-0.5 text-xs text-muted">{s.last_seen ? t('Last log {ago}', { ago: fmtAgo(s.last_seen * 1000) }) : s.total ? t('No senders left at this site') : t('No logs yet')}</div>
        </button>
        <button onClick={() => nav('/devices?' + new URLSearchParams({ site: s.site }))} className="flex shrink-0 cursor-pointer items-center gap-1.5 rounded-lg px-2 py-1 text-xs text-muted ring-1 ring-[var(--border)] hover:text-[var(--text)]">
          <RouterIcon className="size-3.5" />{s.devices === 1 ? t('1 device') : t('{n} devices', { n: s.devices })}
        </button>
      </div>
      {s.silent > 0 && (
        <button onClick={() => nav('/devices?' + new URLSearchParams({ site: s.site, status: 'silent' }))} className="mt-3 flex cursor-pointer items-center gap-2 rounded-lg bg-amber-500/10 px-3 py-2 text-left text-xs text-amber-500 ring-1 ring-amber-500/30">
          <AlertTriangle className="size-4 shrink-0" />{s.silent === 1 ? t('1 device has stopped sending logs') : t('{n} devices have stopped sending logs', { n: s.silent })}
        </button>
      )}
      <div className="mt-4 grid grid-cols-4 gap-2 text-center">
        {[
          { l: 'Logs 24h', v: s.total, c: '' },
          { l: 'Blocked', v: s.blocked, c: 'text-rose-500', i: <Ban className="size-3" /> },
          { l: 'IPS', v: s.threats, c: 'text-fuchsia-500', i: <ShieldAlert className="size-3" /> },
          { l: 'Errors', v: s.errors, c: 'text-orange-500' },
        ].map((x) => (
          <div key={x.l} className="rounded-xl bg-[var(--panel-2)] px-1 py-2">
            <div className={cx('text-lg font-semibold tabular-nums', x.v > 0 && x.c)}>{fmtCompact(x.v)}</div>
            <div className="flex items-center justify-center gap-1 text-[10px] uppercase tracking-wide text-muted">{x.i}{t(x.l)}</div>
          </div>
        ))}
      </div>
      <div className="mt-3 flex flex-wrap gap-1.5 text-[11px] text-muted">
        <span>{t('{n} active', { n: s.active })}</span><span>·</span><span>{s.senders === 1 ? t('1 sender') : t('{n} senders', { n: s.senders })}</span>
        {s.ranges.length > 0 && <><span>·</span><span className="font-mono">{s.ranges.join(', ')}</span></>}
      </div>
    </div>
  )
}

export default function Sites() {
  const { user } = useAuth()
  const isAdmin = user.role === 'admin'
  const overview = useAsync(() => api.get<SiteView[]>('/sites/overview'))
  return (
    <>
      <PageHeader title={t('Sites')} subtitle={t('Status of all your sites over the last 24 hours')} icon={<MapPin className="size-5" />}
        actions={<Button onClick={overview.reload} loading={overview.loading}>{t('Refresh')}</Button>} />
      {overview.loading && !overview.data ? <div className="grid place-items-center py-16"><Spinner /></div>
        : !overview.data?.length ? <Card><Empty icon={<MapPin className="size-8" />} title={t('No sites yet')} text={t('Sites appear here as soon as UniFi devices send logs. Map networks to a site below.')} /></Card>
        : <div className="grid grid-cols-1 gap-4 md:grid-cols-2 2xl:grid-cols-3">{overview.data.map((s) => <SiteCard key={s.site} s={s} />)}</div>}
      {isAdmin && <div className="mt-8"><SiteConfig onChange={overview.reload} /></div>}
    </>
  )
}

function SiteConfig({ onChange }: { onChange: () => void }) {
  const toast = useToast()
  const sources = useAsync(() => api.get<Source[]>('/sources'))
  const ranges = useAsync(() => api.get<Range[]>('/site-ranges'))
  const [nr, setNr] = useState({ site: '', cidr: '' })
  const [edits, setEdits] = useState<Record<string, { name: string; site: string }>>({})

  const addRange = async () => {
    try {
      await api.post('/site-ranges', nr)
      setNr({ site: '', cidr: '' })
      ranges.reload(); sources.reload(); onChange()
      toast('ok', t('Network added'))
    } catch (e) { toast('err', (e as Error).message) }
  }
  const saveSource = async (ip: string) => {
    const e = edits[ip]
    try {
      await api.put('/sources/' + encodeURIComponent(ip), e)
      setEdits(({ [ip]: _, ...rest }) => rest)
      sources.reload(); onChange()
      toast('ok', t('Saved'))
    } catch (err) { toast('err', (err as Error).message) }
  }
  const siteNames = [...new Set([...(ranges.data ?? []).map((r) => r.site), ...(sources.data ?? []).map((s) => s.site)])].filter((s) => s && s !== 'default')

  return (
    <>
      <h2 className="mb-3 text-sm font-semibold uppercase tracking-wide text-muted">{t('Assignment')}</h2>
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-5">
        <Card className="xl:col-span-2" title={t('Networks per site')} subtitle={t('Senders from these networks (e.g. the WAN address of a branch office) are automatically assigned to this site')}>
          <div className="mb-4 grid grid-cols-[1fr_1fr_auto] items-end gap-2">
            <Field label={t('Site')}><Input list="sites" value={nr.site} onChange={(e) => setNr({ ...nr, site: e.target.value })} placeholder={t('Head office')} /></Field>
            <Field label={t('Network (CIDR)')}><Input value={nr.cidr} onChange={(e) => setNr({ ...nr, cidr: e.target.value })} placeholder="84.12.34.56/32" /></Field>
            <Button variant="primary" onClick={addRange} disabled={!nr.site || !nr.cidr} icon={<Plus className="size-4" />} />
          </div>
          <datalist id="sites">{siteNames.map((s) => <option key={s} value={s} />)}</datalist>
          {!ranges.data?.length ? <p className="py-4 text-center text-sm text-muted">{t('No networks configured yet.')}</p> : (
            <ul className="divide-y divide-[var(--border)]">
              {ranges.data.map((r) => (
                <li key={r.id} className="flex items-center justify-between py-2.5 text-sm">
                  <span><span className="font-medium">{r.site}</span> <span className="ml-2 font-mono text-xs text-muted">{r.cidr}</span></span>
                  <button onClick={async () => { await api.del('/site-ranges/' + r.id); ranges.reload(); onChange() }} className="cursor-pointer rounded-lg p-1.5 text-red-500 hover:bg-red-500/10"><Trash2 className="size-4" /></button>
                </li>
              ))}
            </ul>
          )}
        </Card>

        <Card className="xl:col-span-3" title={t('Senders')} subtitle={t('Every IP address sending syslog. Give it a name and site.')} pad={false}>
          {sources.loading && !sources.data ? <div className="grid place-items-center py-16"><Spinner /></div>
            : !sources.data?.length ? <Empty icon={<MapPin className="size-8" />} title={t('No senders yet')} text={t('A UniFi device appears here as soon as it sends logs.')} />
            : (
              <div className="overflow-x-auto scroll-thin">
                <table className="w-full text-sm">
                  <thead className="text-left text-[11px] uppercase tracking-wide text-muted"><tr className="border-b border-app">
                    <th className="px-5 py-3 font-medium">{t('IP address')}</th><th className="px-3 py-3 font-medium">{t('Name')}</th><th className="px-3 py-3 font-medium">{t('Site')}</th><th className="px-3 py-3 font-medium">{t('Last seen')}</th><th className="px-5 py-3" />
                  </tr></thead>
                  <tbody>
                    {sources.data.map((s) => {
                      const e = edits[s.ip] ?? { name: s.name, site: s.site === 'default' ? '' : s.site }
                      const dirty = !!edits[s.ip]
                      return (
                        <tr key={s.ip} className="border-b border-app/60">
                          <td className="px-5 py-2"><div className="font-mono text-xs">{s.ip}</div><div className="text-[11px] text-muted">{s.hostname}</div></td>
                          <td className="px-3 py-2"><Input value={e.name} placeholder={s.hostname} onChange={(ev) => setEdits({ ...edits, [s.ip]: { ...e, name: ev.target.value } })} className="!py-1.5" /></td>
                          <td className="px-3 py-2"><Input list="sites" value={e.site} placeholder={t('Default')} onChange={(ev) => setEdits({ ...edits, [s.ip]: { ...e, site: ev.target.value } })} className="!py-1.5" /></td>
                          <td className="px-3 py-2 whitespace-nowrap text-xs text-muted">{fmtAgo(s.last_seen * 1000)}</td>
                          <td className="px-5 py-2 text-right">{dirty && <Button variant="primary" onClick={() => saveSource(s.ip)} icon={<Save className="size-4" />} />}</td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
                <p className="px-5 py-3 text-xs text-muted">{t('A changed site applies to new messages; existing logs keep their original site.')}</p>
              </div>
            )}
        </Card>
      </div>
    </>
  )
}
