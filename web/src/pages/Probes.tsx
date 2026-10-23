import { useEffect, useState, type ReactNode } from 'react'
import {
  RadioTower, Plus, Pencil, Trash2, KeyRound, RefreshCw, Settings2, CheckCircle2, AlertCircle, CircleDashed,
  ArrowUpCircle, HardDrive, Cpu, MemoryStick, Router as RouterIcon, Gauge, Database, Server, Terminal, Cpu as Chip,
} from 'lucide-react'
import { api } from '../api'
import { Badge, Button, Card, Empty, Field, Input, Modal, PageHeader, Select, Spinner, Stat, Toggle, CopyBox, cx, useAsync, useToast } from '../components/ui'
import { fmtAgo, fmtBytes, fmtDuration, fmtNum, fmtTime } from '../lib/format'
import { t } from '../i18n'

export interface ProbeSettings { udp: string; tcp: string; buffer_mb: number; buffer_hours: number; log_level: string }
interface UpdateReport { state: string; from: string; to: string; error?: string; at: number }
interface ProbeStatus {
  version: string; arch: string; os: string; hostname: string; ips: string[]; uptime: number
  received: number; rate: number; sent: number; buffer_records: number; buffer_bytes: number; buffer_max: number
  dropped: number; expired: number; oldest: number; devices: number
  cpu: number; load1: number; mem_used: number; mem_total: number; disk_used: number; disk_total: number
  listen: string; last_error?: string; update?: UpdateReport
}
interface Probe {
  id: number; name: string; site: string; settings: ProbeSettings; status: ProbeStatus; version: string; hostname: string; remote_ip: string
  target_version: string; update_state: string; update_error: string; enrolled: boolean; code_expires: number; enrolled_at: number
  last_seen: number; created_at: number; online: boolean; outdated: boolean; ingested: number
}
interface HubSettings { auto_update: boolean; offline_minutes: number; buffer_alert_percent: number; notify: boolean; recipients: string; server_url: string }
interface List { probes: Probe[]; server_version: string; server_url: string; pin: string; repo: string; settings: HubSettings }
interface Enrollment { probe: Probe; code: string; key: string; server_url: string; expires: number }

const defaultSettings: ProbeSettings = { udp: ':514', tcp: ':514', buffer_mb: 1024, buffer_hours: 168, log_level: 'info' }
const siteLabel = (s: string) => (!s || s === 'default' ? t('Default') : s)
const pct = (a: number, b: number) => (b > 0 ? Math.min(100, (a / b) * 100) : 0)

