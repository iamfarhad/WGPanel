package httpapi

import (
	"fmt"

	"wgpanel-api/internal/events"
)

// formatToman renders a Toman amount with thousands separators: 1234000 -> "1,234,000".
func formatToman(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprint(n)
	out := make([]byte, 0, len(s)+len(s)/3+1)
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// renderSalesEvent formats the sales events for renderEvent (notify_format.go calls it
// first, so that file only carries one extra line). ok=false for any other event type.
func renderSalesEvent(ev events.Event, lang string) (rendered, bool) {
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
	amount := formatToman(dataInt(d, "amount_toman"))
	who := dataStr(d, "label")
	if who == "" {
		who = dataStr(d, "reseller_username")
	}
	if who == "" {
		who = dataStr(d, "contact")
	}
	method := map[string]string{"card_to_card": "card-to-card", "zarinpal": "ZarinPal", "nowpayments": "crypto"}[dataStr(d, "method")]
	methodFa := map[string]string{"card_to_card": "کارت‌به‌کارت", "zarinpal": "زرین‌پال", "nowpayments": "ارز دیجیتال"}[dataStr(d, "method")]

	var r rendered
	switch ev.Type {
	case events.PaymentAwaitingReview:
		r.Title = pick("Payment awaiting review", "پرداخت در انتظار بررسی")
		r.Body = pick(fmt.Sprintf("A %s payment of %s Toman (%s) needs your review: open Orders to approve or reject it.", method, amount, who),
			faDigits(fmt.Sprintf("یک پرداخت %s به مبلغ %s تومان (%s) منتظر بررسی شماست؛ از بخش سفارش‌ها تأیید یا رد کنید.", methodFa, amount, who)))
	case events.PaymentApproved:
		r.Title = pick("Payment approved", "پرداخت تأیید شد")
		r.Body = pick(fmt.Sprintf("Payment of %s Toman (%s) was accepted.", amount, who),
			faDigits(fmt.Sprintf("پرداخت %s تومانی (%s) تأیید شد.", amount, who)))
	case events.PaymentRejected:
		r.Title = pick("Payment rejected", "پرداخت رد شد")
		r.Body = pick(fmt.Sprintf("Payment of %s Toman (%s) was rejected. %s", amount, who, dataStr(d, "note")),
			faDigits(fmt.Sprintf("پرداخت %s تومانی (%s) رد شد. %s", amount, who, dataStr(d, "note"))))
	case events.OrderFulfilled:
		r.Title = pick("Order fulfilled", "سفارش تحویل داده شد")
		r.Body = pick(fmt.Sprintf("Order of %s Toman (%s) has been delivered.", amount, who),
			faDigits(fmt.Sprintf("سفارش %s تومانی (%s) تحویل داده شد.", amount, who)))
	case events.WalletCredited:
		r.Title = pick("Wallet credited", "کیف پول شارژ شد")
		r.Body = pick(fmt.Sprintf("%s Toman was added to the wallet. Balance: %s Toman.", formatToman(dataInt(d, "amount_toman")), formatToman(dataInt(d, "balance_toman"))),
			faDigits(fmt.Sprintf("%s تومان به کیف پول اضافه شد. موجودی: %s تومان.", formatToman(dataInt(d, "amount_toman")), formatToman(dataInt(d, "balance_toman")))))
	default:
		return rendered{}, false
	}
	return r, true
}
