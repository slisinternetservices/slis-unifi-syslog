import { useState, type ReactNode } from 'react'
import {
  Megaphone, Plus, Pencil, Trash2, PlugZap, CheckCircle2, AlertCircle, PauseCircle, CircleDashed, MessagesSquare, Hash, Send,
  Smartphone, BellRing, Webhook, Moon, Layers, Clock,
} from 'lucide-react'
import { api } from '../api'
import { Badge, Button, Card, Empty, Field, Input, Modal, PageHeader, Select, Spinner, Toggle, cx, useAsync, useToast } from '../components/ui'
import { fmtAgo } from '../lib/format'
import { languages, t } from '../i18n'

export type Kind = 'teams' | 'slack' | 'telegram' | 'sms' | 'pushover' | 'webhook'
export type Severity = 'info' | 'warning' | 'critical'

interface Config {
  min_severity: Severity; events: string[]; language: string
  quiet_enabled: boolean; quiet_start: string; quiet_end: string; quiet_critical: boolean; batch_minutes: number
}
interface Settings {
  url?: string; mode?: string; token?: string; channel?: string; chat_id?: string; thread_id?: string; user?: string
  provider?: string; workspace?: string; channel_id?: string; originator?: string; recipients?: string; max_length?: number
  secret?: string; headers?: string
}
export interface Channel {
  id: number; name: string; kind: Kind; enabled: boolean; config: Config; settings: Settings
  last_ok: number; last_error: string; last_error_at: number; created_at: number; queued?: number
}

export const kinds = (): { kind: Kind; name: string; text: string; icon: ReactNode; color: string }[] => [
  { kind: 'teams', name: t('Microsoft Teams'), text: t('Adaptive card in a channel through a Workflows webhook'), icon: <MessagesSquare className="size-5" />, color: '#6264a7' },
  { kind: 'slack', name: t('Slack'), text: t('Incoming webhook or a bot in a channel'), icon: <Hash className="size-5" />, color: '#e01e5a' },
  { kind: 'telegram', name: t('Telegram'), text: t('Bot that posts to a chat, group or topic'), icon: <Send className="size-5" />, color: '#229ed9' },
  { kind: 'sms', name: t('SMS (MessageBird / Bird)'), text: t('Text messages to one or more phone numbers'), icon: <Smartphone className="size-5" />, color: '#10b981' },
  { kind: 'pushover', name: t('Pushover'), text: t('Push notifications to phones and desktops'), icon: <BellRing className="size-5" />, color: '#f59e0b' },
  { kind: 'webhook', name: t('Webhook'), text: t('POST JSON to any HTTPS endpoint, optionally signed'), icon: <Webhook className="size-5" />, color: '#6366f1' },
]
export const kindInfo = (k: Kind) => kinds().find((x) => x.kind === k)!

export const severityOptions = () => [
  { value: 'info', label: t('Info') },
  { value: 'warning', label: t('Warning') },
  { value: 'critical', label: t('Critical') },
]
export const severityColor: Record<Severity, string> = {
  info: 'bg-sky-500/10 text-sky-400 ring-sky-500/30',
  warning: 'bg-amber-500/10 text-amber-500 ring-amber-500/30',
  critical: 'bg-red-500/10 text-red-500 ring-red-500/30',
}

const events = () => [
  { value: 'devices', label: t('Devices silent or back online') },
  { value: 'probes', label: t('Remote probes offline or buffer filling up') },
  { value: 'firmware', label: t('Outdated firmware') },
  { value: 'backup', label: t('Backups') },
  { value: 'system', label: t('System status: disk space, ClickHouse, dropped messages') },
]

const defaults: Record<Kind, { config?: Partial<Config>; settings?: Settings }> = {
  teams: {},
  slack: { settings: { mode: 'webhook' } },
  telegram: {},
  sms: { config: { min_severity: 'critical', quiet_enabled: true, quiet_critical: true }, settings: { provider: 'messagebird', max_length: 306 } },
  pushover: {},
  webhook: {},
}

const newChannel = (kind: Kind): Channel => ({
  id: 0, name: kindInfo(kind).name, kind, enabled: true, last_ok: 0, last_error: '', last_error_at: 0, created_at: 0,
  config: { min_severity: 'info', events: ['devices', 'probes', 'firmware', 'backup', 'system'], language: '', quiet_enabled: false, quiet_start: '22:00', quiet_end: '07:00', quiet_critical: true, batch_minutes: 0, ...defaults[kind].config },
  settings: { ...defaults[kind].settings },
})

function StateIcon({ c }: { c: Channel }) {
  if (!c.enabled) return <PauseCircle className="size-4 shrink-0 text-muted" />
  if (c.last_error_at > c.last_ok) return <AlertCircle className="size-4 shrink-0 text-red-500" />
  if (c.last_ok) return <CheckCircle2 className="size-4 shrink-0 text-emerald-500" />
  return <CircleDashed className="size-4 shrink-0 text-muted" />
}

