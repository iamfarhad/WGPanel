import { useI18n } from '../../lib/i18n'
import { Badge } from '../ui/Badge'
import type { MaintenanceFields } from './useNodeMaintenance'

/** Badge shown next to a node's status while it is in maintenance mode. */
export function MaintenanceBadge({ node }: { node: MaintenanceFields }) {
  const { t, formatDate } = useI18n()
  if (!node.maintenance) return null
  return (
    <Badge
      tone="amber"
      className="ms-1.5"
      title={node.maintenance_since ? t('In maintenance since {date}', { date: formatDate(node.maintenance_since) }) : undefined}
    >
      {t('Maintenance')}
    </Badge>
  )
}
