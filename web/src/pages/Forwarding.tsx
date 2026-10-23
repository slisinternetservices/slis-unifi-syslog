import { useState, type ReactNode } from 'react'
import {
  Send, Plus, Pencil, Trash2, PlugZap, CheckCircle2, AlertCircle, PauseCircle, CircleDashed, Download, Shield, Database,
  Workflow, Radio, Webhook, BarChart3, FileCode2, Upload,
} from 'lucide-react'
import { api } from '../api'
import { Badge, Button, Card, Empty, Field, Input, Modal, PageHeader, Select, Spinner, Stat, Toggle, cx, useAsync, useToast } from '../components/ui'
import { Sparkline } from '../components/charts'
import { catLabel, categoryMeta, fmtAgo, fmtBytes, fmtNum, severities, severityLabel } from '../lib/format'
import { t } from '../i18n'

type Kind = 'sentinel' | 'elasticsearch' | 'logstash' | 'syslog' | 'webhook' | 'splunk'

interface Filter { categories: string[]; sites: string[]; devices: string[]; max_severity: number; saved_search: number; query: string }
interface Settings {
  tenant_id?: string; client_id?: string; auth_method?: string; client_secret?: string; certificate?: string
  endpoint?: string; dcr_id?: string; stream?: string; cef_stream?: string; table_mode?: string
  url?: string; es_auth?: string; api_key?: string; username?: string; password?: string; data_stream?: string
  address?: string; transport?: string; framing?: string; format?: string
  token?: string; index?: string; sourcetype?: string; headers?: string
  verify_tls: boolean; ca_cert?: string; queue_max_mb?: number; price_per_gb?: number
}
interface Totals { events: number; bytes: number; errors: number; rejected: number; dropped: number }
interface Status { state: 'ok' | 'error' | 'idle' | 'disabled'; queue_events: number; queue_bytes: number; last_ok: number; last_error: string; last_error_at: number; retry_at: number }
interface Destination {
  id: number; name: string; kind: Kind; enabled: boolean; filter: Filter; settings: Settings; created_at: number
  status?: Status; total?: Totals; day?: Totals; hourly?: number[]; daily_bytes?: number
}
interface SavedSearch { id: number; name: string; query: string }
interface ContentFile { path: string; size: number }

const kinds = (): { kind: Kind; name: string; text: string; icon: ReactNode; color: string }[] => [
  { kind: 'sentinel', name: t('Microsoft Sentinel'), text: t('Azure Monitor Logs Ingestion API with a data collection rule'), icon: <Shield className="size-5" />, color: '#0ea5e9' },
  { kind: 'elasticsearch', name: t('Elasticsearch / OpenSearch'), text: t('Bulk API into a data stream, Elastic Common Schema'), icon: <Database className="size-5" />, color: '#f59e0b' },
  { kind: 'logstash', name: t('Logstash'), text: t('JSON lines over TCP/TLS or JSON over HTTP'), icon: <Workflow className="size-5" />, color: '#10b981' },
  { kind: 'syslog', name: t('Syslog'), text: t('RFC 5424 or CEF over UDP, TCP or TLS: Graylog, Wazuh, QRadar…'), icon: <Radio className="size-5" />, color: '#8b5cf6' },
  { kind: 'splunk', name: t('Splunk'), text: t('HTTP Event Collector (HEC)'), icon: <BarChart3 className="size-5" />, color: '#ec4899' },
  { kind: 'webhook', name: t('JSON webhook'), text: t('POST batches of events to any HTTPS endpoint'), icon: <Webhook className="size-5" />, color: '#6366f1' },
]
const kindInfo = (k: Kind) => kinds().find((x) => x.kind === k)!

const defaults: Record<Kind, Partial<Settings>> = {
  sentinel: { auth_method: 'secret', stream: 'Custom-UnifiSyslog', cef_stream: 'Custom-UnifiCommonSecurityLog', table_mode: 'custom', verify_tls: true },
  elasticsearch: { es_auth: 'api_key', data_stream: 'logs-unifi.syslog-default', verify_tls: true },
  logstash: { transport: 'tcp', verify_tls: true },
  syslog: { transport: 'tcp', framing: 'octet', format: 'rfc5424', verify_tls: true },
  splunk: { sourcetype: 'unifi:syslog', verify_tls: true },
  webhook: { format: 'ecs', verify_tls: true },
}

