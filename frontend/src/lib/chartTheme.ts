import type { CSSProperties } from 'react'

/** Shared Recharts styling, all from the theme tokens so charts follow light/dark:
 * brand green first, blue second, near-invisible grid, quiet axes, minimal tooltip. */
export const chartColors = {
  primary: 'var(--ui-chart-1)',
  secondary: 'var(--ui-chart-2)',
}

export const chartGrid = { strokeDasharray: '3 3', stroke: 'var(--ui-edge)', strokeOpacity: 0.6, vertical: false }

export const chartAxis = {
  tick: { fontSize: 11, fill: 'var(--ui-faint)' },
  stroke: 'var(--ui-edge)',
  tickLine: false,
}

export const chartTooltip: { contentStyle: CSSProperties; labelStyle: CSSProperties; cursor: { stroke: string } } = {
  contentStyle: {
    fontSize: 12,
    backgroundColor: 'var(--ui-surface)',
    border: '1px solid var(--ui-edge)',
    borderRadius: 10,
    boxShadow: 'var(--ui-shadow-pop)',
    color: 'var(--ui-fg)',
    padding: '8px 10px',
  },
  labelStyle: { color: 'var(--ui-muted)', marginBottom: 4 },
  cursor: { stroke: 'var(--ui-edge-strong)' },
}

export const chartLegend = { wrapperStyle: { fontSize: 12, color: 'var(--ui-muted)' } }
