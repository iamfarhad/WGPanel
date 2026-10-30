import { useQuery } from '@tanstack/react-query'
import { Copy, Bot } from 'lucide-react'
import { apiFetch } from '../../lib/api'
import { useToast } from '../../lib/toast'
import { useI18n } from '../../lib/i18n'
import { Card, CardHeader } from '../ui/Card'
import { Badge } from '../ui/Badge'

/** API Keys page -> "Connect a sales bot": what to type into a Marzban-capable bot
 * (Mirza panel, Faoxima, ShopVPN...) to use this panel. The panel URL is the origin the
 * admin is looking at - the same one Caddy proxies /api/* on. Username/password are the
 * API key's ID and secret, so the card points at the key list instead of showing either
 * (the secret is shown once, at creation). See docs/marzban-compat.md. */
export function ConnectSalesBotCard() {
  const { t, tx } = useI18n()
  const { push } = useToast()
  const status = useQuery({
    queryKey: ['marzban-compat'],
    queryFn: () => apiFetch<{ enabled: boolean }>('/api/v1/marzban-compat'),
  })
  const panelUrl = window.location.origin

  function copy(value: string) {
    navigator.clipboard.writeText(value)
    push('success', 'Copied to clipboard')
  }

  return (
    <Card className="mb-6 overflow-hidden">
      <CardHeader
        title="Connect a sales bot"
        description="Telegram bots that support Marzban (Mirza panel, Faoxima, ShopVPN) can sell from this panel."
        action={
          status.data && (
            <Badge dot tone={status.data.enabled ? 'green' : 'amber'}>
              {status.data.enabled ? 'Marzban API on' : 'Marzban API off'}
            </Badge>
          )
        }
      />
      <div className="space-y-4 p-5">
        {status.data && !status.data.enabled && (
          <p className="rounded-control border border-warning/25 bg-warning-soft px-3 py-2.5 text-sm text-warning-fg">
            {t('The Marzban-compatible API is switched off. A super admin can turn it on in Settings.')}
          </p>
        )}
        <ol className="list-decimal space-y-2 ps-5 text-sm leading-relaxed text-muted">
          <li>{t('Create an API key below with the permissions the bot needs: read, create, update, suspend and delete for full selling.')}</li>
          <li>{t('In the bot, add a panel and choose the panel type Marzban.')}</li>
          <li>
            {tx(
              'Enter the panel URL below, the key ID as the <b>username</b> and the key secret as the <b>password</b>.',
              { b: (c) => <strong className="text-fg">{c}</strong> },
            )}
          </li>
          <li>{t('Leave inbounds and proxies at their defaults: they are accepted and ignored.')}</li>
        </ol>
        <div>
          <p className="mb-1.5 text-xs font-medium text-muted">{t('Panel URL')}</p>
          <div className="flex items-center gap-2">
            <p className="ltr min-w-0 flex-1 break-all rounded-lg border border-edge bg-inset p-2.5 font-mono text-xs text-fg">{panelUrl}</p>
            <button
              type="button"
              onClick={() => copy(panelUrl)}
              aria-label={t('Copy panel URL')}
              className="cursor-pointer rounded-control border border-edge p-2 text-muted hover:text-fg focus-visible:ring-2 focus-visible:ring-accent/50 focus-visible:outline-none"
            >
              <Copy className="h-4 w-4" />
            </button>
          </div>
        </div>
        <p className="flex items-start gap-2 text-xs leading-relaxed text-faint">
          <Bot className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          {t('Use the panel domain, not the separate subscription domain. Details and limits: docs/marzban-compat.md.')}
        </p>
      </div>
    </Card>
  )
}