export default function Probes() {
  const { data, loading, reload } = useAsync(() => api.get<List>('/probes'))
  const toast = useToast()
  const [edit, setEdit] = useState<Probe | 'new' | null>(null)
  const [enroll, setEnroll] = useState<Enrollment | null>(null)
  const [settings, setSettings] = useState(false)
  const [selected, setSelected] = useState<number[]>([])
  const [busy, setBusy] = useState(false)
  // Probes report every 30 seconds.
  useEffect(() => { const i = setInterval(reload, 15000); return () => clearInterval(i) }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const list = data?.probes ?? []
  const enrolled = list.filter((p) => p.enrolled)
  const online = enrolled.filter((p) => p.online)
  const outdated = enrolled.filter((p) => p.outdated && !p.target_version)
  const sum = (f: (p: Probe) => number) => online.reduce((a, p) => a + f(p), 0)

  const update = async (ids: number[] | 'all') => {
    setBusy(true)
    try {
      const r = await api.post<{ requested: number; version: string }>('/probes/update', ids === 'all' ? { all: true } : { ids })
      toast('ok', r.requested ? t('{n} probes will update to {version}', { n: r.requested, version: r.version }) : t('All selected probes already run {version}', { version: r.version }))
      setSelected([])
      reload()
    } catch (e) { toast('err', (e as Error).message) } finally { setBusy(false) }
  }

  return (
    <>
      <PageHeader title={t('Remote probes')} subtitle={t('Small receivers at your sites that buffer UniFi logs and forward them over HTTPS')} icon={<RadioTower className="size-5" />}
        actions={<>
          {selected.length > 0 && <Button onClick={() => update(selected)} loading={busy} icon={<ArrowUpCircle className="size-4" />}>{t('Update selected ({n})', { n: selected.length })}</Button>}
          {outdated.length > 0 && selected.length === 0 && <Button onClick={() => update('all')} loading={busy} icon={<ArrowUpCircle className="size-4" />}>{t('Update all ({n})', { n: outdated.length })}</Button>}
          <Button onClick={() => setSettings(true)} icon={<Settings2 className="size-4" />}>{t('Settings')}</Button>
          <Button variant="primary" onClick={() => setEdit('new')} icon={<Plus className="size-4" />}>{t('Add probe')}</Button>
        </>} />

      {enrolled.length > 0 && (
        <div className="mb-6 grid grid-cols-2 gap-4 xl:grid-cols-4">
          <Stat label={t('Online')} value={`${online.length} / ${enrolled.length}`} sub={enrolled.length - online.length ? t('{n} offline', { n: enrolled.length - online.length }) : t('All probes are reporting')}
            accent={online.length < enrolled.length ? '#ef4444' : '#10b981'} icon={<RadioTower className="size-4" />} />
          <Stat label={t('Messages per second')} value={sum((p) => p.status.rate).toFixed(1)} sub={t('{n} received since the main server started', { n: fmtNum(enrolled.reduce((a, p) => a + p.ingested, 0)) })} accent="#0ea5e9" icon={<Gauge className="size-4" />} />
          <Stat label={t('Buffered at the sites')} value={fmtNum(sum((p) => p.status.buffer_records))} sub={fmtBytes(sum((p) => p.status.buffer_bytes))}
            accent={sum((p) => p.status.buffer_records) > 10000 ? '#f59e0b' : '#6366f1'} icon={<Database className="size-4" />} />
          <Stat label={t('Devices behind probes')} value={fmtNum(sum((p) => p.status.devices))} sub={t('distinct senders, last 24 hours')} accent="#a855f7" icon={<RouterIcon className="size-4" />} />
        </div>
      )}

      {loading && !data ? <Card><div className="grid place-items-center py-16"><Spinner /></div></Card>
        : !list.length ? (
          <Card><Empty icon={<RadioTower className="size-8" />} title={t('No probes yet')}
            text={t('Place a probe at a remote site: UniFi devices send syslog to it, it keeps everything on disk during an outage and forwards it encrypted to this server.')}
            action={<Button variant="primary" onClick={() => setEdit('new')} icon={<Plus className="size-4" />}>{t('Add probe')}</Button>} /></Card>
        ) : (
          <div className="grid grid-cols-1 gap-4 2xl:grid-cols-2">
            {list.map((p) => (
              <ProbeCard key={p.id} p={p} serverVersion={data!.server_version} selected={selected.includes(p.id)}
                onSelect={(v) => setSelected(v ? [...selected, p.id] : selected.filter((x) => x !== p.id))}
                onEdit={() => setEdit(p)} onUpdate={() => update([p.id])}
                onKey={async () => {
                  try { setEnroll(await api.post<Enrollment>(`/probes/${p.id}/code`, {})) } catch (e) { toast('err', (e as Error).message) }
                }} />
            ))}
          </div>
        )}

      {edit && data && <EditProbe p={edit === 'new' ? null : edit} serverUrl={data.server_url} onClose={() => setEdit(null)}
        onSaved={(e) => { setEdit(null); if (e) setEnroll(e); reload() }} />}
      {enroll && data && <EnrollModal e={enroll} list={data} onClose={() => { setEnroll(null); reload() }} />}
      {settings && data && <SettingsModal s={data.settings} defaultUrl={data.server_url} pin={data.pin} onClose={() => setSettings(false)} onSaved={() => { setSettings(false); reload() }} />}
    </>
  )
}

function StateBadge({ p }: { p: Probe }) {
  if (!p.enrolled) return <Badge color="#94a3b8"><CircleDashed className="size-3" />{t('Waiting for enrollment')}</Badge>
  if (p.online) return <Badge color="#10b981"><CheckCircle2 className="size-3" />{t('Online')}</Badge>
  return <Badge color="#ef4444"><AlertCircle className="size-3" />{t('Offline')}</Badge>
}

function UpdateLine({ p, serverVersion }: { p: Probe; serverVersion: string }) {
  if (p.target_version) return <span className="text-indigo-400">{t('Updating to {version}…', { version: p.target_version })}</span>
  if (p.update_state === 'failed' || p.update_state === 'rolled_back')
    return <span className="text-red-500">{p.update_state === 'rolled_back' ? t('Update rolled back') : t('Update failed')}{p.update_error ? ': ' + p.update_error : ''}</span>
  if (p.outdated) return <span className="text-amber-500">{t('Version {v} · main server runs {s}', { v: p.version, s: serverVersion })}</span>
  return null
}

function Bar({ value, warn = 80 }: { value: number; warn?: number }) {
  return (
    <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-slate-500/15">
      <div className={cx('h-full rounded-full', value >= warn ? 'bg-amber-500' : 'bg-gradient-to-r from-indigo-500 to-cyan-500')} style={{ width: `${Math.max(value, value > 0 ? 2 : 0)}%` }} />
    </div>
  )
}

function Metric({ icon, label, value, sub, bar, warn }: { icon: ReactNode; label: string; value: ReactNode; sub?: ReactNode; bar?: number; warn?: boolean }) {
  return (
    <div className="min-w-0 rounded-xl bg-[var(--panel-2)] px-3 py-2.5">
      <div className="flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-widest text-muted [&_svg]:size-3">{icon}{label}</div>
      <div className={cx('mt-0.5 truncate font-semibold', warn && 'text-amber-500')}>{value}</div>
      {sub && <div className="truncate text-xs text-muted">{sub}</div>}
      {bar !== undefined && <Bar value={bar} />}
    </div>
  )
}

function ProbeCard({ p, serverVersion, selected, onSelect, onEdit, onKey, onUpdate }: {
  p: Probe; serverVersion: string; selected: boolean; onSelect: (v: boolean) => void; onEdit: () => void; onKey: () => void; onUpdate: () => void
}) {
  const s = p.status
  const buf = pct(s.buffer_bytes, s.buffer_max)
  return (
    <Card pad={false} className={cx(selected && 'ring-2 ring-indigo-500/50')}>
      <div className="flex flex-wrap items-start gap-3 px-5 py-4">
        {p.enrolled && <input type="checkbox" aria-label={t('Select')} checked={selected} onChange={(e) => onSelect(e.target.checked)} className="mt-1.5 size-4 cursor-pointer accent-indigo-500" />}
        <span className={cx('grid size-10 shrink-0 place-items-center rounded-xl', p.online ? 'bg-emerald-500/15 text-emerald-500' : p.enrolled ? 'bg-red-500/15 text-red-500' : 'bg-slate-500/15 text-muted')}>
          <RadioTower className="size-5" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="truncate font-medium">{p.name}</span>
            <StateBadge p={p} />
            <Badge>{siteLabel(p.site)}</Badge>
          </div>
          <div className="mt-1 truncate text-xs text-muted">
            {p.enrolled
              ? [p.hostname, s.ips?.join(', '), s.os, s.arch, p.version && 'v' + p.version].filter(Boolean).join(' · ')
              : t('Enrollment key valid until {date}', { date: fmtTime(p.code_expires * 1000) })}
          </div>
          <div className="mt-1 text-xs"><UpdateLine p={p} serverVersion={serverVersion} /></div>
          {p.enrolled && s.last_error && <div className="mt-1 truncate text-xs text-amber-500" title={s.last_error}>{s.last_error}</div>}
        </div>
        <div className="flex items-center gap-1">
          {p.outdated && !p.target_version && <Button variant="ghost" title={t('Update to {version}', { version: serverVersion })} onClick={onUpdate} icon={<ArrowUpCircle className="size-4" />} />}
          <Button variant="ghost" title={p.enrolled ? t('New enrollment key (reinstall)') : t('Show enrollment key')} onClick={onKey} icon={<KeyRound className="size-4" />} />
          <Button variant="ghost" title={t('Edit')} onClick={onEdit} icon={<Pencil className="size-4" />} />
        </div>
      </div>
      {p.enrolled && (
        <div className="grid grid-cols-2 gap-2 border-t border-app px-5 py-4 text-sm sm:grid-cols-4">
          <Metric icon={<Gauge />} label={t('Messages/s')} value={p.online ? s.rate.toFixed(1) : '–'} sub={t('{n} received', { n: fmtNum(s.received) })} />
          <Metric icon={<Database />} label={t('Buffer')} value={fmtNum(s.buffer_records)} sub={`${fmtBytes(s.buffer_bytes)} / ${fmtBytes(s.buffer_max)}`} bar={buf} warn={buf >= 80} />
          <Metric icon={<RouterIcon />} label={t('Devices')} value={fmtNum(s.devices)} sub={t('last 24 hours')} />
          <Metric icon={<RefreshCw />} label={t('Last contact')} value={p.last_seen ? fmtAgo(p.last_seen * 1000) : '–'} sub={s.uptime ? t('up {d}', { d: fmtDuration(s.uptime) }) : undefined} warn={!p.online} />
          <Metric icon={<Cpu />} label={t('CPU')} value={`${s.cpu.toFixed(0)}%`} sub={t('load {l}', { l: s.load1.toFixed(2) })} bar={s.cpu} />
          <Metric icon={<MemoryStick />} label={t('Memory')} value={fmtBytes(s.mem_used)} sub={t('of {total}', { total: fmtBytes(s.mem_total) })} bar={pct(s.mem_used, s.mem_total)} />
          <Metric icon={<HardDrive />} label={t('Disk')} value={fmtBytes(s.disk_used)} sub={t('of {total}', { total: fmtBytes(s.disk_total) })} bar={pct(s.disk_used, s.disk_total)} />
          <Metric icon={<Server />} label={t('Listening')} value={s.listen || '–'} sub={(s.dropped || s.expired) ? t('{d} dropped · {e} expired', { d: fmtNum(s.dropped), e: fmtNum(s.expired) }) : undefined} warn={s.dropped > 0} />
        </div>
      )}
    </Card>
  )
}

function EditProbe({ p, serverUrl, onClose, onSaved }: { p: Probe | null; serverUrl: string; onClose: () => void; onSaved: (e?: Enrollment) => void }) {
  const toast = useToast()
  const sites = useAsync(() => api.get<string[]>('/sites'))
  const [f, setF] = useState({ name: p?.name ?? '', site: p?.site && p.site !== 'default' ? p.site : '', settings: { ...defaultSettings, ...p?.settings }, server_url: '' })
  const [busy, setBusy] = useState('')
  const us = (x: Partial<ProbeSettings>) => setF({ ...f, settings: { ...f.settings, ...x } })
  const save = async () => {
    setBusy('save')
    try {
      if (p) {
        await api.put(`/probes/${p.id}`, f)
        toast('ok', t('Saved; the probe applies it at its next contact'))
        onSaved()
      } else {
        onSaved(await api.post<Enrollment>('/probes', f))
      }
    } catch (e) { toast('err', (e as Error).message) } finally { setBusy('') }
  }
  const remove = async () => {
    if (!p || !confirm(t('Remove probe {name}? Its key stops working at once; messages still buffered on the probe are not delivered.', { name: p.name }))) return
    setBusy('remove')
    try { await api.del(`/probes/${p.id}`); toast('ok', t('Removed')); onSaved() } catch (e) { toast('err', (e as Error).message) } finally { setBusy('') }
  }
  return (
    <Modal open onClose={onClose} title={p ? p.name : t('Add probe')} wide
      footer={<>
        {p && <Button variant="danger" onClick={remove} loading={busy === 'remove'} icon={<Trash2 className="size-4" />} className="mr-auto">{t('Remove')}</Button>}
        <Button onClick={onClose}>{t('Cancel')}</Button>
        <Button variant="primary" onClick={save} loading={busy === 'save'} disabled={!f.name.trim()}>{p ? t('Save') : t('Create and show enrollment key')}</Button>
      </>}>
      <div className="grid grid-cols-1 gap-8 lg:grid-cols-2">
        <div className="space-y-4">
          <Field label={t('Name')}><Input value={f.name} placeholder={t('e.g. Branch Utrecht')} onChange={(e) => setF({ ...f, name: e.target.value })} autoFocus /></Field>
          <Field label={t('Site')} hint={t('Every message from this probe is assigned to this site')}>
            <Input list="probe-sites" value={f.site} placeholder={t('Default')} onChange={(e) => setF({ ...f, site: e.target.value })} />
            <datalist id="probe-sites">{(sites.data ?? []).filter((x) => x !== 'default').map((x) => <option key={x} value={x} />)}</datalist>
          </Field>
          {!p && (
            <Field label={t('Main server address for the probe')} hint={t('Must be reachable from the site; the web port also speaks HTTPS. Leave empty for {url}', { url: serverUrl })}>
              <Input className="font-mono" value={f.server_url} placeholder={serverUrl} onChange={(e) => setF({ ...f, server_url: e.target.value.trim() })} />
            </Field>
          )}
        </div>
        <div className="space-y-4">
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('UDP listen address')} hint={t('Empty = off')}><Input className="font-mono" value={f.settings.udp} onChange={(e) => us({ udp: e.target.value.trim() })} /></Field>
            <Field label={t('TCP listen address')} hint={t('Empty = off')}><Input className="font-mono" value={f.settings.tcp} onChange={(e) => us({ tcp: e.target.value.trim() })} /></Field>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('Buffer size (MB)')} hint={t('Beyond this the oldest messages are dropped')}>
              <Input type="number" min={16} value={f.settings.buffer_mb} onChange={(e) => us({ buffer_mb: Number(e.target.value) })} />
            </Field>
            <Field label={t('Keep buffered messages (hours)')} hint={t('0 = until delivered')}>
              <Input type="number" min={0} value={f.settings.buffer_hours} onChange={(e) => us({ buffer_hours: Number(e.target.value) })} />
            </Field>
          </div>
          <Field label={t('Log level')}>
            <Select value={f.settings.log_level} onChange={(log_level) => us({ log_level })} options={['debug', 'info', 'warn', 'error'].map((v) => ({ value: v, label: v }))} />
          </Field>
          <p className="text-xs text-muted">{t('Settings are sent to the probe with its next heartbeat (every 30 seconds).')}</p>
        </div>
      </div>
    </Modal>
  )
}

