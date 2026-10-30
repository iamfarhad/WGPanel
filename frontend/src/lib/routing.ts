/** Split-tunnel routing: shared types and form helpers for accounts, bulk create and plan
 * templates. The mode decides what the client puts in AllowedIPs. */

export type RoutingMode = 'full' | 'bypass_iran' | 'custom'
export type RoutingPrecision = 'compact' | 'exact'

export const routingModes: { value: RoutingMode; label: string }[] = [
  { value: 'full', label: 'Full tunnel' },
  { value: 'bypass_iran', label: 'Iran direct' },
  { value: 'custom', label: 'Custom exclusions' },
]

/** The API fields carried by accounts and templates. */
export interface RoutingFields {
  routing_mode: RoutingMode | null
  routing_excluded_cidrs: string[]
  routing_precision: RoutingPrecision | null
}

/** Form state. mode '' = "not chosen": the default (full tunnel), or the template's mode on
 * create. precision '' = compact. */
export interface RoutingState {
  mode: RoutingMode | ''
  cidrs: string
  precision: RoutingPrecision | ''
}

export const emptyRouting: RoutingState = { mode: '', cidrs: '', precision: '' }

/** CIDRs typed one per line, or separated by commas/spaces. */
export function parseCidrs(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean)
}

export function routingFrom(src: Partial<RoutingFields> | undefined): RoutingState {
  if (!src) return emptyRouting
  return {
    mode: src.routing_mode ?? '',
    cidrs: (src.routing_excluded_cidrs ?? []).join('\n'),
    precision: src.routing_precision ?? '',
  }
}

/** Request fields for create / bulk create / template save. Only what the form set is sent. */
export function routingBody(s: RoutingState): Record<string, unknown> {
  const body: Record<string, unknown> = {}
  if (s.mode) body.routing_mode = s.mode
  if (s.mode === 'custom') body.routing_excluded_cidrs = parseCidrs(s.cidrs)
  if (s.mode && s.mode !== 'full' && s.precision) body.routing_precision = s.precision
  return body
}

/** PATCH fields for an account: only what differs from the saved state. A cleared mode is
 * sent as "" (the API's "back to the default"). */
export function routingPatch(initial: RoutingState, current: RoutingState): Record<string, unknown> {
  const changes: Record<string, unknown> = {}
  const same = (a: string[], b: string[]) => a.length === b.length && a.every((x, i) => x === b[i])
  const cidrsChanged = !same(parseCidrs(initial.cidrs), parseCidrs(current.cidrs))
  if (current.mode !== initial.mode) changes.routing_mode = current.mode
  if (current.mode === 'custom' && (cidrsChanged || current.mode !== initial.mode)) {
    changes.routing_excluded_cidrs = parseCidrs(current.cidrs)
  }
  if (current.precision !== initial.precision) changes.routing_precision = current.precision
  return changes
}

/** Short label for a table cell or badge. */
export function routingLabel(mode: RoutingMode | null | undefined): string {
  return routingModes.find((m) => m.value === (mode ?? 'full'))?.label ?? 'Full tunnel'
}

/** Customer-facing wording for the public subscription page. */
export function routingText(mode: RoutingMode | undefined): string {
  switch (mode) {
    case 'bypass_iran':
      return 'Iranian sites connect directly'
    case 'custom':
      return 'Some addresses connect directly'
    default:
      return 'All traffic through the VPN'
  }
}

export interface NodeIPv6 {
  node_id: string
  ipv6_enabled: boolean
  ipv6_ula_prefix: string | null
  ipv6_host_global: boolean
  ipv6_ready: boolean
  ipv6_active: boolean
  psk_supported: boolean
}
