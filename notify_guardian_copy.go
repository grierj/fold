package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"time"

	"fold/internal/jobs"
	"fold/internal/mail"
	"fold/internal/notify"
	"fold/models"

	"gorm.io/gorm"
)

// A guardian's copy of an optional notice sent to their teen (DEC-19). When
// the teen's own email is sent, sendHeldNotification queues one job per
// confirmed guardian in the same transaction (guardianCopyJobs). The job reads
// the facts again when it runs and sends one marked, plain-text copy with the
// guardian's own footer and unsubscribe. Email only (DEC-29).
//
// Nothing here is rendered from the teen's stored email text: that can carry
// the teen's signed manage link and video link. A copy shows the bell title
// and body (notify.Kind.CopyText), so no capability and, for a kind whose body
// can carry a leader's note, no note. The one kind written for someone who has
// lost access, registration.cancelled, is built from the plain event title its
// writer stored (notify.Kind.LostAccessCopyText) instead, because its stored
// title can carry a date (DEC-149).

func (a *App) guardianCopyKind() jobs.Kind {
	return jobs.Kind{
		Type: guardianCopyType, Label: "Sending a guardian a copy of a notice",
		Run: func(ctx context.Context, payload json.RawMessage) error {
			var p guardianCopyJob
			if err := json.Unmarshal(payload, &p); err != nil {
				return err
			}
			return a.sendGuardianCopy(ctx, p)
		},
	}
}

// guardianCopyJobs lists one job per confirmed adult guardian of the teen who
// is about to be mailed an optional notice, or none when the notice is not
// copied. It runs inside the claim, before the mail is sent: a failed read
// returns its error so the claim rolls back with nothing sent.
func guardianCopyJobs(tx *gorm.DB, n *models.Notification, k notify.Kind, recipient *models.User, now time.Time) ([]jobs.Spec, error) {
	if k.Required || recipient.AccountType != models.AccountTeen {
		return nil, nil
	}
	on, err := boolSettingErrIn(tx, settingGuardianCopies, false)
	if err != nil || !on {
		return nil, err
	}
	guardians, err := contactGuardians(tx, recipient.ID)
	if err != nil {
		return nil, err
	}
	specs := make([]jobs.Spec, 0, len(guardians))
	for _, g := range guardians {
		specs = append(specs, guardianCopyType.Spec(guardianCopyJob{NotificationID: n.ID, GuardianID: g}, now, guardianCopyKey(n.ID, g)))
	}
	return specs, nil
}

// sendGuardianCopy is the job: read every fact in one lock-free transaction,
// finish quietly when any of them says no, then send without holding a
// transaction. A failed read is an error and retries. No lock is held from
// the read to the provider, so an erasure, revocation or access change that
// commits in that window may not stop that one copy; one committed before the
// read does.
func (a *App) sendGuardianCopy(ctx context.Context, p guardianCopyJob) error {
	var msg mail.Message
	var suppressions []string
	var send bool
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		msg, suppressions, send, err = a.prepareGuardianCopy(tx, p, a.now())
		return err
	})
	if err != nil {
		log.Printf("mail: guardian copy of notice #%d preparation: %s", p.NotificationID, jobs.CleanError(err))
		return err
	}
	if !send {
		return nil
	}
	sendCtx, cancel := context.WithTimeout(ctx, deliverSendTimeout)
	defer cancel()
	if err := sendCtx.Err(); err != nil {
		return err
	}
	res := sendTracked(sendCtx, mail.Unwrap(a.mailSender()), msg, suppressions)
	if res.outcome == sendSent {
		return nil
	}
	log.Printf("mail: guardian copy of notice #%d: %s", p.NotificationID, jobs.CleanError(res.err))
	switch res.outcome {
	case sendSuppressed, sendPermanent, sendNotConfigured:
		return nil
	}
	return res.err
}

