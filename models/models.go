package models

import (
	"time"

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
	Tags        string `gorm:"type:text"` // Store tags as comma-separated string
	Approved    bool   `gorm:"default:false"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   gorm.DeletedAt `gorm:"index"`
}

type User struct {
	ID        uint   `gorm:"primaryKey"`
	Username  string `gorm:"unique;not null"`
	IsAdmin   bool   `gorm:"default:false"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}
