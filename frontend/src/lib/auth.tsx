import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { apiFetch, getStoredRefreshToken, setStoredRefreshToken } from './api'
import { setAccessToken } from './tokenStore'
import { decodeAccessToken } from './jwt'
import { getPasskey, type RequestOptionsJSON } from './webauthn'

export type AdminRole = 'super_admin' | 'operator' | 'support' | 'reseller'

interface CurrentUser {
  username: string
  role: AdminRole
}

/** Returned by login() when the password was right but a second factor is still needed. */
export interface MfaChallenge {
  mfaToken: string
  methods: string[]
}

interface AuthContextValue {
  isAuthenticated: boolean
  isLoading: boolean
  user: CurrentUser | null
  /** Resolves null on a complete sign-in, or the pending second-factor challenge. */
  login: (username: string, password: string) => Promise<MfaChallenge | null>
  /** Finishes a challenge with an authenticator code (`totp`) or a recovery code (`recovery`). */
  completeMfa: (method: 'totp' | 'recovery', mfaToken: string, code: string) => Promise<void>
  /** Finishes a challenge with a passkey. */
  completeMfaWithPasskey: (mfaToken: string) => Promise<void>
  /** Passwordless sign-in with a discoverable passkey. */
  loginWithPasskey: () => Promise<void>
  logout: () => void
}

const AuthContext = createContext<AuthContextValue | undefined>(undefined)

interface LoginResponse {
  access_token: string
  refresh_token: string
  expires_in_seconds: number
}

interface LoginOrChallenge extends Partial<LoginResponse> {
  mfa_required?: boolean
  mfa_token?: string
  methods?: string[]
}

interface PasskeyBegin<T> {
  session_id: string
  options: T
}

interface RefreshResponse {
  access_token: string
  expires_in_seconds: number
}

function userFromToken(token: string): CurrentUser | null {
  const decoded = decodeAccessToken(token)
  if (!decoded) return null
  return { username: decoded.username, role: decoded.role as AdminRole }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [isAuthenticated, setIsAuthenticated] = useState(false)
  const [isLoading, setIsLoading] = useState(true)
  const [user, setUser] = useState<CurrentUser | null>(null)

  useEffect(() => {
    // The access token is in-memory only, so a page reload always starts without
    // one - if a refresh token survived in localStorage, exchange it silently
    // instead of forcing a full re-login on every reload.
    const refreshToken = getStoredRefreshToken()
    if (!refreshToken) {
      setIsLoading(false)
      return
    }
    apiFetch<RefreshResponse>('/api/v1/auth/refresh', {
      method: 'POST',
      body: JSON.stringify({ refresh_token: refreshToken }),
    })
      .then((data) => {
        setAccessToken(data.access_token)
        setUser(userFromToken(data.access_token))
        setIsAuthenticated(true)
      })
      .catch(() => {
        setStoredRefreshToken(null)
      })
      .finally(() => setIsLoading(false))
  }, [])

  function startSession(data: LoginResponse) {
    setAccessToken(data.access_token)
    setStoredRefreshToken(data.refresh_token)
    setUser(userFromToken(data.access_token))
    setIsAuthenticated(true)
  }

  async function login(username: string, password: string): Promise<MfaChallenge | null> {
    const data = await apiFetch<LoginOrChallenge>('/api/v1/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    })
    if (data.mfa_required && data.mfa_token) {
      return { mfaToken: data.mfa_token, methods: data.methods ?? [] }
    }
    startSession(data as LoginResponse)
    return null
  }

  async function completeMfa(method: 'totp' | 'recovery', mfaToken: string, code: string) {
    const data = await apiFetch<LoginResponse>(`/api/v1/auth/mfa/${method}`, {
      method: 'POST',
      body: JSON.stringify(method === 'totp' ? { mfa_token: mfaToken, code } : { mfa_token: mfaToken, recovery_code: code }),
    })
    startSession(data)
  }

  async function completeMfaWithPasskey(mfaToken: string) {
    const begin = await apiFetch<PasskeyBegin<RequestOptionsJSON>>('/api/v1/auth/mfa/passkey/begin', {
      method: 'POST',
      body: JSON.stringify({ mfa_token: mfaToken }),
    })
    const credential = await getPasskey(begin.options)
    const data = await apiFetch<LoginResponse>(
      `/api/v1/auth/mfa/passkey/finish?session=${encodeURIComponent(begin.session_id)}&mfa_token=${encodeURIComponent(mfaToken)}`,
      { method: 'POST', body: JSON.stringify(credential) },
    )
    startSession(data)
  }

  async function loginWithPasskey() {
    const begin = await apiFetch<PasskeyBegin<RequestOptionsJSON>>('/api/v1/auth/passkey/begin', { method: 'POST' })
    const credential = await getPasskey(begin.options)
    const data = await apiFetch<LoginResponse>(`/api/v1/auth/passkey/finish?session=${encodeURIComponent(begin.session_id)}`, {
      method: 'POST',
      body: JSON.stringify(credential),
    })
    startSession(data)
  }

  function logout() {
    setAccessToken(null)
    setStoredRefreshToken(null)
    setUser(null)
    setIsAuthenticated(false)
  }

  return (
    <AuthContext.Provider value={{ isAuthenticated, isLoading, user, login, completeMfa, completeMfaWithPasskey, loginWithPasskey, logout }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used within AuthProvider')
  return ctx
}
