import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Router as RouterIcon, Search, Wifi, Network, Shield, Server, Pencil, Trash2, Settings2, BellOff, ArrowUpCircle, Layers } from 'lucide-react'
import { api } from '../api'
import { useAuth } from '../App'
import { useUrlState } from '../components/Filters'
import { Badge, Button, Card, Empty, Field, Input, Modal, PageHeader, Select, Spinner, Stat, Toggle, cx, useAsync, useToast } from '../components/ui'
import { fmtAgo, fmtNum, fmtTime } from '../lib/format'
import { t } from '../i18n'

interface Device {
  key: string; mac: string; hostname: string; model: string; version: string; prev_version: string; version_changed: number
  name: string; notes: string; site: string; source_ip: string; monitored: boolean; first_seen: number; last_seen: number
  status: 'active' | 'silent' | 'inactive'; count: number; errors: number
  controller_name?: string; updatable?: boolean
}
interface InventorySettings { silent_minutes: number; notify: boolean; firmware_notify: boolean; recipients: string }
interface FirmwareReport {
  models: { model: string; latest: string; mixed: boolean; versions: { version: string; count: number }[] }[]
  outdated: { key: string; name: string; model: string; site: string; version: string; latest: string; reason: 'controller' | 'newer' }[]
}

function kind(d: Device) {
  const m = (d.model || d.hostname).toUpperCase()
  if (/^(U6|U7|UAP|UAL|UK|E7|U-?LTE|UWB|UMA|AC)/.test(m)) return { label: 'Access point', icon: <Wifi className="size-4" /> }
  if (/^(US|USW|USL|ECS)/.test(m)) return { label: 'Switch', icon: <Network className="size-4" /> }
  if (/(UDM|UDR|UXG|UCG|USG|GATEWAY|UDW|EFG|CLOUD)/.test(m)) return { label: 'Gateway', icon: <Shield className="size-4" /> }
  return { label: 'Other', icon: <Server className="size-4" /> }
}

const siteLabel = (s: string) => (!s || s === 'default' ? t('Default') : s)
const recentUpgrade = (d: Device) => d.version_changed > 0 && Date.now() / 1000 - d.version_changed < 7 * 86400

function StatusDot({ d }: { d: Device }) {
  const c = { active: 'bg-emerald-500 shadow-[0_0_8px] shadow-emerald-500/60', silent: 'bg-amber-500', inactive: 'bg-slate-500' }[d.status]
  const tip = { active: 'Active', silent: 'No longer sending logs', inactive: 'Inactive (not monitored)' }[d.status]
  return <span title={t(tip)} className={cx('inline-block size-2.5 rounded-full', c)} />
}

