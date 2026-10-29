export const settings: Record<string, string> = {
  'Panel-wide configuration and defaults.': 'پیکربندی و مقادیر پیش‌فرض کل پنل.',
  'Failed to create backup': 'ساخت بکاپ ناموفق بود',
  'Backup downloaded - keep the file AND its password safe; the password cannot be recovered':
    'بکاپ دانلود شد. فایل و رمزش را هر دو جای امن نگه دارید؛ رمز قابل بازیابی نیست.',
  'Backup restored': 'بکاپ بازیابی شد',
  'That file is not a Jirjirak WireGuard backup': 'این فایل بکاپ جیرجیرک وایرگارد نیست',
  'Restore failed': 'بازیابی ناموفق بود',
  'Backup & restore': 'بکاپ و بازیابی',
  'One password-encrypted file with everything: admins, nodes, accounts and their keys, API keys, settings, audit log, the node CA, and the encryption keys from deploy/.env. Restoring needs only this file and its password - even on a brand-new server.':
    'یک فایل رمزگذاری‌شده با همه‌چیز: ادمین‌ها، نودها، اکانت‌ها و کلیدهایشان، کلیدهای API، تنظیمات، گزارش فعالیت‌ها، CA نودها و کلیدهای رمزنگاری deploy/.env. برای بازیابی فقط همین فایل و رمزش لازم است، حتی روی یک سرور کاملاً تازه.',
  'The file is useless without the password you choose - and the password cannot be recovered, so store both safely. Metrics history (usage charts) is not included; account usage totals are.':
    'بدون رمزی که انتخاب می‌کنید، فایل به هیچ دردی نمی‌خورد و رمز هم قابل بازیابی نیست؛ پس هر دو را جای امن نگه دارید. تاریخچه‌ی متریک‌ها (نمودارهای مصرف) در بکاپ نیست، ولی مجموع مصرف اکانت‌ها هست.',
  'Download backup': 'دانلود بکاپ',
  "Restore replaces <b>all</b> current panel data with the file's contents - including admin users, so your own login may change. Works on a fresh install with new <code>.env</code> keys: account keys are re-encrypted automatically.":
    'بازیابی، <b>همه‌ی</b> اطلاعات فعلی پنل را با محتوای فایل جایگزین می‌کند؛ از جمله ادمین‌ها، پس ممکن است ورود خودتان هم عوض شود. روی نصب تازه با کلیدهای جدید <code>.env</code> هم کار می‌کند: کلیدهای اکانت‌ها خودکار دوباره رمز می‌شوند.',
  'Restore backup': 'بازیابی بکاپ',
  'Restored {accounts} accounts, {nodes} nodes, {admins} admins and {keys} API keys.':
    '{accounts} اکانت، {nodes} نود، {admins} ادمین و {keys} کلید API بازیابی شد.',
  'The node CA changed - restart the api container (wgpanel restart) so agents can reconnect.':
    'CA نودها عوض شده است. کانتینر api را ری‌استارت کنید (wgpanel restart) تا ایجنت‌ها دوباره وصل شوند.',
  'Log in again with the restored credentials.': 'با اطلاعات ورودِ بازیابی‌شده دوباره وارد شوید.',
  'Log out now': 'همین حالا خارج شو',
  'Encrypt backup': 'رمزگذاری بکاپ',
  'Choose a password for this backup. Restoring it - here or on a new server - requires exactly this password; it is not stored anywhere and cannot be recovered.':
    'برای این بکاپ یک رمز انتخاب کنید. بازیابی آن، چه همین‌جا چه روی سرور جدید، دقیقاً همین رمز را می‌خواهد؛ جایی ذخیره نمی‌شود و قابل بازیابی نیست.',
  'Backup password': 'رمز بکاپ',
  'At least 8 characters': 'حداقل ۸ کاراکتر',
  'Confirm password': 'تکرار رمز',
  'Passwords do not match.': 'رمزها یکسان نیستند.',
  'Encrypting…': 'در حال رمزگذاری…',
  Download: 'دانلود',
  'Replace all panel data?': 'همه‌ی اطلاعات پنل جایگزین شود؟',
  'Everything currently in this panel - accounts, nodes, admins, API keys, settings, audit log - will be replaced by':
    'هرچه الان در این پنل هست (اکانت‌ها، نودها، ادمین‌ها، کلیدهای API، تنظیمات، گزارش فعالیت‌ها) با این فایل جایگزین می‌شود:',
  'This cannot be undone. Download a backup of the current state first if you might need it.':
    'این کار برگشت ندارد. اگر ممکن است به وضعیت فعلی نیاز پیدا کنید، اول از آن بکاپ بگیرید.',
  'The password this backup was encrypted with': 'رمزی که این بکاپ با آن رمزگذاری شده',
  'Restoring…': 'در حال بازیابی…',
  'Replace everything': 'جایگزین‌کردن همه‌چیز',
  'Settings saved': 'تنظیمات ذخیره شد',
  'Failed to save settings': 'ذخیره‌ی تنظیمات ناموفق بود',
  '{a} and {b}': '{a} و {b}',
  'Domains updated - Caddy is provisioning certificates for {domains}': 'دامنه‌ها به‌روز شد. Caddy در حال گرفتن گواهی برای {domains} است',
  'Domains saved, but could not push them to Caddy live': 'دامنه‌ها ذخیره شد، ولی اعمال زنده‌ی آن‌ها روی Caddy نشد',
  'Failed to update domains': 'به‌روزرسانی دامنه‌ها ناموفق بود',
  'Domain & TLS': 'دامنه و TLS',
  "The domains Caddy serves and automatically provisions Let's Encrypt certificates for. Changes take effect live via Caddy's admin API - no restart or redeploy needed. Requires DNS for each domain to already point at this server.":
    'دامنه‌هایی که Caddy سرو می‌کند و برایشان خودکار گواهی Let\'s Encrypt می‌گیرد. تغییرات با API ادمین Caddy زنده اعمال می‌شود و ری‌استارت یا دیپلوی دوباره لازم نیست. DNS هر دامنه باید از قبل به همین سرور اشاره کند.',
  'Panel domain': 'دامنه‌ی پنل',
  'Subscription domain': 'دامنه‌ی اشتراک',
  'Optional separate domain for the subscription links handed to end users. It serves only the subscription endpoints - the admin panel is never reachable through it - with its own automatic certificate. Leave empty to keep subscription links on the panel domain.':
    'دامنه‌ی جداگانه و اختیاری برای لینک‌های اشتراکی که به کاربران نهایی داده می‌شود. فقط اندپوینت‌های اشتراک روی آن سرو می‌شود، پنل ادمین از آن در دسترس نیست، و گواهی خودکار خودش را دارد. خالی بگذارید تا لینک‌های اشتراک روی دامنه‌ی پنل بمانند.',
  'Subscription port': 'پورت اشتراک',
  'Default 443 works with no extra setup. Any other port must match SUB_PORT in deploy/.env (default 8443) and be open in the firewall.':
    'پورت پیش‌فرض 443 بدون هیچ تنظیم اضافه کار می‌کند. هر پورت دیگری باید با SUB_PORT در deploy/.env (پیش‌فرض 8443) یکی باشد و در فایروال باز باشد.',
  'Applied live - Caddy is now serving with this configuration.': 'زنده اعمال شد. Caddy الان با همین پیکربندی کار می‌کند.',
  'Saved, but the live push to Caddy failed:': 'ذخیره شد، ولی اعمال زنده روی Caddy ناموفق بود:',
  'The API retries it automatically on its next restart.': 'API در ری‌استارت بعدی خودکار دوباره تلاش می‌کند.',
  'Applying…': 'در حال اعمال…',
  'Apply domains': 'اعمال دامنه‌ها',
  'Defaults & panel info': 'پیش‌فرض‌ها و اطلاعات پنل',
  'Baseline values applied to new nodes and accounts, plus what other admins see about this panel.':
    'مقادیر پایه برای نودها و اکانت‌های جدید، و چیزی که ادمین‌های دیگر درباره‌ی این پنل می‌بینند.',
  'Could not load settings.': 'بارگذاری تنظیمات ممکن نشد.',
  'Public base URL': 'آدرس عمومی پنل',
  "The domain this panel is served on (e.g. https://panel.example.com). Informational - changing it here does not move DNS/TLS, which is still configured in deploy/.env's PANEL_DOMAIN and requires a redeploy to actually change.":
    'دامنه‌ای که پنل روی آن سرو می‌شود (مثلاً https://panel.example.com). فقط اطلاعاتی است؛ تغییر آن اینجا DNS و TLS را جابه‌جا نمی‌کند. آن‌ها هنوز با PANEL_DOMAIN در deploy/.env تنظیم می‌شوند و تغییر واقعی‌شان دیپلوی دوباره می‌خواهد.',
  'Default account data quota (GB)': 'سقف حجم پیش‌فرض اکانت (GB)',
  'Default account device limit': 'محدودیت دستگاه پیش‌فرض اکانت',
  'Default node capacity (max peers)': 'ظرفیت پیش‌فرض نود (حداکثر پیر)',
  'Client DNS': 'DNS کاربران',
  The: 'خط',
  'line of every generated WireGuard config. Clients full-tunnel, so they can only resolve names through it - a resolver unreachable from where a node egresses means clients connect but have "no internet".':
    'در همه‌ی کانفیگ‌های وایرگارد ساخته‌شده. ترافیک کاربران کامل از تونل رد می‌شود و نام‌ها را فقط از همین راه حل می‌کنند؛ اگر ریزالور از جایی که نود خارج می‌شود در دسترس نباشد، کاربران وصل می‌شوند ولی «اینترنت ندارند».',
  'Each node': 'گزینه‌ی «هر نود»',
  'avoids that: clients ask the node itself, which resolves through whatever its server uses (nodes running an older agent fall back to 1.1.1.1). Takes effect on the next config download or subscription refresh.':
    'این مشکل را ندارد: کاربران از خود نود می‌پرسند و نود با همان ریزالور سرورش جواب می‌دهد (نودهای با ایجنت قدیمی‌تر روی 1.1.1.1 می‌روند). از دانلود بعدی کانفیگ یا بازخوانی بعدی اشتراک اعمال می‌شود.',
  'Each node resolves for its clients (recommended)': 'هر نود برای کاربرانش جواب می‌دهد (پیشنهادی)',
  'Custom resolvers…': 'ریزالور دلخواه…',
  'Support contact': 'راه ارتباط با پشتیبانی',
  'Shown to other admins who need help (e.g. an email address or Telegram handle).':
    'به ادمین‌هایی که کمک لازم دارند نشان داده می‌شود (مثلاً ایمیل یا آیدی تلگرام).',
  'Save settings': 'ذخیره‌ی تنظیمات',
}
