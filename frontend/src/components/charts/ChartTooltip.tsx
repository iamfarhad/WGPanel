import type { ReactNode } from 'react'

interface Item {
  name?: ReactNode
  value?: number | string | (number | string)[]
  color?: string
  dataKey?: string | number
}

/** Recharts tooltip content: value first (the reader already knows the series), a
 * short line key in the series color, and every series at that X in one readout. */
export function ChartTooltip({
  active,
  payload,
  label,
  formatValue = (v) => String(v),
  formatLabel = (l) => String(l),
}: {
  active?: boolean
  payload?: Item[]
  label?: string | number
  formatValue?: (value: number) => string
  formatLabel?: (label: string | number) => string
}) {
  if (!active || !payload || payload.length === 0) return null
  return (
    <div className="min-w-36 rounded-control border border-edge bg-surface px-3 py-2 text-xs shadow-pop">
      {label !== undefined && <p className="mb-1.5 text-muted">{formatLabel(label)}</p>}
      <ul className="space-y-1">
        {payload.map((item) => (
          <li key={String(item.dataKey)} className="flex items-center gap-2">
            <span className="h-0.5 w-3 shrink-0 rounded-full" style={{ backgroundColor: item.color }} />
            <span className="font-semibold text-fg tabular-nums">{formatValue(Number(item.value ?? 0))}</span>
            <span className="text-muted">{item.name}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}
