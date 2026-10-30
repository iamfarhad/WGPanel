import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { apiFetch } from '../../lib/api'
import { apiErrorMessage } from '../../lib/apiErrors'
import { useI18n } from '../../lib/i18n'
import { useToast } from '../../lib/toast'
import { Button } from '../ui/Button'
import { Dialog } from '../ui/Dialog'
import { Field } from '../ui/Field'
import { Input } from '../ui/Input'
import { Select } from '../ui/Select'
import { BYTES_PER_GB, MAX_RESELLER_DEPTH, formatGB, toLimit, type ResellerRow } from '../../lib/resellerTree'
import { ltr } from '../../lib/bidi'

interface NodeSummary {
  node_group: string
}

/** What the signed-in reseller has left to give a new child (null = unlimited). */
export interface ChildRoom {
  accounts: number | null
  bytes: number | null
  expiresAt: string | null
  nodeGroups: string[]
}

/** Create/edit dialog for resellers, shared by the staff page and a reseller's own
 * "My resellers" page.
 *
 * `child` mode talks to /reseller/children (parent = the signed-in reseller), cannot
 * set passwords, and shows what is left of the caller's own plan - a child's limits are
 * carved out of it. Staff mode can pick a parent when creating. */
export function ResellerFormDialog({
  open,
  reseller,
  onClose,
  onSaved,
  mode = 'staff',
  room,
  parents = [],
}: {
  open: boolean
  reseller: ResellerRow | null
  onClose: () => void
  onSaved: () => void
  mode?: 'staff' | 'child'
  room?: ChildRoom
  /** Staff mode: resellers a new one may be placed under (the ones with depth room left). */
  parents?: ResellerRow[]
}) {
  const { t } = useI18n()
  const { push } = useToast()
  const editing = reseller !== null
  const child = mode === 'child'
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [parentId, setParentId] = useState('')
  const [maxAccounts, setMaxAccounts] = useState('')
  const [dataLimitGB, setDataLimitGB] = useState('')
  const [expiresOn, setExpiresOn] = useState('') // yyyy-mm-dd, '' = never
  const [groups, setGroups] = useState<string[]>([])
  const [notes, setNotes] = useState('')
  const [error, setError] = useState<string | null>(null)

  const nodesQuery = useQuery({
    queryKey: ['nodes'],
    queryFn: () => apiFetch<{ nodes: NodeSummary[] | null }>('/api/v1/nodes'),
    enabled: open,
  })
  const availableGroups = useMemo(() => {
    const all = Array.from(new Set((nodesQuery.data?.nodes ?? []).map((n) => n.node_group))).sort()
    // A child may only use groups its parent has.
    return child && room && room.nodeGroups.length > 0 ? all.filter((g) => room.nodeGroups.includes(g)) : all
  }, [nodesQuery.data, child, room])

  // The dialog stays mounted between opens; reset to the selected reseller each time.
  useEffect(() => {
    if (!open) return
    setUsername(reseller?.username ?? '')
    setPassword('')
    setParentId('')
    setMaxAccounts(reseller?.max_accounts?.toString() ?? '')
    setDataLimitGB(reseller?.data_limit_bytes != null ? String(reseller.data_limit_bytes / BYTES_PER_GB) : '')
    setExpiresOn(reseller?.expires_at ? reseller.expires_at.slice(0, 10) : '')
    setGroups(reseller?.node_groups ?? [])
    setNotes(reseller?.notes ?? '')
    setError(null)
  }, [open, reseller])

  const saveMutation = useMutation({
    mutationFn: () => {
      const limits = {
        max_accounts: toLimit(maxAccounts),
        data_limit_gb: toLimit(dataLimitGB),
        // End of the chosen day, like account expiry dates.
        expires_at: expiresOn ? new Date(`${expiresOn}T23:59:59`).toISOString() : null,
        node_groups: groups,
        notes,
      }
      const base = child ? '/api/v1/reseller/children' : '/api/v1/resellers'
      if (editing) {
        return apiFetch<ResellerRow>(`${base}/${reseller.id}`, {
          method: 'PATCH',
          body: JSON.stringify({ ...limits, ...(password && !child ? { password } : {}) }),
        })
      }
      return apiFetch<ResellerRow>(base, {
        method: 'POST',
        body: JSON.stringify({ username, password, ...limits, ...(parentId && !child ? { parent_id: parentId } : {}) }),
      })
    },
    onSuccess: () => {
      push('success', editing ? t('Reseller updated') : t('Reseller {name} created', { name: username }))
      onSaved()
    },
    onError: (err) => setError(apiErrorMessage(err, t)),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    saveMutation.mutate()
  }

  function toggleGroup(group: string) {
    setGroups((prev) => (prev.includes(group) ? prev.filter((g) => g !== group) : [...prev, group]))
  }

  const title = editing ? t('Edit reseller') : child ? t('New sub-reseller') : t('New reseller')
  // With a limited parent a child needs finite limits; say how much there is to give.
  const roomHint =
    child && room
      ? t('You can give at most {accounts} accounts and {traffic} of traffic credit to a new sub-reseller.', {
          accounts: room.accounts === null ? t('Unlimited') : String(room.accounts),
          traffic: room.bytes === null ? t('Unlimited') : ltr(formatGB(room.bytes)),
        })
      : null

  return (
    <Dialog open={open} onClose={onClose} title={title} maxWidthClassName="max-w-lg">
      <form onSubmit={handleSubmit} className="space-y-4">
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label={t('Username')}>
            <Input value={username} onChange={(e) => setUsername(e.target.value)} required disabled={editing} autoComplete="off" />
          </Field>
          {(!editing || !child) && (
            <Field label={t('Password')} labelSuffix={editing ? t('(leave blank to keep)') : undefined}>
              <Input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required={!editing}
                minLength={8}
                autoComplete="new-password"
              />
            </Field>
          )}
        </div>

        {!child && !editing && parents.length > 0 && (
          <Field label={t('Parent reseller')} hint={t('Leave empty for a top-level reseller. Its limits are taken from the parent’s plan.')}>
            <Select value={parentId} onChange={(e) => setParentId(e.target.value)}>
              <option value="">{t('None (top level)')}</option>
              {parents
                .filter((p) => p.depth < MAX_RESELLER_DEPTH)
                .map((p) => (
                  <option key={p.id} value={p.id}>
                    {'— '.repeat(p.depth - 1)}
                    {p.username}
                  </option>
                ))}
            </Select>
          </Field>
        )}

        <div className="rounded-card border border-edge bg-inset/50 p-4">
          <p className="text-sm font-medium text-fg">{t('Limits')}</p>
          <p className="mt-0.5 text-xs text-muted">{child ? roomHint : t('Leave a field empty for no limit.')}</p>
          <div className="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-3">
            <Field label={t('Max accounts')}>
              <Input type="number" min="0" value={maxAccounts} onChange={(e) => setMaxAccounts(e.target.value)} placeholder={t('Unlimited')} />
            </Field>
            <Field label={t('Traffic credit (GB)')}>
              <Input type="number" min="0" step="any" value={dataLimitGB} onChange={(e) => setDataLimitGB(e.target.value)} placeholder={t('Unlimited')} />
            </Field>
            <Field label={t('Plan expires on')}>
              <Input
                type="date"
                value={expiresOn}
                min={new Date().toISOString().slice(0, 10)}
                max={child && room?.expiresAt ? room.expiresAt.slice(0, 10) : undefined}
                onChange={(e) => setExpiresOn(e.target.value)}
              />
            </Field>
          </div>
          <p className="mt-3 text-xs leading-relaxed text-muted">
            {t('Traffic credit is the total data quota the reseller can hand out across its accounts. With a credit set, it cannot create unlimited accounts.')}{' '}
            {t('After the plan expires the reseller and its API keys stop working; its accounts keep running, and none can be set to expire after the plan.')}
          </p>
          {(child || parentId) && (
            <p className="mt-2 text-xs leading-relaxed text-muted">
              {t('A sub-reseller’s accounts, traffic credit and plan end come out of its parent’s: they must fit in what the parent has left and cannot outlast it.')}
            </p>
          )}
        </div>

        <Field label={t('Node groups')} hint={t('None selected means all node groups.')}>
          {availableGroups.length === 0 ? (
            <p className="text-sm text-muted">{t('No node groups yet.')}</p>
          ) : (
            <div className="flex flex-wrap gap-2">
              {availableGroups.map((g) => {
                const on = groups.includes(g)
                return (
                  <button
                    key={g}
                    type="button"
                    aria-pressed={on}
                    onClick={() => toggleGroup(g)}
                    className={`cursor-pointer rounded-full border px-3 py-1 text-xs font-medium transition-colors duration-150 ${
                      on ? 'border-accent bg-accent/10 text-accent-fg' : 'border-edge text-muted hover:border-edge-strong hover:text-fg'
                    }`}
                  >
                    {g}
                  </button>
                )
              })}
            </div>
          )}
        </Field>

        <Field label={t('Notes')} labelSuffix={t('(optional)')}>
          <textarea
            value={notes}
            onChange={(e) => setNotes(e.target.value)}
            rows={2}
            className="w-full rounded-control border border-edge bg-surface px-3 py-2 text-sm text-fg transition-[border-color,box-shadow] duration-150 placeholder:text-faint hover:border-edge-strong focus:border-accent focus:ring-[3px] focus:ring-ring focus:outline-none"
          />
        </Field>

        {error && <p className="text-sm text-danger-fg">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('Cancel')}
          </Button>
          <Button type="submit" disabled={saveMutation.isPending}>
            {saveMutation.isPending ? t('Saving…') : editing ? t('Save changes') : t('Create reseller')}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
