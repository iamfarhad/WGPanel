import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from './api'

export interface AppNotification {
  id: number
  event: string
  severity: 'info' | 'warning' | 'critical'
  title: string
  body: string
  data: Record<string, unknown> | null
  read: boolean
  created_at: string
}

export interface NotificationsResponse {
  items: AppNotification[]
  unread_count: number
  /** Enabled notification channels with 3+ failed deliveries in a row (staff only). */
  broken_channels: number
}

export const notificationsKey = ['notifications'] as const

/** Shared by the bell, the notifications page and the broken-channel banner, so they
 * hit the API once per poll. */
export function useNotifications(limit = 30) {
  return useQuery({
    queryKey: [...notificationsKey, limit],
    queryFn: () => apiFetch<NotificationsResponse>(`/api/v1/notifications?limit=${limit}`),
    refetchInterval: 30_000,
    refetchOnWindowFocus: true,
  })
}

export function useNotificationMutations() {
  const queryClient = useQueryClient()
  const invalidate = () => queryClient.invalidateQueries({ queryKey: notificationsKey })
  const markRead = useMutation({
    mutationFn: (id: number) => apiFetch(`/api/v1/notifications/${id}/read`, { method: 'POST' }),
    onSuccess: invalidate,
  })
  const markAllRead = useMutation({
    mutationFn: () => apiFetch('/api/v1/notifications/read-all', { method: 'POST' }),
    onSuccess: invalidate,
  })
  return { markRead, markAllRead }
}