const newDest = (kind: Kind): Destination => ({
  id: 0, name: kindInfo(kind).name, kind, enabled: true, created_at: 0,
  filter: { categories: [], sites: [], devices: [], max_severity: 7, saved_search: 0, query: '' },
  settings: { verify_tls: true, ...defaults[kind] },
})

function StateIcon({ s }: { s?: Status }) {
  switch (s?.state) {
    case 'ok': return <CheckCircle2 className="size-4 shrink-0 text-emerald-500" />
    case 'error': return <AlertCircle className="size-4 shrink-0 text-red-500" />
    case 'disabled': return <PauseCircle className="size-4 shrink-0 text-muted" />
    default: return <CircleDashed className="size-4 shrink-0 text-muted" />
  }
}

function filterSummary(f: Filter, saved: SavedSearch[]) {
  const parts: string[] = []
  if (f.categories.length) parts.push(f.categories.map(catLabel).join(', '))
  if (f.sites.length) parts.push(t('sites: {list}', { list: f.sites.join(', ') }))
  if (f.devices.length) parts.push(t('devices: {list}', { list: f.devices.join(', ') }))
  if (f.max_severity < 7) parts.push(t('{sev} or worse', { sev: t(severityLabel[f.max_severity]) }))
  if (f.saved_search) parts.push(t('saved search: {name}', { name: saved.find((s) => s.id === f.saved_search)?.name ?? '?' }))
  if (f.query) parts.push(f.query)
  return parts.length ? parts.join(' · ') : t('All events')
}

export default function Forwarding() {
  const { data, loading, reload } = useAsync(() => api.get<Destination[]>('/forward'))
  const saved = useAsync(() => api.get<SavedSearch[]>('/categories'))
  const content = useAsync(() => api.get<ContentFile[]>('/forward/content'))
  const [edit, setEdit] = useState<Destination | null>(null)
  const [pick, setPick] = useState(false)
  const list = data ?? []
  const active = list.filter((d) => d.enabled)
  const sum = (f: (d: Destination) => number) => list.reduce((a, d) => a + f(d), 0)

  return (
    <>
      <PageHeader title={t('Forwarding')} subtitle={t('Send selected UniFi events to Microsoft Sentinel, Elastic and other log tools')} icon={<Send className="size-5" />}
        actions={<Button variant="primary" onClick={() => setPick(true)} icon={<Plus className="size-4" />}>{t('Add destination')}</Button>} />
      {list.length > 0 && (
        <div className="mb-6 grid grid-cols-2 gap-4 xl:grid-cols-4">
          <Stat label={t('Active destinations')} value={`${active.length} / ${list.length}`} icon={<Send className="size-4" />} />
          <Stat label={t('Forwarded (24 h)')} value={fmtNum(sum((d) => d.day?.events ?? 0))} sub={fmtBytes(sum((d) => d.day?.bytes ?? 0))} accent="#10b981" icon={<Upload className="size-4" />} />
          <Stat label={t('Estimated volume per day')} value={fmtBytes(sum((d) => d.daily_bytes ?? 0))} accent="#0ea5e9" icon={<BarChart3 className="size-4" />} />
          <Stat label={t('Waiting in queue')} value={fmtNum(sum((d) => d.status?.queue_events ?? 0))} sub={fmtBytes(sum((d) => d.status?.queue_bytes ?? 0))}
            accent={sum((d) => d.status?.queue_events ?? 0) > 10000 ? '#f59e0b' : '#6366f1'} icon={<Database className="size-4" />} />
        </div>
      )}
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-3">
        <div className="space-y-4 xl:col-span-2">
          {loading && !data ? <Card><div className="grid place-items-center py-16"><Spinner /></div></Card>
            : !list.length ? (
              <Card><Empty icon={<Send className="size-8" />} title={t('No destinations yet')} text={t('Forward firewall, IPS and other events to the security tools you already use.')}
                action={<Button variant="primary" onClick={() => setPick(true)} icon={<Plus className="size-4" />}>{t('Add destination')}</Button>} /></Card>
            ) : list.map((d) => <DestinationCard key={d.id} d={d} saved={saved.data ?? []} onEdit={() => setEdit(d)} />)}
        </div>
        <ContentCard files={content.data ?? []} />
      </div>
      <Modal open={pick} onClose={() => setPick(false)} title={t('Add destination')} wide>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          {kinds().map((k) => (
            <button key={k.kind} onClick={() => { setPick(false); setEdit(newDest(k.kind)) }}
              className="panel flex items-start gap-3 rounded-2xl p-4 text-left transition hover:border-indigo-400/60 cursor-pointer">
              <span className="grid size-10 shrink-0 place-items-center rounded-xl" style={{ background: k.color + '22', color: k.color }}>{k.icon}</span>
              <span><span className="block font-medium">{k.name}</span><span className="mt-0.5 block text-xs text-muted">{k.text}</span></span>
            </button>
          ))}
        </div>
      </Modal>
      {edit && <EditDestination d={edit} saved={saved.data ?? []} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reload() }} />}
    </>
  )
}