export default function Notifications() {
  const { data, loading, reload } = useAsync(() => api.get<Channel[]>('/notify'))
  const [edit, setEdit] = useState<Channel | null>(null)
  const [pick, setPick] = useState(false)
  const list = data ?? []

  return (
    <>
      <PageHeader title={t('Notification channels')} subtitle={t('Get alerts in Teams, Slack, Telegram, by SMS and more, next to e-mail')} icon={<Megaphone className="size-5" />}
        actions={<Button variant="primary" onClick={() => setPick(true)} icon={<Plus className="size-4" />}>{t('Add channel')}</Button>} />
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-3">
        <div className="space-y-4 xl:col-span-2">
          {loading && !data ? <Card><div className="grid place-items-center py-16"><Spinner /></div></Card>
            : !list.length ? (
              <Card><Empty icon={<Megaphone className="size-8" />} title={t('No channels yet')} text={t('Add Teams, Slack, Telegram or SMS to hear about problems where you already are.')}
                action={<Button variant="primary" onClick={() => setPick(true)} icon={<Plus className="size-4" />}>{t('Add channel')}</Button>} /></Card>
            ) : list.map((c) => <ChannelCard key={c.id} c={c} onEdit={() => setEdit(c)} />)}
        </div>
        <Card title={t('How it works')}>
          <ul className="space-y-3 text-sm text-muted">
            <li className="flex gap-3"><BellRing className="mt-0.5 size-4 shrink-0 text-indigo-400" /><span>{t('Alert rules choose their own channels and severity under Alerts, e.g. Teams for everything and SMS only for critical alerts.')}</span></li>
            <li className="flex gap-3"><Layers className="mt-0.5 size-4 shrink-0 text-indigo-400" /><span>{t('System events such as silent devices, offline probes, outdated firmware, backups and system status go to every channel that subscribes to them.')}</span></li>
            <li className="flex gap-3"><Moon className="mt-0.5 size-4 shrink-0 text-indigo-400" /><span>{t('During quiet hours notifications are held and sent as one summary afterwards; critical ones can still go out right away.')}</span></li>
            <li className="flex gap-3"><Clock className="mt-0.5 size-4 shrink-0 text-indigo-400" /><span>{t('Batching combines everything within a few minutes into one message, so an alert storm does not flood your phone.')}</span></li>
          </ul>
        </Card>
      </div>
      <Modal open={pick} onClose={() => setPick(false)} title={t('Add channel')} wide>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          {kinds().map((k) => (
            <button key={k.kind} onClick={() => { setPick(false); setEdit(newChannel(k.kind)) }}
              className="panel flex items-start gap-3 rounded-2xl p-4 text-left transition hover:border-indigo-400/60 cursor-pointer">
              <span className="grid size-10 shrink-0 place-items-center rounded-xl" style={{ background: k.color + '22', color: k.color }}>{k.icon}</span>
              <span><span className="block font-medium">{k.name}</span><span className="mt-0.5 block text-xs text-muted">{k.text}</span></span>
            </button>
          ))}
        </div>
      </Modal>
      {edit && <EditChannel c={edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reload() }} />}
    </>
  )
}

