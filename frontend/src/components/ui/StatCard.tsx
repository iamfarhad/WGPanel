import type { LucideIcon } from 'lucide-react'
import { Card } from './Card'

/** KPI tile: small label, clear number, one quiet icon. `hint` is the line under the
 * number ("of 12 total"); `positive` tints it brand green for good news. */
export function StatCard({
  icon: Icon,
  label,
  value,
  hint,
  positive = false,
}: {
  icon: LucideIcon
  label: string
  value: string | number
  hint?: string
  positive?: boolean
}) {
  return (
    <Card className="p-4 sm:p-5">
      <div className="flex items-center justify-between gap-3">
        <p className="truncate text-[13px] font-medium text-muted">{label}</p>
        <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-accent/10 text-accent-fg">
          <Icon className="h-[17px] w-[17px]" strokeWidth={1.75} />
        </span>
      </div>
      <p className="mt-3 text-[22px] leading-none sm:text-[26px] font-semibold tracking-tight text-fg tabular-nums">{value}</p>
      {hint && <p className={`mt-2 text-xs ${positive ? 'font-medium text-success-fg' : 'text-faint'}`}>{hint}</p>}
    </Card>
  )
}
