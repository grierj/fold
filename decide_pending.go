package main

import (
	"errors"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// errAlreadyHandled: a pending item (group, guardian link, account request)
// was decided by someone else first.
var errAlreadyHandled = errors.New("already handled")

// decidePendingIn is the one conditional UPDATE every approval queue shares: it
// moves the row from its pending status to the decided one only while it is
// still pending, and answers errAlreadyHandled when another decision won.
func decidePendingIn(tx *gorm.DB, model any, id uint, pending string, updates map[string]any) error {
	res := tx.Model(model).Where("id = ? AND status = ?", id, pending).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errAlreadyHandled
	}
	return nil
}

// answerAlreadyHandled answers 409 with who got there first when err says the
// item was already decided, and reports whether it did.
func (a *App) answerAlreadyHandled(c *gin.Context, err error, kind string, subjectID uint) bool {
	if !errors.Is(err, errAlreadyHandled) {
		return false
	}
	a.alreadyHandled(c, kind, subjectID)
	return true
}