function ChannelCard({ c, onEdit }: { c: Channel; onEdit: () => void }) {
  const k = kindInfo(c.kind)
  const subs = events().filter((e) => c.config.events.includes(e.value)).map((e) => e.label)
  const sev = severityOptions().find((s) => s.value === (c.config.min_severity || 'info'))!.label
  return (
    <Card pad={false}>
      <div className="flex flex-wrap items-start gap-4 px-5 py-4">
        <span className="grid size-10 shrink-0 place-items-center rounded-xl" style={{ background: k.color + '22', color: k.color }}>{k.icon}</span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <StateIcon c={c} />
            <span className="truncate font-medium">{c.name}</span>
            <Badge>{k.name}</Badge>
            {!c.enabled && <Badge>{t('Off')}</Badge>}
            {c.config.min_severity !== 'info' && <Badge className={severityColor[c.config.min_severity]}>{t('{sev} and up', { sev })}</Badge>}
          </div>
          <div className="mt-1 text-xs text-muted">{subs.length ? subs.join(' · ') : t('Only alert rules that choose this channel')}</div>
          <div className="mt-1 flex flex-wrap gap-x-3 text-xs text-muted">
            {c.config.quiet_enabled && <span className="inline-flex items-center gap-1"><Moon className="size-3" />{t('Quiet {from}–{to}', { from: c.config.quiet_start, to: c.config.quiet_end })}</span>}
            {c.config.batch_minutes > 0 && <span className="inline-flex items-center gap-1"><Layers className="size-3" />{t('Batched per {n} min', { n: c.config.batch_minutes })}</span>}
            {(c.queued ?? 0) > 0 && <span className="inline-flex items-center gap-1 text-amber-500"><Clock className="size-3" />{t('{n} waiting', { n: c.queued! })}</span>}
            <span>{c.last_ok ? t('Last delivery {ago}', { ago: fmtAgo(c.last_ok * 1000) }) : t('Nothing sent yet')}</span>
          </div>
          {c.last_error_at > c.last_ok && c.last_error && <div className="mt-1 text-xs text-red-500">{c.last_error}</div>}
        </div>
        <Button variant="ghost" title={t('Edit')} onClick={onEdit} icon={<Pencil className="size-4" />} />
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

function EditChannel({ c, onClose, onSaved }: { c: Channel; onClose: () => void; onSaved: () => void }) {
  const toast = useToast()
  const [f, setF] = useState(c)
  const [busy, setBusy] = useState('')
  const s = f.settings
  const cfg = f.config
  const up = (p: Partial<Channel>) => setF({ ...f, ...p })
  const us = (p: Partial<Settings>) => setF({ ...f, settings: { ...f.settings, ...p } })
  const uc = (p: Partial<Config>) => setF({ ...f, config: { ...f.config, ...p } })
  const run = async (what: string, fn: () => Promise<void>) => {
    setBusy(what)
    try { await fn() } catch (e) { toast('err', (e as Error).message) } finally { setBusy('') }
  }
  const save = () => run('save', async () => {
    await (f.id ? api.put(`/notify/${f.id}`, f) : api.post('/notify', f))
    toast('ok', t('Saved'))
    onSaved()
  })
  const test = () => run('test', async () => {
    await api.post('/notify/test', f)
    toast('ok', t('Test message sent'))
  })
  const remove = () => run('remove', async () => {
    if (!confirm(t('Remove {name}? Notifications waiting for it are discarded.', { name: c.name }))) return
    await api.del(`/notify/${c.id}`)
    toast('ok', t('Removed'))
    onSaved()
  })
  const secret = (label: string, key: keyof Settings, hint?: ReactNode, placeholder?: string) => (
    <Field label={label} hint={hint}><Input type="password" autoComplete="new-password" className="font-mono" placeholder={placeholder} value={(s[key] as string) ?? ''} onChange={(e) => us({ [key]: e.target.value.trim() })} /></Field>
  )
  const text = (label: string, key: keyof Settings, placeholder?: string, hint?: ReactNode, mono = true) => (
    <Field label={label} hint={hint}><Input className={mono ? 'font-mono' : ''} value={(s[key] as string) ?? ''} placeholder={placeholder} onChange={(e) => us({ [key]: e.target.value })} /></Field>
  )
  const k = kindInfo(f.kind)

  return (
    <Modal open onClose={onClose} wide title={<span className="flex items-center gap-2"><span style={{ color: k.color }}>{k.icon}</span>{c.id ? c.name : t('New {kind} channel', { kind: k.name })}</span>}
      footer={<>
        {c.id > 0 && <Button variant="danger" onClick={remove} loading={busy === 'remove'} icon={<Trash2 className="size-4" />} className="mr-auto">{t('Remove')}</Button>}
        <Button onClick={test} loading={busy === 'test'} icon={<PlugZap className="size-4" />}>{t('Send test message')}</Button>
        <Button variant="primary" onClick={save} loading={busy === 'save'} disabled={!f.name}>{t('Save')}</Button>
      </>}>
      <div className="grid grid-cols-1 gap-8 lg:grid-cols-2">
        <Section title={t('Connection')}>
          <Field label={t('Name')}><Input value={f.name} onChange={(e) => up({ name: e.target.value })} /></Field>

          {f.kind === 'teams' && <>
            {secret(t('Webhook URL'), 'url', t('In Teams: channel ⋯ > Workflows > "Post to a channel when a webhook request is received", then copy the URL. Stored encrypted.'), 'https://…/workflows/…')}
          </>}

          {f.kind === 'slack' && <>
            <Field label={t('Connection type')}>
              <Select value={s.mode ?? 'webhook'} onChange={(v) => us({ mode: v })} options={[{ value: 'webhook', label: t('Incoming webhook') }, { value: 'bot', label: t('Bot token (chat:write)') }]} />
            </Field>
            {s.mode === 'bot' ? <>
              {secret(t('Bot token'), 'token', t('Starts with xoxb-. Invite the bot to the channel first.'), 'xoxb-…')}
              {text(t('Channel'), 'channel', '#noc', t('Channel name or ID'))}
            </> : secret(t('Webhook URL'), 'url', t('Create an app with Incoming Webhooks at api.slack.com/apps and copy the URL. Stored encrypted.'), 'https://hooks.slack.com/services/…')}
          </>}

          {f.kind === 'telegram' && <>
            {secret(t('Bot token'), 'token', t('Create a bot with @BotFather and add it to the group.'), '123456:ABC…')}
            <div className="grid grid-cols-2 gap-3">
              {text(t('Chat ID'), 'chat_id', '-1001234567890', t('Group IDs start with -100'))}
              {text(t('Topic ID (optional)'), 'thread_id', '', t('For a forum topic'))}
            </div>
          </>}

          {f.kind === 'sms' && <>
            <Field label={t('Provider')}>
              <Select value={s.provider ?? 'messagebird'} onChange={(v) => us({ provider: v })} options={[{ value: 'messagebird', label: t('MessageBird SMS API') }, { value: 'bird', label: t('Bird (channels API)') }]} />
            </Field>
            {secret(t('Access key'), 'token', t('Live access key; stored encrypted'))}
            {s.provider === 'bird' ? (
              <div className="grid grid-cols-2 gap-3">
                {text(t('Workspace ID'), 'workspace')}
                {text(t('SMS channel ID'), 'channel_id')}
              </div>
            ) : text(t('Sender'), 'originator', 'Syslog', t('Up to 11 letters or a phone number'), false)}
            {text(t('Phone numbers'), 'recipients', '+31612345678, +31687654321', t('International format, comma-separated'))}
            <Field label={t('Maximum length')} hint={t('Longer texts are cut; 160 characters is one SMS')}>
              <Input type="number" min={70} max={1600} value={s.max_length || 306} onChange={(e) => us({ max_length: Number(e.target.value) })} />
            </Field>
          </>}

          {f.kind === 'pushover' && <>
            {secret(t('Application token'), 'token', t('Create an application at pushover.net'))}
            {secret(t('User or group key'), 'user')}
          </>}

          {f.kind === 'webhook' && <>
            {text(t('URL'), 'url', 'https://example.com/hooks/syslog')}
            {secret(t('Signing secret (optional)'), 'secret', t('Signs the body with HMAC-SHA256 in the X-USS-Signature header'))}
            <Field label={t('Extra headers')} hint={t('One per line, e.g. Authorization: Bearer …; stored encrypted')}>
              <textarea rows={3} className="input" value={s.headers ?? ''} onChange={(e) => us({ headers: e.target.value })} />
            </Field>
          </>}
          <Toggle checked={f.enabled} onChange={(enabled) => up({ enabled })} label={t('Enabled')} description={t('When off, nothing is sent to this channel')} />
        </Section>

        <div className="space-y-8">
          <Section title={t('What to send')}>
            <Field label={t('Minimum severity')} hint={t('For example only critical alerts by SMS')}>
              <Select value={cfg.min_severity || 'info'} onChange={(v) => uc({ min_severity: v as Severity })} options={severityOptions().map((o) => ({ ...o, label: o.value === 'info' ? t('Everything') : t('{sev} and up', { sev: o.label }) }))} />
            </Field>
            <Field label={t('System events')} hint={t('Alert rules pick their channels themselves under Alerts')}>
              <Chips options={events()} value={cfg.events} onChange={(events) => uc({ events })} />
            </Field>
            <Field label={t('Language')}>
              <Select value={cfg.language} onChange={(language) => uc({ language })} options={[{ value: '', label: t('Server default') }, ...languages.map(([value, label]) => ({ value, label }))]} />
            </Field>
          </Section>
          <Section title={t('When to send')}>
            <Toggle checked={cfg.quiet_enabled} onChange={(quiet_enabled) => uc({ quiet_enabled })} label={t('Quiet hours')} description={t('Hold notifications and send one summary afterwards')} />
            {cfg.quiet_enabled && <>
              <div className="grid grid-cols-2 gap-3">
                <Field label={t('From')}><Input type="time" value={cfg.quiet_start} onChange={(e) => uc({ quiet_start: e.target.value })} /></Field>
                <Field label={t('Until')}><Input type="time" value={cfg.quiet_end} onChange={(e) => uc({ quiet_end: e.target.value })} /></Field>
              </div>
              <Toggle checked={cfg.quiet_critical} onChange={(quiet_critical) => uc({ quiet_critical })} label={t('Critical notifications still go out')} />
            </>}
            <Field label={t('Batching')} hint={t('Combine notifications into one message; critical ones are never delayed')}>
              <Select value={String(cfg.batch_minutes)} onChange={(v) => uc({ batch_minutes: Number(v) })}
                options={[{ value: '0', label: t('Send right away') }, ...[2, 5, 15, 30, 60].map((n) => ({ value: String(n), label: t('Collect for {n} minutes', { n }) }))]} />
            </Field>
          </Section>
        </div>
      </div>
    </Modal>
  )
}
