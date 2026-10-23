/// <reference types="vite/client" />
// Interface translations. Keys are the English source strings, so English
// needs no entries and an untranslated key simply shows in English. Every
// src/locales/<code>.json file is picked up automatically: adding a language
// means adding a file, not changing code. "_language" holds the language's
// own name for the language picker.

type Dict = Record<string, string>

const files = import.meta.glob<{ default: Dict }>('./locales/*.json', { eager: true })
const dicts: Record<string, Dict> = {}
for (const [path, mod] of Object.entries(files)) {
  const code = path.replace(/^.*\/(.+)\.json$/, '$1')
  dicts[code] = mod.default
}

/** Available languages as [code, native name], English first. */
export const languages: [string, string][] = Object.entries(dicts)
  .map(([code, d]) => [code, d._language || code] as [string, string])
  .sort((a, b) => (a[0] === 'en' ? -1 : b[0] === 'en' ? 1 : a[1].localeCompare(b[1])))

const intlLocales: Record<string, string> = { en: 'en-GB', nl: 'nl-NL', de: 'de-DE', fr: 'fr-FR' }

let current = 'en'

/** Picks the best available language for a tag like "nl-BE". */
export function resolveLanguage(...tags: (string | undefined)[]): string {
  for (const tag of tags) {
    if (!tag) continue
    const t = tag.toLowerCase()
    if (dicts[t]) return t
    const base = t.split(/[-_]/)[0]
    if (dicts[base]) return base
  }
  return 'en'
}

export function setLanguage(code: string) {
  current = resolveLanguage(code)
  document.documentElement.lang = current
}

export const language = () => current

/** Locale for Intl number and date formatting. */
export const locale = () => intlLocales[current] ?? current

/**
 * Translates an English source string. Placeholders like {name} are filled
 * from vars: t('Deleted {n} rules', { n: 3 }).
 */
export function t(key: string, vars?: Record<string, string | number>): string {
  let s = dicts[current]?.[key] || key
  if (vars) s = s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m))
  return s
}
