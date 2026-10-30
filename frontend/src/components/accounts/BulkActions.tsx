import { useEffect, useState, type FormEvent } from 'react'
import { useMutation } from '@tanstack/react-query'
import { Ban, CheckCircle2, FileDown, RotateCcw, RefreshCw, Tag, Trash2, X } from 'lucide-react'
import { apiFetch } from '../../lib/api'
import { apiCodeMessage, apiErrorMessage } from '../../lib/apiErrors'
import { useI18n } from '../../lib/i18n'
import { useToast } from '../../lib/toast'
import {
  MAX_BULK,
  accountsCsv,
  downloadTextFile,
  useTemplates,
  type BulkAction,
  type BulkResponse,
  type CsvAccount,
} from '../../lib/lifecycle'
import { Button } from '../ui/Button'
import { Dialog } from '../ui/Dialog'
import { Field } from '../ui/Field'
import { Input } from '../ui/Input'
import { Select } from '../ui/Select'
import { Badge } from '../ui/Badge'
import { LifecycleFields } from './LifecycleFields'
import { RoutingFields } from './RoutingFields'
import { emptyRouting, routingBody, routingFrom, type RoutingState } from '../../lib/routing'
import { emptyLifecycle, lifecycleBody, lifecycleFromTemplate, type LifecycleState } from '../../lib/lifecycleForm'

export type BulkAccount = CsvAccount & { id: string }

/** Sticky bar shown while one or more table rows are selected. */
export function BulkActionBar({
  count,
  busy,
  onClear,
  onAction,
  onTags,
}: {
  count: number
  busy: boolean
  onClear: () => void
  onAction: (action: Exclude<BulkAction, 'create' | 'add_tags' | 'remove_tags'>) => void
  /** Opens the add/remove tags dialog (see TagChips.tsx). */
  onTags?: () => void
}) {
  const { t } = useI18n()
  if (count === 0) return null
  return (
    <div
      role="toolbar"
      aria-label={t('Bulk actions')}
      className="sticky bottom-4 z-20 mx-auto mt-4 flex w-fit max-w-full flex-wrap items-center gap-2 rounded-modal border border-edge bg-surface px-3 py-2 shadow-pop"
    >
      <span className="px-1 text-sm font-medium text-fg">{t('{n} selected', { n: count })}</span>
      <Button size="sm" variant="secondary" disabled={busy} onClick={() => onAction('renew')}>
        <RotateCcw className="h-3.5 w-3.5" />
        {t('Renew')}
      </Button>
      <Button size="sm" variant="secondary" disabled={busy} onClick={() => onAction('enable')}>
        <CheckCircle2 className="h-3.5 w-3.5" />
        {t('Enable')}
      </Button>
      <Button size="sm" variant="secondary" disabled={busy} onClick={() => onAction('suspend')}>
        <Ban className="h-3.5 w-3.5" />
        {t('Suspend')}
      </Button>
      <Button size="sm" variant="secondary" disabled={busy} onClick={() => onAction('reset_usage')}>
        <RefreshCw className="h-3.5 w-3.5" />
        {t('Reset usage')}
      </Button>
      {onTags && (
        <Button size="sm" variant="secondary" disabled={busy} onClick={onTags}>
          <Tag className="h-3.5 w-3.5" />
          {t('Tags')}
        </Button>
      )}
      <Button size="sm" variant="danger" disabled={busy} onClick={() => onAction('delete')}>
        <Trash2 className="h-3.5 w-3.5" />
        {t('Delete')}
      </Button>
      <Button size="icon" variant="ghost" onClick={onClear} title={t('Clear selection')} aria-label={t('Clear selection')}>
        <X className="h-4 w-4" />
      </Button>
    </div>
  )
}

