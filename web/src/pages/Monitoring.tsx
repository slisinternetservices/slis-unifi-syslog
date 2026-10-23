import { useEffect, useState } from 'react'
import { Gauge, Save, Download, BarChart3, Flame } from 'lucide-react'
import { Link } from 'react-router-dom'
import { api } from '../api'
import { Button, Card, CopyBox, Field, PageHeader, Spinner, Toggle, useToast } from '../components/ui'
import { t } from '../i18n'

interface Settings { metrics_enabled: boolean; anonymous_cidrs: string[]; grafana_enabled: boolean }
interface Resp { settings: Settings; base_url: string }

function DownloadLink({ path, label }: { path: string; label: string }) {
  return (
    <a href={`/api/monitoring/content/${path}`} download={path.split('/').pop()}
      className="flex items-center gap-3 rounded-xl px-3 py-2.5 text-sm ring-1 ring-[var(--border)] transition hover:bg-[var(--panel-2)] hover:ring-indigo-400/60">
      <Download className="size-4 text-indigo-400" /><span className="flex-1">{label}</span><span className="font-mono text-xs text-muted">{path.split('/').pop()}</span>
    </a>
  )
}

function Step({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  return (
    <li className="flex gap-3">
      <span className="grid size-6 shrink-0 place-items-center rounded-full bg-indigo-500/15 text-xs font-semibold text-indigo-400">{n}</span>
      <div className="min-w-0 flex-1 space-y-2"><div className="text-sm font-medium">{title}</div>{children}</div>
    </li>
  )
}

export default function Monitoring() {
  const toast = useToast()
  const [d, setD] = useState<Resp>()
  const [cidrs, setCidrs] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => { api.get<Resp>('/settings/monitoring').then((r) => { setD(r); setCidrs((r.settings.anonymous_cidrs ?? []).join('\n')) }) }, [])
  if (!d) return <div className="grid place-items-center py-24"><Spinner /></div>
  const s = d.settings
  const up = (p: Partial<Settings>) => setD({ ...d, settings: { ...s, ...p } })
  const save = async () => {
    setBusy(true)
    try {
      const r = await api.put<Resp>('/settings/monitoring', { ...s, anonymous_cidrs: cidrs.split(/[\s,]+/).filter(Boolean) })
      setD(r); setCidrs((r.settings.anonymous_cidrs ?? []).join('\n')); toast('ok', t('Monitoring settings saved'))
    } catch (e) { toast('err', (e as Error).message) } finally { setBusy(false) }
  }
  const host = d.base_url.replace(/^https?:\/\//, '')

  return (
    <>
      <PageHeader title={t('Metrics & Grafana')} subtitle={t('Prometheus metrics and Grafana dashboards for this server')} icon={<Gauge className="size-5" />}
        actions={<Button variant="primary" loading={busy} onClick={save} icon={<Save className="size-4" />}>{t('Save')}</Button>} />
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-2">
        <Card title={t('Access settings')} className="xl:col-span-2">
          <div className="grid gap-6 lg:grid-cols-3">
            <Toggle checked={s.metrics_enabled} onChange={(metrics_enabled) => up({ metrics_enabled })} label={t('Prometheus metrics')} description={t('Serve /metrics; reading it needs an API token')} />
            <Toggle checked={s.grafana_enabled} onChange={(grafana_enabled) => up({ grafana_enabled })} label={t('Grafana access to the log store')} description={t('Read-only ClickHouse access at /clickhouse for the Grafana ClickHouse data source')} />
            <Field label={t('Without a token from (optional)')} hint={t('Addresses or networks that may read /metrics without a token, one per line, e.g. your Prometheus server')}>
              <textarea rows={3} className="input font-mono text-xs" value={cidrs} onChange={(e) => setCidrs(e.target.value)} placeholder="10.0.0.5/32" />
            </Field>
          </div>
        </Card>

        <Card title={<span className="flex items-center gap-2"><Flame className="size-4 text-orange-500" />Prometheus</span>} subtitle={t('Ingest, queue, ClickHouse, disks, forwarding, probes, backups and high availability')}>
          <ol className="space-y-5">
            <Step n={1} title={t('Create an API token')}>
              <p className="text-xs text-muted">{t('Under')} <Link to="/account" className="text-indigo-400 hover:underline">{t('My account')}</Link> · {t('API tokens')}. {t('Read-only is enough.')}</p>
            </Step>
            <Step n={2} title={t('Test the endpoint')}>
              <CopyBox multiline text={`curl -k -H "Authorization: Bearer uss_…" ${d.base_url}/metrics`} />
            </Step>
            <Step n={3} title={t('Add the scrape job and alert rules')}>
              <div className="space-y-2">
                <DownloadLink path="prometheus/scrape-config.yml" label={t('Scrape configuration')} />
                <DownloadLink path="prometheus/alert-rules.yml" label={t('Alert rules')} />
              </div>
              <p className="text-xs text-muted">{t('With high availability, scrape both nodes, not the virtual IP.')}</p>
            </Step>
          </ol>
        </Card>

        <Card title={<span className="flex items-center gap-2"><BarChart3 className="size-4 text-amber-500" />Grafana</span>} subtitle={t('Ready-made dashboards for health and logs')}>
          <ol className="space-y-5">
            <Step n={1} title={t('Install the ClickHouse plugin')}>
              <CopyBox text="grafana-cli plugins install grafana-clickhouse-datasource" />
            </Step>
            <Step n={2} title={t('Add the data sources')}>
              <DownloadLink path="grafana/datasources.yml" label={t('Data sources (provisioning)')} />
              <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs">
                <dt className="text-muted">{t('Server')}</dt><dd className="font-mono">{host}</dd>
                <dt className="text-muted">{t('Protocol')}</dt><dd className="font-mono">HTTP · {t('path')} /clickhouse · TLS</dd>
                <dt className="text-muted">{t('User')}</dt><dd className="font-mono">grafana</dd>
                <dt className="text-muted">{t('Password')}</dt><dd>{t('A read-only API token of an administrator')}</dd>
              </dl>
              {!s.grafana_enabled && <p className="text-xs text-amber-500">{t('Turn on Grafana access to the log store above first.')}</p>}
            </Step>
            <Step n={3} title={t('Import the dashboards')}>
              <div className="space-y-2">
                <DownloadLink path="grafana/unifi-syslog-health.json" label={t('Server health (Prometheus)')} />
                <DownloadLink path="grafana/unifi-syslog-logs.json" label={t('Logs (ClickHouse)')} />
              </div>
            </Step>
          </ol>
        </Card>
      </div>
    </>
  )
}
