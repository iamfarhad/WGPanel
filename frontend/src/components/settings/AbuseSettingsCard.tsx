import { useEffect, useState, type FormEvent } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Save, ShieldAlert } from 'lucide-react'
import { apiFetch } from '../../lib/api'
import { apiErrorMessage } from '../../lib/apiErrors'
import { useAbuseSettings, type AbuseSettings } from '../../lib/abuse'
import { useAuth } from '../../lib/auth'
import { useToast } from '../../lib/toast'
import { useI18n } from '../../lib/i18n'
import { Card } from '../ui/Card'
import { Button } from '../ui/Button'
import { Input } from '../ui/Input'
import { Select } from '../ui/Select'
import { Field } from '../ui/Field'
import { Badge } from '../ui/Badge'
import { ErrorState } from '../ui/ErrorState'
import { Skeleton } from '../ui/Skeleton'

type NodeChoice = 'inherit' | 'on' | 'off'

function choiceOf(v: boolean | null): NodeChoice {
  return v === null ? 'inherit' : v ? 'on' : 'off'
}

function Check({ checked, onChange, children }: { checked: boolean; onChange: (v: boolean) => void; children: string }) {
  const { t } = useI18n()
  return (
    <label className="flex cursor-pointer items-center gap-2 text-sm font-medium text-fg">
      <input type="checkbox" className="h-4 w-4 accent-accent" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      {t(children)}
    </label>
  )
}

/** Settings -> abuse controls: auto-cleanup of long-dead accounts, how the device limit
 * counts, the shared-account heuristic and torrent blocking (global + per-node). Shown
 * to staff; only super_admin can save. */