export default function Devices() {
  const { user } = useAuth()
  const isAdmin = user.role === 'admin'
  const nav = useNavigate()
  const [st, set] = useUrlState({ days: '7', site: '', status: '', q: '' })
  const { data, loading, reload } = useAsync(() => api.get<Device[]>('/devices?days=' + st.days), [st.days])
  const fw = useAsync(() => api.get<FirmwareReport>('/firmware'))
  const [edit, setEdit] = useState<Device | null>(null)
  const [settings, setSettings] = useState(false)

  const all = data ?? []
  const sites = useMemo(() => [...new Set(all.map((d) => d.site || 'default'))].sort(), [all])
  const list = useMemo(() => all.filter((d) =>
    (!st.site || (d.site || 'default') === st.site) &&
    (!st.status || d.status === st.status) &&
    (!st.q || `${d.name} ${d.hostname} ${d.model} ${d.mac} ${d.source_ip} ${d.site} ${d.version}`.toLowerCase().includes(st.q.toLowerCase()))), [all, st.site, st.status, st.q])

  // Firmware per model from the server: it also knows what the UniFi
  // controller says about available updates.
  const outdated = useMemo(() => new Map((fw.data?.outdated ?? []).map((o) => [o.key, o])), [fw.data])
  const firmware = useMemo(() => [...(fw.data?.models ?? [])].filter((m) => !st.site || list.some((d) => d.model === m.model))
    .sort((a, b) => Number(b.mixed) - Number(a.mixed) || a.model.localeCompare(b.model)), [fw.data, list, st.site])
  const mixed = firmware.filter((f) => f.mixed).length
  const count = (s: Device['status']) => list.filter((d) => d.status === s).length

  return (
    <>
      <PageHeader title={t('Devices')} subtitle={t('Inventory of all UniFi gateways, switches and access points')} icon={<RouterIcon className="size-5" />}
        actions={<>
          <div className="relative"><Search className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted" /><input value={st.q} onChange={(e) => set({ q: e.target.value })} placeholder={t('Search…')} className="input !pl-9 w-56" /></div>
          <Select value={st.site} onChange={(site) => set({ site })} className="w-44" options={[{ value: '', label: t('All sites') }, ...sites.map((s) => ({ value: s, label: siteLabel(s) }))]} />
          <Select value={st.days} onChange={(days) => set({ days })} className="w-44" options={[{ value: '1', label: t('Messages 24 hours') }, { value: '7', label: t('Messages 7 days') }, { value: '30', label: t('Messages 30 days') }]} />
          {isAdmin && <Button onClick={() => setSettings(true)} icon={<Settings2 className="size-4" />}>{t('Monitoring')}</Button>}
        </>} />

      <div className="mb-6 grid grid-cols-2 gap-4 xl:grid-cols-4">
        {[
          { s: '', label: 'Devices', value: list.length, accent: '#6366f1', icon: <RouterIcon className="size-5" /> },
          { s: 'active', label: 'Active', value: count('active'), accent: '#10b981', icon: <Wifi className="size-5" /> },
          { s: 'silent', label: 'Silent', value: count('silent'), accent: '#f59e0b', icon: <BellOff className="size-5" />, sub: 'No longer sending logs' },
        ].map((x) => (
          <button key={x.label} onClick={() => set({ status: st.status === x.s ? '' : x.s })} className={cx('cursor-pointer rounded-2xl text-left', st.status === x.s && x.s && 'ring-2 ring-indigo-500/50')}>
            <Stat label={t(x.label)} value={fmtNum(x.value)} accent={x.accent} icon={x.icon} sub={x.sub && t(x.sub)} />
          </button>
        ))}
        <Stat label={t('Firmware')} value={outdated.size ? (outdated.size === 1 ? t('1 outdated') : t('{n} outdated', { n: outdated.size })) : mixed ? (mixed === 1 ? t('1 model') : t('{n} models', { n: mixed })) : t('Consistent')}
          accent={outdated.size || mixed ? '#f59e0b' : '#06b6d4'} icon={<Layers className="size-5" />}
          sub={outdated.size ? t('devices can be updated') : mixed ? t('with mixed versions') : t('Each model runs one version')} />
      </div>

      <div className="grid grid-cols-1 gap-6 2xl:grid-cols-4">
        <Card pad={false} className="2xl:col-span-3">
          {loading && !data ? <div className="grid place-items-center py-20"><Spinner /></div>
            : !list.length ? <Empty icon={<RouterIcon className="size-8" />} title={all.length ? t('No devices found') : t('No devices yet')} text={all.length ? t('Adjust the filters.') : t('In the UniFi Network app, go to Settings → System → Logging and configure a remote syslog server with the IP of this server, port 514.')} />
            : (
              <div className="overflow-x-auto scroll-thin">
                <table className="w-full text-sm">
                  <thead className="text-left text-[11px] uppercase tracking-wide text-muted"><tr className="border-b border-app">
                    <th className="w-8 py-3 pl-5" /><th className="px-3 py-3 font-medium">{t('Device')}</th><th className="px-3 py-3 font-medium">{t('Type')}</th><th className="px-3 py-3 font-medium">{t('Firmware')}</th>
                    <th className="px-3 py-3 font-medium">MAC</th><th className="px-3 py-3 font-medium">{t('Site')}</th>
                    <th className="px-3 py-3 font-medium text-right">{t('Messages')}</th><th className="px-3 py-3 font-medium text-right">{t('Errors')}</th><th className="px-3 py-3 font-medium">{t('Last seen')}</th>
                    {isAdmin && <th className="px-5 py-3" />}
                  </tr></thead>
                  <tbody>
                    {list.map((d) => {
                      const k = kind(d)
                      return (
                        <tr key={d.key} onClick={() => nav('/logs?' + new URLSearchParams({ q: d.mac ? `mac:${d.mac}` : `host:"${d.hostname}"`, preset: '24h' }))} className="group cursor-pointer border-b border-app/60 hover:bg-[var(--panel-2)]">
                          <td className="py-2.5 pl-5"><StatusDot d={d} /></td>
                          <td className="px-3 py-2.5">
                            <div className="font-medium">{d.name || d.controller_name || d.hostname}</div>
                            <div className="text-xs text-muted">{[(d.name || d.controller_name) && d.hostname, d.model !== d.hostname && d.model].filter(Boolean).join(' · ')}{!d.monitored && <span className="ml-1 inline-flex items-center gap-1"><BellOff className="size-3" />{t('not monitored')}</span>}</div>
                            {d.notes && <div className="mt-0.5 max-w-xs truncate text-[11px] italic text-muted">{d.notes}</div>}
                          </td>
                          <td className="px-3 py-2.5 whitespace-nowrap"><span className="inline-flex items-center gap-1.5 text-muted">{k.icon}{t(k.label)}</span></td>
                          <td className="px-3 py-2.5 whitespace-nowrap">
                            <span className="font-mono text-xs">{d.version}</span>
                            {outdated.has(d.key) && <span title={outdated.get(d.key)!.reason === 'controller' ? t('UniFi Network offers an update') : t('Newer version {version} runs on other devices of this model', { version: outdated.get(d.key)!.latest })} className="ml-1.5 inline-flex"><Badge className="bg-amber-500/15 text-amber-500 ring-amber-500/30"><ArrowUpCircle className="size-3" />{t('update available')}</Badge></span>}
                            {recentUpgrade(d) && <span title={t('Was {version}, changed on {time}', { version: d.prev_version, time: fmtTime(d.version_changed * 1000) })} className="ml-1.5 inline-flex"><Badge className="bg-cyan-500/15 text-cyan-500 ring-cyan-500/30"><ArrowUpCircle className="size-3" />{t('updated')}</Badge></span>}
                          </td>
                          <td className="px-3 py-2.5 font-mono text-xs">{d.mac}</td>
                          <td className="px-3 py-2.5"><div>{siteLabel(d.site)}</div><div className="font-mono text-[11px] text-muted">{d.source_ip}</div></td>
                          <td className="px-3 py-2.5 text-right tabular-nums">{fmtNum(d.count)}</td>
                          <td className="px-3 py-2.5 text-right tabular-nums">{d.errors ? <span className="text-orange-500">{fmtNum(d.errors)}</span> : 0}</td>
                          <td className="px-3 py-2.5 whitespace-nowrap">
                            {d.status === 'silent' ? <Badge className="bg-amber-500/15 text-amber-500 ring-amber-500/30">{fmtAgo(d.last_seen * 1000)}</Badge>
                              : <span className={d.status === 'active' ? 'text-emerald-500' : 'text-muted'}>{fmtAgo(d.last_seen * 1000)}</span>}
                          </td>
                          {isAdmin && (
                            <td className="px-5 py-2.5 text-right" onClick={(e) => e.stopPropagation()}>
                              <button title={t('Edit')} onClick={() => setEdit(d)} className="cursor-pointer rounded-lg p-1.5 text-muted opacity-0 transition hover:bg-[var(--panel)] hover:text-[var(--text)] group-hover:opacity-100"><Pencil className="size-4" /></button>
                            </td>
                          )}
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            )}
        </Card>

        <Card title={t('Firmware per model')} subtitle={t('Models with multiple versions are listed first')}>
          {!firmware.length ? <p className="py-4 text-center text-sm text-muted">{t('No firmware data.')}</p> : (
            <ul className="space-y-3">
              {firmware.map((f) => (
                <li key={f.model}>
                  <div className="flex items-center justify-between text-sm">
                    <span className="font-medium">{f.model}</span>
                    {f.mixed && <Badge className="bg-amber-500/15 text-amber-500 ring-amber-500/30">{t('{n} versions', { n: f.versions.length })}</Badge>}
                  </div>
                  <div className="mt-1 flex flex-wrap gap-1.5">
                    {f.versions.map((v) => (
                      <button key={v.version} onClick={() => set({ q: v.version })} className={cx('cursor-pointer rounded-md px-1.5 py-0.5 font-mono text-[11px] ring-1 ring-inset', v.version === f.latest ? 'bg-slate-500/10 ring-slate-500/30' : 'bg-amber-500/10 text-amber-500 ring-amber-500/30')}>
                        {v.version} <span className="text-muted">×{v.count}</span>
                      </button>
                    ))}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>

      {edit && <EditDevice d={edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reload() }} />}
      {settings && <MonitorSettings onClose={() => setSettings(false)} onSaved={() => { setSettings(false); reload(); fw.reload() }} />}
    </>
  )
}

function EditDevice({ d, onClose, onSaved }: { d: Device; onClose: () => void; onSaved: () => void }) {
  const toast = useToast()
  const [f, setF] = useState({ name: d.name, notes: d.notes, monitored: d.monitored })
  const [busy, setBusy] = useState(false)
  const path = '/devices/' + encodeURIComponent(d.key)
  const save = async () => {
    setBusy(true)
    try { await api.put(path, f); toast('ok', t('Saved')); onSaved() } catch (e) { toast('err', (e as Error).message) } finally { setBusy(false) }
  }
  const remove = async () => {
    if (!confirm(t('Remove {name} from the inventory? It will reappear as soon as it sends logs again.', { name: d.name || d.hostname }))) return
    try { await api.del(path); toast('ok', t('Removed')); onSaved() } catch (e) { toast('err', (e as Error).message) }
  }
  return (
    <Modal open onClose={onClose} title={d.name || d.hostname}
      footer={<>
        <Button variant="danger" onClick={remove} icon={<Trash2 className="size-4" />} className="mr-auto">{t('Remove')}</Button>
        <Button onClick={onClose}>{t('Cancel')}</Button>
        <Button variant="primary" onClick={save} loading={busy}>{t('Save')}</Button>
      </>}>
      <div className="space-y-4">
        <div className="grid grid-cols-2 gap-3 rounded-xl bg-[var(--panel-2)] p-3 text-xs">
          <div><div className="text-muted">{t('Hostname')}</div><div className="font-medium">{d.hostname}</div></div>
          <div><div className="text-muted">{t('Model')}</div><div className="font-medium">{d.model || '–'}</div></div>
          <div><div className="text-muted">MAC</div><div className="font-mono">{d.mac || '–'}</div></div>
          <div><div className="text-muted">{t('Firmware')}</div><div className="font-mono">{d.version || '–'}{d.prev_version && <span className="text-muted"> ({t('was {version}', { version: d.prev_version })})</span>}</div></div>
          <div><div className="text-muted">{t('First seen')}</div><div>{fmtTime(d.first_seen * 1000)}</div></div>
          <div><div className="text-muted">{t('Last seen')}</div><div>{fmtTime(d.last_seen * 1000)}</div></div>
        </div>
        <Field label={t('Name')} hint={t('Shown instead of the hostname')}><Input value={f.name} placeholder={d.hostname} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
        <Field label={t('Notes')}><Input value={f.notes} placeholder={t('For example: reception ceiling, 1st floor')} onChange={(e) => setF({ ...f, notes: e.target.value })} /></Field>
        <Toggle checked={f.monitored} onChange={(monitored) => setF({ ...f, monitored })} label={t('Monitor')} description={t('Alert when this device stops sending logs')} />
      </div>
    </Modal>
  )
}

function MonitorSettings({ onClose, onSaved }: { onClose: () => void; onSaved: () => void }) {
  const toast = useToast()
  const { data } = useAsync(() => api.get<InventorySettings>('/settings/inventory'))
  const [f, setF] = useState<InventorySettings | null>(null)
  const [busy, setBusy] = useState(false)
  const v = f ?? data
  const save = async () => {
    if (!v) return
    setBusy(true)
    try { await api.put('/settings/inventory', v); toast('ok', t('Saved')); onSaved() } catch (e) { toast('err', (e as Error).message) } finally { setBusy(false) }
  }
  return (
    <Modal open onClose={onClose} title={t('Device monitoring')}
      footer={<><Button onClick={onClose}>{t('Cancel')}</Button><Button variant="primary" onClick={save} loading={busy} disabled={!v}>{t('Save')}</Button></>}>
      {!v ? <div className="grid place-items-center py-10"><Spinner /></div> : (
        <div className="space-y-4">
          <Field label={t('Silent after (minutes)')} hint={t('A device that sends no logs at all for this long counts as silent. Switches sometimes log very little; choose a longer time for them.')}>
            <Input type="number" min={5} value={v.silent_minutes} onChange={(e) => setF({ ...v, silent_minutes: Number(e.target.value) })} />
          </Field>
          <Toggle checked={v.notify} onChange={(notify) => setF({ ...v, notify })} label={t('Send email')} description={t('When a device goes silent and when it becomes active again')} />
          <Toggle checked={v.firmware_notify} onChange={(firmware_notify) => setF({ ...v, firmware_notify })} label={t('Firmware warnings')} description={t('Email when a device runs older firmware than other devices of the same model, or when UniFi Network offers an update')} />
          {(v.notify || v.firmware_notify) && (
            <Field label={t('Recipients')} hint={t('Comma-separated. Empty = the mail server\'s default recipient.')}>
              <Input value={v.recipients} placeholder="admin@example.com" onChange={(e) => setF({ ...v, recipients: e.target.value })} />
            </Field>
          )}
        </div>
      )}
    </Modal>
  )
}
