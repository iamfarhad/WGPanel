import { useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, Pencil, Trash2, RefreshCw } from 'lucide-react'
import { apiFetch } from '../lib/api'
import { apiErrorMessage } from '../lib/apiErrors'
import { useAuth } from '../lib/auth'
import { useI18n } from '../lib/i18n'
import { useToast } from '../lib/toast'
import { Dialog } from './ui/Dialog'
import { Button } from './ui/Button'
import { Badge } from './ui/Badge'
import { Field } from './ui/Field'
import { Input } from './ui/Input'
import { Select } from './ui/Select'
import { ConfirmDialog } from './ui/ConfirmDialog'

export interface NodeEndpoint {
  id: string
  node_id: string
  label: string
  address: string
  kind: 'direct' | 'relay'
  priority: number
  enabled: boolean
  mtu: number | null
  health_check: 'none' | 'tcp'
  last_status: 'unknown' | 'up' | 'down'
  last_checked_at: string | null
  last_latency_ms: number | null
}

export function useNodeEndpoints(nodeId: string, enabled = true) {
  return useQuery({
    queryKey: ['node-endpoints', nodeId],
    enabled,
    queryFn: () => apiFetch<{ endpoints: NodeEndpoint[] | null }>(`/api/v1/nodes/${nodeId}/endpoints`),
  })
}

function HealthBadge({ ep }: { ep: NodeEndpoint }) {
  const { t } = useI18n()
  if (!ep.enabled) return <Badge tone="slate">{t('Disabled')}</Badge>
  if (ep.health_check === 'none') return <Badge tone="slate">{t('Not checked')}</Badge>
  if (ep.last_status === 'up') return <Badge dot tone="green">{t('Healthy')}</Badge>
  if (ep.last_status === 'down') return <Badge dot tone="red">{t('Unreachable')}</Badge>
  return <Badge dot tone="amber">{t('Waiting for first check')}</Badge>
}

interface FormState {
  label: string
  address: string
  kind: 'direct' | 'relay'
  priority: string
  mtu: string
  healthCheck: 'none' | 'tcp'
  enabled: boolean
}

const emptyForm: FormState = { label: '', address: '', kind: 'relay', priority: '100', mtu: '', healthCheck: 'tcp', enabled: true }

function EndpointForm({
  nodeId,
  editing,
  onDone,
}: {
  nodeId: string
  editing: NodeEndpoint | null
  onDone: () => void
}) {
  const { t } = useI18n()
  const { push } = useToast()
  const queryClient = useQueryClient()
  const [form, setForm] = useState<FormState>(
    editing
      ? {
          label: editing.label,
          address: editing.address,
          kind: editing.kind,
          priority: String(editing.priority),
          mtu: editing.mtu ? String(editing.mtu) : '',
          healthCheck: editing.health_check,
          enabled: editing.enabled,
        }
      : emptyForm,
  )
  const set = <K extends keyof FormState>(key: K, value: FormState[K]) => setForm((f) => ({ ...f, [key]: value }))

  const save = useMutation({
    mutationFn: () => {
      const body: Record<string, unknown> = {
        label: form.label.trim(),
        address: form.address.trim(),
        kind: form.kind,
        priority: Number(form.priority) || 0,
        health_check: form.healthCheck,
        enabled: form.enabled,
      }
      if (form.mtu) body.mtu = Number(form.mtu)
      else if (editing?.mtu) body.clear_mtu = true
      return apiFetch<NodeEndpoint>(
        editing ? `/api/v1/nodes/${nodeId}/endpoints/${editing.id}` : `/api/v1/nodes/${nodeId}/endpoints`,
        { method: editing ? 'PATCH' : 'POST', body: JSON.stringify(body) },
      )
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['node-endpoints', nodeId] })
      push('success', t('Endpoint saved'))
      onDone()
    },
    onError: (err) => push('error', apiErrorMessage(err, t, 'Failed to save endpoint')),
  })

  function submit(e: FormEvent) {
    e.preventDefault()
    save.mutate()
  }

  return (
    <form onSubmit={submit} className="mt-4 space-y-4 rounded-lg border border-edge bg-inset/40 p-4">
      <p className="text-sm font-medium text-fg">{editing ? t('Edit endpoint') : t('New endpoint')}</p>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Label">
          <Input value={form.label} onChange={(e) => set('label', e.target.value)} required maxLength={64} placeholder={t('e.g. Tehran relay')} />
        </Field>
        <Field label="Address (host:port)">
          <Input
            className="ltr"
            value={form.address}
            onChange={(e) => set('address', e.target.value)}
            required
            placeholder="relay.example.ir:51820"
          />
        </Field>
        <Field label="Kind">
          <Select value={form.kind} onChange={(e) => set('kind', e.target.value as FormState['kind'])}>
            <option value="relay">{t('Relay')}</option>
            <option value="direct">{t('Direct')}</option>
          </Select>
        </Field>
        <Field label="Priority" hint="Higher numbers are tried first.">
          <Input type="number" value={form.priority} onChange={(e) => set('priority', e.target.value)} required />
        </Field>
        <Field label="Health check" hint="TCP connect works when the relay also listens on TCP on the same port.">
          <Select value={form.healthCheck} onChange={(e) => set('healthCheck', e.target.value as FormState['healthCheck'])}>
            <option value="tcp">{t('TCP connect to host:port')}</option>
            <option value="none">{t('None (always assumed healthy)')}</option>
          </Select>
        </Field>
        <Field label="MTU override" labelSuffix="(optional)" hint="1200-1420. Empty uses the panel's client MTU setting.">
          <Input
            className="ltr"
            type="number"
            min="1200"
            max="1420"
            value={form.mtu}
            onChange={(e) => set('mtu', e.target.value)}
            placeholder="1280"
          />
        </Field>
      </div>
      <label className="flex cursor-pointer items-center gap-2 text-sm text-fg">
        <input type="checkbox" checked={form.enabled} onChange={(e) => set('enabled', e.target.checked)} className="h-4 w-4 accent-accent" />
        {t('Enabled')}
      </label>
      <div className="flex justify-end gap-2">
        <Button type="button" variant="secondary" onClick={onDone}>
          Cancel
        </Button>
        <Button type="submit" disabled={save.isPending}>
          {save.isPending ? 'Saving…' : 'Save'}
        </Button>
      </div>
    </form>
  )
}

