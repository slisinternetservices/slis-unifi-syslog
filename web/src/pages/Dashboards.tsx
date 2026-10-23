import type { ReactNode } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { ShieldX, Wifi, Network, Lock, Siren } from 'lucide-react'
import { api, type Bucket, type TopItem } from '../api'
import { Card, PageHeader, Spinner, Stat, useAsync } from '../components/ui'
import { BarList, Timeline } from '../components/charts'
import { WorldMap } from '../components/WorldMap'
import { RangePicker, RefreshButton, SiteFilter, toRange, useUrlState } from '../components/Filters'
import { t } from '../i18n'
import { actionLabel, fmtCompact, fmtNum, severityLabel } from '../lib/format'
import { asLabel, countryName, flag } from '../lib/countries'

// A ready-made dashboard is a set of counts, one timeline and top lists,
// all evaluated server-side in one /search/panels request.
interface StatSpec { key: string; label: string; query: string; accent: string; sub?: string }
interface TopSpec { key: string; title: string; subtitle?: string; query: string; field: string; fallback?: string; n?: number; map?: boolean; wide?: boolean }
interface DashSpec {
  title: string; subtitle: string; icon: ReactNode
  stats: StatSpec[]
  timeline: { query: string; group: string }
  tops: TopSpec[]
}

const wifiConnect = 'category:wifi event:associated OR category:wifi event:sta_join OR category:wifi event:authenticated'
const wifiRoam = 'category:wifi event:reassociated OR category:wifi event:*roam*'
const wifiProblems = 'category:wifi action:fail OR category:wifi event:auth_failed OR category:wifi event:deauthenticated OR category:wifi event:*failure*'
const dhcpLeases = 'category:dhcp event:DHCPACK OR category:dhcp event:LEASE_ALLOC OR category:dhcp event:LEASE_RENEW'
const wireguard = 'category:vpn app:wireguard OR category:vpn app:wg* OR category:vpn app:teleport'
const siteToSite = 'category:vpn app:charon OR category:vpn app:ipsec OR category:vpn app:strongswan'

