import { useI18n } from './i18n'
import { formatToman } from './sales'

/** "1,234,000 Toman" / "۱٬۲۳۴٬۰۰۰ تومان" - amount formatting for the current UI language. */
export function useToman(): (n: number) => string {
  const { t, lang } = useI18n()
  return (n: number) => t('{amount} Toman', { amount: formatToman(n, lang) })
}
