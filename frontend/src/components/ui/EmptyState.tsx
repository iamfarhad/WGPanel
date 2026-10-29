import type { LucideIcon } from 'lucide-react'
import type { ReactNode } from 'react'
import { Mascot } from '../brand/Brand'

/** Empty list/search state. `mascot` swaps the icon for the Jirjirak mascot - reserve
 * it for "nothing here yet" first-run moments (no servers, no accounts), not for
 * search misses or secondary lists. */
export function EmptyState({
  icon: Icon,
  mascot = false,
  title,
  description,
  action,
}: {
  icon?: LucideIcon
  mascot?: boolean
  title: string
  description?: string
  action?: ReactNode
}) {
  return (
    <div className={`flex flex-col items-center justify-center px-6 text-center ${mascot ? 'py-12' : 'py-16'}`}>
      {mascot ? (
        <Mascot width={200} className="mb-3" />
      ) : (
        Icon && (
          <div className="mb-4 flex h-11 w-11 items-center justify-center rounded-card border border-edge bg-inset">
            <Icon className="h-5 w-5 text-faint" strokeWidth={1.75} />
          </div>
        )
      )}
      <p className="text-[15px] font-semibold text-fg">{title}</p>
      {description && <p className="mt-1 max-w-sm text-sm leading-relaxed text-muted">{description}</p>}
      {action && <div className="mt-5">{action}</div>}
    </div>
  )
}
