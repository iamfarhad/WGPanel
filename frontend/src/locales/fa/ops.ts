export const ops: Record<string, string> = {
  // Node maintenance
  Maintenance: 'نگهداری',
  'In maintenance since {date}': 'در حالت نگهداری از {date}',
  'Start maintenance': 'شروع نگهداری',
  'End maintenance': 'پایان نگهداری',
  '{name} is now in maintenance': '{name} وارد حالت نگهداری شد',
  '{name} is back in rotation': '{name} دوباره وارد چرخه شد',
  'Could not change maintenance mode': 'تغییر حالت نگهداری ممکن نشد',
  'That node is in maintenance and takes no new accounts.': 'این نود در حالت نگهداری است و اکانت جدید نمی‌گیرد.',
  'Node maintenance started': 'نگهداری نود شروع شد',
  'Node maintenance ended': 'نگهداری نود تمام شد',
  'Node "{node}" is in maintenance: no new peers or steering until it ends.':
    'نود «{node}» در حالت نگهداری است؛ تا پایان آن اکانت جدید روی آن ساخته نمی‌شود و ترافیک به آن هدایت نمی‌شود.',
  'Node "{node}" is back in rotation.': 'نود «{node}» دوباره وارد چرخه شد.',

  // Refresh-token reuse
  'Session token reuse detected': 'استفادهٔ مجدد از توکن نشست شناسایی شد',
  'A refresh token of admin "{admin}" was replayed after rotation. All their sessions from that login were revoked.':
    'یک توکن نشست قدیمی ادمین «{admin}» دوباره استفاده شد؛ همهٔ نشست‌های آن ورود لغو شد. در صورت نیاز رمز را عوض کنید.',

  // SSH provisioning
  'Install over SSH': 'نصب از طریق SSH',
  'Install a node over SSH': 'نصب نود از طریق SSH',
  'The panel creates the node, then connects to the server over SSH and runs the installer for you. The login is used once, kept in memory only and discarded when the job ends.':
    'پنل نود را می‌سازد، با SSH به سرور وصل می‌شود و نصب‌کننده را برایتان اجرا می‌کند. اطلاعات ورود فقط یک‌بار استفاده می‌شود، فقط در حافظه می‌ماند و با پایان کار دور ریخته می‌شود.',
  'Retrying with the node created by the previous attempt.': 'تلاش دوباره با نودی که تلاش قبلی ساخته بود.',
  'host:port clients connect to, e.g. vpn1.example.com:51820': 'host:port که کلاینت‌ها به آن وصل می‌شوند، مثل vpn1.example.com:51820',
  'SSH host': 'هاست SSH',
  'SSH port': 'پورت SSH',
  'SSH username': 'نام کاربری SSH',
  'Login method': 'روش ورود',
  'Private key': 'کلید خصوصی',
  'SSH password': 'رمز SSH',
  'Also used for sudo when the user is not root.': 'اگر کاربر root نباشد، برای sudo هم از همین رمز استفاده می‌شود.',
  'Paste the OpenSSH/PEM private key.': 'کلید خصوصی OpenSSH یا PEM را اینجا بچسبانید.',
  'Key passphrase': 'عبارت عبور کلید',
  '(if the key is encrypted)': '(اگر کلید رمزگذاری شده است)',
  'Contacting the server…': 'در حال اتصال به سرور…',
  'Check host key': 'بررسی اثر انگشت سرور',
  'Confirm that this is the fingerprint of the server you mean to install on (on the server: ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub). If it differs, someone may be intercepting the connection.':
    'مطمئن شوید این اثر انگشت مال همان سروری است که می‌خواهید روی آن نصب کنید (روی سرور: ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub). اگر فرق دارد، ممکن است کسی در مسیر اتصال شنود می‌کند.',
  Back: 'بازگشت',
  'Starting…': 'در حال شروع…',
  'Fingerprint matches, install': 'اثر انگشت یکی است، نصب کن',
  Connecting: 'اتصال',
  'Checking the server': 'بررسی سرور',
  'Uploading the installer': 'ارسال نصب‌کننده',
  Installing: 'نصب',
  'Installed. The node registers with the panel on its own and shows as online in a few seconds.':
    'نصب شد. نود خودش به پنل ثبت می‌شود و چند ثانیهٔ دیگر آنلاین دیده می‌شود.',
  'Failed.': 'ناموفق بود.',
  'Try again': 'تلاش دوباره',
  'The server could not be reached over SSH. Check the host, port and firewall.':
    'اتصال SSH به سرور برقرار نشد. هاست، پورت و فایروال را بررسی کنید.',
  "The server's host key changed after you confirmed it. The install was aborted: possible interception.":
    'اثر انگشت سرور بعد از تأیید شما عوض شد. نصب متوقف شد؛ احتمال شنود وجود دارد.',
  'SSH login failed: wrong username, password or key (or the key needs its passphrase).':
    'ورود SSH ناموفق بود: نام کاربری، رمز یا کلید اشتباه است (یا کلید عبارت عبور می‌خواهد).',
  'This user is not root and cannot use sudo. Log in as root, or as a user with sudo (passwordless, or with the login password).':
    'این کاربر root نیست و sudo ندارد. با root وارد شوید یا با کاربری که sudo دارد (بدون رمز، یا با همان رمز ورود).',
  'Unsupported operating system: only Debian and Ubuntu are supported.': 'سیستم‌عامل پشتیبانی نمی‌شود؛ فقط دبیان و اوبونتو پشتیبانی می‌شوند.',
  'Docker is not installed and the installer could not install it.': 'داکر نصب نیست و نصب‌کننده هم نتوانست آن را نصب کند.',
  'The installer rejected its settings. See the log.': 'نصب‌کننده تنظیمات را نپذیرفت. لاگ را ببینید.',
  'The panel rejected the join token. Retry the install.': 'پنل توکن اتصال را نپذیرفت. نصب را دوباره انجام دهید.',
  'The installer failed. See the log for details.': 'نصب ناموفق بود. جزئیات در لاگ است.',
  'The installation took too long and was stopped.': 'نصب بیش از حد طول کشید و متوقف شد.',
  'The installation was cancelled.': 'نصب لغو شد.',
  'The panel restarted while the installation was running.': 'پنل وسط نصب دوباره راه‌اندازی شد.',
  'Set the panel domain in Settings first: the node needs an address to reach the panel.':
    'اول دامنهٔ پنل را در تنظیمات بگذارید؛ نود برای وصل شدن به پنل به یک آدرس نیاز دارد.',
  'This node is already installed.': 'این نود قبلاً نصب شده است.',
  'Too many installations are running. Wait for one to finish.': 'نصب‌های زیادی در حال اجراست. صبر کنید یکی تمام شود.',
}
