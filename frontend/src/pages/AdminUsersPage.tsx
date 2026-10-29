import { useEffect, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, ShieldOff, Trash2 } from 'lucide-react'
import { apiFetch, ApiError } from '../lib/api'
import { useToast } from '../lib/toast'
import { useAuth } from '../lib/auth'
import { useI18n } from '../lib/i18n'
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

interface AdminUser {
  id: string
  username: string
  role: string
  created_at: string
  totp_enabled?: boolean
  passkey_count?: number
}

const ROLE_TONE: Record<string, 'blue' | 'green' | 'slate'> = {
  super_admin: 'blue',
  operator: 'green',
  support: 'slate',
}

export function AdminUsersPage() {
  const { t, formatDate } = useI18n()
  const queryClient = useQueryClient()
  const { user: currentUser } = useAuth()
  const { push } = useToast()
  const [createOpen, setCreateOpen] = useState(false)
  const [editTarget, setEditTarget] = useState<AdminUser | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<AdminUser | null>(null)
  const [resetTarget, setResetTarget] = useState<AdminUser | null>(null)

  const adminsQuery = useQuery({
    queryKey: ['admin-users'],
    queryFn: () => apiFetch<{ admins: AdminUser[] | null }>('/api/v1/admins'),
  })
  const admins = adminsQuery.data?.admins ?? []

  function invalidate() {
    queryClient.invalidateQueries({ queryKey: ['admin-users'] })
  }

  const deleteMutation = useMutation({
    mutationFn: (id: string) => apiFetch<void>(`/api/v1/admins/${id}`, { method: 'DELETE' }),
    onSuccess: () => {
      push('success', t('{name} deleted', { name: deleteTarget?.username ?? '' }))
      invalidate()
      setDeleteTarget(null)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to delete admin'),
  })

  const resetMfaMutation = useMutation({
    mutationFn: (id: string) => apiFetch<void>(`/api/v1/admins/${id}/mfa`, { method: 'DELETE' }),
    onSuccess: () => {
      push('success', t('Two-factor authentication reset for {name}', { name: resetTarget?.username ?? '' }))
      invalidate()
      setResetTarget(null)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : t('Failed to reset two-factor authentication')),
  })

  return (
    <div>
      <PageHeader
        title="Admin Users"
        description="Panel operators with dashboard access. Role controls what they can change."
        action={
          <Button onClick={() => setCreateOpen(true)}>
            <Plus className="h-4 w-4" />
            New admin
          </Button>
        }
      />

      <Card className="overflow-hidden">
        {adminsQuery.isLoading && <TableSkeleton cols={4} />}
        {adminsQuery.isError && <ErrorState title="Could not load admin users." onRetry={() => adminsQuery.refetch()} />}
        {!adminsQuery.isLoading && !adminsQuery.isError && admins.length === 0 && (
          <EmptyState illustration="security" title="No admin users found" />
        )}
        {!adminsQuery.isLoading && !adminsQuery.isError && admins.length > 0 && (
          <Table>
            <THead>
              <tr>
                <Th>Username</Th>
                <Th>Role</Th>
                <Th>Two-factor</Th>
                <Th>Created</Th>
                <Th className="text-end">Actions</Th>
              </tr>
            </THead>
            <tbody>
              {admins.map((a) => {
                const isSelf = currentUser?.username === a.username
                return (
                  <Tr key={a.id} interactive onClick={() => setEditTarget(a)}>
                    <Td className="font-medium text-fg">
                      {a.username}
                      {isSelf && <span className="ms-2 text-xs font-normal text-faint">{t('(you)')}</span>}
                    </Td>
                    <Td>
                      <Badge tone={ROLE_TONE[a.role] ?? 'slate'}>{t(a.role.replace('_', ' '))}</Badge>
                    </Td>
                    <Td>
                      <div className="flex flex-wrap gap-1.5">
                        {a.totp_enabled && <Badge tone="green">Authenticator</Badge>}
                        {(a.passkey_count ?? 0) > 0 && (
                          <Badge tone="blue">{t('{n} passkey(s)', { n: String(a.passkey_count) })}</Badge>
                        )}
                        {!a.totp_enabled && !(a.passkey_count ?? 0) && <span className="text-faint">{t('Off')}</span>}
                      </div>
                    </Td>
                    <Td className="text-muted">{formatDate(a.created_at, { dateStyle: 'medium' })}</Td>
                    <Td className="text-end">
                      <RowActions
                        actions={[
                          ...(a.totp_enabled || (a.passkey_count ?? 0) > 0
                            ? [{ label: 'Reset two-factor', icon: ShieldOff, onSelect: () => setResetTarget(a) }]
                            : []),
                          ...(!isSelf ? [{ label: 'Delete', icon: Trash2, onSelect: () => setDeleteTarget(a), danger: true }] : []),
                        ]}
                      />
                    </Td>
                  </Tr>
                )
              })}
            </tbody>
          </Table>
        )}
      </Card>

      <CreateAdminDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={() => {
          invalidate()
          setCreateOpen(false)
        }}
      />

      {editTarget && (
        <EditAdminDialog
          admin={editTarget}
          onClose={() => setEditTarget(null)}
          onSaved={() => {
            invalidate()
            setEditTarget(null)
          }}
        />
      )}

      {resetTarget && (
        <ConfirmDialog
          open
          onClose={() => setResetTarget(null)}
          onConfirm={() => resetMfaMutation.mutate(resetTarget.id)}
          title="Reset two-factor authentication"
          description={t('This removes the authenticator app, recovery codes and passkeys of "{name}". They can sign in with just their password until they set a new second step.', {
            name: resetTarget.username,
          })}
          confirmLabel="Reset"
          danger
          submitting={resetMfaMutation.isPending}
        />
      )}

      {deleteTarget && (
        <ConfirmDialog
          open={!!deleteTarget}
          onClose={() => setDeleteTarget(null)}
          onConfirm={() => deleteMutation.mutate(deleteTarget.id)}
          title="Delete admin"
          description={t('This will permanently remove "{name}"\'s access to the panel. This cannot be undone.', {
            name: deleteTarget.username,
          })}
          confirmLabel="Delete"
          danger
          submitting={deleteMutation.isPending}
        />
      )}
    </div>
  )
}

function EditAdminDialog({
  admin,
  onClose,
  onSaved,
}: {
  admin: AdminUser
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useI18n()
  const { push } = useToast()
  const [role, setRole] = useState(admin.role)
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    setRole(admin.role)
    setPassword('')
    setError(null)
  }, [admin])

  const saveMutation = useMutation({
    mutationFn: () => {
      const body: Record<string, string> = {}
      if (role !== admin.role) body.role = role
      if (password) body.password = password
      return apiFetch(`/api/v1/admins/${admin.id}`, { method: 'PATCH', body: JSON.stringify(body) })
    },
    onSuccess: () => {
      push('success', t('{name} updated', { name: admin.username }))
      onSaved()
    },
    onError: (err) => setError(err instanceof ApiError ? err.message : t('Failed to update admin')),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    if (role === admin.role && !password) {
      onClose()
      return
    }
    saveMutation.mutate()
  }

  return (
    <Dialog open onClose={onClose} title={t('Edit {name}', { name: admin.username })}>
      <form onSubmit={handleSubmit} className="space-y-4">
        <Field label="Role">
          <Select value={role} onChange={(e) => setRole(e.target.value)}>
            <option value="support">{t('Support (read-only)')}</option>
            <option value="operator">{t('Operator')}</option>
            <option value="super_admin">{t('Super admin')}</option>
          </Select>
        </Field>
        <Field label="Reset password">
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder={t('Leave blank to keep current password')}
            autoComplete="new-password"
          />
        </Field>
        {error && <p className="text-sm text-danger-fg">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={saveMutation.isPending}>
            {saveMutation.isPending ? 'Saving…' : 'Save changes'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

function CreateAdminDialog({
  open,
  onClose,
  onCreated,
}: {
  open: boolean
  onClose: () => void
  onCreated: () => void
}) {
  const { t } = useI18n()
  const { push } = useToast()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState('support')
  const [error, setError] = useState<string | null>(null)

  const createMutation = useMutation({
    mutationFn: () =>
      apiFetch('/api/v1/admins', {
        method: 'POST',
        body: JSON.stringify({ username, password, role }),
      }),
    onSuccess: () => {
      push('success', t('Admin "{name}" created', { name: username }))
      setUsername('')
      setPassword('')
      setRole('support')
      onCreated()
    },
    onError: (err) => setError(err instanceof ApiError ? err.message : t('Failed to create admin')),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    createMutation.mutate()
  }

  return (
    <Dialog open={open} onClose={onClose} title="New admin user">
      <form onSubmit={handleSubmit} className="space-y-4">
        <Field label="Username">
          <Input value={username} onChange={(e) => setUsername(e.target.value)} required autoComplete="off" />
        </Field>
        <Field label="Password">
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
            autoComplete="new-password"
          />
        </Field>
        <Field label="Role">
          <Select value={role} onChange={(e) => setRole(e.target.value)}>
            <option value="support">{t('Support (read-only)')}</option>
            <option value="operator">{t('Operator')}</option>
            <option value="super_admin">{t('Super admin')}</option>
          </Select>
        </Field>
        {error && <p className="text-sm text-danger-fg">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={createMutation.isPending}>
            {createMutation.isPending ? 'Creating…' : 'Create admin'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
