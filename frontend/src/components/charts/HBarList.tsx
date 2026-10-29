export type BarTone = 'accent' | 'secondary' | 'warning' | 'danger' | 'muted'

const toneClass: Record<BarTone, string> = {
  accent: 'bg-[var(--ui-chart-1)]',
  secondary: 'bg-[var(--ui-chart-2)]',
  warning: 'bg-warning',
  danger: 'bg-danger',
  muted: 'bg-edge-strong',
}

export interface BarItem {
  key: string
  label: string
  value: number
  /** What to print at the bar tip; defaults to the number. */
  display?: string
  tone?: BarTone
  /** Extra line under the label (e.g. owner). */
  sub?: string
}

/** Horizontal bars for ranked or bucketed values: one baseline, thin bars (<=10px)
 * with a rounded data end, value at the tip in text ink (never the bar color).
 * Status tones are only for buckets that mean good/bad (quota exceeded, expired). */
export function HBarList({ items, max, empty }: { items: BarItem[]; max?: number; empty: string }) {
  const top = max ?? Math.max(1, ...items.map((i) => i.value))
  if (items.length === 0) return <p className="text-sm text-muted">{empty}</p>
  return (
    <ul className="space-y-3">
      {items.map((item) => {
        const pct = top > 0 ? Math.max(item.value > 0 ? 1.5 : 0, (item.value / top) * 100) : 0
        return (
          <li key={item.key} title={`${item.label}: ${item.display ?? item.value}`}>
            <div className="mb-1.5 flex items-baseline justify-between gap-3 text-sm">
              <span className="min-w-0 truncate text-fg">
                {item.label}
                {item.sub && <span className="ms-1.5 text-xs text-faint">{item.sub}</span>}
              </span>
              <span className="shrink-0 text-xs font-medium text-muted tabular-nums">{item.display ?? item.value}</span>
            </div>
            <div className="h-2 w-full rounded-e-full bg-inset">
              <div
                className={`h-full rounded-e-full transition-[width] duration-500 ${toneClass[item.tone ?? 'accent']}`}
                style={{ width: `${pct}%` }}
              />
            </div>
          </li>
        )
      })}
    </ul>
  )
}
