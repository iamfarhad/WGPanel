import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '../../lib/api'
import { apiErrorMessage } from '../../lib/apiErrors'
import { useAuth } from '../../lib/auth'
import { useToast } from '../../lib/toast'
import { useI18n } from '../../lib/i18n'
import { Card, CardHeader } from '../ui/Card'
import { Badge } from '../ui/Badge'
import { ErrorState } from '../ui/ErrorState'
import { Skeleton } from '../ui/Skeleton'

/** Settings -> the Marzban-compatible API switch (docs/marzban-compat.md). Off by
 * default: while off, every Marzban route on the panel answers 404. Only a super admin
 * can flip it; other roles see the state. */
export function MarzbanCompatCard() {
  const { t, tx } = useI18n()
  const { user } = useAuth()
  const { push } = useToast()
  const queryClient = useQueryClient()
  const canEdit = user?.role === 'super_admin'

  const query = useQuery({
    queryKey: ['marzban-compat'],
    queryFn: () => apiFetch<{ enabled: boolean }>('/api/v1/marzban-compat'),
  })
  const enabled = query.data?.enabled ?? false

  const save = useMutation({
    mutationFn: (next: boolean) =>
      apiFetch<{ enabled: boolean }>('/api/v1/marzban-compat', { method: 'PUT', body: JSON.stringify({ enabled: next }) }),
    onSuccess: (saved) => {
      queryClient.setQueryData(['marzban-compat'], saved)
      push('success', saved.enabled ? 'Marzban-compatible API enabled' : 'Marzban-compatible API disabled')
    },
    onError: (err) => push('error', apiErrorMessage(err, t)),
  })

  return (
    <Card className="overflow-hidden">
      <CardHeader
        title="Sales bot compatibility (Marzban API)"
        description="Lets Telegram sales bots that support Marzban work with this panel."
        action={
          query.data && <Badge dot tone={enabled ? 'green' : 'slate'}>{enabled ? 'enabled' : 'disabled'}</Badge>
        }
      />
      {query.isLoading && <Skeleton className="m-6 h-24" />}
      {query.isError && <ErrorState title="Could not load this setting." onRetry={() => query.refetch()} />}
      {query.data && (
        <div className="space-y-4 p-6">
          <p className="text-sm leading-relaxed text-muted">
            {tx(
              'When on, bots such as Mirza panel connect with the panel type <b>Marzban</b>: the username is an API key ID and the password is its secret. Each key keeps its own permissions, node groups and rate limit. Create keys on the API Keys page, which also shows the connection steps.',
              { b: (c) => <strong>{c}</strong> },
            )}
          </p>
          <label className={`flex items-center gap-2 text-sm font-medium text-fg ${canEdit ? 'cursor-pointer' : 'opacity-60'}`}>
            <input
              type="checkbox"
              className="h-4 w-4 accent-accent"
              checked={enabled}
              disabled={!canEdit || save.isPending}
              onChange={(e) => save.mutate(e.target.checked)}
            />
            {t('Enable the Marzban-compatible API')}
          </label>
          {!canEdit && <p className="text-xs text-faint">{t('Only a super admin can change this.')}</p>}
        </div>
      )}
    </Card>
  )
}
