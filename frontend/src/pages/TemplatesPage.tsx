import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Pencil, Plus, Trash2 } from 'lucide-react'
import { apiFetch } from '../lib/api'
import { apiErrorMessage } from '../lib/apiErrors'
import { useAuth } from '../lib/auth'
import { useI18n } from '../lib/i18n'
import { useToast } from '../lib/toast'
import { resetStrategies, useTemplates, type PlanTemplate } from '../lib/lifecycle'
import { ltr } from '../lib/bidi'
import { PageHeader } from '../components/ui/PageHeader'
import { Card } from '../components/ui/Card'
import { Button } from '../components/ui/Button'
import { Input } from '../components/ui/Input'
import { Select } from '../components/ui/Select'
import { Field } from '../components/ui/Field'
import { Dialog } from '../components/ui/Dialog'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { Badge } from '../components/ui/Badge'
import { EmptyState } from '../components/ui/EmptyState'
import { ErrorState } from '../components/ui/ErrorState'
import { TableSkeleton } from '../components/ui/Skeleton'
import { Table, THead, Th, Tr, Td } from '../components/ui/Table'
import { RowActions } from '../components/ui/RowActions'
import { LifecycleFields } from '../components/accounts/LifecycleFields'
import { RoutingFields } from '../components/accounts/RoutingFields'
import { emptyRouting, routingBody, routingFrom, routingLabel, type RoutingState } from '../lib/routing'
import { TemplatePriceCell } from '../components/sales/TemplatePriceCell'
import { emptyLifecycle, lifecycleFromTemplate, type LifecycleState } from '../lib/lifecycleForm'

function gbText(bytes: number | null): string {
  if (bytes == null) return ''
  return String(Number((bytes / 1e9).toFixed(3))) // the API's GB is decimal (1e9)
}

