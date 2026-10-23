import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Bookmark, Pin, PinOff, Pencil, Trash2, Link2, BellPlus, Users2, Lock, ArrowRight, Search } from 'lucide-react'
import { api, can, searchLink, searchesChanged, type SavedSearch } from '../api'
import { useAuth } from '../App'
import { Badge, Button, Empty, Field, Input, Modal, PageHeader, Select, Spinner, Toggle, cx, useAsync, useToast } from '../components/ui'
import { fmtTime, rangePresets } from '../lib/format'
import { t } from '../i18n'

export type SearchDraft = Pick<SavedSearch, 'name' | 'description' | 'query' | 'preset' | 'from' | 'to' | 'site' | 'shared' | 'pinned'> & { id?: number }

export function rangeLabel(s: Pick<SavedSearch, 'preset' | 'from' | 'to'>) {
  if (s.preset === 'custom') return `${fmtTime(s.from)} – ${fmtTime(s.to)}`
  const p = rangePresets.find((x) => x.key === s.preset)
  return p ? t(p.label) : t('Time range of the Logs page')
}

/** Create or edit a saved search; also used by the Logs page. */
export function SearchModal({ draft, onClose, onSaved }: { draft?: SearchDraft; onClose: () => void; onSaved?: (s: SavedSearch) => void }) {
  const { user } = useAuth()
  const toast = useToast()
  const [d, setD] = useState<SearchDraft>()
  const [busy, setBusy] = useState(false)
  useEffect(() => setD(draft), [draft])
  if (!d) return null
  const save = async () => {
    setBusy(true)
    try {
      const saved = d.id ? await api.put<SavedSearch>('/saved-searches/' + d.id, d) : await api.post<SavedSearch>('/saved-searches', d)
      toast('ok', t('Search saved'))
      searchesChanged()
      onSaved?.(saved)
      onClose()
    } catch (e) { toast('err', (e as Error).message) } finally { setBusy(false) }
  }
  const keepRange = d.preset !== ''
  return (
    <Modal open onClose={onClose} title={d.id ? t('Edit saved search') : t('Save search')}
      footer={<><Button onClick={onClose}>{t('Cancel')}</Button><Button variant="primary" loading={busy} disabled={!d.name.trim()} onClick={save}>{t('Save')}</Button></>}>
      <div className="space-y-4">
        <Field label={t('Name')}><Input autoFocus value={d.name} onChange={(e) => setD({ ...d, name: e.target.value })} placeholder={t('e.g. SSH attacks')} /></Field>
        <Field label={t('Description')}><Input value={d.description} onChange={(e) => setD({ ...d, description: e.target.value })} /></Field>
        <Field label={t('Filter')}><Input className="font-mono" value={d.query} onChange={(e) => setD({ ...d, query: e.target.value })} placeholder={t('(all)')} /></Field>
        <Field label={t('Time range')} hint={keepRange ? undefined : t('Opens with the time range the Logs page uses by default')}>
          <Select value={d.preset} onChange={(preset) => setD({ ...d, preset })}
            options={[{ value: '', label: t('Time range of the Logs page') }, ...rangePresets.map((p) => ({ value: p.key, label: t(p.label) })),
              ...(d.preset === 'custom' ? [{ value: 'custom', label: rangeLabel(d) }] : [])]} />
        </Field>
        {d.site && <Field label={t('Site')}><code className="block rounded-xl bg-[var(--panel-2)] px-3 py-2 text-xs">{d.site}</code></Field>}
        <Toggle checked={d.pinned} onChange={(pinned) => setD({ ...d, pinned })} label={t('Show in the menu')} description={t('Adds a shortcut under Saved searches in the menu on the left')} />
        {can.categories(user) && <Toggle checked={d.shared} onChange={(shared) => setD({ ...d, shared })} label={t('Share with everyone')} description={t('All users can open and pin this search; only you and administrators can change it')} />}
      </div>
    </Modal>
  )
}

