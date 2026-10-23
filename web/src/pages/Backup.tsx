import { useEffect, useState } from 'react'
import { DatabaseBackup, Save, Play, Plus, Pencil, Trash2, PlugZap, RotateCcw, Download, Server, FolderOpen, Cloud, KeyRound } from 'lucide-react'
import { api } from '../api'
import { Badge, Button, Card, Field, Input, Modal, PageHeader, Select, Spinner, Toggle, useAsync, useToast } from '../components/ui'
import { fmtBytes, fmtNum, fmtTime } from '../lib/format'
import { t } from '../i18n'

interface Target { id: string; name: string; type: string; host: string; port: number; username: string; password: string; private_key: string; host_key: string; path: string; skip_verify: boolean; enabled: boolean; endpoint: string; bucket: string; region: string }
interface Settings { enabled: boolean; time: string; full_every_days: number; incremental_hours: number; keep_chains: number; include_logs: boolean; notify_to: string; notify_on: string; encrypt: boolean; passphrase: string; targets: Target[] }
interface Status { running: boolean; job: string; step: string; started: string; last_error: string; last_done: string }
interface Rec { id: string; kind: string; file: string; size: number; rows: number; status: string; message: string; targets: string; created_at: number }
interface Entry { name: string; kind: string; id: string; base_id: string; time: string; size: number }

const typeLabel: Record<string, string> = { ftp: 'FTP', ftps: 'FTPS (explicit)', 'ftps-implicit': 'FTPS (implicit)', scp: 'SCP (SSH)', sftp: 'SFTP (SSH)', s3: 'S3 (MinIO, Wasabi, AWS)', local: 'Local folder' }
const typeName = (type: string) => t(typeLabel[type] ?? type)
const blankTarget: Target = { id: '', name: '', type: 'scp', host: '', port: 0, username: '', password: '', private_key: '', host_key: '', path: '/backup/unify-syslog', skip_verify: false, enabled: true, endpoint: '', bucket: '', region: '' }

