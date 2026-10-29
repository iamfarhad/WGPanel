import type { ReactNode } from 'react'
import { useI18n } from '../../lib/i18n'

export function PageHeader({
  title,
  description,
  action,
}: {
  title: string
  description?: string
  action?: ReactNode
}) {
  const { t } = useI18n()
  return (
    <div className="mb-7 flex flex-wrap items-end justify-between gap-4">
      <div>
        <h1 className="text-[22px] font-semibold tracking-tight text-fg">{t(title)}</h1>
        {description && <p className="mt-1 max-w-2xl text-sm leading-relaxed text-muted">{t(description)}</p>}
      </div>
      {action}
    </div>
  )
}
