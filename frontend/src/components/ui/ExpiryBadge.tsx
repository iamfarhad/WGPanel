import { CalendarClock } from 'lucide-react'
import { expiryInfo } from '../../lib/expiry'
import { useI18n } from '../../lib/i18n'
import { Badge } from './Badge'

/** Days left until an expiry, colored by urgency: quiet when far, amber when near
 * (within soonDays), red once past. The exact date is in the tooltip. */
export function ExpiryBadge({ expiresAt, soonDays = 3 }: { expiresAt: string | null | undefined; soonDays?: number }) {
  const { t, formatDate } = useI18n()
  const info = expiryInfo(expiresAt, soonDays)
  if (info.state === 'none') return <span className="text-xs text-faint">{t('No expiry')}</span>

  const title = formatDate(expiresAt!, { dateStyle: 'medium' })
  switch (info.state) {
    case 'expired':
      return (
        <Badge tone="red" title={title}>
          {info.days === 0 ? t('Expired today') : t('Expired {n} days ago', { n: info.days })}
        </Badge>
      )
    case 'today':
      return (
        <Badge tone="amber" title={title}>
          {t('Expires today')}
        </Badge>
      )
    case 'soon':
      return (
        <Badge tone="amber" title={title}>
          <CalendarClock className="h-3 w-3" />
          {t('{n} days left', { n: info.days })}
        </Badge>
      )
    default:
      return (
        <span className="text-xs text-muted tabular-nums" title={title}>
          {t('{n} days left', { n: info.days })}
        </span>
      )
  }
}
