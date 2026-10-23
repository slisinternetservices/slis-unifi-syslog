import { useEffect, useState } from 'react'
import { HardDrive, Database, PlugZap, Save, Pencil, Snowflake, Server, Network, Lock, RotateCcw, AlertTriangle } from 'lucide-react'
import { api } from '../api'
import { Badge, Button, Card, Field, Input, Modal, PageHeader, Select, Spinner, Stat, Toggle, useAsync, useToast } from '../components/ui'
import { fmtBytes, fmtNum, fmtTime } from '../lib/format'
import { t } from '../i18n'

interface CH { addr: string; addrs: string[]; http_url: string; database: string; username: string; password: string; secure: boolean; skip_verify: boolean; ca_cert: string; cluster: string }
interface Cold { enabled: boolean; mode: string; after_days: number; endpoint: string; bucket: string; prefix: string; region: string; access_key: string; secret_key: string; cache_gb: number; policy: string; volume: string }
interface Job { running: boolean; step: string; copied: number; total: number; error: string; finished: string }
interface DiskUse { disk: string; type: string; rows: number; bytes: number; oldest: string; newest: string }
interface Policy { name: string; volumes: { name: string; disks: string[] }[] }
interface StorageResp {
  clickhouse: CH; node_override: boolean; local: boolean; managed_available: boolean; cold: Cold; job: Job; retention_days: number
  storage?: { rows: number; bytes_on_disk: number; bytes_uncompressed: number; oldest: string; newest: string; clickhouse_version: string }
  disks?: DiskUse[]; policies?: Policy[]; policy?: string
}

const stepLabel = (s: string) => ({ connecting: t('Connecting'), 'copying logs': t('Copying logs'), restarting: t('Restarting the service') } as Record<string, string>)[s] ?? s
const zeroDate = (s?: string) => !s || s.startsWith('0001') || s.startsWith('1970')

