import type { HTMLAttributes, ReactNode } from 'react'
import { useI18n } from '../../lib/i18n'

export function Card({ className = '', ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={`rounded-card border border-edge bg-surface shadow-card ${className}`} {...props} />
}

/** Card heading row - title (500) plus an optional trailing control. */
export function CardHeader({ title, description, action }: { title: string; description?: string; action?: ReactNode }) {
  const { t } = useI18n()
  return (
    <div className="flex items-center justify-between gap-4 border-b border-edge px-5 py-4">
      <div className="min-w-0">
        <h2 className="text-sm font-semibold text-fg">{t(title)}</h2>
        {description && <p className="mt-0.5 text-xs text-muted">{t(description)}</p>}
      </div>
      {action}
    </div>
  )
}