function EnrollModal({ e, list, onClose }: { e: Enrollment; list: List; onClose: () => void }) {
  const [tab, setTab] = useState<'lxc' | 'debian' | 'pi'>('lxc')
  const raw = `https://raw.githubusercontent.com/${list.repo}/main`
  const dev = list.server_version.includes('dev')
  const image = dev ? `https://github.com/${list.repo}/releases` : `https://github.com/${list.repo}/releases/download/v${list.server_version}/unify-syslog-probe-rpi-arm64.img.xz`
  const tabs: { key: typeof tab; label: string; icon: ReactNode }[] = [
    { key: 'lxc', label: t('Proxmox LXC'), icon: <Server className="size-4" /> },
    { key: 'debian', label: t('Debian 13 VM or server'), icon: <Terminal className="size-4" /> },
    { key: 'pi', label: t('Raspberry Pi'), icon: <Chip className="size-4" /> },
  ]
  return (
    <Modal open onClose={onClose} wide title={t('Install probe {name}', { name: e.probe.name })} footer={<Button variant="primary" onClick={onClose}>{t('Done')}</Button>}>
      <div className="space-y-5">
        <div>
          <div className="mb-1.5 text-xs font-medium uppercase tracking-wide text-muted">{t('Enrollment key')}</div>
          <CopyBox text={e.key} />
          <p className="mt-1.5 text-xs text-muted">
            {t('One-time key, valid until {date}. It contains the address {url} and the fingerprint of this server, so the probe connects encrypted without a public certificate.', { date: fmtTime(e.expires * 1000), url: e.server_url })}
          </p>
        </div>
        <div className="flex flex-wrap gap-1.5">
          {tabs.map((x) => (
            <button key={x.key} onClick={() => setTab(x.key)}
              className={cx('flex items-center gap-2 rounded-xl px-3 py-2 text-sm ring-1 transition cursor-pointer', tab === x.key ? 'bg-indigo-500/15 text-indigo-400 ring-indigo-500/40' : 'ring-[var(--border)] text-muted hover:text-[var(--text)]')}>
              {x.icon}{x.label}
            </button>
          ))}
        </div>
        {tab === 'lxc' && <Steps steps={[
          [t('Run this in the Proxmox VE shell of the host at the site. It creates a Debian 13 container (1 CPU, 512 MB, 4 GB disk) and enrolls the probe.'),
            <CopyBox multiline text={`USS_PROBE_KEY='${e.key}' bash -c "$(curl -fsSL ${raw}/ct/unify-syslog-probe.sh)"`} />],
          [t('In UniFi Network go to Settings → CyberSecure → Traffic Logging, choose SIEM Server or Syslog and enter the address of the container with port 514.')],
        ]} />}
        {tab === 'debian' && <Steps steps={[
          [t('On a Debian 13 VM or physical machine at the site, run as root:'),
            <CopyBox multiline text={`curl -fsSL ${raw}/deploy/install-probe.sh | USS_PROBE_KEY='${e.key}' bash`} />],
          [t('Without the key the installer asks for it, or enroll later with:'), <CopyBox text={`unify-syslog-probe enroll '${e.key}'`} />],
          [t('In UniFi Network go to Settings → CyberSecure → Traffic Logging, choose SIEM Server or Syslog and enter the address of the machine with port 514.')],
        ]} />}
        {tab === 'pi' && <Steps steps={[
          [<>{t('Download the Raspberry Pi image (64-bit, Raspberry Pi 3, 4, 5 and Zero 2 W):')} <a className="text-indigo-400 hover:underline" href={image} target="_blank" rel="noreferrer">unify-syslog-probe-rpi-arm64.img.xz</a></>],
          [t('Write it to an SD card with Raspberry Pi Imager (choose "Use custom"); there you can also set Wi-Fi, a user and SSH.')],
          [t('Optional: put the enrollment key in a file uss-probe.txt on the boot partition, then the probe connects by itself on first boot.')],
          [t('Otherwise open http://<address of the Pi>:8080 after booting and paste the key, or sign in and run sudo unify-syslog-probe setup.')],
          [t('In UniFi Network go to Settings → CyberSecure → Traffic Logging, choose SIEM Server or Syslog and enter the address of the Pi with port 514.')],
        ]} />}
        <p className="text-xs text-muted">{t('Updates of the probe come from this server, matching its own version. Use Update on the Probes page, or turn on automatic updates in Settings.')}</p>
      </div>
    </Modal>
  )
}

