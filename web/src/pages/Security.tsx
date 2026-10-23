import { useEffect, useState } from 'react'
import { ShieldCheck, Save } from 'lucide-react'
import { api } from '../api'
import { Button, Card, Field, Input, PageHeader, Spinner, Toggle, useToast } from '../components/ui'
import { t } from '../i18n'

interface SecuritySettings { require_totp: boolean; audit_retention_days: number }

export default function Security() {
  const toast = useToast()
  const [s, setS] = useState<SecuritySettings>()
  const [without, setWithout] = useState(0)
  const [busy, setBusy] = useState(false)
  useEffect(() => { api.get<{ settings: SecuritySettings; local_without_totp: number }>('/settings/security').then((r) => { setS(r.settings); setWithout(r.local_without_totp) }) }, [])
  if (!s) return <div className="grid place-items-center py-24"><Spinner /></div>
  const save = async () => {
    setBusy(true)
    try { setS(await api.put<SecuritySettings>('/settings/security', s)); toast('ok', t('Settings saved')) } catch (e) { toast('err', (e as Error).message) } finally { setBusy(false) }
  }
  return (
    <>
      <PageHeader title={t('Security')} subtitle={t('Sign-in policy and audit log retention')} icon={<ShieldCheck className="size-5" />}
        actions={<Button variant="primary" loading={busy} onClick={save} icon={<Save className="size-4" />}>{t('Save')}</Button>} />
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-2">
        <Card title={t('Two-factor authentication')}>
          <div className="space-y-4">
            <Toggle checked={s.require_totp} onChange={(require_totp) => setS({ ...s, require_totp })} label={t('Require two-factor authentication for local accounts')}
              description={t('Users without it set up an authenticator app at their next sign-in. Entra accounts use the MFA of Microsoft Entra.')} />
            {without > 0 && <p className="rounded-xl bg-amber-500/10 px-3 py-2 text-xs text-amber-600 ring-1 ring-amber-500/30 dark:text-amber-400">{t('{n} local accounts do not use two-factor authentication yet.', { n: without })}</p>}
            <p className="text-xs text-muted">{t('Lost authenticator? Reset two-factor authentication for that user under Users.')}</p>
          </div>
        </Card>
        <Card title={t('Audit log')}>
          <Field label={t('Keep audit log entries (days)')} hint={t('Between 30 and 3650 days')}>
            <Input type="number" min={30} max={3650} value={s.audit_retention_days} onChange={(e) => setS({ ...s, audit_retention_days: Number(e.target.value) })} />
          </Field>
        </Card>
      </div>
    </>
  )
}