export default function SavedSearches() {
  const { user } = useAuth()
  const toast = useToast()
  const nav = useNavigate()
  const list = useAsync(() => api.get<SavedSearch[]>('/saved-searches'))
  const [filter, setFilter] = useState<'all' | 'mine' | 'shared'>('all')
  const [text, setText] = useState('')
  const [edit, setEdit] = useState<SearchDraft>()

  const pin = async (s: SavedSearch) => {
    try {
      const n = await api.put<SavedSearch>(`/saved-searches/${s.id}/pin`, { pinned: !s.pinned })
      list.setData(list.data?.map((x) => (x.id === s.id ? n : x)))
      searchesChanged()
    } catch (e) { toast('err', (e as Error).message) }
  }
  const del = async (s: SavedSearch) => {
    if (!confirm(t('Delete saved search "{name}"?', { name: s.name }))) return
    try { await api.del('/saved-searches/' + s.id); list.reload(); searchesChanged() } catch (e) { toast('err', (e as Error).message) }
  }
  const copy = async (s: SavedSearch) => {
    try { await navigator.clipboard.writeText(window.location.origin + searchLink(s)); toast('ok', t('Link copied')) } catch { toast('err', t('Copying is not allowed by the browser')) }
  }
  const items = (list.data ?? []).filter((s) => (filter === 'all' || (filter === 'mine' ? s.mine : s.shared)) &&
    (!text || (s.name + ' ' + s.query + ' ' + s.description).toLowerCase().includes(text.toLowerCase())))

  return (
    <>
      <PageHeader title={t('Saved searches')} subtitle={t('Your own searches and the ones shared with everyone, one click away')} icon={<Bookmark className="size-5" />}
        actions={<Link to="/logs"><Button variant="primary" icon={<Search className="size-4" />}>{t('New search')}</Button></Link>} />

      <div className="mb-4 flex flex-wrap items-center gap-2">
        <div className="panel flex rounded-xl p-1 text-sm">
          {([['all', t('All')], ['mine', t('Mine')], ['shared', t('Shared')]] as const).map(([k, l]) => (
            <button key={k} onClick={() => setFilter(k)} className={cx('rounded-lg px-3 py-1 cursor-pointer', filter === k ? 'bg-indigo-500/15 text-indigo-400' : 'text-muted hover:text-[var(--text)]')}>{l}</button>
          ))}
        </div>
        <Input className="max-w-xs" value={text} onChange={(e) => setText(e.target.value)} placeholder={t('Filter by name or query')} />
      </div>

      {list.loading && !list.data ? <div className="grid place-items-center py-20"><Spinner /></div>
        : !items.length ? (
          <div className="panel rounded-2xl">
            <Empty icon={<Bookmark className="size-8" />} title={list.data?.length ? t('No matching searches') : t('No saved searches yet')}
              text={t('Search on the Logs page and click Save search. Pinned searches appear in the menu on the left.')}
              action={<Link to="/logs"><Button icon={<ArrowRight className="size-4" />}>{t('Go to Logs')}</Button></Link>} />
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
            {items.map((s) => (
              <div key={s.id} className="panel group flex flex-col rounded-2xl p-5 fade-in">
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <h3 className="flex items-center gap-2 font-semibold"><span className="truncate">{s.name}</span>
                      {s.shared ? <Badge className="bg-cyan-500/10 text-cyan-500 ring-cyan-500/30"><Users2 className="size-3" />{t('Shared')}</Badge> : <Badge><Lock className="size-3" />{t('Private')}</Badge>}
                    </h3>
                    {s.description && <p className="mt-0.5 text-sm text-muted">{s.description}</p>}
                  </div>
                  <button title={s.pinned ? t('Remove from the menu') : t('Show in the menu')} onClick={() => pin(s)}
                    className={cx('cursor-pointer rounded-lg p-1.5 hover:bg-[var(--panel-2)]', s.pinned ? 'text-indigo-400' : 'text-muted')}>
                    {s.pinned ? <Pin className="size-4" /> : <PinOff className="size-4" />}
                  </button>
                </div>
                <code className="mt-3 block truncate rounded-lg bg-[var(--panel-2)] px-2.5 py-1.5 font-mono text-xs text-muted" title={s.query}>{s.query || t('(all)')}</code>
                <div className="mt-2 text-xs text-muted">{rangeLabel(s)}{s.site && ' · ' + s.site}{!s.mine && ' · ' + t('by {name}', { name: s.owner })}</div>
                <div className="mt-auto flex items-center justify-between gap-2 pt-4">
                  <Link to={searchLink(s)} className="inline-flex items-center gap-1 text-sm text-indigo-400 hover:underline">{t('Open')} <ArrowRight className="size-3.5" /></Link>
                  <div className="flex gap-1 opacity-70 transition group-hover:opacity-100">
                    <button title={t('Copy link')} onClick={() => copy(s)} className="cursor-pointer rounded-lg p-1.5 hover:bg-[var(--panel-2)]"><Link2 className="size-4" /></button>
                    {can.admin(user) && <button title={t('Create alert from this search')} onClick={() => nav('/alerts?' + new URLSearchParams({ new: s.name, query: s.query }))} className="cursor-pointer rounded-lg p-1.5 hover:bg-[var(--panel-2)]"><BellPlus className="size-4" /></button>}
                    {s.can_edit && <>
                      <button title={t('Edit')} onClick={() => setEdit(s)} className="cursor-pointer rounded-lg p-1.5 hover:bg-[var(--panel-2)]"><Pencil className="size-4" /></button>
                      <button title={t('Delete')} onClick={() => del(s)} className="cursor-pointer rounded-lg p-1.5 text-red-500 hover:bg-red-500/10"><Trash2 className="size-4" /></button>
                    </>}
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}
      <SearchModal draft={edit} onClose={() => setEdit(undefined)} onSaved={() => list.reload()} />
    </>
  )
}
