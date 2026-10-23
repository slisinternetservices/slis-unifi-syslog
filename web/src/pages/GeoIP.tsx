import { useEffect, useState } from 'react'
import { Globe, Save, Download, CheckCircle2, AlertCircle } from 'lucide-react'
import { api } from '../api'
import { Button, Card, Field, Input, PageHeader, Select, Spinner, Toggle, useToast } from '../components/ui'
import { fmtAgo, fmtBytes } from '../lib/format'
import { locale, t } from '../i18n'

interface Settings { enabled: boolean; provider: 'dbip' | 'maxmind'; account_id: string; license_key: string; auto_update: boolean }
interface DBMeta { type: string; built: string; updated: string; size: number }
interface Status { loaded: boolean; databases: DBMeta[]; updating: boolean; last_check: string; last_error: string }
interface Resp { settings: Settings; status?: Status }

const zero = (s: string) => !s || s.startsWith('0001-')

export default function GeoIP() {
  const toast = useToast()
  const [r, setR] = useState<Resp>()
  const [busy, setBusy] = useState(false)
  const [updating, setUpdating] = useState(false)
  const load = () => api.get<Resp>('/settings/geoip').then(setR)
  useEffect(() => { load() }, [])
  // Poll while a background download runs.
  useEffect(() => {
    if (!r?.status?.updating) return
    const id = setInterval(load, 3000)
    return () => clearInterval(id)
  }, [r?.status?.updating])
  if (!r) return <div className="grid place-items-center py-24"><Spinner /></div>
  const s = r.settings
  const up = (p: Partial<Settings>) => setR({ ...r, settings: { ...s, ...p } })
  const save = async () => {
    setBusy(true)
    try { setR(await api.put<Resp>('/settings/geoip', s)); toast('ok', t('Saved')) } catch (e) { toast('err', (e as Error).message) } finally { setBusy(false) }
  }
  const update = async () => {
    setUpdating(true)
    try { setR(await api.post<Resp>('/geoip/update')); toast('ok', t('GeoIP databases updated')) } catch (e) { toast('err', (e as Error).message); load() } finally { setUpdating(false) }
  }
  const st = r.status
  return (
    <>
      <PageHeader title={t('GeoIP')} subtitle={t('Country and network (AS number) for external IP addresses, with a map on the dashboards')} icon={<Globe className="size-5" />}
        actions={<Button variant="primary" loading={busy} onClick={save} icon={<Save className="size-4" />}>{t('Save')}</Button>} />
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-3">
        <Card className="xl:col-span-2" title={t('Database')}>
          <div className="space-y-5">
            <Toggle checked={s.enabled} onChange={(enabled) => up({ enabled })} label={t('Look up external IP addresses')} description={t('New log lines get the country and AS number of public source and destination addresses. Existing logs are not changed.')} />
            <Field label={t('Provider')}>
              <Select value={s.provider} onChange={(provider) => up({ provider: provider as Settings['provider'] })} options={[
                { value: 'dbip', label: t('DB-IP Lite (free, no account)') },
                { value: 'maxmind', label: t('MaxMind GeoLite2 (free account and license key)') },
              ]} />
            </Field>
            {s.provider === 'maxmind' && (
              <div className="grid gap-4 md:grid-cols-2">
                <Field label={t('Account ID')}><Input value={s.account_id} onChange={(e) => up({ account_id: e.target.value.trim() })} /></Field>
                <Field label={t('License key')}><Input type="password" autoComplete="new-password" value={s.license_key} onChange={(e) => up({ license_key: e.target.value.trim() })} /></Field>
              </div>
            )}
            <Toggle checked={s.auto_update} onChange={(auto_update) => up({ auto_update })} label={t('Update automatically')} description={s.provider === 'maxmind' ? t('Checked daily, refreshed weekly') : t('DB-IP publishes a new version every month')} />
            <p className="text-xs text-muted">
              {s.provider === 'maxmind'
                ? t('This product includes GeoLite2 data created by MaxMind, available from https://www.maxmind.com.')
                : t('IP geolocation by DB-IP (https://db-ip.com), licensed under CC BY 4.0.')}
            </p>
          </div>
        </Card>
        <Card title={t('Status')} actions={<Button onClick={update} loading={updating || st?.updating} disabled={!s.enabled} icon={<Download className="size-4" />}>{t('Update now')}</Button>}>
          {!st ? <p className="text-sm text-muted">{t('Not available')}</p> : (
            <div className="space-y-4 text-sm">
              <div className="flex items-center gap-2">
                {st.loaded ? <CheckCircle2 className="size-4 text-emerald-500" /> : <AlertCircle className="size-4 text-amber-500" />}
                <span>{st.loaded ? t('Active') : s.enabled ? t('No database loaded yet') : t('Off')}</span>
              </div>
              {st.databases.map((d) => (
                <div key={d.type} className="rounded-xl bg-[var(--panel-2)] p-3 text-xs">
                  <div className="font-medium">{d.type}</div>
                  <div className="text-muted">{t('Built {date}', { date: new Date(d.built).toLocaleDateString(locale()) })} · {fmtBytes(d.size)}</div>
                  <div className="text-muted">{t('Downloaded {ago}', { ago: fmtAgo(d.updated) })}</div>
                </div>
              ))}
              {!zero(st.last_check) && <div className="text-xs text-muted">{t('Last check {ago}', { ago: fmtAgo(st.last_check) })}</div>}
              {st.last_error && <div className="text-xs text-red-500">{st.last_error}</div>}
            </div>
          )}
        </Card>
      </div>
    </>
  )
}
