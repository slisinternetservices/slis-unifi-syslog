import { brandingUrl, type Branding } from '../api'
import { cx } from './ui'

/** Company logo for the current theme, or the app mark when none is uploaded. */
export function BrandMark({ branding, large, theme }: { branding: Branding; large?: boolean; theme?: 'light' | 'dark' }) {
  const { logo_light: light, logo_dark: dark } = branding.assets
  if (light || dark) {
    const h = large ? 'max-h-16 max-w-[220px]' : 'max-h-9 max-w-[140px]'
    if (theme) {
      const a = theme === 'dark' ? (dark ? 'logo_dark' : 'logo_light') : light ? 'logo_light' : 'logo_dark'
      return <img src={brandingUrl(branding, a)} alt={branding.company_name} className={cx(h, 'w-auto object-contain')} />
    }
    return <>
      <img src={brandingUrl(branding, light ? 'logo_light' : 'logo_dark')} alt={branding.company_name} className={cx(h, 'w-auto object-contain', dark && 'dark:hidden')} />
      {dark && <img src={brandingUrl(branding, 'logo_dark')} alt={branding.company_name} className={cx(h, 'hidden w-auto object-contain dark:block')} />}
    </>
  }
  // The letterhead wordmark includes INTERNET SERVICES. Preserve its native
  // aspect ratio; a square badge would stretch or crop the original artwork.
  const h = cx(large ? 'w-[220px] max-h-20' : 'w-[140px] max-h-12', 'h-auto max-w-full shrink-0 object-contain')
  if (theme) {
    return <img src={`/slis-logo-${theme}.png`} alt="SLIS Internet Services" width={1189} height={367} className={h} />
  }
  return <>
    <img src="/slis-logo-light.png" alt="SLIS Internet Services" width={1189} height={367} className={cx(h, 'dark:hidden')} />
    <img src="/slis-logo-dark.png" alt="SLIS Internet Services" width={1189} height={367} className={cx(h, 'hidden dark:block')} />
  </>
}

/**
 * Logo with server and company name, as at the top of the menu. A company
 * logo gets its own row since it is usually wider than the app mark.
 */
export function BrandHeader({ branding, instance, theme, className }: { branding: Branding; instance: string; theme?: 'light' | 'dark'; className?: string }) {
  const muted = theme === 'light' ? 'text-slate-500' : theme === 'dark' ? 'text-slate-400' : 'text-muted'
  const text = (
    <div className="min-w-0">
      <div className="truncate text-sm font-semibold">{instance}</div>
      <div className={cx('truncate text-[11px]', muted)}>{branding.company_name || 'SLIS Internet Services'}</div>
    </div>
  )
  return <div className={cx('space-y-2.5', className)}><BrandMark branding={branding} theme={theme} />{text}</div>
}