export default function Storage() {
  const toast = useToast()
  const st = useAsync(() => api.get<StorageResp>('/settings/storage'))
  const [edit, setEdit] = useState<CH>()
  const [cold, setCold] = useState<Cold>()
  const [saving, setSaving] = useState(false)
  const d = st.data

  useEffect(() => { if (d) setCold(d.cold) }, [d])
  // Follow a running switch; the service restarts at the end of it.
  useEffect(() => {
    if (!d?.job.running && d?.job.step !== 'restarting') return
    const id = setInterval(() => {
      api.get<StorageResp>('/settings/storage').then((x) => st.setData(x)).catch(() => {})
    }, 2000)
    return () => clearInterval(id)
  }, [d?.job.running, d?.job.step]) // eslint-disable-line react-hooks/exhaustive-deps

  if (!d || !cold) return <div className="grid place-items-center py-24"><Spinner /></div>
  const ch = d.clickhouse
  const uc = (p: Partial<Cold>) => setCold({ ...cold, ...p })
  const saveCold = async () => {
    setSaving(true)
    try {
      const r = await api.put<{ cold: Cold; restart_clickhouse: boolean }>('/settings/storage/cold', cold)
      setCold(r.cold)
      toast('ok', r.restart_clickhouse ? t('Saved. Restart ClickHouse to use the changed S3 connection (systemctl restart clickhouse-server).') : t('Cold storage saved'))
      st.reload()
    } catch (e) { toast('err', (e as Error).message) } finally { setSaving(false) }
  }
  const reset = async () => {
    if (!confirm(t('Go back to the ClickHouse connection from config.yaml? The service restarts.'))) return
    try { await api.put('/settings/storage/clickhouse', { reset: true }); toast('ok', t('Restarting the service')); setTimeout(() => location.reload(), 8000) } catch (e) { toast('err', (e as Error).message) }
  }
  const policy = d.policies?.find((p) => p.name === cold.policy)
  const s3Possible = d.local && d.managed_available
  const job = d.job

  return (
    <>
      <PageHeader title={t('Storage')} subtitle={t('Where log data is stored: local or external ClickHouse, cluster and cold storage')} icon={<HardDrive className="size-5" />} />

      {(job.running || job.step === 'restarting') && (
        <div className="mb-6 flex flex-wrap items-center gap-3 rounded-2xl bg-indigo-500/10 px-5 py-4 text-sm ring-1 ring-indigo-500/30">
          <Spinner className="size-4" /><span className="font-medium">{stepLabel(job.step)}</span>
          {job.total > 0 && <span className="text-muted">{t('{copied} of {total} rows', { copied: fmtNum(job.copied), total: fmtNum(job.total) })}</span>}
          {job.total > 0 && <div className="h-1.5 w-full overflow-hidden rounded-full bg-[var(--panel-2)]"><div className="h-full bg-gradient-to-r from-indigo-500 to-cyan-500" style={{ width: `${Math.min(100, (job.copied / job.total) * 100)}%` }} /></div>}
        </div>
      )}
      {!job.running && job.error && (
        <div className="mb-6 flex items-start gap-3 rounded-2xl bg-red-500/10 px-5 py-4 text-sm text-red-500 ring-1 ring-red-500/30">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" /><span>{t('The switch to the other ClickHouse failed; this server still uses the current one.')} {job.error}</span>
        </div>
      )}

      <div className="mb-6 grid grid-cols-2 gap-4 xl:grid-cols-4">
        <Stat label={t('Log rows')} value={fmtNum(d.storage?.rows)} icon={<Database className="size-4" />} />
        <Stat label={t('Size on disk')} value={fmtBytes(d.storage?.bytes_on_disk ?? 0)} sub={d.storage && t('{size} uncompressed', { size: fmtBytes(d.storage.bytes_uncompressed) })} icon={<HardDrive className="size-4" />} accent="#06b6d4" />
        <Stat label={t('Oldest log')} value={<span className="text-xl">{d.storage && !zeroDate(d.storage.oldest) ? fmtTime(d.storage.oldest) : '–'}</span>} sub={t('Retention {n} days', { n: d.retention_days })} accent="#a855f7" />
        <Stat label={t('ClickHouse')} value={<span className="text-xl">{d.storage?.clickhouse_version || '–'}</span>} sub={ch.cluster ? t('Cluster {name}', { name: ch.cluster }) : d.local ? t('On this server') : t('External server')} icon={<Server className="size-4" />} accent="#10b981" />
      </div>

      <div className="grid grid-cols-1 gap-6 xl:grid-cols-2">
        <Card title={t('Log database')} subtitle={t('The ClickHouse server or cluster that stores the logs')}
          actions={<>
            {d.node_override && <Button onClick={reset} icon={<RotateCcw className="size-4" />}>{t('Reset')}</Button>}
            <Button variant="primary" disabled={job.running} onClick={() => setEdit({ ...ch, addrs: ch.addrs ?? [] })} icon={<Pencil className="size-4" />}>{t('Change')}</Button>
          </>}>
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2.5 text-sm">
            <dt className="text-muted">{t('Type')}</dt>
            <dd className="flex flex-wrap gap-1.5">
              {d.local ? <Badge>{t('Local')}</Badge> : <Badge className="bg-cyan-500/15 text-cyan-400 ring-cyan-500/30">{t('External')}</Badge>}
              {ch.cluster && <Badge className="bg-indigo-500/15 text-indigo-400 ring-indigo-500/30"><Network className="size-3" />{t('Cluster {name}', { name: ch.cluster })}</Badge>}
              {ch.secure && <Badge className="bg-emerald-500/15 text-emerald-500 ring-emerald-500/30"><Lock className="size-3" />TLS</Badge>}
            </dd>
            <dt className="text-muted">{t('Servers')}</dt>
            <dd className="font-mono text-xs leading-6">{[ch.addr, ...(ch.addrs ?? [])].map((a) => <div key={a}>{a}</div>)}</dd>
            <dt className="text-muted">{t('HTTP URL')}</dt><dd className="font-mono text-xs leading-6">{ch.http_url}</dd>
            <dt className="text-muted">{t('Database')}</dt><dd className="font-mono text-xs leading-6">{ch.database}</dd>
            <dt className="text-muted">{t('User')}</dt><dd className="font-mono text-xs leading-6">{ch.username || 'default'}</dd>
            <dt className="text-muted">{t('Set in')}</dt><dd>{d.node_override ? t('This page (node.yaml)') : 'config.yaml'}</dd>
          </dl>
        </Card>

        <Card title={t('Cold storage')} subtitle={t('Move older logs to cheap S3 storage; they stay searchable')}
          actions={<Button variant="primary" loading={saving} onClick={saveCold} icon={<Save className="size-4" />}>{t('Save')}</Button>}>
          <div className="space-y-4">
            <Toggle checked={cold.enabled} onChange={(enabled) => uc({ enabled, mode: cold.mode || (s3Possible ? 's3' : 'policy') })} label={t('Cold storage')} description={t('ClickHouse moves whole partitions; searches read hot and cold data together.')} />
            {cold.enabled && <>
              <div className="grid grid-cols-2 gap-3">
                <Field label={t('Storage')}><Select value={cold.mode} onChange={(mode) => uc({ mode })} options={[
                  ...(s3Possible || cold.mode === 's3' ? [{ value: 's3', label: t('S3 bucket (MinIO, Wasabi, AWS)') }] : []),
                  { value: 'policy', label: t('Existing storage policy') },
                ]} /></Field>
                <Field label={t('Move after (days)')} hint={t('Must be shorter than the retention of {n} days', { n: d.retention_days })}><Input type="number" min={1} value={cold.after_days || ''} onChange={(e) => uc({ after_days: Number(e.target.value) })} /></Field>
              </div>
              {cold.mode === 's3' ? <>
                <div className="grid grid-cols-2 gap-3">
                  <Field label={t('Endpoint')} className="col-span-2"><Input value={cold.endpoint} onChange={(e) => uc({ endpoint: e.target.value.trim() })} placeholder="https://s3.eu-central-1.wasabisys.com" /></Field>
                  <Field label={t('Bucket')}><Input value={cold.bucket} onChange={(e) => uc({ bucket: e.target.value.trim() })} /></Field>
                  <Field label={t('Folder in the bucket')}><Input value={cold.prefix} onChange={(e) => uc({ prefix: e.target.value.trim() })} placeholder="unify-syslog/" /></Field>
                  <Field label={t('Region')}><Input value={cold.region} onChange={(e) => uc({ region: e.target.value.trim() })} placeholder="us-east-1" /></Field>
                  <Field label={t('Local cache (GB)')} hint={t('For cold data that is read')}><Input type="number" min={1} value={cold.cache_gb || ''} onChange={(e) => uc({ cache_gb: Number(e.target.value) })} /></Field>
                  <Field label={t('Access key')}><Input value={cold.access_key} onChange={(e) => uc({ access_key: e.target.value.trim() })} /></Field>
                  <Field label={t('Secret key')}><Input type="password" autoComplete="new-password" value={cold.secret_key} onChange={(e) => uc({ secret_key: e.target.value })} /></Field>
                </div>
                {!s3Possible && <p className="rounded-xl bg-amber-500/10 px-4 py-3 text-xs text-amber-500 ring-1 ring-amber-500/30">{d.local
                  ? t('This installation cannot configure ClickHouse yet. Run the installer once more as root to enable it.')
                  : t('An S3 disk can only be configured on the local ClickHouse. For an external server or cluster, add the disk there and choose the existing storage policy.')}</p>}
              </> : (
                <div className="grid grid-cols-2 gap-3">
                  <Field label={t('Storage policy')}><Select value={cold.policy} onChange={(p) => uc({ policy: p, volume: '' })} options={[{ value: '', label: '–' }, ...(d.policies ?? []).filter((p) => p.volumes.length > 1).map((p) => ({ value: p.name, label: p.name }))]} /></Field>
                  <Field label={t('Volume for cold data')}><Select value={cold.volume} onChange={(volume) => uc({ volume })} options={[{ value: '', label: '–' }, ...(policy?.volumes.slice(1) ?? []).map((v) => ({ value: v.name, label: `${v.name} (${v.disks.join(', ')})` }))]} /></Field>
                  <p className="col-span-2 text-xs text-muted">{t('Only policies with more than one volume are listed. deploy/install-clickhouse.sh can create one (uss_tiered, volume cold).')}</p>
                </div>
              )}
            </>}
            {d.policy && <p className="text-xs text-muted">{t('Current storage policy of the logs table: {name}', { name: d.policy })}</p>}
          </div>
        </Card>

        <Card title={t('Data per disk')} className="xl:col-span-2" pad={false}>
          {!d.disks?.length ? <p className="py-8 text-center text-sm text-muted">{t('No data yet.')}</p> : (
            <div className="overflow-x-auto scroll-thin">
              <table className="w-full text-sm">
                <thead className="text-left text-[11px] uppercase tracking-wide text-muted"><tr className="border-b border-app">
                  <th className="px-5 py-3 font-medium">{t('Disk')}</th><th className="px-3 py-3 font-medium">{t('Type')}</th><th className="px-3 py-3 text-right font-medium">{t('Log rows')}</th>
                  <th className="px-3 py-3 text-right font-medium">{t('Size')}</th><th className="px-3 py-3 font-medium">{t('Oldest')}</th><th className="px-5 py-3 font-medium">{t('Newest')}</th>
                </tr></thead>
                <tbody>{d.disks.map((x) => (
                  <tr key={x.disk} className="border-b border-app/60">
                    <td className="px-5 py-2.5"><span className="flex items-center gap-2 font-medium">{x.type.toLowerCase().includes('s3') || x.disk === 'uss_cold' ? <Snowflake className="size-4 text-cyan-400" /> : <HardDrive className="size-4 text-muted" />}{x.disk}</span></td>
                    <td className="px-3 py-2.5 text-muted">{x.type}</td>
                    <td className="px-3 py-2.5 text-right tabular-nums">{fmtNum(x.rows)}</td>
                    <td className="px-3 py-2.5 text-right tabular-nums">{fmtBytes(x.bytes)}</td>
                    <td className="px-3 py-2.5 whitespace-nowrap">{zeroDate(x.oldest) ? '–' : fmtTime(x.oldest)}</td>
                    <td className="px-5 py-2.5 whitespace-nowrap">{zeroDate(x.newest) ? '–' : fmtTime(x.newest)}</td>
                  </tr>
                ))}</tbody>
              </table>
            </div>
          )}
        </Card>
      </div>

      {edit && <ConnectionModal ch={edit} cold={d.cold.enabled} onClose={() => setEdit(undefined)} onStarted={() => { setEdit(undefined); st.reload() }} />}
    </>
  )
}

