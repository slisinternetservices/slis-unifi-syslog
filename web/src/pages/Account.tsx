import { useState } from 'react'
import { UserRound, KeyRound, ShieldCheck, ShieldOff, Plus, Trash2, Copy, Code2, RefreshCw } from 'lucide-react'
import { api, can } from '../api'
import { useAuth } from '../App'
import { RecoveryCodes, RoleBadge, sitesSummary } from '../components/Access'
import { Badge, Button, Card, Empty, Field, Input, Modal, PageHeader, Select, Spinner, Toggle, useAsync, useToast } from '../components/ui'
import { fmtAgo, fmtTime } from '../lib/format'
import { t } from '../i18n'

interface TOTPState { enabled: boolean; recovery_left: number; required: boolean; available: boolean }
interface Enrollment { secret: string; uri: string; qr: string }
export interface APIToken { id: number; user_id: number; username: string; name: string; prefix: string; read_only: boolean; expires_at: number; last_used: number; last_ip: string; created_at: number }

export default function Account() {
  const { user } = useAuth()
  return (
    <>
      <PageHeader title={t('My account')} subtitle={t('Password, two-factor authentication and API tokens')} icon={<UserRound className="size-5" />} />
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-2">
        <Card title={t('Access rights')}>
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-3 text-sm">
            <dt className="text-muted">{t('User')}</dt><dd>{user.name || user.username} <span className="text-muted">({user.username})</span></dd>
            <dt className="text-muted">{t('Role')}</dt><dd><RoleBadge role={user.role} /></dd>
            <dt className="text-muted">{t('Sites')}</dt><dd>{can.admin(user) ? t('All sites') : sitesSummary(user.sites)}</dd>
            <dt className="text-muted">{t('Source')}</dt><dd>{user.provider === 'entra' ? 'Microsoft Entra' : t('Local')}{user.role_managed && <span className="text-muted"> · {t('role from Entra groups')}</span>}</dd>
          </dl>
        </Card>
        {user.provider === 'local' ? <Password /> : (
          <Card title={t('Sign-in')}><p className="text-sm text-muted">{t('Your password and multi-factor authentication are managed in Microsoft Entra.')}</p></Card>
        )}
        {user.provider === 'local' && <TwoFactor />}
        <Tokens />
      </div>
    </>
  )
}

function Password() {
  const toast = useToast()
  const [pw, setPw] = useState({ current: '', new: '' })
  const changePw = async () => {
    try { await api.post('/me/password', pw); setPw({ current: '', new: '' }); toast('ok', t('Password changed')) } catch (e) { toast('err', (e as Error).message) }
  }
  return (
    <Card title={t('My password')}>
      <div className="grid gap-4 md:grid-cols-[1fr_1fr_auto] md:items-end">
        <Field label={t('Current password')}><Input type="password" autoComplete="current-password" value={pw.current} onChange={(e) => setPw({ ...pw, current: e.target.value })} /></Field>
        <Field label={t('New password')}><Input type="password" autoComplete="new-password" value={pw.new} onChange={(e) => setPw({ ...pw, new: e.target.value })} /></Field>
        <Button onClick={changePw} disabled={!pw.current || pw.new.length < 10} icon={<KeyRound className="size-4" />}>{t('Change')}</Button>
      </div>
    </Card>
  )
}

