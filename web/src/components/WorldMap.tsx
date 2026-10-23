import { useEffect, useMemo, useState } from 'react'
import { geoNaturalEarth1, geoPath } from 'd3-geo'
import { feature } from 'topojson-client'
import type { Feature, FeatureCollection, Geometry } from 'geojson'
import type { Topology } from 'topojson-specification'
import type { TopItem } from '../api'
import { t } from '../i18n'
import { countryName, flag, numericToAlpha2 } from '../lib/countries'
import { fmtNum } from '../lib/format'
import { Spinner } from './ui'

type Country = Feature<Geometry, { name: string }> & { id?: string }

// The topology is ~100 KB, so it is loaded only when a map is shown.
let cache: Promise<Country[]> | null = null
function loadCountries() {
  cache ??= import('world-atlas/countries-110m.json').then((m) => {
    const topo = m.default as unknown as Topology
    return (feature(topo, topo.objects.countries) as unknown as FeatureCollection<Geometry, { name: string }>).features as Country[]
  })
  return cache
}

const W = 960, H = 470

/** Choropleth of counts per country (alpha-2 keys). */
export function WorldMap({ items, onSelect, color = '#e66767' }: { items: TopItem[]; onSelect?: (cc: string) => void; color?: string }) {
  const [countries, setCountries] = useState<Country[] | null>(null)
  const [hover, setHover] = useState<{ cc: string; x: number; y: number } | null>(null)
  useEffect(() => { loadCountries().then(setCountries) }, [])

  const counts = useMemo(() => new Map(items.map((i) => [i.key.toUpperCase(), i.count])), [items])
  const max = Math.max(1, ...items.map((i) => i.count))
  const path = useMemo(() => geoPath(geoNaturalEarth1().fitSize([W, H], { type: 'Sphere' })), [])

  if (!countries) return <div className="grid h-64 place-items-center"><Spinner /></div>
  const code = (c: Country) => (c.id ? numericToAlpha2[c.id] : c.properties.name === 'Kosovo' ? 'XK' : '') ?? ''
  // Log scale: a handful of countries usually dominate.
  const opacity = (n: number) => (n ? 0.2 + 0.8 * (Math.log(n + 1) / Math.log(max + 1)) : 0)

  return (
    <div className="relative" onMouseLeave={() => setHover(null)}>
      <svg viewBox={`0 0 ${W} ${H}`} className="h-auto w-full" role="img" aria-label={t('Map of traffic per country')}>
        <path d={path({ type: 'Sphere' }) ?? ''} fill="var(--panel-2)" />
        {countries.map((c, i) => {
          const cc = code(c)
          const n = counts.get(cc) ?? 0
          return (
            <g key={i} onMouseMove={(e) => {
              const r = (e.currentTarget.ownerSVGElement as SVGSVGElement).getBoundingClientRect()
              setHover({ cc, x: e.clientX - r.left, y: e.clientY - r.top })
            }} onClick={() => n && cc && onSelect?.(cc)} className={n && onSelect ? 'cursor-pointer' : undefined}>
              <path d={path(c) ?? ''} fill="var(--border)" stroke="var(--panel)" strokeWidth={0.5} />
              {n > 0 && <path d={path(c) ?? ''} fill={color} fillOpacity={opacity(n)} stroke="var(--panel)" strokeWidth={0.5} />}
            </g>
          )
        })}
      </svg>
      {hover?.cc && (
        <div className="pointer-events-none absolute z-10 rounded-xl bg-[var(--panel)] px-3 py-2 text-xs shadow-xl ring-1 ring-[var(--border)]"
          style={{ left: Math.min(hover.x + 12, 9999), top: hover.y + 12 }}>
          <div className="font-medium">{flag(hover.cc)} {countryName(hover.cc)}</div>
          <div className="tabular-nums text-muted">{t('{n} events', { n: fmtNum(counts.get(hover.cc) ?? 0) })}</div>
        </div>
      )}
    </div>
  )
}