function Steps({ steps }: { steps: ReactNode[][] }) {
  return (
    <ol className="space-y-4">
      {steps.map((s, i) => (
        <li key={i} className="flex gap-3">
          <span className="grid size-6 shrink-0 place-items-center rounded-full bg-indigo-500/15 text-xs font-semibold text-indigo-400">{i + 1}</span>
          <div className="min-w-0 flex-1 space-y-2 text-sm">{s.map((x, j) => <div key={j}>{x}</div>)}</div>
        </li>
      ))}
    </ol>
  )
}

function SettingsModal({ s, defaultUrl, pin, onClose, onSaved }: { s: HubSettings; defaultUrl: string; pin: string; onClose: () => void; onSaved: () => void }) {
  const toast = useToast()
  const [f, setF] = useState(s)
  const [busy, setBusy] = useState(false)
  const save = async () => {
    setBusy(true)
    try { await api.put('/settings/probes', f); toast('ok', t('Saved')); onSaved() } catch (e) { toast('err', (e as Error).message) } finally { setBusy(false) }
  }
  return (
    <Modal open onClose={onClose} title={t('Probe settings')}
      footer={<><Button onClick={onClose}>{t('Cancel')}</Button><Button variant="primary" onClick={save} loading={busy}>{t('Save')}</Button></>}>
      <div className="space-y-5">
        <Toggle checked={f.auto_update} onChange={(auto_update) => setF({ ...f, auto_update })} label={t('Update probes automatically')}
          description={t('Probes follow the version (and release channel) of this server. A version that fails to start is rolled back and not retried automatically.')} />
        <div className="grid grid-cols-2 gap-3">
          <Field label={t('Offline after (minutes)')}><Input type="number" min={1} value={f.offline_minutes} onChange={(e) => setF({ ...f, offline_minutes: Number(e.target.value) })} /></Field>
          <Field label={t('Buffer warning at (%)')}><Input type="number" min={1} max={100} value={f.buffer_alert_percent} onChange={(e) => setF({ ...f, buffer_alert_percent: Number(e.target.value) })} /></Field>
        </div>
        <Toggle checked={f.notify} onChange={(notify) => setF({ ...f, notify })} label={t('E-mail when a probe goes offline or its buffer fills up')} description={t('Uses the mail server settings')} />
        {f.notify && <Field label={t('Recipients')} hint={t('Comma-separated; empty = the recipients of device alerts')}><Input value={f.recipients} onChange={(e) => setF({ ...f, recipients: e.target.value })} /></Field>}
        <Field label={t('Main server address in enrollment keys')} hint={t('For example a public name or port forward; empty = {url}', { url: defaultUrl })}>
          <Input className="font-mono" value={f.server_url} placeholder={defaultUrl} onChange={(e) => setF({ ...f, server_url: e.target.value.trim() })} />
        </Field>
        <Field label={t('Certificate fingerprint (SHA-256 public key)')}><CopyBox text={pin} /></Field>
      </div>
    </Modal>
  )
}
