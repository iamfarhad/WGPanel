import { useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, Copy, History, KeyRound, Pencil, Plus, RotateCw, Send, Trash2, X } from 'lucide-react'
import { apiFetch } from '../lib/api'
import { apiErrorMessage } from '../lib/apiErrors'
import { useToast } from '../lib/toast'
import { useI18n } from '../lib/i18n'
import { useAuth } from '../lib/auth'
import { PageHeader } from '../components/ui/PageHeader'
import { Card } from '../components/ui/Card'
import { Button } from '../components/ui/Button'
import { Input } from '../components/ui/Input'
import { Field } from '../components/ui/Field'
import { Dialog } from '../components/ui/Dialog'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { Badge } from '../components/ui/Badge'
import { EmptyState } from '../components/ui/EmptyState'
import { ErrorState } from '../components/ui/ErrorState'
import { TableSkeleton } from '../components/ui/Skeleton'
import { Table, THead, Th, Tr, Td } from '../components/ui/Table'
import { RowActions } from '../components/ui/RowActions'
import { EventPicker } from '../components/EventPicker'

interface Webhook {
  id: string
  namespace: string | null
  url: string
  description: string
  events: string[]
  enabled: boolean
  created_at: string
  secret?: string
}

interface Delivery {
  id: string
  event: string
  event_id: string
  status: 'pending' | 'retrying' | 'success' | 'failed'
  attempts: number
  next_attempt_at: string | null
  response_status: number | null
  latency_ms: number | null
  response_body: string
  error: string
  payload: string
  created_at: string
}

// payment.awaiting_review is staff-only (the server never sends it to a namespace).
const RESELLER_EVENT_PREFIXES = ['account.', 'reseller.', 'order.', 'payment.approved', 'payment.rejected', 'wallet.']

function deliveryTone(status: Delivery['status']) {
  return status === 'success' ? 'green' : status === 'failed' ? 'red' : status === 'retrying' ? 'amber' : 'slate'
}

