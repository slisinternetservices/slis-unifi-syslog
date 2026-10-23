import { useMemo } from 'react'
import { Area, AreaChart, Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import type { Bucket, TopItem } from '../api'
import { t } from '../i18n'
import { catColor, catLabel, chartInk, fmtCompact, fmtNum, otherColor, primaryCategories } from '../lib/format'

function tickFmt(stepSec: number) {
  return (t: number) => {
    const d = new Date(t)
    if (stepSec >= 86400) return d.toLocaleDateString('en-GB', { day: '2-digit', month: '2-digit' })
    if (stepSec >= 3600) return d.toLocaleString('en-GB', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' })
    return d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })
  }
}

// Fold buckets into one row per time slot; non-primary categories become
// "other". In key mode the six largest keys keep their own series.
function pivot(buckets: Bucket[], byCategory: boolean, stepSec: number, range?: { from: Date; to: Date }, byKey = false) {
  const rows = new Map<number, Record<string, number>>()
  // Empty slots are real zeros: fill the whole requested range.
  if (range && stepSec > 0) {
    const step = stepSec * 1000
    const start = Math.floor(range.from.getTime() / step) * step
    for (let t = start; t <= range.to.getTime() && rows.size < 400; t += step) rows.set(t, { t })
  }
  const keys = new Set<string>()
  let top: string[] = []
  if (byKey) {
    const tot = new Map<string, number>()
    for (const b of buckets) tot.set(b.key, (tot.get(b.key) ?? 0) + b.count)
    top = [...tot.entries()].filter(([k]) => k !== '').sort((a, b) => b[1] - a[1]).slice(0, 6).map(([k]) => k)
  }
  for (const b of buckets) {
    const t = new Date(b.t).getTime()
    const k = byKey ? (top.includes(b.key) ? b.key : 'other') : byCategory ? (primaryCategories.includes(b.key) ? b.key : 'other') : 'count'
    keys.add(k)
    const r = rows.get(t) ?? { t }
    r[k] = (r[k] ?? 0) + b.count
    rows.set(t, r)
  }
  const order = byKey ? [...top, ...(keys.has('other') ? ['other'] : [])]
    : byCategory ? [...primaryCategories.filter((c) => keys.has(c)), ...(keys.has('other') ? ['other'] : [])] : ['count']
  return { data: [...rows.values()].sort((a, b) => a.t - b.t), keys: order }
}

function TipBox({ active, payload, label, stepSec, name = catLabel }: { active?: boolean; payload?: { dataKey: string; value: number; color: string }[]; label?: number; stepSec: number; name?: (k: string) => string }) {
  if (!active || !payload?.length) return null
  const ink = chartInk()
  const total = payload.reduce((s, p) => s + (p.value || 0), 0)
  return (
    <div className="rounded-xl px-3 py-2 text-xs shadow-xl ring-1 ring-[var(--border)]" style={{ background: ink.tip }}>
      <div className="mb-1 font-medium">{tickFmt(stepSec)(label ?? 0)}</div>
      {[...payload].reverse().filter((p) => p.value).map((p) => (
        <div key={p.dataKey} className="flex items-center justify-between gap-4">
          <span className="flex items-center gap-1.5"><span className="size-2 rounded-sm" style={{ background: p.color }} />{p.dataKey === 'count' ? t('Messages') : p.dataKey === 'other' ? t('Other') : name(p.dataKey)}</span>
          <span className="font-medium tabular-nums">{fmtNum(p.value)}</span>
        </div>
      ))}
      {payload.length > 1 && <div className="mt-1 flex justify-between border-t border-app pt-1 font-medium"><span>{t('Total')}</span><span className="tabular-nums">{fmtNum(total)}</span></div>}
    </div>
  )
}

/** Series colours for key mode, in rank order (validated categorical slots). */
const keyPalette = () => (document.documentElement.classList.contains('dark')
  ? ['#3987e5', '#d95926', '#199e70', '#c98500', '#9085e9', '#d55181']
  : ['#2a78d6', '#eb6834', '#1baf7a', '#eda100', '#6250d6', '#e87ba4'])

