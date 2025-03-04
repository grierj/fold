package models

import (
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type Group struct {
	ID          uint      `gorm:"primaryKey"`
	Title       string    `gorm:"not null"`
	DateTime    time.Time `gorm:"not null"`
	IsRecurring bool      `gorm:"default:false"`
	Description string    `gorm:"not null"`
	ImageURL    string
	CreatorID   uint   `gorm:"not null"`
	Creator     User   `gorm:"foreignKey:CreatorID"`
	Tags        string `gorm:"type:text"`
	Approved    bool   `gorm:"default:false"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   gorm.DeletedAt `gorm:"index"`
}

type User struct {
	ID        uint   `gorm:"primaryKey"`
	Username  string `gorm:"unique;not null"`
	Password  string `gorm:"not null"` // Hashed password
	IsAdmin   bool   `gorm:"default:false"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

// HashPassword hashes the password before saving
func (u *User) BeforeCreate(tx *gorm.DB) (err error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.Password = string(hashed)
	return nil
}

// CheckPassword compares a password with the stored hash
func (u *User) CheckPassword(password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(password)) == nil
}
