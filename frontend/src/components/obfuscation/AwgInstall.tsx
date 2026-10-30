import { Apple, Laptop, MonitorSmartphone, Smartphone } from 'lucide-react'
import { Card } from '../ui/Card'
import { Badge } from '../ui/Badge'
import { useI18n } from '../../lib/i18n'

export interface AwgApp {
  os: 'android' | 'ios' | 'windows' | 'macos' | 'linux'
  name: string
  url: string
}

const osMeta = {
  android: { label: 'Android', icon: Smartphone },
  ios: { label: 'iPhone / iPad', icon: Apple },
  windows: { label: 'Windows', icon: Laptop },
  macos: { label: 'macOS', icon: MonitorSmartphone },
  linux: { label: 'Linux', icon: Laptop },
} as const

/** Public subscription page, AmneziaWG nodes: the plain WireGuard apps cannot import
 * these configs, so point the user at the AmneziaVPN / AmneziaWG apps instead. */
export function AwgInstall({ apps, compat }: { apps: AwgApp[]; compat?: 'awg1' | 'awg2' }) {
  const { t } = useI18n()
  const order: AwgApp['os'][] = ['android', 'ios', 'windows', 'macos', 'linux']

  return (
    <>
      <Card className="p-5">
        <h2 className="mb-3 text-sm font-semibold text-fg">{t('1. Install the AmneziaWG app')}</h2>
        <p className="mb-3 text-xs leading-relaxed text-muted">
          {t('This server uses AmneziaWG. The regular WireGuard app cannot connect to it - use one of these apps instead.')}
        </p>
        <div className="space-y-2">
          {order.map((os) => {
            const list = apps.filter((a) => a.os === os)
            if (list.length === 0) return null
            const Icon = osMeta[os].icon
            return (
              <div key={os} className="flex flex-wrap items-center gap-2 rounded-lg border border-edge px-3 py-2.5">
                <span className="flex min-w-32 items-center gap-2 text-sm font-medium text-fg">
                  <Icon className="h-4 w-4 shrink-0 text-accent" />
                  {t(osMeta[os].label)}
                </span>
                {list.map((a) => (
                  <a
                    key={a.url}
                    href={a.url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="rounded-md bg-inset px-2.5 py-1 text-xs font-medium text-fg outline-none transition-colors hover:bg-edge focus-visible:ring-[3px] focus-visible:ring-ring"
                  >
                    {a.name}
                  </a>
                ))}
              </div>
            )
          })}
        </div>
        <p className="mt-3 flex flex-wrap items-center gap-2 text-xs text-muted">
          {t('Only install the apps from the links above.')}
          {compat === 'awg2' && <Badge tone="amber">{t('Needs a recent app version (AWG 2.0)')}</Badge>}
        </p>
      </Card>

      <Card className="p-5">
        <h2 className="mb-3 text-sm font-semibold text-fg">{t('2. Add your configuration')}</h2>
        <ol className="list-decimal space-y-2 ps-5 text-sm leading-relaxed text-muted marker:text-faint">
          <li>{t('Open the AmneziaWG (or AmneziaVPN) app and choose to add a tunnel from a file or a QR code.')}</li>
          <li>{t('Scan the QR code above, or import the downloaded config file.')}</li>
          <li>{t('Turn the tunnel on. If you were given a new link or config, delete the old tunnel first.')}</li>
        </ol>
        <p className="mt-3 text-xs leading-relaxed text-muted">
          {t('Keep this page private: anyone with this link can use your subscription.')}
        </p>
      </Card>
    </>
  )
}