// prepareGuardianCopy decides whether the copy goes and builds it. send is
// false for every "no": the notice, teen or link is gone or changed, the
// switch is off, or the guardian may not or does not want it. Each no is
// final, so the job is done.
func (a *App) prepareGuardianCopy(tx *gorm.DB, p guardianCopyJob, now time.Time) (msg mail.Message, suppressions []string, send bool, err error) {
	var n models.Notification
	if err := tx.Where("id = ?", p.NotificationID).First(&n).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return msg, nil, false, nil
		}
		return msg, nil, false, err
	}
	k, ok := notify.Lookup(n.Kind)
	if !ok || !k.AllowEmail || k.Required {
		return msg, nil, false, nil
	}
	// The job exists only with the teen's sent email; the savepoint in the
	// claim makes this a check of an invariant, not a decision.
	var sent int64
	if err := tx.Model(&models.NotificationDelivery{}).
		Where("notification_id = ? AND channel = ? AND status = ?", n.ID, models.DeliveryEmail, models.DeliverySent).
		Count(&sent).Error; err != nil || sent == 0 {
		return msg, nil, false, err
	}
	on, err := boolSettingErrIn(tx, settingGuardianCopies, false)
	if err != nil || !on {
		return msg, nil, false, err
	}
	teen, err := userIn(tx, n.UserID)
	if err != nil || teen == nil || teen.AccountType != models.AccountTeen || teen.Status != models.UserActive {
		return msg, nil, false, err
	}
	guardians, err := contactGuardians(tx, teen.ID)
	if err != nil || !slices.Contains(guardians, p.GuardianID) {
		return msg, nil, false, err
	}
	// A guardian who is a direct recipient of the same notice (membership
	// departure) already has it. A notice with no dedupe key never matches.
	if n.DedupeKey != nil {
		var direct int64
		if err := tx.Model(&models.Notification{}).
			Where("user_id = ? AND kind = ? AND subject_type = ? AND subject_id IS NOT DISTINCT FROM ? AND dedupe_key = ?",
				p.GuardianID, n.Kind, n.SubjectType, n.SubjectID, *n.DedupeKey).
			Count(&direct).Error; err != nil || direct > 0 {
			return msg, nil, false, err
		}
	}
	title, body := k.CopyText(n.Title, n.Body)
	if k.CopyWithoutSubjectAccess {
		// The teen has lost the subject, so the stored text, which can carry more
		// than the event's title, is not what a guardian sees. A row with no
		// stored plain title gets no copy.
		var ok bool
		if title, body, ok = k.LostAccessCopyText(n.Data); !ok {
			return msg, nil, false, nil
		}
	} else {
		sees, err := a.teenSeesNoticeSubject(tx, teen, &n, now)
		if err != nil || !sees {
			return msg, nil, false, err
		}
	}
	guardian, off, suppressions, footer, err := readEmailRecipient(tx, p.GuardianID, true)
	if err != nil {
		return msg, nil, false, err
	}
	if guardian == nil || guardian.Status != models.UserActive || off.Off(guardian.ID, guardian.Email, categoryScope(k.Category)) {
		return msg, nil, false, nil
	}
	return a.guardianCopyEmailMessage(title, body, k, teen, guardian, footer), suppressions, true, nil
}

// teenSeesNoticeSubject is whether the teen may still see what the notice is
// about, by the same rules the page uses: a date through reminderReaders (as a
// reminder's copy does), an event through its facts, a group through
// userCanSeeGroupIn. A subject of any other type is never copied.
func (a *App) teenSeesNoticeSubject(tx *gorm.DB, teen *models.User, n *models.Notification, now time.Time) (bool, error) {
	if n.SubjectID == nil {
		return false, nil
	}
	switch n.SubjectType {
	case "occurrence":
		can, err := a.reminderReaders(tx, *n.SubjectID, []uint{teen.ID}, now)
		if errors.Is(err, errAudienceGone) {
			return false, nil
		}
		return can[teen.ID], err
	case "event":
		var ev models.Event
		if err := tx.First(&ev, *n.SubjectID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return false, nil
			}
			return false, err
		}
		facts, err := eventFactsIn(tx, teen, &ev)
		if err != nil {
			return false, err
		}
		return facts.canSee(teen), nil
	case "group":
		var g models.Group
		if err := tx.First(&g, *n.SubjectID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return false, nil
			}
			return false, err
		}
		return userCanSeeGroupIn(tx, teen.ID, &g)
	}
	return false, nil
}

// guardianCopyEmailMessage is the copy: a marker naming the teen, then the
// notice's title and body (prepareGuardianCopy chose them), then the guardian's own footer and
// one-click unsubscribe. It never carries the notice's link or any token of
// the teen's.
func (a *App) guardianCopyEmailMessage(title, body string, k notify.Kind, teen, guardian *models.User, footer footerFacts) mail.Message {
	text := "Copy: sent to " + teen.PublicName() + " because your church sends guardian copies.\n\n" + title
	if body != "" {
		text += "\n\n" + body
	}
	token := a.unsubscribeToken(guardian.ID, categoryScope(k.Category), guardian.Email)
	text += a.optionalEmailFooter(footer, k.Category, token)
	return mail.Message{To: guardian.Email, Subject: "Copy: " + title, Text: text, Optional: true, ListUnsubscribe: a.oneClickURL(token)}
}
