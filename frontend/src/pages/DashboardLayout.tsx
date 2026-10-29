import { useState } from 'react'
import { NavLink, Outlet, useLocation } from 'react-router-dom'
import {
  LayoutDashboard,
  Server,
  Users,
  KeyRound,
  ShieldCheck,
  ScrollText,
  LogOut,
  Settings,
  Store,
  BarChart3,
  Layers,
  Menu,
  X,
  Sun,
  Moon,
  Monitor,
  BookOpen,
  ExternalLink,
} from 'lucide-react'
import { useAuth } from '../lib/auth'
import type { AdminRole } from '../lib/auth'
import { useTheme, type Theme } from '../lib/theme'
import { BrandLockup, BrandTile } from '../components/brand/Brand'
import { LanguageToggle } from '../components/ui/LanguageToggle'
import { useI18n } from '../lib/i18n'

interface NavItem {
  to: string
  label: string
  icon: typeof LayoutDashboard
  minRole?: AdminRole
  // Shown to reseller sessions. Resellers get a deliberately small panel (their own
  // dashboard and accounts); everything else is staff tooling the API denies them.
  reseller?: boolean
  // External items are plain links opened in a new tab (e.g. the static Swagger
  // page), not SPA routes - they render as <a>, never match as "active".
  external?: boolean
}

interface NavGroup {
  label: string | null
  items: NavItem[]
}

// Resellers rank below every staff role, so no minRole item ever matches them; what
// they see is decided by NavItem.reseller instead.
const roleRank: Record<AdminRole, number> = { reseller: 0, support: 1, operator: 2, super_admin: 3 }

const navGroups: NavGroup[] = [
  {
    label: null,
    items: [{ to: '/dashboard', label: 'Dashboard', icon: LayoutDashboard, reseller: true }],
  },
  {
    label: 'Infrastructure',
    items: [
      { to: '/nodes', label: 'Nodes', icon: Server },
      { to: '/accounts', label: 'Accounts', icon: Users, reseller: true },
      { to: '/templates', label: 'Templates', icon: Layers, reseller: true },
      { to: '/reports', label: 'Reports', icon: BarChart3, reseller: true },
    ],
  },
  {
    label: 'Administration',
    items: [
      { to: '/resellers', label: 'Resellers', icon: Store, minRole: 'super_admin' },
      { to: '/api-keys', label: 'API Keys', icon: KeyRound, minRole: 'super_admin', reseller: true },
      { to: '/admins', label: 'Admin Users', icon: ShieldCheck, minRole: 'super_admin' },
      { to: '/audit-log', label: 'Audit Log', icon: ScrollText, minRole: 'super_admin' },
      { to: '/settings', label: 'Settings', icon: Settings, minRole: 'super_admin' },
      // No minRole: the Swagger page is a static asset nginx serves to anyone
      // with the URL anyway, so hiding the link would only be security theater.
      { to: '/api-docs.html', label: 'API Docs', icon: BookOpen, external: true, reseller: true },
    ],
  },
]

const themeOrder: Theme[] = ['light', 'dark', 'system']
const themeMeta: Record<Theme, { icon: typeof Sun; label: string }> = {
  light: { icon: Sun, label: 'Light theme' },
  dark: { icon: Moon, label: 'Dark theme' },
  system: { icon: Monitor, label: 'System theme' },
}

const roleLabels: Record<string, string> = {
  super_admin: 'Super Admin',
  operator: 'Operator',
  support: 'Support',
  reseller: 'Reseller',
}

function ThemeToggle() {
  const { theme, setTheme } = useTheme()
  const { t } = useI18n()
  return (
    <div className="flex flex-1 items-center gap-1 rounded-lg bg-inset p-1" role="group" aria-label={t('Theme')}>
      {themeOrder.map((th) => {
        const Icon = themeMeta[th].icon
        const active = theme === th
        return (
          <button
            key={th}
            type="button"
            title={t(themeMeta[th].label)}
            aria-pressed={active}
            onClick={() => setTheme(th)}
            className={`flex h-7 flex-1 cursor-pointer items-center justify-center rounded-md transition-colors duration-150 ${
              active ? 'bg-surface text-fg shadow-xs ring-1 ring-edge' : 'text-faint hover:text-muted'
            }`}
          >
            <Icon className="h-3.5 w-3.5" />
          </button>
        )
      })}
    </div>
  )
}

