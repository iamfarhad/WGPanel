/** Persian strings for AmneziaWG obfuscation profiles (pages/ObfuscationPage.tsx,
 * components/obfuscation/*, the node form/badge and the public subscription page).
 * Keys are the English source text. Technical names (AmneziaWG, AWG, Jc, H1...) stay
 * in Latin script. */
export const obfuscation: Record<string, string> = {
  // Navigation and page
  Obfuscation: 'مبهم‌سازی',
  "AmneziaWG profiles that make a node's traffic harder to fingerprint. Assign a profile to a node (or a whole node group) and re-import the configs on the clients.":
    'پروفایل‌های AmneziaWG ترافیک نود را سخت‌تر قابل شناسایی می‌کنند. یک پروفایل را به یک نود (یا کل یک گروه نود) بدهید و کانفیگ را روی دستگاه کاربرها دوباره وارد کنید.',
  'New profile': 'پروفایل جدید',
  'No obfuscation profiles yet': 'هنوز پروفایل مبهم‌سازی‌ای نیست',
  'Plain WireGuard can be blocked by deep packet inspection. Create a profile from a preset and assign it to a node.':
    'وایرگاردِ ساده با بازرسی عمیق بسته‌ها (DPI) مسدود می‌شود. یک پروفایل از روی پیش‌تنظیم بسازید و به یک نود بدهید.',
  'Could not load profiles.': 'بارگذاری پروفایل‌ها ممکن نشد.',
  Compatibility: 'سازگاری',
  Revision: 'نسخه',
  Updated: 'آخرین تغییر',
  'Stock clients OK': 'کلاینت معمولی هم وصل می‌شود',
  '{applied} of {total} running': '{applied} از {total} نود اعمال شده',
  'Rotate parameters': 'چرخاندن پارامترها',
  'Assign to node group': 'اختصاص به گروه نود',
  'Delete profile?': 'پروفایل حذف شود؟',
  'The profile is removed. A profile that is still assigned to nodes cannot be deleted - unassign it first.':
    'پروفایل حذف می‌شود. پروفایلی که هنوز به نودی داده شده را نمی‌شود حذف کرد؛ اول آن را از نودها بردارید.',
  'Profile deleted': 'پروفایل حذف شد',
  'Failed to delete profile': 'حذف پروفایل انجام نشد',

  // Create / edit
  'New obfuscation profile': 'پروفایل مبهم‌سازی جدید',
  'Start from': 'شروع از',
  'Custom parameters': 'پارامترهای دلخواه',
  'Fresh random values are generated when the profile is created. Use Rotate later to regenerate them.':
    'مقدارهای تصادفی تازه هنگام ساخت پروفایل تولید می‌شوند. بعداً می‌توانید با «چرخاندن» دوباره بسازیدشان.',
  'AWG 1.x works with every AmneziaWG app. AWG 2.0 adds more fields but needs a recent app version.':
    'AWG 1.x با همه‌ی اپ‌های AmneziaWG کار می‌کند. AWG 2.0 فیلدهای بیشتری دارد اما به نسخه‌ی جدید اپ نیاز دارد.',
  'Jc junk packets of Jmin to Jmax bytes are sent before each handshake. S1 and S2 pad the handshake messages (S1 + 56 must not equal S2).':
    'پیش از هر هندشیک، Jc بسته‌ی بی‌مصرف با اندازه‌ی Jmin تا Jmax بایت فرستاده می‌شود. S1 و S2 به پیام‌های هندشیک بایت اضافه می‌کنند (S1 + 56 نباید با S2 برابر باشد).',
  'H1-H4: leave all empty, or give four non-overlapping numbers or ranges (A-B), each at least 5.':
    'H1 تا H4: یا همه را خالی بگذارید، یا چهار عدد یا بازه‌ی (A-B) بدون هم‌پوشانی بدهید که هر کدام دست‌کم ۵ باشد.',
  'H1-H4: leave all empty, or give four different numbers, each at least 5.':
    'H1 تا H4: یا همه را خالی بگذارید، یا چهار عدد متفاوت بدهید که هر کدام دست‌کم ۵ باشد.',
  'Custom signature packet sent before the handshake, e.g. <b 0xc0><r 40><t>. Tags: <b 0xHEX>, <r N>, <rc N>, <rd N>, <t>.':
    'بسته‌ی امضای دلخواه که پیش از هندشیک فرستاده می‌شود، مثلاً <b 0xc0><r 40><t>. تگ‌ها: <b 0xHEX>، <r N>، <rc N>، <rd N>، <t>.',
  'This profile is used by {n} node(s). Changing its parameters makes those nodes reconfigure, and clients must re-import their configs.':
    'این پروفایل روی {n} نود استفاده می‌شود. با تغییر پارامترها آن نودها خودشان را دوباره پیکربندی می‌کنند و کاربرها باید کانفیگ جدید را وارد کنند.',
  'Profile created': 'پروفایل ساخته شد',
  'Failed to save profile': 'ذخیره‌ی پروفایل انجام نشد',
  'Create profile': 'ساخت پروفایل',

  // Presets (names and descriptions come from the API)
  'Compatible (junk only)': 'سازگار (فقط بسته‌ی بی‌مصرف)',
  'Random junk packets before each handshake only. Stock WireGuard clients keep working; AWG apps add the junk.':
    'فقط چند بسته‌ی تصادفی پیش از هر هندشیک. کلاینت‌های معمولی وایرگارد همچنان وصل می‌شوند؛ اپ‌های AWG این بسته‌ها را اضافه می‌کنند.',
  'AWG 1.x (full)': 'AWG 1.x (کامل)',
  'Junk, padding and custom message headers. Every AWG app supports it; stock WireGuard clients cannot connect.':
    'بسته‌ی بی‌مصرف، بایت اضافه و هدر دلخواه پیام‌ها. همه‌ی اپ‌های AWG پشتیبانی می‌کنند؛ کلاینت معمولی وایرگارد نمی‌تواند وصل شود.',
  'AWG 2.0 (QUIC-like)': 'AWG 2.0 (شبیه QUIC)',
  'AWG 1.x plus transport padding, header ranges and a QUIC Initial lookalike signature packet. Needs a current AWG app.':
    'AWG 1.x به‌علاوه‌ی بایت اضافه روی داده‌ها، بازه‌ی هدر و یک بسته‌ی امضا شبیه QUIC Initial. به نسخه‌ی جدید اپ AWG نیاز دارد.',
  'AWG 2.0 (DNS-like)': 'AWG 2.0 (شبیه DNS)',
  'AWG 1.x plus transport padding, header ranges and a DNS query lookalike signature packet. Needs a current AWG app.':
    'AWG 1.x به‌علاوه‌ی بایت اضافه روی داده‌ها، بازه‌ی هدر و یک بسته‌ی امضا شبیه کوئری DNS. به نسخه‌ی جدید اپ AWG نیاز دارد.',

  // Rotate
  'Rotate {name}': 'چرخاندن {name}',
  '{name} rotated': '{name} چرخانده شد',
  'Failed to rotate profile': 'چرخاندن پروفایل انجام نشد',
  'Fresh random parameters replace the current ones. Nodes using this profile reconfigure on their next heartbeat, and every client config issued for them stops working until the user imports the new config.':
    'پارامترهای تصادفی تازه جای پارامترهای فعلی را می‌گیرند. نودهایی که از این پروفایل استفاده می‌کنند در هارت‌بیت بعدی خودشان را دوباره پیکربندی می‌کنند و تا وقتی کاربر کانفیگ جدید را وارد نکند، هیچ‌کدام از کانفیگ‌های قبلی کار نمی‌کند.',
  'Signature packets (I1-I5)': 'بسته‌های امضا (I1 تا I5)',
  'Keep the current ones': 'همین‌هایی که هست بماند',
  'Replace I1 with a QUIC-like packet': 'I1 با یک بسته‌ی شبیه QUIC جایگزین شود',
  'Replace I1 with a DNS-like packet': 'I1 با یک بسته‌ی شبیه DNS جایگزین شود',
  'Remove them': 'حذف شوند',

  // Assign to a group
  'Assign {name} to a node group': 'اختصاص {name} به یک گروه نود',
  'Remove the profile from this group instead (back to plain WireGuard)':
    'به‌جای آن، پروفایل از این گروه برداشته شود (بازگشت به وایرگارد ساده)',
  'Every node in the group switches on its next heartbeat. Clients need an AmneziaWG app and a re-imported config.':
    'همه‌ی نودهای گروه در هارت‌بیت بعدی تغییر می‌کنند. کاربرها به اپ AmneziaWG و کانفیگ تازه نیاز دارند.',
  '{n} node(s) updated': '{n} نود به‌روز شد',
  'Failed to assign profile': 'اختصاص پروفایل انجام نشد',
  Assign: 'اختصاص بده',

  // Node list and edit form
  AmneziaWG: 'AmneziaWG',
  'AWG pending': 'AWG در انتظار',
  'Obfuscation profile': 'پروفایل مبهم‌سازی',
  'None (plain WireGuard)': 'هیچ (وایرگارد ساده)',
  'Nodes with a profile run AmneziaWG instead of plain WireGuard. Clients need an AmneziaWG app and a re-imported config.':
    'نودی که پروفایل دارد به‌جای وایرگارد ساده با AmneziaWG اجرا می‌شود. کاربرها به اپ AmneziaWG و کانفیگ تازه نیاز دارند.',
  'Not applied yet: {error}': 'هنوز اعمال نشده: {error}',
  'Assigned, waiting for the node to apply it (next heartbeat).':
    'اختصاص داده شد؛ منتظر اعمال روی نود (هارت‌بیت بعدی).',
  'This node cannot run AmneziaWG yet - its container needs the /dev/net/tun device and the current node image.':
    'این نود هنوز نمی‌تواند AmneziaWG اجرا کند؛ کانتینر آن به دستگاه /dev/net/tun و ایمیج جدید نود نیاز دارد.',
  'Running {profile} (revision {rev}).': '{profile} (نسخه‌ی {rev}) در حال اجراست.',

  // Public subscription page
  '1. Install the AmneziaWG app': '۱. اپ AmneziaWG را نصب کنید',
  'This server uses AmneziaWG. The regular WireGuard app cannot connect to it - use one of these apps instead.':
    'این سرور از AmneziaWG استفاده می‌کند. اپ معمولی وایرگارد به آن وصل نمی‌شود؛ یکی از این اپ‌ها را نصب کنید.',
  'Only install the apps from the links above.': 'اپ‌ها را فقط از لینک‌های بالا نصب کنید.',
  'Needs a recent app version (AWG 2.0)': 'به نسخه‌ی جدید اپ نیاز دارد (AWG 2.0)',
  '2. Add your configuration': '۲. کانفیگ خود را اضافه کنید',
  'Open the AmneziaWG (or AmneziaVPN) app and choose to add a tunnel from a file or a QR code.':
    'اپ AmneziaWG (یا AmneziaVPN) را باز کنید و افزودن تونل از فایل یا QR کد را بزنید.',
  'Scan the QR code above, or import the downloaded config file.':
    'QR کد بالا را اسکن کنید یا فایل کانفیگ دانلودشده را وارد کنید.',
  'Turn the tunnel on. If you were given a new link or config, delete the old tunnel first.':
    'تونل را روشن کنید. اگر لینک یا کانفیگ جدید گرفته‌اید، اول تونل قبلی را حذف کنید.',
  'On a phone, open the AmneziaWG app and scan this QR code. On a computer, download the file and import it into the app.':
    'روی گوشی اپ AmneziaWG را باز کنید و این QR کد را اسکن کنید. روی کامپیوتر فایل را دانلود کنید و در اپ وارد کنید.',

  // Notification
  'Obfuscation profile rotated': 'پروفایل مبهم‌سازی چرخانده شد',
  'Profile "{name}" was rotated to revision {rev}. Clients of its nodes must re-import their configs.':
    'پروفایل «{name}» به نسخه‌ی {rev} چرخانده شد. کاربرهای نودهای آن باید کانفیگ جدید را وارد کنند.',
}
