import type { ReactNode } from 'react'
import { Megaphone } from 'lucide-react'
import { BrandLockup } from '../brand/Brand'
import { useI18n } from '../../lib/i18n'
import { accentStyle } from '../../lib/brandAccent'

/** The owning reseller's (or the panel owner's) branding, as /api/v1/sub/{token}/info
 * returns it. It carries no ids and nothing about any other reseller. */
export interface PublicBrand {
  name: string
  logo_url: string | null
  support_label: string
  support_url: string
  announcement: string
  accent_color: string
}

/** One wrapper for the whole public page: applies the accent colour to every token below
 * it and shows the announcement strip on top. Renders its children untouched when there is
 * no brand, so the unbranded page is exactly what it was. */
export function BrandFrame({ brand, children }: { brand: PublicBrand | null | undefined; children: ReactNode }) {
  return (
    <div className="contents" style={accentStyle(brand?.accent_color)}>
      {brand?.announcement && (
        <div className="bg-accent-soft px-4 py-2.5 text-accent-fg">
          <p className="mx-auto flex w-full max-w-xl items-start gap-2 text-sm leading-relaxed">
            <Megaphone className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
            {/* Plain text only (React escapes it); newlines are kept. */}
            <span className="whitespace-pre-line text-fg">{brand.announcement}</span>
          </p>
        </div>
      )}
      {children}
    </div>
  )
}

/** Header mark: the brand's logo and name, or the Jirjirak lockup when unbranded. */
export function BrandMark({ brand }: { brand: PublicBrand | null | undefined }) {
  if (!brand || (!brand.name && !brand.logo_url)) return <BrandLockup />
  return (
    <div className="flex min-w-0 items-center gap-2.5">
      {brand.logo_url && (
        // referrerPolicy: an external logo host must never see the page's secret URL.
        <img
          src={brand.logo_url}
          alt=""
          referrerPolicy="no-referrer"
          className="size-9 shrink-0 rounded-lg object-contain"
        />
      )}
      {brand.name && <p className="truncate text-[15px] font-semibold tracking-tight text-fg">{brand.name}</p>}
    </div>
  )
}

/** "Need help?" footer line: a link when the brand gave one, otherwise the plain text
 * contact the panel has always shown. */
export function BrandSupport({ brand, contact }: { brand: PublicBrand | null | undefined; contact: string | null }) {
  const { t } = useI18n()
  if (brand?.support_url) {
    return (
      <p className="text-center text-xs text-muted">
        {t('Need help?')}{' '}
        <a
          href={brand.support_url}
          target="_blank"
          rel="noopener noreferrer"
          className="ltr font-mono text-accent-fg underline-offset-2 hover:underline"
        >
          {brand.support_label}
        </a>
      </p>
    )
  }
  if (!contact) return null
  return (
    <p className="text-center text-xs text-muted">
      {t('Need help?')} <span className="ltr font-mono text-fg">{contact}</span>
    </p>
  )
}
