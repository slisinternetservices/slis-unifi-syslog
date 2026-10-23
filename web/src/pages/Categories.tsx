import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Tags, Plus, Pencil, Trash2, ArrowRight } from 'lucide-react'
import { api, can, type Category } from '../api'
import { useAuth } from '../App'
import { Button, Empty, Field, Input, Modal, PageHeader, Spinner, Toggle, useAsync, useToast } from '../components/ui'
import { RangePicker, SiteFilter, toRange, useUrlState } from '../components/Filters'
import { fmtNum } from '../lib/format'
import { t } from '../i18n'

const colors = ['#3987e5', '#d95926', '#199e70', '#c98500', '#d55181', '#008300', '#9085e9', '#e66767']

export default function Categories() {
  const { user } = useAuth()
  const admin = can.categories(user)
  const [st, set] = useUrlState({ preset: '24h', from: '', to: '', site: '' })
  const nav = useNavigate()
  const toast = useToast()
  const cats = useAsync(() => api.get<Category[]>('/categories'))
  const stats = useAsync(() => {
    const r = toRange(st)
    const q = new URLSearchParams({ from: r.from.toISOString(), to: r.to.toISOString() })
    if (st.site) q.set('sites', st.site)
    return api.get<{ id: number; count: number; error?: string }[]>('/categories/stats?' + q)
  }, [st.preset, st.from, st.to, st.site, cats.data?.length])
  const [edit, setEdit] = useState<Partial<Category>>()

  const count = (id: number) => stats.data?.find((s) => s.id === id)
  const open = (c: Category) => {
    const q = new URLSearchParams({ q: c.query, preset: st.preset })
    if (st.site) q.set('site', st.site)
    nav('/logs?' + q)
  }
  const del = async (c: Category) => {
    if (!confirm(t('Delete category "{name}"?', { name: c.name }))) return
    await api.del('/categories/' + c.id)
    cats.reload()
  }
  const saveEdit = async () => {
    try {
      if (edit?.id) await api.put('/categories/' + edit.id, edit)
      else await api.post('/categories', { pinned: true, icon: 'filter', description: '', ...edit })
      setEdit(undefined)
      cats.reload()
      toast('ok', t('Category saved'))
    } catch (e) { toast('err', (e as Error).message) }
  }

  return (
    <>
      <PageHeader title={t('Categories')} subtitle={t('Custom filters to group logs into what you want to track')} icon={<Tags className="size-5" />}
        actions={<>
          <SiteFilter value={st.site} onChange={(site) => set({ site })} />
          <RangePicker value={st} onChange={(r) => set({ preset: r.preset, from: r.from ?? '', to: r.to ?? '' })} />
          {admin && <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setEdit({ name: '', query: '', color: colors[0] })}>{t('New category')}</Button>}
        </>} />

      {cats.loading && !cats.data ? <div className="grid place-items-center py-20"><Spinner /></div>
        : !cats.data?.length ? <Empty icon={<Tags className="size-8" />} title={t('No categories yet')} text={t('Create a category with a search filter, or save a search from the logs.')} />
        : (
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
            {cats.data.map((c) => {
              const s = count(c.id)
              return (
                <div key={c.id} className="panel group relative overflow-hidden rounded-2xl p-5 fade-in">
                  <div className="absolute inset-y-0 left-0 w-1" style={{ background: c.color }} />
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <h3 className="font-semibold">{c.name}</h3>
                      {c.description && <p className="mt-0.5 text-sm text-muted">{c.description}</p>}
                    </div>
                    {admin && (
                      <div className="flex gap-1 opacity-0 transition group-hover:opacity-100">
                        <button title={t('Edit')} onClick={() => setEdit(c)} className="cursor-pointer rounded-lg p-1.5 hover:bg-[var(--panel-2)]"><Pencil className="size-4" /></button>
                        <button title={t('Delete')} onClick={() => del(c)} className="cursor-pointer rounded-lg p-1.5 text-red-500 hover:bg-red-500/10"><Trash2 className="size-4" /></button>
                      </div>
                    )}
                  </div>
                  <div className="mt-4 text-3xl font-semibold tabular-nums">{s ? fmtNum(s.count) : <Spinner className="size-5" />}</div>
                  {s?.error && <p className="mt-1 text-xs text-red-500">{s.error}</p>}
                  <code className="mt-3 block truncate rounded-lg bg-[var(--panel-2)] px-2.5 py-1.5 font-mono text-xs text-muted">{c.query || t('(all)')}</code>
                  <button onClick={() => open(c)} className="mt-3 inline-flex items-center gap-1 text-sm text-indigo-400 hover:underline cursor-pointer">{t('View logs')} <ArrowRight className="size-3.5" /></button>
                </div>
              )
            })}
          </div>
        )}

      <Modal open={!!edit} onClose={() => setEdit(undefined)} title={edit?.id ? t('Edit category') : t('New category')}
        footer={<><Button onClick={() => setEdit(undefined)}>{t('Cancel')}</Button><Button variant="primary" onClick={saveEdit} disabled={!edit?.name}>{t('Save')}</Button></>}>
        {edit && (
          <div className="space-y-4">
            <Field label={t('Name')}><Input value={edit.name} onChange={(e) => setEdit({ ...edit, name: e.target.value })} /></Field>
            <Field label={t('Description')}><Input value={edit.description ?? ''} onChange={(e) => setEdit({ ...edit, description: e.target.value })} /></Field>
            <Field label={t('Filter')} hint={t('Same query language as the Logs page, e.g. {example}', { example: 'category:firewall action:block dport:22' })}>
              <Input className="font-mono" value={edit.query} onChange={(e) => setEdit({ ...edit, query: e.target.value })} />
            </Field>
            <Field label={t('Color')}>
              <div className="flex gap-2">
                {colors.map((c) => <button key={c} onClick={() => setEdit({ ...edit, color: c })} className="size-8 cursor-pointer rounded-lg ring-2 ring-offset-2 ring-offset-[var(--panel)]" style={{ background: c, ['--tw-ring-color' as string]: edit.color === c ? c : 'transparent' }} />)}
              </div>
            </Field>
            <Toggle checked={edit.pinned ?? true} onChange={(pinned) => setEdit({ ...edit, pinned })} label={t('Pinned')} description={t('Show this category at the top')} />
          </div>
        )}
      </Modal>
    </>
  )
}
