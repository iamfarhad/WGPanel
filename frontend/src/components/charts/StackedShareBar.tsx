import type { BarTone } from './HBarList'

const toneClass: Record<BarTone, string> = {
  accent: 'bg-[var(--ui-chart-1)]',
  secondary: 'bg-[var(--ui-chart-2)]',
  warning: 'bg-warning',
  danger: 'bg-danger',
  muted: 'bg-edge-strong',
}

export interface SharePart {
  key: string
  label: string
  value: number
  tone: BarTone
}

/** Part-to-whole as one horizontal 100% bar: segments separated by a 2px surface gap
 * (never a stroke), with a legend that carries label, count and share - so identity
 * never rests on color alone and every value is readable without hovering. */
export function StackedShareBar({ parts, empty }: { parts: SharePart[]; empty: string }) {
  const total = parts.reduce((n, p) => n + p.value, 0)
  if (total === 0) return <p className="text-sm text-muted">{empty}</p>
  const visible = parts.filter((p) => p.value > 0)
  return (
    <div>
      <div className="flex h-3 w-full gap-[2px] overflow-hidden rounded-full">
        {visible.map((p) => (
          <div
            key={p.key}
            title={`${p.label}: ${p.value}`}
            className={`h-full first:rounded-s-full last:rounded-e-full ${toneClass[p.tone]}`}
            style={{ width: `${(p.value / total) * 100}%` }}
          />
        ))}
      </div>
      <ul className="mt-4 grid grid-cols-1 gap-2 sm:grid-cols-2">
        {parts.map((p) => (
          <li key={p.key} className="flex items-center gap-2 text-sm">
            <span className={`h-2.5 w-2.5 shrink-0 rounded-[3px] ${toneClass[p.tone]}`} />
            <span className="min-w-0 flex-1 truncate text-muted">{p.label}</span>
            <span className="font-medium text-fg tabular-nums">{p.value}</span>
            <span className="w-11 text-end text-xs text-faint tabular-nums">{Math.round((p.value / total) * 100)}%</span>
          </li>
        ))}
      </ul>
    </div>
  )
}
