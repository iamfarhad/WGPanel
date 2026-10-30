import type { CSSProperties } from 'react'

function hexToRgb(hex: string): [number, number, number] | null {
  const m = /^#([0-9a-f]{6})$/i.exec(hex)
  if (!m) return null
  const n = parseInt(m[1], 16)
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
}

function shade([r, g, b]: [number, number, number], factor: number): string {
  const f = (c: number) => Math.max(0, Math.min(255, Math.round(c * factor)))
  return `rgb(${f(r)} ${f(g)} ${f(b)})`
}

/** The theme tokens a brand accent colour drives (see index.css --ui-accent*). Text on
 * the accent flips between white and near-black by WCAG relative luminance so buttons
 * stay legible whatever colour a reseller picks. */
export function accentStyle(hex: string | undefined): CSSProperties | undefined {
  const rgb = hex ? hexToRgb(hex) : null
  if (!rgb) return undefined
  const lin = (c: number) => {
    const v = c / 255
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
  }
  const lum = 0.2126 * lin(rgb[0]) + 0.7152 * lin(rgb[1]) + 0.0722 * lin(rgb[2])
  return {
    '--ui-accent': hex,
    '--ui-accent-hover': shade(rgb, 0.9),
    '--ui-accent-active': shade(rgb, 0.8),
    '--ui-accent-soft': `rgb(${rgb[0]} ${rgb[1]} ${rgb[2]} / 0.12)`,
    '--ui-accent-fg': hex,
    '--ui-on-accent': lum > 0.4 ? '#111827' : '#ffffff',
  } as CSSProperties
}
