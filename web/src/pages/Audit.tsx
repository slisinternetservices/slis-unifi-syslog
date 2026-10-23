import { useEffect, useState } from 'react'
import { History, Download, Search } from 'lucide-react'
import { api } from '../api'
import { Badge, Button, Card, Empty, Input, PageHeader, Select, Spinner, cx } from '../components/ui'
import { fmtTime } from '../lib/format'
import { t } from '../i18n'

interface Entry { id: number; ts: number; user: string; action: string; detail: string; ip: string; kind: string; via: string }
interface Page { rows: Entry[]; users: string[]; next?: number }

const kinds = () => [
  { value: '', label: t('Everything') },
  { value: 'auth', label: t('Sign-ins') },
  { value: 'view', label: t('Viewed') },
  { value: 'change', label: t('Changed') },
  { value: 'export', label: t('Exported') },
  { value: 'restore', label: t('Restored') },
]

const kindClass: Record<string, string> = {
  auth: 'bg-sky-500/15 text-sky-500 ring-sky-500/30',
  view: '',
  change: 'bg-indigo-500/15 text-indigo-400 ring-indigo-500/30',
  export: 'bg-amber-500/15 text-amber-500 ring-amber-500/30',
  restore: 'bg-red-500/15 text-red-500 ring-red-500/30',
}

export default function Audit() {
  const [filter, setFilter] = useState({ kind: '', user: '', q: '' })
  const [text, setText] = useState('')
  const [page, setPage] = useState<Page>()
  const [more, setMore] = useState(false)
  const params = (before?: number) => {
    const p = new URLSearchParams()
    Object.entries(filter).forEach(([k, v]) => v && p.set(k, v))
    if (before) p.set('before', String(before))
    return p.toString()
  }
  useEffect(() => {
    setPage(undefined)
    api.get<Page>('/audit?' + params()).then(setPage).catch(() => setPage({ rows: [], users: [] }))
  }, [filter])
  useEffect(() => {
    const id = setTimeout(() => setFilter((f) => (f.q === text ? f : { ...f, q: text })), 350)
    return () => clearTimeout(id)
  }, [text])
  const loadMore = async () => {
    if (!page?.next) return
    setMore(true)
    try {
      const p = await api.get<Page>('/audit?' + params(page.next))
      setPage({ ...p, rows: [...page.rows, ...p.rows] })
    } finally { setMore(false) }
  }
  const kindLabel = (k: string) => kinds().find((x) => x.value === k)?.label ?? k
  return (
    <>
      <PageHeader title={t('Audit log')} subtitle={t('Who viewed, changed, exported or restored what')} icon={<History className="size-5" />}
        actions={<a href={'/api/audit/export?' + params()}><Button icon={<Download className="size-4" />}>CSV</Button></a>} />
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <div className="flex flex-wrap gap-1 rounded-xl bg-[var(--panel-2)] p-1">
          {kinds().map((k) => (
            <button key={k.value} onClick={() => setFilter({ ...filter, kind: k.value })}
              className={cx('rounded-lg px-3 py-1.5 text-xs font-medium transition cursor-pointer', filter.kind === k.value ? 'bg-[var(--panel)] text-[var(--text)] shadow-sm' : 'text-muted hover:text-[var(--text)]')}>
              {k.label}
            </button>
          ))}
        </div>
        <Select className="w-48" value={filter.user} onChange={(user) => setFilter({ ...filter, user })}
          options={[{ value: '', label: t('All users') }, ...(page?.users ?? []).map((u) => ({ value: u, label: u }))]} />
        <div className="relative min-w-56 flex-1">
          <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted" />
          <Input className="pl-9" value={text} onChange={(e) => setText(e.target.value)} placeholder={t('Search action, details or IP address')} />
        </div>
      </div>
      <Card pad={false}>
        {!page ? <div className="grid place-items-center py-16"><Spinner /></div> : !page.rows.length ? (
          <Empty icon={<History className="size-6" />} title={t('No entries')} text={t('Adjust the filters.')} />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="text-left text-[11px] uppercase tracking-wide text-muted"><tr className="border-b border-app">
                <th className="px-5 py-3 font-medium">{t('Time')}</th><th className="px-3 py-3 font-medium">{t('User')}</th><th className="px-3 py-3 font-medium">{t('Type')}</th><th className="px-3 py-3 font-medium">{t('Action')}</th><th className="px-3 py-3 font-medium">{t('Details')}</th><th className="px-5 py-3 font-medium">{t('IP address')}</th>
              </tr></thead>
              <tbody>{page.rows.map((a) => (
                <tr key={a.id} className="border-b border-app/60 align-top">
                  <td className="px-5 py-2 font-mono text-xs text-muted whitespace-nowrap">{fmtTime(a.ts * 1000)}</td>
                  <td className="px-3 py-2 whitespace-nowrap">{a.user}{a.via && <div className="text-[11px] text-muted">{a.via.replace(/^api: /, t('API token') + ': ')}</div>}</td>
                  <td className="px-3 py-2"><Badge className={kindClass[a.kind]}>{kindLabel(a.kind)}</Badge></td>
                  <td className="px-3 py-2 font-mono text-xs">{a.action}</td>
                  <td className="px-3 py-2 text-muted break-all">{a.detail}</td>
                  <td className="px-5 py-2 font-mono text-xs text-muted whitespace-nowrap">{a.ip}</td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        )}
      </Card>
      {page?.next && <div className="mt-4 flex justify-center"><Button loading={more} onClick={loadMore}>{t('Load more')}</Button></div>}
    </>
  )
}
