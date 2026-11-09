package main

import (
	"strings"

	"fold/internal/mail"
	"fold/internal/notify"
	"fold/models"

	"gorm.io/gorm"
)

// readEmailRecipients loads accounts and footer, then open suppressions.
// Optional mail also needs the effective choices: three reads for up to 500
// destinations. Required mail never reads account or address choices.
func readEmailRecipients(db *gorm.DB, userIDs []uint, needChoice bool) ([]emailRecipient, emailOff, error) {
	if len(userIDs) == 0 {
		return nil, emailOff{}, nil
	}
	var users []emailRecipient
	err := db.Model(&models.User{}).Select(`users.*,
		COALESCE((SELECT value FROM settings WHERE key = ?), '') AS site_name,
		COALESCE((SELECT value FROM settings WHERE key = ?), '') AS church_address`, settingSiteName, settingChurchAddress).
		Where("users.id IN ?", userIDs).Find(&users).Error
	if err != nil {
		return nil, emailOff{}, err
	}
	emails := make([]string, 0, len(users))
	dests := make([]emailDest, 0, len(users))
	for i := range users {
		emails = append(emails, users[i].Email)
		dests = append(dests, emailDest{UserID: users[i].ID, Address: users[i].Email})
	}
	reasons, err := mail.OpenSuppressionReasons(db, emails)
	if err != nil {
		return nil, emailOff{}, err
	}
	for i := range users {
		users[i].EmailSuppressions = strings.Join(reasons[mail.NormalizeAddress(users[i].Email)], ",")
	}
	var off emailOff
	if needChoice {
		off, err = readEmailOff(db, dests)
		if err != nil {
			return nil, emailOff{}, err
		}
	}
	return users, off, nil
}

// readEmailRecipient is that read for one person. A missing account returns
// a nil user and no error. A database error returns the error and nothing
// else, so the caller records no delivery.
func readEmailRecipient(db *gorm.DB, userID uint, needChoice bool) (u *models.User, off emailOff, suppressions []string, footer footerFacts, err error) {
	users, off, err := readEmailRecipients(db, []uint{userID}, needChoice)
	if err != nil {
		return nil, emailOff{}, nil, footerFacts{}, err
	}
	byID, suppressed, footer := indexEmailRecipients(users)
	return byID[userID], off, suppressed[userID], footer, nil
}

// indexEmailRecipients splits the joined read into the maps the sender uses.
// The footer comes from the first row; site name and address are the same
// on every row.
func indexEmailRecipients(users []emailRecipient) (byID map[uint]*models.User, suppressed map[uint][]string, footer footerFacts) {
	byID = make(map[uint]*models.User, len(users))
	suppressed = make(map[uint][]string, len(users))
	footer = footerFacts{site: defaultSiteName}
	if len(users) > 0 {
		if users[0].SiteName != "" {
			footer.site = users[0].SiteName
		}
		footer.address = users[0].ChurchAddress
	}
	for i := range users {
		byID[users[i].ID] = &users[i].User
		if users[i].EmailSuppressions != "" {
			suppressed[users[i].ID] = strings.Split(users[i].EmailSuppressions, ",")
		}
	}
	return byID, suppressed, footer
}

// emailRecipient is an account with its suppression and footer facts.
type emailRecipient struct {
	models.User
	// EmailSuppressions are the open suppression reasons on the address
	// (DEC-112), comma-joined in ascending suppression id: one more read for
	// the whole set, filled after the accounts load.
	EmailSuppressions string
	// SiteName and ChurchAddress ride along for optional email footers, so
	// footers add no reads (the same on every row).
	SiteName      string
	ChurchAddress string
}

// categoryScope is the consent and unsubscribe scope of one optional
// notification category; categoryOfScope reads it back.
const categoryScopePrefix = "category:"

func categoryScope(category string) string { return categoryScopePrefix + category }

func categoryOfScope(scope string) (string, bool) {
	return strings.CutPrefix(scope, categoryScopePrefix)
}

// notificationEmailMessage is the mail notify.deliver sends: the
// stored email text, the absolute link, and, for an optional kind, the
// footer and one-click unsubscribe. A spam complaint stops optional mail,
// not required notices (DEC-112). Required mail never offers an unsubscribe.
func (a *App) notificationEmailMessage(n *models.Notification, u *models.User, k notify.Kind, footer footerFacts) mail.Message {
	text := n.EmailTitle
	if n.EmailBody != "" {
		text += "\n\n" + n.EmailBody
	}
	text += "\n\n" + a.emailLink(n.Link)
	msg := mail.Message{To: u.Email, Subject: n.EmailTitle, Text: text, Optional: !k.Required}
	if !k.Required {
		token := a.unsubscribeToken(u.ID, categoryScope(k.Category), u.Email)
		msg.ListUnsubscribe = a.oneClickURL(token)
		msg.Text += a.optionalEmailFooter(footer, k.Category, token)
	}
	return msg
}
