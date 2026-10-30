import { useQuery } from '@tanstack/react-query'
import { apiFetch } from './api'

/** Types and helpers for the abuse controls: account notes/tags, the shared-account
 * heuristic, torrent blocking and the auto-cleanup setting (backend: abuse.go). */

export interface SharingNetwork {
  network: string
  ips: string[]
  nodes: string[]
  last_seen_at: string
}

export interface SharingDetail {
  enabled: boolean
  suspected: boolean
  since?: string
  min_networks: number
  window_minutes: number
  networks: SharingNetwork[]
}

export interface TorrentDetail {
  blocking_enabled: boolean
  hits_24h: number
  hits_30d: number
  last_hit_at: string | null
  recent: { at: string; node_name: string; hits: number }[]
}

/** The extra fields GET /accounts/{id}/devices carries (absent from an older API). */
export interface AccountAbuseExtras {
  device_limit_mode?: 'endpoint' | 'ip'
  device_window_minutes?: number
  sharing?: SharingDetail
  torrent?: TorrentDetail
}

export interface AbuseNode {
  id: string
  name: string
  torrent_block: boolean | null
  torrent_block_effective: boolean
}

export interface AbuseSettings {
  auto_cleanup_enabled: boolean
  auto_cleanup_days: number
  device_limit_mode: 'endpoint' | 'ip'
  device_window_minutes: number
  sharing_detection_enabled: boolean
  sharing_min_networks: number
  sharing_window_minutes: number
  torrent_block_enabled: boolean
  torrent_suspend_enabled: boolean
  torrent_suspend_hits: number
  torrent_suspend_window_minutes: number
  nodes: AbuseNode[]
}

export function useAbuseSettings(enabled = true) {
  return useQuery({
    queryKey: ['abuse-settings'],
    queryFn: () => apiFetch<AbuseSettings>('/api/v1/abuse-settings'),
    enabled,
  })
}

export const MAX_TAG_LENGTH = 32
export const MAX_TAGS = 20

/** Mirrors the API's NormalizeTags for one tag so the UI shows what will be stored:
 * lowercase, whitespace -> "-", only letters/digits/_-:. kept. */
export function normalizeTag(raw: string): string {
  return raw
    .trim()
    .toLowerCase()
    .replace(/\s+/g, '-')
    .replace(/[^\p{L}\p{N}_:.-]/gu, '')
    .slice(0, MAX_TAG_LENGTH)
}

/** Splits free text on commas (Latin and Arabic) into normalized, de-duplicated tags. */
export function parseTags(text: string): string[] {
  const out: string[] = []
  for (const part of text.split(/[,،\n]/)) {
    const t = normalizeTag(part)
    if (t && !out.includes(t)) out.push(t)
  }
  return out.sort()
}

export function sameTags(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((t, i) => t === b[i])
}
