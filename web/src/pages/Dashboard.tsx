import { Link, useNavigate } from 'react-router-dom'
import { Activity, ShieldX, Siren, TriangleAlert, LayoutDashboard, Gauge, RadioTower } from 'lucide-react'
import { api, can, type Bucket, type TopItem } from '../api'
import { useAuth } from '../App'
import { Badge, Card, PageHeader, Spinner, Stat, cx, useAsync } from '../components/ui'
import { BarList, Timeline } from '../components/charts'
import { WorldMap } from '../components/WorldMap'
import { countryName, flag } from '../lib/countries'
import { RangePicker, RefreshButton, SiteFilter, toRange, useUrlState } from '../components/Filters'
import { t } from '../i18n'
import { catColor, catLabel, fmtAgo, fmtBytes, fmtCompact, fmtNum, severityLabel } from '../lib/format'

interface Dash {
  overview: { total: number; blocked: number; threats: number; errors: number; by_category: TopItem[]; by_site: TopItem[]; by_severity: TopItem[]; timeline: Bucket[]; step_sec: number }
  top: Record<string, TopItem[]>
  ingest: Record<string, number>
}

export default function Dashboard() {
  const [st, set] = useUrlState({ preset: '24h', from: '', to: '', site: '' })
  const nav = useNavigate()
  const { data, loading, reload } = useAsync(() => {
    const r = toRange(st)
    const q = new URLSearchParams({ from: r.from.toISOString(), to: r.to.toISOString() })
    if (st.site) q.set('sites', st.site)
    return api.get<Dash>('/dashboard?' + q)
  }, [st.preset, st.from, st.to, st.site])

  const go = (query: string, extra: Record<string, string> = {}) => {
    const q = new URLSearchParams({ q: query, preset: st.preset, ...extra })
    if (st.preset === 'custom') { q.set('from', st.from); q.set('to', st.to) }
    if (st.site) q.set('site', st.site)
    nav('/logs?' + q)
  }
  const o = data?.overview

  return (
    <>
      <PageHeader title={t('Overview')} subtitle={t('All UniFi sites at a glance')} icon={<LayoutDashboard className="size-5" />}
        actions={<>
          <SiteFilter value={st.site} onChange={(site) => set({ site })} />
          <RangePicker value={st} onChange={(r) => set({ preset: r.preset, from: r.from ?? '', to: r.to ?? '' })} />
          <RefreshButton onClick={reload} loading={loading} />
        </>} />

      {!data ? <div className="grid place-items-center py-24"><Spinner className="size-8" /></div> : (
        <div className="space-y-6">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-5">
            <Stat label={t('Messages')} value={fmtCompact(o!.total)} sub={t('{n} in this period', { n: fmtNum(o!.total) })} icon={<Activity className="size-5" />} accent="#3987e5" />
            <Stat label={t('Blocked')} value={fmtCompact(o!.blocked)} sub={t('by the firewall')} icon={<ShieldX className="size-5" />} accent="#e66767" />
            <Stat label={t('Threats')} value={fmtCompact(o!.threats)} sub={t('IPS/IDS alerts')} icon={<Siren className="size-5" />} accent="#d95926" />
            <Stat label={t('Errors')} value={fmtCompact(o!.errors)} sub={t('severity error or higher')} icon={<TriangleAlert className="size-5" />} accent="#c98500" />
            <Stat label={t('Ingest')} value={fmtCompact(data.ingest.parsed)} sub={t('{dropped} dropped · {errors} write errors', { dropped: fmtNum(data.ingest.dropped), errors: fmtNum(data.ingest.insert_errors) })} icon={<Gauge className="size-5" />} accent="#199e70" />
          </div>

          <ProbesOverview />

          <Card title={t('Messages per category')} subtitle={t('Click a bar to open that period in the logs')}>
            <Timeline buckets={o!.timeline ?? []} stepSec={o!.step_sec} range={toRange(st)} onSelect={(f, t) => go('', { preset: 'custom', from: localIso(f), to: localIso(t) })} />
          </Card>

          {(data.top.countries?.length ?? 0) > 0 && (
            <Card title={t('Blocked traffic and threats per country')} subtitle={t('Source country of blocked packets and IPS alerts')}>
              <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
                <div className="lg:col-span-2"><WorldMap items={data.top.countries} onSelect={(cc) => go(`category:firewall action:block src_country:${cc} OR category:ips src_country:${cc}`)} /></div>
                <BarList mono={false} items={data.top.countries.slice(0, 10)} color="#e66767" format={(k) => `${flag(k)} ${countryName(k)}`}
                  onClick={(cc) => go(`category:firewall action:block src_country:${cc} OR category:ips src_country:${cc}`)} />
              </div>
            </Card>
          )}

          <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
            <Card title={t('Categories')}><BarList mono={false} items={o!.by_category} color={catColor} format={catLabel} onClick={(k) => go(`category:${k}`)} /></Card>
            <Card title={t('Blocked sources')} subtitle={t('Top IP addresses stopped by the firewall')}>
              <BarList items={data.top.blocked_src} color="#e66767" onClick={(k) => go(`category:firewall action:block src:${k}`)} />
            </Card>
            <Card title={t('Blocked ports')}><BarList items={data.top.blocked_dport} color="#e66767" format={portName} onClick={(k) => go(`category:firewall action:block dport:${k}`)} /></Card>
            <Card title={t('IPS signatures')}><BarList items={data.top.ips_rules} color="#d95926" empty={t('No threats detected')} onClick={(k) => go(`category:ips rule:"${k}"`)} /></Card>
            <Card title={t('Firewall rules')}><BarList items={data.top.fw_rules} color="#e66767" onClick={(k) => go(`category:firewall rule:"${k}"`)} /></Card>
            <Card title={t('Most active Wi-Fi clients')}><BarList items={data.top.wifi_clients} color="#3987e5" onClick={(k) => go(`category:wifi client:${k}`)} /></Card>
            <Card title={t("SSID's")}><BarList items={data.top.ssids} color="#3987e5" onClick={(k) => go(`ssid:"${k}"`)} /></Card>
            <Card title={t('DHCP clients')}><BarList items={data.top.dhcp_clients} color="#199e70" onClick={(k) => go(`category:dhcp client_name:"${k}"`)} /></Card>
            <Card title={t('Devices')} subtitle={t('Most messages')}><BarList items={data.top.sources} color="#3987e5" onClick={(k) => go(`host:"${k}"`)} /></Card>
          </div>

          <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <Card title={t('Severity')}><BarList mono={false} items={[...o!.by_severity].sort((a, b) => +a.key - +b.key)} color="#c98500" format={(k) => (severityLabel[+k] ? t(severityLabel[+k]) : k)} onClick={(k) => go(`severity:${k}`)} /></Card>
            <Card title={t('Sites')}><BarList mono={false} items={o!.by_site} format={(k) => (k === 'default' ? t('Default') : k)} onClick={(k) => go('', { site: k })} /></Card>
          </div>
        </div>
      )}
    </>
  )
}

