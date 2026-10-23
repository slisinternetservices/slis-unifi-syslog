import { useState } from 'react'
import { Plus, Pencil, RefreshCw, Server, CheckCircle2, AlertCircle, PlugZap, Trash2 } from 'lucide-react'
import { api } from '../api'
import { Badge, Button, Card, Empty, Field, Input, Modal, PageHeader, Spinner, Toggle, useAsync, useToast } from '../components/ui'
import { fmtAgo, fmtNum } from '../lib/format'
import { t } from '../i18n'

interface Controller {
  id: number; name: string; url: string; api_key: string; site: string; verify_tls: boolean; enabled: boolean
  last_sync: number; last_error: string; clients: number; devices: number
}
interface SyncResult { sites: number; clients: number; devices: number }

const blank: Controller = { id: 0, name: '', url: '', api_key: '', site: '', verify_tls: false, enabled: true, last_sync: 0, last_error: '', clients: 0, devices: 0 }

export default function Controllers() {
  const toast = useToast()
  const { data, loading, reload } = useAsync(() => api.get<Controller[]>('/controllers'))
  const [edit, setEdit] = useState<Controller | null>(null)
  const [syncing, setSyncing] = useState(0)
  const sync = async (c: Controller) => {
    setSyncing(c.id)
    try {
      const r = await api.post<SyncResult>(`/controllers/${c.id}/sync`)
      toast('ok', t('{clients} clients and {devices} devices read', { clients: fmtNum(r.clients), devices: fmtNum(r.devices) }))
    } catch (e) { toast('err', (e as Error).message) } finally { setSyncing(0); reload() }
  }
  return (
    <>
      <PageHeader title={t('UniFi controllers')} subtitle={t('Read client and device names from UniFi Network, so logs show who and what instead of addresses')} icon={<Server className="size-5" />}
        actions={<Button variant="primary" onClick={() => setEdit(blank)} icon={<Plus className="size-4" />}>{t('Add controller')}</Button>} />
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-3">
        <Card pad={false} className="xl:col-span-2">
          {loading && !data ? <div className="grid place-items-center py-16"><Spinner /></div>
            : !data?.length ? <Empty icon={<Server className="size-8" />} title={t('No controllers yet')} text={t('Add your UniFi console with an API key to see client and device names in the logs.')}
              action={<Button variant="primary" onClick={() => setEdit(blank)} icon={<Plus className="size-4" />}>{t('Add controller')}</Button>} />
            : (
              <ul className="divide-y divide-[var(--border)]">
                {data.map((c) => (
                  <li key={c.id} className="flex flex-wrap items-center gap-4 px-5 py-4">
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        {c.last_error ? <AlertCircle className="size-4 shrink-0 text-red-500" /> : c.last_sync ? <CheckCircle2 className="size-4 shrink-0 text-emerald-500" /> : <RefreshCw className="size-4 shrink-0 text-muted" />}
                        <span className="truncate font-medium">{c.name}</span>
                        {!c.enabled && <Badge>{t('Off')}</Badge>}
                        {c.site && <Badge className="bg-indigo-500/10 text-indigo-400 ring-indigo-500/30">{c.site}</Badge>}
                      </div>
                      <div className="mt-0.5 truncate font-mono text-xs text-muted">{c.url}</div>
                      {c.last_error ? <div className="mt-1 text-xs text-red-500">{c.last_error}</div>
                        : <div className="mt-1 text-xs text-muted">{c.last_sync ? t('{clients} clients · {devices} devices · synced {ago}', { clients: fmtNum(c.clients), devices: fmtNum(c.devices), ago: fmtAgo(c.last_sync * 1000) }) : t('Not synced yet')}</div>}
                    </div>
                    <Button onClick={() => sync(c)} loading={syncing === c.id} disabled={!c.enabled} icon={<RefreshCw className="size-4" />}>{t('Sync now')}</Button>
                    <Button variant="ghost" title={t('Edit')} onClick={() => setEdit(c)} icon={<Pencil className="size-4" />} />
                  </li>
                ))}
              </ul>
            )}
        </Card>
        <Card title={t('How to create an API key')}>
          <ol className="list-decimal space-y-2 pl-4 text-sm text-muted">
            <li>{t('Open UniFi Network (version 9.0 or later) on your console.')}</li>
            <li>{t('Go to Settings → Control Plane → Integrations.')}</li>
            <li>{t('Create an API key and copy it; it is shown only once.')}</li>
            <li>{t('Enter the console address, e.g. https://192.168.1.1, and the key here.')}</li>
          </ol>
          <p className="mt-4 text-xs text-muted">{t('Names are refreshed every 5 minutes. Clients that are offline keep their last known name. Without a controller, names are learnt from DHCP logs.')}</p>
        </Card>
      </div>
      {edit && <EditController c={edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reload() }} />}
    </>
  )
}