/** Renew several accounts: extra days and/or extra data. */
export function BulkRenewDialog({
  open,
  count,
  submitting,
  onClose,
  onSubmit,
}: {
  open: boolean
  count: number
  submitting: boolean
  onClose: () => void
  onSubmit: (v: { extendDays?: number; addQuotaGb?: number }) => void
}) {
  const { t } = useI18n()
  const [days, setDays] = useState('30')
  const [gb, setGb] = useState('')
  useEffect(() => {
    if (open) {
      setDays('30')
      setGb('')
    }
  }, [open])

  function submit(e: FormEvent) {
    e.preventDefault()
    onSubmit({ extendDays: days ? Number(days) : undefined, addQuotaGb: gb ? Number(gb) : undefined })
  }

  return (
    <Dialog open={open} onClose={onClose} title="Renew accounts">
      <form onSubmit={submit} className="space-y-4">
        <p className="text-sm text-muted">{t('Renew {n} accounts. Leave a field empty to skip it.', { n: count })}</p>
        <div className="grid grid-cols-2 gap-4">
          <Field label="Add days" hint="Accounts still on hold get a longer validity once they start.">
            <Input type="number" min="1" max="3650" value={days} onChange={(e) => setDays(e.target.value)} />
          </Field>
          <Field label="Add data (GB)">
            <Input type="number" min="0" step="0.1" value={gb} onChange={(e) => setGb(e.target.value)} />
          </Field>
        </div>
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose} disabled={submitting}>
            Cancel
          </Button>
          <Button type="submit" disabled={submitting || (!days && !gb)}>
            {submitting ? t('Working…') : t('Renew')}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

const actionTitle: Record<BulkAction, string> = {
  create: 'Accounts created',
  renew: 'Accounts renewed',
  suspend: 'Accounts suspended',
  enable: 'Accounts enabled',
  delete: 'Accounts deleted',
  reset_usage: 'Usage reset',
  add_tags: 'Tags added',
  remove_tags: 'Tags removed',
}

/** Per-item outcome of a bulk call. Failures are listed with the reason; for a bulk
 * create the successful accounts can be exported as CSV. */
export function BulkResultDialog({
  result,
  accountsForCsv,
  onClose,
}: {
  result: BulkResponse<BulkAccount> | null
  accountsForCsv?: BulkAccount[]
  onClose: () => void
}) {
  const { t } = useI18n()
  if (!result) return null
  const failures = result.results.filter((r) => !r.ok)
  return (
    <Dialog open onClose={onClose} title={actionTitle[result.action as BulkAction] ?? 'Bulk action'} maxWidthClassName="max-w-lg">
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone="green" dot>
          {t('{n} succeeded', { n: result.succeeded })}
        </Badge>
        {result.failed > 0 && (
          <Badge tone="red" dot>
            {t('{n} failed', { n: result.failed })}
          </Badge>
        )}
      </div>
      {accountsForCsv && accountsForCsv.length > 0 && (
        <div className="mt-4 rounded-lg border border-edge bg-inset/50 p-4">
          <p className="text-sm text-muted">{t('Download the new accounts with their subscription links and expiry.')}</p>
          <Button
            className="mt-3"
            onClick={() =>
              downloadTextFile(`accounts-${new Date().toISOString().slice(0, 10)}.csv`, accountsCsv(accountsForCsv, window.location.origin))
            }
          >
            <FileDown className="h-4 w-4" />
            {t('Download CSV')}
          </Button>
        </div>
      )}
      {failures.length > 0 && (
        <div className="mt-4">
          <p className="mb-2 text-xs font-semibold tracking-wider text-faint uppercase">{t('Failed items')}</p>
          <div className="max-h-56 space-y-1.5 overflow-y-auto">
            {failures.map((f, i) => (
              <div key={`${f.id ?? f.label}-${i}`} className="rounded-lg border border-edge px-3 py-2 text-xs">
                <span className="font-medium text-fg">{f.label || f.id}</span>
                <span className="ms-2 text-danger-fg">
                  {f.error ? apiCodeMessage(f.error.code, f.error.message, t) : ''}
                </span>
              </div>
            ))}
          </div>
        </div>
      )}
      <div className="mt-6 flex justify-end">
        <Button onClick={onClose}>{t('Done')}</Button>
      </div>
    </Dialog>
  )
}

interface NodeLite {
  id: string
  name: string
  node_group: string
}

/** Create up to MAX_BULK accounts from a template or from explicit fields; ends in the
 * result view with the CSV export. */
export function BulkCreateDialog({
  open,
  nodes,
  onClose,
  onCreated,
}: {
  open: boolean
  nodes: NodeLite[]
  onClose: () => void
  onCreated: () => void
}) {
  const { t } = useI18n()
  const { push } = useToast()
  const templatesQuery = useTemplates()
  const templates = templatesQuery.data?.templates ?? []

  const [prefix, setPrefix] = useState('')
  const [count, setCount] = useState('10')
  const [templateId, setTemplateId] = useState('')
  const [nodeId, setNodeId] = useState('')
  const [quotaGb, setQuotaGb] = useState('')
  const [deviceLimit, setDeviceLimit] = useState('')
  const [bandwidth, setBandwidth] = useState('')
  const [expiryDays, setExpiryDays] = useState('')
  const [life, setLife] = useState<LifecycleState>(emptyLifecycle)
  const [routing, setRouting] = useState<RoutingState>(emptyRouting)
  const [error, setError] = useState<string | null>(null)
  const [result, setResult] = useState<BulkResponse<BulkAccount> | null>(null)

  useEffect(() => {
    if (!open) return
    setPrefix('')
    setCount('10')
    setTemplateId('')
    setNodeId('')
    setQuotaGb('')
    setDeviceLimit('')
    setBandwidth('')
    setExpiryDays('')
    setLife(emptyLifecycle)
    setRouting(emptyRouting)
    setError(null)
    setResult(null)
  }, [open])

  function pickTemplate(id: string) {
    setTemplateId(id)
    const tmpl = templates.find((x) => x.id === id)
    setQuotaGb(tmpl?.data_quota_bytes != null ? String(tmpl.data_quota_bytes / 1e9) : '')
    setDeviceLimit(tmpl?.device_limit?.toString() ?? '')
    setBandwidth(tmpl?.bandwidth_limit_mbps?.toString() ?? '')
    setExpiryDays(tmpl && !tmpl.on_hold && tmpl.duration_days ? String(tmpl.duration_days) : '')
    setLife(lifecycleFromTemplate(tmpl))
    setRouting(routingFrom(tmpl))
  }

  const createMutation = useMutation({
    mutationFn: () =>
      apiFetch<BulkResponse<BulkAccount>>('/api/v1/accounts/bulk', {
        method: 'POST',
        body: JSON.stringify({
          action: 'create',
          count: Number(count),
          label_prefix: prefix.trim(),
          template_id: templateId || undefined,
          node_id: nodeId || undefined,
          data_quota_gb: quotaGb ? Number(quotaGb) : undefined,
          device_limit: deviceLimit ? Number(deviceLimit) : undefined,
          bandwidth_limit_mbps: bandwidth ? Number(bandwidth) : undefined,
          expiry_at:
            !life.onHold && expiryDays ? new Date(Date.now() + Number(expiryDays) * 24 * 60 * 60 * 1000).toISOString() : undefined,
          ...lifecycleBody(life),
          ...routingBody(routing),
        }),
      }),
    onSuccess: (res) => {
      setResult(res)
      onCreated()
      if (res.failed === 0) push('success', t('{n} accounts created', { n: res.succeeded }))
    },
    onError: (err) => setError(apiErrorMessage(err, t, 'Failed to create accounts')),
  })

  function submit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    createMutation.mutate()
  }

  if (open && result) {
    const created = result.results.filter((r) => r.ok && r.account).map((r) => r.account!)
    return <BulkResultDialog result={result} accountsForCsv={created} onClose={onClose} />
  }

  const n = Number(count)
  return (
    <Dialog open={open} onClose={onClose} title="Create accounts in bulk" maxWidthClassName="max-w-xl">
      <form onSubmit={submit} className="space-y-4">
        <div className="grid grid-cols-2 gap-4">
          <Field label="Label prefix" hint="Accounts are named prefix-01, prefix-02, and so on.">
            <Input value={prefix} onChange={(e) => setPrefix(e.target.value)} required maxLength={80} placeholder={t('e.g. promo')} />
          </Field>
          <Field label="How many" hint={t('Up to {max} at a time.', { max: MAX_BULK })}>
            <Input type="number" min="1" max={MAX_BULK} required value={count} onChange={(e) => setCount(e.target.value)} />
          </Field>
        </div>
        <Field label="Template" labelSuffix="(optional)">
          <Select value={templateId} onChange={(e) => pickTemplate(e.target.value)}>
            <option value="">{t('No template')}</option>
            {templates.map((x) => (
              <option key={x.id} value={x.id}>
                {x.name}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Nodes">
          <Select value={nodeId} onChange={(e) => setNodeId(e.target.value)}>
            <option value="">{t('All eligible nodes (recommended)')}</option>
            {nodes.map((x) => (
              <option key={x.id} value={x.id}>
                {t('Pin to just: {name}', { name: `${x.name} (${x.node_group})` })}
              </option>
            ))}
          </Select>
        </Field>
        <div className="grid grid-cols-2 gap-4">
          <Field label="Data quota (GB)">
            <Input type="number" min="0" step="0.1" value={quotaGb} onChange={(e) => setQuotaGb(e.target.value)} placeholder={t('Unlimited')} />
          </Field>
          <Field label="Device limit">
            <Input type="number" min="1" value={deviceLimit} onChange={(e) => setDeviceLimit(e.target.value)} placeholder={t('Unlimited')} />
          </Field>
          <Field label="Bandwidth (Mbps)">
            <Input type="number" min="1" value={bandwidth} onChange={(e) => setBandwidth(e.target.value)} placeholder={t('Unlimited')} />
          </Field>
          {!life.onHold && (
            <Field label="Expires in (days)">
              <Input type="number" min="1" max="3650" value={expiryDays} onChange={(e) => setExpiryDays(e.target.value)} placeholder={t('Never')} />
            </Field>
          )}
        </div>
        <LifecycleFields value={life} onChange={setLife} />
        <RoutingFields value={routing} onChange={setRouting} defaultLabel={templateId ? 'Same as template' : 'Default (full tunnel)'} />
        {error && <p className="text-sm text-danger-fg">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={createMutation.isPending || !prefix.trim() || !(n >= 1 && n <= MAX_BULK)}>
            {createMutation.isPending ? t('Creating…') : t('Create {n} accounts', { n: n >= 1 ? n : 0 })}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