function DestinationCard({ d, saved, onEdit }: { d: Destination; saved: SavedSearch[]; onEdit: () => void }) {
  const k = kindInfo(d.kind)
  const s = d.status
  const price = d.settings.price_per_gb ?? 0
  const perDay = (d.daily_bytes ?? 0) / 1024 ** 3
  return (
    <Card pad={false}>
      <div className="flex flex-wrap items-start gap-4 px-5 py-4">
        <span className="grid size-10 shrink-0 place-items-center rounded-xl" style={{ background: k.color + '22', color: k.color }}>{k.icon}</span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <StateIcon s={s} />
            <span className="truncate font-medium">{d.name}</span>
            <Badge>{k.name}</Badge>
            {!d.enabled && <Badge>{t('Off')}</Badge>}
          </div>
          <div className="mt-1 truncate text-xs text-muted" title={filterSummary(d.filter, saved)}>{filterSummary(d.filter, saved)}</div>
          {s?.state === 'error' && s.last_error
            ? <div className="mt-1 text-xs text-red-500">{s.last_error}{s.retry_at > Date.now() / 1000 ? ' · ' + t('retrying') : ''}</div>
            : s?.last_error && s.last_error_at > 0 && <div className="mt-1 text-xs text-amber-500">{t('Last problem {ago}: {msg}', { ago: fmtAgo(s.last_error_at * 1000), msg: s.last_error })}</div>}
        </div>
        <Button variant="ghost" title={t('Edit')} onClick={onEdit} icon={<Pencil className="size-4" />} />
      </div>
      <div className="grid grid-cols-2 gap-4 border-t border-app px-5 py-4 text-sm sm:grid-cols-5">
        <Metric label={t('Last 24 h')} value={fmtNum(d.day?.events)} sub={fmtBytes(d.day?.bytes ?? 0)} />
        <Metric label={t('Per day (est.)')} value={fmtBytes(d.daily_bytes ?? 0)} sub={price > 0 ? t('≈ {cost} per month', { cost: (perDay * price * 30).toFixed(2) }) : undefined} />
        <Metric label={t('Queue')} value={fmtNum(s?.queue_events)} sub={fmtBytes(s?.queue_bytes ?? 0)} warn={(s?.queue_events ?? 0) > 10000} />
        <Metric label={t('Errors (24 h)')} value={fmtNum(d.day?.errors)} sub={(d.day?.rejected || d.day?.dropped) ? t('{r} rejected · {d} dropped', { r: fmtNum(d.day?.rejected), d: fmtNum(d.day?.dropped) }) : undefined} warn={(d.day?.errors ?? 0) > 0} />
        <Metric label={t('Last delivery')} value={s?.last_ok ? fmtAgo(s.last_ok * 1000) : '–'} sub={t('{n} in total', { n: fmtNum(d.total?.events) })} />
      </div>
      <div className="px-3 pb-2"><Sparkline values={d.hourly ?? []} color={k.color} /></div>
    </Card>
  )
}