export function AbuseSettingsCard() {
  const { t } = useI18n()
  const { user } = useAuth()
  const canEdit = user?.role === 'super_admin'
  const { push } = useToast()
  const queryClient = useQueryClient()
  const query = useAbuseSettings()
  const data = query.data

  const [cleanup, setCleanup] = useState(false)
  const [cleanupDays, setCleanupDays] = useState('30')
  const [mode, setMode] = useState<'endpoint' | 'ip'>('endpoint')
  const [devWindow, setDevWindow] = useState('5')
  const [sharing, setSharing] = useState(false)
  const [minNets, setMinNets] = useState('3')
  const [shareWindow, setShareWindow] = useState('10')
  const [torrent, setTorrent] = useState(false)
  const [suspend, setSuspend] = useState(false)
  const [hits, setHits] = useState('50')
  const [hitWindow, setHitWindow] = useState('60')
  const [nodeChoice, setNodeChoice] = useState<Record<string, NodeChoice>>({})
  const [loaded, setLoaded] = useState<AbuseSettings | null>(null)

  useEffect(() => {
    if (!data || data === loaded) return
    setLoaded(data)
    setCleanup(data.auto_cleanup_enabled)
    setCleanupDays(String(data.auto_cleanup_days))
    setMode(data.device_limit_mode)
    setDevWindow(String(data.device_window_minutes))
    setSharing(data.sharing_detection_enabled)
    setMinNets(String(data.sharing_min_networks))
    setShareWindow(String(data.sharing_window_minutes))
    setTorrent(data.torrent_block_enabled)
    setSuspend(data.torrent_suspend_enabled)
    setHits(String(data.torrent_suspend_hits))
    setHitWindow(String(data.torrent_suspend_window_minutes))
    setNodeChoice(Object.fromEntries(data.nodes.map((n) => [n.id, choiceOf(n.torrent_block)])))
  }, [data, loaded])

  const save = useMutation({
    mutationFn: () => {
      // Only node overrides that changed are sent (null = back to inherit).
      const overrides: Record<string, boolean | null> = {}
      for (const n of data?.nodes ?? []) {
        const now = nodeChoice[n.id] ?? 'inherit'
        if (now !== choiceOf(n.torrent_block)) overrides[n.id] = now === 'inherit' ? null : now === 'on'
      }
      return apiFetch<AbuseSettings>('/api/v1/abuse-settings', {
        method: 'PATCH',
        body: JSON.stringify({
          auto_cleanup_enabled: cleanup,
          auto_cleanup_days: Number(cleanupDays),
          device_limit_mode: mode,
          device_window_minutes: Number(devWindow),
          sharing_detection_enabled: sharing,
          sharing_min_networks: Number(minNets),
          sharing_window_minutes: Number(shareWindow),
          torrent_block_enabled: torrent,
          torrent_suspend_enabled: suspend,
          torrent_suspend_hits: Number(hits),
          torrent_suspend_window_minutes: Number(hitWindow),
          node_overrides: overrides,
        }),
      })
    },
    onSuccess: (saved) => {
      push('success', 'Abuse settings saved')
      setLoaded(null)
      queryClient.setQueryData(['abuse-settings'], saved)
    },
    onError: (err) => push('error', apiErrorMessage(err, t)),
  })

  function submit(e: FormEvent) {
    e.preventDefault()
    save.mutate()
  }
  const ro = !canEdit

  return (
    <Card className="overflow-hidden">
      <div className="flex items-center gap-3 border-b border-edge px-6 py-4">
        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-accent-soft text-accent-fg">
          <ShieldAlert className="h-4 w-4" />
        </div>
        <div className="min-w-0 flex-1">
          <h2 className="text-sm font-semibold tracking-tight text-fg">{t('Abuse controls')}</h2>
          <p className="mt-0.5 text-xs leading-relaxed text-muted">
            {t('Clean up dead accounts, catch shared accounts and block torrents. Everything is off until you turn it on.')}
          </p>
        </div>
      </div>

      {query.isLoading && <Skeleton className="m-6 h-40" />}
      {query.isError && <ErrorState title="Could not load the abuse settings." onRetry={() => query.refetch()} />}
      {data && (
        <form onSubmit={submit} className="space-y-5 p-6">
          <fieldset disabled={ro} className="space-y-5">
            <div className="rounded-card border border-edge p-4">
              <Check checked={cleanup} onChange={setCleanup}>Delete long-dead accounts automatically</Check>
              <p className="mt-1 text-xs leading-relaxed text-muted">
                {t(
                  'Deletes accounts that have been expired or out of data, and suspended because of it, for the number of days below. Accounts you suspended yourself, and accounts suspended for abuse, a device limit or torrents, are never deleted. Each deletion is written to the audit log.',
                )}
              </p>
              {cleanup && (
                <div className="mt-3 max-w-xs">
                  <Field label="After (days)">
                    <Input className="ltr" type="number" min={1} max={3650} required value={cleanupDays} onChange={(e) => setCleanupDays(e.target.value)} />
                  </Field>
                </div>
              )}
            </div>

            <div className="rounded-card border border-edge p-4">
              <p className="text-sm font-medium text-fg">{t('Device limit counts')}</p>
              <p className="mt-1 text-xs leading-relaxed text-muted">
                {t(
                  'Distinct endpoints (IP and port) is the strictest and can count one phone twice when the carrier changes its port. Distinct IPs ignores ports, so several devices behind one home router count once; carrier-grade NAT can still make strangers share an IP, and a roaming phone can look like two.',
                )}
              </p>
              <div className="mt-3 grid gap-4 sm:grid-cols-2">
                <Field label="Count">
                  <Select value={mode} onChange={(e) => setMode(e.target.value as 'endpoint' | 'ip')}>
                    <option value="endpoint">{t('Distinct endpoints (IP:port)')}</option>
                    <option value="ip">{t('Distinct IPs')}</option>
                  </Select>
                </Field>
                <Field label="Within the last (minutes)">
                  <Input className="ltr" type="number" min={1} max={1440} required value={devWindow} onChange={(e) => setDevWindow(e.target.value)} />
                </Field>
              </div>
            </div>

            <div className="rounded-card border border-edge p-4">
              <Check checked={sharing} onChange={setSharing}>Detect shared accounts</Check>
              <p className="mt-1 text-xs leading-relaxed text-muted">
                {t(
                  'Flags an account that is active from several different networks (IPv4 /24 or IPv6 /48) at the same time, shows a "possibly shared" badge and sends the account.sharing_suspected event to your channels and webhooks. It is a hint, never an automatic suspension.',
                )}
              </p>
              {sharing && (
                <div className="mt-3 grid gap-4 sm:grid-cols-2">
                  <Field label="Networks that trigger it">
                    <Input className="ltr" type="number" min={2} max={50} required value={minNets} onChange={(e) => setMinNets(e.target.value)} />
                  </Field>
                  <Field label="Within the last (minutes)">
                    <Input className="ltr" type="number" min={1} max={1440} required value={shareWindow} onChange={(e) => setShareWindow(e.target.value)} />
                  </Field>
                </div>
              )}
            </div>

            <div className="rounded-card border border-edge p-4">
              <Check checked={torrent} onChange={setTorrent}>Block torrents on nodes</Check>
              <p className="mt-1 text-xs leading-relaxed text-muted">
                {t(
                  'Nodes drop packets that carry plain BitTorrent markers (peer handshake, DHT, tracker requests). Encrypted or obfuscated BitTorrent is not detected - most clients can use it, and many fall back to it when the plain handshake is dropped - so this stops casual use and gives you a signal, not a guarantee. It inspects the start of every forwarded packet, which costs some CPU on busy nodes; needs the xt_string kernel module on the node.',
                )}
              </p>
              <div className="mt-3">
                <Check checked={suspend} onChange={setSuspend}>Suspend accounts that keep trying</Check>
              </div>
              {suspend && (
                <div className="mt-3 grid gap-4 sm:grid-cols-2">
                  <Field label="Blocked packets" hint="Counts packets, not attempts: one blocked connection is retried several times.">
                    <Input className="ltr" type="number" min={1} required value={hits} onChange={(e) => setHits(e.target.value)} />
                  </Field>
                  <Field label="Within the last (minutes)">
                    <Input className="ltr" type="number" min={1} max={10080} required value={hitWindow} onChange={(e) => setHitWindow(e.target.value)} />
                  </Field>
                </div>
              )}
              {data.nodes.length > 0 && (
                <div className="mt-4">
                  <p className="mb-2 text-xs font-semibold tracking-wider text-faint uppercase">{t('Per node')}</p>
                  <div className="space-y-1.5">
                    {data.nodes.map((n) => {
                      const choice = nodeChoice[n.id] ?? 'inherit'
                      const effective = choice === 'inherit' ? torrent : choice === 'on'
                      return (
                        <div key={n.id} className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-edge px-3 py-2 text-sm">
                          <span className="flex items-center gap-2 text-fg">
                            {n.name}
                            <Badge tone={effective ? 'green' : 'slate'}>{effective ? 'Blocking' : 'Not blocking'}</Badge>
                          </span>
                          <Select
                            className="w-full max-w-48"
                            value={choice}
                            aria-label={t('Torrent blocking on {name}', { name: n.name })}
                            onChange={(e) => setNodeChoice({ ...nodeChoice, [n.id]: e.target.value as NodeChoice })}
                          >
                            <option value="inherit">{t('Use the default above')}</option>
                            <option value="on">{t('Always block')}</option>
                            <option value="off">{t('Never block')}</option>
                          </Select>
                        </div>
                      )
                    })}
                  </div>
                </div>
              )}
            </div>
          </fieldset>

          {canEdit ? (
            <div className="flex justify-end border-t border-edge pt-4">
              <Button type="submit" disabled={save.isPending}>
                <Save className="h-4 w-4" />
                {save.isPending ? 'Saving…' : 'Save settings'}
              </Button>
            </div>
          ) : (
            <p className="text-xs text-muted">{t('Only a super admin can change these settings.')}</p>
          )}
        </form>
      )}
    </Card>
  )
}
