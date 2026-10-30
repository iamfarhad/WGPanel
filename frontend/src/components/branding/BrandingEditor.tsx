import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ImagePlus, Trash2 } from 'lucide-react'
import { apiFetch } from '../../lib/api'
import { apiErrorMessage } from '../../lib/apiErrors'
import { useI18n } from '../../lib/i18n'
import { useToast } from '../../lib/toast'
import { Button } from '../ui/Button'
import { Card, CardHeader } from '../ui/Card'
import { Field } from '../ui/Field'
import { Input } from '../ui/Input'
import { ErrorState } from '../ui/ErrorState'
import { accentStyle } from '../../lib/brandAccent'

interface Branding {
  brand_name: string
  support_link: string
  announcement: string
  accent_color: string
  logo_url: string
  has_logo_upload: boolean
  logo_preview: string | null
  inherited_from: '' | 'parent' | 'global'
}

// Mirrors the server limits (store/branding.go); the server stays the authority.
const MAX_LOGO_BYTES = 64 * 1024
const LOGO_TYPES = ['image/png', 'image/jpeg', 'image/webp']

/** Edits one branding profile: the global default (`/api/v1/branding`, super admin) or
 * the signed-in reseller's own (`/api/v1/reseller/branding`). What is saved is what the
 * public subscription page of the owner's accounts - and of every sub-reseller without
 * a profile of its own - shows. */
