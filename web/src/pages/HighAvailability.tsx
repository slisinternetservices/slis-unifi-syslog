import { useEffect, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { ServerCog, Server, Link2, Plus, RefreshCw, LogOut, Save, Crown, Moon, AlertTriangle, CheckCircle2, Clock } from 'lucide-react'
import { api } from '../api'
import { Badge, Button, Card, CopyBox, Field, Input, PageHeader, Select, Spinner, cx, useToast } from '../components/ui'
import { fmtAgo } from '../lib/format'
import { t } from '../i18n'

interface Peer { name: string; url: string; active: boolean; version: string; fingerprint: string; seen: number; error: string }
interface HAConfig { enabled: boolean; vip: string; node_name: string; self_url: string; peer_url: string; peer_pin: string; sync_seconds: number; interface: string; priority: number; router_id: number }
interface State { enabled: boolean; active: boolean; since: number; node_name: string; vip: string; self_url: string; peer: Peer; sync: { last: number; error: string; pending: boolean }; version: string; config: HAConfig }
interface Resp { state: State; interfaces: string[]; default_url: string; clickhouse: { local: boolean; addr: string }; code?: string; keepalived?: string }
interface Setup { node_name: string; self_url: string; vip: string; interface: string; priority: number; code: string }

const role = (active: boolean) => active
  ? <Badge className="bg-emerald-500/15 text-emerald-500 ring-emerald-500/30"><Crown className="size-3" />{t('Active')}</Badge>
  : <Badge className="bg-slate-500/15 ring-slate-500/30"><Moon className="size-3" />{t('Standby')}</Badge>

function NodeCard({ title, name, url, active, version, extra, self }: { title: string; name: string; url: string; active?: boolean; version: string; extra?: ReactNode; self?: boolean }) {
  return (
    <div className={cx('panel rounded-2xl p-5', active && 'ring-1 ring-emerald-500/40')}>
      <div className="flex items-start justify-between gap-3">
        <div className="flex items-center gap-3">
          <span className={cx('grid size-10 place-items-center rounded-xl', active ? 'bg-emerald-500/15 text-emerald-500' : 'bg-[var(--panel-2)] text-muted')}><Server className="size-5" /></span>
          <div>
            <div className="text-[11px] uppercase tracking-wide text-muted">{title}</div>
            <div className="font-semibold">{name || '–'}{self && <span className="ml-2 text-xs font-normal text-muted">({t('this node')})</span>}</div>
          </div>
        </div>
        {active !== undefined && role(active)}
      </div>
      <dl className="mt-4 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
        <dt className="text-muted">URL</dt><dd className="truncate font-mono">{url || '–'}</dd>
        <dt className="text-muted">{t('Version')}</dt><dd className="font-mono">{version || '–'}</dd>
        {extra}
      </dl>
    </div>
  )
}

export default function HighAvailability() {
  const toast = useToast()
  const [d, setD] = useState<Resp>()
  const [busy, setBusy] = useState('')
  const load = () => api.get<Resp>('/settings/ha').then(setD).catch((e) => toast('err', e.message))
  useEffect(() => {
    load()
    const id = setInterval(load, 10000)
    return () => clearInterval(id)
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  if (!d) return <div className="grid place-items-center py-24"><Spinner /></div>
  const act = async (action: string, setup?: Partial<Setup>, confirmText?: string) => {
    if (confirmText && !confirm(confirmText)) return
    setBusy(action)
    try {
      const r = await api.put<{ state: State; restart: boolean }>('/settings/ha', { action, ...setup })
      if (r.restart) { toast('ok', t('Paired. The service restarts and copies the settings of the active node.')); setTimeout(() => location.reload(), 10000) }
      else toast('ok', action === 'sync' ? t('Settings copied') : t('Saved'))
      load()
    } catch (e) { toast('err', (e as Error).message) } finally { setBusy('') }
  }
  const st = d.state
  const header = <PageHeader title={t('High availability')} subtitle={t('Two servers behind one virtual IP with keepalived')} icon={<ServerCog className="size-5" />} />

  if (!st.enabled) return (
    <>
      {header}
      <Card className="mb-6" title={t('How it works')}>
        <ul className="grid gap-3 text-sm md:grid-cols-3">
          <li className="panel-2 rounded-xl p-4"><b className="block pb-1">{t('One virtual IP')}</b><span className="text-muted">{t('UniFi devices send syslog to the virtual IP. keepalived moves it to the other server when one fails.')}</span></li>
          <li className="panel-2 rounded-xl p-4"><b className="block pb-1">{t('One log store')}</b><span className="text-muted">{t('Both servers write to the same external ClickHouse server or cluster.')}</span></li>
          <li className="panel-2 rounded-xl p-4"><b className="block pb-1">{t('One configuration')}</b><span className="text-muted">{t('The standby copies all settings from the active server. Alerts, backups and probes run on the active server only.')}</span></li>
        </ul>
      </Card>
      {d.clickhouse.local && (
        <div className="mb-6 flex items-start gap-3 rounded-2xl bg-amber-500/10 px-5 py-4 text-sm text-amber-500 ring-1 ring-amber-500/30">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" />
          <span>{t('This server stores logs in its own ClickHouse. Connect it to an external ClickHouse server or cluster first under')} <Link to="/storage" className="underline">{t('Storage')}</Link>.</span>
        </div>
      )}
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-2">
        <SetupCard d={d} mode="start" busy={busy === 'start'} onSubmit={(s) => act('start', s)} />
        <SetupCard d={d} mode="join" busy={busy === 'join'} onSubmit={(s) => act('join', s, t('Join the pair? This server takes over the ClickHouse connection and all settings of the other node, and restarts.'))} />
      </div>
    </>
  )

  const peer = st.peer
  const peerOK = !!peer.seen && !peer.error && Date.now() / 1000 - peer.seen < 120
  const bothActive = st.active && peerOK && peer.active
  const cmd = d.keepalived && `apt-get install -y keepalived curl\ncat > /etc/keepalived/keepalived.conf <<'EOF'\n${d.keepalived.trimEnd()}\nEOF\nsystemctl enable --now keepalived\nsystemctl restart keepalived`

  return (
    <>
      {header}
      {bothActive && (
        <div className="mb-6 flex items-start gap-3 rounded-2xl bg-red-500/10 px-5 py-4 text-sm text-red-500 ring-1 ring-red-500/30">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" /><span>{t('Both nodes hold the virtual IP. Check that keepalived runs on both and that they can reach each other.')}</span>
        </div>
      )}
      <div className="mb-6 grid grid-cols-1 gap-6 lg:grid-cols-2">
        <NodeCard self title={t('This node')} name={st.node_name} url={st.self_url} active={st.active} version={st.version}
          extra={<><dt className="text-muted">{t('Virtual IP')}</dt><dd className="font-mono">{st.vip}</dd><dt className="text-muted">{t('Role since')}</dt><dd>{fmtAgo(st.since * 1000)}</dd></>} />
        {st.config.peer_url ? (
          <NodeCard title={t('Other node')} name={peer.name} url={peer.url || st.config.peer_url} active={peerOK ? peer.active : undefined} version={peer.version}
            extra={<><dt className="text-muted">{t('Reachable')}</dt><dd>{peerOK
              ? <span className="text-emerald-500">{t('Yes, {ago}', { ago: fmtAgo(peer.seen * 1000) })}</span>
              : <span className="text-red-500">{peer.error || t('No contact yet')}</span>}</dd></>} />
        ) : (
          <Card title={t('Add the second node')} subtitle={t('Paste this code on the High availability page of the second server, under "Join a pair".')}>
            {d.code && <CopyBox multiline text={d.code} />}
            <p className="mt-3 text-xs text-muted">{t('The code contains the shared secret; treat it like a password.')}</p>
          </Card>
        )}
      </div>

      <div className="grid grid-cols-1 gap-6 xl:grid-cols-2">
        <Card title={t('Settings copy')} subtitle={t('The standby compares its settings with the active node every {n} seconds and copies them when they differ', { n: st.config.sync_seconds || 30 })}
          actions={!st.active && <Button onClick={() => act('sync')} loading={busy === 'sync'} icon={<RefreshCw className="size-4" />}>{t('Copy now')}</Button>}>
          <div className="flex items-center gap-3 text-sm">
            {st.active ? <><Crown className="size-4 text-emerald-500" /><span>{t('This node is active; the other node copies its settings from here.')}</span></>
              : st.sync.error ? <><AlertTriangle className="size-4 text-red-500" /><span className="text-red-500">{st.sync.error}</span></>
              : st.sync.last ? <><CheckCircle2 className="size-4 text-emerald-500" /><span>{t('Last copied {ago}', { ago: fmtAgo(st.sync.last * 1000) })}</span></>
              : <><Clock className="size-4 text-muted" /><span className="text-muted">{t('Not copied yet')}</span></>}
          </div>
          {!st.active && <p className="mt-3 text-xs text-muted">{t('Changes made on this node are refused; make them on the active node.')}</p>}
        </Card>
        <UpdateCard d={d} busy={busy} onUpdate={(s) => act('update', s)} onLeave={() => act('leave', undefined, t('Turn off high availability on this node? Remove keepalived or stop it here as well.'))} />
        {cmd && (
          <Card className="xl:col-span-2" title={t('keepalived on this node')} subtitle={t('Run this as root on this server. The other node shows its own configuration on its High availability page.')}>
            <CopyBox multiline text={cmd} />
            <p className="mt-3 text-xs text-muted">{t('keepalived checks /api/health every few seconds and moves the virtual IP away from a node whose server stopped or lost ClickHouse. A node that comes back does not take the IP back.')}</p>
          </Card>
        )}
      </div>
    </>
  )
}

function SetupCard({ d, mode, busy, onSubmit }: { d: Resp; mode: 'start' | 'join'; busy: boolean; onSubmit: (s: Setup) => void }) {
  const [s, setS] = useState<Setup>({ node_name: '', self_url: d.default_url, vip: '', interface: d.interfaces[0] ?? 'eth0', priority: mode === 'start' ? 110 : 100, code: '' })
  const u = (p: Partial<Setup>) => setS({ ...s, ...p })
  const start = mode === 'start'
  return (
    <Card title={start ? t('Start a pair') : t('Join a pair')} subtitle={start ? t('On the first server, which holds the virtual IP first') : t('On the second server, with the code of the first')}
      actions={<Button variant="primary" loading={busy} disabled={start ? (d.clickhouse.local || !s.vip) : !s.code} onClick={() => onSubmit(s)} icon={start ? <Plus className="size-4" /> : <Link2 className="size-4" />}>{start ? t('Start') : t('Join')}</Button>}>
      <div className="grid grid-cols-2 gap-3">
        {start
          ? <Field label={t('Virtual IP')} hint={t('With prefix length, e.g. 192.168.1.50/24')} className="col-span-2"><Input value={s.vip} onChange={(e) => u({ vip: e.target.value.trim() })} placeholder="192.168.1.50/24" /></Field>
          : <Field label={t('Pairing code')} className="col-span-2"><textarea rows={3} className="input font-mono text-xs" value={s.code} onChange={(e) => u({ code: e.target.value.trim() })} placeholder="USSHA1.…" /></Field>}
        <Field label={t('Node name')}><Input value={s.node_name} onChange={(e) => u({ node_name: e.target.value })} placeholder={start ? 'syslog-a' : 'syslog-b'} /></Field>
        <Field label={t('Address of this node')} hint={t('Its own address, not the virtual IP')}><Input value={s.self_url} onChange={(e) => u({ self_url: e.target.value.trim() })} /></Field>
        <Field label={t('Network interface')}><Select value={s.interface} onChange={(i) => u({ interface: i })} options={(d.interfaces.length ? d.interfaces : ['eth0']).map((i) => ({ value: i, label: i }))} /></Field>
        <Field label={t('keepalived priority')} hint={t('Higher wins when both start')}><Input type="number" min={1} max={254} value={s.priority} onChange={(e) => u({ priority: Number(e.target.value) })} /></Field>
      </div>
    </Card>
  )
}

function UpdateCard({ d, busy, onUpdate, onLeave }: { d: Resp; busy: string; onUpdate: (s: Partial<Setup>) => void; onLeave: () => void }) {
  const c = d.state.config
  const [s, setS] = useState({ node_name: c.node_name, self_url: c.self_url, interface: c.interface, priority: c.priority })
  const u = (p: Partial<typeof s>) => setS({ ...s, ...p })
  const ifs = d.interfaces.includes(s.interface) ? d.interfaces : [s.interface, ...d.interfaces]
  return (
    <Card title={t('This node')} actions={<>
      <Button variant="danger" loading={busy === 'leave'} onClick={onLeave} icon={<LogOut className="size-4" />}>{t('Leave')}</Button>
      <Button variant="primary" loading={busy === 'update'} onClick={() => onUpdate(s)} icon={<Save className="size-4" />}>{t('Save')}</Button>
    </>}>
      <div className="grid grid-cols-2 gap-3">
        <Field label={t('Node name')}><Input value={s.node_name} onChange={(e) => u({ node_name: e.target.value })} /></Field>
        <Field label={t('Address of this node')}><Input value={s.self_url} onChange={(e) => u({ self_url: e.target.value.trim() })} /></Field>
        <Field label={t('Network interface')}><Select value={s.interface} onChange={(i) => u({ interface: i })} options={ifs.map((i) => ({ value: i, label: i }))} /></Field>
        <Field label={t('keepalived priority')}><Input type="number" min={1} max={254} value={s.priority} onChange={(e) => u({ priority: Number(e.target.value) })} /></Field>
      </div>
    </Card>
  )
}
