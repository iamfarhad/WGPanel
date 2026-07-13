import { useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, KeyRound, Ban, RotateCw, Copy, AlertTriangle } from 'lucide-react'
import { apiFetch, ApiError } from '../lib/api'
import { useToast } from '../lib/toast'
import { PageHeader } from '../components/ui/PageHeader'
import { Card } from '../components/ui/Card'
import { Button } from '../components/ui/Button'
import { Input } from '../components/ui/Input'
import { Dialog } from '../components/ui/Dialog'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { Badge } from '../components/ui/Badge'
import { EmptyState } from '../components/ui/EmptyState'
import { TableSkeleton } from '../components/ui/Skeleton'

interface ApiKey {
  id: string
  key_id: string
  label: string
  node_groups: string[]
  permissions: string[]
  revoked: boolean
  created_at: string
}

interface ApiKeyCreated extends ApiKey {
  secret: string
}

const ALL_PERMISSIONS = ['read', 'create', 'update', 'suspend', 'delete']

export function ApiKeysPage() {
  const queryClient = useQueryClient()
  const { push } = useToast()
  const [createOpen, setCreateOpen] = useState(false)
  const [revokeTarget, setRevokeTarget] = useState<ApiKey | null>(null)
  const [revealedSecret, setRevealedSecret] = useState<{ keyId: string; secret: string } | null>(null)

  const keysQuery = useQuery({
    queryKey: ['api-keys'],
    queryFn: () => apiFetch<{ api_keys: ApiKey[] | null }>('/api/v1/api-keys'),
  })
  const keys = keysQuery.data?.api_keys ?? []

  function invalidate() {
    queryClient.invalidateQueries({ queryKey: ['api-keys'] })
  }

  const revokeMutation = useMutation({
    mutationFn: (id: string) => apiFetch(`/api/v1/api-keys/${id}/revoke`, { method: 'POST' }),
    onSuccess: () => {
      push('success', 'API key revoked')
      invalidate()
      setRevokeTarget(null)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to revoke key'),
  })

  const rotateMutation = useMutation({
    mutationFn: (id: string) => apiFetch<ApiKeyCreated>(`/api/v1/api-keys/${id}/rotate`, { method: 'POST' }),
    onSuccess: (key) => {
      push('success', `Secret rotated for ${key.label}`)
      invalidate()
      setRevealedSecret({ keyId: key.key_id, secret: key.secret })
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to rotate key'),
  })

  return (
    <div>
      <PageHeader
        title="API Keys"
        description="Credentials for the Telegram sales bot and other integrations to call this panel's API."
        action={
          <Button onClick={() => setCreateOpen(true)}>
            <Plus className="mr-2 h-4 w-4" />
            New API key
          </Button>
        }
      />

      <Card>
        {keysQuery.isLoading && <TableSkeleton cols={5} />}
        {keysQuery.isError && <p className="p-6 text-sm text-red-600 dark:text-red-400">Could not load API keys.</p>}
        {!keysQuery.isLoading && !keysQuery.isError && keys.length === 0 && (
          <EmptyState icon={KeyRound} title="No API keys yet" description="Create one to let your Telegram bot talk to this panel." />
        )}
        {!keysQuery.isLoading && !keysQuery.isError && keys.length > 0 && (
          <table className="w-full text-left text-sm">
            <thead className="border-b border-slate-200 text-slate-500 dark:border-slate-800 dark:text-slate-400">
              <tr>
                <th className="px-6 py-3 font-medium">Label</th>
                <th className="px-6 py-3 font-medium">Key ID</th>
                <th className="px-6 py-3 font-medium">Permissions</th>
                <th className="px-6 py-3 font-medium">Node groups</th>
                <th className="px-6 py-3 font-medium">Status</th>
                <th className="px-6 py-3 font-medium">Actions</th>
              </tr>
            </thead>
            <tbody>
              {keys.map((k) => (
                <tr key={k.id} className="border-b border-slate-100 last:border-0 dark:border-slate-800">
                  <td className="px-6 py-3 text-slate-900 dark:text-slate-100">{k.label}</td>
                  <td className="px-6 py-3 font-mono text-xs text-slate-500 dark:text-slate-400">{k.key_id}</td>
                  <td className="px-6 py-3">
                    <div className="flex flex-wrap gap-1">
                      {k.permissions.map((p) => (
                        <Badge key={p} tone="blue">
                          {p}
                        </Badge>
                      ))}
                    </div>
                  </td>
                  <td className="px-6 py-3 text-slate-500 dark:text-slate-400">
                    {k.node_groups.length > 0 ? k.node_groups.join(', ') : 'all'}
                  </td>
                  <td className="px-6 py-3">
                    <Badge tone={k.revoked ? 'red' : 'green'}>{k.revoked ? 'revoked' : 'active'}</Badge>
                  </td>
                  <td className="px-6 py-3">
                    {!k.revoked && (
                      <div className="flex gap-2">
                        <Button variant="secondary" onClick={() => rotateMutation.mutate(k.id)} disabled={rotateMutation.isPending}>
                          <RotateCw className="mr-1 h-3.5 w-3.5" />
                          Rotate
                        </Button>
                        <Button
                          variant="secondary"
                          onClick={() => setRevokeTarget(k)}
                          className="text-red-600 hover:bg-red-50 dark:text-red-400 dark:hover:bg-red-950"
                        >
                          <Ban className="mr-1 h-3.5 w-3.5" />
                          Revoke
                        </Button>
                      </div>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>

      <CreateApiKeyDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={(created) => {
          invalidate()
          setCreateOpen(false)
          setRevealedSecret({ keyId: created.key_id, secret: created.secret })
        }}
      />

      {revokeTarget && (
        <ConfirmDialog
          open={!!revokeTarget}
          onClose={() => setRevokeTarget(null)}
          onConfirm={() => revokeMutation.mutate(revokeTarget.id)}
          title="Revoke API key"
          description={`"${revokeTarget.label}" will immediately stop being able to authenticate. This cannot be undone.`}
          confirmLabel="Revoke"
          danger
          submitting={revokeMutation.isPending}
        />
      )}

      {revealedSecret && (
        <Dialog open onClose={() => setRevealedSecret(null)} title="Secret key">
          <div className="mb-4 flex items-start gap-2 rounded-md bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/30 dark:text-amber-300">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
            <span>This secret is shown once. Store it securely — it cannot be retrieved again.</span>
          </div>
          <div className="space-y-3">
            <div>
              <p className="mb-1 text-xs font-medium text-slate-500 dark:text-slate-400">Key ID</p>
              <p className="rounded-md bg-slate-100 p-2 font-mono text-xs text-slate-800 dark:bg-slate-950 dark:text-slate-200">
                {revealedSecret.keyId}
              </p>
            </div>
            <div>
              <p className="mb-1 text-xs font-medium text-slate-500 dark:text-slate-400">Secret</p>
              <p className="break-all rounded-md bg-slate-100 p-2 font-mono text-xs text-slate-800 dark:bg-slate-950 dark:text-slate-200">
                {revealedSecret.secret}
              </p>
            </div>
          </div>
          <div className="mt-4 flex justify-end">
            <Button
              variant="secondary"
              onClick={() => {
                navigator.clipboard.writeText(revealedSecret.secret)
                push('success', 'Secret copied to clipboard')
              }}
            >
              <Copy className="mr-2 h-4 w-4" />
              Copy secret
            </Button>
          </div>
        </Dialog>
      )}
    </div>
  )
}

function CreateApiKeyDialog({
  open,
  onClose,
  onCreated,
}: {
  open: boolean
  onClose: () => void
  onCreated: (created: ApiKeyCreated) => void
}) {
  const [label, setLabel] = useState('')
  const [nodeGroups, setNodeGroups] = useState('')
  const [permissions, setPermissions] = useState<string[]>(['read'])
  const [error, setError] = useState<string | null>(null)

  const createMutation = useMutation({
    mutationFn: () =>
      apiFetch<ApiKeyCreated>('/api/v1/api-keys', {
        method: 'POST',
        body: JSON.stringify({
          label,
          node_groups: nodeGroups
            .split(',')
            .map((g) => g.trim())
            .filter(Boolean),
          permissions,
        }),
      }),
    onSuccess: (created) => {
      setLabel('')
      setNodeGroups('')
      setPermissions(['read'])
      onCreated(created)
    },
    onError: (err) => setError(err instanceof ApiError ? err.message : 'Failed to create API key'),
  })

  function togglePermission(perm: string) {
    setPermissions((prev) => (prev.includes(perm) ? prev.filter((p) => p !== perm) : [...prev, perm]))
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    createMutation.mutate()
  }

  return (
    <Dialog open={open} onClose={onClose} title="New API key">
      <form onSubmit={handleSubmit} className="space-y-4">
        <div>
          <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">Label</label>
          <Input value={label} onChange={(e) => setLabel(e.target.value)} required placeholder="e.g. telegram-sales-bot" />
        </div>
        <div>
          <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">
            Node groups <span className="font-normal text-slate-400">(comma-separated, blank = all)</span>
          </label>
          <Input value={nodeGroups} onChange={(e) => setNodeGroups(e.target.value)} placeholder="eu-west, us-east" />
        </div>
        <div>
          <label className="mb-2 block text-sm font-medium text-slate-700 dark:text-slate-300">Permissions</label>
          <div className="flex flex-wrap gap-2">
            {ALL_PERMISSIONS.map((perm) => (
              <button
                type="button"
                key={perm}
                onClick={() => togglePermission(perm)}
                className={`rounded-full border px-3 py-1 text-xs font-medium transition-colors ${
                  permissions.includes(perm)
                    ? 'border-slate-900 bg-slate-900 text-white dark:border-slate-100 dark:bg-slate-100 dark:text-slate-900'
                    : 'border-slate-300 text-slate-600 hover:border-slate-400 dark:border-slate-700 dark:text-slate-400'
                }`}
              >
                {perm}
              </button>
            ))}
          </div>
        </div>
        {error && <p className="text-sm text-red-600 dark:text-red-400">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={createMutation.isPending || permissions.length === 0}>
            {createMutation.isPending ? 'Creating…' : 'Create key'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
