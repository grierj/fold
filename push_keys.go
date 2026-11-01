package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log"
	"strings"
	"sync"

	"fold/internal/audit"
	"fold/internal/mail"
	"fold/internal/notify"
	"fold/internal/push"
	"fold/migrations"
	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Web push keys (M3.13a, research 2.1, DEC-125). The VAPID key pair is made
// on first boot and kept in settings: the public half as browsers take it,
// the private half AES-GCM encrypted with a key derived from SESSION_SECRET.
// Nothing for an admin to set up, and no new env var.

const (
	settingVAPIDPublic  = "vapid_public_key"
	settingVAPIDPrivate = "vapid_private_key_enc"
	// settingPushEnabled is the church's switch (DEC-124, default on). The
	// admin control arrives in M3.13c2; delivery reads it now.
	settingPushEnabled = "push_enabled"
)

// vapidAAD binds the ciphertext to what it is.
const vapidAAD = "vapid-private-key"

// errVAPIDUnreadable: the stored private key doesn't decrypt with this
// SESSION_SECRET (it changed), or isn't a key.
var errVAPIDUnreadable = errors.New("push: the stored web push key can't be read with this SESSION_SECRET")

func vapidCipher(secret []byte) (cipher.AEAD, error) {
	if len(secret) == 0 {
		return nil, errors.New("push: SESSION_SECRET is empty")
	}
	key := sha256.Sum256(append([]byte("fold:vapid:v1:"), secret...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealVAPIDKey encrypts key for the settings row.
func sealVAPIDKey(secret []byte, key *ecdsa.PrivateKey) (string, error) {
	crypt, err := vapidCipher(secret)
	if err != nil {
		return "", err
	}
	raw, err := push.MarshalPrivateKey(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, crypt.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(crypt.Seal(nonce, nonce, raw, []byte(vapidAAD))), nil
}

// openVAPIDKey decrypts a sealed key; errVAPIDUnreadable when it can't.
func openVAPIDKey(secret []byte, enc string) (*ecdsa.PrivateKey, error) {
	crypt, err := vapidCipher(secret)
	if err != nil {
		return nil, err
	}
	data, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || len(data) < crypt.NonceSize()+crypt.Overhead() {
		return nil, errVAPIDUnreadable
	}
	raw, err := crypt.Open(nil, data[:crypt.NonceSize()], data[crypt.NonceSize():], []byte(vapidAAD))
	if err != nil {
		return nil, errVAPIDUnreadable
	}
	key, err := push.ParsePrivateKey(raw)
	if err != nil {
		return nil, errVAPIDUnreadable
	}
	return key, nil
}

// readVAPID reads the two settings ("" when missing).
func readVAPID(db *gorm.DB) (public, enc string, err error) {
	var rows []models.Setting
	if err := db.Where("key IN ?", []string{settingVAPIDPublic, settingVAPIDPrivate}).Find(&rows).Error; err != nil {
		return "", "", err
	}
	for _, r := range rows {
		switch r.Key {
		case settingVAPIDPublic:
			public = r.Value
		case settingVAPIDPrivate:
			enc = r.Value
		}
	}
	return public, enc, nil
}

// lockVAPID takes the migrations advisory lock for the rest of tx, so
// processes booting together (or a rotation from the CLI) make one pair.
func lockVAPID(tx *gorm.DB) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(?)", migrations.LockID).Error
}

// writeVAPIDKeys stores a new pair and removes every device, which was
// bound to the old public key and can't be used with the new one. It
// returns how many devices it removed.
func writeVAPIDKeys(tx *gorm.DB, secret []byte) (int64, error) {
	key, err := push.GenerateKey()
	if err != nil {
		return 0, err
	}
	public, err := push.PublicKey(key)
	if err != nil {
		return 0, err
	}
	enc, err := sealVAPIDKey(secret, key)
	if err != nil {
		return 0, err
	}
	if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"})}).
		Create(&[]models.Setting{{Key: settingVAPIDPublic, Value: public}, {Key: settingVAPIDPrivate, Value: enc}}).Error; err != nil {
		return 0, err
	}
	res := tx.Exec("DELETE FROM push_subscriptions")
	return res.RowsAffected, res.Error
}

// ensureVAPIDKeys runs at boot: it makes the pair if it is missing, and
// makes it again if the private half no longer decrypts (SESSION_SECRET
// changed), removing every device and telling the admins in the same
// transaction. It returns the notices written, for email after commit.
func ensureVAPIDKeys(db *gorm.DB, secret []byte) ([]uint, error) {
	var notices []uint
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := lockVAPID(tx); err != nil {
			return err
		}
		public, enc, err := readVAPID(tx)
		if err != nil {
			return err
		}
		if public == "" || enc == "" {
			_, err := writeVAPIDKeys(tx, secret)
			return err
		}
		key, err := openVAPIDKey(secret, enc)
		if err == nil {
			if p, perr := push.PublicKey(key); perr == nil && p == public {
				return nil
			}
		} else if !errors.Is(err, errVAPIDUnreadable) {
			return err
		}
		removed, err := writeVAPIDKeys(tx, secret)
		if err != nil {
			return err
		}
		log.Printf("push: %v; made new web push keys and removed %d devices (members need to turn notifications on again)", errVAPIDUnreadable, removed)
		if err := audit.Write(tx, audit.Entry{Action: audit.PushKeysReset, TargetType: "setting",
			Meta: map[string]any{"devicesRemoved": removed}}); err != nil {
			return err
		}
		admins, err := activeAdminIDs(tx)
		if err != nil {
			return err
		}
		notices, err = notify.Send(tx, notify.Msg{Kind: notify.PushKeysReset, To: admins, SubjectType: "setting", Link: "/admin?tab=system"})
		return err
	})
	if err != nil {
		return nil, err
	}
	return notices, nil
}

// rotateVAPIDKeys is `fold push keys rotate`: a new pair, every device
// removed. A running server picks the new key up on its next send.
func rotateVAPIDKeys(db *gorm.DB, secret []byte) (int64, error) {
	var removed int64
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := lockVAPID(tx); err != nil {
			return err
		}
		var err error
		if removed, err = writeVAPIDKeys(tx, secret); err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Action: audit.PushKeysRotated, TargetType: "setting",
			Meta: map[string]any{"devicesRemoved": removed}})
	})
	return removed, err
}