export function TemplatesPage() {
  const { t } = useI18n()
  const { user } = useAuth()
  const { push } = useToast()
  const queryClient = useQueryClient()
  const readOnly = user?.role === 'support'
  const isStaff = user?.role !== 'reseller'
  const templatesQuery = useTemplates()
  const templates = templatesQuery.data?.templates ?? []

  const [editing, setEditing] = useState<PlanTemplate | 'new' | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<PlanTemplate | null>(null)

  const deleteMutation = useMutation({
    mutationFn: (id: string) => apiFetch<void>(`/api/v1/templates/${id}`, { method: 'DELETE' }),
    onSuccess: () => {
      push('success', t('Template deleted'))
      queryClient.invalidateQueries({ queryKey: ['templates'] })
      setDeleteTarget(null)
    },
    onError: (err) => push('error', apiErrorMessage(err, t, 'Failed to delete template')),
  })

  // A reseller can read the staff templates shared with it but only change its own.
  const canChange = (tm: PlanTemplate) => !readOnly && (isStaff || tm.owner != null)

  return (
    <div>
      <PageHeader
        title="Templates"
        description="Reusable plans - pick one when creating accounts to fill in the quota, duration, limits and reset schedule."
        action={
          !readOnly && (
            <Button onClick={() => setEditing('new')}>
              <Plus className="h-4 w-4" />
              New template
            </Button>
          )
        }
      />
      <Card className="overflow-hidden">
        {templatesQuery.isLoading && <TableSkeleton cols={5} />}
        {templatesQuery.isError && <ErrorState title="Could not load templates." onRetry={() => templatesQuery.refetch()} />}
        {!templatesQuery.isLoading && !templatesQuery.isError && templates.length === 0 && (
          <EmptyState
            illustration="wave"
            title="No templates yet"
            description="Create a template to reuse the same plan for many accounts."
            action={
              !readOnly && (
                <Button onClick={() => setEditing('new')}>
                  <Plus className="h-4 w-4" />
                  New template
                </Button>
              )
            }
          />
        )}
        {templates.length > 0 && (
          <Table>
            <THead>
              <tr>
                <Th>Name</Th>
                <Th>Data</Th>
                <Th>Duration</Th>
                <Th>Limits</Th>
                <Th>Reset</Th>
                <Th>Routing</Th>
                <Th>Availability</Th>
                <Th>Price</Th>
                <Th className="text-end">Actions</Th>
              </tr>
            </THead>
            <tbody>
              {templates.map((tm) => (
                <Tr key={tm.id}>
                  <Td className="font-medium text-fg">{tm.name}</Td>
                  <Td className="text-muted">
                    <span className="ltr tabular-nums">{tm.data_quota_bytes != null ? ltr(`${gbText(tm.data_quota_bytes)} GB`) : t('Unlimited')}</span>
                  </Td>
                  <Td className="whitespace-nowrap text-muted">
                    {tm.duration_days == null ? (
                      t('No expiry')
                    ) : tm.on_hold ? (
                      <span className="inline-flex flex-wrap items-center gap-1.5">
                        {t('{n} days', { n: tm.duration_days })}
                        <Badge tone="blue">{t('On hold')}</Badge>
                      </span>
                    ) : (
                      t('{n} days', { n: tm.duration_days })
                    )}
                  </Td>
                  <Td className="text-xs text-muted">
                    {tm.device_limit != null || tm.bandwidth_limit_mbps != null || tm.node_group ? (
                      <span className="inline-flex flex-wrap gap-x-3 gap-y-0.5">
                        {tm.device_limit != null && <span>{t('{n} devices', { n: tm.device_limit })}</span>}
                        {tm.bandwidth_limit_mbps != null && <span className="ltr">{tm.bandwidth_limit_mbps} Mbps</span>}
                        {tm.node_group && <span className="font-mono">{tm.node_group}</span>}
                      </span>
                    ) : (
                      '—'
                    )}
                  </Td>
                  <Td className="whitespace-nowrap text-muted">
                    {t(resetStrategies.find((r) => r.value === tm.reset_strategy)?.label ?? 'Never')}
                  </Td>
                  <Td className="whitespace-nowrap text-muted">{t(routingLabel(tm.routing_mode))}</Td>
                  <Td className="whitespace-nowrap">
                    {tm.owner != null ? (
                      <Badge>{t('Yours')}</Badge>
                    ) : tm.available_to_resellers ? (
                      <Badge tone="blue">{t('Shared with resellers')}</Badge>
                    ) : (
                      <span className="text-xs text-faint">{t('Staff only')}</span>
                    )}
                  </Td>
                  <Td>
                    <TemplatePriceCell template={tm} canChange={canChange(tm)} />
                  </Td>
                  <Td className="text-end">
                    {canChange(tm) && (
                      <RowActions
                        actions={[
                          { label: 'Edit', icon: Pencil, onSelect: () => setEditing(tm) },
                          { label: 'Delete', icon: Trash2, danger: true, onSelect: () => setDeleteTarget(tm) },
                        ]}
                      />
                    )}
                  </Td>
                </Tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>

      <TemplateDialog template={editing} isStaff={isStaff} onClose={() => setEditing(null)} />

      {deleteTarget && (
        <ConfirmDialog
          open
          onClose={() => setDeleteTarget(null)}
          onConfirm={() => deleteMutation.mutate(deleteTarget.id)}
          title="Delete template"
          description={t('Delete the template "{name}"? Accounts already created from it are not affected.', { name: deleteTarget.name })}
          confirmLabel="Delete"
          danger
          submitting={deleteMutation.isPending}
        />
      )}
    </div>
  )
}

function TemplateDialog({
  template,
  isStaff,
  onClose,
}: {
  template: PlanTemplate | 'new' | null
  isStaff: boolean
  onClose: () => void
}) {
  const { t } = useI18n()
  const { push } = useToast()
  const queryClient = useQueryClient()
  const open = template !== null
  const existing = template && template !== 'new' ? template : undefined

  const nodesQuery = useQuery({
    queryKey: ['nodes'],
    queryFn: () => apiFetch<{ nodes: { id: string; node_group: string }[] | null }>('/api/v1/nodes'),
    enabled: open,
  })
  const groups = useMemo(() => [...new Set((nodesQuery.data?.nodes ?? []).map((n) => n.node_group))].sort(), [nodesQuery.data])

  const [name, setName] = useState('')
  const [quotaGb, setQuotaGb] = useState('')
  const [durationDays, setDurationDays] = useState('')
  const [deviceLimit, setDeviceLimit] = useState('')
  const [bandwidth, setBandwidth] = useState('')
  const [nodeGroup, setNodeGroup] = useState('')
  const [shared, setShared] = useState(false)
  const [life, setLife] = useState<LifecycleState>(emptyLifecycle)
  const [routing, setRouting] = useState<RoutingState>(emptyRouting)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    setName(existing?.name ?? '')
    setQuotaGb(gbText(existing?.data_quota_bytes ?? null))
    setDurationDays(existing?.duration_days?.toString() ?? '')
    setDeviceLimit(existing?.device_limit?.toString() ?? '')
    setBandwidth(existing?.bandwidth_limit_mbps?.toString() ?? '')
    setNodeGroup(existing?.node_group ?? '')
    setShared(existing?.available_to_resellers ?? false)
    setLife(existing ? lifecycleFromTemplate(existing) : emptyLifecycle)
    setRouting(routingFrom(existing))
    setError(null)
  }, [open, existing])

  const saveMutation = useMutation({
    mutationFn: () =>
      apiFetch<PlanTemplate>(existing ? `/api/v1/templates/${existing.id}` : '/api/v1/templates', {
        method: existing ? 'PUT' : 'POST',
        body: JSON.stringify({
          name: name.trim(),
          available_to_resellers: isStaff && shared,
          data_quota_gb: quotaGb ? Number(quotaGb) : null,
          duration_days: durationDays ? Number(durationDays) : null,
          device_limit: deviceLimit ? Number(deviceLimit) : null,
          bandwidth_limit_mbps: bandwidth ? Number(bandwidth) : null,
          node_group: nodeGroup || null,
          reset_strategy: life.reset,
          on_hold: life.onHold,
          hold_deadline_days: life.onHold && life.deadlineDays ? Number(life.deadlineDays) : null,
          ...routingBody(routing),
        }),
      }),
    onSuccess: (saved) => {
      push('success', t('Template "{name}" saved', { name: saved.name }))
      queryClient.invalidateQueries({ queryKey: ['templates'] })
      onClose()
    },
    onError: (err) => setError(apiErrorMessage(err, t, 'Failed to save template')),
  })

  function submit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    saveMutation.mutate()
  }

  return (
    <Dialog open={open} onClose={onClose} title={existing ? 'Edit template' : 'New template'} maxWidthClassName="max-w-xl">
      <form onSubmit={submit} className="space-y-4">
        <Field label="Name">
          <Input value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} placeholder={t('e.g. Monthly 50 GB')} />
        </Field>
        <div className="grid grid-cols-2 gap-4">
          <Field label="Data quota (GB)">
            <Input type="number" min="0" step="0.1" value={quotaGb} onChange={(e) => setQuotaGb(e.target.value)} placeholder={t('Unlimited')} />
          </Field>
          <Field label="Duration (days)" hint={life.onHold ? 'Counted from the first connection.' : undefined}>
            <Input
              type="number"
              min="1"
              max="3650"
              required={life.onHold}
              value={durationDays}
              onChange={(e) => setDurationDays(e.target.value)}
              placeholder={t('No expiry')}
            />
          </Field>
          <Field label="Device limit">
            <Input type="number" min="1" value={deviceLimit} onChange={(e) => setDeviceLimit(e.target.value)} placeholder={t('Unlimited')} />
          </Field>
          <Field label="Bandwidth (Mbps)">
            <Input type="number" min="1" value={bandwidth} onChange={(e) => setBandwidth(e.target.value)} placeholder={t('Unlimited')} />
          </Field>
        </div>
        <Field label="Node group" hint="Accounts from this template are placed on nodes of this group.">
          <Select value={nodeGroup} onChange={(e) => setNodeGroup(e.target.value)}>
            <option value="">{t('Any group')}</option>
            {nodeGroup && !groups.includes(nodeGroup) && <option value={nodeGroup}>{nodeGroup}</option>}
            {groups.map((g) => (
              <option key={g} value={g}>
                {g}
              </option>
            ))}
          </Select>
        </Field>
        <LifecycleFields value={life} onChange={setLife} holdDaysManaged />
        <RoutingFields value={routing} onChange={setRouting} />
        {isStaff && (
          <label className="flex cursor-pointer items-start gap-2.5 rounded-lg border border-edge p-3 text-sm text-fg transition-colors hover:bg-inset/50">
            <input type="checkbox" className="mt-0.5" checked={shared} onChange={(e) => setShared(e.target.checked)} />
            <span>
              <span className="font-medium">{t('Available to resellers')}</span>
              <span className="mt-0.5 block text-xs leading-relaxed text-muted">
                {t('Resellers and their API keys can pick this template when creating accounts.')}
              </span>
            </span>
          </label>
        )}
        {error && <p className="text-sm text-danger-fg">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={saveMutation.isPending || !name.trim()}>
            {saveMutation.isPending ? t('Saving…') : t('Save template')}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
