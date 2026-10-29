import { useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Bell, Mail, Pencil, Plus, Send, Trash2 } from 'lucide-react'
import { apiFetch } from '../../lib/api'
import { apiErrorMessage } from '../../lib/apiErrors'
import { useToast } from '../../lib/toast'
import { useI18n } from '../../lib/i18n'
import { EVENT_CATALOG } from '../../lib/notificationEvents'
import { Card } from '../ui/Card'
import { Button } from '../ui/Button'
import { Input } from '../ui/Input'
import { Select } from '../ui/Select'
import { Field } from '../ui/Field'
import { Dialog } from '../ui/Dialog'
import { ConfirmDialog } from '../ui/ConfirmDialog'
import { Badge } from '../ui/Badge'
import { EmptyState } from '../ui/EmptyState'
import { ErrorState } from '../ui/ErrorState'
import { Skeleton } from '../ui/Skeleton'
import { RowActions } from '../ui/RowActions'
import { EventPicker } from '../EventPicker'

interface Channel {
  id: string
  kind: 'telegram' | 'email'
  name: string
  enabled: boolean
  config: { chat_ids?: string[]; host?: string; port?: number; username?: string; from?: string; to?: string[]; tls?: string }
  has_secret: boolean
  events: string[]
  lang: 'en' | 'fa'
  broken: boolean
  last_error: string
  last_success_at: string | null
}

const splitList = (s: string) =>
  s
    .split(/[\s,;]+/)
    .map((x) => x.trim())
    .filter(Boolean)

/** Settings -> Notifications: Telegram / email channels with per-channel event
 * subscriptions, language and a "Send test" button. super_admin only. */
export function NotificationChannelsCard() {
  const { t } = useI18n()
  const { push } = useToast()
  const queryClient = useQueryClient()
  const [editing, setEditing] = useState<Channel | 'new' | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<Channel | null>(null)

  const query = useQuery({
    queryKey: ['notification-channels'],
    queryFn: () => apiFetch<{ items: Channel[] }>('/api/v1/notification-channels'),
  })
  const channels = query.data?.items ?? []
  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ['notification-channels'] })
    queryClient.invalidateQueries({ queryKey: ['notifications'] })
  }

  const test = useMutation({
    mutationFn: (id: string) => apiFetch(`/api/v1/notification-channels/${id}/test`, { method: 'POST' }),
    onSuccess: () => {
      push('success', 'Test message sent')
      refresh()
    },
    onError: (err) => {
      push('error', apiErrorMessage(err, t))
      refresh()
    },
  })
  const remove = useMutation({
    mutationFn: (id: string) => apiFetch(`/api/v1/notification-channels/${id}`, { method: 'DELETE' }),
    onSuccess: () => {
      push('success', 'Channel deleted')
      setDeleteTarget(null)
      refresh()
    },
    onError: (err) => push('error', apiErrorMessage(err, t)),
  })

  return (
    <Card className="overflow-hidden">
      <div className="flex items-center gap-3 border-b border-edge px-6 py-4">
        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-accent-soft text-accent-fg">
          <Bell className="h-4 w-4" />
        </div>
        <div className="min-w-0 flex-1">
          <h2 className="text-sm font-semibold tracking-tight text-fg">{t('Notifications')}</h2>
          <p className="mt-0.5 text-xs leading-relaxed text-muted">
            {t('Send alerts to a Telegram chat or by email. The notification bell in the panel always shows every alert.')}
          </p>
        </div>
        <Button size="sm" onClick={() => setEditing('new')}>
          <Plus className="h-4 w-4" />
          Add channel
        </Button>
      </div>

      {query.isLoading && <Skeleton className="m-6 h-16" />}
      {query.isError && <ErrorState title="Could not load notification channels." onRetry={() => query.refetch()} />}
      {!query.isLoading && !query.isError && channels.length === 0 && (
        <EmptyState
          illustration="wave"
          title="No notification channels yet"
          description="Add a Telegram chat or an email address to hear about node outages and backups without opening the panel."
        />
      )}
      {channels.map((c) => (
        <div key={c.id} className="flex items-start gap-3 border-b border-edge/70 px-6 py-4 last:border-0">
          <div className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-inset text-muted">
            {c.kind === 'telegram' ? <Send className="h-4 w-4" /> : <Mail className="h-4 w-4" />}
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <p className="text-sm font-medium text-fg">{c.name}</p>
              <Badge>{c.kind === 'telegram' ? 'Telegram' : 'Email'}</Badge>
              <Badge>{c.lang === 'fa' ? 'Persian' : 'English'}</Badge>
              {!c.enabled ? (
                <Badge dot>Disabled</Badge>
              ) : c.broken ? (
                <Badge tone="red" dot>Broken</Badge>
              ) : c.last_success_at ? (
                <Badge tone="green" dot>Working</Badge>
              ) : null}
            </div>
            <p className="mt-1 text-xs text-muted">
              {c.events.length === 0
                ? t('Not subscribed to any events')
                : t('{count} of {total} events', { count: c.events.length, total: EVENT_CATALOG.length })}
            </p>
            {c.broken && c.last_error && <p className="ltr mt-1 text-xs break-words text-danger-fg">{c.last_error}</p>}
          </div>
          <RowActions
            actions={[
              { label: 'Send test', icon: Send, onSelect: () => test.mutate(c.id) },
              { label: 'Edit', icon: Pencil, onSelect: () => setEditing(c) },
              { label: 'Delete', icon: Trash2, danger: true, onSelect: () => setDeleteTarget(c) },
            ]}
          />
        </div>
      ))}

      {editing && (
        <ChannelDialog
          channel={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            push('success', 'Channel saved')
            refresh()
          }}
        />
      )}
      {deleteTarget && (
        <ConfirmDialog
          open
          onClose={() => setDeleteTarget(null)}
          onConfirm={() => remove.mutate(deleteTarget.id)}
          title="Delete notification channel"
          description="Alerts stop being sent to this channel."
          confirmLabel="Delete"
          danger
          submitting={remove.isPending}
        />
      )}
    </Card>
  )
}

