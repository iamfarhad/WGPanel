import { useState, type ReactNode } from 'react'
import { BarChart3, Table2 } from 'lucide-react'
import { Card } from '../ui/Card'
import { useI18n } from '../../lib/i18n'

/** A chart with its table twin: every value a chart shows stays reachable without
 * hovering (and for screen readers) through the table view. `busy` dims the previous
 * render while a new range loads, instead of flashing a skeleton. */
export function ChartCard({
  title,
  description,
  table,
  busy = false,
  className = '',
  children,
}: {
  title: string
  description?: string
  table?: ReactNode
  busy?: boolean
  className?: string
  children: ReactNode
}) {
  const { t } = useI18n()
  const [view, setView] = useState<'chart' | 'table'>('chart')
  return (
    <Card className={`flex flex-col ${className}`}>
      <div className="flex items-start justify-between gap-4 px-5 pt-4">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold text-fg">{t(title)}</h2>
          {description && <p className="mt-0.5 text-xs text-muted">{t(description)}</p>}
        </div>
        {table && (
          <div className="flex shrink-0 items-center gap-0.5 rounded-lg bg-inset p-0.5" role="group" aria-label={t('View')}>
            {(['chart', 'table'] as const).map((v) => {
              const Icon = v === 'chart' ? BarChart3 : Table2
              return (
                <button
                  key={v}
                  type="button"
                  aria-pressed={view === v}
                  title={v === 'chart' ? t('Chart') : t('Table')}
                  onClick={() => setView(v)}
                  className={`flex h-6 w-7 cursor-pointer items-center justify-center rounded-md transition-colors duration-150 ${
                    view === v ? 'bg-surface text-fg shadow-xs ring-1 ring-edge' : 'text-faint hover:text-muted'
                  }`}
                >
                  <Icon className="h-3.5 w-3.5" />
                </button>
              )
            })}
          </div>
        )}
      </div>
      <div className={`flex-1 p-5 transition-opacity duration-200 ${busy ? 'opacity-60' : ''}`}>
        {view === 'table' && table ? <div className="max-h-80 overflow-auto">{table}</div> : children}
      </div>
    </Card>
  )
}
