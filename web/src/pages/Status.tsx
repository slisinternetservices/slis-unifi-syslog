import { useEffect, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { Activity, AlertTriangle, CheckCircle2, CircleAlert, Database, Gauge, HardDrive, PackageX, Server, Boxes, ArrowRight } from 'lucide-react'
import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { api } from '../api'
import { Badge, Card, PageHeader, Spinner, Stat, cx } from '../components/ui'
import { chartInk, fmtBytes, fmtDuration, fmtNum, fmtTime } from '../lib/format'
import { t } from '../i18n'

interface Issue { key: string; code: string; severity: 'warning' | 'critical'; message: string; params: Record<string, string>; since: number }
interface Point { t: number; received: number; stored: number; dropped: number }
interface Disk { name: string; path: string; total: number; free: number }
interface Component { key: string; state: 'ok' | 'warning' | 'error' | 'off'; detail: string; params?: Record<string, string | number>; link?: string }
interface Status {
  health: 'ok' | 'warning' | 'critical'
  issues: Issue[]
  checked: string
  version: string
  ingest: {
    rate: number; rate_stored: number; peak: number; series: Point[]; received: number; udp_packets: number; tcp_messages: number; tcp_connections: number
    parsed: number; inserted: number; dropped: number; rejected: number; insert_errors: number; discarded: number; pending: number
    queue_length: number; queue_capacity: number; last_insert: number; last_error: string; kernel?: Record<string, number>; dropped_hour: number
  }
  disks: Disk[]
  clickhouse: { ok: boolean; error?: string; latency_ms: number; version: string; uptime_sec: number; parts: number; max_parts_per_partition: number; merges: number; mutations: number; memory_bytes: number }
  storage: { rows: number; bytes_on_disk: number; bytes_uncompressed: number; oldest: string; newest: string; partitions: number }
  process: { uptime_sec: number; goroutines: number; heap_bytes: number; rss_bytes: number; cpus: number; load: number[]; mem_total: number; mem_available: number }
  components: Component[]
}

const healthStyle = {
  ok: { cls: 'bg-emerald-500/10 text-emerald-500 ring-emerald-500/30', icon: <CheckCircle2 className="size-4" />, label: () => t('All systems normal') },
  warning: { cls: 'bg-amber-500/10 text-amber-500 ring-amber-500/30', icon: <AlertTriangle className="size-4" />, label: () => t('Needs attention') },
  critical: { cls: 'bg-red-500/10 text-red-500 ring-red-500/30', icon: <CircleAlert className="size-4" />, label: () => t('Problem') },
}
const stateDot = { ok: 'bg-emerald-500', warning: 'bg-amber-500', error: 'bg-red-500', off: 'bg-slate-500/50' }
const componentName: Record<string, string> = { probes: 'Remote probes', forwarding: 'Forwarding', notifications: 'Notification channels', backup: 'Backup & restore' }

const rate = (n: number) => (n >= 100 ? fmtNum(Math.round(n)) : n.toLocaleString(undefined, { maximumFractionDigits: 1 }))

export default function SystemStatus() {
  const [st, setSt] = useState<Status>()
  const [error, setError] = useState<string>()
  useEffect(() => {
    let alive = true
    const load = () => api.get<Status>('/system/status').then((s) => { if (alive) { setSt(s); setError(undefined) } }).catch((e) => alive && setError(e.message))
    load()
    const id = setInterval(load, 5000)
    return () => { alive = false; clearInterval(id) }
  }, [])

  if (!st) return error ? <div className="rounded-xl bg-red-500/10 px-4 py-3 text-sm text-red-500 ring-1 ring-red-500/30">{error}</div> : <div className="grid place-items-center py-24"><Spinner /></div>
  const h = healthStyle[st.health]
  const ing = st.ingest
  const ch = st.clickhouse
  const kernelDrops = ing.kernel?.RcvbufErrors ?? 0
  const minFree = st.disks.length ? Math.min(...st.disks.map((d) => (d.total ? (d.free / d.total) * 100 : 100))) : 100
  const ratio = st.storage.bytes_on_disk ? st.storage.bytes_uncompressed / st.storage.bytes_on_disk : 0

  return (
    <>
      <PageHeader title={t('System status')} subtitle={t('Messages per second, dropped packets, disk space and ClickHouse health')} icon={<Activity className="size-5" />}
        actions={<>
          <span className={cx('inline-flex items-center gap-2 rounded-xl px-3 py-2 text-sm font-medium ring-1', h.cls)}>{h.icon}{h.label()}</span>
          <span className="text-xs text-muted">{t('Updated {time}', { time: fmtTime(st.checked, false) })}</span>
        </>} />

      {error && <div className="mb-4 rounded-xl bg-red-500/10 px-4 py-3 text-sm text-red-500 ring-1 ring-red-500/30">{error}</div>}
      {st.issues.length > 0 && (
        <div className="mb-6 space-y-2">
          {st.issues.map((is) => (
            <div key={is.key} className={cx('flex items-start gap-3 rounded-xl px-4 py-3 text-sm ring-1', is.severity === 'critical' ? 'bg-red-500/10 ring-red-500/30' : 'bg-amber-500/10 ring-amber-500/30')}>
              {is.severity === 'critical' ? <CircleAlert className="mt-0.5 size-4 shrink-0 text-red-500" /> : <AlertTriangle className="mt-0.5 size-4 shrink-0 text-amber-500" />}
              <div className="min-w-0 flex-1 break-words">{t(is.message, is.params)}</div>
              <span className="shrink-0 text-xs text-muted">{t('since {time}', { time: fmtTime(is.since * 1000) })}</span>
            </div>
          ))}
        </div>
      )}

      <div className="mb-6 grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <Stat label={t('Messages per second')} value={rate(ing.rate)} sub={t('peak {n}/s in the last hour', { n: rate(ing.peak) })} icon={<Gauge className="size-5" />} accent="#3987e5" />
        <Stat label={t('Dropped (last hour)')} value={fmtNum(ing.dropped_hour)} sub={t('{n} since start, {k} by the kernel', { n: fmtNum(ing.dropped), k: fmtNum(kernelDrops) })} icon={<PackageX className="size-5" />} accent={ing.dropped_hour ? '#e34948' : '#199e70'} />
        <Stat label={t('Lowest free disk space')} value={`${minFree.toFixed(0)}%`} sub={st.disks.length ? t('{n} disks checked', { n: st.disks.length }) : t('Unknown')} icon={<HardDrive className="size-5" />} accent={minFree < 5 ? '#e34948' : minFree < 15 ? '#eda100' : '#199e70'} />
        <Stat label="ClickHouse" value={ch.ok ? t('Online') : t('Offline')} sub={ch.ok ? t('{ms} ms response · version {v}', { ms: ch.latency_ms.toFixed(1), v: ch.version }) : ch.error} icon={<Database className="size-5" />} accent={ch.ok ? '#199e70' : '#e34948'} />
      </div>

      <Card className="mb-6" title={t('Messages per second')} subtitle={t('Last hour, per 10 seconds')}>
        <RateChart points={ing.series} />
      </Card>

      <div className="grid grid-cols-1 gap-6 xl:grid-cols-2">
        <Card title={t('Ingest pipeline')} subtitle={t('Counters since the server started')}>
          <Rows rows={[
            [t('Syslog received (UDP)'), fmtNum(ing.udp_packets)],
            [t('Syslog received (TCP)'), t('{n} messages, {c} open connections', { n: fmtNum(ing.tcp_messages), c: ing.tcp_connections })],
            [t('Processed (all sources, probes included)'), fmtNum(ing.parsed)],
            [t('Stored in ClickHouse'), fmtNum(ing.inserted)],
            [t('Queue'), t('{n} of {max}', { n: fmtNum(ing.queue_length), max: fmtNum(ing.queue_capacity) }) + (ing.pending ? ' · ' + t('{n} in the current batch', { n: fmtNum(ing.pending) }) : '')],
            [t('Last stored'), ing.last_insert ? fmtTime(ing.last_insert * 1000) : '–'],
          ]} />
        </Card>
        <Card title={t('Dropped and rejected')} subtitle={t('Messages that did not make it into the log store')}>
          <Rows rows={[
            [t('Dropped: ingest queue full'), <Num n={ing.dropped} bad />],
            [t('Dropped by the kernel: UDP receive buffer full'), ing.kernel ? <Num n={kernelDrops} bad /> : t('Unknown')],
            [t('UDP receive errors (kernel)'), ing.kernel ? <Num n={ing.kernel.InErrors ?? 0} bad /> : t('Unknown')],
            [t('Rejected by the source allowlist'), <Link to="/allowlist" className="text-indigo-400 hover:underline">{fmtNum(ing.rejected)}</Link>],
            [t('Failed inserts'), <Num n={ing.insert_errors} bad />],
            [t('Discarded after failed inserts'), <Num n={ing.discarded} bad />],
          ]} />
          {ing.last_error && <p className="mt-3 break-words rounded-lg bg-red-500/10 px-3 py-2 font-mono text-xs text-red-500">{ing.last_error}</p>}
          {kernelDrops > 0 && <p className="mt-3 text-xs text-muted">{t('Kernel drops mean bursts arrive faster than the server reads them. Raise udp_read_buffer in the configuration and net.core.rmem_max on the host.')}</p>}
        </Card>

        <Card title={t('Disk space')}>
          {!st.disks.length ? <p className="text-sm text-muted">{t('Unknown')}</p> : (
            <div className="space-y-4">
              {st.disks.map((d) => {
                const used = d.total ? 100 - (d.free / d.total) * 100 : 0
                return (
                  <div key={d.name + d.path}>
                    <div className="flex items-baseline justify-between gap-3 text-sm">
                      <span className="font-medium">{d.name === 'Server data' ? t('Server data') : d.name}</span>
                      <span className="text-xs text-muted">{t('{free} free of {total}', { free: fmtBytes(d.free), total: fmtBytes(d.total) })}</span>
                    </div>
                    <div className="mt-1.5 h-2 overflow-hidden rounded-full bg-[var(--panel-2)]">
                      <div className={cx('h-full rounded-full', used > 95 ? 'bg-red-500' : used > 85 ? 'bg-amber-500' : 'bg-gradient-to-r from-indigo-500 to-cyan-500')} style={{ width: `${used}%` }} />
                    </div>
                    <div className="mt-1 font-mono text-[11px] text-muted">{d.path}</div>
                  </div>
                )
              })}
            </div>
          )}
        </Card>

        <Card title={t('ClickHouse')} subtitle={ch.ok ? t('Up for {d}', { d: fmtDuration(ch.uptime_sec) }) : undefined}>
          {!ch.ok ? <p className="break-words text-sm text-red-500">{ch.error}</p> : (
            <Rows rows={[
              [t('Stored messages'), fmtNum(st.storage.rows)],
              [t('Size on disk'), fmtBytes(st.storage.bytes_on_disk) + (ratio ? ' · ' + t('{x}× compressed', { x: ratio.toFixed(1) }) : '')],
              [t('Oldest and newest'), st.storage.rows ? `${fmtTime(st.storage.oldest)} – ${fmtTime(st.storage.newest)}` : '–'],
              [t('Data parts'), t('{n} in {p} days, at most {m} in one day', { n: fmtNum(ch.parts), p: st.storage.partitions, m: ch.max_parts_per_partition })],
              [t('Running merges'), fmtNum(ch.merges)],
              [t('Pending mutations'), ch.mutations ? <Badge className="bg-amber-500/10 text-amber-500 ring-amber-500/30">{fmtNum(ch.mutations)}</Badge> : '0'],
              [t('Memory in use'), fmtBytes(ch.memory_bytes)],
            ]} />
          )}
        </Card>

        <Card title={<span className="flex items-center gap-2"><Server className="size-4 text-indigo-400" />{t('Server')}</span>} subtitle={t('Version {v}, up for {d}', { v: st.version, d: fmtDuration(st.process.uptime_sec) })}>
          <Rows rows={[
            [t('Load (1, 5, 15 min)'), `${st.process.load.map((l) => l.toFixed(2)).join(' · ')} ` + t('on {n} CPUs', { n: st.process.cpus })],
            [t('System memory'), st.process.mem_total ? t('{free} free of {total}', { free: fmtBytes(st.process.mem_available), total: fmtBytes(st.process.mem_total) }) : t('Unknown')],
            [t('Memory used by the server'), fmtBytes(st.process.rss_bytes || st.process.heap_bytes)],
            [t('Goroutines'), fmtNum(st.process.goroutines)],
          ]} />
        </Card>

        <Card title={<span className="flex items-center gap-2"><Boxes className="size-4 text-indigo-400" />{t('Components')}</span>}>
          <ul className="divide-y divide-[var(--border)]">
            {st.components.map((c) => (
              <li key={c.key} className="flex items-center justify-between gap-3 py-2.5 first:pt-0 last:pb-0">
                <div className="flex min-w-0 items-center gap-3">
                  <span className={cx('size-2.5 shrink-0 rounded-full', stateDot[c.state])} />
                  <div className="min-w-0">
                    <div className="text-sm font-medium">{t(componentName[c.key] ?? c.key)}</div>
                    <div className="truncate text-xs text-muted">{t(c.detail, c.key === 'backup' && c.params?.time ? { ...c.params, time: fmtTime(Number(c.params.time) * 1000) } : c.params)}</div>
                  </div>
                </div>
                {c.link && <Link to={c.link} title={t('Open')} className="rounded-lg p-1.5 text-muted hover:bg-[var(--panel-2)] hover:text-[var(--text)]"><ArrowRight className="size-4" /></Link>}
              </li>
            ))}
          </ul>
        </Card>
      </div>
      <p className="mt-4 text-xs text-muted">{t('Problems are also sent to notification channels that subscribe to System status, and by e-mail to the default recipient.')}</p>
    </>
  )
}

function Num({ n, bad }: { n: number; bad?: boolean }) {
  return <span className={cx('tabular-nums', bad && n > 0 && 'font-medium text-red-500')}>{fmtNum(n)}</span>
}

function Rows({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <table className="w-full text-sm">
      <tbody>
        {rows.map(([k, v]) => (
          <tr key={k} className="border-b border-app/60 last:border-0">
            <td className="py-2 pr-4 text-muted">{k}</td>
            <td className="py-2 text-right tabular-nums">{v}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function RateChart({ points }: { points: Point[] }) {
  const ink = chartInk()
  if (points.length < 2) return <p className="py-10 text-center text-sm text-muted">{t('Collecting data, the chart fills in over the next minutes')}</p>
  const data = points.map((p) => ({ ...p, t: p.t * 1000 }))
  const fmt = (v: number) => new Date(v).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
  const series: [keyof Point, string, string][] = [['received', t('Received'), '#3987e5'], ['stored', t('Stored'), '#199e70'], ['dropped', t('Dropped'), '#e34948']]
  return (
    <div>
      <div className="h-56">
        <ResponsiveContainer>
          <AreaChart data={data}>
            <CartesianGrid stroke={ink.grid} vertical={false} />
            <XAxis dataKey="t" type="number" domain={['dataMin', 'dataMax']} scale="time" tickFormatter={fmt} stroke={ink.axis} fontSize={11} tickLine={false} axisLine={false} minTickGap={50} />
            <YAxis stroke={ink.axis} fontSize={11} tickLine={false} axisLine={false} width={44} tickFormatter={(v) => rate(v)} />
            <Tooltip contentStyle={{ background: ink.tip, border: '1px solid var(--border)', borderRadius: 12, fontSize: 12 }} labelFormatter={(v) => fmt(Number(v))}
              formatter={(v, name) => [rate(Number(v)) + '/s', series.find((s) => s[0] === name)?.[1] ?? name]} />
            {series.map(([k, , c]) => <Area key={k} dataKey={k} stroke={c} strokeWidth={2} fill={c} fillOpacity={k === 'received' ? 0.12 : 0.05} isAnimationActive={false} />)}
          </AreaChart>
        </ResponsiveContainer>
      </div>
      <div className="mt-3 flex flex-wrap gap-x-4 gap-y-1.5 text-xs text-muted">
        {series.map(([k, l, c]) => <span key={k} className="flex items-center gap-1.5"><span className="size-2.5 rounded-sm" style={{ background: c }} />{l}</span>)}
      </div>
    </div>
  )
}
