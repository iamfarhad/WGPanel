import { useState } from 'react'
import { CheckCheck } from 'lucide-react'
import { useI18n } from '../lib/i18n'
import { notificationText } from '../lib/notificationEvents'
import { useNotificationMutations, useNotifications } from '../lib/notifications'
import { PageHeader } from '../components/ui/PageHeader'
import { Card } from '../components/ui/Card'
import { Button } from '../components/ui/Button'
import { Badge } from '../components/ui/Badge'
import { EmptyState } from '../components/ui/EmptyState'
import { ErrorState } from '../components/ui/ErrorState'
import { TableSkeleton } from '../components/ui/Skeleton'
import { SeverityIcon } from '../components/NotificationBell'

export function NotificationsPage() {
  const { t, formatDate } = useI18n()
  const query = useNotifications(100)
  const { markRead, markAllRead } = useNotificationMutations()
  const [unreadOnly, setUnreadOnly] = useState(false)

  const all = query.data?.items ?? []
  const items = unreadOnly ? all.filter((n) => !n.read) : all
  const unread = query.data?.unread_count ?? 0

  return (
    <div>
      <PageHeader
        title="Notifications"
        description="Alerts about nodes, accounts, plans and backups. They are kept for 30 days."
        action={
          <div className="flex items-center gap-2">
            <Button variant="secondary" size="sm" onClick={() => setUnreadOnly((v) => !v)} aria-pressed={unreadOnly}>
              {unreadOnly ? 'Show all' : 'Show unread only'}
            </Button>
            <Button size="sm" disabled={unread === 0 || markAllRead.isPending} onClick={() => markAllRead.mutate()}>
              <CheckCheck className="h-4 w-4" />
              Mark all as read
            </Button>
          </div>
        }
      />
      <Card className="overflow-hidden">
        {query.isLoading && <TableSkeleton cols={3} />}
        {query.isError && <ErrorState title="Could not load notifications." onRetry={() => query.refetch()} />}
        {!query.isLoading && !query.isError && items.length === 0 && (
          <EmptyState
            illustration="success"
            title={unreadOnly ? 'No unread notifications' : 'Nothing to report'}
            description="Alerts show up here when a node goes down, a quota runs out or a backup fails."
          />
        )}
        {items.map((n) => {
          const text = notificationText(n, t, (v) => formatDate(v))
          return (
            <div
              key={n.id}
              className={`flex items-start gap-3 border-b border-edge/70 px-5 py-4 last:border-0 ${n.read ? '' : 'bg-accent-soft/40'}`}
            >
              <span className="mt-0.5 shrink-0">
                <SeverityIcon severity={n.severity} />
              </span>
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <p className="text-sm font-medium text-fg">{text.title}</p>
                  <Badge tone={n.severity === 'critical' ? 'red' : n.severity === 'warning' ? 'amber' : 'blue'}>
                    {n.severity === 'critical' ? 'Critical' : n.severity === 'warning' ? 'Warning' : 'Info'}
                  </Badge>
                </div>
                <p className="mt-1 text-sm leading-relaxed break-words text-muted">{text.body}</p>
                <p className="mt-1 text-xs text-faint">{formatDate(n.created_at)}</p>
              </div>
              {!n.read && (
                <Button variant="ghost" size="sm" onClick={() => markRead.mutate(n.id)} disabled={markRead.isPending}>
                  Mark as read
                </Button>
              )}
            </div>
          )
        })}
      </Card>
    </div>
  )
}
