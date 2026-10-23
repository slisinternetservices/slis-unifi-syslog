import { useState } from 'react'
import { Users as UsersIcon, Plus, Pencil, Trash2, ShieldCheck, ShieldOff } from 'lucide-react'
import { api, type User } from '../api'
import { useAuth } from '../App'
import { RoleBadge, SitePicker, roleOptions, sitesSummary } from '../components/Access'
import { Badge, Button, Card, Field, Input, Modal, PageHeader, Select, Spinner, Toggle, useAsync, useToast } from '../components/ui'
import { fmtAgo } from '../lib/format'
import { languages, t } from '../i18n'

type Edit = Partial<User> & { password?: string }

export default function UsersPage() {
  const { user: me } = useAuth()
  const toast = useToast()
  const users = useAsync(() => api.get<User[]>('/users'))
  const [edit, setEdit] = useState<Edit>()
  const save = async () => {
    try {
      if (edit!.id) await api.put('/users/' + edit!.id, edit)
      else await api.post('/users', edit)
      setEdit(undefined); users.reload(); toast('ok', t('User saved'))
    } catch (e) { toast('err', (e as Error).message) }
  }
  const resetTOTP = async (u: User) => {
    if (!confirm(t('Reset two-factor authentication for {name}? They set it up again at their next sign-in if it is required.', { name: u.username }))) return
    try { await api.post(`/users/${u.id}/totp/reset`); users.reload(); toast('ok', t('Two-factor authentication reset')) } catch (e) { toast('err', (e as Error).message) }
  }
  return (
    <>
      <PageHeader title={t('Users')} subtitle={t('Local accounts and users signed in via Microsoft Entra')} icon={<UsersIcon className="size-5" />}
        actions={<Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setEdit({ username: '', role: 'viewer', password: '', sites: [] })}>{t('New user')}</Button>} />
      <Card pad={false}>
        {!users.data ? <div className="grid place-items-center py-16"><Spinner /></div> : (
          <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-[11px] uppercase tracking-wide text-muted"><tr className="border-b border-app">
              <th className="px-5 py-3 font-medium">{t('User')}</th><th className="px-3 py-3 font-medium">{t('Role')}</th><th className="px-3 py-3 font-medium">{t('Sites')}</th><th className="px-3 py-3 font-medium">{t('Source')}</th><th className="px-3 py-3 font-medium">2FA</th><th className="px-3 py-3 font-medium">{t('Language')}</th><th className="px-3 py-3 font-medium">{t('Last sign-in')}</th><th className="px-5 py-3" />
            </tr></thead>
            <tbody>
              {users.data.map((u) => (
                <tr key={u.id} className="border-b border-app/60">
                  <td className="px-5 py-3"><div className="font-medium">{u.name || u.username}{u.disabled && <Badge className="ml-2">{t('disabled')}</Badge>}</div><div className="text-xs text-muted">{u.username}{u.email && u.email !== u.username ? ` · ${u.email}` : ''}</div></td>
                  <td className="px-3 py-3"><RoleBadge role={u.role} /></td>
                  <td className="px-3 py-3 text-muted">{u.role === 'admin' ? t('All sites') : sitesSummary(u.sites)}</td>
                  <td className="px-3 py-3 text-muted">{u.provider === 'entra' ? <>Microsoft Entra{u.role_managed && <div className="text-[11px]">{t('role from Entra groups')}</div>}</> : t('Local')}</td>
                  <td className="px-3 py-3">{u.provider !== 'local' ? <span className="text-xs text-muted">{t('via Entra')}</span> : u.totp_enabled
                    ? <span title={t('On')}><ShieldCheck className="size-4 text-emerald-500" /></span>
                    : <span title={t('Off')}><ShieldOff className="size-4 text-muted" /></span>}</td>
                  <td className="px-3 py-3 text-muted">{u.language ? languages.find(([c]) => c === u.language)?.[1] ?? u.language : t('Server default')}</td>
                  <td className="px-3 py-3 text-muted">{u.last_login ? fmtAgo(u.last_login * 1000) : t('never')}</td>
                  <td className="px-5 py-3 text-right">
                    {u.totp_enabled && <button title={t('Reset two-factor authentication')} aria-label={t('Reset two-factor authentication')} onClick={() => resetTOTP(u)} className="cursor-pointer rounded-lg p-2 hover:bg-[var(--panel-2)]"><ShieldOff className="size-4" /></button>}
                    <button aria-label={t('Edit user')} onClick={() => setEdit({ ...u, password: '' })} className="cursor-pointer rounded-lg p-2 hover:bg-[var(--panel-2)]"><Pencil className="size-4" /></button>
                    {u.id !== me.id && <button onClick={async () => { if (confirm(t('Delete {name}?', { name: u.username }))) { await api.del('/users/' + u.id); users.reload() } }} className="cursor-pointer rounded-lg p-2 text-red-500 hover:bg-red-500/10"><Trash2 className="size-4" /></button>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
        )}
      </Card>

      <Modal open={!!edit} onClose={() => setEdit(undefined)} title={edit?.id ? t('Edit user') : t('New user')}
        footer={<><Button onClick={() => setEdit(undefined)}>{t('Cancel')}</Button><Button variant="primary" onClick={save}>{t('Save')}</Button></>}>
        {edit && (
          <div className="space-y-4">
            <Field label={t('Username')}><Input value={edit.username} disabled={!!edit.id} onChange={(e) => setEdit({ ...edit, username: e.target.value })} /></Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label={t('Name')}><Input value={edit.name ?? ''} onChange={(e) => setEdit({ ...edit, name: e.target.value })} /></Field>
              <Field label={t('E-mail')}><Input value={edit.email ?? ''} onChange={(e) => setEdit({ ...edit, email: e.target.value })} /></Field>
            </div>
            {edit.role_managed && <p className="rounded-xl bg-[var(--panel-2)] px-3 py-2 text-xs text-muted">{t('Role and sites come from Entra group mappings and are updated at every sign-in. Change them under Microsoft Entra SSO.')}</p>}
            <Field label={t('Role')}>{edit.role_managed ? <div className="py-1"><RoleBadge role={edit.role ?? 'viewer'} /></div> : <Select value={edit.role ?? 'viewer'} onChange={(role) => setEdit({ ...edit, role: role as User['role'] })} options={roleOptions()} />}</Field>
            {edit.role !== 'admin' && (
              <Field label={t('Sites')} hint={t('Which sites this user sees in logs, dashboards and inventory')}>
                <SitePicker value={edit.sites ?? []} onChange={(sites) => setEdit({ ...edit, sites })} disabled={edit.role_managed} />
              </Field>
            )}
            <Field label={t('Language')} hint={edit.provider === 'entra' && !edit.language_manual ? t('Follows the Microsoft account until a language is chosen here') : t('For the interface and e-mails to this address')}>
              <Select value={edit.language_manual || edit.provider !== 'entra' ? edit.language ?? '' : ''} onChange={(language) => setEdit({ ...edit, language })} options={[{ value: '', label: t('Server default') }, ...languages.map(([value, label]) => ({ value, label }))]} />
            </Field>
            {edit.provider !== 'entra' && <Field label={edit.id ? t('New password (optional)') : t('Password')} hint={t('At least 10 characters')}><Input type="password" value={edit.password} onChange={(e) => setEdit({ ...edit, password: e.target.value })} /></Field>}
            {edit.id && <Toggle checked={!!edit.disabled} onChange={(disabled) => setEdit({ ...edit, disabled })} label={t('Disabled')} description={t('Can no longer sign in')} />}
          </div>
        )}
      </Modal>
    </>
  )
}
