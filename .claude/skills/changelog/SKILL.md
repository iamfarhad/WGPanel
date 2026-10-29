---
name: changelog
description: >-
  Write a polished Persian release-changelog post for the Jirjirak WireGuard panel (WGPanel): the
  «🚀 پنل جیرجیرک وایرگارد به نسخه X.Y.Z به‌روزرسانی شد» headline, a «✨ ویژگی‌های برجسته» highlight
  block with where-to-find-it paths in the panel, per-area sections with their own icons, ✔️
  one-line capabilities, a short fixes block, the update command and the fixed Jirjirak footer —
  delivered twice, as a paste-into-Telegram text and as Bot-API HTML. Use when asked for a
  changelog, release notes, an update post, «چنج‌لاگ», «پست آپدیت», «تغییرات نسخه» for the panel,
  or when a panel version is about to be tagged or announced. Also covers picking the version,
  CHANGELOG.md, the tag, the GitHub release and the GHCR images it triggers.
---

# Release changelog for the panel

Same family as the Jirjirak bot's changelog skill (`~/Documents/Bot/.claude/skills/changelog`), so
both products read as one brand in the announcement channel.

The reader is a **VPN seller who runs their own Jirjirak WireGuard panel** — or a reseller on
someone's panel — scrolling a Telegram channel on a phone. They give a post about three seconds. In
those three seconds it has to say: *which* product updated, *the one thing* worth stopping for, and
*where in the panel* to find it.

They do not care about Go packages, migrations or endpoints. Every line is something their VPN
business can now do, in their words. Write the Persian the way the `persian-copywriting` skill
describes — natural, short, never a translation of an English sentence.

## Anatomy of the post

```
🚀 پنل جیرجیرک وایرگارد به نسخه 0.2.0 به‌روزرسانی شد
━━━━━━━━━━━━━━━━━━

✨ ویژگی‌های برجسته این نسخه

⏳ شروع اکانت از اولین اتصال
اکانت را از قبل بسازید؛ روزها از اولین وصل‌شدن مشتری شمرده می‌شوند.
📍 اکانت‌ها ← اکانت جدید ← شروع از اولین استفاده

━━━━━━━━━━━━━━━━━━

👥 اکانت‌ها
✔️ تمدید، تعلیق و حذف گروهی اکانت‌ها

🛠 رفع اشکال
✔️ اکانت‌هایی که وصل می‌شدند ولی اینترنت نداشتند درست شدند
✔️ بهینه‌سازی و رفع باگ‌های گزارش‌شده

💡 برای اعلان لحظه‌ای ربات، دسترسی webhooks را به کلید API بدهید.
⬆️ به‌روزرسانی روی سرور پنل: wgpanel update

➿➿➿➿➿➿

🤖 فروش خودکار اکانت با ربات جیرجیرک:
@JirjirakPremiumBot

📢 اخبار و آموزش‌های جیرجیرک:
@JirjirakPremium
```

### 1. Headline — fixed wording

`🚀 پنل جیرجیرک وایرگارد به نسخه {version} به‌روزرسانی شد`, one line, no date, and under it a `━`
rule of 18 characters. «پنل» is what tells this post apart from the bot's in the same channel. The
version is the tag without its `v`, in latin digits.

### 2. «✨ ویژگی‌های برجسته این نسخه» — the reason to stop scrolling

One or two features — **never more than three** — that a seller would change how they work for.
Each is a card of exactly three lines:

1. **Title**: one fitting emoji + a short noun phrase (≤ 6 words).
2. **Benefit**: one sentence, what it does FOR THEM, not how it works. Ends with «.».
3. **Path**: `📍 ` + the path in the panel, joined with ` ← `, using the Persian labels exactly as
   the panel draws them: the sidebar item, then the button/tab/field. For something that needs
   nothing from the admin (a customer-facing page, an automatic check), write
   `📍 خودکار برای همه‌ی اکانت‌ها` (or the fitting noun). For API-only features, `📍 مستندات API`.

A blank line between cards. Pick highlights by impact on the seller's money, their customers or
their daily work — not by how much code changed. A fixes-only release has no highlight block.

### 3. Sections — one icon per area

Each section is a header line `{icon} {name}` (no colon), then its ✔️ lines, then a blank line.
Only sections with content, always in this order:

| Icon | Section | What goes in it |
|---|---|---|
| 👥 | اکانت‌ها | creating/selling accounts, limits, templates, bulk, subscription page |
| 🤝 | نماینده‌ها | reseller plans, what resellers see and can do |
| 🌐 | نودها و شبکه | nodes, relays/endpoints, DNS, MTU, speed and connectivity |
| 🔔 | اعلان‌ها و بکاپ | notifications, webhooks, scheduled backups |
| 🔐 | امنیت | sign-in, 2FA, passkeys, allowlist, roles, audit |
| 📊 | گزارش‌ها و داشبورد | reports, dashboard, language/theme |
| 🤖 | API و ربات | bot API, keys, rate limits, Jirjirak bot integration |
| ⚙️ | نصب و نگهداری | installers, `wgpanel` CLI, images, upgrades |
| 🛠 | رفع اشکال | fixes — always LAST |

A feature already shown as a highlight is **not repeated** in its section.

### 4. Feature lines

- Start with `✔️ `, exactly one line (≈ 60 characters; 75 is the ceiling). Two lines means two
  features or the wrong wording.
- Say the capability, not the mechanism: «ساخت گروهی اکانت با خروجی CSV لینک‌ها» — not
  «POST /api/v1/accounts/bulk».
- Lead new abilities with «امکان …» / «پشتیبانی از …» / «اضافه شدن …», or the capability noun
  itself. Automatic behaviour takes the plain verb: «قطع خودکار اکانت وقتی حجمش تمام شود».
- Most impactful line first. **At most 7 lines per section**; fold or drop the small ones.
- Quote a label in «» exactly as the panel draws it in Persian.
- Technical names that sellers type or see as-is (`wgpanel update`, `webhooks`, MTU, CSV, QR, S3)
  stay latin.

### 5. Fixes

A fix line names the **symptom the seller or their customer saw, now gone**: «اکانت‌هایی که وصل
می‌شدند ولی اینترنت نداشتند درست شدند», not «fix DNS forwarder». At most 5 named fixes. End with
`✔️ بهینه‌سازی و رفع باگ‌های گزارش‌شده` when there were smaller ones.

### 6. Notes

After the last section:

- `💡 …` — up to one line for anything the seller must DO to get a feature (a permission to grant,
  a setting that ships off, a domain needed for passkeys). Leave it out when there is nothing.
- `⬆️ به‌روزرسانی روی سرور پنل: wgpanel update` — **always**, because the panel is self-hosted:
  nobody gets the release until they run it. If remote nodes also need updating in this release
  (node image or `node-entrypoint.sh` changed), add `⬆️ نودهای جدا: install-node.sh را دوباره اجرا کنید`.
- `⚠️ …` instead of the plain update line when the upgrade needs more than that (a manual step, a
  breaking API change for bots). Say exactly what to do.

### 7. Footer — verbatim

```
➿➿➿➿➿➿

🤖 فروش خودکار اکانت با ربات جیرجیرک:
@JirjirakPremiumBot

📢 اخبار و آموزش‌های جیرجیرک:
@JirjirakPremium
```

Copied exactly, both blank lines included. These handles are the real ones from the bot's
changelog skill; never invent, shorten or "improve" them.

### Typography rules

- **Digits**: Persian in prose (`۳ روز`), latin in the version number (`0.2.0`) and in commands.
- **Half-space (ZWNJ)**: «به‌روزرسانی», «اکانت‌ها», «می‌شود», «همه‌ی». A missing one is the first
  thing that makes a post look amateur.
- **Emoji are signposts**: one per highlight title, one per section header, ✔️ per line. Never two
  in a row, never inside a sentence except a quoted label.
- **Length**: under 2 000 characters is the target; 4 096 is Telegram's hard limit. A release that
  does not fit is split into two posts, not squeezed.

## Delivering it — two versions, every time

Give both, each in its own fenced block, labelled:

**1. «نسخهٔ آمادهٔ ارسال در تلگرام»** — Telegram's typed-markdown bold, converted by the official
apps on send: wrap the headline, each highlight title and each section header in `**…**`. Nothing
else bold. No `>` quotes, no `__`, no markdown links.