interface ProbeItem {
  id: number; name: string; site: string; enrolled: boolean; online: boolean; outdated: boolean; version: string; last_seen: number
  rate: number; buffer_records: number; buffer_bytes: number; buffer_max: number; devices: number
}

/** Status of the remote probes; hidden when there are none. */
function ProbesOverview() {
  const { user } = useAuth()
  const { data } = useAsync(() => api.get<ProbeItem[]>('/probes/summary'))
  const list = (data ?? []).filter((p) => p.enrolled)
  if (!list.length) return null
  const offline = list.filter((p) => !p.online).length
  return (
    <Card title={t('Remote probes')} pad={false}
      subtitle={offline ? t('{n} of {total} offline', { n: offline, total: list.length }) : t('All {n} probes online', { n: list.length })}
      actions={can.admin(user) && <Link to="/probes" className="text-xs text-indigo-400 hover:underline">{t('Manage')}</Link>}>
      <div className="grid grid-cols-1 divide-y divide-[var(--border)] sm:grid-cols-2 sm:divide-y-0 xl:grid-cols-3">
        {list.map((p) => {
          const buf = p.buffer_max ? Math.min(100, (p.buffer_bytes / p.buffer_max) * 100) : 0
          return (
            <div key={p.id} className="flex items-center gap-3 px-5 py-3">
              <span className={cx('grid size-9 shrink-0 place-items-center rounded-xl', p.online ? 'bg-emerald-500/15 text-emerald-500' : 'bg-red-500/15 text-red-500')}><RadioTower className="size-4" /></span>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="truncate text-sm font-medium">{p.name}</span>
                  <Badge>{p.site === 'default' ? t('Default') : p.site}</Badge>
                </div>
                <div className={cx('truncate text-xs', p.online ? 'text-muted' : 'text-red-500')}>
                  {p.online
                    ? t('{rate}/s · {devices} devices · buffer {buf}', { rate: p.rate.toFixed(1), devices: p.devices, buf: p.buffer_records ? `${fmtNum(p.buffer_records)} (${fmtBytes(p.buffer_bytes)})` : '0' })
                    : t('Offline since {ago}', { ago: p.last_seen ? fmtAgo(p.last_seen * 1000) : '–' })}
                </div>
                {buf >= 5 && <div className="mt-1 h-1 overflow-hidden rounded-full bg-slate-500/15"><div className={cx('h-full', buf >= 80 ? 'bg-amber-500' : 'bg-indigo-500')} style={{ width: `${buf}%` }} /></div>}
              </div>
            </div>
          )
        })}
      </div>
    </Card>
  )
}

const ports: Record<string, string> = { '22': 'SSH', '23': 'Telnet', '80': 'HTTP', '443': 'HTTPS', '445': 'SMB', '3389': 'RDP', '1433': 'MSSQL', '3306': 'MySQL', '5060': 'SIP', '8080': 'HTTP-alt', '8443': 'HTTPS-alt', '6379': 'Redis', '53': 'DNS', '25': 'SMTP', '21': 'FTP' }
const portName = (p: string) => (ports[p] ? `${p} · ${ports[p]}` : p)
const localIso = (d: Date) => new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16)
