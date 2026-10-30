import { useQuery } from '@tanstack/react-query'
import { apiFetch } from './api'
import type { RoutingFields } from './routing'

/** Shared types and helpers for the account lifecycle features: on-hold accounts,
 * scheduled usage resets, plan templates and bulk actions. */

export type ResetStrategy = 'none' | 'day' | 'week' | 'month' | 'year'

export const resetStrategies: { value: ResetStrategy; label: string }[] = [
  { value: 'none', label: 'Never' },
  { value: 'day', label: 'Every day' },
  { value: 'week', label: 'Every week' },
  { value: 'month', label: 'Every month' },
  { value: 'year', label: 'Every year' },
]

export interface PlanTemplate extends RoutingFields {
  id: string
  name: string
  owner: string | null
  available_to_resellers: boolean
  data_quota_bytes: number | null
  duration_days: number | null
  device_limit: number | null
  bandwidth_limit_mbps: number | null
  node_group: string | null
  reset_strategy: ResetStrategy
  on_hold: boolean
  hold_deadline_days: number | null
  created_at: string
  updated_at: string
}

export function useTemplates() {
  return useQuery({
    queryKey: ['templates'],
    queryFn: () => apiFetch<{ templates: PlanTemplate[] | null }>('/api/v1/templates'),
  })
}

export interface BulkError {
  code: string
  message: string
}

export interface BulkItemResult<A> {
  id: string | null
  label: string
  ok: boolean
  account: A | null
  error: BulkError | null
}

export interface BulkResponse<A> {
  action: string
  total: number
  succeeded: number
  failed: number
  results: BulkItemResult<A>[]
}

export type BulkAction = 'create' | 'renew' | 'suspend' | 'enable' | 'delete' | 'reset_usage' | 'add_tags' | 'remove_tags'

/** Hard cap enforced by the API (maxBulkItems). */
export const MAX_BULK = 200

/** Quote a CSV cell. A leading = + - @ would be run as a formula by spreadsheet apps
 * (labels are user-typed), so such cells get a leading apostrophe. */
export function csvCell(value: string): string {
  const safe = /^[=+\-@\t\r]/.test(value) ? `'${value}` : value
  return `"${safe.replace(/"/g, '""')}"`
}

export interface CsvAccount {
  label: string
  subscription_url: string | null
  subscription_path: string
  expiry_at: string | null
  on_hold: boolean
  hold_duration_days: number | null
  note?: string
  tags?: string[]
}

/** CSV of label, subscription URL, expiry, tags and note for the given accounts. An
 * on-hold account has no expiry yet, so its column says how long it will run once
 * first used. Tags are joined with ";" so the cell stays one field. */
export function accountsCsv(accounts: CsvAccount[], origin: string): string {
  const rows = [['label', 'subscription_url', 'expiry', 'tags', 'note']]
  for (const a of accounts) {
    const url = a.subscription_url ?? `${origin}${a.subscription_path}`
    const expiry = a.expiry_at ?? (a.on_hold && a.hold_duration_days ? `on hold: ${a.hold_duration_days} days from first use` : '')
    rows.push([a.label, url, expiry, (a.tags ?? []).join(';'), a.note ?? ''])
  }
  return rows.map((r) => r.map(csvCell).join(',')).join('\r\n') + '\r\n'
}

export function downloadTextFile(filename: string, text: string, mime = 'text/csv;charset=utf-8') {
  // Leading BOM so Excel reads UTF-8 (Persian labels) correctly.
  const blob = new Blob(['﻿', text], { type: mime })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}