**2. «نسخهٔ HTML (ربات / Bot API)»** — the same post for `parse_mode: HTML`:
- `<b>…</b>` exactly where version 1 has `**…**`.
- The fixes section's lines (not its header) inside `<blockquote expandable>…</blockquote>`.
- Escape `&`, `<`, `>` as `&amp;`, `&lt;`, `&gt;`. Real newlines, no `<br>`.
- No other tags.

Then one line after the blocks: it is a draft and has not been sent anywhere.

## Getting the content right

Write from **what actually shipped**, never from memory or a plan:

```bash
git describe --tags --abbrev=0                       # last release tag
git log --oneline --first-parent <last-tag>..HEAD    # merged PRs + direct commits
gh pr view <n> --json title,body                     # each PR's story
git diff --stat <last-tag>..HEAD
```

For each change ask «what can a seller, reseller or their customer do now?». Refactors, tests and
CI-only changes get no line.

Check every claim before delivering:

- **Labels and paths**: the sidebar is `frontend/src/pages/DashboardLayout.tsx` (its `to:` items,
  `reseller: true` marks what resellers see); every Persian label is in
  `frontend/src/locales/fa.ts` and `frontend/src/locales/fa/*.ts`, keyed by the English text. Grep
  the English label in the page, then its Persian value — use the BUTTON/field text, not the
  page description.
- **Reseller lines**: confirm the route is on the reseller allowlist
  (`backend/internal/httpapi/reseller_gate.go`) before claiming resellers can use it.
- **Bot lines**: confirm against `docs/openapi.yaml`.
- Anything off by default or needing a permission gets the `💡` note.

## Picking the version, tagging and releasing

Semver against the seller's experience. While below 1.0:

- **minor** (`0.X.0`) — new capabilities, or anything a bot integration must adapt to.
- **patch** (`0.x.Y`) — fixes only.

`git tag --sort=-v:refname | head -5` gives the last one.

The release is one ordered sequence — the order is the point:

```bash
git status --short                                   # 1. nothing uncommitted that belongs in it
# 2. CHANGELOG.md: add "## [X.Y.Z] - YYYY-MM-DD" in English (Keep a Changelog sections) and
#    the link reference at the bottom; bump the frontend version:
(cd frontend && npm version X.Y.Z --no-git-tag-version)
git commit -am "Release X.Y.Z: changelog and version"
git tag -a vX.Y.Z -m "پنل جیرجیرک وایرگارد به نسخه X.Y.Z به‌روزرسانی شد"
git push origin main && git push origin vX.Y.Z      # 3. the tag builds ghcr.io/iamfarhad/wgpanel-{api,frontend,node}:vX.Y.Z
gh release create vX.Y.Z --title "Jirjirak WireGuard X.Y.Z" --notes-file <that CHANGELOG section>
```

**Push, tag, release and deploy only on words that name them** («تگ بزن», «پوش کن», «دیپلوی کن»).
A yes to a bundled question is not one, and an earlier release's approval does not carry over. A
pushed tag is never moved: a mistake is fixed by the next version.

## Also update, in the same change

- **`CHANGELOG.md`** — the English, technical record (the post is the Persian, seller-facing one).
- **`README.md` / `README.fa.md`** — the feature lists, and screenshots when a page changed visibly.
- **The GitHub release** — notes from the CHANGELOG section plus the three image tags.

## Before you hand it over — checklist

- [ ] Headline wording exact, «پنل» in it, version latin, `━` rule under it
- [ ] 1–3 highlights, each title / benefit / `📍` path, paths checked against the Persian labels
- [ ] Sections in canonical order, each with its icon, none empty, ≤ 7 lines, no highlight repeated
- [ ] Every ✔️ is one line and a capability (or a symptom gone), checked against git and the code
- [ ] `💡` only when the seller must act; `⬆️ … wgpanel update` line present
- [ ] ZWNJ throughout, Persian digits in prose
- [ ] Footer verbatim, both blank lines
- [ ] Under 2 000 characters (hard limit 4 096)
- [ ] Both versions delivered: Telegram paste (`**bold**`) and HTML (`<b>`, `<blockquote expandable>`)
- [ ] CHANGELOG.md, tag, GitHub release consistent with the post