export function BrandingEditor({
  endpoint,
  title,
  description,
}: {
  endpoint: string
  title: string
  description: string
}) {
  const { t } = useI18n()
  const { push } = useToast()
  const queryClient = useQueryClient()
  const fileRef = useRef<HTMLInputElement>(null)
  const queryKey = ['branding', endpoint]

  const query = useQuery({ queryKey, queryFn: () => apiFetch<Branding>(endpoint) })

  const [name, setName] = useState('')
  const [support, setSupport] = useState('')
  const [announcement, setAnnouncement] = useState('')
  const [accent, setAccent] = useState('')
  const [logoUrl, setLogoUrl] = useState('')
  // A freshly picked file (data URL, sent as logo_base64) or a pending removal.
  const [newLogo, setNewLogo] = useState<string | null>(null)
  const [removeLogo, setRemoveLogo] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    const b = query.data
    if (!b) return
    setName(b.brand_name)
    setSupport(b.support_link)
    setAnnouncement(b.announcement)
    setAccent(b.accent_color)
    setLogoUrl(b.logo_url)
    setNewLogo(null)
    setRemoveLogo(false)
    setError(null)
  }, [query.data])

  const saveMutation = useMutation({
    mutationFn: () =>
      apiFetch<Branding>(endpoint, {
        method: 'PUT',
        body: JSON.stringify({
          brand_name: name,
          support_link: support,
          announcement,
          accent_color: accent,
          logo_url: newLogo || removeLogo ? '' : logoUrl,
          ...(newLogo ? { logo_base64: newLogo } : {}),
          ...(removeLogo ? { remove_logo: true } : {}),
        }),
      }),
    onSuccess: (saved) => {
      queryClient.setQueryData(queryKey, saved)
      push('success', t('Branding saved'))
    },
    onError: (err) => setError(apiErrorMessage(err, t)),
  })

  function pickLogo(file: File | undefined) {
    if (!file) return
    setError(null)
    if (!LOGO_TYPES.includes(file.type)) {
      setError(t('The logo must be a PNG, JPEG or WebP image.'))
      return
    }
    if (file.size > MAX_LOGO_BYTES) {
      setError(t('The logo is too large - keep it under 64 KB.'))
      return
    }
    const reader = new FileReader()
    reader.onload = () => {
      setNewLogo(String(reader.result))
      setRemoveLogo(false)
      setLogoUrl('')
    }
    reader.readAsDataURL(file)
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    saveMutation.mutate()
  }

  const data = query.data
  const shownLogo = removeLogo ? null : (newLogo ?? data?.logo_preview ?? (logoUrl || null))
  const hasLogo = shownLogo !== null

  return (
    <Card className="overflow-hidden">
      <CardHeader title={title} description={description} />
      {query.isError && <ErrorState title={t('Could not load branding.')} onRetry={() => query.refetch()} />}
      {data && (
        <form onSubmit={handleSubmit} className="space-y-5 p-5">
          {data.inherited_from && !name && !support && !announcement && !accent && !hasLogo && (
            <p className="rounded-lg bg-inset px-3 py-2 text-xs leading-relaxed text-muted">
              {data.inherited_from === 'parent'
                ? t('You have no branding yet: your customers currently see the branding of the reseller above you.')
                : t('You have no branding yet: your customers currently see the default branding.')}
            </p>
          )}

          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field label={t('Brand name')} hint={t('Shown at the top of the subscription page.')}>
              <Input value={name} maxLength={40} onChange={(e) => setName(e.target.value)} />
            </Field>
            <Field label={t('Support link')} hint={t('A Telegram handle like @my_support, or an https link.')}>
              <Input
                value={support}
                maxLength={200}
                dir="ltr"
                onChange={(e) => setSupport(e.target.value)}
                placeholder="@my_support"
              />
            </Field>
          </div>

          <Field label={t('Announcement')} hint={t('A short notice shown above the subscription page (plain text, up to 280 characters).')}>
            <textarea
              value={announcement}
              maxLength={280}
              rows={2}
              onChange={(e) => setAnnouncement(e.target.value)}
              className="w-full rounded-control border border-edge bg-surface px-3 py-2 text-sm text-fg transition-[border-color,box-shadow] duration-150 placeholder:text-faint hover:border-edge-strong focus:border-accent focus:ring-[3px] focus:ring-ring focus:outline-none"
            />
          </Field>

          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field label={t('Accent color')} labelSuffix={t('(optional)')}>
              <div className="flex items-center gap-2">
                <input
                  type="color"
                  aria-label={t('Accent color')}
                  value={/^#[0-9a-f]{6}$/i.test(accent) ? accent : '#16b96f'}
                  onChange={(e) => setAccent(e.target.value)}
                  className="h-10 w-12 cursor-pointer rounded-control border border-edge bg-surface p-1"
                />
                <Input
                  value={accent}
                  dir="ltr"
                  maxLength={7}
                  onChange={(e) => setAccent(e.target.value)}
                  placeholder="#16b96f"
                  className="max-w-32"
                />
                {accent && (
                  <Button type="button" variant="secondary" onClick={() => setAccent('')}>
                    {t('Reset')}
                  </Button>
                )}
              </div>
            </Field>

            <Field label={t('Logo')} hint={t('PNG, JPEG or WebP, up to 64 KB. Or paste the address of a logo hosted elsewhere (https).')}>
              <div className="flex items-center gap-3">
                <div className="flex size-12 shrink-0 items-center justify-center overflow-hidden rounded-lg border border-edge bg-inset">
                  {shownLogo ? (
                    <img src={shownLogo} alt="" referrerPolicy="no-referrer" className="size-full object-contain" />
                  ) : (
                    <ImagePlus className="h-5 w-5 text-faint" aria-hidden="true" />
                  )}
                </div>
                <input
                  ref={fileRef}
                  type="file"
                  accept={LOGO_TYPES.join(',')}
                  className="hidden"
                  onChange={(e) => {
                    pickLogo(e.target.files?.[0])
                    e.target.value = ''
                  }}
                />
                <Button type="button" variant="secondary" onClick={() => fileRef.current?.click()}>
                  {t('Upload logo')}
                </Button>
                {hasLogo && (
                  <Button
                    type="button"
                    variant="secondary"
                    onClick={() => {
                      setNewLogo(null)
                      setLogoUrl('')
                      setRemoveLogo(true)
                    }}
                  >
                    <Trash2 className="h-4 w-4" />
                    {t('Remove')}
                  </Button>
                )}
              </div>
              <Input
                className="mt-2"
                dir="ltr"
                value={logoUrl}
                maxLength={300}
                disabled={newLogo !== null}
                onChange={(e) => {
                  setLogoUrl(e.target.value)
                  if (e.target.value) setRemoveLogo(false)
                }}
                placeholder="https://example.com/logo.png"
              />
            </Field>
          </div>

          {/* Live preview of what the header strip of the customer page will look like. */}
          <div className="rounded-card border border-edge p-4" style={accentStyle(accent)}>
            <p className="mb-2 text-xs text-muted">{t('Preview')}</p>
            {announcement && <p className="mb-3 rounded-lg bg-accent-soft px-3 py-2 text-sm whitespace-pre-line text-fg">{announcement}</p>}
            <div className="flex items-center gap-2.5">
              {shownLogo && <img src={shownLogo} alt="" referrerPolicy="no-referrer" className="size-9 rounded-lg object-contain" />}
              <p className="text-[15px] font-semibold tracking-tight text-fg">{name || t('Your brand name')}</p>
              <span className="ms-auto inline-flex h-8 items-center rounded-control bg-accent px-3 text-xs font-medium text-on-accent">
                {t('Download config file')}
              </span>
            </div>
          </div>

          {error && <p className="text-sm text-danger-fg">{error}</p>}
          <div className="flex justify-end">
            <Button type="submit" disabled={saveMutation.isPending}>
              {saveMutation.isPending ? t('Saving…') : t('Save branding')}
            </Button>
          </div>
        </form>
      )}
    </Card>
  )
}
