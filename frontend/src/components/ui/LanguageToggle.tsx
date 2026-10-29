import { useI18n, type Lang } from '../../lib/i18n'

const options: { lang: Lang; label: string; title: string }[] = [
  { lang: 'fa', label: 'فا', title: 'فارسی' },
  { lang: 'en', label: 'EN', title: 'English' },
]

/** Two-option segmented switch, styled like the theme toggle. Labels are each
 * language's own name, so it reads right whatever language is active. */
export function LanguageToggle({ className = '' }: { className?: string }) {
  const { lang, setLang, t } = useI18n()
  return (
    <div className={`flex items-center gap-1 rounded-lg bg-inset p-1 ${className}`} role="group" aria-label={t('Language')}>
      {options.map((o) => {
        const active = lang === o.lang
        return (
          <button
            key={o.lang}
            type="button"
            title={o.title}
            lang={o.lang}
            aria-pressed={active}
            onClick={() => setLang(o.lang)}
            className={`flex h-7 min-w-9 flex-1 cursor-pointer items-center justify-center rounded-md px-2 text-xs font-medium transition-colors duration-150 ${
              active ? 'bg-surface text-fg shadow-xs ring-1 ring-edge' : 'text-faint hover:text-muted'
            }`}
          >
            {o.label}
          </button>
        )
      })}
    </div>
  )
}
