import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react'
import { fa } from '../locales/fa'

export type Lang = 'en' | 'fa'

const STORAGE_KEY = 'wgpanel-lang'

/** English source text is the message key (t('Sign in')), so untranslated strings fall
 * back to readable English instead of a key name, and retrofitting a page is just
 * wrapping its literals. Persian lives in locales/fa.ts. */
const dictionaries: Record<Lang, Record<string, string>> = { en: {}, fa }

export function readStoredLang(): Lang {
  try {
    const stored = localStorage.getItem(STORAGE_KEY)
    if (stored === 'en' || stored === 'fa') return stored
  } catch {
    // storage blocked - fall through to the browser language
  }
  return navigator.language.toLowerCase().startsWith('fa') ? 'fa' : 'en'
}

function applyLang(lang: Lang) {
  document.documentElement.lang = lang
  document.documentElement.dir = lang === 'fa' ? 'rtl' : 'ltr'
}

type Vars = Record<string, string | number>

function interpolate(text: string, vars?: Vars): string {
  if (!vars) return text
  return text.replace(/\{(\w+)\}/g, (match, name: string) => (name in vars ? String(vars[name]) : match))
}

interface I18nValue {
  lang: Lang
  setLang: (lang: Lang) => void
  /** Translate English source text; `{name}` placeholders are filled from vars. */
  t: (text: string, vars?: Vars) => string
  /** Locale-aware date/time: Persian (Jalali calendar) or English. */
  formatDate: (value: string | number | Date, options?: Intl.DateTimeFormatOptions) => string
}

const I18nContext = createContext<I18nValue | undefined>(undefined)

export function I18nProvider({ children }: { children: ReactNode }) {
  const [lang, setLangState] = useState<Lang>(() => {
    const initial = readStoredLang()
    applyLang(initial)
    return initial
  })

  const setLang = useCallback((next: Lang) => {
    try {
      localStorage.setItem(STORAGE_KEY, next)
    } catch {
      // not persisted - still switch for this session
    }
    applyLang(next)
    setLangState(next)
  }, [])

  const value = useMemo<I18nValue>(() => {
    const dict = dictionaries[lang]
    const locale = lang === 'fa' ? 'fa-IR' : 'en-US'
    return {
      lang,
      setLang,
      t: (text, vars) => interpolate(dict[text] ?? text, vars),
      formatDate: (v, options = { dateStyle: 'medium', timeStyle: 'short' }) =>
        new Intl.DateTimeFormat(locale, options).format(new Date(v)),
    }
  }, [lang, setLang])

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>
}

export function useI18n(): I18nValue {
  const ctx = useContext(I18nContext)
  if (!ctx) throw new Error('useI18n must be used within I18nProvider')
  return ctx
}