const specs: Record<string, () => DashSpec> = {
  firewall: () => ({
    title: t('Firewall'), subtitle: t('Blocked and allowed traffic, sources and rules'), icon: <ShieldX className="size-5" />,
    stats: [
      { key: 'total', label: t('Firewall events'), query: 'category:firewall', accent: '#3987e5' },
      { key: 'blocked', label: t('Blocked'), query: 'category:firewall action:block', accent: '#e66767' },
      { key: 'rejected', label: t('Rejected'), query: 'category:firewall action:reject', accent: '#d95926' },
      { key: 'allowed', label: t('Allowed'), query: 'category:firewall action:allow', accent: '#199e70' },
    ],
    timeline: { query: 'category:firewall', group: 'action' },
    tops: [
      { key: 'countries', title: t('Blocked traffic per country'), subtitle: t('Source country of blocked packets from the internet'), query: 'category:firewall action:block', field: 'src_country', n: 100, map: true, wide: true },
      { key: 'src', title: t('Blocked sources'), query: 'category:firewall action:block', field: 'src_ip' },
      { key: 'as', title: t('Blocked networks'), subtitle: t('Autonomous systems (providers) of blocked sources'), query: 'category:firewall action:block', field: 'src_as' },
      { key: 'dport', title: t('Blocked ports'), query: 'category:firewall action:block', field: 'dst_port' },
      { key: 'rules', title: t('Firewall rules'), query: 'category:firewall', field: 'rule' },
      { key: 'clients', title: t('Blocked internal clients'), subtitle: t('Clients on your own network whose traffic was blocked'), query: 'category:firewall action:block', field: 'src_name' },
      { key: 'in', title: t('Interfaces'), query: 'category:firewall action:block', field: 'in_if' },
    ],
  }),
  wifi: () => ({
    title: t('Wi-Fi'), subtitle: t('Connections, roaming and connection problems'), icon: <Wifi className="size-5" />,
    stats: [
      { key: 'total', label: t('Wi-Fi events'), query: 'category:wifi', accent: '#3987e5' },
      { key: 'connect', label: t('Connections'), query: wifiConnect, accent: '#199e70' },
      { key: 'roam', label: t('Roaming'), query: wifiRoam, accent: '#9085e9', sub: t('Clients moving to another access point') },
      { key: 'problems', label: t('Connection problems'), query: wifiProblems, accent: '#e66767', sub: t('Failed authentication and disconnects') },
    ],
    timeline: { query: 'category:wifi', group: 'event_type' },
    tops: [
      { key: 'problem_clients', title: t('Clients with connection problems'), query: wifiProblems, field: 'client_name' },
      { key: 'problem_macs', title: t('Problems per MAC address'), query: wifiProblems, field: 'client_mac' },
      { key: 'roamers', title: t('Clients that roam most'), query: wifiRoam, field: 'client_name' },
      { key: 'aps', title: t('Access points'), subtitle: t('Most Wi-Fi events'), query: 'category:wifi', field: 'device_name', fallback: 'hostname' },
      { key: 'ssids', title: t("SSID's"), query: 'category:wifi OR category:client', field: 'ssid' },
      { key: 'events', title: t('Event types'), query: 'category:wifi', field: 'event_type' },
    ],
  }),
  dhcp: () => ({
    title: t('DHCP'), subtitle: t('Leases, clients and refused requests'), icon: <Network className="size-5" />,
    stats: [
      { key: 'total', label: t('DHCP events'), query: 'category:dhcp', accent: '#3987e5' },
      { key: 'leases', label: t('Leases'), query: dhcpLeases, accent: '#199e70' },
      { key: 'discover', label: t('Requests'), query: 'category:dhcp event:DHCPDISCOVER', accent: '#c98500' },
      { key: 'nak', label: t('Refused'), query: 'category:dhcp event:DHCPNAK', accent: '#e66767', sub: t('DHCPNAK: address not available') },
    ],
    timeline: { query: 'category:dhcp', group: 'event_type' },
    tops: [
      { key: 'clients', title: t('Clients'), query: dhcpLeases, field: 'client_name' },
      { key: 'macs', title: t('MAC addresses'), query: dhcpLeases, field: 'client_mac' },
      { key: 'ips', title: t('Assigned addresses'), query: dhcpLeases, field: 'client_ip' },
      { key: 'nets', title: t('Networks'), subtitle: t('Interface the lease was handed out on'), query: dhcpLeases, field: 'in_if' },
      { key: 'nak', title: t('Refused clients'), query: 'category:dhcp event:DHCPNAK', field: 'client_mac' },
      { key: 'events', title: t('Event types'), query: 'category:dhcp', field: 'event_type' },
    ],
  }),
  vpn: () => ({
    title: t('VPN'), subtitle: t('WireGuard, Teleport and site-to-site tunnels'), icon: <Lock className="size-5" />,
    stats: [
      { key: 'total', label: t('VPN events'), query: 'category:vpn', accent: '#3987e5' },
      { key: 'up', label: t('Connected'), query: 'category:vpn event:connected', accent: '#199e70' },
      { key: 'down', label: t('Disconnected'), query: 'category:vpn event:disconnected', accent: '#c98500' },
      { key: 'fail', label: t('Failed'), query: 'category:vpn action:fail', accent: '#e66767', sub: t('Failed authentication') },
    ],
    timeline: { query: 'category:vpn', group: 'app' },
    tops: [
      { key: 'types', title: t('VPN type'), subtitle: t('Process that logged the event'), query: 'category:vpn', field: 'app' },
      { key: 'wg', title: t('WireGuard and Teleport peers'), query: wireguard, field: 'src_ip' },
      { key: 's2s', title: t('Site-to-site (IPsec)'), query: siteToSite, field: 'event_type' },
      { key: 'fail', title: t('Failed connection attempts'), query: 'category:vpn action:fail', field: 'src_ip' },
      { key: 'countries', title: t('Countries'), subtitle: t('Where VPN connections come from'), query: 'category:vpn', field: 'src_country' },
      { key: 'events', title: t('Event types'), query: 'category:vpn', field: 'event_type' },
    ],
  }),
  ips: () => ({
    title: t('Threats (IPS)'), subtitle: t('Intrusion detection and prevention'), icon: <Siren className="size-5" />,
    stats: [
      { key: 'total', label: t('Threats'), query: 'category:ips', accent: '#d95926' },
      { key: 'blocked', label: t('Blocked'), query: 'category:ips action:block', accent: '#e66767', sub: t('Stopped by IPS') },
      { key: 'high', label: t('High severity'), query: 'category:ips severity:<=2', accent: '#c98500', sub: t('Critical or worse') },
      { key: 'alert', label: t('Detected only'), query: 'category:ips action:alert', accent: '#3987e5', sub: t('Detected, not blocked') },
    ],
    timeline: { query: 'category:ips', group: 'severity' },
    tops: [
      { key: 'countries', title: t('Threats per country'), subtitle: t('Source country of IPS alerts'), query: 'category:ips', field: 'src_country', n: 100, map: true, wide: true },
      { key: 'sigs', title: t('IPS signatures'), query: 'category:ips', field: 'rule' },
      { key: 'class', title: t('Classification'), query: 'category:ips', field: 'classification' },
      { key: 'src', title: t('Attackers'), query: 'category:ips', field: 'src_ip' },
      { key: 'as', title: t('Attacking networks'), query: 'category:ips', field: 'src_as' },
      { key: 'dst', title: t('Targets'), query: 'category:ips', field: 'dst_ip' },
      { key: 'dport', title: t('Target ports'), query: 'category:ips', field: 'dst_port' },
    ],
  }),
}

