import { NavLink, Outlet } from 'react-router-dom'
import {
  LayoutDashboard,
  Server,
  Users,
  KeyRound,
  ShieldCheck,
  ScrollText,
  LogOut,
  ShieldHalf,
  Settings,
} from 'lucide-react'
import { useAuth } from '../lib/auth'
import type { AdminRole } from '../lib/auth'

interface NavItem {
  to: string
  label: string
  icon: typeof LayoutDashboard
  minRole?: AdminRole
}

const roleRank: Record<AdminRole, number> = { support: 1, operator: 2, super_admin: 3 }

const navItems: NavItem[] = [
  { to: '/dashboard', label: 'Dashboard', icon: LayoutDashboard },
  { to: '/nodes', label: 'Nodes', icon: Server },
  { to: '/accounts', label: 'Accounts', icon: Users },
  { to: '/api-keys', label: 'API Keys', icon: KeyRound, minRole: 'super_admin' },
  { to: '/admins', label: 'Admin Users', icon: ShieldCheck, minRole: 'super_admin' },
  { to: '/audit-log', label: 'Audit Log', icon: ScrollText, minRole: 'super_admin' },
  { to: '/settings', label: 'Settings', icon: Settings, minRole: 'super_admin' },
]

export function DashboardLayout() {
  const { logout, user } = useAuth()
  const visibleItems = navItems.filter(
    (item) => !item.minRole || (user && roleRank[user.role] >= roleRank[item.minRole]),
  )

  return (
    <div className="flex min-h-screen bg-slate-50 dark:bg-slate-950">
      <aside className="flex w-64 shrink-0 flex-col border-r border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
        <div className="flex items-center gap-2 px-6 py-5">
          <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-slate-900 text-white dark:bg-slate-100 dark:text-slate-900">
            <ShieldHalf className="h-5 w-5" />
          </div>
          <span className="text-lg font-semibold text-slate-900 dark:text-slate-100">WGPanel</span>
        </div>
        <nav className="flex-1 space-y-1 px-3">
          {visibleItems.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              className={({ isActive }) =>
                `flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors ${
                  isActive
                    ? 'bg-slate-900 text-white dark:bg-slate-100 dark:text-slate-900'
                    : 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800'
                }`
              }
            >
              <item.icon className="h-4 w-4" />
              {item.label}
            </NavLink>
          ))}
        </nav>
        <div className="border-t border-slate-200 p-4 dark:border-slate-800">
          <div className="mb-3 px-2">
            <p className="truncate text-sm font-medium text-slate-900 dark:text-slate-100">{user?.username}</p>
            <p className="text-xs capitalize text-slate-500 dark:text-slate-400">{user?.role.replace('_', ' ')}</p>
          </div>
          <button
            onClick={logout}
            className="flex w-full items-center gap-3 rounded-md px-3 py-2 text-sm font-medium text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800"
          >
            <LogOut className="h-4 w-4" />
            Log out
          </button>
        </div>
      </aside>
      <main className="flex-1 overflow-y-auto p-8">
        <Outlet />
      </main>
    </div>
  )
}
