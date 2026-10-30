import { useEffect, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '../lib/api'
import { apiErrorMessage } from '../lib/apiErrors'
import { useAuth } from '../lib/auth'
import { useI18n } from '../lib/i18n'
import { useToast } from '../lib/toast'
import type { NodeIPv6 } from '../lib/routing'
import { Dialog } from './ui/Dialog'
import { Button } from './ui/Button'
import { Badge } from './ui/Badge'
import { Field } from './ui/Field'
import { Input } from './ui/Input'

function useNodeIPv6() {
  return useQuery({
    queryKey: ['node-ipv6'],
    queryFn: () => apiFetch<{ nodes: NodeIPv6[] | null }>('/api/v1/node-ipv6'),
  })
}

/** Opt-in IPv6 egress for one node. Enabling only asks the node's agent to prepare itself;
 * client configs get an IPv6 address once the agent reports it working. */
export function NodeIPv6Dialog({ node, onClose }: { node: { id: string; name: string }; onClose: () => void }) {
  const { t } = useI18n()
  const { push } = useToast()
  const { user } = useAuth()
  const queryClient = useQueryClient()
  const readOnly = user?.role === 'support'
  const query = useNodeIPv6()
  const state = (query.data?.nodes ?? []).find((n) => n.node_id === node.id)

  const [enabled, setEnabled] = useState(false)
  const [prefix, setPrefix] = useState('')
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!state) return
    setEnabled(state.ipv6_enabled)
    setPrefix(state.ipv6_ula_prefix ?? '')
  }, [state])

  const saveMutation = useMutation({
    mutationFn: () =>
      apiFetch<NodeIPv6>(`/api/v1/node-ipv6/${node.id}`, {
        method: 'PUT',
        body: JSON.stringify({ enabled, ula_prefix: prefix.trim() || undefined }),
      }),
    onSuccess: () => {
      push('success', t('IPv6 settings saved for {name}', { name: node.name }))
      queryClient.invalidateQueries({ queryKey: ['node-ipv6'] })
      onClose()
    },
    onError: (err) => setError(apiErrorMessage(err, t, 'Failed to save IPv6 settings')),
  })

  function submit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    saveMutation.mutate()
  }

  // The prefix is locked while the node is enabled: clients hold addresses derived from it.
  const prefixLocked = !!state?.ipv6_enabled

  return (
    <Dialog open onClose={onClose} title={t('IPv6 egress - {name}', { name: node.name })}>
      <form onSubmit={submit} className="space-y-4">
        <p className="text-sm leading-relaxed text-muted">
          {t(
            'Lets clients of this node reach IPv6 sites. Each client gets a private address from the node\'s /64, translated (NAT66) to the node\'s own global IPv6 address. Off by default; ::/0 stays in client routes either way, so nothing leaks while it is off.',
          )}
        </p>
        {state && (
          <div className="flex flex-wrap items-center gap-2 text-xs">
            <Badge tone={state.ipv6_host_global ? 'green' : 'amber'}>
              {state.ipv6_host_global ? t('Node has global IPv6') : t('No global IPv6 on the node')}
            </Badge>
            <Badge tone={state.ipv6_active ? 'green' : 'slate'}>{state.ipv6_active ? t('Active in client configs') : t('Not active')}</Badge>
            <Badge tone={state.psk_supported ? 'green' : 'slate'}>
              {state.psk_supported ? t('Agent supports PresharedKey') : t('Agent without PresharedKey support')}
            </Badge>
          </div>
        )}
        <label className="flex cursor-pointer items-start gap-2.5 rounded-lg border border-edge p-3 text-sm text-fg transition-colors hover:bg-inset/50">
          <input type="checkbox" className="mt-0.5" checked={enabled} disabled={readOnly || !state} onChange={(e) => setEnabled(e.target.checked)} />
          <span>
            <span className="font-medium">{t('Enable IPv6 egress on this node')}</span>
            <span className="mt-0.5 block text-xs leading-relaxed text-muted">
              {t('Needs a global IPv6 route on the node. For the container install that means an IPv6-enabled Docker network and the IPv6 sysctls; the agent reports what is missing.')}
            </span>
          </span>
        </label>
        <Field
          label="Node IPv6 prefix"
          labelSuffix="(optional)"
          hint={prefixLocked ? 'Disable IPv6 on the node to change the prefix: clients hold addresses derived from it.' : 'A unique-local /64, e.g. fd12:3456:789a:1::/64. Leave empty to generate one.'}
        >
          <Input className="ltr font-mono" value={prefix} disabled={readOnly || prefixLocked} onChange={(e) => setPrefix(e.target.value)} placeholder="fd12:3456:789a:1::/64" />
        </Field>
        {error && <p className="text-sm text-danger-fg">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          {!readOnly && (
            <Button type="submit" disabled={saveMutation.isPending || !state}>
              {saveMutation.isPending ? t('Saving…') : t('Save')}
            </Button>
          )}
        </div>
      </form>
    </Dialog>
  )
}
