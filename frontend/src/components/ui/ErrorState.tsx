import { RotateCw } from 'lucide-react'
import { Illustration } from '../brand/Brand'
import { Button } from './Button'
import { useI18n } from '../../lib/i18n'

/** Failed-to-load state for a page section: the puzzled mascot, what failed, and a
 * retry when the caller can refetch. */
export function ErrorState({ title, onRetry }: { title: string; onRetry?: () => void }) {
  const { t } = useI18n()
  return (
    <div className="flex flex-col items-center justify-center px-6 py-12 text-center" role="alert">
      <Illustration name="error" size={140} className="mb-3" />
      <p className="text-[15px] font-semibold text-fg">{t(title)}</p>
      <p className="mt-1 max-w-sm text-sm leading-relaxed text-muted">
        {t('Check your connection to the panel and try again.')}
      </p>
      {onRetry && (
        <Button variant="secondary" size="sm" className="mt-5" onClick={onRetry}>
          <RotateCw className="h-3.5 w-3.5" />
          {t('Try again')}
        </Button>
      )}
    </div>
  )
}
