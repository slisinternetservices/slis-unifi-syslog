import { useMemo, type ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import { Smartphone, Search, Wifi, Cable, Lock } from 'lucide-react'
import { api } from '../api'
import { useUrlState } from '../components/Filters'
import { Badge, Card, Empty, PageHeader, Select, Spinner, useAsync } from '../components/ui'
import { fmtAgo, fmtNum } from '../lib/format'
import { t } from '../i18n'

interface Client { mac: string; name: string; hostname: string; ip: string; site: string; kind: string; source: 'unifi' | 'dhcp'; updated: number }

const kindIcon: Record<string, ReactNode> = { wireless: <Wifi className="size-4" />, wired: <Cable className="size-4" />, vpn: <Lock className="size-4" />, teleport: <Lock className="size-4" /> }

export default function Clients() {
  const nav = useNavigate()
  const [st, set] = useUrlState({ q: '', source: '' })
  const { data, loading } = useAsync(() => api.get<Client[]>('/clients'))
  const list = useMemo(() => (data ?? []).filter((c) =>
    (!st.source || c.source === st.source) &&
    (!st.q || `${c.name} ${c.hostname} ${c.mac} ${c.ip} ${c.site}`.toLowerCase().includes(st.q.toLowerCase()))), [data, st.q, st.source])
  return (
    <>
      <PageHeader title={t('Clients')} subtitle={t('Names shown in the logs instead of MAC addresses')} icon={<Smartphone className="size-5" />}
        actions={<>
          <div className="relative"><Search className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted" /><input value={st.q} onChange={(e) => set({ q: e.target.value })} placeholder={t('Search…')} className="input !pl-9 w-56" /></div>
          <Select value={st.source} onChange={(source) => set({ source })} className="w-48" options={[{ value: '', label: t('All sources') }, { value: 'unifi', label: t('From UniFi controller') }, { value: 'dhcp', label: t('Learnt from DHCP') }]} />
        </>} />
      <Card pad={false}>
        {loading && !data ? <div className="grid place-items-center py-16"><Spinner /></div>
          : !list.length ? <Empty icon={<Smartphone className="size-8" />} title={data?.length ? t('No clients found') : t('No clients yet')} text={data?.length ? t('Adjust the filters.') : t('Add a UniFi controller under Configuration, or wait for DHCP logs: names are learnt from the hostnames clients send.')} />
          : (
            <div className="overflow-x-auto scroll-thin">
              <table className="w-full text-sm">
                <thead className="text-left text-[11px] uppercase tracking-wide text-muted"><tr className="border-b border-app">
                  <th className="px-5 py-3 font-medium">{t('Name')}</th><th className="px-3 py-3 font-medium">MAC</th><th className="px-3 py-3 font-medium">{t('IP address')}</th>
                  <th className="px-3 py-3 font-medium">{t('Site')}</th><th className="px-3 py-3 font-medium">{t('Source')}</th><th className="px-3 py-3 font-medium">{t('Updated')}</th>
                </tr></thead>
                <tbody>
                  {list.slice(0, 2000).map((c) => (
                    <tr key={c.mac} onClick={() => nav('/logs?' + new URLSearchParams({ q: `client:${c.mac}`, preset: '24h' }))} className="cursor-pointer border-b border-app/60 hover:bg-[var(--panel-2)]">
                      <td className="px-5 py-2.5">
                        <div className="flex items-center gap-2 font-medium"><span className="text-muted">{kindIcon[c.kind]}</span>{c.name || c.hostname || '–'}</div>
                        {c.name && c.hostname && c.hostname !== c.name && <div className="text-xs text-muted">{c.hostname}</div>}
                      </td>
                      <td className="px-3 py-2.5 font-mono text-xs">{c.mac}</td>
                      <td className="px-3 py-2.5 font-mono text-xs">{c.ip}</td>
                      <td className="px-3 py-2.5">{!c.site || c.site === 'default' ? t('Default') : c.site}</td>
                      <td className="px-3 py-2.5">{c.source === 'unifi' ? <Badge className="bg-indigo-500/10 text-indigo-400 ring-indigo-500/30">UniFi</Badge> : <Badge>DHCP</Badge>}</td>
                      <td className="px-3 py-2.5 whitespace-nowrap text-xs text-muted">{c.updated ? fmtAgo(c.updated * 1000) : '–'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <p className="px-5 py-3 text-xs text-muted">{t('{n} clients', { n: fmtNum(list.length) })}{list.length > 2000 && ' · ' + t('showing the first 2,000; search to narrow down')}</p>
            </div>
          )}
      </Card>
    </>
  )
}