export function Timeline({ buckets, stepSec, byCategory = true, byKey = false, format, colorOf, height = 260, onSelect, range }: { buckets: Bucket[]; stepSec: number; byCategory?: boolean; byKey?: boolean; format?: (k: string) => string; colorOf?: (k: string) => string | undefined; height?: number; onSelect?: (from: Date, to: Date) => void; range?: { from: Date; to: Date } }) {
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const { data, keys } = useMemo(() => pivot(buckets, byCategory, stepSec, range, byKey), [buckets, byCategory, byKey, stepSec])
  const ink = chartInk()
  const pal = keyPalette()
  const name = format ?? catLabel
  const color = (k: string) => (k === 'other' ? otherColor() : k === 'count' ? '#3987e5' : byKey ? (colorOf?.(k) ?? pal[keys.indexOf(k) % pal.length]) : catColor(k))
  return (
    <div>
      <div style={{ height }}>
        <ResponsiveContainer>
          <BarChart data={data} barCategoryGap={1} onClick={(e) => {
            const t = (e as { activeLabel?: number } | null)?.activeLabel
            if (onSelect && t) onSelect(new Date(t), new Date(t + stepSec * 1000))
          }}>
            <CartesianGrid stroke={ink.grid} vertical={false} />
            <XAxis dataKey="t" type="category" tickFormatter={tickFmt(stepSec)} stroke={ink.axis} fontSize={11} tickLine={false} axisLine={false} minTickGap={40} />
            <YAxis tickFormatter={fmtCompact} stroke={ink.axis} fontSize={11} tickLine={false} axisLine={false} width={44} />
            <Tooltip cursor={{ fill: ink.grid, opacity: 0.5 }} content={<TipBox stepSec={stepSec} name={name} />} />
            {keys.map((k, i) => (
              <Bar key={k} dataKey={k} stackId="a" fill={color(k)} stroke="var(--panel)" strokeWidth={1}
                radius={i === keys.length - 1 ? [3, 3, 0, 0] : 0} cursor={onSelect ? 'pointer' : undefined} isAnimationActive={false} />
            ))}
          </BarChart>
        </ResponsiveContainer>
      </div>
      {keys.length > 1 && (
        <div className="mt-3 flex flex-wrap gap-x-4 gap-y-1.5 text-xs text-muted">
          {keys.map((k) => <span key={k} className="flex items-center gap-1.5"><span className="size-2.5 rounded-sm" style={{ background: color(k) }} />{k === 'other' ? t('Other') : name(k)}</span>)}
        </div>
      )}
    </div>
  )
}

export function Sparkline({ values, color = '#3987e5' }: { values: number[]; color?: string }) {
  const data = values.map((v, i) => ({ i, v }))
  return (
    <div className="h-10">
      <ResponsiveContainer>
        <AreaChart data={data}>
          <Area dataKey="v" stroke={color} strokeWidth={2} fill={color} fillOpacity={0.15} isAnimationActive={false} />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  )
}

// Horizontal magnitude bars: one hue, ranked. Labels in text ink.
export function BarList({ items, onClick, format = (k: string) => k, color = '#3987e5', empty = t('No data'), mono = true }: { items: TopItem[]; onClick?: (k: string) => void; format?: (k: string) => React.ReactNode; color?: string | ((k: string) => string); empty?: string; mono?: boolean }) {
  if (!items.length) return <p className="py-6 text-center text-sm text-muted">{empty}</p>
  const max = Math.max(...items.map((i) => i.count))
  return (
    <ul className="space-y-1">
      {items.map((it) => {
        const c = typeof color === 'function' ? color(it.key) : color
        return (
          <li key={it.key}>
            <button onClick={() => onClick?.(it.key)} title={`${it.key}: ${fmtNum(it.count)}`}
              className="group relative flex w-full items-center justify-between gap-3 overflow-hidden rounded-lg px-2.5 py-1.5 text-left text-sm hover:bg-[var(--panel-2)] cursor-pointer">
              <span className="absolute inset-y-1 left-0 rounded-r" style={{ width: `${Math.max(2, (it.count / max) * 100)}%`, background: c, opacity: 0.18 }} />
              <span className={'relative truncate ' + (mono ? 'font-mono text-[13px]' : '')}>{format(it.key)}</span>
              <span className="relative shrink-0 tabular-nums text-muted">{fmtNum(it.count)}</span>
            </button>
          </li>
        )
      })}
    </ul>
  )
}
