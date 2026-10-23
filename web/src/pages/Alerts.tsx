import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { BellRing, Plus, Pencil, Trash2, FlaskConical, CalendarClock, CheckCircle2, CircleSlash } from 'lucide-react'
import { api } from '../api'
import { Badge, Button, Card, Empty, Field, Input, Modal, PageHeader, Select, Spinner, Toggle, cx, useAsync, useToast } from '../components/ui'
import { fmtNum, fmtTime } from '../lib/format'
import { t } from '../i18n'
import { kindInfo, severityColor, severityOptions, type Channel, type Severity } from './Notifications'

type Condition = 'above' | 'below' | 'distinct' | 'spike'
interface Schedule { days: number[]; from: string; to: string }
interface Rule {
  id?: number; name: string; query: string; threshold: number; window_minutes: number; cooldown_minutes: number; recipients: string; enabled: boolean; last_fired?: number
  destinations: number[]; channels: number[]; severity: Severity; email: boolean
  condition: Condition; group_by: string; distinct_field: string; spike_percent: number; baseline_hours: number; schedule: Schedule
}
interface Destination { id: number; name: string; kind: string; enabled: boolean }
interface Hit { key: string; value: number; normal: number }
interface TestResult { hits: Hit[]; groups: number; top?: Hit }

const blank: Rule = {
  name: '', query: '', threshold: 10, window_minutes: 5, cooldown_minutes: 30, recipients: '', enabled: true, destinations: [], channels: [], severity: 'warning', email: true,
  condition: 'above', group_by: '', distinct_field: '', spike_percent: 200, baseline_hours: 24, schedule: { days: [], from: '', to: '' },
}

const presets: Partial<Rule>[] = [
  { name: 'Brute-force SSH', query: 'category:auth event:login_failed', threshold: 20, window_minutes: 5, group_by: 'src_ip' },
  { name: 'Critical IPS alert', query: 'category:ips severity:<=crit', threshold: 1, window_minutes: 5, severity: 'critical' },
  { name: 'Port scan (many ports from one source)', query: 'category:firewall action:block', condition: 'distinct', distinct_field: 'dst_port', group_by: 'src_ip', threshold: 25, window_minutes: 5 },
  { name: 'Spike in blocked traffic', query: 'category:firewall action:block', condition: 'spike', threshold: 100, spike_percent: 300, baseline_hours: 24, window_minutes: 15 },
  { name: 'Site went quiet', query: '', condition: 'below', group_by: 'site', threshold: 1, window_minutes: 15, baseline_hours: 24, severity: 'critical' },
  { name: 'Sign-in outside office hours', query: 'category:auth event:login_success', threshold: 1, window_minutes: 5, schedule: { days: [], from: '18:00', to: '08:00' } },
  { name: 'Switch port down', query: 'category:switch event:link_down', threshold: 1, window_minutes: 5 },
]

/** Fields a rule can group by or count, with their labels. */
const fieldLabels: Record<string, string> = {
  src_ip: 'Source IP', dst_ip: 'Destination IP', dst_port: 'Destination port', src_country: 'Source country', dst_country: 'Destination country',
  src_as: 'Source network', dst_as: 'Destination network', src_name: 'Source name', dst_name: 'Destination name', proto: 'Protocol', rule: 'Rule',
  action: 'Action', category: 'Category', event_type: 'Event', app: 'Application', severity: 'Severity', classification: 'Classification',
  site: 'Site', hostname: 'Device', device_name: 'Device name', device_model: 'Model', source_ip: 'Sending IP address', in_if: 'Interface in', out_if: 'Interface out',
  client_mac: 'Client MAC', client_name: 'Client name', client_ip: 'Client IP', ssid: 'SSID', user: 'User', camera: 'Camera', door: 'Door',
}
const fieldLabel = (f: string) => (fieldLabels[f] ? t(fieldLabels[f]) : f)

const conditionOptions = () => [
  { value: 'above', label: t('At least a number of events') },
  { value: 'below', label: t('Fewer events than expected') },
  { value: 'distinct', label: t('Many different values') },
  { value: 'spike', label: t('Spike compared with normal') },
]
// Monday first; values are weekdays with 0 = Sunday.
const weekdays = () => [[1, t('Mon')], [2, t('Tue')], [3, t('Wed')], [4, t('Thu')], [5, t('Fri')], [6, t('Sat')], [0, t('Sun')]] as const

