import { createContext, Fragment, useCallback, useContext, useMemo, useState, type ReactNode } from 'react'
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
  /** Translate a whole sentence that contains markup, e.g.
   * tx('Restore replaces <b>all</b> data', { b: (c) => <strong>{c}</strong> }). The
   * translator writes the full sentence in natural word order, tags included -
   * splitting it into t() fragments forces English word order onto Persian. */
  tx: (text: string, tags: Record<string, (children: string) => ReactNode>) => ReactNode
  /** Locale-aware date/time: Persian (Jalali calendar) or English. */
  formatDate: (value: string | number | Date, options?: Intl.DateTimeFormatOptions) => string
}

function renderTags(text: string, tags: Record<string, (children: string) => ReactNode>): ReactNode {
  const parts: ReactNode[] = []
  const re = /<(\w+)>(.*?)<\/\1>/g
  let last = 0
  let match: RegExpExecArray | null
  while ((match = re.exec(text)) !== null) {
    if (match.index > last) parts.push(text.slice(last, match.index))
    const render = tags[match[1]]
    parts.push(<Fragment key={match.index}>{render ? render(match[2]) : match[2]}</Fragment>)
    last = re.lastIndex
  }
  if (last < text.length) parts.push(text.slice(last))
  return parts
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
      tx: (text, tags) => renderTags(dict[text] ?? text, tags),
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