function TwoFactor() {
  const toast = useToast()
  const { reloadStatus } = useAuth()
  const st = useAsync(() => api.get<TOTPState>('/me/totp'))
  const [enroll, setEnroll] = useState<Enrollment>()
  const [code, setCode] = useState('')
  const [codes, setCodes] = useState<string[]>()
  const [disable, setDisable] = useState<{ password: string; code: string }>()
  const [renew, setRenew] = useState<string>()
  const [busy, setBusy] = useState(false)

  const run = async (fn: () => Promise<void>) => {
    setBusy(true)
    try { await fn() } catch (e) { toast('err', (e as Error).message) } finally { setBusy(false) }
  }
  const start = () => run(async () => { setEnroll(await api.post<Enrollment>('/me/totp/setup')); setCode('') })
  const confirm = () => run(async () => {
    const r = await api.post<{ recovery_codes: string[] }>('/me/totp/enable', { code })
    setEnroll(undefined); setCodes(r.recovery_codes); st.reload(); reloadStatus()
  })
  const off = () => run(async () => {
    await api.post('/me/totp/disable', disable); setDisable(undefined); st.reload(); reloadStatus(); toast('ok', t('Two-factor authentication turned off'))
  })
  const newCodes = () => run(async () => {
    const r = await api.post<{ recovery_codes: string[] }>('/me/totp/recovery', { code: renew }); setRenew(undefined); setCodes(r.recovery_codes); st.reload()
  })

  return (
    <Card title={t('Two-factor authentication')} subtitle={t('A code from an authenticator app on every sign-in (TOTP)')}>
      {!st.data ? <Spinner /> : st.data.enabled ? (
        <div className="space-y-4">
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <Badge className="bg-emerald-500/15 text-emerald-500 ring-emerald-500/30"><ShieldCheck className="mr-1 inline size-3.5" />{t('On')}</Badge>
            <span className="text-muted">{t('{n} recovery codes left', { n: st.data.recovery_left })}</span>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button onClick={() => setRenew('')} icon={<RefreshCw className="size-4" />}>{t('New recovery codes')}</Button>
            {!st.data.required && <Button variant="danger" onClick={() => setDisable({ password: '', code: '' })} icon={<ShieldOff className="size-4" />}>{t('Turn off')}</Button>}
          </div>
          {st.data.required && <p className="text-xs text-muted">{t('Your administrator requires two-factor authentication for local accounts.')}</p>}
        </div>
      ) : (
        <div className="space-y-4">
          <p className="text-sm text-muted">{t('Protect your account with a second step at sign-in. Works with Microsoft Authenticator, Google Authenticator, 1Password and other TOTP apps.')}</p>
          <Button variant="primary" loading={busy} onClick={start} icon={<ShieldCheck className="size-4" />}>{t('Set up two-factor authentication')}</Button>
        </div>
      )}

      <Modal open={!!enroll} onClose={() => setEnroll(undefined)} title={t('Set up two-factor authentication')}
        footer={<><Button onClick={() => setEnroll(undefined)}>{t('Cancel')}</Button><Button variant="primary" loading={busy} disabled={code.trim().length !== 6} onClick={confirm}>{t('Turn on')}</Button></>}>
        {enroll && (
          <div className="space-y-4">
            <p className="text-sm text-muted">{t('Scan this code with your authenticator app, or enter the key by hand. Then enter the 6-digit code it shows.')}</p>
            <div className="flex justify-center"><img src={enroll.qr} alt={t('QR code for the authenticator app')} className="size-48 rounded-xl bg-white p-2" /></div>
            <p className="break-all text-center font-mono text-xs text-muted">{enroll.secret}</p>
            <Field label={t('Code')}><Input autoFocus inputMode="numeric" autoComplete="one-time-code" value={code} onChange={(e) => setCode(e.target.value)} placeholder="123456" /></Field>
          </div>
        )}
      </Modal>
      <Modal open={!!codes} onClose={() => setCodes(undefined)} title={t('Recovery codes')} footer={<Button variant="primary" onClick={() => setCodes(undefined)}>{t('Done')}</Button>}>
        {codes && <RecoveryCodes codes={codes} />}
      </Modal>
      <Modal open={!!disable} onClose={() => setDisable(undefined)} title={t('Turn off two-factor authentication')}
        footer={<><Button onClick={() => setDisable(undefined)}>{t('Cancel')}</Button><Button variant="danger" loading={busy} disabled={!disable?.password || !disable?.code} onClick={off}>{t('Turn off')}</Button></>}>
        {disable && (
          <div className="space-y-4">
            <Field label={t('Current password')}><Input type="password" autoComplete="current-password" value={disable.password} onChange={(e) => setDisable({ ...disable, password: e.target.value })} /></Field>
            <Field label={t('Code or recovery code')}><Input autoComplete="one-time-code" value={disable.code} onChange={(e) => setDisable({ ...disable, code: e.target.value })} /></Field>
          </div>
        )}
      </Modal>
      <Modal open={renew !== undefined} onClose={() => setRenew(undefined)} title={t('New recovery codes')}
        footer={<><Button onClick={() => setRenew(undefined)}>{t('Cancel')}</Button><Button variant="primary" loading={busy} disabled={(renew ?? '').trim().length !== 6} onClick={newCodes}>{t('Create')}</Button></>}>
        <div className="space-y-4">
          <p className="text-sm text-muted">{t('Your current recovery codes stop working.')}</p>
          <Field label={t('Code from your authenticator app')}><Input autoFocus inputMode="numeric" value={renew ?? ''} onChange={(e) => setRenew(e.target.value)} placeholder="123456" /></Field>
        </div>
      </Modal>
    </Card>
  )
}

const expiries = () => [
  { value: '30', label: t('30 days') },
  { value: '90', label: t('90 days') },
  { value: '365', label: t('1 year') },
  { value: '0', label: t('Never') },
]