export const dashboardKinds = Object.keys(specs)

/** Search term for a clicked top-list key. */
function term(field: string, k: string) {
  const name: Record<string, string> = { src_ip: 'src', dst_ip: 'dst', dst_port: 'dport', client_mac: 'client', event_type: 'event', in_if: 'in', hostname: 'host', classification: 'field.classification', src_as: 'src_asn', dst_as: 'dst_asn' }
  const v = field.endsWith('_as') ? k.split('|')[0] : k
  return `${name[field] ?? field}:"${v.replace(/"/g, '')}"`
}

// Terms are ANDed and OR splits the whole query, so a drill-down term has to
// be added to every alternative.
const drill = (q: string, extra: string) => q.split(' OR ').map((p) => `${p} ${extra}`).join(' OR ')

// Outcomes keep their meaning across dashboards: blocked is always red.
const fixedColors: Record<string, Record<string, string>> = {
  action: { block: '#e66767', reject: '#d95926', allow: '#199e70', fail: '#c98500', alert: '#9085e9' },
  severity: { '0': '#b42318', '1': '#d92d20', '2': '#e66767', '3': '#d95926', '4': '#c98500', '5': '#3987e5', '6': '#199e70', '7': '#8591a8' },
}

function formatter(field: string): (k: string) => string {
  if (field.endsWith('_country')) return (k) => `${flag(k)} ${countryName(k)}`
  if (field.endsWith('_as')) return asLabel
  if (field === 'action') return (k) => (actionLabel[k] ? t(actionLabel[k]) : k)
  if (field === 'severity') return (k) => (severityLabel[+k] ? t(severityLabel[+k]) : k)
  return (k) => k
}

interface GeoState { enabled: boolean; loaded: boolean; error: string }

// Why a country map is empty, so the hint matches the real situation.
function geoHint(g?: GeoState) {
  if (!g?.enabled) return t('No country data yet. Turn on GeoIP under Configuration → GeoIP.')
  if (!g.loaded) return g.error ? t('GeoIP is on, but the database could not be downloaded: {error}', { error: g.error }) : t('GeoIP is on, but the database is still being downloaded.')
  return t('No blocked traffic from internet addresses in this period. Only logs received after GeoIP was turned on have a country.')
}

interface Panels { [key: string]: { count?: number; items?: TopItem[]; error?: string } }

export default function Dashboards() {
  const { kind = 'firewall' } = useParams()
  return <DashboardView key={kind} kind={specs[kind] ? kind : 'firewall'} />
}