export function describe(r: Rule) {
  const per = r.group_by ? ' ' + t('per {field}', { field: fieldLabel(r.group_by) }) : ''
  switch (r.condition) {
    case 'below': return t('fewer than {n} in {min} min', { n: r.threshold, min: r.window_minutes }) + per
    case 'distinct': return t('≥ {n} different {field} in {min} min', { n: r.threshold, field: fieldLabel(r.distinct_field), min: r.window_minutes }) + per
    case 'spike': return t('{pct}% above normal in {min} min (at least {n})', { pct: r.spike_percent, min: r.window_minutes, n: r.threshold }) + per
    default: return t('≥ {n} in {min} min', { n: r.threshold, min: r.window_minutes }) + per
  }
}

function scheduleText(s: Schedule) {
  if (!s.days.length && !(s.from && s.to)) return ''
  const days = s.days.length ? weekdays().filter(([d]) => s.days.includes(d)).map(([, l]) => l).join(', ') : t('every day')
  return s.from && s.to ? t('{days}, {from}–{to}', { days, from: s.from, to: s.to }) : days
}

export default function Alerts() {
  const toast = useToast()
  const rules = useAsync(() => api.get<Rule[]>('/alerts'))
  const dests = useAsync(() => api.get<Destination[]>('/forward'))
  const destName = (id: number) => dests.data?.find((d) => d.id === id)?.name
  const chans = useAsync(() => api.get<Channel[]>('/notify'))
  const chan = (id: number) => chans.data?.find((c) => c.id === id)
  const fields = useAsync(() => api.get<string[]>('/alerts/fields'))
  const [edit, setEdit] = useState<Rule>()
  const [test, setTest] = useState<TestResult & { rule: Rule }>()
  const [testing, setTesting] = useState(false)

  // "Create alert" on a saved search opens a new rule with its filter.
  useEffect(() => {
    const q = new URLSearchParams(window.location.search)
    if (q.has('new')) {
      setEdit({ ...blank, name: q.get('new') ?? '', query: q.get('query') ?? '' })
      window.history.replaceState(null, '', '/alerts')
    }
  }, [])

  const open = (r?: Rule) => { setTest(undefined); setEdit(r ? { ...blank, ...r, schedule: { ...blank.schedule, ...r.schedule } } : { ...blank }) }
  const save = async () => {
    try {
      if (edit!.id) await api.put('/alerts/' + edit!.id, edit)
      else await api.post('/alerts', edit)
      setEdit(undefined); rules.reload(); toast('ok', t('Alert saved'))
    } catch (e) { toast('err', (e as Error).message) }
  }
  const runTest = async () => {
    setTesting(true)
    try { setTest({ ...(await api.post<TestResult>('/alerts/test', edit)), rule: edit! }) } catch (e) { toast('err', (e as Error).message) } finally { setTesting(false) }
  }
  const num = (k: keyof Rule) => (e: React.ChangeEvent<HTMLInputElement>) => setEdit({ ...edit!, [k]: Number(e.target.value) })
  const fieldOptions = (none?: string) => [...(none ? [{ value: '', label: none }] : []),
    ...(fields.data ?? Object.keys(fieldLabels)).map((f) => ({ value: f, label: fieldLabel(f) })).sort((a, b) => a.label.localeCompare(b.label))]
  const thresholdLabel = { above: t('At least (events)'), below: t('Fewer than (events)'), distinct: t('At least (values)'), spike: t('At least (events)') }

  return (
    <>
      <PageHeader title={t('Alerts')} subtitle={t('Get a notification as soon as a filter exceeds a threshold, goes quiet or spikes')} icon={<BellRing className="size-5" />}
        actions={<Button variant="primary" icon={<Plus className="size-4" />} onClick={() => open()}>{t('New alert')}</Button>} />
      <Card pad={false}>
        {rules.loading && !rules.data ? <div className="grid place-items-center py-16"><Spinner /></div>
          : !rules.data?.length ? (
            <Empty icon={<BellRing className="size-8" />} title={t('No alerts yet')} text={t('Start with an example:')} action={
              <div className="flex flex-wrap justify-center gap-2">{presets.map((p) => <Button key={p.name} onClick={() => setEdit({ ...blank, ...p, name: t(p.name!) })}>{t(p.name!)}</Button>)}</div>
            } />
          ) : (
            <ul className="divide-y divide-[var(--border)]">
              {rules.data.map((r) => {
                const rule = { ...blank, ...r }
                const sched = scheduleText(rule.schedule)
                return (
                  <li key={r.id} className="flex items-center justify-between gap-4 px-5 py-4">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2 font-medium">{r.name}<Badge className={severityColor[r.severity ?? 'warning']}>{severityOptions().find((s) => s.value === (r.severity ?? 'warning'))!.label}</Badge>{!r.enabled && <Badge>{t('off')}</Badge>}</div>
                      <code className="mt-1 block truncate font-mono text-xs text-muted">{r.query || t('(all)')}</code>
                      <div className="mt-1 text-xs text-muted">{describe(rule)} · {t('to {to}', { to: r.email === false ? t('no e-mail') : r.recipients || t('default recipient') })}{r.last_fired ? ' · ' + t('last {time}', { time: fmtTime(r.last_fired * 1000) }) : ''}</div>
                      {sched && <div className="mt-1 flex items-center gap-1 text-xs text-muted"><CalendarClock className="size-3.5" />{sched}</div>}
                      {(r.channels ?? []).length > 0 && <div className="mt-1 flex flex-wrap gap-1">{r.channels.map((id) => { const c = chan(id); return c && <Badge key={id} className="ring-1" color={kindInfo(c.kind).color}>{c.name}</Badge> })}</div>}
                      {(r.destinations ?? []).length > 0 && <div className="mt-1 flex flex-wrap gap-1">{r.destinations.map((id) => destName(id) && <Badge key={id} className="bg-indigo-500/10 text-indigo-400 ring-indigo-500/30">{destName(id)}</Badge>)}</div>}
                    </div>
                    <div className="flex gap-1">
                      <button title={t('Edit')} onClick={() => open(r)} className="cursor-pointer rounded-lg p-2 hover:bg-[var(--panel-2)]"><Pencil className="size-4" /></button>
                      <button title={t('Delete')} onClick={async () => { if (confirm(t('Delete?'))) { await api.del('/alerts/' + r.id); rules.reload() } }} className="cursor-pointer rounded-lg p-2 text-red-500 hover:bg-red-500/10"><Trash2 className="size-4" /></button>
                    </div>
                  </li>
                )
              })}
            </ul>
          )}
      </Card>
      <p className="mt-3 text-xs text-muted">{t('Alerts are evaluated every minute and sent by e-mail, to the chosen notification channels and to the chosen forwarding destinations.')}</p>
      {!!rules.data?.length && (
        <div className="mt-4 flex flex-wrap items-center gap-2 text-xs text-muted">{t('Examples:')}
          {presets.map((p) => <button key={p.name} onClick={() => setEdit({ ...blank, ...p, name: t(p.name!) })} className="cursor-pointer rounded-full px-3 py-1 ring-1 ring-[var(--border)] hover:ring-indigo-400/50 hover:text-[var(--text)]">{t(p.name!)}</button>)}
        </div>
      )}

      <Modal open={!!edit} onClose={() => setEdit(undefined)} title={edit?.id ? t('Edit alert') : t('New alert')} wide
        footer={<>
          <Button onClick={runTest} loading={testing} disabled={!edit?.query && edit?.condition !== 'below'} icon={<FlaskConical className="size-4" />} className="mr-auto">{t('Test now')}</Button>
          <Button onClick={() => setEdit(undefined)}>{t('Cancel')}</Button><Button variant="primary" onClick={save} disabled={!edit?.name || (!edit?.query && edit?.condition !== 'below')}>{t('Save')}</Button>
        </>}>
        {edit && (
          <div className="grid gap-6 lg:grid-cols-2">
            <div className="space-y-4">
              <Field label={t('Name')}><Input value={edit.name} onChange={(e) => setEdit({ ...edit, name: e.target.value })} /></Field>
              <Field label={t('Filter')} hint={edit.condition === 'below' ? t('Empty = all messages, e.g. to notice a site that stopped sending logs') : undefined}>
                <Input className="font-mono" value={edit.query} onChange={(e) => setEdit({ ...edit, query: e.target.value })} />
              </Field>
              <Field label={t('Condition')}>
                <div className="grid grid-cols-2 gap-1.5">
                  {conditionOptions().map((o) => (
                    <button key={o.value} type="button" onClick={() => setEdit({ ...edit, condition: o.value as Condition, distinct_field: o.value === 'distinct' ? edit.distinct_field || 'dst_port' : '' })}
                      className={cx('rounded-xl px-3 py-2 text-left text-sm ring-1 transition cursor-pointer', edit.condition === o.value ? 'bg-indigo-500/15 text-indigo-400 ring-indigo-500/40' : 'ring-[var(--border)] text-muted hover:text-[var(--text)]')}>
                      {o.label}
                    </button>
                  ))}
                </div>
              </Field>
              {edit.condition === 'distinct' && (
                <Field label={t('Count different values of')} hint={t('For example many destination ports from one source IP is a port scan')}>
                  <Select value={edit.distinct_field} onChange={(distinct_field) => setEdit({ ...edit, distinct_field })} options={fieldOptions()} />
                </Field>
              )}
              <div className="grid grid-cols-3 gap-3">
                <Field label={thresholdLabel[edit.condition]}><Input type="number" min={1} value={edit.threshold} onChange={num('threshold')} /></Field>
                <Field label={t('Window (min)')}><Input type="number" min={1} value={edit.window_minutes} onChange={num('window_minutes')} /></Field>
                <Field label={t('Cooldown (min)')}><Input type="number" min={1} value={edit.cooldown_minutes} onChange={num('cooldown_minutes')} /></Field>
              </div>
              {edit.condition === 'spike' && (
                <div className="grid grid-cols-2 gap-3">
                  <Field label={t('Above normal (%)')} hint={t('200% = three times the normal number')}><Input type="number" min={1} value={edit.spike_percent} onChange={num('spike_percent')} /></Field>
                  <Field label={t('Normal = average of the last (hours)')}><Input type="number" min={1} max={168} value={edit.baseline_hours} onChange={num('baseline_hours')} /></Field>
                </div>
              )}
              <Field label={t('Evaluate')} hint={edit.group_by ? t('Each value is checked on its own; the alert lists every value that matches') : undefined}>
                <Select value={edit.group_by} onChange={(group_by) => setEdit({ ...edit, group_by })} options={fieldOptions(t('For the whole filter')).map((o) => (o.value ? { ...o, label: t('Per {field}', { field: o.label }) } : o))} />
              </Field>
              {edit.condition === 'below' && edit.group_by && (
                <Field label={t('Values seen in the last (hours)')} hint={t('Only values that sent events in this period can go quiet')}>
                  <Input type="number" min={1} max={168} value={edit.baseline_hours} onChange={num('baseline_hours')} />
                </Field>
              )}
              <Field label={t('Active')} hint={t('Server time. A start after the end spans midnight, e.g. 18:00 to 08:00')}>
                <div className="space-y-2">
                  <div className="flex flex-wrap gap-1.5">
                    {weekdays().map(([d, l]) => {
                      const on = edit.schedule.days.includes(d)
                      return <button key={d} type="button" onClick={() => setEdit({ ...edit, schedule: { ...edit.schedule, days: on ? edit.schedule.days.filter((x) => x !== d) : [...edit.schedule.days, d] } })}
                        className={cx('w-11 rounded-lg py-1 text-xs ring-1 transition cursor-pointer', on ? 'bg-indigo-500/15 text-indigo-400 ring-indigo-500/40' : 'ring-[var(--border)] text-muted hover:text-[var(--text)]')}>{l}</button>
                    })}
                  </div>
                  <div className="flex items-center gap-2 text-sm">
                    <Input type="time" className="!w-32" value={edit.schedule.from} onChange={(e) => setEdit({ ...edit, schedule: { ...edit.schedule, from: e.target.value } })} />
                    <span className="text-muted">{t('to')}</span>
                    <Input type="time" className="!w-32" value={edit.schedule.to} onChange={(e) => setEdit({ ...edit, schedule: { ...edit.schedule, to: e.target.value } })} />
                  </div>
                  <p className="text-xs text-muted">{scheduleText(edit.schedule) || t('Always (no days or times chosen)')}</p>
                </div>
              </Field>
            </div>

            <div className="space-y-4">
              {test && <TestPanel res={test} />}
              <Field label={t('Severity')} hint={t('Channels can skip lower severities, e.g. SMS only for critical alerts')}>
                <Select value={edit.severity ?? 'warning'} onChange={(v) => setEdit({ ...edit, severity: v as Severity })} options={severityOptions()} />
              </Field>
              <Toggle checked={edit.email ?? true} onChange={(email) => setEdit({ ...edit, email })} label={t('Send e-mail')} />
              {(edit.email ?? true) && <Field label={t('Recipients')} hint={t('Comma-separated. Empty = default recipient from the mail settings.')}><Input value={edit.recipients} onChange={(e) => setEdit({ ...edit, recipients: e.target.value })} placeholder="noc@example.com" /></Field>}
              <Field label={t('Notification channels')} hint={(chans.data ?? []).length ? t('Channels with a higher minimum severity skip this alert') : <Link to="/notifications" className="text-indigo-400 hover:underline">{t('Add Teams, Slack, Telegram or SMS under Notification channels')}</Link>}>
                <div className="flex flex-wrap gap-1.5">
                  {(chans.data ?? []).map((c) => {
                    const on = (edit.channels ?? []).includes(c.id)
                    return (
                      <button key={c.id} type="button" onClick={() => setEdit({ ...edit, channels: on ? edit.channels.filter((x) => x !== c.id) : [...(edit.channels ?? []), c.id] })}
                        className={cx('flex items-center gap-1.5 rounded-lg px-2.5 py-1 text-xs ring-1 transition cursor-pointer', on ? 'bg-indigo-500/15 text-indigo-400 ring-indigo-500/40' : 'ring-[var(--border)] text-muted hover:text-[var(--text)]')}>
                        <span className="[&_svg]:size-3.5" style={{ color: kindInfo(c.kind).color }}>{kindInfo(c.kind).icon}</span>{c.name}{!c.enabled && ' (' + t('off') + ')'}
                      </button>
                    )
                  })}
                </div>
              </Field>
              {(dests.data ?? []).length > 0 && (
                <Field label={t('Also send to')} hint={t('Forwarding destinations that receive the alert, e.g. Microsoft Sentinel')}>
                  <div className="flex flex-wrap gap-1.5">
                    {dests.data!.map((d) => {
                      const on = (edit.destinations ?? []).includes(d.id)
                      return (
                        <button key={d.id} type="button" onClick={() => setEdit({ ...edit, destinations: on ? edit.destinations.filter((x) => x !== d.id) : [...(edit.destinations ?? []), d.id] })}
                          className={'rounded-lg px-2.5 py-1 text-xs ring-1 transition cursor-pointer ' + (on ? 'bg-indigo-500/15 text-indigo-400 ring-indigo-500/40' : 'ring-[var(--border)] text-muted hover:text-[var(--text)]')}>
                          {d.name}{!d.enabled && ' (' + t('off') + ')'}
                        </button>
                      )
                    })}
                  </div>
                </Field>
              )}
              <Toggle checked={edit.enabled} onChange={(enabled) => setEdit({ ...edit, enabled })} label={t('Enabled')} />
            </div>
          </div>
        )}
      </Modal>
    </>
  )
}

