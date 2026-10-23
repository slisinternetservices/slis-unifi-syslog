import { useEffect, useState } from 'react'
import { RefreshCw, Download, CheckCircle2, Sparkles, ExternalLink, FlaskConical, ShieldCheck, History, RotateCcw, AlertTriangle, ArrowDownCircle } from 'lucide-react'
import { api } from '../api'
import { useAuth } from '../App'
import { Badge, Button, Modal, Spinner, cx, useToast } from './ui'
import { fmtBytes, fmtDuration, fmtNum, fmtTime } from '../lib/format'
import { t } from '../i18n'

interface InfoData {
  version: string; commit: string; build_date: string; repo: string; go: string; os: string; hostname: string; uptime_sec: number
  storage: { rows: number; bytes_on_disk: number; bytes_uncompressed: number; oldest: string; clickhouse_version: string }
  ingest: Record<string, number | string>
  syslog: { udp: string; tcp: string }
}
interface Check { current: string; latest: string; channel: Channel; available: boolean; prerelease: boolean; downgrade: boolean; notes: string; url: string; published: string }
type Channel = 'stable' | 'test'
interface Kept { version: string; kept_at: string; reason: 'update' | 'rollback' | 'previous'; has_snapshot: boolean }
interface Rollback { from: string; to: string; at: string; auto: boolean; reason?: string }
interface Versions { current: string; channel: Channel; kept: Kept[]; last_rollback: Rollback | null }