function Metric({ label, value, sub, warn }: { label: string; value: ReactNode; sub?: ReactNode; warn?: boolean }) {
  return (
    <div className="min-w-0">
      <div className="text-[10px] font-semibold uppercase tracking-widest text-muted">{label}</div>
      <div className={cx('mt-0.5 truncate font-semibold', warn && 'text-amber-500')}>{value}</div>
      {sub && <div className="truncate text-xs text-muted">{sub}</div>}
    </div>
  )
}

function ContentCard({ files }: { files: ContentFile[] }) {
  const groups: { dir: string; title: string; text: string }[] = [
    { dir: 'sentinel', title: t('Microsoft Sentinel'), text: t('DCR template, KQL queries, workbook and analytics rules') },
    { dir: 'elastic', title: t('Elastic / Kibana'), text: t('Index template, Kibana dashboards and detection rules') },
    { dir: 'logstash', title: t('Logstash'), text: t('Sample pipeline') },
  ]
  return (
    <Card title={t('Ready-made content')} subtitle={t('Import these into your log tool')}>
      <div className="space-y-5">
        {groups.map((g) => (
          <div key={g.dir}>
            <div className="text-sm font-medium">{g.title}</div>
            <div className="mb-2 text-xs text-muted">{g.text}</div>
            <ul className="space-y-1">
              {files.filter((f) => f.path.startsWith(g.dir + '/')).map((f) => (
                <li key={f.path}>
                  <a href={`/api/forward/content/${f.path}`} className="flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm hover:bg-[var(--panel-2)]">
                    <FileCode2 className="size-4 shrink-0 text-muted" />
                    <span className="min-w-0 flex-1 truncate font-mono text-xs">{f.path.slice(g.dir.length + 1)}</span>
                    <span className="text-xs text-muted">{fmtBytes(f.size)}</span>
                    <Download className="size-3.5 text-muted" />
                  </a>
                </li>
              ))}
            </ul>
          </div>
        ))}
        <p className="text-xs text-muted">{t('The README in each folder explains the setup step by step.')}</p>
      </div>
    </Card>
  )
}

function Chips({ options, value, onChange }: { options: { value: string; label: string }[]; value: string[]; onChange: (v: string[]) => void }) {
  return (
    <div className="flex flex-wrap gap-1.5">
      {options.map((o) => {
        const on = value.includes(o.value)
        return (
          <button key={o.value} type="button" onClick={() => onChange(on ? value.filter((v) => v !== o.value) : [...value, o.value])}
            className={cx('rounded-lg px-2.5 py-1 text-xs ring-1 transition cursor-pointer', on ? 'bg-indigo-500/15 text-indigo-400 ring-indigo-500/40' : 'ring-[var(--border)] text-muted hover:text-[var(--text)]')}>
            {o.label}
          </button>
        )
      })}
    </div>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="space-y-4">
      <h4 className="border-b border-app pb-2 text-xs font-semibold uppercase tracking-widest text-muted">{title}</h4>
      {children}
    </div>
  )
}