function ChannelDialog({ channel, onClose, onSaved }: { channel: Channel | null; onClose: () => void; onSaved: () => void }) {
  const { t } = useI18n()
  const { push } = useToast()
  const [kind, setKind] = useState<'telegram' | 'email'>(channel?.kind ?? 'telegram')
  const [name, setName] = useState(channel?.name ?? '')
  const [lang, setLang] = useState<'en' | 'fa'>(channel?.lang ?? 'en')
  const [enabled, setEnabled] = useState(channel?.enabled ?? true)
  const [events, setEvents] = useState<string[]>(channel?.events ?? ['node.down', 'node.recovered', 'backup.failed'])
  const [secret, setSecret] = useState('')
  const [chatIds, setChatIds] = useState((channel?.config.chat_ids ?? []).join(', '))
  const [host, setHost] = useState(channel?.config.host ?? '')
  const [port, setPort] = useState(String(channel?.config.port ?? 587))
  const [username, setUsername] = useState(channel?.config.username ?? '')
  const [from, setFrom] = useState(channel?.config.from ?? '')
  const [to, setTo] = useState((channel?.config.to ?? []).join(', '))
  const [tls, setTls] = useState(channel?.config.tls ?? 'starttls')

  const save = useMutation({
    mutationFn: () => {
      const config = kind === 'telegram' ? { chat_ids: splitList(chatIds) } : { host: host.trim(), port: Number(port), username: username.trim(), from: from.trim(), to: splitList(to), tls }
      const body = { ...(channel ? {} : { kind }), name, lang, enabled, events, config, ...(secret ? { secret } : {}) }
      return apiFetch(channel ? `/api/v1/notification-channels/${channel.id}` : '/api/v1/notification-channels', {
        method: channel ? 'PATCH' : 'POST',
        body: JSON.stringify(body),
      })
    },
    onSuccess: onSaved,
    onError: (err) => push('error', apiErrorMessage(err, t)),
  })

  function submit(e: FormEvent) {
    e.preventDefault()
    save.mutate()
  }

  return (
    <Dialog open onClose={onClose} title={channel ? 'Edit channel' : 'Add channel'} maxWidthClassName="max-w-2xl">
      <form onSubmit={submit} className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Type">
            <Select value={kind} disabled={!!channel} onChange={(e) => setKind(e.target.value as 'telegram' | 'email')}>
              <option value="telegram">Telegram</option>
              <option value="email">{t('Email (SMTP)')}</option>
            </Select>
          </Field>
          <Field label="Name">
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={kind === 'telegram' ? 'Ops chat' : 'On-call email'} maxLength={60} />
          </Field>
        </div>

        {kind === 'telegram' ? (
          <>
            <Field
              label="Bot token"
              hint={channel?.has_secret ? 'A token is saved. Leave empty to keep it.' : 'Create a bot with @BotFather and paste its token. It is stored encrypted.'}
            >
              <Input className="ltr" type="password" autoComplete="off" required={!channel} value={secret} onChange={(e) => setSecret(e.target.value)} placeholder="123456:ABC…" />
            </Field>
            <Field label="Chat IDs" hint="One or more chat or channel IDs, separated by commas. The bot must be a member of each chat.">
              <Input className="ltr" required value={chatIds} onChange={(e) => setChatIds(e.target.value)} placeholder="-1001234567890" />
            </Field>
          </>
        ) : (
          <>
            <div className="grid gap-4 sm:grid-cols-[1fr_7rem_10rem]">
              <Field label="SMTP host">
                <Input className="ltr" required value={host} onChange={(e) => setHost(e.target.value)} placeholder="smtp.example.com" />
              </Field>
              <Field label="Port">
                <Input className="ltr" type="number" required min={1} max={65535} value={port} onChange={(e) => setPort(e.target.value)} />
              </Field>
              <Field label="Encryption">
                <Select value={tls} onChange={(e) => setTls(e.target.value)}>
                  <option value="starttls">STARTTLS</option>
                  <option value="tls">TLS</option>
                  <option value="none">{t('None')}</option>
                </Select>
              </Field>
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Username" labelSuffix="(optional)">
                <Input className="ltr" autoComplete="off" value={username} onChange={(e) => setUsername(e.target.value)} />
              </Field>
              <Field label="Password" hint={channel?.has_secret ? 'A password is saved. Leave empty to keep it.' : undefined}>
                <Input className="ltr" type="password" autoComplete="new-password" value={secret} onChange={(e) => setSecret(e.target.value)} />
              </Field>
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="From address">
                <Input className="ltr" type="email" required value={from} onChange={(e) => setFrom(e.target.value)} placeholder="alerts@example.com" />
              </Field>
              <Field label="Recipients" hint="Separate several addresses with commas.">
                <Input className="ltr" required value={to} onChange={(e) => setTo(e.target.value)} placeholder="ops@example.com" />
              </Field>
            </div>
          </>
        )}

        <Field label="Message language">
          <Select value={lang} onChange={(e) => setLang(e.target.value as 'en' | 'fa')}>
            <option value="en">English</option>
            <option value="fa">فارسی</option>
          </Select>
        </Field>
        <Field label="Send these events" hint="Critical alerts that fail on Telegram are also sent to your email channels.">
          <div className="mb-2 flex gap-3 text-xs">
            <button type="button" className="cursor-pointer font-medium text-accent-fg hover:underline" onClick={() => setEvents(EVENT_CATALOG.map((e) => e.type))}>
              {t('Select all')}
            </button>
            <button type="button" className="cursor-pointer font-medium text-accent-fg hover:underline" onClick={() => setEvents([])}>
              {t('Clear')}
            </button>
          </div>
          <EventPicker value={events} onChange={setEvents} />
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
            {save.isPending ? 'Saving…' : 'Save'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
