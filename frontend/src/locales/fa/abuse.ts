/** Persian strings for the abuse controls: account notes and tags, auto-cleanup, the
 * device-limit counting mode, the shared-account heuristic and torrent blocking.
 * Keyed by the English source text. */
export const abuse: Record<string, string> = {
  // Notes and tags
  Tags: 'برچسب‌ها',
  Tag: 'برچسب',
  Note: 'یادداشت',
  'Short labels to group and filter accounts, like vip or trial.': 'برچسب‌های کوتاه برای دسته‌بندی و فیلتر اکانت‌ها، مثل vip یا trial.',
  'Short labels to group and filter accounts.': 'برچسب‌های کوتاه برای دسته‌بندی و فیلتر اکانت‌ها.',
  'Only you see this - for example who paid and how.': 'فقط خودتان این را می‌بینید؛ مثلاً اینکه چه کسی و چطور پرداخت کرده.',
  'Only you see this.': 'فقط خودتان این را می‌بینید.',
  'Add a tag and press Enter': 'یک برچسب بنویسید و Enter بزنید',
  'Remove tag {name}': 'حذف برچسب {name}',
  'Filter by tag': 'فیلتر با برچسب',
  'All tags': 'همه برچسب‌ها',
  'Add tag filter…': 'افزودن فیلتر برچسب…',
  'Export CSV': 'خروجی CSV',
  'Tag accounts': 'برچسب‌گذاری اکانت‌ها',
  'Change the tags of {n} accounts. Their other tags are left alone.': 'برچسب‌های {n} اکانت تغییر می‌کند. بقیهٔ برچسب‌های آن‌ها دست نمی‌خورد.',
  'Add these tags': 'افزودن این برچسب‌ها',
  'Remove these tags': 'حذف این برچسب‌ها',
  Apply: 'اعمال',
  'Tags added': 'برچسب‌ها اضافه شد',
  'Tags removed': 'برچسب‌ها حذف شد',

  // Account badges and details
  'possibly shared': 'احتمالاً اشتراکی',
  'torrent ×{n}': 'تورنت ×{n}',
  'Active from several different networks at once. Open the Devices tab for details.': 'هم‌زمان از چند شبکهٔ مختلف وصل است. جزئیات در تب دستگاه‌ها.',
  'Blocked BitTorrent packets in the last 24 hours': 'بسته‌های بیت‌تورنت مسدودشده در ۲۴ ساعت گذشته',
  'The device limit counts distinct source IPs seen in the last {n} minutes (ports are ignored).':
    'سقف دستگاه بر اساس IPهای مبدأ متفاوتِ {n} دقیقهٔ اخیر شمرده می‌شود (پورت‌ها نادیده گرفته می‌شوند).',
  'The device limit counts distinct ip:port endpoints seen in the last {n} minutes.':
    'سقف دستگاه بر اساس endpointهای (IP و پورت) متفاوتِ {n} دقیقهٔ اخیر شمرده می‌شود.',
  'Possibly shared': 'احتمالاً اشتراکی',
  'Active from {n} or more different networks within {m} minutes. This is a heuristic: roaming between mobile networks can look like sharing, and users behind carrier-grade NAT can hide it.':
    'در {m} دقیقه از {n} شبکهٔ مختلف یا بیشتر وصل بوده است. این فقط یک حدس است: جابه‌جایی بین شبکه‌های موبایل شبیه اشتراک‌گذاری دیده می‌شود و کاربرانِ پشت CGNAT ممکن است اصلاً دیده نشوند.',
  'Networks in the last {m} minutes': 'شبکه‌ها در {m} دقیقهٔ اخیر',
  'Torrent attempts': 'تلاش برای تورنت',
  '{a} blocked packets in the last 24 hours, {b} in the last 30 days. Counts are packets, not attempts: one blocked connection is retried several times.':
    '{a} بستهٔ مسدودشده در ۲۴ ساعت گذشته و {b} بسته در ۳۰ روز گذشته. شمارش بر اساس بسته است نه تعداد تلاش: یک اتصال مسدودشده چند بار دوباره تلاش می‌شود.',

  // Notifications
  'Account may be shared': 'احتمال اشتراک‌گذاری اکانت',
  'Torrent traffic blocked': 'ترافیک تورنت مسدود شد',
  'Account "{label}" is active from {n} different networks at once, so it may be shared.':
    'اکانت «{label}» هم‌زمان از {n} شبکهٔ مختلف وصل است و احتمالاً بین چند نفر دست‌به‌دست می‌شود.',
  'Account "{label}" tried to use BitTorrent ({n} blocked packets).': 'اکانت «{label}» تلاش کرده از تورنت استفاده کند ({n} بسته مسدود شد).',
  'The account was suspended.': 'اکانت تعلیق شد.',

  // Settings card
  'Abuse controls': 'کنترل سوءاستفاده',
  'Clean up dead accounts, catch shared accounts and block torrents. Everything is off until you turn it on.':
    'اکانت‌های مرده را پاک کنید، اکانت‌های اشتراکی را پیدا کنید و تورنت را ببندید. تا وقتی روشن نکنید همه‌چیز خاموش است.',
  'Abuse settings saved': 'تنظیمات کنترل سوءاستفاده ذخیره شد',
  'Could not load the abuse settings.': 'بارگذاری تنظیمات کنترل سوءاستفاده ممکن نشد.',
  'Delete long-dead accounts automatically': 'حذف خودکار اکانت‌های مدت‌ها مرده',
  'Deletes accounts that have been expired or out of data, and suspended because of it, for the number of days below. Accounts you suspended yourself, and accounts suspended for abuse, a device limit or torrents, are never deleted. Each deletion is written to the audit log.':
    'اکانت‌هایی را که به‌خاطر اتمام زمان یا حجم تعلیق شده‌اند و به تعداد روز زیر در همین حال مانده‌اند حذف می‌کند. اکانت‌هایی که خودتان تعلیق کرده‌اید یا به‌خاطر سوءاستفاده، سقف دستگاه یا تورنت تعلیق شده‌اند هرگز حذف نمی‌شوند. هر حذف در گزارش حسابرسی ثبت می‌شود.',
  'After (days)': 'بعد از (روز)',
  'Device limit counts': 'سقف دستگاه چه چیزی را می‌شمارد',
  'Distinct endpoints (IP and port) is the strictest and can count one phone twice when the carrier changes its port. Distinct IPs ignores ports, so several devices behind one home router count once; carrier-grade NAT can still make strangers share an IP, and a roaming phone can look like two.':
    'شمارش endpoint (IP و پورت) سخت‌گیرانه‌ترین حالت است و وقتی اپراتور پورت را عوض کند یک گوشی را دو بار می‌شمارد. شمارش IP پورت را نادیده می‌گیرد و چند دستگاه پشت یک مودم خانگی یک دستگاه حساب می‌شوند؛ ولی با CGNAT ممکن است چند غریبه یک IP داشته باشند و گوشیِ در حال جابه‌جایی هم گاهی دو دستگاه به نظر برسد.',
  Count: 'شمارش',
  'Distinct endpoints (IP:port)': 'endpointهای متفاوت (IP:پورت)',
  'Distinct IPs': 'IPهای متفاوت',
  'Within the last (minutes)': 'در (دقیقهٔ) اخیر',
  'Detect shared accounts': 'تشخیص اکانت‌های اشتراکی',
  'Flags an account that is active from several different networks (IPv4 /24 or IPv6 /48) at the same time, shows a "possibly shared" badge and sends the account.sharing_suspected event to your channels and webhooks. It is a hint, never an automatic suspension.':
    'اکانتی را که هم‌زمان از چند شبکهٔ مختلف (IPv4 با /24 یا IPv6 با /48) وصل است علامت می‌زند، نشان «احتمالاً اشتراکی» می‌گذارد و رویداد account.sharing_suspected را به کانال‌ها و وب‌هوک‌های شما می‌فرستد. فقط یک هشدار است و هیچ‌وقت اکانت را خودکار تعلیق نمی‌کند.',
  'Networks that trigger it': 'تعداد شبکه برای فعال شدن هشدار',
  'Block torrents on nodes': 'مسدود کردن تورنت روی نودها',
  'Nodes drop packets that carry plain BitTorrent markers (peer handshake, DHT, tracker requests). Encrypted or obfuscated BitTorrent is not detected - most clients can use it, and many fall back to it when the plain handshake is dropped - so this stops casual use and gives you a signal, not a guarantee. It inspects the start of every forwarded packet, which costs some CPU on busy nodes; needs the xt_string kernel module on the node.':
    'نودها بسته‌هایی را که نشانه‌های ساده‌ی بیت‌تورنت دارند (دست‌دادن همتا، DHT، درخواست ترکر) دور می‌ریزند. بیت‌تورنتِ رمزشده یا مبهم‌شده تشخیص داده نمی‌شود؛ بیشتر کلاینت‌ها می‌توانند از آن استفاده کنند و وقتی دست‌دادن ساده مسدود شود اغلب به همان برمی‌گردند. پس این قابلیت جلوی استفادهٔ معمولی را می‌گیرد و یک نشانه به شما می‌دهد، نه تضمین. ابتدای هر بسته‌ای که از نود عبور می‌کند بررسی می‌شود و روی نودهای شلوغ کمی CPU مصرف می‌کند؛ ماژول xt_string باید روی نود موجود باشد.',
  'Suspend accounts that keep trying': 'تعلیق اکانت‌هایی که مدام تلاش می‌کنند',
  'Blocked packets': 'بستهٔ مسدودشده',
  'Counts packets, not attempts: one blocked connection is retried several times.': 'شمارش بر اساس بسته است نه تعداد تلاش: یک اتصال مسدودشده چند بار دوباره تلاش می‌شود.',
  'Per node': 'برای هر نود',
  Blocking: 'مسدود می‌کند',
  'Not blocking': 'مسدود نمی‌کند',
  'Torrent blocking on {name}': 'مسدودسازی تورنت روی {name}',
  'Use the default above': 'همان پیش‌فرض بالا',
  'Always block': 'همیشه مسدود کن',
  'Never block': 'هیچ‌وقت مسدود نکن',
  'Only a super admin can change these settings.': 'فقط سوپرادمین می‌تواند این تنظیمات را تغییر دهد.',
}
