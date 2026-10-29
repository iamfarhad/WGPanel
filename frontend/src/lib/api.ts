import { getAccessToken, setAccessToken } from './tokenStore'

const REFRESH_TOKEN_KEY = 'wgpanel_refresh_token'
const REFRESH_PATH = '/api/v1/auth/refresh'

export function getStoredRefreshToken(): string | null {
  return localStorage.getItem(REFRESH_TOKEN_KEY)
}

export function setStoredRefreshToken(token: string | null): void {
  if (token) localStorage.setItem(REFRESH_TOKEN_KEY, token)
  else localStorage.removeItem(REFRESH_TOKEN_KEY)
}

export class ApiError extends Error {
  status: number
  /** The API's machine-readable error code (body.error.code), e.g.
   * "reseller_account_limit_reached" - see lib/apiErrors.ts. */
  code?: string
  /** Seconds from the Retry-After header (429 too_many_attempts), when present. */
  retryAfter?: number
  constructor(status: number, message: string, code?: string, retryAfter?: number) {
    super(message)
    this.status = status
    this.code = code
    this.retryAfter = retryAfter
  }
}

interface RefreshResponse {
  access_token: string
  expires_in_seconds: number
}

// Coalesces concurrent 401s into a single refresh call rather than one per
// in-flight request.
let refreshInFlight: Promise<boolean> | null = null

async function tryRefresh(): Promise<boolean> {
  const refreshToken = getStoredRefreshToken()
  if (!refreshToken) return false

  if (!refreshInFlight) {
    refreshInFlight = (async () => {
      try {
        const res = await fetch(REFRESH_PATH, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ refresh_token: refreshToken }),
        })
        if (!res.ok) return false
        const data = (await res.json()) as RefreshResponse
        setAccessToken(data.access_token)
        return true
      } catch {
        return false
      } finally {
        refreshInFlight = null
      }
    })()
  }
  return refreshInFlight
}

/** Fetches path, attaching the bearer token and transparently retrying once via
 * refresh on a 401 - callers never see the retry, just the eventual success or a
 * thrown ApiError. */
export async function apiFetch<T>(path: string, init: RequestInit = {}): Promise<T> {
  const doFetch = () => {
    const token = getAccessToken()
    return fetch(path, {
      ...init,
      headers: {
        'Content-Type': 'application/json',
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        ...(init.headers ?? {}),
      },
    })
  }

  let res = await doFetch()

  // Refreshing itself can 401 (invalid/expired refresh token) - don't retry that
  // call against itself.
  if (res.status === 401 && path !== REFRESH_PATH) {
    const refreshed = await tryRefresh()
    if (refreshed) {
      res = await doFetch()
    }
  }

  if (!res.ok) {
    let message = res.statusText
    let code: string | undefined
    try {
      const body = (await res.json()) as { error?: { message?: string; code?: string } }
      message = body?.error?.message ?? message
      code = body?.error?.code
    } catch {
      // Response wasn't JSON - fall back to statusText.
    }
    if (res.status === 401) {
      setAccessToken(null)
      setStoredRefreshToken(null)
    }
    const retry = Number(res.headers.get('Retry-After'))
    throw new ApiError(res.status, message, code, Number.isFinite(retry) && retry > 0 ? retry : undefined)
  }

  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}