function EditController({ c, onClose, onSaved }: { c: Controller; onClose: () => void; onSaved: () => void }) {
  const toast = useToast()
  const [f, setF] = useState(c)
  const [busy, setBusy] = useState(false)
  const [testing, setTesting] = useState(false)
  const sites = useAsync(() => api.get<string[]>('/sites'))
  const up = (p: Partial<Controller>) => setF({ ...f, ...p })
  const save = async () => {
    setBusy(true)
    try {
      await (f.id ? api.put(`/controllers/${f.id}`, f) : api.post('/controllers', f))
      toast('ok', t('Saved'))
      onSaved()
    } catch (e) { toast('err', (e as Error).message) } finally { setBusy(false) }
  }
  const test = async () => {
    setTesting(true)
    try {
      const r = await api.post<SyncResult>('/controllers/test', f)
      toast('ok', t('Connected: {sites} sites, {clients} clients, {devices} devices', { sites: r.sites, clients: fmtNum(r.clients), devices: fmtNum(r.devices) }))
    } catch (e) { toast('err', (e as Error).message) } finally { setTesting(false) }
  }
  const remove = async () => {
    if (!confirm(t('Remove {name}? Names read from it are removed too.', { name: c.name }))) return
    try { await api.del(`/controllers/${c.id}`); toast('ok', t('Removed')); onSaved() } catch (e) { toast('err', (e as Error).message) }
  }
  return (
    <Modal open onClose={onClose} title={c.id ? c.name : t('Add controller')}
      footer={<>
        {c.id > 0 && <Button variant="danger" onClick={remove} icon={<Trash2 className="size-4" />} className="mr-auto">{t('Remove')}</Button>}
        <Button onClick={test} loading={testing} disabled={!f.url || !f.api_key} icon={<PlugZap className="size-4" />}>{t('Test connection')}</Button>
        <Button variant="primary" onClick={save} loading={busy} disabled={!f.name || !f.url || !f.api_key}>{t('Save')}</Button>
      </>}>
      <div className="space-y-4">
        <Field label={t('Name')}><Input value={f.name} placeholder={t('Head office')} onChange={(e) => up({ name: e.target.value })} /></Field>
        <Field label={t('Console address')} hint={t('UniFi OS console (UDM, UCG, Cloud Key) or self-hosted Network application')}>
          <Input value={f.url} placeholder="https://192.168.1.1" onChange={(e) => up({ url: e.target.value.trim() })} />
        </Field>
        <Field label={t('API key')}><Input type="password" autoComplete="new-password" value={f.api_key} onChange={(e) => up({ api_key: e.target.value.trim() })} /></Field>
        <Field label={t('Site in this server')} hint={t('Private IP addresses repeat between locations. Pick the site whose logs this controller\'s clients appear in; leave empty to match any site.')}>
          <Input list="ctrl-sites" value={f.site} placeholder={t('Any site')} onChange={(e) => up({ site: e.target.value })} />
          <datalist id="ctrl-sites">{(sites.data ?? []).map((s) => <option key={s} value={s} />)}</datalist>
        </Field>
        <Toggle checked={f.verify_tls} onChange={(verify_tls) => up({ verify_tls })} label={t('Verify certificate')} description={t('UniFi consoles use a self-signed certificate by default; turn this on only when the console has a trusted certificate')} />
        <Toggle checked={f.enabled} onChange={(enabled) => up({ enabled })} label={t('Enabled')} description={t('Read names every 5 minutes')} />
      </div>
    </Modal>
  )
}
