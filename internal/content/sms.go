package content

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf16"
)

// Texts (PLAN 7.3, M3.4b). Fold counts a text the way carriers bill it:
// GSM-7 when every character is in the GSM 03.38 alphabet (160 per text, 153
// per part once it is split), else UCS-2 (70, or 67 per part, counting
// UTF-16 units, so one emoji is two). The church-name prefix and the STOP
// footer are added to every text and counted. web/src/lib/sms.ts is the same
// logic for the composer's live counter; both run the cases in
// web/src/lib/sms-cases.json. Texting itself arrives with M3.7 (DEC-117).

// gsmBasic is the GSM 03.38 basic alphabet; gsmExtended characters take an
// escape plus a character, so two septets.
const (
	gsmBasic    = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"
	gsmExtended = "^{}\\[~]|€\f"
)

// SMS encodings.
const (
	EncodingGSM  = "GSM-7"
	EncodingUCS2 = "UCS-2"
)

// SMSCount is how a text will be sent: its encoding, its length in septets
// (GSM-7) or UTF-16 units (UCS-2), and how many parts it takes.
type SMSCount struct {
	Encoding string `json:"encoding"`
	Units    int    `json:"units"`
	Segments int    `json:"segments"`
}

// CountSMS counts s as it will be sent. A part never splits an escape pair
// or a surrogate pair, as carriers do, so a long text can take one more part
// than its length alone suggests.
func CountSMS(s string) SMSCount {
	sizes := []int{}
	gsm := true
	for _, r := range s {
		switch {
		case strings.ContainsRune(gsmBasic, r):
			sizes = append(sizes, 1)
		case strings.ContainsRune(gsmExtended, r):
			sizes = append(sizes, 2)
		default:
			gsm = false
		}
		if !gsm {
			break
		}
	}
	single, part, enc := 160, 153, EncodingGSM
	if !gsm {
		sizes = sizes[:0]
		for _, r := range s {
			sizes = append(sizes, len(utf16.Encode([]rune{r})))
		}
		single, part, enc = 70, 67, EncodingUCS2
	}
	units := 0
	for _, n := range sizes {
		units += n
	}
	out := SMSCount{Encoding: enc, Units: units}
	switch {
	case units == 0:
	case units <= single:
		out.Segments = 1
	default:
		used := 0
		out.Segments = 1
		for _, n := range sizes {
			if used+n > part {
				out.Segments++
				used = 0
			}
			used += n
		}
	}
	return out
}

// SMSFooter ends every text Fold sends.
const SMSFooter = "Reply STOP to opt out"

// FramedSMS is a text as it goes out: "First Baptist: " before the body and
// the STOP footer on its own line after it.
func FramedSMS(church, body string) string {
	return strings.TrimSpace(church) + ": " + body + "\n" + SMSFooter
}

// Asking for money by text makes carriers treat it as charity traffic
// (DEC-3), so the composer warns. These words, and links to giving pages
// (a giving word in the address, or a giving service), set it off.
var (
	donationWords = regexp.MustCompile(`(?i)\b(give|gives|giving|donate|donates|donating|donation|donations|tithe|tithes|tithing|offering|offerings)\b`)
	urlRe         = regexp.MustCompile(`(?i)\b((?:https?://)?(?:[a-z0-9-]+\.)+[a-z]{2,}(?:/[^\s]*)?)`)
	givingHosts   = []string{"tithe.ly", "pushpay.com", "givelify.com", "easytithe.com", "paypal.me", "paypal.com", "venmo.com", "cash.app", "givebutter.com", "onrealm.org"}
	givingPath    = regexp.MustCompile(`(?i)(give|giving|donat|tithe|offering)`)
)

// DonationAsk reports whether text reads as asking for money (DEC-3).
func DonationAsk(text string) bool {
	for _, m := range urlRe.FindAllString(text, -1) {
		raw := m
		if !strings.Contains(raw, "://") {
			raw = "https://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		host := strings.ToLower(u.Hostname())
		for _, h := range givingHosts {
			if host == h || strings.HasSuffix(host, "."+h) {
				return true
			}
		}
		if givingPath.MatchString(u.Path) || givingPath.MatchString(strings.Split(host, ".")[0]) {
			return true
		}
	}
	return donationWords.MatchString(text)
}
