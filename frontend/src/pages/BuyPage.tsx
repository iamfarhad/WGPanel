import { useEffect } from 'react'
import { useI18n, readStoredLang } from '../lib/i18n'
import { BrandLockup } from '../components/brand/Brand'
import { LanguageToggle } from '../components/ui/LanguageToggle'
import { PublicCheckout } from '../components/sales/PublicCheckout'

/** Public purchase page (/buy): pick a plan, pay, get the subscription link. Off by
 * default; the server answers an empty, disabled shop until an admin turns it on. */
export function BuyPage() {
  const { t, lang, setLang } = useI18n()

  // Same default as the subscription page: its audience is mostly Persian speakers.
  useEffect(() => {
    let stored: string | null = null
    try {
      stored = localStorage.getItem('wgpanel-lang')
    } catch {
      // storage blocked - treat as no stored choice
    }
    if (!stored && readStoredLang() !== 'fa') setLang('fa')
    // run once on mount
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <div className="min-h-dvh bg-bg pb-12">
      <header className="mx-auto flex w-full max-w-xl items-center justify-between gap-3 px-4 pt-5">
        <BrandLockup />
        <LanguageToggle />
      </header>
      <main className="mx-auto mt-5 w-full max-w-xl space-y-4 px-4">
        <div>
          <h1 className="text-lg font-semibold text-fg">{t('Buy a subscription')}</h1>
          <p className="mt-1 text-sm leading-relaxed text-muted">{t('Choose a plan and pay. Your connection details appear right after the payment is confirmed.')}</p>
        </div>
        <PublicCheckout base="/api/v1/sub/buy" needContact title="Choose a plan" storageKey="wgpanel-buy-order" />
        <p className="text-center text-[11px] text-faint" lang={lang}>
          Jirjirak WireGuard
        </p>
      </main>
    </div>
  )
}