export function WebhooksPage() {
  const { t } = useI18n()
  const { user } = useAuth()
  const isReseller = user?.role === 'reseller'
  const isSuper = user?.role === 'super_admin'
  const canWrite = user?.role !== 'support'
  const queryClient = useQueryClient()
  const { push } = useToast()

  const [editing, setEditing] = useState<Webhook | 'new' | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<Webhook | null>(null)
  const [rotateTarget, setRotateTarget] = useState<Webhook | null>(null)
  const [revealed, setRevealed] = useState<Webhook | null>(null)
  const [logFor, setLogFor] = useState<Webhook | null>(null)

  const query = useQuery({ queryKey: ['webhooks'], queryFn: () => apiFetch<{ items: Webhook[] }>('/api/v1/webhooks') })
  const hooks = query.data?.items ?? []
  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['webhooks'] })
  const onError = (err: unknown) => push('error', apiErrorMessage(err, t))

  const deleteMutation = useMutation({
    mutationFn: (id: string) => apiFetch(`/api/v1/webhooks/${id}`, { method: 'DELETE' }),
    onSuccess: () => {
      push('success', 'Webhook deleted')
      setDeleteTarget(null)
      invalidate()
    },
    onError,
  })
  const rotateMutation = useMutation({
    mutationFn: (id: string) => apiFetch<Webhook>(`/api/v1/webhooks/${id}/rotate-secret`, { method: 'POST' }),
    onSuccess: (hook) => {
      setRotateTarget(null)
      setRevealed(hook)
      invalidate()
    },
    onError,
  })
  const testMutation = useMutation({
    mutationFn: (id: string) => apiFetch<Delivery>(`/api/v1/webhooks/${id}/test`, { method: 'POST' }),
    onSuccess: (d) => {
      if (d.status === 'success') push('success', t('Test ping delivered (HTTP {code}, {ms} ms)', { code: d.response_status ?? 0, ms: d.latency_ms ?? 0 }))
      else push('error', t('Test ping failed: {reason}', { reason: d.error || (d.response_status ? `HTTP ${d.response_status}` : '') }))
    },
    onError,
  })

  const addButton = canWrite && (
    <Button onClick={() => setEditing('new')}>
      <Plus className="h-4 w-4" />
      New webhook
    </Button>
  )

  return (
    <div>
      <PageHeader
        title="Webhooks"
        description={
          isReseller
            ? 'Get a signed HTTP request at your own server when something happens to your accounts.'
            : 'Send signed HTTP requests to other systems when events happen. Failed deliveries are retried automatically.'
        }
        action={addButton}
      />

      <Card className="overflow-hidden">
        {query.isLoading && <TableSkeleton cols={5} />}
        {query.isError && <ErrorState title="Could not load webhooks." onRetry={() => query.refetch()} />}
        {!query.isLoading && !query.isError && hooks.length === 0 && (
          <EmptyState
            illustration="setup"
            title="No webhooks yet"
            description="Add an endpoint to be notified about accounts, nodes and backups as they happen."
            action={addButton || undefined}
          />
        )}
        {!query.isLoading && !query.isError && hooks.length > 0 && (
          <Table>
            <THead>
              <tr>
                <Th>Endpoint</Th>
                <Th>Events</Th>
                {!isReseller && <Th>Scope</Th>}
                <Th>Status</Th>
                <Th className="text-end">Actions</Th>
              </tr>
            </THead>
            <tbody>
              {hooks.map((h) => (
                <Tr key={h.id}>
                  <Td>
                    <p className="ltr max-w-xs truncate font-mono text-xs text-fg" title={h.url}>
                      {h.url}
                    </p>
                    {h.description && <p className="mt-0.5 text-xs text-muted">{h.description}</p>}
                  </Td>
                  <Td className="text-xs text-muted">
                    {h.events.length === 0 ? t('All events') : t('{count} events', { count: h.events.length })}
                  </Td>
                  {!isReseller && <Td>{h.namespace ? <Badge tone="blue">Reseller</Badge> : <Badge>Global</Badge>}</Td>}
                  <Td>{h.enabled ? <Badge tone="green" dot>Enabled</Badge> : <Badge dot>Disabled</Badge>}</Td>
                  <Td className="text-end">
                    <RowActions
                      actions={[
                        { label: 'Delivery log', icon: History, onSelect: () => setLogFor(h) },
                        ...(canWrite
                          ? [
                              { label: 'Send test ping', icon: Send, onSelect: () => testMutation.mutate(h.id) },
                              { label: 'Edit', icon: Pencil, onSelect: () => setEditing(h) },
                              { label: 'Rotate secret', icon: KeyRound, onSelect: () => setRotateTarget(h) },
                              { label: 'Delete', icon: Trash2, danger: true, onSelect: () => setDeleteTarget(h) },
                            ]
                          : []),
                      ]}
                    />
                  </Td>
                </Tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>

      {isSuper && <PrivateTargetsCard />}

      {editing && (
        <WebhookDialog
          hook={editing === 'new' ? null : editing}
          reseller={isReseller}
          onClose={() => setEditing(null)}
          onSaved={(hook, created) => {
            setEditing(null)
            invalidate()
            if (created) setRevealed(hook)
            else push('success', 'Webhook saved')
          }}
        />
      )}
      {deleteTarget && (
        <ConfirmDialog
          open
          onClose={() => setDeleteTarget(null)}
          onConfirm={() => deleteMutation.mutate(deleteTarget.id)}
          title="Delete webhook"
          description="This endpoint stops receiving events and its delivery log is deleted."
          confirmLabel="Delete"
          danger
          submitting={deleteMutation.isPending}
        />
      )}
      {rotateTarget && (
        <ConfirmDialog
          open
          onClose={() => setRotateTarget(null)}
          onConfirm={() => rotateMutation.mutate(rotateTarget.id)}
          title="Rotate signing secret"
          description="The current secret stops working immediately. Update your receiver with the new one right after."
          confirmLabel="Rotate"
          submitting={rotateMutation.isPending}
        />
      )}
      {revealed?.secret && <SecretDialog hook={revealed} onClose={() => setRevealed(null)} />}
      {logFor && <DeliveryDrawer hook={logFor} canWrite={canWrite} onClose={() => setLogFor(null)} />}
    </div>
  )
}

function SecretDialog({ hook, onClose }: { hook: Webhook; onClose: () => void }) {
  const { t } = useI18n()
  const { push } = useToast()
  return (
    <Dialog open onClose={onClose} title="Signing secret" maxWidthClassName="max-w-lg">
      <div className="mb-4 flex items-start gap-2.5 rounded-control border border-warning/25 bg-warning-soft px-3 py-2.5 text-sm text-warning-fg">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
        <span>{t('This secret is shown once. Store it securely — it cannot be retrieved again.')}</span>
      </div>
      <p className="ltr break-all rounded-lg border border-edge bg-inset p-2.5 font-mono text-xs leading-relaxed text-fg">
        {hook.secret}
      </p>
      <p className="mt-3 text-xs leading-relaxed text-muted">
        {t('Each request carries an X-Jirjirak-Signature header: t=<unix time>,v1=<HMAC-SHA256 of "<t>.<body>"> using this secret.')}
      </p>
      <div className="mt-4 flex justify-end">
        <Button
          variant="secondary"
          onClick={() => {
            navigator.clipboard.writeText(hook.secret ?? '')
            push('success', 'Secret copied to clipboard')
          }}
        >
          <Copy className="h-4 w-4" />
          Copy secret
        </Button>
      </div>
    </Dialog>
  )
}

function WebhookDialog({
  hook,
  reseller,
  onClose,
  onSaved,
}: {
  hook: Webhook | null
  reseller: boolean
  onClose: () => void
  onSaved: (hook: Webhook, created: boolean) => void
}) {
  const { t } = useI18n()
  const { push } = useToast()
  const [url, setUrl] = useState(hook?.url ?? '')
  const [description, setDescription] = useState(hook?.description ?? '')
  const [events, setEvents] = useState<string[]>(hook?.events ?? [])
  const [enabled, setEnabled] = useState(hook?.enabled ?? true)
  const only = reseller || hook?.namespace ? RESELLER_EVENT_PREFIXES : undefined

  const save = useMutation({
    mutationFn: () =>
      apiFetch<Webhook>(hook ? `/api/v1/webhooks/${hook.id}` : '/api/v1/webhooks', {
        method: hook ? 'PATCH' : 'POST',
        body: JSON.stringify({ url: url.trim(), description, events, enabled }),
      }),
    onSuccess: (saved) => onSaved(saved, !hook),
    onError: (err) => push('error', apiErrorMessage(err, t)),
  })

  function submit(e: FormEvent) {
    e.preventDefault()
    save.mutate()
  }

  return (
    <Dialog open onClose={onClose} title={hook ? 'Edit webhook' : 'New webhook'} maxWidthClassName="max-w-xl">
      <form onSubmit={submit} className="space-y-4">
        <Field label="Endpoint URL" hint="Must be a public http(s) address. Private network addresses are refused.">
          <Input
            className="ltr"
            type="url"
            required
            placeholder="https://example.com/hooks/jirjirak"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
          />
        </Field>
        <Field label="Description" labelSuffix="(optional)">
          <Input value={description} onChange={(e) => setDescription(e.target.value)} maxLength={120} />
        </Field>
        <Field label="Events" hint="Leave all unchecked to receive every event.">
          <EventPicker value={events} onChange={setEvents} only={only} />
        </Field>
        <label className="flex cursor-pointer items-center gap-2 text-sm text-fg">
          <input type="checkbox" className="h-4 w-4 accent-accent" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          {t('Enabled')}
        </label>
        <div className="flex justify-end gap-2 border-t border-edge pt-4">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={save.isPending}>
            {save.isPending ? 'Saving…' : hook ? 'Save' : 'Create webhook'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

/** Right-hand drawer with the delivery log of one webhook. */
function DeliveryDrawer({ hook, canWrite, onClose }: { hook: Webhook; canWrite: boolean; onClose: () => void }) {
  const { t, formatDate } = useI18n()
  const { push } = useToast()
  const queryClient = useQueryClient()
  const [open, setOpen] = useState<string | null>(null)
  const key = ['webhook-deliveries', hook.id]
  const query = useQuery({
    queryKey: key,
    queryFn: () => apiFetch<{ items: Delivery[] }>(`/api/v1/webhooks/${hook.id}/deliveries?limit=100`),
    refetchInterval: 5000,
  })
  const redeliver = useMutation({
    mutationFn: (id: string) => apiFetch(`/api/v1/webhooks/${hook.id}/deliveries/${id}/redeliver`, { method: 'POST' }),
    onSuccess: () => {
      push('success', 'Delivery queued')
      queryClient.invalidateQueries({ queryKey: key })
    },
    onError: (err) => push('error', apiErrorMessage(err, t)),
  })
  const items = query.data?.items ?? []

  return (
    <div className="fixed inset-0 z-40 flex justify-end">
      <div className="animate-backdrop-in absolute inset-0 bg-overlay" onClick={onClose} />
      <aside
        role="dialog"
        aria-modal="true"
        aria-label={t('Delivery log')}
        className="animate-dialog-in relative flex h-full w-full max-w-lg flex-col border-s border-edge bg-surface shadow-pop"
      >
        <div className="flex items-start justify-between gap-3 border-b border-edge px-5 py-4">
          <div className="min-w-0">
            <h2 className="text-base font-semibold text-fg">{t('Delivery log')}</h2>
            <p className="ltr mt-0.5 truncate font-mono text-xs text-muted" title={hook.url}>
              {hook.url}
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label={t('Close')}
            className="cursor-pointer rounded-lg p-1.5 text-faint hover:bg-inset hover:text-fg"
          >
            <X className="h-5 w-5" />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">
          {query.isLoading && <TableSkeleton cols={2} />}
          {query.isError && <ErrorState title="Could not load the delivery log." onRetry={() => query.refetch()} />}
          {!query.isLoading && !query.isError && items.length === 0 && (
            <EmptyState illustration="search" title="No deliveries yet" description="Events sent to this endpoint will be listed here." />
          )}
          {items.map((d) => (
            <div key={d.id} className="border-b border-edge/70 px-5 py-3 last:border-0">
              <button
                type="button"
                onClick={() => setOpen(open === d.id ? null : d.id)}
                className="flex w-full cursor-pointer items-center gap-3 text-start"
              >
                <Badge tone={deliveryTone(d.status)}>
                  {d.status === 'success' ? 'Delivered' : d.status === 'failed' ? 'Failed' : d.status === 'retrying' ? 'Retrying' : 'Pending'}
                </Badge>
                <span className="ltr min-w-0 flex-1 truncate font-mono text-xs text-fg">{d.event}</span>
                <span className="ltr text-xs text-muted tabular-nums">
                  {d.response_status ? `HTTP ${d.response_status}` : '—'}
                  {d.latency_ms != null ? ` · ${d.latency_ms} ms` : ''}
                </span>
              </button>
              <p className="mt-1 text-xs text-faint">
                {formatDate(d.created_at)} · {t('{count} attempts', { count: d.attempts })}
                {d.status === 'retrying' && d.next_attempt_at ? ` · ${t('next try {when}', { when: formatDate(d.next_attempt_at) })}` : ''}
              </p>
              {open === d.id && (
                <div className="mt-3 space-y-2">
                  {d.error && <p className="text-xs text-danger-fg">{d.error}</p>}
                  {d.response_body && (
                    <div>
                      <p className="mb-1 text-[11px] font-medium text-muted">{t('Response')}</p>
                      <pre className="ltr max-h-32 overflow-auto rounded-lg border border-edge bg-inset p-2 font-mono text-[11px] break-all whitespace-pre-wrap text-fg">
                        {d.response_body}
                      </pre>
                    </div>
                  )}
                  <div>
                    <p className="mb-1 text-[11px] font-medium text-muted">{t('Payload')}</p>
                    <pre className="ltr max-h-40 overflow-auto rounded-lg border border-edge bg-inset p-2 font-mono text-[11px] break-all whitespace-pre-wrap text-fg">
                      {d.payload}
                    </pre>
                  </div>
                  {canWrite && (
                    <Button size="sm" variant="secondary" disabled={redeliver.isPending} onClick={() => redeliver.mutate(d.id)}>
                      <RotateCw className="h-3.5 w-3.5" />
                      Redeliver
                    </Button>
                  )}
                </div>
              )}
            </div>
          ))}
        </div>
      </aside>
    </div>
  )
}

/** Staff-only override of the SSRF guard (super_admin can change it). */
function PrivateTargetsCard() {
  const { t } = useI18n()
  const { push } = useToast()
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: ['webhook-settings'],
    queryFn: () => apiFetch<{ allow_private_targets: boolean }>('/api/v1/webhook-settings'),
  })
  const save = useMutation({
    mutationFn: (v: boolean) => apiFetch('/api/v1/webhook-settings', { method: 'PATCH', body: JSON.stringify({ allow_private_targets: v }) }),
    onSuccess: () => {
      push('success', 'Setting saved')
      queryClient.invalidateQueries({ queryKey: ['webhook-settings'] })
    },
    onError: (err) => push('error', apiErrorMessage(err, t)),
  })
  if (!query.data) return null
  return (
    <Card className="mt-6 px-5 py-4">
      <label className="flex cursor-pointer items-start gap-3">
        <input
          type="checkbox"
          className="mt-0.5 h-4 w-4 accent-accent"
          checked={query.data.allow_private_targets}
          disabled={save.isPending}
          onChange={(e) => save.mutate(e.target.checked)}
        />
        <span>
          <span className="block text-sm font-medium text-fg">{t('Allow webhooks to reach private networks')}</span>
          <span className="mt-0.5 block text-xs leading-relaxed text-muted">
            {t('Off by default: addresses on private, loopback and link-local ranges (including cloud metadata) are refused. Turn this on only to reach an internal service. It never applies to reseller webhooks.')}
          </span>
        </span>
      </label>
    </Card>
  )
}
