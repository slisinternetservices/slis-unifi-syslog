import { useState } from 'react'
import { LogIn, ShieldCheck, ArrowRight } from 'lucide-react'
import { api, type Branding, type User } from '../api'
import { BrandMark } from '../components/Brand'
import { RecoveryCodes } from '../components/Access'
import { t } from '../i18n'
import { Button, Field, Input } from '../components/ui'

interface Enrollment { secret: string; uri: string; qr: string }
type LoginResult = User | { totp_required?: boolean; totp_setup?: boolean; ticket: string; enrollment?: Enrollment }

export default function Login({ instance, branding, sso, onLogin, initialError }: { instance: string; branding: Branding; sso?: { enabled: boolean; label: string }; onLogin: (u: User) => void; initialError?: string }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState(initialError)
  const [busy, setBusy] = useState(false)
  // Second step: a code from the authenticator app, or setting one up.
  const [ticket, setTicket] = useState<string>()
  const [enroll, setEnroll] = useState<Enrollment>()
  const [code, setCode] = useState('')
  const [recovery, setRecovery] = useState<{ codes: string[]; user: User }>()

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      const res = await api.post<LoginResult>('/auth/login', { username, password })
      if ('ticket' in res) {
        setTicket(res.ticket)
        setEnroll(res.enrollment)
        setCode('')
      } else onLogin(res)
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const submitCode = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      const res = await api.post<{ user: User; recovery_codes?: string[] }>('/auth/login/totp', { ticket, code })
      if (res.recovery_codes) setRecovery({ codes: res.recovery_codes, user: res.user })
      else onLogin(res.user)
    } catch (err) {
      setError((err as Error).message)
      if ((err as Error).message === t('sign-in expired, please start again')) { setTicket(undefined); setEnroll(undefined) }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="relative grid min-h-full place-items-center overflow-hidden p-4">
      <div className="pointer-events-none absolute -left-40 -top-40 size-[34rem] rounded-full bg-indigo-600/25 blur-3xl" />
      <div className="pointer-events-none absolute -bottom-40 -right-40 size-[34rem] rounded-full bg-cyan-500/20 blur-3xl" />
      <div className="panel relative w-full max-w-sm rounded-3xl p-8 shadow-2xl fade-in">
        <div className="mb-7 flex flex-col items-center text-center">
          <div className="mb-4 flex justify-center"><BrandMark branding={branding} large /></div>
          <h1 className="text-xl font-semibold">{instance}</h1>
          {branding.company_name && <p className="mt-0.5 text-sm font-medium text-muted">{branding.company_name}</p>}
          <p className="mt-1 text-sm text-muted">{t('Sign in to continue')}</p>
        </div>
        {error && <div className="mb-4 rounded-xl bg-red-500/10 px-4 py-3 text-sm text-red-500 ring-1 ring-red-500/30">{t(error)}</div>}
        {recovery ? (
          <div className="space-y-4">
            <div className="flex items-center gap-2 text-sm font-medium text-emerald-500"><ShieldCheck className="size-4" />{t('Two-factor authentication is on')}</div>
            <RecoveryCodes codes={recovery.codes} />
            <Button variant="primary" className="w-full" icon={<ArrowRight className="size-4" />} onClick={() => onLogin(recovery.user)}>{t('Continue')}</Button>
          </div>
        ) : ticket ? (
          <form onSubmit={submitCode} className="space-y-4">
            {enroll ? (
              <>
                <p className="text-sm text-muted">{t('Two-factor authentication is required. Scan this code with an authenticator app (Microsoft Authenticator, Google Authenticator, 1Password …) and enter the 6-digit code.')}</p>
                <div className="flex justify-center"><img src={enroll.qr} alt={t('QR code for the authenticator app')} className="size-44 rounded-xl bg-white p-2" /></div>
                <p className="break-all text-center font-mono text-xs text-muted">{enroll.secret}</p>
              </>
            ) : (
              <p className="text-sm text-muted">{t('Enter the 6-digit code from your authenticator app, or one of your recovery codes.')}</p>
            )}
            <Field label={t('Code')}><Input autoFocus inputMode={enroll ? 'numeric' : 'text'} autoComplete="one-time-code" value={code} onChange={(e) => setCode(e.target.value)} placeholder="123456" /></Field>
            <Button type="submit" variant="primary" loading={busy} disabled={code.trim().length < 6} className="w-full" icon={<ShieldCheck className="size-4" />}>{t('Verify')}</Button>
            <button type="button" onClick={() => { setTicket(undefined); setEnroll(undefined); setError(undefined) }} className="w-full cursor-pointer text-center text-xs text-muted hover:text-[var(--text)]">{t('Back')}</button>
          </form>
        ) : <>
        {sso?.enabled && (
          <>
            <a href="/api/auth/entra/login" className="flex w-full items-center justify-center gap-3 rounded-xl bg-[#2f2f2f] px-4 py-2.5 text-sm font-medium text-white ring-1 ring-white/10 transition hover:bg-[#3a3a3a]">
              <svg viewBox="0 0 21 21" className="size-4"><rect x="1" y="1" width="9" height="9" fill="#f25022" /><rect x="11" y="1" width="9" height="9" fill="#7fba00" /><rect x="1" y="11" width="9" height="9" fill="#00a4ef" /><rect x="11" y="11" width="9" height="9" fill="#ffb900" /></svg>
              {!sso.label || sso.label === 'Sign in with Microsoft' ? t('Sign in with Microsoft') : sso.label}
            </a>
            <div className="my-5 flex items-center gap-3 text-xs text-muted"><div className="h-px flex-1 bg-[var(--border)]" />{t('or with a local account')}<div className="h-px flex-1 bg-[var(--border)]" /></div>
          </>
        )}
        <form onSubmit={submit} className="space-y-4">
          <Field label={t('Username')}><Input autoFocus autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} /></Field>
          <Field label={t('Password')}><Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} /></Field>
          <Button type="submit" variant="primary" loading={busy} className="w-full" icon={<LogIn className="size-4" />}>{t('Sign in')}</Button>
        </form>
        </>}
      </div>
    </div>
  )
}
