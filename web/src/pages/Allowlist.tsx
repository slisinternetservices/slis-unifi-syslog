import { useEffect, useState } from 'react'
import { ListChecks, Save, Plus, Trash2, Router as RouterIcon, ShieldAlert } from 'lucide-react'
import { api } from '../api'
import { Badge, Button, Card, Empty, Input, PageHeader, Spinner, Toggle, useToast } from '../components/ui'
import { fmtAgo, fmtNum } from '../lib/format'
import { t } from '../i18n'

interface Entry { cidr: string; note: string }
interface Settings { enabled: boolean; entries: Entry[] }
interface Rejected { ip: string; count: number; first: number; last: number; protocol: string }
interface State { settings: Settings; rejected: Rejected[]; rejected_total: number }
interface Source { ip: string; name: string; hostname: string; site: string }

export default function Allowlist() {
  const toast = useToast()
  const [st, setSt] = useState<State>()
  const [s, setS] = useState<Settings>()
  const [busy, setBusy] = useState(false)
  const load = () => api.get<State>('/settings/allowlist').then((r) => { setSt(r); setS((cur) => cur ?? r.settings) })
  useEffect(() => { load() }, [])
  if (!s || !st) return <div className="grid place-items-center py-24"><Spinner /></div>
  const add = (cidr: string, note = '') => {
    if (s.entries.some((e) => e.cidr === cidr)) return
    setS({ ...s, entries: [...s.entries, { cidr, note }] })
  }
  const addSenders = async () => {
    const src = await api.get<Source[]>('/sources')
    const next = [...s.entries]
    for (const x of src) if (!next.some((e) => e.cidr === x.ip)) next.push({ cidr: x.ip, note: x.name || x.hostname || x.site })
    setS({ ...s, entries: next })
    toast('ok', t('{n} known senders added', { n: next.length - s.entries.length }))
  }
  const save = async () => {
    setBusy(true)
    try { setS(await api.put<Settings>('/settings/allowlist', s)); toast('ok', t('Allowlist saved')); load() } catch (e) { toast('err', (e as Error).message) } finally { setBusy(false) }
  }
  return (
    <>
      <PageHeader title={t('Source allowlist')} subtitle={t('Only accept syslog from known IP addresses or networks')} icon={<ListChecks className="size-5" />}
        actions={<Button variant="primary" loading={busy} onClick={save} icon={<Save className="size-4" />}>{t('Save')}</Button>} />
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-3">
        <Card className="xl:col-span-2" title={t('Allowed senders')}
          actions={<>
            <Button variant="ghost" onClick={addSenders} icon={<RouterIcon className="size-4" />}>{t('Add known senders')}</Button>
            <Button onClick={() => add('')} icon={<Plus className="size-4" />}>{t('Add')}</Button>
          </>}>
          <div className="space-y-4">
            <Toggle checked={s.enabled} onChange={(enabled) => setS({ ...s, enabled })} label={t('Only accept listed senders')}
              description={t('Messages from other addresses are dropped on UDP and TCP and counted below. Off: every sender is accepted.')} />
            {!s.entries.length ? (
              <p className="text-sm text-muted">{t('No entries yet. Add known senders to start from the devices that already send logs.')}</p>
            ) : (
              <div className="space-y-2">
                {s.entries.map((e, i) => {
                  const set = (p: Partial<Entry>) => setS({ ...s, entries: s.entries.map((x, j) => (j === i ? { ...x, ...p } : x)) })
                  return (
                    <div key={i} className="grid grid-cols-[1fr_1fr_auto] items-center gap-2">
                      <Input value={e.cidr} onChange={(ev) => set({ cidr: ev.target.value.trim() })} placeholder={t('e.g. {a} or {b}', { a: '10.1.0.0/16', b: '203.0.113.7' })} className="font-mono" />
                      <Input value={e.note} onChange={(ev) => set({ note: ev.target.value })} placeholder={t('Note, e.g. site or device')} />
                      <button aria-label={t('Remove')} onClick={() => setS({ ...s, entries: s.entries.filter((_, j) => j !== i) })} className="cursor-pointer rounded-lg p-2 text-red-500 hover:bg-red-500/10"><Trash2 className="size-4" /></button>
                    </div>
                  )
                })}
              </div>
            )}
            <p className="text-xs text-muted">{t('Use the address the server sees: for sites behind NAT that is the public WAN address of the site.')}</p>
          </div>
        </Card>
        <Card title={t('Rejected senders')} subtitle={t('{n} messages rejected since the last restart', { n: fmtNum(st.rejected_total) })} pad={false}>
          {!st.rejected.length ? (
            <Empty icon={<ShieldAlert className="size-6" />} title={t('Nothing rejected')} text={s.enabled ? t('Every sender is on the list.') : t('The allowlist is off.')} />
          ) : (
            <ul className="divide-y divide-[var(--border)]">
              {st.rejected.map((r) => (
                <li key={r.ip} className="flex items-center gap-3 px-5 py-3 text-sm">
                  <div className="min-w-0 flex-1">
                    <div className="font-mono">{r.ip}</div>
                    <div className="text-xs text-muted">{fmtNum(r.count)} × · {fmtAgo(r.last * 1000)} · <Badge>{r.protocol.toUpperCase()}</Badge></div>
                  </div>
                  <Button variant="ghost" onClick={() => add(r.ip)} disabled={s.entries.some((e) => e.cidr === r.ip)} icon={<Plus className="size-4" />}>{t('Allow')}</Button>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>
    </>
  )
}
