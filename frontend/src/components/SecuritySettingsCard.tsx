import { useEffect, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Lock, Save } from 'lucide-react'
import { apiFetch } from '../lib/api'
import { apiErrorMessage } from '../lib/apiErrors'
import { useI18n } from '../lib/i18n'
import { useToast } from '../lib/toast'
import { Card } from './ui/Card'
import { Button } from './ui/Button'
import { Input } from './ui/Input'
import { Field } from './ui/Field'
import { ErrorState } from './ui/ErrorState'
import { Skeleton } from './ui/Skeleton'

interface SecuritySettings {
  admin_ip_allowlist: string[]
  login_max_failures: number
  login_lockout_minutes: number
  your_ip: string
}

/** Settings -> Security (super admin): login lockout thresholds and the admin IP allowlist. */
export function SecuritySettingsCard() {
  const { t } = useI18n()
  const { push } = useToast()
  const queryClient = useQueryClient()
  const query = useQuery({ queryKey: ['security-settings'], queryFn: () => apiFetch<SecuritySettings>('/api/v1/security/settings') })

  const [allowlist, setAllowlist] = useState('')
  const [maxFailures, setMaxFailures] = useState('5')
  const [lockoutMinutes, setLockoutMinutes] = useState('15')
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!query.data) return
    setAllowlist(query.data.admin_ip_allowlist.join('\n'))
    setMaxFailures(String(query.data.login_max_failures))
    setLockoutMinutes(String(query.data.login_lockout_minutes))
  }, [query.data])

  const save = useMutation({
    mutationFn: () =>
      apiFetch<SecuritySettings>('/api/v1/security/settings', {
        method: 'PATCH',
        body: JSON.stringify({
          admin_ip_allowlist: allowlist
            .split(/[\s,]+/)
            .map((s) => s.trim())
            .filter(Boolean),
          login_max_failures: Number(maxFailures),
          login_lockout_minutes: Number(lockoutMinutes),
        }),
      }),
    onSuccess: (d) => {
      setError(null)
      push('success', t('Security settings saved'))
      queryClient.setQueryData(['security-settings'], d)
    },
    onError: (err) => setError(apiErrorMessage(err, t, 'Failed to save security settings')),
  })

  function submit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    save.mutate()
  }

  return (
    <Card className="overflow-hidden">
      <div className="flex items-center gap-3 border-b border-edge px-6 py-4">
        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-accent-soft text-accent-fg">
          <Lock className="h-4 w-4" />
        </div>
        <div>
          <h2 className="text-sm font-semibold tracking-tight text-fg">{t('Security')}</h2>
          <p className="mt-0.5 text-xs leading-relaxed text-muted">
            {t('Sign-in lockout and which IP addresses may reach the admin panel.')}
          </p>
        </div>
      </div>
      {query.isLoading ? (
        <div className="space-y-4 p-6">
          <Skeleton className="h-10" />
          <Skeleton className="h-24" />
        </div>
      ) : query.isError ? (
        <ErrorState title="Could not load security settings." onRetry={() => query.refetch()} />
      ) : (
        <form onSubmit={submit} className="space-y-6 p-6">
          <div className="grid grid-cols-2 gap-4">
            <Field label="Failed sign-ins before lockout" hint="Counted per username, and per IP address at four times this number.">
              <Input type="number" min="1" max="100" value={maxFailures} onChange={(e) => setMaxFailures(e.target.value)} required />
            </Field>
            <Field label="Lockout (minutes)" hint="Doubles each time the same username or IP is locked again within 24 hours.">
              <Input type="number" min="1" max="1440" value={lockoutMinutes} onChange={(e) => setLockoutMinutes(e.target.value)} required />
            </Field>
          </div>

          <Field
            label="Admin IP allowlist"
            hint={
              <>
                {t('One IP address or CIDR range per line, e.g. 203.0.113.7 or 10.0.0.0/8. Leave empty to allow any address. It limits admin sign-in and the admin panel only - subscription links, node agents and API keys are not affected. Locked out? Run')}{' '}
                <code className="ltr inline-block">wgpanel clear-ip-allowlist</code> {t('on the server.')}
              </>
            }
          >
            <textarea
              value={allowlist}
              onChange={(e) => setAllowlist(e.target.value)}
              rows={4}
              spellCheck={false}
              placeholder={'203.0.113.7\n10.0.0.0/8'}
              className="ltr w-full rounded-control border border-edge bg-surface px-3 py-2 font-mono text-sm text-fg outline-none placeholder:text-faint focus-visible:border-accent focus-visible:ring-[3px] focus-visible:ring-ring"
            />
            <p className="mt-1.5 text-xs text-muted">
              {t('Your current IP address:')} <code className="ltr inline-block text-fg">{query.data?.your_ip}</code>
            </p>
          </Field>

          {error && <p className="text-sm text-danger-fg">{error}</p>}
          <div className="flex justify-end border-t border-edge pt-4">
            <Button type="submit" disabled={save.isPending}>
              <Save className="h-4 w-4" />
              {save.isPending ? 'Saving…' : 'Save security settings'}
            </Button>
          </div>
        </form>
      )}
    </Card>
  )
}
