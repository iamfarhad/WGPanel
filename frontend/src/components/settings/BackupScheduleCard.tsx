import { useEffect, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { CloudUpload, Play, Save } from 'lucide-react'
import { apiFetch } from '../../lib/api'
import { apiErrorMessage } from '../../lib/apiErrors'
import { useToast } from '../../lib/toast'
import { useI18n } from '../../lib/i18n'
import { Card } from '../ui/Card'
import { Button } from '../ui/Button'
import { Input } from '../ui/Input'
import { Field } from '../ui/Field'
import { Badge } from '../ui/Badge'
import { ErrorState } from '../ui/ErrorState'
import { Skeleton } from '../ui/Skeleton'

interface BackupRun {
  destination: 'telegram' | 's3'
  trigger: 'schedule' | 'manual'
  status: 'success' | 'failed'
  error: string
  filename: string
  size_bytes: number
  created_at: string
}

interface Schedule {
  enabled: boolean
  interval_hours: number
  retention_count: number
  has_password: boolean
  telegram: { enabled: boolean; has_token: boolean; chat_id: string }
  s3: {
    enabled: boolean
    endpoint: string
    region: string
    bucket: string
    prefix: string
    access_key: string
    has_secret: boolean
    path_style: boolean
  }
  last_run_at: string | null
  running: boolean
  last: Partial<Record<'telegram' | 's3', BackupRun>>
}

function formatSize(n: number): string {
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)} MB`
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)} KB`
  return `${n} B`
}

const INTERVALS = [1, 6, 12, 24, 48, 168]

/** Settings -> scheduled off-box backups: encrypted panel backups pushed to Telegram
 * and/or an S3-compatible bucket on an interval, with retention and a run-now button.
 * super_admin only. */
