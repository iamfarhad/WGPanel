import { useEffect, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Save, ShieldCheck } from 'lucide-react'
import { apiFetch, ApiError } from '../lib/api'
import { useToast } from '../lib/toast'
import { PageHeader } from '../components/ui/PageHeader'
import { Card } from '../components/ui/Card'
import { Button } from '../components/ui/Button'
import { Input } from '../components/ui/Input'
import { Skeleton } from '../components/ui/Skeleton'

interface Settings {
  public_base_url: string | null
  default_data_quota_gb: number | null
  default_device_limit: number | null
  default_node_capacity: number
  support_contact: string | null
  panel_domain: string | null
}

interface UpdateSettingsResult extends Settings {
  domain_live_applied: boolean
  domain_apply_error: string | null
}

export function SettingsPage() {
  const queryClient = useQueryClient()
  const { push } = useToast()

  const settingsQuery = useQuery({
    queryKey: ['settings'],
    queryFn: () => apiFetch<Settings>('/api/v1/settings'),
  })

  const [publicBaseUrl, setPublicBaseUrl] = useState('')
  const [defaultQuotaGb, setDefaultQuotaGb] = useState('')
  const [defaultDeviceLimit, setDefaultDeviceLimit] = useState('')
  const [defaultNodeCapacity, setDefaultNodeCapacity] = useState('250')
  const [supportContact, setSupportContact] = useState('')
  const [panelDomain, setPanelDomain] = useState('')
  const [domainApplyError, setDomainApplyError] = useState<string | null>(null)
  const [domainLiveApplied, setDomainLiveApplied] = useState(false)

  useEffect(() => {
    if (!settingsQuery.data) return
    setPublicBaseUrl(settingsQuery.data.public_base_url ?? '')
    setDefaultQuotaGb(settingsQuery.data.default_data_quota_gb?.toString() ?? '')
    setDefaultDeviceLimit(settingsQuery.data.default_device_limit?.toString() ?? '')
    setDefaultNodeCapacity(settingsQuery.data.default_node_capacity.toString())
    setSupportContact(settingsQuery.data.support_contact ?? '')
    setPanelDomain(settingsQuery.data.panel_domain ?? '')
  }, [settingsQuery.data])

  const saveMutation = useMutation({
    mutationFn: () =>
      apiFetch<Settings>('/api/v1/settings', {
        method: 'PATCH',
        body: JSON.stringify({
          public_base_url: publicBaseUrl || null,
          default_data_quota_gb: defaultQuotaGb ? Number(defaultQuotaGb) : null,
          default_device_limit: defaultDeviceLimit ? Number(defaultDeviceLimit) : null,
          default_node_capacity: Number(defaultNodeCapacity),
          support_contact: supportContact || null,
        }),
      }),
    onSuccess: (settings) => {
      push('success', 'Settings saved')
      queryClient.setQueryData(['settings'], settings)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to save settings'),
  })

  const domainMutation = useMutation({
    mutationFn: () =>
      apiFetch<UpdateSettingsResult>('/api/v1/settings', {
        method: 'PATCH',
        body: JSON.stringify({ panel_domain: panelDomain }),
      }),
    onSuccess: (settings) => {
      setDomainLiveApplied(settings.domain_live_applied)
      setDomainApplyError(settings.domain_apply_error)
      if (settings.domain_live_applied) {
        push('success', `Domain updated - Caddy is provisioning a certificate for ${panelDomain}`)
      } else {
        push('error', settings.domain_apply_error ?? 'Domain saved, but could not push it to Caddy live')
      }
      queryClient.setQueryData(['settings'], settings)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to update domain'),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    saveMutation.mutate()
  }

  function handleDomainSubmit(e: FormEvent) {
    e.preventDefault()
    setDomainApplyError(null)
    domainMutation.mutate()
  }

  return (
    <div>
      <PageHeader title="Settings" description="Panel-wide configuration and defaults." />

      <Card className="mb-6 max-w-2xl p-6">
        {settingsQuery.isLoading ? (
          <Skeleton className="h-10" />
        ) : (
          <form onSubmit={handleDomainSubmit} className="space-y-4">
            <div className="flex items-center gap-2">
              <ShieldCheck className="h-4 w-4 text-slate-500 dark:text-slate-400" />
              <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">Domain &amp; TLS</h2>
            </div>
            <p className="text-xs text-slate-500 dark:text-slate-400">
              The domain Caddy serves the panel on and automatically provisions a Let's Encrypt certificate for.
              Unlike public base URL above, changing this takes effect live via Caddy's admin API - no restart or
              redeploy needed. Requires DNS for the domain to already point at this server.
            </p>
            <div>
              <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">Domain</label>
              <Input value={panelDomain} onChange={(e) => setPanelDomain(e.target.value)} placeholder="panel.example.com" required />
            </div>
            {domainLiveApplied && !domainApplyError && (
              <p className="text-sm text-emerald-600 dark:text-emerald-400">Applied live - Caddy is now serving this domain.</p>
            )}
            {domainApplyError && (
              <p className="text-sm text-amber-600 dark:text-amber-400">
                Domain saved, but the live push to Caddy failed: {domainApplyError}. It will take effect after Caddy
                is next restarted with a matching PANEL_DOMAIN.
              </p>
            )}
            <div className="flex justify-end border-t border-slate-100 pt-4 dark:border-slate-800">
              <Button type="submit" disabled={domainMutation.isPending || !panelDomain}>
                <ShieldCheck className="mr-2 h-4 w-4" />
                {domainMutation.isPending ? 'Applying…' : 'Apply domain'}
              </Button>
            </div>
          </form>
        )}
      </Card>

      <Card className="max-w-2xl p-6">
        {settingsQuery.isLoading ? (
          <div className="space-y-4">
            {Array.from({ length: 5 }).map((_, i) => (
              <Skeleton key={i} className="h-10" />
            ))}
          </div>
        ) : settingsQuery.isError ? (
          <p className="text-sm text-red-600 dark:text-red-400">Could not load settings.</p>
        ) : (
          <form onSubmit={handleSubmit} className="space-y-6">
            <div>
              <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">
                Public base URL
              </label>
              <p className="mb-2 text-xs text-slate-500 dark:text-slate-400">
                The domain this panel is served on (e.g. https://panel.example.com). Informational -
                changing it here does not move DNS/TLS, which is still configured in deploy/.env's
                PANEL_DOMAIN and requires a redeploy to actually change.
              </p>
              <Input
                value={publicBaseUrl}
                onChange={(e) => setPublicBaseUrl(e.target.value)}
                placeholder="https://panel.example.com"
              />
            </div>

            <div className="grid grid-cols-2 gap-4">
              <div>
                <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">
                  Default account data quota (GB)
                </label>
                <Input
                  type="number"
                  min="0"
                  step="0.1"
                  value={defaultQuotaGb}
                  onChange={(e) => setDefaultQuotaGb(e.target.value)}
                  placeholder="Unlimited"
                />
              </div>
              <div>
                <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">
                  Default account device limit
                </label>
                <Input
                  type="number"
                  min="1"
                  value={defaultDeviceLimit}
                  onChange={(e) => setDefaultDeviceLimit(e.target.value)}
                  placeholder="Unlimited"
                />
              </div>
            </div>

            <div>
              <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">
                Default node capacity (max peers)
              </label>
              <Input
                type="number"
                min="1"
                value={defaultNodeCapacity}
                onChange={(e) => setDefaultNodeCapacity(e.target.value)}
                required
              />
            </div>

            <div>
              <label className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">
                Support contact
              </label>
              <p className="mb-2 text-xs text-slate-500 dark:text-slate-400">
                Shown to other admins who need help (e.g. an email address or Telegram handle).
              </p>
              <Input
                value={supportContact}
                onChange={(e) => setSupportContact(e.target.value)}
                placeholder="ops@example.com"
              />
            </div>

            <div className="flex justify-end border-t border-slate-100 pt-4 dark:border-slate-800">
              <Button type="submit" disabled={saveMutation.isPending}>
                <Save className="mr-2 h-4 w-4" />
                {saveMutation.isPending ? 'Saving…' : 'Save settings'}
              </Button>
            </div>
          </form>
        )}
      </Card>
    </div>
  )
}
