package models

import (
	"time"
)

// User - пользователь системы
type User struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	Email           string    `gorm:"uniqueIndex;size:255;not null" json:"email"`
	IsAdmin         bool      `gorm:"default:false" json:"is_admin"`
	Role            string    `gorm:"size:20;default:'admin'" json:"role"` // admin, dealer, viewer
	DealerAccountID *int64    `json:"dealer_account_id"`                   // WialonID привязанного дилерского аккаунта
	CreatedAt       time.Time `gorm:"autoCreateTime" json:"created_at"`
}

// OTPCode - одноразовый код для входа
type OTPCode struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"not null" json:"user_id"`
	Code      string    `gorm:"size:6;not null" json:"code"`
	ExpiresAt time.Time `gorm:"not null" json:"expires_at"`
	Used      bool      `gorm:"default:false" json:"used"`
	CreatedAt time.Time `gorm:"autoCreateTime" json:"created_at"`
	User      User      `gorm:"foreignKey:UserID" json:"-"`
}

// WialonConnection - подключение к Wialon
type WialonConnection struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	UserID       uint      `gorm:"not null" json:"user_id"`
	Name         string    `gorm:"size:255" json:"name"`          // Название подключения
	WialonHost   string    `gorm:"size:255;not null" json:"host"` // hst-api.wialon.com
	Token        string    `gorm:"size:100;not null" json:"-"`    // 72-символьный токен (скрыт в JSON)
	WialonUserID int64     `json:"wialon_user_id"`                // ID пользователя в Wialon
	AccountName  string    `gorm:"size:255" json:"account_name"`  // Имя аккаунта Wialon
	CreatedAt    time.Time `gorm:"autoCreateTime" json:"created_at"`
	User         User      `gorm:"foreignKey:UserID" json:"-"`
}
