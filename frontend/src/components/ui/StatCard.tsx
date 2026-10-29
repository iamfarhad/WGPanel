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
        <Icon className="h-[18px] w-[18px] shrink-0 text-faint" strokeWidth={1.75} />
      </div>
      <p className="mt-3 text-[22px] leading-none sm:text-[26px] font-semibold tracking-tight text-fg tabular-nums">{value}</p>
      {hint && <p className={`mt-2 text-xs ${positive ? 'font-medium text-success-fg' : 'text-faint'}`}>{hint}</p>}
    </Card>
  )
}
