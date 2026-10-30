import { useQuery } from '@tanstack/react-query'
import { apiFetch } from './api'

/** AmneziaWG parameters; 0 / "" = not set. Mirrors backend/internal/obfuscation. */
export interface ObfuscationParams {
  jc: number
  jmin: number
  jmax: number
  s1: number
  s2: number
  s3: number
  s4: number
  h1: string
  h2: string
  h3: string
  h4: string
  i1: string
  i2: string
  i3: string
  i4: string
  i5: string
}

export type ObfuscationCompat = 'awg1' | 'awg2'

export interface ObfuscationProfile {
  id: string
  name: string
  compat: ObfuscationCompat
  params: ObfuscationParams
  revision: number
  node_count: number
  stock_client_compatible: boolean
  created_at: string
  updated_at: string
}

export interface ObfuscationPreset {
  id: string
  name: string
  description: string
  compat: ObfuscationCompat
  stock_client_compatible: boolean
}

/** The obfuscation block of a node as the nodes API returns it. `mode` is what the
 * agent last reported running; `applied` means client configs carry the AWG params. */
export interface NodeObfuscationInfo {
  mode: 'wireguard' | 'awg'
  profile_id?: string
  profile_name?: string
  profile_compat?: ObfuscationCompat
  profile_revision?: number
  applied_revision?: number
  applied: boolean
  pending: boolean
  supported: boolean
  error?: string
}

export const emptyParams: ObfuscationParams = {
  jc: 0, jmin: 0, jmax: 0, s1: 0, s2: 0, s3: 0, s4: 0,
  h1: '', h2: '', h3: '', h4: '', i1: '', i2: '', i3: '', i4: '', i5: '',
}

export function useObfuscationProfiles(enabled = true) {
  return useQuery({
    queryKey: ['obfuscation-profiles'],
    queryFn: () => apiFetch<{ profiles: ObfuscationProfile[] | null }>('/api/v1/obfuscation/profiles'),
    enabled,
  })
}

export function useObfuscationPresets() {
  return useQuery({
    queryKey: ['obfuscation-presets'],
    queryFn: () => apiFetch<{ presets: ObfuscationPreset[] }>('/api/v1/obfuscation/presets'),
    staleTime: Infinity,
  })
}

/** Staff roles that may manage profiles (the API needs operator for writes). */
export function canManageObfuscation(role: string | undefined): boolean {
  return role === 'super_admin' || role === 'operator'
}

export const compatLabels: Record<ObfuscationCompat, string> = {
  awg1: 'AWG 1.x',
  awg2: 'AWG 2.0',
}
