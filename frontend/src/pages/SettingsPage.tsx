import { useEffect, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Save, ShieldCheck, SlidersHorizontal } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { apiFetch, ApiError } from '../lib/api'
import { useToast } from '../lib/toast'
import { PageHeader } from '../components/ui/PageHeader'
import { Card } from '../components/ui/Card'
import { Button } from '../components/ui/Button'
import { Input } from '../components/ui/Input'
import { Field } from '../components/ui/Field'
import { Skeleton } from '../components/ui/Skeleton'

interface Settings {
  public_base_url: string | null
  default_data_quota_gb: number | null
  default_device_limit: number | null
  default_node_capacity: number
  support_contact: string | null
  panel_domain: string | null
  client_dns: string
  sub_domain: string | null
  sub_port: number | null
}

interface UpdateSettingsResult extends Settings {
  domain_live_applied: boolean
  domain_apply_error: string | null
}

function SectionHeader({ icon: Icon, title, description }: { icon: LucideIcon; title: string; description: string }) {
  return (
    <div className="flex items-start gap-3 border-b border-edge px-6 py-4">
      <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-accent-soft text-accent-fg">
        <Icon className="h-4 w-4" />
      </div>
      <div>
        <h2 className="text-sm font-semibold tracking-tight text-fg">{title}</h2>
        <p className="mt-0.5 text-xs leading-relaxed text-muted">{description}</p>
      </div>
    </div>
  )
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
  const [clientDns, setClientDns] = useState('1.1.1.1, 1.0.0.1')
  const [panelDomain, setPanelDomain] = useState('')
  const [subDomain, setSubDomain] = useState('')
  const [subPort, setSubPort] = useState('')
  const [domainApplyError, setDomainApplyError] = useState<string | null>(null)
  const [domainLiveApplied, setDomainLiveApplied] = useState(false)

  useEffect(() => {
    if (!settingsQuery.data) return
    setPublicBaseUrl(settingsQuery.data.public_base_url ?? '')
    setDefaultQuotaGb(settingsQuery.data.default_data_quota_gb?.toString() ?? '')
    setDefaultDeviceLimit(settingsQuery.data.default_device_limit?.toString() ?? '')
    setDefaultNodeCapacity(settingsQuery.data.default_node_capacity.toString())
    setSupportContact(settingsQuery.data.support_contact ?? '')
    setClientDns(settingsQuery.data.client_dns)
    setPanelDomain(settingsQuery.data.panel_domain ?? '')
    setSubDomain(settingsQuery.data.sub_domain ?? '')
    setSubPort(settingsQuery.data.sub_port?.toString() ?? '')
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
          client_dns: clientDns || null,
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
        body: JSON.stringify({
          panel_domain: panelDomain,
          // Empty field = feature off / default port: the API treats "" and 0 as an
          // explicit reset to NULL (unlike omitting the key, which means unchanged).
          sub_domain: subDomain,
          sub_port: subPort ? Number(subPort) : 0,
        }),
      }),
    onSuccess: (settings) => {
      setDomainLiveApplied(settings.domain_live_applied)
      setDomainApplyError(settings.domain_apply_error)
      if (settings.domain_live_applied) {
        const domains = settings.sub_domain ? `${panelDomain} and ${settings.sub_domain}` : panelDomain
        push('success', `Domains updated - Caddy is provisioning certificates for ${domains}`)
      } else {
        push('error', settings.domain_apply_error ?? 'Domains saved, but could not push them to Caddy live')
      }
      queryClient.setQueryData(['settings'], settings)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to update domains'),
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

      <div className="max-w-2xl space-y-6">
        <Card className="overflow-hidden">
          <SectionHeader
            icon={ShieldCheck}
            title="Domain & TLS"
            description="The domains Caddy serves and automatically provisions Let's Encrypt certificates for. Changes take effect live via Caddy's admin API - no restart or redeploy needed. Requires DNS for each domain to already point at this server."
          />
          {settingsQuery.isLoading ? (
            <div className="p-6">
              <Skeleton className="h-10" />
            </div>
          ) : (
            <form onSubmit={handleDomainSubmit} className="space-y-4 p-6">
              <Field label="Panel domain">
                <Input value={panelDomain} onChange={(e) => setPanelDomain(e.target.value)} placeholder="panel.example.com" required />
              </Field>
              <div className="grid grid-cols-[1fr_8rem] gap-4">
                <Field
                  label="Subscription domain"
                  hint="Optional separate domain for the subscription links handed to end users. It serves only the subscription endpoints - the admin panel is never reachable through it - with its own automatic certificate. Leave empty to keep subscription links on the panel domain."
                >
                  <Input value={subDomain} onChange={(e) => setSubDomain(e.target.value)} placeholder="sub.example.com" />
                </Field>
                <Field
                  label="Subscription port"
                  hint="Default 443 works with no extra setup. Any other port must match SUB_PORT in deploy/.env (default 8443) and be open in the firewall."
                >
                  <Input
                    type="number"
                    min="1"
                    max="65535"
                    value={subPort}
                    onChange={(e) => setSubPort(e.target.value)}
                    placeholder="443"
                  />
                </Field>
              </div>
              {domainLiveApplied && !domainApplyError && (
                <p className="text-sm text-emerald-600 dark:text-emerald-400">Applied live - Caddy is now serving with this configuration.</p>
              )}
              {domainApplyError && (
                <p className="rounded-lg border border-amber-500/25 bg-amber-500/10 px-3 py-2.5 text-sm leading-relaxed text-amber-700 dark:text-amber-400">
                  Saved, but the live push to Caddy failed: {domainApplyError}. The API retries it automatically on its
                  next restart.
                </p>
              )}
              <div className="flex justify-end border-t border-edge pt-4">
                <Button type="submit" disabled={domainMutation.isPending || !panelDomain}>
                  <ShieldCheck className="h-4 w-4" />
                  {domainMutation.isPending ? 'Applying…' : 'Apply domains'}
                </Button>
              </div>
            </form>
          )}
        </Card>

        <Card className="overflow-hidden">
          <SectionHeader
            icon={SlidersHorizontal}
            title="Defaults & panel info"
            description="Baseline values applied to new nodes and accounts, plus what other admins see about this panel."
          />
          {settingsQuery.isLoading ? (
            <div className="space-y-4 p-6">
              {Array.from({ length: 5 }).map((_, i) => (
                <Skeleton key={i} className="h-10" />
              ))}
            </div>
          ) : settingsQuery.isError ? (
            <p className="p-6 text-sm text-rose-600 dark:text-rose-400">Could not load settings.</p>
          ) : (
            <form onSubmit={handleSubmit} className="space-y-6 p-6">
              <Field
                label="Public base URL"
                hint="The domain this panel is served on (e.g. https://panel.example.com). Informational - changing it here does not move DNS/TLS, which is still configured in deploy/.env's PANEL_DOMAIN and requires a redeploy to actually change."
              >
                <Input
                  value={publicBaseUrl}
                  onChange={(e) => setPublicBaseUrl(e.target.value)}
                  placeholder="https://panel.example.com"
                />
              </Field>

              <div className="grid grid-cols-2 gap-4">
                <Field label="Default account data quota (GB)">
                  <Input
                    type="number"
                    min="0"
                    step="0.1"
                    value={defaultQuotaGb}
                    onChange={(e) => setDefaultQuotaGb(e.target.value)}
                    placeholder="Unlimited"
                  />
                </Field>
                <Field label="Default account device limit">
                  <Input
                    type="number"
                    min="1"
                    value={defaultDeviceLimit}
                    onChange={(e) => setDefaultDeviceLimit(e.target.value)}
                    placeholder="Unlimited"
                  />
                </Field>
              </div>

              <Field label="Default node capacity (max peers)">
                <Input
                  type="number"
                  min="1"
                  value={defaultNodeCapacity}
                  onChange={(e) => setDefaultNodeCapacity(e.target.value)}
                  required
                />
              </Field>

              <Field
                label="Client DNS server(s)"
                hint={
                  <>
                    Written into every generated WireGuard config's <code>DNS =</code> line (comma-separated). Since clients
                    full-tunnel, they can only resolve names through this server - if it's unreachable from where your nodes
                    egress, clients connect but have "no internet". The Cloudflare default is blocked on some networks; use a
                    resolver you've confirmed works from the node. Takes effect on the next config download.
                  </>
                }
              >
                <Input
                  value={clientDns}
                  onChange={(e) => setClientDns(e.target.value)}
                  placeholder="1.1.1.1, 1.0.0.1"
                  required
                />
              </Field>

              <Field
                label="Support contact"
                hint="Shown to other admins who need help (e.g. an email address or Telegram handle)."
              >
                <Input
                  value={supportContact}
                  onChange={(e) => setSupportContact(e.target.value)}
                  placeholder="ops@example.com"
                />
              </Field>

              <div className="flex justify-end border-t border-edge pt-4">
                <Button type="submit" disabled={saveMutation.isPending}>
                  <Save className="h-4 w-4" />
                  {saveMutation.isPending ? 'Saving…' : 'Save settings'}
                </Button>
              </div>
            </form>
          )}
        </Card>
      </div>
    </div>
  )
}
