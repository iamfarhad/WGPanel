/** Persian for sub-resellers ("My resellers", the reseller tree) and branding (the
 * editor, the settings card and the pieces of the public subscription page it drives).
 * Keys are the English source text; strings that already exist in resellers.ts,
 * lifecycle.ts, security.ts etc. are not repeated here. */
export const branding: Record<string, string> = {
  // Sub-resellers: nav, page and tree
  'My resellers': 'نماینده‌های من',
  'Sub-resellers you sell through. Their plans come out of yours.': 'نماینده‌های زیرمجموعه‌ای که از طریق آن‌ها می‌فروشید. سقف آن‌ها از سقف خودتان کم می‌شود.',
  'Sub-resellers': 'نماینده‌های زیرمجموعه',
  'New sub-reseller': 'زیرمجموعه‌ی جدید',
  'No sub-resellers yet': 'هنوز زیرمجموعه‌ای ندارید',
  'Add a sub-reseller to let an agent of yours sell accounts out of your plan.':
    'یک زیرمجموعه اضافه کنید تا از محل سقف شما اکانت بفروشد.',
  'You can create up to {n} levels below you': 'تا {n} سطح زیر خودتان می‌توانید نماینده بسازید',
  'Deepest level - no more sub-resellers': 'پایین‌ترین سطح هستید و دیگر نمی‌توانید زیرمجموعه بسازید',
  'Accounts left to give': 'اکانت قابل واگذاری',
  'Traffic credit left to give': 'اعتبار ترافیک قابل واگذاری',
  '{n} reserved for sub-resellers': '{n} برای زیرمجموعه‌ها کنار گذاشته شده',
  '{amount} reserved for sub-resellers': '{amount} برای زیرمجموعه‌ها کنار گذاشته شده',
  '{n} sub-resellers': '{n} زیرمجموعه',
  'parent disabled': 'والد غیرفعال است',
  "Every active account of {name} and of its sub-resellers stops working until you reactivate them. They can't re-enable them.":
    'همه‌ی اکانت‌های فعال {name} و زیرمجموعه‌هایش تا وقتی دوباره فعالشان نکنید از کار می‌افتند و خودشان هم نمی‌توانند فعالشان کنند.',
  'Accounts you suspended this way come back. Accounts that have since expired or used up their quota stay suspended.':
    'اکانت‌هایی که این‌طور تعلیق کرده بودید برمی‌گردند. اکانتی که در این فاصله منقضی شده یا حجمش تمام شده باشد همچنان معلق می‌ماند.',
  '{name} and every sub-reseller below it will be deleted. This only works when none of them owns accounts.':
    '{name} و همه‌ی زیرمجموعه‌هایش حذف می‌شوند. این کار فقط وقتی انجام می‌شود که هیچ‌کدام اکانت نداشته باشند.',

  // Create/edit dialog
  'You can give at most {accounts} accounts and {traffic} of traffic credit to a new sub-reseller.':
    'حداکثر می‌توانید {accounts} اکانت و {traffic} اعتبار ترافیک به یک زیرمجموعه‌ی جدید بدهید.',
  'Parent reseller': 'نماینده‌ی والد',
  'Leave empty for a top-level reseller. Its limits are taken from the parent’s plan.':
    'برای نماینده‌ی سطح اول خالی بگذارید. سقف‌های نماینده از پلن والد کم می‌شود.',
  'None (top level)': 'هیچ‌کدام (سطح اول)',
  'A sub-reseller’s accounts, traffic credit and plan end come out of its parent’s: they must fit in what the parent has left and cannot outlast it.':
    'تعداد اکانت، اعتبار ترافیک و تاریخ پایان پلنِ زیرمجموعه از پلن والدش کم می‌شود: باید در سهمِ باقی‌مانده‌ی والد جا شود و نمی‌تواند بعد از پایان پلن والد تمام شود.',

  // Branding editor and the Settings card
  Branding: 'برندینگ',
  'Default branding': 'برندینگ پیش‌فرض',
  'Brand name, logo, support link and an announcement shown on the subscription page of your own accounts and of resellers without branding.':
    'نام برند، لوگو، لینک پشتیبانی و اعلان صفحه‌ی اشتراکِ اکانت‌های خودتان و نماینده‌هایی که برندینگ ندارند.',
  "How your customers' subscription page looks: your name, logo, support link and a notice. Sub-resellers without their own branding inherit it.":
    'ظاهر صفحه‌ی اشتراک مشتری‌هایتان: نام، لوگو، لینک پشتیبانی و یک اعلان. زیرمجموعه‌هایی که برندینگ خودشان را ندارند همین را نشان می‌دهند.',
  'Branding saved': 'برندینگ ذخیره شد',
  'Save branding': 'ذخیره‌ی برندینگ',
  'Could not load branding.': 'بارگذاری برندینگ ممکن نشد.',
  'You have no branding yet: your customers currently see the branding of the reseller above you.':
    'هنوز برندینگی تنظیم نکرده‌اید: مشتری‌هایتان فعلاً برندینگ نماینده‌ی بالادستی را می‌بینند.',
  'You have no branding yet: your customers currently see the default branding.':
    'هنوز برندینگی تنظیم نکرده‌اید: مشتری‌هایتان فعلاً برندینگ پیش‌فرض را می‌بینند.',
  'Brand name': 'نام برند',
  'Shown at the top of the subscription page.': 'بالای صفحه‌ی اشتراک نمایش داده می‌شود.',
  'Support link': 'لینک پشتیبانی',
  'A Telegram handle like @my_support, or an https link.': 'آیدی تلگرام مثل @my_support یا یک لینک https.',
  Announcement: 'اعلان',
  'A short notice shown above the subscription page (plain text, up to 280 characters).':
    'یک اعلان کوتاه که بالای صفحه‌ی اشتراک نشان داده می‌شود (متن ساده، حداکثر ۲۸۰ نویسه).',
  'Accent color': 'رنگ تأکید',
  Logo: 'لوگو',
  'PNG, JPEG or WebP, up to 64 KB. Or paste the address of a logo hosted elsewhere (https).':
    'PNG، JPEG یا WebP تا ۶۴ کیلوبایت. یا نشانی لوگویی را که جای دیگری میزبانی می‌شود (https) بگذارید.',
  'Upload logo': 'بارگذاری لوگو',
  Preview: 'پیش‌نمایش',
  'Your brand name': 'نام برند شما',
  'The logo must be a PNG, JPEG or WebP image.': 'لوگو باید تصویر PNG، JPEG یا WebP باشد.',
  'The logo is too large - keep it under 64 KB.': 'لوگو بزرگ است؛ حجمش را زیر ۶۴ کیلوبایت نگه دارید.',

  // Reports: reseller tree
  'You and your sub-resellers: each row is the reseller itself, and the totals under a name include everyone below it':
    'شما و زیرمجموعه‌هایتان: هر ردیف خودِ آن نماینده است و عددِ «با زیرمجموعه‌ها» زیر هر نام، همه‌ی پایین‌دست‌ها را هم شامل می‌شود',
  'with sub-resellers: {amount} · {n} accounts': 'با زیرمجموعه‌ها: {amount} · {n} اکانت',

  // Notification events
  'Sub-reseller created': 'زیرمجموعه ساخته شد',
  'Sub-reseller updated': 'زیرمجموعه ویرایش شد',
  'Sub-reseller deleted': 'زیرمجموعه حذف شد',

  // API error codes (lib/apiErrors.ts)
  'This reseller still has sub-resellers. Delete them first.': 'این نماینده هنوز زیرمجموعه دارد. اول آن‌ها را حذف کنید.',
  'Sub-resellers cannot go any deeper than this.': 'زیرمجموعه‌ها نمی‌توانند از این عمیق‌تر شوند.',
  'That is more accounts than you have left to give.': 'این تعداد اکانت از سهم باقی‌مانده‌ی شما بیشتر است.',
  'That is more traffic credit than you have left to give.': 'این مقدار اعتبار ترافیک از سهم باقی‌مانده‌ی شما بیشتر است.',
  'Set an account limit and a traffic credit - unlimited is not possible under a limited plan.':
    'سقف اکانت و اعتبار ترافیک را مشخص کنید؛ زیر یک پلن محدود، نامحدود ممکن نیست.',
  'A sub-reseller’s plan cannot end after yours.': 'پلن زیرمجموعه نمی‌تواند بعد از پلن شما تمام شود.',
  'Set a plan end date - your own plan expires.': 'تاریخ پایان پلن را مشخص کنید؛ پلن خودتان تاریخ انقضا دارد.',
  'Pick only node groups you have yourself.': 'فقط از گروه‌های نودی که خودتان دارید انتخاب کنید.',
  'That is below what is already handed to the sub-resellers. Reduce theirs first.':
    'این مقدار از چیزی که قبلاً به زیرمجموعه‌ها داده شده کمتر است. اول سهم آن‌ها را کم کنید.',
  'An administrator disabled this reseller; only they can enable it.': 'مدیر این نماینده را غیرفعال کرده و فقط خودش می‌تواند فعالش کند.',
  'The parent reseller is disabled or its plan has expired.': 'نماینده‌ی والد غیرفعال است یا پلنش تمام شده.',
  'The logo must be a PNG, JPEG or WebP image under 64 KB.': 'لوگو باید تصویر PNG، JPEG یا WebP و کمتر از ۶۴ کیلوبایت باشد.',
}