function EditDestination({ d, saved, onClose, onSaved }: { d: Destination; saved: SavedSearch[]; onClose: () => void; onSaved: () => void }) {
  const toast = useToast()
  const [f, setF] = useState(d)
  const [busy, setBusy] = useState('')
  const sites = useAsync(() => api.get<string[]>('/sites'))
  const s = f.settings
  const up = (p: Partial<Destination>) => setF({ ...f, ...p })
  const us = (p: Partial<Settings>) => setF({ ...f, settings: { ...f.settings, ...p } })
  const uf = (p: Partial<Filter>) => setF({ ...f, filter: { ...f.filter, ...p } })
  const body = () => ({ ...f, filter: { ...f.filter, devices: f.filter.devices.map((x) => x.trim()).filter(Boolean) } })
  const run = async (what: string, fn: () => Promise<void>) => {
    setBusy(what)
    try { await fn() } catch (e) { toast('err', (e as Error).message) } finally { setBusy('') }
  }
  const save = () => run('save', async () => {
    await (f.id ? api.put(`/forward/${f.id}`, body()) : api.post('/forward', body()))
    toast('ok', t('Saved'))
    onSaved()
  })
  const test = () => run('test', async () => {
    await api.post('/forward/test', body())
    toast('ok', t('Test event delivered'))
  })
  const template = () => run('template', async () => {
    const r = await api.post<{ template: string }>('/forward/template', body())
    toast('ok', t('Index template {name} installed', { name: r.template }))
  })
  const remove = () => run('remove', async () => {
    if (!confirm(t('Remove {name}? Events still in its queue are discarded.', { name: d.name }))) return
    await api.del(`/forward/${d.id}`)
    toast('ok', t('Removed'))
    onSaved()
  })
  const secret = (label: string, key: keyof Settings, hint?: ReactNode) => (
    <Field label={label} hint={hint}><Input type="password" autoComplete="new-password" value={(s[key] as string) ?? ''} onChange={(e) => us({ [key]: e.target.value.trim() })} /></Field>
  )
  const text = (label: string, key: keyof Settings, placeholder?: string, hint?: ReactNode, mono = true) => (
    <Field label={label} hint={hint}><Input className={mono ? 'font-mono' : ''} value={(s[key] as string) ?? ''} placeholder={placeholder} onChange={(e) => us({ [key]: e.target.value.trim() })} /></Field>
  )
  const k = kindInfo(f.kind)
  const usesTLS = f.kind !== 'syslog' && f.kind !== 'logstash' || s.transport === 'tls' || s.transport === 'http'

  return (
    <Modal open onClose={onClose} wide title={<span className="flex items-center gap-2"><span style={{ color: k.color }}>{k.icon}</span>{d.id ? d.name : t('New {kind} destination', { kind: k.name })}</span>}
      footer={<>
        {d.id > 0 && <Button variant="danger" onClick={remove} loading={busy === 'remove'} icon={<Trash2 className="size-4" />} className="mr-auto">{t('Remove')}</Button>}
        {f.kind === 'elasticsearch' && <Button onClick={template} loading={busy === 'template'} icon={<FileCode2 className="size-4" />}>{t('Install index template')}</Button>}
        <Button onClick={test} loading={busy === 'test'} icon={<PlugZap className="size-4" />}>{t('Send test event')}</Button>
        <Button variant="primary" onClick={save} loading={busy === 'save'} disabled={!f.name}>{t('Save')}</Button>
      </>}>
      <div className="grid grid-cols-1 gap-8 lg:grid-cols-2">
        <Section title={t('Connection')}>
          <Field label={t('Name')}><Input value={f.name} onChange={(e) => up({ name: e.target.value })} /></Field>

          {f.kind === 'sentinel' && <>
            <div className="grid grid-cols-2 gap-3">
              {text(t('Tenant ID'), 'tenant_id', '00000000-0000-…', t('Same tenant as Entra SSO'))}
              {text(t('Client ID'), 'client_id', '00000000-0000-…', t('Application (client) ID of the app registration'))}
            </div>
            <Field label={t('Sign-in method')}>
              <Select value={s.auth_method ?? 'secret'} onChange={(v) => us({ auth_method: v })} options={[{ value: 'secret', label: t('Client secret') }, { value: 'certificate', label: t('Certificate') }]} />
            </Field>
            {s.auth_method === 'certificate'
              ? <Field label={t('Certificate and private key')} hint={t('PEM with the certificate and its unencrypted RSA private key; upload only the certificate to the app registration')}>
                  <textarea rows={4} className="input" value={s.certificate ?? ''} placeholder="-----BEGIN CERTIFICATE-----" onChange={(e) => us({ certificate: e.target.value })} />
                </Field>
              : secret(t('Client secret'), 'client_secret')}
            {text(t('Logs ingestion endpoint'), 'endpoint', 'https://unifi-syslog-xxxx.westeurope-1.ingest.monitor.azure.com', t('Output logsIngestionEndpoint of the DCR template'))}
            {text(t('DCR immutable ID'), 'dcr_id', 'dcr-0123456789abcdef', t('Output dcrImmutableId of the DCR template'))}
            <Field label={t('Table')}>
              <Select value={s.table_mode ?? 'custom'} onChange={(v) => us({ table_mode: v })} options={[
                { value: 'custom', label: t('Everything to UnifiSyslog_CL (all UniFi fields)') },
                { value: 'split', label: t('Firewall and IPS to CommonSecurityLog, the rest to UnifiSyslog_CL') },
              ]} />
            </Field>
            <div className="grid grid-cols-2 gap-3">
              {text(t('Stream'), 'stream', 'Custom-UnifiSyslog')}
              {s.table_mode === 'split' && text(t('CommonSecurityLog stream'), 'cef_stream', 'Custom-UnifiCommonSecurityLog')}
            </div>
          </>}

          {f.kind === 'elasticsearch' && <>
            {text(t('URL'), 'url', 'https://elastic.example.com:9200', t('Elasticsearch, Elastic Cloud or OpenSearch endpoint'))}
            <Field label={t('Authentication')}>
              <Select value={s.es_auth ?? 'api_key'} onChange={(v) => us({ es_auth: v })} options={[{ value: 'api_key', label: t('API key') }, { value: 'basic', label: t('Username and password') }, { value: 'none', label: t('None') }]} />
            </Field>
            {s.es_auth === 'api_key' && secret(t('API key'), 'api_key', t('The encoded key (base64 of id:api_key)'))}
            {s.es_auth === 'basic' && <div className="grid grid-cols-2 gap-3">{text(t('Username'), 'username')}{secret(t('Password'), 'password')}</div>}
            {text(t('Data stream'), 'data_stream', 'logs-unifi.syslog-default', t('Install the index template once, so fields get the right types'))}
          </>}

          {f.kind === 'logstash' && <>
            <Field label={t('Transport')}>
              <Select value={s.transport ?? 'tcp'} onChange={(v) => us({ transport: v })} options={[{ value: 'tcp', label: 'TCP' }, { value: 'tls', label: 'TCP + TLS' }, { value: 'http', label: 'HTTP(S)' }]} />
            </Field>
            {s.transport === 'http'
              ? <>{text(t('URL'), 'url', 'https://logstash.example.com:8085')}<div className="grid grid-cols-2 gap-3">{text(t('Username'), 'username')}{secret(t('Password'), 'password')}</div></>
              : text(t('Address'), 'address', 'logstash.example.com:5514', t('tcp input with codec json_lines'))}
          </>}

          {f.kind === 'syslog' && <>
            {text(t('Address'), 'address', 'graylog.example.com:6514')}
            <div className="grid grid-cols-3 gap-3">
              <Field label={t('Transport')}>
                <Select value={s.transport ?? 'tcp'} onChange={(v) => us({ transport: v })} options={[{ value: 'udp', label: 'UDP' }, { value: 'tcp', label: 'TCP' }, { value: 'tls', label: 'TLS' }]} />
              </Field>
              <Field label={t('Format')}>
                <Select value={s.format ?? 'rfc5424'} onChange={(v) => us({ format: v })} options={[{ value: 'rfc5424', label: 'RFC 5424' }, { value: 'cef', label: 'CEF' }]} />
              </Field>
              {s.transport !== 'udp' && <Field label={t('Framing')}>
                <Select value={s.framing ?? 'octet'} onChange={(v) => us({ framing: v })} options={[{ value: 'octet', label: t('Octet counting') }, { value: 'newline', label: t('Newline') }]} />
              </Field>}
            </div>
          </>}

          {f.kind === 'splunk' && <>
            {text(t('HEC URL'), 'url', 'https://splunk.example.com:8088')}
            {secret(t('HEC token'), 'token')}
            <div className="grid grid-cols-2 gap-3">{text(t('Index'), 'index', 'main')}{text(t('Source type'), 'sourcetype', 'unifi:syslog')}</div>
          </>}

          {f.kind === 'webhook' && <>
            {text(t('URL'), 'url', 'https://example.com/hooks/unifi')}
            <Field label={t('Format')}>
              <Select value={s.format ?? 'ecs'} onChange={(v) => us({ format: v })} options={[{ value: 'ecs', label: t('Elastic Common Schema') }, { value: 'native', label: t('Native fields') }]} />
            </Field>
            <Field label={t('Extra headers')} hint={t('One per line, e.g. Authorization: Bearer …; stored encrypted')}>
              <textarea rows={3} className="input" value={s.headers ?? ''} onChange={(e) => us({ headers: e.target.value })} />
            </Field>
          </>}

          {usesTLS && <>
            <Toggle checked={s.verify_tls} onChange={(verify_tls) => us({ verify_tls })} label={t('Verify certificate')} description={t('Turn off only for a test setup with a self-signed certificate')} />
            <Field label={t('Extra CA certificate (optional)')} hint={t('PEM, for an internal certificate authority')}>
              <textarea rows={2} className="input" value={s.ca_cert ?? ''} placeholder="-----BEGIN CERTIFICATE-----" onChange={(e) => us({ ca_cert: e.target.value })} />
            </Field>
          </>}
          <Toggle checked={f.enabled} onChange={(enabled) => up({ enabled })} label={t('Enabled')} description={t('When off, nothing is queued or sent')} />
        </Section>

        <div className="space-y-8">
          <Section title={t('What to send')}>
            <Field label={t('Categories')} hint={t('None selected = all categories')}>
              <Chips value={f.filter.categories} onChange={(categories) => uf({ categories })} options={Object.keys(categoryMeta).map((c) => ({ value: c, label: catLabel(c) }))} />
            </Field>
            {(sites.data ?? []).length > 0 && (
              <Field label={t('Sites')} hint={t('None selected = all sites')}>
                <Chips value={f.filter.sites} onChange={(sites) => uf({ sites })} options={(sites.data ?? []).map((x) => ({ value: x, label: x }))} />
              </Field>
            )}
            <Field label={t('Devices')} hint={t('Hostnames, MAC addresses or device names, comma-separated; empty = all devices')}>
              <Input value={f.filter.devices.join(',')} placeholder="UDM-Pro, f4:e2:c6:00:00:01" onChange={(e) => uf({ devices: e.target.value.split(',') })} />
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label={t('Minimum severity')}>
                <Select value={String(f.filter.max_severity)} onChange={(v) => uf({ max_severity: Number(v) })}
                  options={severities.map((_, i) => ({ value: String(i), label: i === 7 ? t('Everything') : t('{sev} or worse', { sev: t(severityLabel[i]) }) }))} />
              </Field>
              <Field label={t('Saved search')}>
                <Select value={String(f.filter.saved_search)} onChange={(v) => uf({ saved_search: Number(v) })}
                  options={[{ value: '0', label: t('None') }, ...saved.map((x) => ({ value: String(x.id), label: x.name }))]} />
              </Field>
            </div>
            <Field label={t('Extra filter')} hint={t('Search language, e.g. -dst:10.0.0.0/8 or country:RU')}>
              <Input className="font-mono" value={f.filter.query} onChange={(e) => uf({ query: e.target.value })} />
            </Field>
            <p className="text-xs text-muted">{t('All parts must match. Only matching events are sent, so you only pay for relevant data in Sentinel or Elastic.')}</p>
          </Section>
          <Section title={t('Queue and cost')}>
            <div className="grid grid-cols-2 gap-3">
              <Field label={t('Max queue size (MB)')} hint={t('When the destination is unreachable events wait on disk; beyond this the oldest are dropped')}>
                <Input type="number" min={16} value={s.queue_max_mb || 1024} onChange={(e) => us({ queue_max_mb: Number(e.target.value) })} />
              </Field>
              <Field label={t('Price per GB (optional)')} hint={t('For the estimated monthly cost')}>
                <Input type="number" min={0} step="0.01" value={s.price_per_gb || ''} placeholder="2.30" onChange={(e) => us({ price_per_gb: Number(e.target.value) })} />
              </Field>
            </div>
          </Section>
        </div>
      </div>
    </Modal>
  )
}