export function BackupScheduleCard() {
  const { t, formatDate } = useI18n()
  const { push } = useToast()
  const queryClient = useQueryClient()

  const query = useQuery({
    queryKey: ['backup-schedule'],
    queryFn: () => apiFetch<Schedule>('/api/v1/backup-schedule'),
    // Poll while a run is in flight so the result appears without a manual refresh.
    refetchInterval: (q) => (q.state.data?.running ? 2000 : false),
  })
  const data = query.data

  const [enabled, setEnabled] = useState(false)
  const [interval, setIntervalHours] = useState('24')
  const [retention, setRetention] = useState('7')
  const [password, setPassword] = useState('')
  const [tgEnabled, setTgEnabled] = useState(false)
  const [tgToken, setTgToken] = useState('')
  const [tgChat, setTgChat] = useState('')
  const [s3Enabled, setS3Enabled] = useState(false)
  const [s3Endpoint, setS3Endpoint] = useState('')
  const [s3Region, setS3Region] = useState('us-east-1')
  const [s3Bucket, setS3Bucket] = useState('')
  const [s3Prefix, setS3Prefix] = useState('')
  const [s3Access, setS3Access] = useState('')
  const [s3Secret, setS3Secret] = useState('')
  const [s3Path, setS3Path] = useState(true)
  const [loadedFor, setLoadedFor] = useState<Schedule | null>(null)

  // Load the form once per server snapshot of the configuration (not on every poll).
  useEffect(() => {
    if (!data || loadedFor) return
    setLoadedFor(data)
    setEnabled(data.enabled)
    setIntervalHours(String(data.interval_hours))
    setRetention(String(data.retention_count))
    setTgEnabled(data.telegram.enabled)
    setTgChat(data.telegram.chat_id)
    setS3Enabled(data.s3.enabled)
    setS3Endpoint(data.s3.endpoint)
    setS3Region(data.s3.region)
    setS3Bucket(data.s3.bucket)
    setS3Prefix(data.s3.prefix)
    setS3Access(data.s3.access_key)
    setS3Path(data.s3.path_style)
  }, [data, loadedFor])

  const save = useMutation({
    mutationFn: () =>
      apiFetch<Schedule>('/api/v1/backup-schedule', {
        method: 'PUT',
        body: JSON.stringify({
          enabled,
          interval_hours: Number(interval),
          retention_count: Number(retention),
          password,
          telegram: { enabled: tgEnabled, bot_token: tgToken, chat_id: tgChat },
          s3: {
            enabled: s3Enabled,
            endpoint: s3Endpoint,
            region: s3Region,
            bucket: s3Bucket,
            prefix: s3Prefix,
            access_key: s3Access,
            secret_key: s3Secret,
            path_style: s3Path,
          },
        }),
      }),
    onSuccess: (saved) => {
      push('success', 'Backup schedule saved')
      setPassword('')
      setTgToken('')
      setS3Secret('')
      queryClient.setQueryData(['backup-schedule'], saved)
    },
    onError: (err) => push('error', apiErrorMessage(err, t)),
  })

  const run = useMutation({
    mutationFn: () => apiFetch('/api/v1/backup-schedule/run', { method: 'POST' }),
    onSuccess: () => {
      push('success', 'Backup started')
      queryClient.invalidateQueries({ queryKey: ['backup-schedule'] })
    },
    onError: (err) => push('error', apiErrorMessage(err, t)),
  })

  function submit(e: FormEvent) {
    e.preventDefault()
    save.mutate()
  }

  return (
    <Card className="overflow-hidden">
      <div className="flex items-center gap-3 border-b border-edge px-6 py-4">
        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-accent-soft text-accent-fg">
          <CloudUpload className="h-4 w-4" />
        </div>
        <div className="min-w-0 flex-1">
          <h2 className="text-sm font-semibold tracking-tight text-fg">{t('Scheduled backups')}</h2>
          <p className="mt-0.5 text-xs leading-relaxed text-muted">
            {t('Send an encrypted backup to Telegram or an S3 bucket on a schedule, so a lost server does not take your only copy with it.')}
          </p>
        </div>
        <Button size="sm" variant="secondary" disabled={run.isPending || data?.running} onClick={() => run.mutate()}>
          <Play className="h-3.5 w-3.5" />
          {data?.running ? 'Backup running…' : 'Run backup now'}
        </Button>
      </div>

      {query.isLoading && <Skeleton className="m-6 h-40" />}
      {query.isError && <ErrorState title="Could not load the backup schedule." onRetry={() => query.refetch()} />}
      {data && (
        <form onSubmit={submit} className="space-y-5 p-6">
          <label className="flex cursor-pointer items-center gap-2 text-sm font-medium text-fg">
            <input type="checkbox" className="h-4 w-4 accent-accent" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
            {t('Run backups automatically')}
          </label>

          <div className="grid gap-4 sm:grid-cols-3">
            <Field label="Every (hours)">
              <Input className="ltr" type="number" min={1} max={8760} required value={interval} onChange={(e) => setIntervalHours(e.target.value)} list="backup-intervals" />
              <datalist id="backup-intervals">
                {INTERVALS.map((h) => (
                  <option key={h} value={h} />
                ))}
              </datalist>
            </Field>
            <Field label="Backups to keep" hint="Older backups in S3 are deleted.">
              <Input className="ltr" type="number" min={1} max={365} required value={retention} onChange={(e) => setRetention(e.target.value)} />
            </Field>
            <Field
              label="Backup password"
              hint={data.has_password ? 'A password is saved. Leave empty to keep it.' : 'Encrypts every backup. Without it a backup cannot be opened, and it cannot be recovered.'}
            >
              <Input className="ltr" type="password" autoComplete="new-password" minLength={8} value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
          </div>

          <div className="rounded-card border border-edge p-4">
            <label className="flex cursor-pointer items-center gap-2 text-sm font-medium text-fg">
              <input type="checkbox" className="h-4 w-4 accent-accent" checked={tgEnabled} onChange={(e) => setTgEnabled(e.target.checked)} />
              {t('Telegram')}
            </label>
            <p className="mt-1 text-xs text-muted">{t('Sent as a document. Telegram bots accept files up to 50 MB; larger backups fail, so use S3 for those.')}</p>
            {tgEnabled && (
              <div className="mt-3 grid gap-4 sm:grid-cols-2">
                <Field label="Bot token" hint={data.telegram.has_token ? 'A token is saved. Leave empty to keep it.' : undefined}>
                  <Input className="ltr" type="password" autoComplete="off" value={tgToken} onChange={(e) => setTgToken(e.target.value)} placeholder="123456:ABC…" />
                </Field>
                <Field label="Chat ID">
                  <Input className="ltr" value={tgChat} onChange={(e) => setTgChat(e.target.value)} placeholder="-1001234567890" />
                </Field>
              </div>
            )}
          </div>

          <div className="rounded-card border border-edge p-4">
            <label className="flex cursor-pointer items-center gap-2 text-sm font-medium text-fg">
              <input type="checkbox" className="h-4 w-4 accent-accent" checked={s3Enabled} onChange={(e) => setS3Enabled(e.target.checked)} />
              {t('S3-compatible storage')}
            </label>
            <p className="mt-1 text-xs text-muted">{t('Works with AWS S3, MinIO and ArvanCloud. Leave the endpoint empty for AWS.')}</p>
            {s3Enabled && (
              <div className="mt-3 space-y-4">
                <div className="grid gap-4 sm:grid-cols-2">
                  <Field label="Endpoint" labelSuffix="(optional)">
                    <Input className="ltr" value={s3Endpoint} onChange={(e) => setS3Endpoint(e.target.value)} placeholder="https://s3.ir-thr-at1.arvanstorage.ir" />
                  </Field>
                  <Field label="Region">
                    <Input className="ltr" value={s3Region} onChange={(e) => setS3Region(e.target.value)} placeholder="us-east-1" />
                  </Field>
                  <Field label="Bucket">
                    <Input className="ltr" value={s3Bucket} onChange={(e) => setS3Bucket(e.target.value)} />
                  </Field>
                  <Field label="Folder prefix" labelSuffix="(optional)">
                    <Input className="ltr" value={s3Prefix} onChange={(e) => setS3Prefix(e.target.value)} placeholder="jirjirak/" />
                  </Field>
                  <Field label="Access key">
                    <Input className="ltr" autoComplete="off" value={s3Access} onChange={(e) => setS3Access(e.target.value)} />
                  </Field>
                  <Field label="Secret key" hint={data.s3.has_secret ? 'A secret key is saved. Leave empty to keep it.' : undefined}>
                    <Input className="ltr" type="password" autoComplete="off" value={s3Secret} onChange={(e) => setS3Secret(e.target.value)} />
                  </Field>
                </div>
                <label className="flex cursor-pointer items-center gap-2 text-sm text-fg">
                  <input type="checkbox" className="h-4 w-4 accent-accent" checked={s3Path} onChange={(e) => setS3Path(e.target.checked)} />
                  {t('Use path-style addressing (needed for MinIO and ArvanCloud)')}
                </label>
              </div>
            )}
          </div>

          <div className="grid gap-3 sm:grid-cols-2">
            {(['telegram', 's3'] as const).map((d) => {
              const r = data.last[d]
              return (
                <div key={d} className="rounded-card border border-edge bg-inset/40 px-4 py-3">
                  <div className="flex items-center justify-between gap-2">
                    <p className="text-sm font-medium text-fg">{d === 'telegram' ? 'Telegram' : 'S3'}</p>
                    {r ? <Badge tone={r.status === 'success' ? 'green' : 'red'} dot>{r.status === 'success' ? 'Succeeded' : 'Failed'}</Badge> : <Badge>Never run</Badge>}
                  </div>
                  {r && (
                    <p className="mt-1 text-xs text-muted">
                      {formatDate(r.created_at)}
                      {r.status === 'success' ? ` · ${formatSize(r.size_bytes)}` : ''}
                    </p>
                  )}
                  {r?.status === 'failed' && r.error && <p className="ltr mt-1 text-xs break-words text-danger-fg">{r.error}</p>}
                </div>
              )
            })}
          </div>

          <div className="flex items-center justify-between gap-3 border-t border-edge pt-4">
            <p className="text-xs text-muted">
              {data.last_run_at ? t('Last run: {when}', { when: formatDate(data.last_run_at) }) : t('No backup has run yet.')}
            </p>
            <Button type="submit" disabled={save.isPending}>
              <Save className="h-4 w-4" />
              {save.isPending ? 'Saving…' : 'Save schedule'}
            </Button>
          </div>
        </form>
      )}
    </Card>
  )
}
