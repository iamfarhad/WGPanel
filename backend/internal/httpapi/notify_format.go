package httpapi

import (
	"fmt"
	"strings"

	"wgpanel-api/internal/events"
)

// rendered is a human-readable notification in one language.
type rendered struct {
	Title string
	Body  string
}

func humanBytes(b int64) string {
	const gb = 1_000_000_000
	switch {
	case b >= gb:
		return fmt.Sprintf("%.2f GB", float64(b)/gb)
	case b >= 1_000_000:
		return fmt.Sprintf("%.1f MB", float64(b)/1_000_000)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// faDigits converts ASCII digits to Persian digits for fa messages.
func faDigits(s string) string {
	return strings.NewReplacer("0", "۰", "1", "۱", "2", "۲", "3", "۳", "4", "۴", "5", "۵", "6", "۶", "7", "۷", "8", "۸", "9", "۹").Replace(s)
}

func dataStr(d map[string]any, k string) string {
	if v, ok := d[k]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func dataInt(d map[string]any, k string) int64 {
	switch v := d[k].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}

// renderEvent formats ev for lang ("en" or "fa"). Persian output is sentence-level,
// not word-for-word, and uses Persian digits.
func renderEvent(ev events.Event, lang string) rendered {
	d := ev.Data
	if d == nil {
		d = map[string]any{}
	}
	fa := lang == "fa"
	pick := func(en, faText string) string {
		if fa {
			return faText
		}
		return en
	}
	label := dataStr(d, "label")
	node := dataStr(d, "node_name")
	reseller := dataStr(d, "reseller_username")

	var r rendered
	switch ev.Type {
	case events.NodeDown:
		r.Title = pick("Node down", "نود از دسترس خارج شد")
		r.Body = pick(fmt.Sprintf("Node %q stopped sending heartbeats and is now offline.", node),
			fmt.Sprintf("نود «%s» دیگر هارت‌بیت نمی‌فرستد و آفلاین شده است.", node))
	case events.NodeRecovered:
		r.Title = pick("Node recovered", "نود دوباره آنلاین شد")
		r.Body = pick(fmt.Sprintf("Node %q is sending heartbeats again.", node),
			fmt.Sprintf("نود «%s» دوباره هارت‌بیت می‌فرستد.", node))
	case events.AccountCreated:
		r.Title = pick("Account created", "اکانت ساخته شد")
		r.Body = pick(fmt.Sprintf("Account %q was created.", label), fmt.Sprintf("اکانت «%s» ساخته شد.", label))
	case events.AccountDeleted:
		r.Title = pick("Account deleted", "اکانت حذف شد")
		r.Body = pick(fmt.Sprintf("Account %q was deleted.", label), fmt.Sprintf("اکانت «%s» حذف شد.", label))
	case events.AccountSuspended:
		r.Title = pick("Account suspended", "اکانت تعلیق شد")
		r.Body = pick(fmt.Sprintf("Account %q was suspended.", label), fmt.Sprintf("اکانت «%s» تعلیق شد.", label))
	case events.AccountEnabled:
		r.Title = pick("Account enabled", "اکانت فعال شد")
		r.Body = pick(fmt.Sprintf("Account %q was enabled again.", label), fmt.Sprintf("اکانت «%s» دوباره فعال شد.", label))
	case events.AccountQuota80:
		used, quota := humanBytes(dataInt(d, "used_bytes")), humanBytes(dataInt(d, "quota_bytes"))
		r.Title = pick("Account used 80% of its data", "مصرف اکانت به ۸۰٪ حجم رسید")
		r.Body = pick(fmt.Sprintf("Account %q has used %s of its %s data quota.", label, used, quota),
			faDigits(fmt.Sprintf("اکانت «%s» %s از %s حجم خود را مصرف کرده است.", label, used, quota)))
	case events.AccountQuotaGone:
		r.Title = pick("Account data quota exhausted", "حجم اکانت تمام شد")
		r.Body = pick(fmt.Sprintf("Account %q has used its whole data quota (%s) and is suspended.", label, humanBytes(dataInt(d, "quota_bytes"))),
			faDigits(fmt.Sprintf("اکانت «%s» تمام حجم خود (%s) را مصرف کرده و تعلیق شده است.", label, humanBytes(dataInt(d, "quota_bytes")))))
	case events.AccountExpiringSoon:
		r.Title = pick("Account expires soon", "اکانت به‌زودی منقضی می‌شود")
		r.Body = pick(fmt.Sprintf("Account %q expires within 3 days (%s).", label, dataStr(d, "expiry_at")),
			faDigits(fmt.Sprintf("اکانت «%s» تا ۳ روز دیگر منقضی می‌شود (%s).", label, dataStr(d, "expiry_at"))))
	case events.AccountExpired:
		r.Title = pick("Account expired", "اکانت منقضی شد")
		r.Body = pick(fmt.Sprintf("Account %q has expired.", label), fmt.Sprintf("اکانت «%s» منقضی شده است.", label))
	case events.ResellerCreditLow:
		r.Title = pick("Reseller data credit is low", "اعتبار حجمی نماینده رو به پایان است")
		r.Body = pick(fmt.Sprintf("Reseller %q has allocated %d%% of its data credit.", reseller, dataInt(d, "percent")),
			faDigits(fmt.Sprintf("نماینده «%s» %d٪ از اعتبار حجمی خود را تخصیص داده است.", reseller, dataInt(d, "percent"))))
	case events.ResellerPlanExpires:
		r.Title = pick("Reseller plan expires soon", "پلن نماینده به‌زودی تمام می‌شود")
		r.Body = pick(fmt.Sprintf("The plan of reseller %q ends on %s.", reseller, dataStr(d, "expires_at")),
			faDigits(fmt.Sprintf("پلن نماینده «%s» در تاریخ %s به پایان می‌رسد.", reseller, dataStr(d, "expires_at"))))
	case events.BackupSucceeded:
		r.Title = pick("Backup completed", "بکاپ با موفقیت انجام شد")
		r.Body = pick(fmt.Sprintf("Backup %s (%s) was sent to %s.", dataStr(d, "filename"), humanBytes(dataInt(d, "size_bytes")), dataStr(d, "destination")),
			faDigits(fmt.Sprintf("بکاپ %s (%s) به مقصد %s ارسال شد.", dataStr(d, "filename"), humanBytes(dataInt(d, "size_bytes")), dataStr(d, "destination"))))
	case events.BackupFailed:
		r.Title = pick("Backup failed", "بکاپ ناموفق بود")
		r.Body = pick(fmt.Sprintf("Backup to %s failed: %s", dataStr(d, "destination"), dataStr(d, "error")),
			fmt.Sprintf("ارسال بکاپ به %s ناموفق بود: %s", dataStr(d, "destination"), dataStr(d, "error")))
	case events.ObfuscationProfileRotated:
		r.Title = pick("Obfuscation profile rotated", "پروفایل مبهم‌سازی چرخانده شد")
		r.Body = pick(fmt.Sprintf("Profile %q was rotated to revision %d. Clients of its nodes must re-import their configs.", dataStr(d, "profile_name"), dataInt(d, "revision")),
			faDigits(fmt.Sprintf("پروفایل «%s» به نسخه %d چرخانده شد. کاربرهای نودهای آن باید کانفیگ جدید را وارد کنند.", dataStr(d, "profile_name"), dataInt(d, "revision"))))
	case "test":
		r.Title = pick("Test notification", "اعلان آزمایشی")
		r.Body = pick("This is a test message from Jirjirak WireGuard. The channel works.",
			"این یک پیام آزمایشی از جیرجیرک وایرگارد است. کانال درست کار می‌کند.")
	default:
		r.Title = ev.Type
		r.Body = ev.Type
	}
	return r
}

func severityTag(sev, lang string) string {
	switch sev {
	case events.SeverityCritical:
		if lang == "fa" {
			return "[بحرانی]"
		}
		return "[CRITICAL]"
	case events.SeverityWarning:
		if lang == "fa" {
			return "[هشدار]"
		}
		return "[WARNING]"
	}
	if lang == "fa" {
		return "[اطلاع]"
	}
	return "[INFO]"
}
