const DAY_MS = 24 * 60 * 60 * 1000

export type ExpiryState = 'none' | 'ok' | 'soon' | 'today' | 'expired'

export interface ExpiryInfo {
  state: ExpiryState
  /** Whole days until expiry (positive) or since it (positive, for 'expired'). */
  days: number
}

/** Classifies an expiry timestamp against now. `soonDays` is how close counts as
 * "near expiry" - accounts and reseller plans use different horizons. */
export function expiryInfo(expiresAt: string | null | undefined, soonDays: number, now = Date.now()): ExpiryInfo {
  if (!expiresAt) return { state: 'none', days: 0 }
  const diff = new Date(expiresAt).getTime() - now
  if (diff <= 0) return { state: 'expired', days: Math.floor(-diff / DAY_MS) }
  const days = Math.ceil(diff / DAY_MS)
  if (diff < DAY_MS) return { state: 'today', days: 0 }
  return { state: days <= soonDays ? 'soon' : 'ok', days }
}
