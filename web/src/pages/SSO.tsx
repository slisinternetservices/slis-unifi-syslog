import { useEffect, useState, type ReactNode } from 'react'
import { KeyRound, Save, Copy, Plus, Trash2 } from 'lucide-react'
import { api } from '../api'
import { SitePicker, roleOptions } from '../components/Access'
import { Button, Card, Field, Input, PageHeader, Select, Spinner, Toggle, useToast } from '../components/ui'
import { t } from '../i18n'

interface SSOSettings { enabled: boolean; tenant_id: string; client_id: string; client_secret: string; admin_group_id: string; allowed_group_id: string; auto_provision: boolean; default_role: string; button_label: string; sync_language: boolean; group_roles: GroupRole[] }
interface GroupRole { group_id: string; name: string; role: string; sites: string[] }

export default function SSO() {
  const toast = useToast()
  const [s, setS] = useState<SSOSettings>()
  const [redirect, setRedirect] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => { api.get<{ settings: SSOSettings; redirect_uri: string }>('/settings/sso').then((r) => { setS(r.settings); setRedirect(r.redirect_uri) }) }, [])
  if (!s) return <div className="grid place-items-center py-24"><Spinner /></div>
  const up = (p: Partial<SSOSettings>) => setS({ ...s, ...p })
  const save = async () => {
    setBusy(true)
    try { setS(await api.put<SSOSettings>('/settings/sso', s)); toast('ok', t('SSO settings saved')) } catch (e) { toast('err', (e as Error).message) } finally { setBusy(false) }
  }
  return (
    <>
      <PageHeader title="Microsoft Entra SSO" subtitle={t('Sign in with your Microsoft 365 / Entra ID account')} icon={<KeyRound className="size-5" />}
        actions={<Button variant="primary" loading={busy} onClick={save} icon={<Save className="size-4" />}>{t('Save')}</Button>} />
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-3">
        <Card className="xl:col-span-2" title={t('Configuration')}>
          <div className="space-y-5">
            <Toggle checked={s.enabled} onChange={(enabled) => up({ enabled })} label={t('Enable Entra SSO')} description={t('Shows "Sign in with Microsoft" on the login screen. Local accounts keep working.')} />
            <div className="grid gap-4 md:grid-cols-2">
              <Field label="Tenant ID (Directory ID)"><Input value={s.tenant_id} onChange={(e) => up({ tenant_id: e.target.value.trim() })} placeholder="00000000-0000-0000-0000-000000000000" /></Field>
              <Field label="Client ID (Application ID)"><Input value={s.client_id} onChange={(e) => up({ client_id: e.target.value.trim() })} /></Field>
              <Field label={t('Client secret')} hint={t('Stored encrypted')}><Input type="password" value={s.client_secret} onChange={(e) => up({ client_secret: e.target.value })} /></Field>
              <Field label={t('Button text')}><Input value={s.button_label} onChange={(e) => up({ button_label: e.target.value })} /></Field>
              <Field label={t('Allowed group (Object ID)')} hint={t('Empty = everyone in the tenant')}><Input value={s.allowed_group_id} onChange={(e) => up({ allowed_group_id: e.target.value.trim() })} /></Field>
              <Field label={t('Default role')} hint={t('For users in none of the groups below')}><Select value={s.default_role} onChange={(default_role) => up({ default_role })} options={roleOptions()} /></Field>
            </div>
            <Toggle checked={s.auto_provision} onChange={(auto_provision) => up({ auto_provision })} label={t('Auto-provision users')} description={t('Creates an account on first sign-in')} />
            <Toggle checked={s.sync_language} onChange={(sync_language) => up({ sync_language })} label={t('Use the language of the Microsoft account')}
              description={t('Read on every sign-in until the user picks a language here. Needs the delegated Graph permission User.Read.')} />
          </div>
        </Card>
        <Card className="order-last xl:col-span-2" title={t('Entra groups to roles')} subtitle={t('Members of a group get its role and sites automatically at every sign-in. The highest role wins; sites of groups with that role are combined.')}
          actions={<Button icon={<Plus className="size-4" />} onClick={() => up({ group_roles: [...s.group_roles, { group_id: '', name: '', role: 'viewer', sites: [] }] })}>{t('Add group')}</Button>}>
          {!s.group_roles.length ? (
            <p className="text-sm text-muted">{t('No groups mapped: roles and sites of Entra users are set by hand under Users.')}</p>
          ) : (
            <div className="space-y-3">
              {s.group_roles.map((g, i) => {
                const set = (p: Partial<GroupRole>) => up({ group_roles: s.group_roles.map((x, j) => (j === i ? { ...x, ...p } : x)) })
                return (
                  <div key={i} className="rounded-xl border border-app p-4">
                    <div className="grid gap-3 md:grid-cols-[1fr_1fr_1fr_auto] md:items-end">
                      <Field label={t('Group (Object ID)')}><Input value={g.group_id} onChange={(e) => set({ group_id: e.target.value.trim() })} placeholder="00000000-0000-0000-0000-000000000000" /></Field>
                      <Field label={t('Label')}><Input value={g.name} onChange={(e) => set({ name: e.target.value })} placeholder={t('e.g. Customer A analysts')} /></Field>
                      <Field label={t('Role')}><Select value={g.role} onChange={(role) => set({ role })} options={roleOptions()} /></Field>
                      <button aria-label={t('Remove')} onClick={() => up({ group_roles: s.group_roles.filter((_, j) => j !== i) })} className="mb-1 cursor-pointer rounded-lg p-2 text-red-500 hover:bg-red-500/10"><Trash2 className="size-4" /></button>
                    </div>
                    {g.role !== 'admin' && <div className="mt-3"><Field label={t('Sites')}><SitePicker value={g.sites} onChange={(sites) => set({ sites })} /></Field></div>}
                  </div>
                )
              })}
            </div>
          )}
        </Card>
        <Card title={t('App registration in Entra')}>
          <ol className="list-decimal space-y-3 pl-4 text-sm text-muted">
            <li>{rich(t('In the {center}, go to App registrations → New registration.'), { center: 'Entra admin center' })}</li>
            <li>{rich(t('Choose platform {web} and enter this redirect URI:'), { web: 'Web' })}
              <div className="mt-2 flex items-center gap-2 rounded-lg bg-[var(--panel-2)] px-2.5 py-2 font-mono text-xs text-[var(--text)]">
                <span className="truncate">{redirect}</span>
                <button onClick={() => { navigator.clipboard.writeText(redirect); toast('ok', t('Copied')) }} aria-label={t('Copy')} className="cursor-pointer"><Copy className="size-3.5" /></button>
              </div>
            </li>
            <li>{t('Under Certificates & secrets, create a client secret.')}</li>
            <li>{rich(t('If you use groups, add the {groups} claim (Security groups) under Token configuration.'), { groups: 'groups' })}</li>
            <li>{rich(t('For the account language: keep the delegated permission {perm} under API permissions, or add the optional claim {claim}.'), { perm: 'User.Read', claim: 'xms_pl' })}</li>
            <li>{t('URI not working behind a reverse proxy? Set the base URL under Settings.')}</li>
          </ol>
        </Card>
      </div>
    </>
  )
}

function rich(text: string, parts: Record<string, ReactNode>) {
  return text.split(/(\{\w+\})/).map((p, i) => {
    const k = p.match(/^\{(\w+)\}$/)?.[1]
    return k && k in parts ? <span key={i} className="text-[var(--text)]">{parts[k]}</span> : p
  })
}
