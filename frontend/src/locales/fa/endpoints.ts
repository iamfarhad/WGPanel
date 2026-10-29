/** Node entry endpoints (relays), the client MTU / Bot API rate settings and the
 * related API error messages. */
export const endpoints: Record<string, string> = {
  'Entry endpoints': 'اندپوینت‌های ورودی',
  '{name} — entry endpoints': '{name} — اندپوینت‌های ورودی',
  'Clients connect to the highest-priority healthy endpoint below, for example a relay inside a country where this node is blocked. With none healthy they use the node public endpoint.':
    'کاربران به سالم‌ترین اندپوینتِ دارای بالاترین اولویت وصل می‌شوند؛ مثلاً رله‌ای در کشوری که دسترسی به این نود مسدود است. اگر هیچ‌کدام سالم نباشد، از اندپوینت عمومی نود استفاده می‌شود.',
  'Public endpoint (fallback)': 'اندپوینت عمومی (جایگزین)',
  'Could not load endpoints.': 'بارگذاری اندپوینت‌ها ممکن نشد.',
  'No extra endpoints yet. Clients use the node public endpoint.':
    'هنوز اندپوینت اضافه‌ای نیست. کاربران از اندپوینت عمومی نود استفاده می‌کنند.',
  Disabled: 'غیرفعال',
  'Not checked': 'بدون بررسی',
  Healthy: 'سالم',
  Unreachable: 'در دسترس نیست',
  'Waiting for first check': 'منتظر اولین بررسی',
  'Priority {n}': 'اولویت {n}',
  'checked {when}': 'بررسی در {when}',
  'Check now': 'بررسی فوری',
  'Add endpoint': 'افزودن اندپوینت',
  'Edit endpoint': 'ویرایش اندپوینت',
  'New endpoint': 'اندپوینت جدید',
  'Endpoint saved': 'اندپوینت ذخیره شد',
  'Failed to save endpoint': 'ذخیره‌ی اندپوینت ناموفق بود',
  'Failed to delete endpoint': 'حذف اندپوینت ناموفق بود',
  'Health check failed to run': 'اجرای بررسی سلامت ناموفق بود',
  'Delete endpoint?': 'این اندپوینت حذف شود؟',
  'Clients using this endpoint switch to the next healthy one the next time they download their config.':
    'کاربرانی که از این اندپوینت استفاده می‌کنند، دفعه‌ی بعد که کانفیگ خود را بگیرند به اندپوینت سالم بعدی منتقل می‌شوند.',
  'Address (host:port)': 'آدرس (هاست:پورت)',
  'e.g. Tehran relay': 'مثلاً رله‌ی تهران',
  Kind: 'نوع',
  Relay: 'رله',
  Direct: 'مستقیم',
  Priority: 'اولویت',
  'Higher numbers are tried first.': 'عدد بزرگ‌تر زودتر امتحان می‌شود.',
  'Health check': 'بررسی سلامت',
  'TCP connect to host:port': 'اتصال TCP به هاست:پورت',
  'None (always assumed healthy)': 'هیچ (همیشه سالم فرض می‌شود)',
  'TCP connect works when the relay also listens on TCP on the same port.':
    'اتصال TCP وقتی کار می‌کند که رله روی همان پورت، TCP هم گوش بدهد.',
  'MTU override': 'مقدار MTU اختصاصی',
  '(optional)': '(اختیاری)',
  "1200-1420. Empty uses the panel's client MTU setting.": '۱۲۰۰ تا ۱۴۲۰. اگر خالی باشد، تنظیم MTU کاربر در پنل استفاده می‌شود.',
  Enabled: 'فعال',
  Enable: 'فعال‌سازی',
  Disable: 'غیرفعال کردن',
  'Entry endpoint': 'اندپوینت ورودی',
  'Endpoint: automatic': 'اندپوینت: خودکار',
  'Node public endpoint': 'اندپوینت عمومی نود',

  // Settings
  'Client MTU': 'MTU کاربر',
  'The MTU line of generated WireGuard configs (1200-1420). 1280 is the safe default. Raise it (for example 1380 or 1420) for better speed on clean paths, or keep it low if clients connect but large pages stall. A node endpoint can override it. Takes effect on the next config download.':
    'مقدار MTU در کانفیگ‌های وایرگاردی که ساخته می‌شود (۱۲۰۰ تا ۱۴۲۰). ۱۲۸۰ مقدار امن پیش‌فرض است. برای سرعت بهتر روی مسیرهای سالم می‌توانید آن را بالا ببرید (مثلاً ۱۳۸۰ یا ۱۴۲۰)، و اگر کاربران وصل می‌شوند ولی صفحه‌های سنگین باز نمی‌شوند، پایین نگهش دارید. هر اندپوینت ورودی می‌تواند مقدار خودش را جایگزین کند. از دفعه‌ی بعد که کانفیگ دانلود شود اعمال می‌شود.',
  'Bot API rate (requests/second per key)': 'نرخ API ربات (درخواست در ثانیه برای هر کلید)',
  "Refill rate of each API key's token bucket. Requests over the limit get 429 with Retry-After.":
    'سرعت پر شدن سطل توکن هر کلید API. به درخواست‌های بیشتر از حد مجاز، خطای ۴۲۹ همراه با Retry-After داده می‌شود.',
  'Bot API burst': 'حداکثر درخواست پشت‌سرهم API ربات',
  'How many requests a key may send at once before being limited.': 'هر کلید پیش از محدود شدن، چند درخواست را می‌تواند یک‌جا بفرستد.',

  // API errors
  'That endpoint no longer exists on this node.': 'این اندپوینت دیگر روی این نود وجود ندارد.',
  'This node already has an endpoint with that label.': 'این نود از قبل اندپوینتی با همین برچسب دارد.',
  'Health checks are turned off for this endpoint.': 'بررسی سلامت برای این اندپوینت خاموش است.',
  'Too many requests - wait a moment and try again.': 'درخواست‌ها بیش از حد است؛ کمی صبر کنید و دوباره تلاش کنید.',
  'The page position is no longer valid - reload the list.': 'موقعیت صفحه دیگر معتبر نیست؛ فهرست را دوباره بارگذاری کنید.',
}
