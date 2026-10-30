/** Persian strings for split-tunnel routing ("Iran direct"), the per-node IPv6 opt-in and
 * the related customer-facing wording. Keyed by the English source text. */
export const routing: Record<string, string> = {
  Routing: 'مسیریابی',
  'Full tunnel': 'تونل کامل',
  'Iran direct': 'اتصال مستقیم ایران',
  'Custom exclusions': 'استثناهای دلخواه',
  'Default (full tunnel)': 'پیش‌فرض (تونل کامل)',
  'Same as template': 'مطابق قالب',
  "Iran direct sends Iranian and local addresses straight out of the user's own connection: Iranian bank and government sites keep working and that traffic does not use the quota. Everything else still goes through the tunnel (DNS too).":
    'در حالت «اتصال مستقیم ایران»، آدرس‌های ایرانی و شبکه‌ی محلی مستقیم از اینترنت خود کاربر می‌روند؛ سایت بانک‌ها و دستگاه‌های دولتی که IP خارجی را قبول نمی‌کنند کار می‌کنند و این ترافیک هم از حجم کم نمی‌شود. بقیه‌ی ترافیک (از جمله DNS) همچنان از تونل رد می‌شود.',
  'Excluded IPv4 ranges': 'رنج‌های IPv4 مستثنا',
  'One CIDR per line, e.g. 203.0.113.0/24. These bypass the tunnel; everything else goes through it.':
    'هر خط یک CIDR، مثلاً 203.0.113.0/24. این رنج‌ها از تونل رد نمی‌شوند و بقیه‌ی ترافیک از تونل می‌رود.',
  'List precision': 'دقت فهرست',
  'Compact keeps the client config near 1000 routes so apps import it quickly; a few foreign addresses may go direct and the smallest excluded ranges may use the tunnel. Exact lists every range (about 8000 for Iran direct) - slow to import on some phones and Windows.':
    'در حالت فشرده، کانفیگ کلاینت حدود ۱۰۰۰ مسیر دارد و برنامه‌ها سریع واردش می‌کنند؛ در عوض چند آدرس خارجی ممکن است مستقیم بروند و کوچک‌ترین رنج‌های مستثنا از تونل رد شوند. حالت دقیق همه‌ی رنج‌ها را می‌نویسد (برای ایران حدود ۸۰۰۰ مسیر) و روی بعضی گوشی‌ها و ویندوز دیر وارد می‌شود.',
  'Compact (default)': 'فشرده (پیش‌فرض)',
  Exact: 'دقیق',
  'This config is too large for a QR code - use the file or the subscription link.':
    'این کانفیگ برای کد QR زیادی بزرگ است؛ از فایل یا لینک اشتراک استفاده کنید.',

  // Public subscription page
  'Iranian sites connect directly': 'سایت‌های ایرانی مستقیم وصل می‌شوند',
  'Some addresses connect directly': 'برخی آدرس‌ها مستقیم وصل می‌شوند',
  'All traffic through the VPN': 'تمام ترافیک از طریق وی‌پی‌ان',

  // Node IPv6 egress
  'IPv6 egress': 'خروجی IPv6',
  'IPv6 egress - {name}': 'خروجی IPv6 - {name}',
  'IPv6 settings saved for {name}': 'تنظیمات IPv6 برای {name} ذخیره شد',
  'Failed to save IPv6 settings': 'ذخیره‌ی تنظیمات IPv6 ناموفق بود',
  "Lets clients of this node reach IPv6 sites. Each client gets a private address from the node's /64, translated (NAT66) to the node's own global IPv6 address. Off by default; ::/0 stays in client routes either way, so nothing leaks while it is off.":
    'کاربران این نود می‌توانند به سایت‌های IPv6 هم وصل شوند. هر کاربر یک آدرس خصوصی از /64 نود می‌گیرد که با NAT66 به آدرس IPv6 عمومی خود نود تبدیل می‌شود. به‌صورت پیش‌فرض خاموش است؛ در هر دو حالت ::/0 در مسیرهای کلاینت می‌ماند، پس تا وقتی خاموش است هم IPv6 کاربر نشت نمی‌کند.',
  'Node has global IPv6': 'نود IPv6 عمومی دارد',
  'No global IPv6 on the node': 'نود IPv6 عمومی ندارد',
  'Active in client configs': 'در کانفیگ کاربران فعال است',
  'Not active': 'فعال نیست',
  'Agent supports PresharedKey': 'ایجنت از PresharedKey پشتیبانی می‌کند',
  'Agent without PresharedKey support': 'ایجنت بدون پشتیبانی از PresharedKey',
  'Enable IPv6 egress on this node': 'فعال‌سازی خروجی IPv6 روی این نود',
  'Needs a global IPv6 route on the node. For the container install that means an IPv6-enabled Docker network and the IPv6 sysctls; the agent reports what is missing.':
    'نود باید یک مسیر IPv6 عمومی داشته باشد. در نصب کانتینری یعنی شبکه‌ی داکر با IPv6 فعال و sysctlهای IPv6؛ ایجنت اگر چیزی کم باشد گزارش می‌دهد.',
  'Node IPv6 prefix': 'پیشوند IPv6 نود',
  'Disable IPv6 on the node to change the prefix: clients hold addresses derived from it.':
    'برای تغییر پیشوند، اول IPv6 نود را خاموش کنید؛ آدرس کاربران از همین پیشوند ساخته شده است.',
  'A unique-local /64, e.g. fd12:3456:789a:1::/64. Leave empty to generate one.':
    'یک /64 از نوع unique-local، مثلاً fd12:3456:789a:1::/64. خالی بگذارید تا خودکار ساخته شود.',
}