/** Manages a node's extra entry endpoints (relays). Staff only - the API refuses these
 * routes for resellers and API keys. */
export function NodeEndpointsDialog({ node, onClose }: { node: { id: string; name: string; public_endpoint: string }; onClose: () => void }) {
  const { t, formatDate } = useI18n()
  const { push } = useToast()
  const { user } = useAuth()
  const queryClient = useQueryClient()
  const canWrite = user?.role === 'super_admin' || user?.role === 'operator'
  const query = useNodeEndpoints(node.id)
  const endpoints = query.data?.endpoints ?? []
  const [formFor, setFormFor] = useState<NodeEndpoint | 'new' | null>(null)
  const [toDelete, setToDelete] = useState<NodeEndpoint | null>(null)

  const refresh = () => queryClient.invalidateQueries({ queryKey: ['node-endpoints', node.id] })

  const check = useMutation({
    mutationFn: (ep: NodeEndpoint) => apiFetch<NodeEndpoint>(`/api/v1/nodes/${node.id}/endpoints/${ep.id}/check`, { method: 'POST' }),
    onSuccess: refresh,
    onError: (err) => push('error', apiErrorMessage(err, t, 'Health check failed to run')),
  })
  const toggle = useMutation({
    mutationFn: (ep: NodeEndpoint) =>
      apiFetch<NodeEndpoint>(`/api/v1/nodes/${node.id}/endpoints/${ep.id}`, { method: 'PATCH', body: JSON.stringify({ enabled: !ep.enabled }) }),
    onSuccess: refresh,
    onError: (err) => push('error', apiErrorMessage(err, t, 'Failed to save endpoint')),
  })
  const remove = useMutation({
    mutationFn: (ep: NodeEndpoint) => apiFetch<void>(`/api/v1/nodes/${node.id}/endpoints/${ep.id}`, { method: 'DELETE' }),
    onSuccess: () => {
      refresh()
      setToDelete(null)
    },
    onError: (err) => push('error', apiErrorMessage(err, t, 'Failed to delete endpoint')),
  })

  return (
    <Dialog open onClose={onClose} title={t('{name} — entry endpoints', { name: node.name })} maxWidthClassName="max-w-2xl">
      <p className="text-sm leading-relaxed text-muted">
        {t(
          'Clients connect to the highest-priority healthy endpoint below, for example a relay inside a country where this node is blocked. With none healthy they use the node public endpoint.',
        )}
      </p>
      <div className="mt-3 flex items-center justify-between gap-3 rounded-lg border border-edge px-3 py-2.5 text-sm">
        <span className="text-muted">{t('Public endpoint (fallback)')}</span>
        <span className="ltr font-mono text-xs text-fg">{node.public_endpoint}</span>
      </div>

      <div className="mt-4 space-y-2">
        {query.isLoading && <p className="text-sm text-muted">{t('Loading…')}</p>}
        {query.isError && <p className="text-sm text-danger-fg">{t('Could not load endpoints.')}</p>}
        {!query.isLoading && !query.isError && endpoints.length === 0 && (
          <p className="text-sm text-muted">{t('No extra endpoints yet. Clients use the node public endpoint.')}</p>
        )}
        {endpoints.map((ep) => (
          <div key={ep.id} className="rounded-lg border border-edge px-3 py-3">
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <span className="font-medium text-fg">{ep.label}</span>
              <Badge tone={ep.kind === 'relay' ? 'blue' : 'slate'}>{ep.kind === 'relay' ? 'Relay' : 'Direct'}</Badge>
              <HealthBadge ep={ep} />
            </div>
            <p className="ltr mt-1 font-mono text-xs text-muted">{ep.address}</p>
            <p className="mt-1 text-xs text-muted">
              {t('Priority {n}', { n: ep.priority })}
              {ep.mtu ? ` · MTU ${ep.mtu}` : ''}
              {ep.last_latency_ms != null && ep.last_status === 'up' ? ` · ${ep.last_latency_ms} ms` : ''}
              {ep.last_checked_at ? ` · ${t('checked {when}', { when: formatDate(ep.last_checked_at, { hour: '2-digit', minute: '2-digit', second: '2-digit' }) })}` : ''}
            </p>
            {canWrite && (
              <div className="mt-2 flex flex-wrap gap-2">
                {ep.health_check === 'tcp' && (
                  <Button size="sm" variant="secondary" onClick={() => check.mutate(ep)} disabled={check.isPending}>
                    <RefreshCw className="h-3.5 w-3.5" />
                    Check now
                  </Button>
                )}
                <Button size="sm" variant="secondary" onClick={() => toggle.mutate(ep)} disabled={toggle.isPending}>
                  {ep.enabled ? 'Disable' : 'Enable'}
                </Button>
                <Button size="sm" variant="secondary" onClick={() => setFormFor(ep)}>
                  <Pencil className="h-3.5 w-3.5" />
                  Edit
                </Button>
                <Button size="sm" variant="ghost" onClick={() => setToDelete(ep)}>
                  <Trash2 className="h-3.5 w-3.5" />
                  Delete
                </Button>
              </div>
            )}
          </div>
        ))}
      </div>

      {canWrite && formFor === null && (
        <div className="mt-4">
          <Button variant="secondary" onClick={() => setFormFor('new')}>
            <Plus className="h-4 w-4" />
            Add endpoint
          </Button>
        </div>
      )}
      {formFor !== null && (
        <EndpointForm key={formFor === 'new' ? 'new' : formFor.id} nodeId={node.id} editing={formFor === 'new' ? null : formFor} onDone={() => setFormFor(null)} />
      )}

      <ConfirmDialog
        open={toDelete !== null}
        onClose={() => setToDelete(null)}
        onConfirm={() => toDelete && remove.mutate(toDelete)}
        title="Delete endpoint?"
        description="Clients using this endpoint switch to the next healthy one the next time they download their config."
        confirmLabel="Delete"
        danger
        submitting={remove.isPending}
      />
    </Dialog>
  )
}

/** Staff-only entry-endpoint picker for downloading an account config: Automatic (the
 * best healthy endpoint), the node's direct address, or one specific endpoint. Renders
 * nothing for resellers or for nodes without extra endpoints. */
export function PeerEndpointSelect({ nodeId, value, onChange }: { nodeId: string; value: string; onChange: (v: string) => void }) {
  const { t } = useI18n()
  const { user } = useAuth()
  const query = useNodeEndpoints(nodeId, user?.role !== undefined && user.role !== 'reseller')
  const endpoints = query.data?.endpoints ?? []
  if (endpoints.length === 0) return null
  return (
    <Select
      className="w-44"
      aria-label={t('Entry endpoint')}
      value={value}
      onChange={(e) => onChange(e.target.value)}
    >
      <option value="">{t('Endpoint: automatic')}</option>
      <option value="direct">{t('Node public endpoint')}</option>
      {endpoints.map((ep) => (
        <option key={ep.id} value={ep.id}>
          {ep.label}
        </option>
      ))}
    </Select>
  )
}