function ConnectionModal({ ch, cold, onClose, onStarted }: { ch: CH; cold: boolean; onClose: () => void; onStarted: () => void }) {
  const toast = useToast()
  const [c, setC] = useState<CH>(ch)
  const [extra, setExtra] = useState((ch.addrs ?? []).join('\n'))
  const [migrate, setMigrate] = useState(true)
  const [testing, setTesting] = useState(false)
  const [tested, setTested] = useState<string>()
  const u = (p: Partial<CH>) => { setC({ ...c, ...p }); setTested(undefined) }
  const body = () => ({ ...c, addrs: extra.split(/[\s,]+/).filter(Boolean) })
  const test = async () => {
    setTesting(true)
    try {
      const r = await api.post<{ version: string; rows: number; nodes?: number }>('/storage/clickhouse/test', body())
      const msg = t('Connected to ClickHouse {version}', { version: r.version }) + (r.nodes ? ' · ' + t('{n} nodes in the cluster', { n: r.nodes }) : '') + (r.rows ? ' · ' + t('{n} log rows already there', { n: fmtNum(r.rows) }) : '')
      setTested(msg); toast('ok', msg)
    } catch (e) { toast('err', (e as Error).message) } finally { setTesting(false) }
  }
  const save = async () => {
    if (!confirm(t('Switch the log store? The service restarts when it is done.'))) return
    try { await api.put('/settings/storage/clickhouse', { clickhouse: body(), migrate }); toast('ok', t('Switch started')); onStarted() } catch (e) { toast('err', (e as Error).message) }
  }
  return (
    <Modal open onClose={onClose} wide title={t('ClickHouse connection')}
      footer={<><Button onClick={test} loading={testing} icon={<PlugZap className="size-4" />}>{t('Test connection')}</Button><Button variant="primary" disabled={!tested} onClick={save}>{t('Switch over')}</Button></>}>
      <div className="space-y-4">
        <div className="grid gap-3 md:grid-cols-2">
          <Field label={t('Server (native protocol)')} hint={t('host:9000, or host:9440 with TLS')}><Input value={c.addr} onChange={(e) => u({ addr: e.target.value.trim() })} placeholder="clickhouse.example.com:9000" /></Field>
          <Field label={t('HTTP URL')} hint={t('Used for backups and the Grafana access')}><Input value={c.http_url} onChange={(e) => u({ http_url: e.target.value.trim() })} placeholder="http://clickhouse.example.com:8123" /></Field>
          <Field label={t('More servers (optional)')} hint={t('Other replicas or cluster nodes, one per line; tried in order when the first is down')}>
            <textarea rows={3} className="input font-mono text-xs" value={extra} onChange={(e) => { setExtra(e.target.value); setTested(undefined) }} placeholder="ch2.example.com:9000" />
          </Field>
          <div className="space-y-3">
            <Field label={t('Cluster (optional)')} hint={t('Name from remote_servers. Logs are then stored in replicated tables on every node.')}><Input value={c.cluster} onChange={(e) => u({ cluster: e.target.value.trim() })} placeholder="uss" /></Field>
            <Field label={t('Database')}><Input value={c.database} onChange={(e) => u({ database: e.target.value.trim() })} /></Field>
          </div>
          <Field label={t('User')}><Input value={c.username} onChange={(e) => u({ username: e.target.value })} /></Field>
          <Field label={t('Password')}><Input type="password" autoComplete="new-password" value={c.password} onChange={(e) => u({ password: e.target.value })} /></Field>
        </div>
        <div className="grid gap-3 md:grid-cols-2">
          <div className="panel-2 rounded-xl p-3"><Toggle checked={c.secure} onChange={(secure) => u({ secure })} label={t('TLS')} description={t('Encrypt the native connection')} /></div>
          {c.secure && <div className="panel-2 rounded-xl p-3"><Toggle checked={c.skip_verify} onChange={(skip_verify) => u({ skip_verify })} label={t('Skip certificate verification')} /></div>}
          {c.secure && !c.skip_verify && <Field label={t('CA certificate file (optional)')} className="md:col-span-2"><Input value={c.ca_cert} onChange={(e) => u({ ca_cert: e.target.value.trim() })} placeholder="/etc/unify-syslog/clickhouse-ca.pem" /></Field>}
        </div>
        <div className="panel-2 rounded-xl p-3"><Toggle checked={migrate} onChange={setMigrate} label={t('Copy the stored logs')} description={t('Copies all logs to the new server before switching. Off: start with the logs that are already there.')} /></div>
        {cold && <p className="rounded-xl bg-amber-500/10 px-4 py-3 text-xs text-amber-500 ring-1 ring-amber-500/30">{t('Cold storage is turned off when switching; set it up again for the new server.')}</p>}
        {tested ? <p className="rounded-xl bg-emerald-500/10 px-4 py-3 text-sm text-emerald-500 ring-1 ring-emerald-500/30">{tested}</p>
          : <p className="text-xs text-muted">{t('Test the connection before switching.')}</p>}
      </div>
    </Modal>
  )
}
