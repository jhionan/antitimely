package invoice

import (
	"fmt"
	"strings"
	"time"
)

// FormatMoney renders an integer-cents amount as "X,XXX.YY CCC".
func FormatMoney(cents int64, currency string) string {
	negative := cents < 0
	if negative {
		cents = -cents
	}
	major := cents / 100
	minor := cents % 100
	majorStr := withThousandsSeparator(major)
	sign := ""
	if negative {
		sign = "-"
	}
	return fmt.Sprintf("%s%s.%02d %s", sign, majorStr, minor, currency)
}

func withThousandsSeparator(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteByte(',')
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteByte(',')
		}
	}
	return b.String()
}

// FormatHours renders an hours-times-100 integer ("4750" = 47.5h) without
// trailing-zero noise. 0 → "0"; 50 → "0.5"; 4750 → "47.5"; 475 → "4.75".
func FormatHours(hoursTimes100 int64) string {
	whole := hoursTimes100 / 100
	frac := hoursTimes100 % 100
	if frac == 0 {
		return fmt.Sprintf("%d", whole)
	}
	if frac%10 == 0 {
		return fmt.Sprintf("%d.%d", whole, frac/10)
	}
	return fmt.Sprintf("%d.%02d", whole, frac)
}

// FormatDate renders "May 27, 2026".
func FormatDate(t time.Time) string {
	return t.Format("January 2, 2006")
}

// FormatInvoiceNumber renders a zero-padded invoice number.
func FormatInvoiceNumber(prefix string, pad int, n int64) string {
	if pad <= 0 {
		return fmt.Sprintf("%s%d", prefix, n)
	}
	return fmt.Sprintf("%s%0*d", prefix, pad, n)
}
