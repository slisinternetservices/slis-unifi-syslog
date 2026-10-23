// Lists interface strings that a translation file is missing.
// Usage: node scripts/i18n-check.mjs [--write]
// Only literal t('...') keys are found; strings translated through a
// variable (t(item.label)) must be added by hand. --write appends missing
// keys with an empty value so translators can fill them in.
import { readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const root = new URL('../src/', import.meta.url).pathname
const files = []
const walk = (d) => readdirSync(d).forEach((f) => {
  const p = join(d, f)
  if (statSync(p).isDirectory()) walk(p)
  else if (/\.tsx?$/.test(f) && f !== 'i18n.ts') files.push(p)
})
walk(root)

const keys = new Set()
const re = /\bt\(\s*(['"`])((?:\\.|(?!\1).)*)\1/g
for (const f of files) {
  for (const m of readFileSync(f, 'utf8').matchAll(re)) {
    if (m[1] === '`' && m[2].includes('${')) continue
    keys.add(m[2].replace(/\\(['"`\\])/g, '$1'))
  }
}

let missingTotal = 0
const dir = join(root, 'locales')
for (const f of readdirSync(dir)) {
  if (f === 'en.json') continue
  const dict = JSON.parse(readFileSync(join(dir, f), 'utf8'))
  const missing = [...keys].filter((k) => !(k in dict)).sort()
  missingTotal += missing.length
  if (missing.length) {
    console.log(`${f}: ${missing.length} missing`)
    missing.forEach((k) => console.log('  ' + JSON.stringify(k)))
    if (process.argv.includes('--write')) {
      missing.forEach((k) => (dict[k] = ''))
      writeFileSync(join(dir, f), JSON.stringify(dict, null, 2) + '\n')
    }
  } else console.log(`${f}: complete`)
}
process.exit(missingTotal ? 1 : 0)