function Tokens() {
  const { user } = useAuth()
  const toast = useToast()
  const [all, setAll] = useState(false)
  const tokens = useAsync(() => api.get<APIToken[]>('/tokens' + (all ? '?all=1' : '')), [all])
  const [create, setCreate] = useState<{ name: string; read_only: boolean; days: string }>()
  const [secret, setSecret] = useState<string>()
  const [busy, setBusy] = useState(false)
  const save = async () => {
    setBusy(true)
    try {
      const r = await api.post<{ secret: string }>('/tokens', { ...create, days: Number(create!.days) })
      setCreate(undefined); setSecret(r.secret); tokens.reload()
    } catch (e) { toast('err', (e as Error).message) } finally { setBusy(false) }
  }
  const revoke = async (tk: APIToken) => {
    if (!confirm(t('Revoke token {name}? Scripts using it stop working.', { name: tk.name }))) return
    try { await api.del('/tokens/' + tk.id); tokens.reload() } catch (e) { toast('err', (e as Error).message) }
  }
  const now = Date.now() / 1000
  return (
    <Card className="xl:col-span-2" title={t('API tokens')} subtitle={t('For scripts and integrations. A token has the role and sites of its user.')}
      actions={<>
        {can.admin(user) && <Toggle checked={all} onChange={setAll} label={t('All users')} />}
        <a href="https://github.com/rhisijmuiden/unifi-syslog-server/blob/main/docs/API.md" target="_blank" rel="noreferrer"><Button variant="ghost" icon={<Code2 className="size-4" />}>{t('API documentation')}</Button></a>
        <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setCreate({ name: '', read_only: true, days: '90' })}>{t('New token')}</Button>
      </>} pad={false}>
      {!tokens.data ? <div className="grid place-items-center py-10"><Spinner /></div> : !tokens.data.length ? (
        <Empty icon={<KeyRound className="size-6" />} title={t('No API tokens')} text={t('Create a token to use the REST API from scripts, Power BI, Grafana or other tools.')} />
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-[11px] uppercase tracking-wide text-muted"><tr className="border-b border-app">
              <th className="px-5 py-3 font-medium">{t('Name')}</th>{all && <th className="px-3 py-3 font-medium">{t('User')}</th>}<th className="px-3 py-3 font-medium">{t('Token')}</th><th className="px-3 py-3 font-medium">{t('Access rights')}</th><th className="px-3 py-3 font-medium">{t('Expires')}</th><th className="px-3 py-3 font-medium">{t('Last used')}</th><th className="px-5 py-3" />
            </tr></thead>
            <tbody>{tokens.data.map((tk) => (
              <tr key={tk.id} className="border-b border-app/60">
                <td className="px-5 py-3 font-medium">{tk.name}</td>
                {all && <td className="px-3 py-3">{tk.username}</td>}
                <td className="px-3 py-3 font-mono text-xs text-muted">{tk.prefix}…</td>
                <td className="px-3 py-3">{tk.read_only ? <Badge>{t('Read-only')}</Badge> : <Badge className="bg-amber-500/15 text-amber-500 ring-amber-500/30">{t('Read and write')}</Badge>}</td>
                <td className="px-3 py-3 text-muted">{!tk.expires_at ? t('Never') : tk.expires_at < now ? <span className="text-red-500">{t('Expired')}</span> : fmtTime(tk.expires_at * 1000)}</td>
                <td className="px-3 py-3 text-muted">{tk.last_used ? <>{fmtAgo(tk.last_used * 1000)}{tk.last_ip && <span className="font-mono text-xs"> · {tk.last_ip}</span>}</> : t('never')}</td>
                <td className="px-5 py-3 text-right"><button aria-label={t('Revoke')} title={t('Revoke')} onClick={() => revoke(tk)} className="cursor-pointer rounded-lg p-2 text-red-500 hover:bg-red-500/10"><Trash2 className="size-4" /></button></td>
              </tr>
            ))}</tbody>
          </table>
        </div>
      )}

      <Modal open={!!create} onClose={() => setCreate(undefined)} title={t('New API token')}
        footer={<><Button onClick={() => setCreate(undefined)}>{t('Cancel')}</Button><Button variant="primary" loading={busy} disabled={!create?.name.trim()} onClick={save}>{t('Create')}</Button></>}>
        {create && (
          <div className="space-y-4">
            <Field label={t('Name')} hint={t('What the token is for, e.g. "Grafana" or "nightly export"')}><Input autoFocus value={create.name} onChange={(e) => setCreate({ ...create, name: e.target.value })} /></Field>
            <Field label={t('Expires after')}><Select value={create.days} onChange={(days) => setCreate({ ...create, days })} options={expiries()} /></Field>
            <Toggle checked={create.read_only} onChange={(read_only) => setCreate({ ...create, read_only })} label={t('Read-only')} description={t('Can search and read, but never change anything. Recommended.')} />
          </div>
        )}
      </Modal>
      <Modal open={!!secret} onClose={() => setSecret(undefined)} title={t('Token created')} footer={<Button variant="primary" onClick={() => setSecret(undefined)}>{t('Done')}</Button>}>
        {secret && (
          <div className="space-y-3">
            <p className="text-sm text-muted">{t('Copy the token now. It is shown only once.')}</p>
            <div className="flex items-center gap-2 rounded-xl bg-[var(--panel-2)] px-3 py-2.5 font-mono text-xs">
              <span className="min-w-0 flex-1 break-all">{secret}</span>
              <button aria-label={t('Copy')} onClick={() => { navigator.clipboard.writeText(secret); toast('ok', t('Copied')) }} className="cursor-pointer"><Copy className="size-4" /></button>
            </div>
            <pre className="overflow-x-auto rounded-xl bg-[var(--panel-2)] p-3 text-xs">{`curl -H "Authorization: Bearer ${secret.slice(0, 10)}…" \\\n  ${window.location.origin}/api/sites`}</pre>
          </div>
        )}
      </Modal>
    </Card>
  )
}