function DashboardView({ kind }: { kind: string }) {
  const spec = specs[kind]()
  const [st, set] = useUrlState({ preset: '24h', from: '', to: '', site: '' })
  const nav = useNavigate()
  const body = () => {
    const r = toRange(st)
    return { from: r.from.toISOString(), to: r.to.toISOString(), sites: st.site ? [st.site] : [] }
  }
  const { data, loading, reload } = useAsync(async () => {
    const b = body()
    const [panels, hist, geo] = await Promise.all([
      api.post<Panels>('/search/panels', { ...b, panels: [
        ...spec.stats.map((s) => ({ key: 'stat:' + s.key, query: s.query })),
        ...spec.tops.map((x) => ({ key: 'top:' + x.key, query: x.query, field: x.field, n: x.n ?? 10 })),
        // Names only exist with a controller; fall back to what syslog has.
        ...spec.tops.filter((x) => x.fallback).map((x) => ({ key: 'fb:' + x.key, query: x.query, field: x.fallback, n: x.n ?? 10 })),
      ] }),
      api.post<{ buckets: Bucket[]; step: number }>('/search/histogram', { ...b, query: spec.timeline.query, group_by: spec.timeline.group }),
      spec.tops.some((x) => x.map) ? api.get<GeoState>('/geoip/status').catch(() => undefined) : Promise.resolve(undefined),
    ])
    return { panels, hist, geo }
  }, [kind, st.preset, st.from, st.to, st.site])

  const go = (query: string) => {
    const q = new URLSearchParams({ q: query, preset: st.preset })
    if (st.preset === 'custom') { q.set('from', st.from); q.set('to', st.to) }
    if (st.site) q.set('site', st.site)
    nav('/logs?' + q)
  }
  const items = (k: string) => data?.panels['top:' + k]?.items ?? []
  const pick = (x: TopSpec) => {
    const own = items(x.key)
    return own.length || !x.fallback ? { list: own, field: x.field } : { list: data?.panels['fb:' + x.key]?.items ?? [], field: x.fallback }
  }

  return (
    <>
      <PageHeader title={spec.title} subtitle={spec.subtitle} icon={spec.icon}
        actions={<>
          <SiteFilter value={st.site} onChange={(site) => set({ site })} />
          <RangePicker value={st} onChange={(r) => set({ preset: r.preset, from: r.from ?? '', to: r.to ?? '' })} />
          <RefreshButton onClick={reload} loading={loading} />
        </>} />
      {!data ? <div className="grid place-items-center py-24"><Spinner className="size-8" /></div> : (
        <div className="space-y-6">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
            {spec.stats.map((s) => {
              const n = data.panels['stat:' + s.key]?.count ?? 0
              return (
                <button key={s.key} onClick={() => go(s.query)} className="cursor-pointer rounded-2xl text-left">
                  <Stat label={s.label} value={fmtCompact(n)} sub={s.sub ?? t('{n} in this period', { n: fmtNum(n) })} accent={s.accent} />
                </button>
              )
            })}
          </div>
          <Card title={t('Over time')} subtitle={t('Click a bar to open that period in the logs')}>
            <Timeline buckets={data.hist.buckets ?? []} stepSec={data.hist.step} byKey format={formatter(spec.timeline.group)} colorOf={(k) => fixedColors[spec.timeline.group]?.[k]} range={toRange(st)}
              onSelect={(f, to) => {
                const q = new URLSearchParams({ q: spec.timeline.query, preset: 'custom', from: localIso(f), to: localIso(to) })
                if (st.site) q.set('site', st.site)
                nav('/logs?' + q)
              }} />
          </Card>
          <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
            {spec.tops.map((x) => {
              const { list, field } = pick(x)
              const fmt = formatter(field)
              const err = data.panels['top:' + x.key]?.error
              if (x.map) {
                return (
                  <Card key={x.key} className="lg:col-span-3" title={x.title} subtitle={x.subtitle}>
                    {!list.length ? <p className="py-6 text-center text-sm text-muted">{geoHint(data.geo)}</p> : (
                      <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
                        <div className="lg:col-span-2"><WorldMap items={list} onSelect={(cc) => go(drill(x.query, `src_country:${cc}`))} /></div>
                        <BarList mono={false} items={list.slice(0, 10)} format={fmt} color="#e66767" onClick={(k) => go(drill(x.query, term(x.field, k)))} />
                      </div>
                    )}
                  </Card>
                )
              }
              return (
                <Card key={x.key} className={x.wide ? 'lg:col-span-2' : undefined} title={x.title} subtitle={x.subtitle}>
                  {err ? <p className="py-6 text-center text-sm text-red-500">{t(err)}</p>
                    : <BarList mono={!field.endsWith('_country') && !field.endsWith('_as')} items={list} format={fmt} onClick={(k) => go(drill(x.query, term(field, k)))} />}
                </Card>
              )
            })}
          </div>
        </div>
      )}
    </>
  )
}

const localIso = (d: Date) => new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16)
