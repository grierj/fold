package main

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"fold/models"

	"github.com/gin-gonic/gin"
)

const contactHistoryPeopleLimit = 50

type contactHistoryPersonJSON struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type contactHistoryPeopleJSON struct {
	People []contactHistoryPersonJSON `json:"people"`
}

func (a *App) apiContactHistoryPeople(c *gin.Context) {
	if !a.canViewContactHistoryAt(apiUser(c), time.Now().UTC()) {
		jsonError(c, http.StatusForbidden, "admin required")
		return
	}

	out := contactHistoryPeopleJSON{People: []contactHistoryPersonJSON{}}
	db := a.DB.WithContext(c.Request.Context()).Select("id", "name", "display_name", "email")
	// Exactly one lookup mode is active, with precedence id > email > q.
	if rawID, provided := c.GetQuery("id"); provided {
		id, valid := historyPositiveID(rawID)
		if !valid {
			c.JSON(http.StatusOK, out)
			return
		}
		db = db.Where("id = ?", id)
	} else if rawEmail, provided := c.GetQuery("email"); provided {
		email := strings.TrimSpace(rawEmail)
		if strings.ContainsRune(email, 0) || utf8.RuneCountInString(email) > 254 {
			c.JSON(http.StatusOK, out)
			return
		}
		db = db.Where("email = ?", email)
	} else {
		query := strings.TrimSpace(c.Query("q"))
		if strings.ContainsRune(query, 0) || utf8.RuneCountInString(query) > 254 {
			c.JSON(http.StatusOK, out)
			return
		}
		if query != "" {
			pattern := likeContains(query)
			db = db.Where("name ILIKE ? ESCAPE '\\' OR display_name ILIKE ? ESCAPE '\\' OR email ILIKE ? ESCAPE '\\'", pattern, pattern, pattern)
		}
	}

	var users []models.User
	if err := db.Order("LOWER(COALESCE(NULLIF(BTRIM(display_name), ''), NULLIF(BTRIM(name), ''), 'Member')), id").Limit(contactHistoryPeopleLimit).Find(&users).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load people")
		return
	}
	for i := range users {
		u := &users[i]
		out.People = append(out.People, contactHistoryPersonJSON{ID: historyID(u.ID), Name: u.PublicName(), Email: u.Email})
	}
	c.JSON(http.StatusOK, out)
}
