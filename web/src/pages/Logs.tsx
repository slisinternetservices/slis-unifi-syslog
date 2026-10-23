import { Fragment, useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { ScrollText, Search, Radio, Pause, Download, Bookmark, HelpCircle, X, Plus, Minus, Tags } from 'lucide-react'
import { api, can, type Bucket, type LogRow } from '../api'
import { useAuth } from '../App'
import { Badge, Button, Card, Empty, Field, Input, Modal, PageHeader, Spinner, cx, useToast } from '../components/ui'
import { Timeline } from '../components/charts'
import { RangePicker, SiteFilter, toRange, useUrlState } from '../components/Filters'
import { t } from '../i18n'
import { actionClass, actionLabel, catColor, catLabel, fmtNum, fmtTime, primaryCategories, severities, severityClass } from '../lib/format'
import { asLabel, countryName, flag } from '../lib/countries'
import { SearchModal, type SearchDraft } from './SavedSearches'

const PAGE = 200

export default function Logs() {
  const [st, set] = useUrlState({ q: '', preset: '1h', from: '', to: '', site: '' })
  const [input, setInput] = useState(st.q)
  const [rows, setRows] = useState<LogRow[]>([])
  const [hist, setHist] = useState<{ buckets: Bucket[]; step: number; total: number }>()
  const [loading, setLoading] = useState(false)
  const [more, setMore] = useState(false)
  const [error, setError] = useState<string>()
  const [took, setTook] = useState(0)
  const [live, setLive] = useState(false)
  const [sel, setSel] = useState<LogRow>()
  const [help, setHelp] = useState(false)
  const [save, setSave] = useState(false)
  const [saveSearch, setSaveSearch] = useState<SearchDraft>()
  const { user } = useAuth()

  const body = useCallback((extra: object = {}) => {
    const r = toRange(st)
    return { query: st.q, from: r.from.toISOString(), to: r.to.toISOString(), sites: st.site ? [st.site] : [], limit: PAGE, ...extra }
  }, [st])

  const run = useCallback(async () => {
    setLoading(true)
    setError(undefined)
    try {
      const [res, h] = await Promise.all([
        api.post<{ rows: LogRow[]; took_ms: number }>('/search', body()),
        api.post<{ buckets: Bucket[]; step: number; total: number }>('/search/histogram', body({ group_by: 'category' })),
      ])
      setRows(res.rows)
      setTook(res.took_ms)
      setMore(res.rows.length === PAGE)
      setHist(h)
    } catch (e) {
      setError((e as Error).message)
      setRows([])
      setHist(undefined)
    } finally {
      setLoading(false)
    }
  }, [body])

  useEffect(() => { if (!live) run() }, [st.q, st.preset, st.from, st.to, st.site, live]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => setInput(st.q), [st.q])

  const loadMore = async () => {
    if (!rows.length) return
    setLoading(true)
    try {
      const res = await api.post<{ rows: LogRow[] }>('/search', body({ before: rows[rows.length - 1].ts }))
      setRows((r) => [...r, ...res.rows])
      setMore(res.rows.length === PAGE)
    } finally { setLoading(false) }
  }

  // Live tail
  const cursor = useRef(0)
  useEffect(() => {
    if (!live) return
    let alive = true
    cursor.current = Date.now() - 5000
    setRows([])
    const tick = async () => {
      try {
        const q = new URLSearchParams({ after: String(cursor.current), query: st.q })
        if (st.site) q.set('sites', st.site)
        const res = await api.get<{ rows: LogRow[]; cursor: number }>('/live?' + q)
        if (!alive) return
        cursor.current = res.cursor
        if (res.rows.length) setRows((r) => [...res.rows, ...r].slice(0, 1000))
        setError(undefined)
      } catch (e) { setError((e as Error).message) }
    }
    tick()
    const id = setInterval(tick, 2000)
    return () => { alive = false; clearInterval(id) }
  }, [live, st.q, st.site])

  const submit = (e?: React.FormEvent) => { e?.preventDefault(); set({ q: input.trim() }) }
  const addTerm = (term: string, replace = false) => {
    const q = replace ? term : (st.q ? st.q + ' ' + term : term)
    setInput(q)
    set({ q })
  }
  const exportUrl = (fmt: string) => {
    const r = toRange(st)
    const q = new URLSearchParams({ query: st.q, from: r.from.toISOString(), to: r.to.toISOString(), format: fmt })
    if (st.site) q.set('sites', st.site)
    return '/api/search/export?' + q
  }

  return (
    <>
      <PageHeader title={t('Logs')} subtitle={t('Search all syslog messages from all your UniFi sites')} icon={<ScrollText className="size-5" />}
        actions={<>
          <SiteFilter value={st.site} onChange={(site) => set({ site })} />
          {!live && <RangePicker value={st} onChange={(r) => set({ preset: r.preset, from: r.from ?? '', to: r.to ?? '' })} />}
          <Button variant={live ? 'primary' : 'secondary'} onClick={() => setLive(!live)} icon={live ? <Pause className="size-4" /> : <Radio className="size-4" />}>{live ? t('Pause live') : t('Live')}</Button>
        </>} />

      <form onSubmit={submit} className="mb-3 flex gap-2">
        <div className="relative flex-1">
          <Search className="pointer-events-none absolute left-3.5 top-1/2 size-4 -translate-y-1/2 text-muted" />
          <input value={input} onChange={(e) => setInput(e.target.value)} placeholder={t('Search, e.g. {a}   or   {b}   or   {c}', { a: 'category:firewall action:block dport:22', b: '"link down"', c: 'src:192.168.1.0/24' })}
            className="input !pl-10 !py-3 font-mono !text-[13px]" spellCheck={false} />
          {input && <button type="button" title={t('Clear')} aria-label={t('Clear')} onClick={() => { setInput(''); set({ q: '' }) }} className="absolute right-3 top-1/2 -translate-y-1/2 text-muted hover:text-[var(--text)] cursor-pointer"><X className="size-4" /></button>}
        </div>
        <Button type="submit" variant="primary" icon={<Search className="size-4" />}>{t('Search')}</Button>
        <Button type="button" title={t('Query language')} onClick={() => setHelp(true)} icon={<HelpCircle className="size-4" />} />
      </form>

      <div className="mb-5 flex flex-wrap gap-1.5">
        {primaryCategories.map((c) => (
          <button key={c} onClick={() => addTerm(`category:${c}`, true)}
            className={cx('flex items-center gap-1.5 rounded-full px-3 py-1 text-xs ring-1 transition cursor-pointer', st.q === `category:${c}` ? 'ring-indigo-400 bg-indigo-500/10' : 'ring-[var(--border)] hover:ring-indigo-400/50')}>
            <span className="size-2 rounded-full" style={{ background: catColor(c) }} />{catLabel(c)}
          </button>
        ))}
        <button onClick={() => addTerm('action:block', true)} className="rounded-full px-3 py-1 text-xs ring-1 ring-[var(--border)] hover:ring-indigo-400/50 cursor-pointer">{t('All blocked')}</button>
        <button onClick={() => addTerm('severity:<=err', true)} className="rounded-full px-3 py-1 text-xs ring-1 ring-[var(--border)] hover:ring-indigo-400/50 cursor-pointer">{t('Errors')}</button>
      </div>

      {error && <div className="mb-4 rounded-xl bg-red-500/10 px-4 py-3 text-sm text-red-500 ring-1 ring-red-500/30">{error}</div>}

      {!live && hist && (
        <Card className="mb-5" title={<span>{t('{n} results', { n: fmtNum(hist.total) })} <span className="font-normal text-muted">· {took} ms</span></span>}
          actions={<>
            <Button variant="ghost" onClick={() => setSaveSearch({ name: '', description: '', query: st.q, preset: st.preset, from: st.from, to: st.to, site: st.site, shared: false, pinned: true })} icon={<Bookmark className="size-4" />}>{t('Save search')}</Button>
            {can.categories(user) && <Button variant="ghost" onClick={() => setSave(true)} icon={<Tags className="size-4" />}>{t('Save as category')}</Button>}
            {can.export(user) && <>
              <a href={exportUrl('csv')}><Button variant="ghost" icon={<Download className="size-4" />}>CSV</Button></a>
              <a href={exportUrl('json')}><Button variant="ghost" icon={<Download className="size-4" />}>JSON</Button></a>
            </>}
          </>}>
          <Timeline buckets={hist.buckets} stepSec={hist.step} height={140} range={toRange(st)}
            onSelect={(f, t) => set({ preset: 'custom', from: localIso(f), to: localIso(t) })} />
        </Card>
      )}

      <div className="panel overflow-hidden rounded-2xl">
        {live && <div className="flex items-center gap-2 border-b border-app px-4 py-2 text-xs text-emerald-500"><span className="size-2 rounded-full bg-emerald-500 pulse-dot" /> {t('Live: new messages appear automatically ({n})', { n: rows.length })}</div>}
        {loading && !rows.length ? <div className="grid place-items-center py-20"><Spinner /></div>
          : !rows.length ? <Empty icon={<ScrollText className="size-8" />} title={live ? t('Waiting for new messages…') : t('No messages found')} text={live ? undefined : t('Adjust the search or time range.')} />
          : (
            <div className="overflow-x-auto scroll-thin">
              <table className="w-full text-sm">
                <thead className="text-left text-[11px] uppercase tracking-wide text-muted">
                  <tr className="border-b border-app">
                    <th className="px-4 py-2.5 font-medium">{t('Time')}</th>
                    <th className="px-2 py-2.5 font-medium">{t('Severity')}</th>
                    <th className="px-2 py-2.5 font-medium">{t('Device')}</th>
                    <th className="px-2 py-2.5 font-medium">{t('Category')}</th>
                    <th className="px-2 py-2.5 font-medium w-full">{t('Message')}</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((r, i) => (
                    <tr key={r.received + i} onClick={() => setSel(r)} className={cx('cursor-pointer border-b border-app/60 hover:bg-[var(--panel-2)]', sel === r && 'bg-indigo-500/10')}>
                      <td className="whitespace-nowrap px-4 py-2 font-mono text-xs text-muted">{fmtTime(r.ts)}</td>
                      <td className="px-2 py-2"><Badge className={severityClass(r.severity)}>{severities[r.severity]}</Badge></td>
                      <td className="max-w-[180px] truncate px-2 py-2 text-xs" title={`${r.hostname} (${r.source_ip})`}>
                        <div className="truncate font-medium">{r.fields?.device_name || r.hostname}</div>
                        {r.site !== 'default' && <div className="truncate text-[11px] text-muted">{r.site}</div>}
                      </td>
                      <td className="px-2 py-2"><Badge color={catColor(r.category)}>{catLabel(r.category)}</Badge></td>
                      <td className="w-full max-w-0 px-2 py-2 pr-4">
                        <div className="flex min-w-0 items-center gap-2">
                          {r.action && r.action !== 'log' && <Badge className={actionClass(r.action)}>{t(actionLabel[r.action] ?? r.action)}</Badge>}
                          <span className="min-w-0 truncate font-mono text-[12.5px]" title={r.message}>{r.message}</span>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {!live && more && <div className="p-4 text-center"><Button onClick={loadMore} loading={loading}>{t('Load more')}</Button></div>}
            </div>
          )}
      </div>

      <Detail row={sel} onClose={() => setSel(undefined)} onFilter={(t) => { addTerm(t); setSel(undefined) }} />
      <HelpModal open={help} onClose={() => setHelp(false)} onUse={(q) => { addTerm(q, true); setHelp(false) }} />
      <SaveCategory open={save} query={st.q} onClose={() => setSave(false)} />
      <SearchModal draft={saveSearch} onClose={() => setSaveSearch(undefined)} />
    </>
  )
}

const localIso = (d: Date) => new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16)
// Fills {name} placeholders in a translated string with JSX nodes.
function rich(s: string, parts: Record<string, ReactNode>) {
  return s.split(/\{(\w+)\}/g).map((p, i) => <Fragment key={i}>{i % 2 ? (parts[p] ?? `{${p}}`) : p}</Fragment>)
}

const quote = (v: string) => (/[\s"]/.test(v) ? `"${v.replace(/"/g, '')}"` : v)

function Detail({ row, onClose, onFilter }: { row?: LogRow; onClose: () => void; onFilter: (t: string) => void }) {
  if (!row) return null
  const f = row.fields ?? {}
  const country = (cc?: string) => (cc ? `${flag(cc)} ${countryName(cc)} (${cc})` : '')
  const asn = (n?: string, org?: string) => (n ? asLabel(`${n}|${org ?? ''}`) : '')
  // [label, search field, shown value, value to search for when different]
  const fields: [string, string, string | number, string?][] = [
    ['Time', '', fmtTime(row.ts)],
    ['Received', '', fmtTime(row.received)],
    ['Site', 'site', row.site],
    ['Source IP', 'source', row.source_ip],
    ['Device', 'host', row.hostname],
    ['Device name', 'device_name', f.device_name ?? ''],
    ['Model', 'model', row.device_model],
    ['Device MAC', 'mac', row.device_mac],
    ['Firmware', 'firmware', row.device_version],
    ['Application', 'app', row.app],
    ['Category', 'category', row.category],
    ['Event', 'event', row.event_type],
    ['Action', 'action', row.action],
    ['Rule', 'rule', row.rule],
    ['Source', 'src', row.src_ip],
    ['Source name', 'src_name', f.src_name ?? ''],
    ['Source country', 'src_country', country(f.src_country), f.src_country],
    ['Source network', 'src_asn', asn(f.src_asn, f.src_as_org), f.src_asn],
    ['Source port', 'sport', row.src_port || ''],
    ['Destination', 'dst', row.dst_ip],
    ['Destination name', 'dst_name', f.dst_name ?? ''],
    ['Destination country', 'dst_country', country(f.dst_country), f.dst_country],
    ['Destination network', 'dst_asn', asn(f.dst_asn, f.dst_as_org), f.dst_asn],
    ['Destination port', 'dport', row.dst_port || ''],
    ['Protocol', 'proto', row.proto],
    ['Interface in', 'in', row.in_if],
    ['Interface out', 'out', row.out_if],
    ['Client MAC', 'client', row.client_mac],
    ['Client name', 'client_name', row.client_name],
    ['Client IP', 'client_ip', row.client_ip],
    ['SSID', 'ssid', row.ssid],
    ['Severity', 'severity', `${row.severity} (${severities[row.severity]})`],
    ['Facility', 'facility', row.facility],
  ]
  return (
    <div className="fixed inset-0 z-40" onClick={onClose}>
      <div className="absolute inset-0 bg-black/30" />
      <aside onClick={(e) => e.stopPropagation()} className="panel absolute right-0 top-0 flex h-full w-full max-w-xl flex-col shadow-2xl fade-in">
        <header className="flex items-center justify-between border-b border-app px-5 py-4">
          <div className="flex items-center gap-2"><Badge color={catColor(row.category)}>{catLabel(row.category)}</Badge><span className="font-semibold">{t('Message details')}</span></div>
          <button onClick={onClose} title={t('Close')} aria-label={t('Close')} className="cursor-pointer rounded-lg p-1 hover:bg-[var(--panel-2)]"><X className="size-4" /></button>
        </header>
        <div className="flex-1 space-y-5 overflow-y-auto scroll-thin p-5">
          <div className="rounded-xl bg-[var(--panel-2)] p-4 font-mono text-[13px] leading-relaxed break-words">{row.message}</div>
          <table className="w-full text-sm">
            <tbody>
              {fields.filter(([, , v]) => v !== '' && v !== undefined).map(([label, key, v, raw]) => (
                <tr key={label} className="group border-b border-app/60">
                  <td className="w-36 py-1.5 pr-3 text-muted">{t(label)}</td>
                  <td className="py-1.5 font-mono text-[12.5px] break-all">{String(v)}</td>
                  <td className="w-14 py-1.5 text-right opacity-0 group-hover:opacity-100">
                    {key && <span className="inline-flex gap-1">
                      <button title={t('Filter on this value')} onClick={() => onFilter(`${key}:${quote(String(key === 'severity' ? row.severity : raw ?? v))}`)} className="cursor-pointer rounded p-0.5 hover:bg-indigo-500/20"><Plus className="size-3.5" /></button>
                      <button title={t('Exclude')} onClick={() => onFilter(`-${key}:${quote(String(key === 'severity' ? row.severity : raw ?? v))}`)} className="cursor-pointer rounded p-0.5 hover:bg-red-500/20"><Minus className="size-3.5" /></button>
                    </span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {Object.keys(row.fields ?? {}).length > 0 && (
            <div>
              <h4 className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted">{t('Extra fields')}</h4>
              <table className="w-full text-sm"><tbody>
                {Object.entries(row.fields).sort().map(([k, v]) => (
                  <tr key={k} className="group border-b border-app/60">
                    <td className="w-44 py-1.5 pr-3 font-mono text-[12px] text-muted break-all">{k}</td>
                    <td className="py-1.5 font-mono text-[12.5px] break-all">{v}</td>
                    <td className="w-8 py-1.5 text-right opacity-0 group-hover:opacity-100">
                      <button title={t('Filter on this value')} onClick={() => onFilter(`field.${k}:${quote(v)}`)} className="cursor-pointer rounded p-0.5 hover:bg-indigo-500/20"><Plus className="size-3.5" /></button>
                    </td>
                  </tr>
                ))}
              </tbody></table>
            </div>
          )}
          <div>
            <h4 className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted">{t('Original message')}</h4>
            <pre className="whitespace-pre-wrap break-all rounded-xl bg-[var(--panel-2)] p-4 font-mono text-[12px] text-muted">{row.raw}</pre>
          </div>
        </div>
      </aside>
    </div>
  )
}

const examples: [string, string][] = [
  ['category:firewall action:block', 'All packets blocked by the firewall'],
  ['category:firewall dport:22 -src:192.168.0.0/16', 'SSH traffic from outside'],
  ['category:ips', 'All IPS/IDS alerts'],
  ['category:wifi client:aa:bb:cc:dd:ee:ff', 'Wi-Fi events for a single client'],
  ['category:dhcp event:DHCPACK', 'Issued DHCP leases'],
  ['category:switch event:link_down', 'Ports that went down'],
  ['severity:<=err', 'All errors and worse'],
  ['host:U6* OR model:U7*', 'Messages from specific access points'],
  ['"authentication failed"', 'Free text (phrase)'],
  ['field.UNIFIwifiName:Guests', 'An extra field from UniFi CEF logs'],
  ['category:firewall action:block src_country:CN', 'Blocked traffic from one country (GeoIP)'],
  ['client_name:iphone', 'Everything about clients with a name (UniFi controller or DHCP)'],
  ['category:protect event:person_detected', 'People detected by UniFi Protect cameras'],
  ['category:access action:fail', 'Refused door access (UniFi Access)'],
]

function HelpModal({ open, onClose, onUse }: { open: boolean; onClose: () => void; onUse: (q: string) => void }) {
  return (
    <Modal open={open} onClose={onClose} title={t('Query language')} wide>
      <div className="grid gap-6 md:grid-cols-2">
        <div className="space-y-3 text-sm">
          <p>{rich(t('Combine terms with spaces (AND) or with {or}. A {minus} excludes, {star} is a wildcard.'), { or: <code className="rounded bg-[var(--panel-2)] px-1">OR</code>, minus: <code className="rounded bg-[var(--panel-2)] px-1">-</code>, star: <code className="rounded bg-[var(--panel-2)] px-1">*</code> })}</p>
          <p>{rich(t('Numbers: {a}, {b}. IP addresses accept CIDR: {c}. Severity also by name: {d}.'), { a: <code className="rounded bg-[var(--panel-2)] px-1">dport:&gt;1024</code>, b: <code className="rounded bg-[var(--panel-2)] px-1">port:20..25</code>, c: <code className="rounded bg-[var(--panel-2)] px-1">src:10.0.0.0/8</code>, d: <code className="rounded bg-[var(--panel-2)] px-1">severity:&lt;=warning</code> })}</p>
          <h4 className="pt-2 text-xs font-semibold uppercase tracking-wide text-muted">{t('Fields')}</h4>
          <div className="flex flex-wrap gap-1 font-mono text-xs">
            {['category', 'action', 'rule', 'event', 'app', 'host', 'site', 'source', 'model', 'mac', 'firmware', 'src', 'dst', 'ip', 'sport', 'dport', 'port', 'proto', 'in', 'out', 'client', 'client_name', 'client_ip', 'ssid', 'severity', 'facility', 'msg', 'raw', 'country', 'src_country', 'dst_country', 'asn', 'src_asn', 'as', 'src_name', 'dst_name', 'device_name', 'camera', 'door', 'user', 'field.<name>'].map((f) => <span key={f} className="rounded bg-[var(--panel-2)] px-1.5 py-0.5">{f}</span>)}
          </div>
        </div>
        <div className="space-y-1">
          {examples.map(([q, d]) => (
            <button key={q} onClick={() => onUse(q)} className="block w-full rounded-lg px-3 py-2 text-left hover:bg-[var(--panel-2)] cursor-pointer">
              <code className="text-[12.5px] text-indigo-400">{q}</code>
              <div className="text-xs text-muted">{t(d)}</div>
            </button>
          ))}
        </div>
      </div>
    </Modal>
  )
}

function SaveCategory({ open, query, onClose }: { open: boolean; query: string; onClose: () => void }) {
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const toast = useToast()
  const submit = async () => {
    setBusy(true)
    try {
      await api.post('/categories', { name, query, color: '#6366f1', icon: 'filter', pinned: true, description: '' })
      toast('ok', t('Category saved'))
      onClose()
      setName('')
    } catch (e) { toast('err', (e as Error).message) } finally { setBusy(false) }
  }
  return (
    <Modal open={open} onClose={onClose} title={t('Save as category')}
      footer={<><Button onClick={onClose}>{t('Cancel')}</Button><Button variant="primary" loading={busy} disabled={!name} onClick={submit}>{t('Save')}</Button></>}>
      <div className="space-y-4">
        <Field label={t('Name')}><Input autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder={t('e.g. SSH attacks')} /></Field>
        <Field label={t('Filter')}><code className="block rounded-xl bg-[var(--panel-2)] px-3 py-2 font-mono text-xs">{query || t('(all)')}</code></Field>
      </div>
    </Modal>
  )
}
