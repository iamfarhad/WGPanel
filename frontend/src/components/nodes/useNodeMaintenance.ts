import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Wrench, PlayCircle } from 'lucide-react'
import { apiFetch } from '../../lib/api'
import { apiErrorMessage } from '../../lib/apiErrors'
import { useAuth } from '../../lib/auth'
import { useI18n } from '../../lib/i18n'
import { useToast } from '../../lib/toast'
import type { RowAction } from '../ui/RowActions'

export interface MaintenanceFields {
  id: string
  name: string
  maintenance?: boolean
  maintenance_since?: string | null
}

/** Maintenance toggle for the node row menu (operator and above). Returns the menu
 * entries for a node, so NodesPage only has to spread them into its actions. */
export function useNodeMaintenance() {
  const { t } = useI18n()
  const { push } = useToast()
  const { user } = useAuth()
  const queryClient = useQueryClient()
  const canToggle = user?.role === 'super_admin' || user?.role === 'operator'

  const mutation = useMutation({
    mutationFn: ({ node, enabled }: { node: MaintenanceFields; enabled: boolean }) =>
      apiFetch(`/api/v1/nodes/${node.id}/maintenance`, { method: 'PUT', body: JSON.stringify({ enabled }) }),
    onSuccess: (_d, { node, enabled }) => {
      queryClient.invalidateQueries({ queryKey: ['nodes'] })
      push(
        'success',
        enabled
          ? t('{name} is now in maintenance', { name: node.name })
          : t('{name} is back in rotation', { name: node.name }),
      )
    },
    onError: (err) => push('error', apiErrorMessage(err, t, 'Could not change maintenance mode')),
  })

  return (node: MaintenanceFields): RowAction[] => {
    if (!canToggle) return []
    return [
      node.maintenance
        ? { label: 'End maintenance', icon: PlayCircle, onSelect: () => mutation.mutate({ node, enabled: false }) }
        : { label: 'Start maintenance', icon: Wrench, onSelect: () => mutation.mutate({ node, enabled: true }) },
    ]
  }
}
