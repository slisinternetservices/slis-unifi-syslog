import { useEffect, useState } from 'react'
import { Mail, Save, Send } from 'lucide-react'
import { api } from '../api'
import { Button, Card, Field, Input, PageHeader, Select, Spinner, Toggle, useToast } from '../components/ui'
import { t } from '../i18n'

interface SMTP {
  enabled: boolean; host: string; port: number; security: string; auth: string; username: string; password: string
  from_address: string; from_name: string; helo: string; skip_verify: boolean; min_tls: string
  oauth_tenant: string; oauth_client_id: string; oauth_secret: string; oauth_scope: string; default_to: string
}

const presets: Record<string, Partial<SMTP>> = {
  'Microsoft 365 (OAuth2)': { host: 'smtp.office365.com', port: 587, security: 'starttls', auth: 'xoauth2' },
  'Microsoft 365 (password)': { host: 'smtp.office365.com', port: 587, security: 'starttls', auth: 'login' },
  'Google Workspace': { host: 'smtp.gmail.com', port: 465, security: 'tls', auth: 'plain' },
  'Custom server (465 TLS)': { port: 465, security: 'tls', auth: 'auto' },
  'Custom server (587 STARTTLS)': { port: 587, security: 'starttls', auth: 'auto' },
}

export default function MailPage() {
  const toast = useToast()
  const [s, setS] = useState<SMTP>()
  const [to, setTo] = useState('')
  const [busy, setBusy] = useState(false)
  const [testing, setTesting] = useState(false)
  useEffect(() => { api.get<SMTP>('/settings/smtp').then(setS) }, [])
  if (!s) return <div className="grid place-items-center py-24"><Spinner /></div>
  const up = (p: Partial<SMTP>) => setS({ ...s, ...p })
  const save = async () => {
    setBusy(true)
    try { setS(await api.put<SMTP>('/settings/smtp', s)); toast('ok', t('Mail settings saved')) } catch (e) { toast('err', (e as Error).message) } finally { setBusy(false) }
  }
  const test = async () => {
    setTesting(true)
    try { const r = await api.post<{ message: string }>('/settings/smtp/test', { to, settings: s }); toast('ok', r.message) } catch (e) { toast('err', (e as Error).message) } finally { setTesting(false) }
  }
  return (
    <>
      <PageHeader title={t('Mail server')} subtitle={t('Your own SMTP server for alerts and backup reports')} icon={<Mail className="size-5" />}
        actions={<Button variant="primary" loading={busy} onClick={save} icon={<Save className="size-4" />}>{t('Save')}</Button>} />
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-3">
        <div className="space-y-6 xl:col-span-2">
          <Card title={t('Server')}>
            <div className="space-y-5">
              <Toggle checked={s.enabled} onChange={(enabled) => up({ enabled })} label={t('Send email')} />
              <div className="flex flex-wrap gap-2">
                {Object.entries(presets).map(([k, v]) => <Button key={k} variant="ghost" className="!px-2.5 !py-1 text-xs ring-1 ring-[var(--border)]" onClick={() => up(v)}>{t(k)}</Button>)}
              </div>
              <div className="grid gap-4 md:grid-cols-3">
                <Field label={t('Host')} className="md:col-span-2"><Input value={s.host} onChange={(e) => up({ host: e.target.value.trim() })} placeholder="smtp.example.com" /></Field>
                <Field label={t('Port')}><Input type="number" value={s.port} onChange={(e) => up({ port: Number(e.target.value) })} /></Field>
                <Field label={t('Security')}><Select value={s.security} onChange={(security) => up({ security })} options={[
                  { value: 'starttls', label: t('STARTTLS (required)') }, { value: 'starttls-optional', label: t('STARTTLS (if available)') }, { value: 'tls', label: t('SSL/TLS (implicit, 465)') }, { value: 'none', label: t('None (not recommended)') }]} /></Field>
                <Field label={t('Minimum TLS version')}><Select value={s.min_tls} onChange={(min_tls) => up({ min_tls })} options={[{ value: '1.2', label: 'TLS 1.2' }, { value: '1.3', label: 'TLS 1.3' }]} /></Field>
                <Field label={t('Authentication')}><Select value={s.auth} onChange={(auth) => up({ auth })} options={[
                  { value: 'auto', label: t('Automatic (strongest)') }, { value: 'plain', label: 'PLAIN' }, { value: 'login', label: 'LOGIN' }, { value: 'cram-md5', label: 'CRAM-MD5' },
                  { value: 'scram-sha-256', label: 'SCRAM-SHA-256' }, { value: 'scram-sha-1', label: 'SCRAM-SHA-1' }, { value: 'xoauth2', label: 'OAuth2 (XOAUTH2)' }, { value: 'none', label: t('None') }]} /></Field>
                {s.auth !== 'none' && <>
                  <Field label={t('Username')}><Input value={s.username} autoComplete="off" onChange={(e) => up({ username: e.target.value })} /></Field>
                  {s.auth !== 'xoauth2' && <Field label={t('Password')}><Input type="password" autoComplete="new-password" value={s.password} onChange={(e) => up({ password: e.target.value })} /></Field>}
                </>}
                <Field label={t('HELO name (optional)')}><Input value={s.helo} onChange={(e) => up({ helo: e.target.value })} /></Field>
              </div>
              <Toggle checked={s.skip_verify} onChange={(skip_verify) => up({ skip_verify })} label={t('Skip certificate verification')} description={t('Only for internal servers with a self-signed certificate')} />
            </div>
          </Card>
          {s.auth === 'xoauth2' && (
            <Card title="OAuth2 (Microsoft 365)" subtitle={t('Client credentials flow. The app needs the SMTP.SendAsApp application role and an Exchange service principal.')}>
              <div className="grid gap-4 md:grid-cols-2">
                <Field label={t('Tenant ID')}><Input value={s.oauth_tenant} onChange={(e) => up({ oauth_tenant: e.target.value.trim() })} /></Field>
                <Field label={t('Client ID')}><Input value={s.oauth_client_id} onChange={(e) => up({ oauth_client_id: e.target.value.trim() })} /></Field>
                <Field label={t('Client secret')}><Input type="password" value={s.oauth_secret} onChange={(e) => up({ oauth_secret: e.target.value })} /></Field>
                <Field label={t('Scope')}><Input value={s.oauth_scope} placeholder="https://outlook.office365.com/.default" onChange={(e) => up({ oauth_scope: e.target.value })} /></Field>
              </div>
            </Card>
          )}
          <Card title={t('Sender')}>
            <div className="grid gap-4 md:grid-cols-3">
              <Field label={t('From address')}><Input value={s.from_address} onChange={(e) => up({ from_address: e.target.value.trim() })} placeholder="syslog@example.com" /></Field>
              <Field label={t('From name')}><Input value={s.from_name} onChange={(e) => up({ from_name: e.target.value })} /></Field>
              <Field label={t('Default recipient')}><Input value={s.default_to} onChange={(e) => up({ default_to: e.target.value })} placeholder="it@example.com" /></Field>
            </div>
          </Card>
        </div>
        <Card title={t('Test message')} subtitle={t('Tests with the current (unsaved) settings')}>
          <div className="space-y-3">
            <Field label={t('To')}><Input value={to} onChange={(e) => setTo(e.target.value)} placeholder="you@example.com" /></Field>
            <Button className="w-full" onClick={test} loading={testing} disabled={!to} icon={<Send className="size-4" />}>{t('Send test message')}</Button>
          </div>
        </Card>
      </div>
    </>
  )
}
