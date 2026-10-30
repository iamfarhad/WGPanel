/** Marzban-compatible API: the Settings switch and the "Connect a sales bot" card on the
 * API Keys page. Product and bot names (Marzban, Mirza panel, Faoxima, ShopVPN) stay in
 * Latin script. */
export const marzban: Record<string, string> = {
  // Settings card
  'Sales bot compatibility (Marzban API)': 'سازگاری با ربات‌های فروش (API مرزبان)',
  'Lets Telegram sales bots that support Marzban work with this panel.':
    'ربات‌های فروش تلگرامی که از Marzban پشتیبانی می‌کنند، می‌توانند با این پنل کار کنند.',
  'Could not load this setting.': 'بارگذاری این تنظیم ممکن نشد.',
  'When on, bots such as Mirza panel connect with the panel type <b>Marzban</b>: the username is an API key ID and the password is its secret. Each key keeps its own permissions, node groups and rate limit. Create keys on the API Keys page, which also shows the connection steps.':
    'با روشن بودن این گزینه، ربات‌هایی مثل Mirza panel با نوع پنل <b>Marzban</b> وصل می‌شوند: نام کاربری همان شناسه‌ی کلید API و رمز عبور سکرت آن است. هر کلید دسترسی‌ها، گروه‌های نود و سقف درخواست خودش را حفظ می‌کند. کلیدها را در صفحه‌ی کلیدهای API بسازید؛ مراحل اتصال هم همان‌جا آمده.',
  'Enable the Marzban-compatible API': 'فعال‌سازی API سازگار با Marzban',
  'Only a super admin can change this.': 'فقط مدیر کل می‌تواند این را تغییر بدهد.',
  'Marzban-compatible API enabled': 'API سازگار با Marzban فعال شد',
  'Marzban-compatible API disabled': 'API سازگار با Marzban غیرفعال شد',
  enabled: 'فعال',
  disabled: 'غیرفعال',

  // API Keys page card
  'Connect a sales bot': 'اتصال ربات فروش',
  'Telegram bots that support Marzban (Mirza panel, Faoxima, ShopVPN) can sell from this panel.':
    'ربات‌های تلگرامی که از Marzban پشتیبانی می‌کنند (Mirza panel، Faoxima، ShopVPN) می‌توانند از این پنل بفروشند.',
  'Marzban API on': 'API مرزبان روشن',
  'Marzban API off': 'API مرزبان خاموش',
  'The Marzban-compatible API is switched off. A super admin can turn it on in Settings.':
    'API سازگار با Marzban خاموش است. مدیر کل می‌تواند آن را از بخش تنظیمات روشن کند.',
  'Create an API key below with the permissions the bot needs: read, create, update, suspend and delete for full selling.':
    'پایین همین صفحه یک کلید API با دسترسی‌های مورد نیاز ربات بسازید: برای فروش کامل، خواندن، ساخت، ویرایش، تعلیق و حذف.',
  'In the bot, add a panel and choose the panel type Marzban.': 'در ربات یک پنل اضافه کنید و نوع پنل را Marzban بگذارید.',
  'Enter the panel URL below, the key ID as the <b>username</b> and the key secret as the <b>password</b>.':
    'آدرس پنل را که پایین آمده وارد کنید؛ شناسه‌ی کلید را به‌عنوان <b>نام کاربری</b> و سکرت کلید را به‌عنوان <b>رمز عبور</b> بگذارید.',
  'Leave inbounds and proxies at their defaults: they are accepted and ignored.':
    'اینباند و پروکسی را روی پیش‌فرض بگذارید؛ پنل آن‌ها را می‌پذیرد ولی نادیده می‌گیرد.',
  'Panel URL': 'آدرس پنل',
  'Copy panel URL': 'کپی آدرس پنل',
  'Copied to clipboard': 'کپی شد',
  'Use the panel domain, not the separate subscription domain. Details and limits: docs/marzban-compat.md.':
    'دامنه‌ی پنل را بدهید، نه دامنه‌ی جدای اشتراک. جزئیات و محدودیت‌ها: docs/marzban-compat.md.',
}
