import { useEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { CalendarClock, MapPin, RefreshCw } from 'lucide-react'
import { api } from '../api'
import { rangePresets } from '../lib/format'
import { Button } from './ui'
import { t } from '../i18n'

export interface RangeState { preset: string; from?: string; to?: string }

export function toRange(r: RangeState) {
  if (r.preset === 'custom' && r.from && r.to) return { from: new Date(r.from), to: new Date(r.to) }
  const p = rangePresets.find((x) => x.key === r.preset) ?? rangePresets[3]
  const to = new Date()
  return { from: new Date(to.getTime() - p.ms), to }
}

const toLocalInput = (d: Date) => new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16)

export function RangePicker({ value, onChange }: { value: RangeState; onChange: (r: RangeState) => void }) {
  const r = toRange(value)
  return (
    <div className="flex flex-wrap items-center gap-2">
      <div className="panel flex items-center gap-1 rounded-xl p-1">
        <CalendarClock className="ml-2 size-4 text-muted" />
        <select value={value.preset} onChange={(e) => onChange(e.target.value === 'custom' ? { preset: 'custom', from: toLocalInput(r.from), to: toLocalInput(r.to) } : { preset: e.target.value })}
          className="cursor-pointer bg-transparent px-2 py-1 text-sm outline-none">
          {rangePresets.map((p) => <option key={p.key} value={p.key}>{t(p.label)}</option>)}
          <option value="custom">{t('Custom…')}</option>
        </select>
      </div>
      {value.preset === 'custom' && (
        <div className="panel flex items-center gap-2 rounded-xl px-2 py-1 text-sm">
          <input type="datetime-local" value={value.from} onChange={(e) => onChange({ ...value, from: e.target.value })} className="bg-transparent outline-none" />
          <span className="text-muted">{t('to')}</span>
          <input type="datetime-local" value={value.to} onChange={(e) => onChange({ ...value, to: e.target.value })} className="bg-transparent outline-none" />
        </div>
      )}
    </div>
  )
}

export function SiteFilter({ value, onChange }: { value: string; onChange: (s: string) => void }) {
  const [sites, setSites] = useState<string[]>([])
  useEffect(() => { api.get<string[]>('/sites').then(setSites).catch(() => {}) }, [])
  if (sites.length <= 1) return null
  return (
    <div className="panel flex items-center gap-1 rounded-xl p-1">
      <MapPin className="ml-2 size-4 text-muted" />
      <select value={value} onChange={(e) => onChange(e.target.value)} className="cursor-pointer bg-transparent px-2 py-1 text-sm outline-none">
        <option value="">{t('All sites')}</option>
        {sites.map((s) => <option key={s} value={s}>{s === 'default' ? t('Default') : s}</option>)}
      </select>
    </div>
  )
}

export function RefreshButton({ onClick, loading }: { onClick: () => void; loading?: boolean }) {
  return <Button variant="secondary" title={t('Refresh')} onClick={onClick} icon={<RefreshCw className={loading ? 'size-4 animate-spin' : 'size-4'} />} />
}

// Persist filter state in the URL so views are shareable and survive reloads.
export function useUrlState<T extends Record<string, string>>(defaults: T): [T, (p: Partial<T>) => void] {
  const read = () => {
    const q = new URLSearchParams(window.location.search)
    const out = { ...defaults }
    for (const k of Object.keys(defaults)) { const v = q.get(k); if (v !== null) (out as Record<string, string>)[k] = v }
    return out
  }
  const [s, set] = useState<T>(read)
  // Links to the same page with other filters (saved searches in the menu)
  // change the URL through the router; take them over.
  const loc = useLocation()
  const first = useRef(true)
  useEffect(() => {
    if (first.current) { first.current = false; return }
    set(read())
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loc.key])
  const update = (p: Partial<T>) => {
    const n = { ...s, ...p } as T
    set(n)
    const q = new URLSearchParams()
    for (const [k, v] of Object.entries(n)) if (v && v !== defaults[k]) q.set(k, v)
    const qs = q.toString()
    window.history.replaceState(null, '', window.location.pathname + (qs ? '?' + qs : ''))
  }
  return [s, update]
}
