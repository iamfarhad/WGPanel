export const security: Record<string, string> = {
  // Login
  'Too many failed attempts. Try again in {minutes} min.': 'تعداد تلاش‌های ناموفق زیاد بود. {minutes} دقیقه دیگر دوباره امتحان کنید.',
  'Verification failed': 'تأیید ناموفق بود',
  'Passkey sign-in failed': 'ورود با کلید عبور ناموفق بود',
  'Authenticator code': 'کد برنامه احراز هویت',
  Passkey: 'کلید عبور',
  'Recovery code': 'کد بازیابی',
  'Two-step verification': 'تأیید دومرحله‌ای',
  'Confirm it is you to finish signing in.': 'برای تکمیل ورود، هویت خود را تأیید کنید.',
  'Enter the 6-digit code from your authenticator app.': 'کد ۶ رقمی را از برنامه احراز هویت خود وارد کنید.',
  'Each recovery code works once. Use one if you lost access to your authenticator.':
    'هر کد بازیابی فقط یک بار کار می‌کند. اگر به برنامه احراز هویت دسترسی ندارید، از یکی از آن‌ها استفاده کنید.',
  'Use your passkey (fingerprint, face, screen lock or security key) to continue.':
    'برای ادامه از کلید عبور خود (اثر انگشت، چهره، قفل صفحه یا کلید امنیتی) استفاده کنید.',
  'Verifying…': 'در حال تأیید…',
  'Use passkey': 'استفاده از کلید عبور',
  Verify: 'تأیید',
  'Back to sign in': 'بازگشت به ورود',
  or: 'یا',
  'Sign in with a passkey': 'ورود با کلید عبور',

  // API error codes
  'Too many failed attempts. Try again later.': 'تعداد تلاش‌های ناموفق زیاد بود. کمی بعد دوباره امتحان کنید.',
  'That code is not valid. Check it and try again.': 'این کد معتبر نیست. دوباره بررسی کنید و تلاش کنید.',
  'The verification step expired. Sign in again.': 'مهلت مرحله تأیید تمام شد. دوباره وارد شوید.',
  'Admin access is not permitted from your IP address.': 'دسترسی ادمین از این آدرس IP مجاز نیست.',
  'That list would block your own IP address. Add it first.': 'با این فهرست، آدرس IP خودتان هم مسدود می‌شود. ابتدا آن را اضافه کنید.',
  'The passkey could not be verified.': 'تأیید کلید عبور ممکن نشد.',
  'Passkeys need the panel to be opened over HTTPS on its configured domain.':
    'برای استفاده از کلید عبور باید پنل را با HTTPS و از طریق دامنه تنظیم‌شده‌اش باز کنید.',
  'The passkey step expired. Try again.': 'مهلت مرحله کلید عبور تمام شد. دوباره تلاش کنید.',
  'This passkey is already registered.': 'این کلید عبور قبلاً ثبت شده است.',
  'You have reached the maximum number of passkeys.': 'به حداکثر تعداد مجاز کلید عبور رسیده‌اید.',
  'That passkey no longer exists.': 'این کلید عبور دیگر وجود ندارد.',
  'This account has no passkeys.': 'این حساب کلید عبوری ندارد.',
  'Two-factor authentication is already enabled.': 'تأیید دومرحله‌ای از قبل فعال است.',
  'There is no pending two-factor setup. Start again.': 'راه‌اندازی تأیید دومرحله‌ای در جریان نیست. از اول شروع کنید.',
  'Two-factor authentication is not enabled.': 'تأیید دومرحله‌ای فعال نیست.',

  // My security page
  Security: 'امنیت',
  'Protect your sign-in with a second step: an authenticator app, passkeys, or both.':
    'ورود خود را با یک مرحله دوم محافظت کنید: برنامه احراز هویت، کلید عبور، یا هر دو.',
  'Could not load your security settings.': 'بارگذاری تنظیمات امنیتی شما ممکن نشد.',
  'Authenticator app (TOTP)': 'برنامه احراز هویت (TOTP)',
  'A 6-digit code from an app such as Google Authenticator, Aegis or 1Password is asked for after your password.':
    'بعد از رمز عبور، یک کد ۶ رقمی از برنامه‌ای مثل Google Authenticator، Aegis یا 1Password پرسیده می‌شود.',
  Enabled: 'فعال',
  Off: 'خاموش',
  'Recovery codes left: {n}. Each one can be used once if you lose access to your authenticator.':
    'کدهای بازیابی باقی‌مانده: {n}. اگر به برنامه احراز هویت دسترسی نداشتید، هر کدام را یک بار می‌توانید استفاده کنید.',
  'New recovery codes': 'کدهای بازیابی جدید',
  'Turn off': 'خاموش کردن',
  'Set up authenticator app': 'راه‌اندازی برنامه احراز هویت',
  Copied: 'کپی شد',
  Copy: 'کپی',
  Download: 'دانلود',
  'Two-factor authentication is on': 'تأیید دومرحله‌ای فعال شد',
  'Save your recovery codes': 'کدهای بازیابی را ذخیره کنید',
  'Keep these codes somewhere safe. Each works once if you lose your authenticator, and they will not be shown again.':
    'این کدها را جای امنی نگه دارید. اگر برنامه احراز هویت را از دست بدهید هر کدام یک بار کار می‌کند و دیگر نمایش داده نمی‌شوند.',
  Done: 'تمام',
  'Scan this QR code with your authenticator app, then enter the 6-digit code it shows.':
    'این QR را با برنامه احراز هویت اسکن کنید و سپس کد ۶ رقمی نمایش‌داده‌شده را وارد کنید.',
  'Authenticator QR code': 'QR برنامه احراز هویت',
  'Cannot scan? Enter this key manually:': 'اسکن نمی‌شود؟ این کلید را دستی وارد کنید:',
  'Verification code': 'کد تأیید',
  'Turn on': 'روشن کردن',
  'Two-factor authentication is off': 'تأیید دومرحله‌ای خاموش شد',
  'Your new recovery codes': 'کدهای بازیابی جدید شما',
  'The old codes no longer work. Save these now - they will not be shown again.':
    'کدهای قبلی دیگر کار نمی‌کنند. همین حالا این‌ها را ذخیره کنید، دوباره نمایش داده نمی‌شوند.',
  'Turn off two-factor authentication': 'خاموش کردن تأیید دومرحله‌ای',
  'Confirm with your password and a current authenticator code (or an unused recovery code).':
    'با رمز عبور و یک کد فعلی برنامه احراز هویت (یا یک کد بازیابی استفاده‌نشده) تأیید کنید.',
  'Authenticator or recovery code': 'کد برنامه احراز هویت یا کد بازیابی',
  Generate: 'ساخت',
  Passkeys: 'کلیدهای عبور',
  'Sign in with your fingerprint, face, screen lock or a security key. A passkey also works as your second step after a password.':
    'با اثر انگشت، چهره، قفل صفحه یا کلید امنیتی وارد شوید. کلید عبور بعد از رمز عبور هم می‌تواند مرحله دوم ورود باشد.',
  'Add passkey': 'افزودن کلید عبور',
  'This browser or connection does not support passkeys. Open the panel over HTTPS in a recent browser to use them.':
    'این مرورگر یا اتصال از کلید عبور پشتیبانی نمی‌کند. برای استفاده، پنل را با HTTPS و در یک مرورگر به‌روز باز کنید.',
  'No passkeys yet': 'هنوز کلید عبوری ندارید',
  'Add one to sign in without typing a password.': 'یکی اضافه کنید تا بدون تایپ رمز عبور وارد شوید.',
  'Added {date}': 'افزوده‌شده در {date}',
  'Last used {date}': 'آخرین استفاده: {date}',
  'Never used': 'هرگز استفاده نشده',
  Remove: 'حذف',
  'Passkey removed': 'کلید عبور حذف شد',
  'Remove passkey': 'حذف کلید عبور',
  '"{name}" will no longer work for signing in. This cannot be undone.': 'کلید «{name}» دیگر برای ورود کار نمی‌کند و برگشتی ندارد.',
  Name: 'نام',
  'Something that tells you which device or key this is, e.g. Work laptop.': 'نامی که نشان دهد این کلید مال کدام دستگاه است، مثلاً لپ‌تاپ کار.',
  'Work laptop': 'لپ‌تاپ کار',
  'Passkey added': 'کلید عبور اضافه شد',
  'Could not add the passkey': 'افزودن کلید عبور ممکن نشد',
  'Waiting for your device…': 'در انتظار دستگاه شما…',
  Continue: 'ادامه',

  // Settings -> Security
  'Security settings saved': 'تنظیمات امنیتی ذخیره شد',
  'Failed to save security settings': 'ذخیره تنظیمات امنیتی ناموفق بود',
  'Sign-in lockout and which IP addresses may reach the admin panel.':
    'قفل شدن ورود و اینکه از کدام آدرس‌های IP می‌شود به پنل ادمین دسترسی داشت.',
  'Could not load security settings.': 'بارگذاری تنظیمات امنیتی ممکن نشد.',
  'Failed sign-ins before lockout': 'تعداد ورود ناموفق تا قفل شدن',
  'Counted per username, and per IP address at four times this number.':
    'برای هر نام کاربری شمرده می‌شود و برای هر آدرس IP چهار برابر این عدد.',
  'Lockout (minutes)': 'مدت قفل (دقیقه)',
  'Doubles each time the same username or IP is locked again within 24 hours.':
    'اگر همان نام کاربری یا IP ظرف ۲۴ ساعت دوباره قفل شود، مدت قفل دو برابر می‌شود.',
  'Admin IP allowlist': 'فهرست IPهای مجاز ادمین',
  'One IP address or CIDR range per line, e.g. 203.0.113.7 or 10.0.0.0/8. Leave empty to allow any address. It limits admin sign-in and the admin panel only - subscription links, node agents and API keys are not affected. Locked out? Run':
    'در هر خط یک آدرس IP یا محدوده CIDR بنویسید، مثل 203.0.113.7 یا 10.0.0.0/8. خالی بگذارید تا همه آدرس‌ها مجاز باشند. این محدودیت فقط روی ورود ادمین و پنل مدیریت اثر دارد و لینک‌های اشتراک، ایجنت نودها و کلیدهای API را تغییر نمی‌دهد. دسترسی‌تان بسته شد؟ این دستور را اجرا کنید:',
  'on the server.': 'روی سرور.',
  'Your current IP address:': 'آدرس IP فعلی شما:',
  'Save security settings': 'ذخیره تنظیمات امنیتی',

  // Admin users
  'Two-factor': 'تأیید دومرحله‌ای',
  Authenticator: 'برنامه احراز هویت',
  '{n} passkey(s)': '{n} کلید عبور',
  'Reset two-factor': 'بازنشانی تأیید دومرحله‌ای',
  'Reset two-factor authentication': 'بازنشانی تأیید دومرحله‌ای',
  'This removes the authenticator app, recovery codes and passkeys of "{name}". They can sign in with just their password until they set a new second step.':
    'برنامه احراز هویت، کدهای بازیابی و کلیدهای عبور «{name}» حذف می‌شود. تا زمانی که مرحله دوم جدیدی تنظیم نکند، فقط با رمز عبور می‌تواند وارد شود.',
  Reset: 'بازنشانی',
  'Two-factor authentication reset for {name}': 'تأیید دومرحله‌ای {name} بازنشانی شد',
  'Failed to reset two-factor authentication': 'بازنشانی تأیید دومرحله‌ای ناموفق بود',
}