// vapidCache keeps the decrypted key while the stored ciphertext is the
// same. Every send rechecks the settings row, so a key rotated by another
// process is used without a restart.
type vapidCache struct {
	mu  sync.Mutex
	enc string
	key *ecdsa.PrivateKey
}

// vapidKey is the current VAPID private key.
func (a *App) vapidKey(ctx context.Context) (*ecdsa.PrivateKey, error) {
	var s models.Setting
	if err := a.DB.WithContext(ctx).Select("value").Where("key = ?", settingVAPIDPrivate).First(&s).Error; err != nil {
		return nil, err
	}
	a.vapid.mu.Lock()
	defer a.vapid.mu.Unlock()
	if a.vapid.key != nil && a.vapid.enc == s.Value {
		return a.vapid.key, nil
	}
	key, err := openVAPIDKey(a.Config.SessionSecret, s.Value)
	if err != nil {
		return nil, err
	}
	a.vapid.enc, a.vapid.key = s.Value, key
	return key, nil
}

// vapidSubject is the VAPID sub claim: the From address, unless it is the
// localhost default (push services refuse it), else SITE_URL.
func vapidSubject(cfg Config) string {
	from := mail.Address(cfg.EmailFrom)
	if from != "" && strings.Contains(from, "@") && !strings.HasSuffix(from, "@localhost") {
		return "mailto:" + from
	}
	if cfg.SiteURL != "" {
		return strings.TrimSuffix(cfg.SiteURL, "/")
	}
	return "mailto:" + from
}

// pushClient is the App's push sender: a.Push when set (tests), else one
// built from the config, with the safe outbound client.
func (a *App) pushClient() *push.Client {
	a.pushOnce.Do(func() {
		if a.Push == nil {
			a.Push = &push.Client{Hosts: push.NewAllowList(a.Config.PushHosts...), Subject: vapidSubject(a.Config)}
		}
		if a.Push.Key == nil {
			a.Push.Key = a.vapidKey
		}
	})
	return a.Push
}