export default function Backup() {
  const toast = useToast()
  const [s, setS] = useState<Settings>()
  const [status, setStatus] = useState<Status>()
  const history = useAsync(() => api.get<Rec[]>('/backup/history'))
  const [edit, setEdit] = useState<{ t: Target; idx: number }>()
  const [testing, setTesting] = useState(false)
  const [busy, setBusy] = useState(false)
  const [restoreOpen, setRestoreOpen] = useState(false)

  useEffect(() => { api.get<Settings>('/settings/backup').then(setS) }, [])
  useEffect(() => {
    let prevRunning = false
    const poll = () => api.get<Status>('/backup/status').then((st) => {
      setStatus(st)
      if (prevRunning && !st.running) {
        history.reload()
        if (st.last_error) toast('err', st.last_error)
        else toast('ok', t('Job completed'))
      }
      prevRunning = st.running
    }).catch(() => {})
    poll()
    const id = setInterval(poll, 2000)
    return () => clearInterval(id)
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  if (!s) return <div className="grid place-items-center py-24"><Spinner /></div>
  const up = (p: Partial<Settings>) => setS({ ...s, ...p })
  const save = async (next = s) => {
    setBusy(true)
    try { setS(await api.put<Settings>('/settings/backup', next)); toast('ok', t('Backup settings saved')) } catch (e) { toast('err', (e as Error).message) } finally { setBusy(false) }
  }
  const run = async (kind: string) => {
    try { setStatus(await api.post<Status>('/backup/run', { kind })) } catch (e) { toast('err', (e as Error).message) }
  }
  const saveTarget = () => {
    const targets = [...s.targets]
    if (edit!.idx >= 0) targets[edit!.idx] = edit!.t
    else targets.push(edit!.t)
    setEdit(undefined)
    save({ ...s, targets })
  }
  const testTarget = async () => {
    setTesting(true)
    try { const r = await api.post<{ message: string }>('/backup/test-target', edit!.t); toast('ok', r.message) } catch (e) { toast('err', (e as Error).message) } finally { setTesting(false) }
  }
  const et = edit?.t
  const upT = (p: Partial<Target>) => setEdit({ ...edit!, t: { ...edit!.t, ...p } })

  return (
    <>
      <PageHeader title={t('Backup & restore')} subtitle={t('Full and incremental backups to FTP, FTPS, SCP, SFTP or S3')} icon={<DatabaseBackup className="size-5" />}
        actions={<>
          <Button onClick={() => setRestoreOpen(true)} icon={<RotateCcw className="size-4" />}>{t('Restore')}</Button>
          <Button onClick={() => run('incremental')} disabled={status?.running} icon={<Play className="size-4" />}>{t('Incremental')}</Button>
          <Button variant="primary" onClick={() => run('full')} disabled={status?.running} icon={<Play className="size-4" />}>{t('Full backup')}</Button>
        </>} />

      {status?.running && (
        <div className="mb-6 flex items-center gap-3 rounded-2xl bg-indigo-500/10 px-5 py-4 text-sm ring-1 ring-indigo-500/30">
          <Spinner className="size-4" /><span className="font-medium">{status.job}</span><span className="text-muted">{status.step}</span>
        </div>
      )}

      <div className="grid grid-cols-1 gap-6 xl:grid-cols-3">
        <Card title={t('Schedule')} className="xl:col-span-1" actions={<Button variant="primary" loading={busy} onClick={() => save()} icon={<Save className="size-4" />}>{t('Save')}</Button>}>
          <div className="space-y-4">
            <Toggle checked={s.enabled} onChange={(enabled) => up({ enabled })} label={t('Automatic backups')} />
            <div className="grid grid-cols-2 gap-3">
              <Field label={t('Daily at')}><Input type="time" value={s.time} onChange={(e) => up({ time: e.target.value })} /></Field>
              <Field label={t('Full every (days)')}><Input type="number" min={1} value={s.full_every_days} onChange={(e) => up({ full_every_days: Number(e.target.value) })} /></Field>
              <Field label={t('Extra incremental every (hours)')} hint={t('0 = daily only')}><Input type="number" min={0} value={s.incremental_hours} onChange={(e) => up({ incremental_hours: Number(e.target.value) })} /></Field>
              <Field label={t('Chains to keep')} hint={t('Number of full backups')}><Input type="number" min={1} value={s.keep_chains} onChange={(e) => up({ keep_chains: Number(e.target.value) })} /></Field>
            </div>
            <Toggle checked={s.encrypt} onChange={(encrypt) => up({ encrypt })} label={t('Encrypt backups')} description={t('With a passphrase (age); needed again to restore')} />
            {s.encrypt && <Field label={t('Passphrase')} hint={t('At least 12 characters. Keep it somewhere safe: without it the backups cannot be restored.')}><Input type="password" autoComplete="new-password" value={s.passphrase} onChange={(e) => up({ passphrase: e.target.value })} /></Field>}
            <Toggle checked={s.include_logs} onChange={(include_logs) => up({ include_logs })} label={t('Include log data')} description={t('Off: configuration only (users, categories, settings)')} />
            <div className="grid grid-cols-2 gap-3">
              <Field label={t('Email report')}><Select value={s.notify_on} onChange={(notify_on) => up({ notify_on })} options={[{ value: 'never', label: t('Never') }, { value: 'failure', label: t('On failure') }, { value: 'always', label: t('Always') }]} /></Field>
              <Field label={t('To')}><Input value={s.notify_to} onChange={(e) => up({ notify_to: e.target.value })} placeholder="it@example.com" /></Field>
            </div>
          </div>
        </Card>

        <Card title={t('Remote targets')} className="xl:col-span-2" subtitle={t('Every backup is also kept locally on the server')}
          actions={<Button onClick={() => setEdit({ t: { ...blankTarget }, idx: -1 })} icon={<Plus className="size-4" />}>{t('Add target')}</Button>}>
          {!s.targets.length ? <p className="py-6 text-center text-sm text-muted">{t('No remote backup targets yet.')}</p> : (
            <ul className="divide-y divide-[var(--border)]">
              {s.targets.map((tg, i) => (
                <li key={tg.id || i} className="flex items-center justify-between gap-3 py-3">
                  <div className="flex items-center gap-3">
                    <div className="grid size-9 place-items-center rounded-xl bg-[var(--panel-2)] text-muted">{tg.type === 'local' ? <FolderOpen className="size-4" /> : tg.type === 's3' ? <Cloud className="size-4" /> : <Server className="size-4" />}</div>
                    <div>
                      <div className="flex items-center gap-2 font-medium">{tg.name || tg.host || tg.bucket}<Badge>{typeName(tg.type)}</Badge>{!tg.enabled && <Badge>{t('off')}</Badge>}</div>
                      <div className="font-mono text-xs text-muted">{tg.type === 'local' ? tg.path : tg.type === 's3' ? `${tg.endpoint}/${tg.bucket}${tg.path ? '/' + tg.path.replace(/^\/+/, '') : ''}` : `${tg.username}@${tg.host}${tg.port ? ':' + tg.port : ''}:${tg.path}`}</div>
                    </div>
                  </div>
                  <div className="flex gap-1">
                    <button onClick={() => setEdit({ t: { ...tg }, idx: i })} title={t('Edit')} aria-label={t('Edit')} className="cursor-pointer rounded-lg p-2 hover:bg-[var(--panel-2)]"><Pencil className="size-4" /></button>
                    <button onClick={() => { if (confirm(t('Delete this backup target?'))) save({ ...s, targets: s.targets.filter((_, j) => j !== i) }) }} title={t('Delete')} aria-label={t('Delete')} className="cursor-pointer rounded-lg p-2 text-red-500 hover:bg-red-500/10"><Trash2 className="size-4" /></button>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Card>

        <Card title={t('History')} className="xl:col-span-3" pad={false}>
          {!history.data?.length ? <p className="py-8 text-center text-sm text-muted">{t('No backups made yet.')}</p> : (
            <div className="overflow-x-auto scroll-thin">
              <table className="w-full text-sm">
                <thead className="text-left text-[11px] uppercase tracking-wide text-muted"><tr className="border-b border-app">
                  <th className="px-5 py-3 font-medium">{t('Time')}</th><th className="px-3 py-3 font-medium">{t('Type')}</th><th className="px-3 py-3 font-medium">{t('Status')}</th><th className="px-3 py-3 font-medium text-right">{t('Log rows')}</th>
                  <th className="px-3 py-3 font-medium text-right">{t('Size')}</th><th className="px-3 py-3 font-medium">{t('Uploaded to')}</th><th className="px-5 py-3" />
                </tr></thead>
                <tbody>{history.data.map((r) => (
                  <tr key={r.id} className="border-b border-app/60">
                    <td className="px-5 py-2.5 whitespace-nowrap">{fmtTime(r.created_at * 1000)}</td>
                    <td className="px-3 py-2.5">{r.kind === 'full' ? <Badge className="bg-indigo-500/15 text-indigo-400 ring-indigo-500/30">{t('Full')}</Badge> : <Badge>{t('Incremental')}</Badge>}</td>
                    <td className="px-3 py-2.5" title={r.message}>{r.status === 'ok' ? <span className="text-emerald-500">{t('Succeeded')}</span> : r.status === 'running' ? <span className="text-indigo-400">{t('Running')}</span> : <span className="text-red-500">{t('Failed')}</span>}{r.message && <div className="max-w-xs truncate text-xs text-muted">{r.message}</div>}</td>
                    <td className="px-3 py-2.5 text-right tabular-nums">{fmtNum(r.rows)}</td>
                    <td className="px-3 py-2.5 text-right tabular-nums">{fmtBytes(r.size)}</td>
                    <td className="px-3 py-2.5 text-muted">{r.targets || t('local only')}</td>
                    <td className="px-5 py-2.5 text-right">{r.status === 'ok' && <a href={`/api/backup/download/${r.file}`} title={t('Download')} className="inline-block rounded-lg p-2 hover:bg-[var(--panel-2)]"><Download className="size-4" /></a>}</td>
                  </tr>
                ))}</tbody>
              </table>
            </div>
          )}
        </Card>
      </div>

      <Modal open={!!edit} onClose={() => setEdit(undefined)} title={edit?.idx === -1 ? t('Add backup target') : t('Edit backup target')}
        footer={<><Button onClick={testTarget} loading={testing} icon={<PlugZap className="size-4" />}>{t('Test connection')}</Button><Button variant="primary" onClick={saveTarget}>{t('Save')}</Button></>}>
        {et && (
          <div className="space-y-4">
            <div className="grid grid-cols-2 gap-3">
              <Field label={t('Name')}><Input value={et.name} onChange={(e) => upT({ name: e.target.value })} placeholder={t('Office NAS')} /></Field>
              <Field label={t('Type')}><Select value={et.type} onChange={(type) => upT({ type })} options={Object.keys(typeLabel).map((value) => ({ value, label: typeName(value) }))} /></Field>
            </div>
            {et.type === 's3' && (
              <div className="grid grid-cols-2 gap-3">
                <Field label={t('Endpoint')} className="col-span-2"><Input value={et.endpoint} onChange={(e) => upT({ endpoint: e.target.value.trim() })} placeholder="https://s3.eu-central-1.wasabisys.com" /></Field>
                <Field label={t('Bucket')}><Input value={et.bucket} onChange={(e) => upT({ bucket: e.target.value.trim() })} /></Field>
                <Field label={t('Region')}><Input value={et.region} onChange={(e) => upT({ region: e.target.value.trim() })} placeholder="us-east-1" /></Field>
                <Field label={t('Access key')}><Input value={et.username} onChange={(e) => upT({ username: e.target.value.trim() })} /></Field>
                <Field label={t('Secret key')}><Input type="password" value={et.password} autoComplete="new-password" onChange={(e) => upT({ password: e.target.value })} /></Field>
              </div>
            )}
            {et.type !== 'local' && et.type !== 's3' && (
              <div className="grid grid-cols-3 gap-3">
                <Field label={t('Host')} className="col-span-2"><Input value={et.host} onChange={(e) => upT({ host: e.target.value.trim() })} /></Field>
                <Field label={t('Port')}><Input type="number" value={et.port || ''} placeholder={et.type === 'scp' || et.type === 'sftp' ? '22' : et.type === 'ftps-implicit' ? '990' : '21'} onChange={(e) => upT({ port: Number(e.target.value) })} /></Field>
                <Field label={t('User')}><Input value={et.username} onChange={(e) => upT({ username: e.target.value })} /></Field>
                <Field label={(et.type === 'scp' || et.type === 'sftp') && et.private_key ? t('Key passphrase') : t('Password')} className="col-span-2"><Input type="password" value={et.password} autoComplete="new-password" onChange={(e) => upT({ password: e.target.value })} /></Field>
              </div>
            )}
            <Field label={et.type === 'local' ? t('Folder on this server') : et.type === 's3' ? t('Folder in the bucket') : t('Folder on the server')}><Input value={et.path} onChange={(e) => upT({ path: e.target.value })} /></Field>
            {(et.type === 'scp' || et.type === 'sftp') && <>
              <Field label={t('Private key (optional)')} hint={t('OpenSSH format; ed25519 and RSA are supported')}><textarea rows={4} className="input" value={et.private_key} onChange={(e) => upT({ private_key: e.target.value })} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" /></Field>
              <Field label={t('Host key fingerprint (recommended)')} hint={t('E.g. {example}', { example: 'SHA256:abc…  (ssh-keyscan host | ssh-keygen -lf -)' })}><Input value={et.host_key} onChange={(e) => upT({ host_key: e.target.value.trim() })} /></Field>
            </>}
            {(et.type.startsWith('ftps') || et.type === 's3') && <Toggle checked={et.skip_verify} onChange={(skip_verify) => upT({ skip_verify })} label={t('Skip certificate verification')} />}
            <Toggle checked={et.enabled} onChange={(enabled) => upT({ enabled })} label={t('Enabled')} />
          </div>
        )}
      </Modal>

      <RestoreModal open={restoreOpen} onClose={() => setRestoreOpen(false)} targets={s.targets} />
    </>
  )
}

function RestoreModal({ open, onClose, targets }: { open: boolean; onClose: () => void; targets: Target[] }) {
  const toast = useToast()
  const [target, setTarget] = useState('local')
  const [list, setList] = useState<Entry[]>()
  const [err, setErr] = useState<string>()
  const [sel, setSel] = useState<string>()
  const [opts, setOpts] = useState({ config: true, logs: true })
  const [passphrase, setPassphrase] = useState('')
  useEffect(() => {
    if (!open) return
    setList(undefined); setErr(undefined); setSel(undefined)
    api.get<Entry[]>('/backup/list?target=' + target).then(setList).catch((e) => setErr(e.message))
  }, [open, target])
  const go = async () => {
    const e = list?.find((x) => x.name === sel)
    if (!e) return
    const warn = [opts.logs && t('ALL current log data will be replaced.'), opts.config && t('The configuration will be replaced and the service restarted.')].filter(Boolean).join(' ')
    if (!confirm(t('Restore from {name}?', { name: e.name }) + '\n\n' + warn)) return
    try { await api.post('/backup/restore', { target, name: e.name, ...opts, passphrase }); toast('ok', t('Restore started')); onClose() } catch (er) { toast('err', (er as Error).message) }
  }
  return (
    <Modal open={open} onClose={onClose} title={t('Restore from backup')} wide
      footer={<><Button onClick={onClose}>{t('Cancel')}</Button><Button variant="danger" disabled={!sel || (!opts.config && !opts.logs)} onClick={go} icon={<RotateCcw className="size-4" />}>{t('Restore')}</Button></>}>
      <div className="space-y-4">
        <Field label={t('Source')}><Select value={target} onChange={setTarget} options={[{ value: 'local', label: t('This server') }, ...targets.map((x) => ({ value: x.id, label: `${x.name || x.host} (${typeName(x.type)})` }))]} /></Field>
        {err && <div className="rounded-xl bg-red-500/10 px-4 py-3 text-sm text-red-500 ring-1 ring-red-500/30">{err}</div>}
        {!list && !err ? <div className="grid place-items-center py-10"><Spinner /></div> : list && (
          <div className="max-h-72 overflow-y-auto rounded-xl ring-1 ring-[var(--border)] scroll-thin">
            {!list.length ? <p className="py-8 text-center text-sm text-muted">{t('No backups found.')}</p> : list.map((e) => (
              <label key={e.name} className={'flex cursor-pointer items-center gap-3 border-b border-app/60 px-4 py-2.5 text-sm hover:bg-[var(--panel-2)] ' + (sel === e.name ? 'bg-indigo-500/10' : '')}>
                <input type="radio" checked={sel === e.name} onChange={() => setSel(e.name)} />
                <span className="flex-1">{fmtTime(e.time + 'Z')}</span>
                {e.name.endsWith('.age') && <span title={t('Encrypted')}><KeyRound className="size-3.5 text-amber-500" /></span>}
                {e.kind === 'full' ? <Badge className="bg-indigo-500/15 text-indigo-400 ring-indigo-500/30">{t('Full')}</Badge> : <Badge>{t('Incremental')}</Badge>}
                <span className="w-20 text-right tabular-nums text-muted">{fmtBytes(e.size)}</span>
              </label>
            ))}
          </div>
        )}
        {list?.find((x) => x.name === sel)?.name.endsWith('.age') && (
          <Field label={t('Passphrase')} hint={t('This backup is encrypted. Leave empty to use the passphrase from the settings.')}><Input type="password" autoComplete="off" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} /></Field>
        )}
        <p className="text-xs text-muted">{t('Restoring an incremental backup automatically restores its full backup and all incremental backups in between.')}</p>
        <div className="grid gap-3 md:grid-cols-2">
          <div className="panel-2 rounded-xl p-3"><Toggle checked={opts.config} onChange={(config) => setOpts({ ...opts, config })} label={t('Configuration')} description={t('Users, settings, categories')} /></div>
          <div className="panel-2 rounded-xl p-3"><Toggle checked={opts.logs} onChange={(logs) => setOpts({ ...opts, logs })} label={t('Log data')} description={t('Replaces all current logs')} /></div>
        </div>
      </div>
    </Modal>
  )
}
