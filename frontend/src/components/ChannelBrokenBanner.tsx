import { Link } from 'react-router-dom'
import { AlertTriangle } from 'lucide-react'
import { useI18n } from '../lib/i18n'
import { useNotifications } from '../lib/notifications'

/** Persistent banner shown to staff while an enabled notification channel keeps
 * failing, so a broken Telegram bot or SMTP login does not go unnoticed. */
export function ChannelBrokenBanner() {
  const { t } = useI18n()
  const { data } = useNotifications()
  const broken = data?.broken_channels ?? 0
  if (broken === 0) return null
  return (
    <div
      role="alert"
      className="relative mx-auto mt-5 flex w-full max-w-7xl items-start gap-3 rounded-card border border-warning/30 bg-warning-soft px-4 py-3 text-sm text-warning-fg sm:mt-8 sm:px-5 lg:mt-10"
    >
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
      <p className="min-w-0 flex-1 leading-relaxed">
        {t('A notification channel keeps failing, so alerts may not be reaching you. Check its credentials and send a test message.')}
      </p>
      <Link to="/settings" className="shrink-0 font-semibold underline underline-offset-2">
        {t('Open notification settings')}
      </Link>
    </div>
  )
}
