/** Shared reseller shapes and tree helpers for the staff Resellers page and the
 * reseller's own "My resellers" page. */

export interface ResellerRow {
  id: string
  username: string
  namespace: string
  disabled: boolean
  notes: string
  max_accounts: number | null
  data_limit_bytes: number | null
  node_groups: string[] | null
  // Plan end: after it the reseller and its API keys are blocked (accounts keep
  // working), and no account it creates may expire later than this. null = never.
  expires_at: string | null
  usage: { accounts_used: number; data_allocated_bytes: number; data_used_bytes: number }
  // Sub-resellers. The reserved_* numbers are what the reseller already handed to its
  // children; effective_* fold in its ancestors (a disabled/expired parent blocks the
  // whole subtree); disabled_by_staff means its parent cannot re-enable it.
  parent_id: string | null
  depth: number
  children: number
  reserved_accounts: number
  reserved_data_bytes: number
  effective_disabled: boolean
  effective_expires_at: string | null
  disabled_by_staff: boolean
  created_at: string
}

/** Mirrors store.MaxResellerDepth: a top-level reseller is depth 1. */
export const MAX_RESELLER_DEPTH = 3

// The reseller API takes limits in decimal GB (data_limit_gb) and returns bytes, like
// the accounts API's quotas - so everything is shown in decimal GB too.
export const BYTES_PER_GB = 1_000_000_000

export function formatGB(bytes: number): string {
  const gb = bytes / BYTES_PER_GB
  return `${gb >= 100 ? Math.round(gb) : Number(gb.toFixed(gb >= 10 ? 1 : 2))} GB`
}

/** Empty input means "no limit"; the API takes null for unlimited. */
export function toLimit(value: string): number | null {
  const trimmed = value.trim()
  return trimmed === '' ? null : Number(trimmed)
}

export interface TreeRow {
  reseller: ResellerRow
  /** 0 for a root of the given list (its parent is not in it), 1 for its children, ... */
  level: number
}

/** Orders a flat list depth-first (a parent directly above its children) and assigns
 * display levels. A reseller whose parent is not in the list is a root, which is how a
 * reseller's own view of its descendants starts at its children. */
export function flattenTree(list: ResellerRow[]): TreeRow[] {
  const ids = new Set(list.map((r) => r.id))
  const kids = new Map<string, ResellerRow[]>()
  const roots: ResellerRow[] = []
  for (const r of list) {
    if (r.parent_id && ids.has(r.parent_id)) {
      const arr = kids.get(r.parent_id) ?? []
      arr.push(r)
      kids.set(r.parent_id, arr)
    } else {
      roots.push(r)
    }
  }
  const out: TreeRow[] = []
  const walk = (rows: ResellerRow[], level: number) => {
    for (const r of rows) {
      out.push({ reseller: r, level })
      walk(kids.get(r.id) ?? [], level + 1)
    }
  }
  walk(roots, 0)
  return out
}