function TestPanel({ res }: { res: TestResult & { rule: Rule } }) {
  const fired = res.hits.length > 0
  const r = res.rule
  return (
    <div className={cx('rounded-xl p-4 text-sm ring-1', fired ? 'bg-amber-500/10 ring-amber-500/30' : 'bg-emerald-500/10 ring-emerald-500/30')}>
      <div className={cx('flex items-center gap-2 font-medium', fired ? 'text-amber-500' : 'text-emerald-500')}>
        {fired ? <CheckCircle2 className="size-4" /> : <CircleSlash className="size-4" />}
        {fired ? t('Would fire now ({n} matches)', { n: res.hits.length }) : t('Would not fire now')}
      </div>
      <p className="mt-1 text-xs text-muted">{describe(r)}</p>
      {!fired && res.top && <p className="mt-1 text-xs text-muted">{t('Highest value: {n}', { n: fmtNum(res.top.value) })}{res.top.key && ` (${res.top.key})`}</p>}
      {!fired && !res.top && <p className="mt-1 text-xs text-muted">{t('No matching events in this window')}</p>}
      {fired && (
        <ul className="mt-2 max-h-48 space-y-0.5 overflow-auto scroll-thin font-mono text-xs">
          {res.hits.slice(0, 50).map((h) => (
            <li key={h.key} className="flex justify-between gap-3">
              <span className="truncate">{h.key || t('(all)')}</span>
              <span className="tabular-nums">{fmtNum(h.value)}{(r.condition === 'spike' || (r.condition === 'below' && r.group_by)) && <span className="text-muted"> / {t('normal')} {h.normal.toFixed(1)}</span>}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