export default function InfoModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { user } = useAuth()
  const toast = useToast()
  const [info, setInfo] = useState<InfoData>()
  const [check, setCheck] = useState<Check>()
  const [checking, setChecking] = useState(false)
  const [updating, setUpdating] = useState<string>()
  const [versions, setVersions] = useState<Versions>()
  const [savingChannel, setSavingChannel] = useState(false)
  const admin = user.role === 'admin'

  useEffect(() => {
    if (!open) return
    api.get<InfoData>('/info').then(setInfo).catch(() => {})
    api.get<Versions>('/update/versions').then(setVersions).catch(() => {})
  }, [open])

  const doCheck = async () => {
    setChecking(true)
    try { setCheck(await api.get<Check>('/update/check')) } catch (e) { toast('err', (e as Error).message) } finally { setChecking(false) }
  }

  const setChannel = async (channel: Channel) => {
    if (!versions || channel === versions.channel) return
    setSavingChannel(true)
    try {
      await api.put('/settings/update', { channel })
      setVersions({ ...versions, channel })
      setCheck(undefined)
      toast('ok', channel === 'test' ? t('Switched to the test channel') : t('Switched to the production channel'))
      await doCheck()
    } catch (e) { toast('err', (e as Error).message) } finally { setSavingChannel(false) }
  }

  // Follows an update or rollback until the service is back on another version.
  const follow = (first: string, done: (v: string) => string) => {
    setUpdating(first)
    const from = info?.version
    const poll = setInterval(async () => {
      try {
        const s = await api.get<{ running: boolean; state: string; version: string }>('/update/status')
        if (s.version !== from) {
          clearInterval(poll)
          setUpdating(undefined)
          toast('ok', done(s.version))
          setTimeout(() => window.location.reload(), 800)
          return
        }
        if (s.state) setUpdating(s.state)
        if (/^(error|fout)/i.test(s.state)) { clearInterval(poll); toast('err', s.state); setUpdating(undefined) }
      } catch { setUpdating(t('Service restarting…')) }
    }, 1500)
  }

  const doUpdate = async () => {
    const q = check?.downgrade
      ? t('Switch to production version {version}? This is lower than the test version you are running now. The service will restart automatically afterwards.', { version: check.latest })
      : t('Update to version {version}? The service will restart automatically afterwards.', { version: check?.latest ?? '' })
    if (!confirm(q)) return
    try {
      await api.post('/update/apply')
      follow(t('Downloading update…'), (v) => t('Updated to {version}', { version: v }))
    } catch (e) { toast('err', (e as Error).message) }
  }

  const doRollback = async (k: Kept) => {
    const q = k.has_snapshot
      ? t('Go back to version {version}? Settings return to how they were when this version was replaced; changes made since then are lost. Logs are kept. The service will restart automatically afterwards.', { version: k.version })
      : t('Go back to version {version}? Logs and settings are kept. The service will restart automatically afterwards.', { version: k.version })
    if (!confirm(q)) return
    try {
      await api.post('/update/rollback', { version: k.version })
      follow(t('Rolling back to v{version}…', { version: k.version }), (v) => t('Rolled back to {version}', { version: v }))
    } catch (e) { toast('err', (e as Error).message) }
  }

  const lastRollback = versions?.last_rollback && versions.last_rollback.to === info?.version ? versions.last_rollback : undefined
  const reasons: Record<Kept['reason'], string> = {
    update: t('replaced by an update'),
    rollback: t('replaced by a rollback'),
    previous: t('previous installation'),
  }

  const row = (k: string, v: React.ReactNode) => (
    <div className="flex justify-between gap-4 py-2 text-sm"><span className="text-muted">{k}</span><span className="text-right font-medium">{v}</span></div>
  )

  return (
    <Modal open={open} onClose={onClose} title={t('Info & updates')}>
      {!info ? <div className="grid place-items-center py-10"><Spinner /></div> : (
        <div className="space-y-5">
          <div className="rounded-2xl bg-gradient-to-br from-indigo-500/15 to-cyan-500/10 p-5 ring-1 ring-indigo-500/20">
            <div className="text-xs uppercase tracking-wide text-muted">{t('Current version')}</div>
            <div className="mt-1 text-3xl font-semibold grad-text">v{info.version}</div>
            <div className="mt-1 text-xs text-muted">{t('commit {commit} · built {date}', { commit: info.commit.slice(0, 8), date: info.build_date })}</div>
          </div>

          {lastRollback && (
            <div className={cx('flex gap-2 rounded-xl px-4 py-3 text-sm ring-1', lastRollback.auto ? 'bg-amber-500/10 text-amber-500 ring-amber-500/30' : 'bg-indigo-500/10 text-indigo-400 ring-indigo-500/20')}>
              {lastRollback.auto ? <AlertTriangle className="mt-0.5 size-4 shrink-0" /> : <RotateCcw className="mt-0.5 size-4 shrink-0" />}
              <span>{lastRollback.auto
                ? t('Version {from} did not start properly, so the server went back to version {to} automatically ({time}).', { from: lastRollback.from, to: lastRollback.to, time: fmtTime(lastRollback.at) })
                : t('Rolled back from version {from} to {to} ({time}).', { from: lastRollback.from, to: lastRollback.to, time: fmtTime(lastRollback.at) })}</span>
            </div>
          )}

          {versions && (
            <div>
              <div className="mb-2 text-xs font-medium uppercase tracking-wide text-muted">{t('Release channel')}</div>
              <div className="grid grid-cols-2 gap-2" role="radiogroup" aria-label={t('Release channel')}>
                {([
                  { id: 'stable', icon: <ShieldCheck className="size-4" />, label: t('Production'), text: t('Stable releases only') },
                  { id: 'test', icon: <FlaskConical className="size-4" />, label: t('Test'), text: t('Also test versions, to try new features early') },
                ] as const).map((c) => {
                  const on = versions.channel === c.id
                  return (
                    <button key={c.id} role="radio" aria-checked={on} disabled={!admin || savingChannel} onClick={() => setChannel(c.id)}
                      className={cx('rounded-xl p-3 text-left ring-1 transition disabled:cursor-default', admin && 'cursor-pointer',
                        on ? 'bg-indigo-500/10 ring-indigo-500/50' : 'ring-[var(--border)] hover:bg-[var(--panel-2)]', !admin && !on && 'opacity-60')}>
                      <div className={cx('flex items-center gap-2 text-sm font-medium', on && 'text-indigo-400')}>{c.icon}{c.label}</div>
                      <div className="mt-0.5 text-xs text-muted">{c.text}</div>
                    </button>
                  )
                })}
              </div>
            </div>
          )}

          <div className="space-y-3">
            <div className="flex gap-2">
              <Button onClick={doCheck} loading={checking} icon={<RefreshCw className="size-4" />} className="flex-1">{t('Check for updates')}</Button>
              {check?.available && admin && (
                <Button variant="primary" onClick={doUpdate} loading={!!updating} icon={check.downgrade ? <ArrowDownCircle className="size-4" /> : <Download className="size-4" />} className="flex-1">
                  {check.downgrade ? t('Switch to v{version}', { version: check.latest }) : t('Update to v{version}', { version: check.latest })}
                </Button>
              )}
            </div>
            {updating && <div className="rounded-xl bg-indigo-500/10 px-4 py-3 text-sm text-indigo-400 ring-1 ring-indigo-500/20 flex items-center gap-2"><Spinner className="size-4" />{updating}</div>}
            {check && !check.available && <div className="flex items-center gap-2 rounded-xl bg-emerald-500/10 px-4 py-3 text-sm text-emerald-500 ring-1 ring-emerald-500/30"><CheckCircle2 className="size-4" /> {t('You are running the latest version (v{version}).', { version: check.current })}</div>}
            {check?.available && (
              <div className="rounded-xl bg-amber-500/10 px-4 py-3 text-sm ring-1 ring-amber-500/30">
                <div className="flex flex-wrap items-center gap-2 font-medium text-amber-500">
                  <Sparkles className="size-4" />
                  {check.downgrade ? t('Production version {version} is available', { version: check.latest }) : t('Version {version} is available', { version: check.latest })}
                  {check.prerelease && <Badge color="#f59e0b">{t('Test version')}</Badge>}
                </div>
                {check.downgrade && <p className="mt-1 text-xs text-muted">{t('You are running a test version. Switching to production installs the newest stable release, even though its version number is lower.')}</p>}
                {check.notes && <pre className="mt-2 max-h-40 overflow-auto whitespace-pre-wrap text-xs text-muted scroll-thin">{check.notes}</pre>}
                <a href={check.url} target="_blank" rel="noreferrer" className="mt-2 inline-flex items-center gap-1 text-xs text-indigo-400 hover:underline">{t('Release notes')} <ExternalLink className="size-3" /></a>
                {!admin && <p className="mt-2 text-xs text-muted">{t('Only an admin can install the update.')}</p>}
              </div>
            )}
          </div>

          {versions && (
            <div>
              <div className="mb-2 flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted"><History className="size-3.5" />{t('Previous versions')}</div>
              {versions.kept.length === 0 ? (
                <p className="rounded-xl px-4 py-3 text-sm text-muted ring-1 ring-[var(--border)]">{t('No previous versions yet. After the next update the current version is kept here, so you can go back to it.')}</p>
              ) : (
                <div className="divide-y divide-[var(--border)] rounded-xl ring-1 ring-[var(--border)]">
                  {versions.kept.map((k, i) => (
                    <div key={k.version} className="flex items-center gap-3 px-4 py-2.5 text-sm">
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-2 font-medium">
                          v{k.version}
                          {i === 0 && <Badge color="#6366f1">{t('Previous')}</Badge>}
                          {/-/.test(k.version) && <Badge color="#f59e0b">{t('Test version')}</Badge>}
                        </div>
                        <div className="text-xs text-muted">{t('Kept {time}, {reason}', { time: fmtTime(k.kept_at), reason: reasons[k.reason] ?? k.reason })}{k.has_snapshot ? ' · ' + t('with settings snapshot') : ''}</div>
                      </div>
                      {admin && <Button onClick={() => doRollback(k)} disabled={!!updating} icon={<RotateCcw className="size-4" />}>{t('Roll back')}</Button>}
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}

          <div className="divide-y divide-[var(--border)]">
            {row(t('Server'), info.hostname)}
            {row(t('Platform'), `${info.os} · ${info.go}`)}
            {row(t('Uptime'), fmtDuration(info.uptime_sec))}
            {row(t('Syslog ports'), `UDP ${info.syslog.udp} · TCP ${info.syslog.tcp}`)}
            {row(t('Messages received'), fmtNum(Number(info.ingest.parsed)))}
            {row(t('Stored rows'), fmtNum(info.storage.rows))}
            {row(t('Storage (compressed)'), t('{used} of {total}', { used: fmtBytes(info.storage.bytes_on_disk), total: fmtBytes(info.storage.bytes_uncompressed) }))}
            {info.storage.rows > 0 && row(t('Oldest log'), fmtTime(info.storage.oldest))}
            {row('ClickHouse', info.storage.clickhouse_version)}
            {row(t('Source code'), <a className="text-indigo-400 hover:underline" href={`https://github.com/${info.repo}`} target="_blank" rel="noreferrer">github.com/{info.repo}</a>)}
          </div>
        </div>
      )}
    </Modal>
  )
}