export function DashboardLayout() {
  const { logout, user } = useAuth()
  const { t } = useI18n()
  const location = useLocation()
  const [mobileOpen, setMobileOpen] = useState(false)

  const visibleGroups = navGroups
    .map((group) => ({
      ...group,
      items: group.items.filter(
        (item) =>
          user?.role === 'reseller'
            ? item.reseller === true
            : !item.minRole || (user && roleRank[user.role] >= roleRank[item.minRole]),
      ),
    }))
    .filter((group) => group.items.length > 0)

  const initials = (user?.username ?? '?').slice(0, 2).toUpperCase()
  const currentLabel =
    t(navGroups.flatMap((g) => g.items).find((i) => location.pathname.startsWith(i.to))?.label ?? 'Jirjirak')

  const sidebar = (
    <>
      <div className="px-5 pt-5 pb-4">
        <BrandLockup subtitle={t('WireGuard · Infrastructure')} />
      </div>

      <nav className="flex-1 space-y-5 overflow-y-auto px-3 py-2">
        {visibleGroups.map((group, gi) => (
          <div key={group.label ?? gi}>
            {group.label && (
              <p className="mb-1.5 px-3 text-[11px] font-medium tracking-wide text-faint uppercase">
                {t(group.label)}
              </p>
            )}
            <div className="space-y-0.5">
              {group.items.map((item) =>
                item.external ? (
                  <a
                    key={item.to}
                    href={item.to}
                    target="_blank"
                    rel="noreferrer"
                    onClick={() => setMobileOpen(false)}
                    className="group relative flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium text-muted transition-colors duration-150 hover:bg-inset/70 hover:text-fg"
                  >
                    <item.icon className="h-[18px] w-[18px] shrink-0 text-faint group-hover:text-muted" strokeWidth={1.75} />
                    {t(item.label)}
                    <ExternalLink className="ms-auto h-3.5 w-3.5 shrink-0 text-faint opacity-0 transition-opacity group-hover:opacity-100" />
                  </a>
                ) : (
                  <NavLink
                    key={item.to}
                    to={item.to}
                    onClick={() => setMobileOpen(false)}
                    className={({ isActive }) =>
                      `group relative flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-colors duration-150 ${
                        isActive ? 'bg-accent/10 text-accent-fg' : 'text-muted hover:bg-inset/70 hover:text-fg'
                      }`
                    }
                  >
                    {({ isActive }) => (
                      <>
                        {isActive && (
                          <span className="absolute start-0 top-1/2 h-5 w-[3px] -translate-y-1/2 rounded-e-full bg-accent" />
                        )}
                        <item.icon
                          className={`h-[18px] w-[18px] shrink-0 ${isActive ? 'text-accent' : 'text-faint group-hover:text-muted'}`}
                          strokeWidth={1.75}
                        />
                        {t(item.label)}
                      </>
                    )}
                  </NavLink>
                ),
              )}
            </div>
          </div>
        ))}
      </nav>

      <div className="space-y-3 border-t border-edge p-4">
        <div className="flex items-center gap-3 px-1">
          <div className="flex h-8.5 w-8.5 shrink-0 items-center justify-center rounded-full bg-accent/10 text-xs font-semibold text-accent-fg">
            {initials}
          </div>
          <div className="min-w-0 flex-1 leading-tight">
            <p className="truncate text-sm font-medium text-fg">{user?.username}</p>
            <p className="text-xs text-faint">{t(roleLabels[user?.role ?? ''] ?? user?.role ?? '')}</p>
          </div>
          <button
            onClick={logout}
            title={t('Log out')}
            className="flex h-8 w-8 shrink-0 cursor-pointer items-center justify-center rounded-lg text-faint transition-colors duration-150 hover:bg-inset hover:text-fg"
          >
            <LogOut className="h-4 w-4" />
            <span className="sr-only">{t('Log out')}</span>
          </button>
        </div>
        <div className="flex gap-2">
          <ThemeToggle />
          <LanguageToggle />
        </div>
      </div>
    </>
  )

  return (
    <div className="flex min-h-screen bg-bg">
      {/* Desktop sidebar */}
      <aside className="sticky top-0 hidden h-screen w-64 shrink-0 flex-col border-e border-edge bg-surface lg:flex">
        {sidebar}
      </aside>

      {/* Mobile slide-over sidebar */}
      {mobileOpen && (
        <div className="fixed inset-0 z-40 lg:hidden">
          <div className="animate-backdrop-in absolute inset-0 bg-overlay" onClick={() => setMobileOpen(false)} />
          <aside className="animate-dialog-in absolute inset-y-0 start-0 flex w-72 flex-col border-e border-edge bg-surface shadow-pop">
            <button
              type="button"
              onClick={() => setMobileOpen(false)}
              aria-label={t('Close menu')}
              className="absolute end-3 top-4 cursor-pointer rounded-lg p-1.5 text-faint hover:bg-inset hover:text-fg"
            >
              <X className="h-5 w-5" />
            </button>
            {sidebar}
          </aside>
        </div>
      )}

      <div className="flex min-w-0 flex-1 flex-col">
        {/* Mobile top bar */}
        <header className="sticky top-0 z-30 flex items-center gap-3 border-b border-edge bg-surface/90 px-4 py-3 backdrop-blur lg:hidden">
          <button
            type="button"
            onClick={() => setMobileOpen(true)}
            aria-label={t('Open menu')}
            className="cursor-pointer rounded-lg p-1.5 text-muted hover:bg-inset hover:text-fg"
          >
            <Menu className="h-5 w-5" />
          </button>
          <BrandTile size="sm" />
          <span className="text-sm font-semibold text-fg">{currentLabel}</span>
        </header>

        <main className="relative flex-1 overflow-y-auto">
          {/* A faint brand-green wash at the top of every page - color, not decoration. */}
          <div
            aria-hidden
            className="pointer-events-none absolute inset-x-0 top-0 h-72 bg-[radial-gradient(70%_100%_at_50%_0%,var(--ui-accent-soft),transparent)] opacity-70"
          />
          <div className="relative mx-auto w-full max-w-7xl p-5 sm:p-8 lg:p-10">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  )
}
