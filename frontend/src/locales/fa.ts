/** Persian UI strings, keyed by the English source text (see lib/i18n.tsx). Keep
 * technical values (IPs, keys, commands, file names) out of translations - they stay
 * in Latin script and render left-to-right. */
import { common } from './fa/common'
import { dashboard } from './fa/dashboard'
import { auditlog } from './fa/auditlog'
import { admins } from './fa/admins'
import { apikeys } from './fa/apikeys'
import { nodes } from './fa/nodes'
import { settings } from './fa/settings'
import { accounts } from './fa/accounts'
import { resellers } from './fa/resellers'
import { reports } from './fa/reports'
import { security } from './fa/security'
import { lifecycle } from './fa/lifecycle'
import { endpoints } from './fa/endpoints'
import { subscription } from './fa/subscription'

export const fa: Record<string, string> = {
  ...common,
  ...dashboard,
  ...auditlog,
  ...admins,
  ...apikeys,
  ...nodes,
  ...settings,
  ...accounts,
  ...resellers,
  ...reports,
  ...security,
  ...lifecycle,
  ...endpoints,
  ...subscription,

  // Shell
  Dashboard: 'داشبورد',
  Infrastructure: 'زیرساخت',
  Administration: 'مدیریت',
  Nodes: 'نودها',
  Accounts: 'اکانت‌ها',
  'API Keys': 'کلیدهای API',
  'Admin Users': 'ادمین‌ها',
  'Audit Log': 'گزارش فعالیت‌ها',
  Settings: 'تنظیمات',
  'API Docs': 'مستندات API',
  'WireGuard · Infrastructure': 'وایرگارد · زیرساخت',
  'Log out': 'خروج',
  'Open menu': 'باز کردن منو',
  'Close menu': 'بستن منو',
  Theme: 'پوسته',
  'Light theme': 'پوسته‌ی روشن',
  'Dark theme': 'پوسته‌ی تیره',
  'System theme': 'پوسته‌ی سیستم',
  Language: 'زبان',
  'Loading…': 'در حال بارگذاری…',
  Actions: 'عملیات',
  'Super Admin': 'مدیر کل',
  Operator: 'اپراتور',
  Support: 'پشتیبان',
  Reseller: 'نماینده',

  // Login
  'Sign in': 'ورود',
  'Manage your WireGuard infrastructure.': 'زیرساخت وایرگارد خود را مدیریت کنید.',
  Username: 'نام کاربری',
  Password: 'رمز عبور',
  'Signing in…': 'در حال ورود…',
  'Login failed': 'ورود ناموفق بود',
  'Jirjirak WireGuard · Secure. Simple. Fast.': 'جیرجیرک وایرگارد · امن، ساده، سریع.',
}
