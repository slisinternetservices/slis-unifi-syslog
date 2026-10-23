import { useRef, useState } from 'react'
import { Palette, Save, Upload, Trash2, ImageOff } from 'lucide-react'
import { api, brandingUrl, type Branding, type BrandingAssetName } from '../api'
import { useAuth } from '../App'
import { BrandHeader } from '../components/Brand'
import { Button, Card, Field, Input, PageHeader, cx, useToast } from '../components/ui'
import { t } from '../i18n'

const slots: { name: BrandingAssetName; title: string; hint: string; accept: string; dark?: boolean }[] = [
  { name: 'logo_light', title: 'Logo for light mode', hint: 'Shown on light backgrounds and in e-mails. PNG or SVG, at most 512 KB.', accept: 'image/png,image/svg+xml' },
  { name: 'logo_dark', title: 'Logo for dark mode', hint: 'Optional. Shown on dark backgrounds; falls back to the light logo.', accept: 'image/png,image/svg+xml', dark: true },
  { name: 'favicon', title: 'Favicon', hint: 'The icon in the browser tab. Square PNG, SVG or ICO.', accept: 'image/png,image/svg+xml,image/x-icon,.ico' },
]

export default function BrandingPage() {
  const toast = useToast()
  const { branding, instance, reloadStatus } = useAuth()
  const [company, setCompany] = useState(branding.company_name)
  const [busy, setBusy] = useState(false)

  const save = async () => {
    setBusy(true)
    try { await api.put<Branding>('/settings/branding', { company_name: company }); toast('ok', t('Branding saved')); reloadStatus() } catch (e) { toast('err', (e as Error).message) } finally { setBusy(false) }
  }

  return (
    <>
      <PageHeader title={t('Branding')} subtitle={t('Your company name and logo in the menu, on the sign-in page and in e-mails')} icon={<Palette className="size-5" />}
        actions={<Button variant="primary" loading={busy} onClick={save} icon={<Save className="size-4" />}>{t('Save')}</Button>} />
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-3">
        <div className="space-y-6 xl:col-span-2">
          <Card title={t('Company')}>
            <Field label={t('Company name')} hint={t('Shown below the server name. Leave empty to hide it.')}>
              <Input value={company} maxLength={120} onChange={(e) => setCompany(e.target.value)} placeholder={t('e.g. {example}', { example: 'Contoso B.V.' })} />
            </Field>
          </Card>
          <div className="grid gap-6 md:grid-cols-3">
            {slots.map((s) => <AssetCard key={s.name} slot={s} branding={branding} onChange={reloadStatus} />)}
          </div>
        </div>
        <Card title={t('Preview')} subtitle={t('Saved settings, as others see them')}>
          <div className="space-y-4">
            {[false, true].map((dark) => (
              <div key={String(dark)}>
                <BrandHeader branding={branding} instance={instance} theme={dark ? 'dark' : 'light'}
                  className={cx('rounded-xl px-4 py-4 ring-1', dark ? 'bg-[#0f1424] text-slate-100 ring-white/10' : 'bg-white text-slate-900 ring-slate-200')} />
                <div className="mt-1 text-[11px] text-muted">{dark ? t('Dark mode') : t('Light mode')}</div>
              </div>
            ))}
          </div>
        </Card>
      </div>
    </>
  )
}

function AssetCard({ slot, branding, onChange }: { slot: (typeof slots)[number]; branding: Branding; onChange: () => void }) {
  const toast = useToast()
  const input = useRef<HTMLInputElement>(null)
  const [busy, setBusy] = useState(false)
  const has = !!branding.assets[slot.name]

  const upload = async (file?: File) => {
    if (!file) return
    setBusy(true)
    try { await api.upload('/settings/branding/' + slot.name, file); toast('ok', t('Image uploaded')); onChange() } catch (e) { toast('err', (e as Error).message) } finally {
      setBusy(false)
      if (input.current) input.current.value = ''
    }
  }
  const remove = async () => {
    if (!confirm(t('Remove this image?'))) return
    try { await api.del('/settings/branding/' + slot.name); onChange() } catch (e) { toast('err', (e as Error).message) }
  }

  return (
    <Card title={t(slot.title)}>
      <div className={cx('grid h-28 place-items-center rounded-xl p-3 ring-1 ring-inset', slot.dark ? 'bg-[#0f1424] ring-white/10' : 'bg-white ring-slate-200')}>
        {has
          ? <img src={brandingUrl(branding, slot.name)} alt="" className={cx('w-auto object-contain', slot.name === 'favicon' ? 'size-12' : 'max-h-20 max-w-full')} />
          : <ImageOff className="size-6 text-slate-400" />}
      </div>
      <p className="mt-3 text-xs text-muted">{t(slot.hint)}</p>
      <div className="mt-3 flex gap-2">
        <input ref={input} type="file" accept={slot.accept} className="hidden" onChange={(e) => upload(e.target.files?.[0])} />
        <Button loading={busy} onClick={() => input.current?.click()} icon={<Upload className="size-4" />} className="flex-1">{has ? t('Replace') : t('Upload')}</Button>
        {has && <Button variant="danger" onClick={remove} icon={<Trash2 className="size-4" />} aria-label={t('Remove')} title={t('Remove')} />}
      </div>
    </Card>
  )
}
