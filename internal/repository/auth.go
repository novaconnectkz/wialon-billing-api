package repository

import (
	"time"

	"github.com/user/wialon-billing-api/internal/models"
	"gorm.io/gorm"
)

// === Users ===

// GetUserByEmail находит пользователя по email
func (r *Repository) GetUserByEmail(email string) (*models.User, error) {
	var user models.User
	if err := r.db.Where("email = ?", email).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &user, nil
}

// GetUserByID находит пользователя по ID
func (r *Repository) GetUserByID(id uint) (*models.User, error) {
	var user models.User
	if err := r.db.First(&user, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &user, nil
}

// CreateUser создаёт нового пользователя
func (r *Repository) CreateUser(user *models.User) error {
	return r.db.Create(user).Error
}

// UpdateUser обновляет данные пользователя
func (r *Repository) UpdateUser(user *models.User) error {
	return r.db.Save(user).Error
}

// === OTP Codes ===

// CreateOTPCode создаёт новый OTP код
func (r *Repository) CreateOTPCode(otp *models.OTPCode) error {
	// Помечаем все старые коды как использованные
	r.db.Model(&models.OTPCode{}).
		Where("user_id = ? AND used = ?", otp.UserID, false).
		Update("used", true)
	return r.db.Create(otp).Error
}

// VerifyOTPCode проверяет OTP код
func (r *Repository) VerifyOTPCode(userID uint, code string) (*models.OTPCode, error) {
	var otp models.OTPCode
	if err := r.db.Where(
		"user_id = ? AND code = ? AND used = ? AND expires_at > ?",
		userID, code, false, time.Now(),
	).First(&otp).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &otp, nil
}

// MarkOTPCodeUsed помечает код как использованный
func (r *Repository) MarkOTPCodeUsed(id uint) error {
	return r.db.Model(&models.OTPCode{}).Where("id = ?", id).Update("used", true).Error
}

// === Wialon Connections ===

// GetConnectionsByUserID возвращает все подключения пользователя
func (r *Repository) GetConnectionsByUserID(userID uint) ([]models.WialonConnection, error) {
	var connections []models.WialonConnection
	if err := r.db.Where("user_id = ?", userID).Order("created_at DESC").Find(&connections).Error; err != nil {
		return nil, err
	}
	return connections, nil
}

// GetConnectionByID возвращает подключение по ID
func (r *Repository) GetConnectionByID(id uint) (*models.WialonConnection, error) {
	var conn models.WialonConnection
	if err := r.db.First(&conn, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &conn, nil
}

// CreateConnection создаёт новое подключение
func (r *Repository) CreateConnection(conn *models.WialonConnection) error {
	return r.db.Create(conn).Error
}

// UpdateConnection обновляет подключение
func (r *Repository) UpdateConnection(conn *models.WialonConnection) error {
	return r.db.Save(conn).Error
}

// DeleteConnection удаляет подключение
func (r *Repository) DeleteConnection(id uint) error {
	return r.db.Delete(&models.WialonConnection{}, id).Error
}

// CountConnectionsByUserID подсчитывает количество подключений пользователя
func (r *Repository) CountConnectionsByUserID(userID uint) (int64, error) {
	var count int64
	if err := r.db.Model(&models.WialonConnection{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// GetAllConnections возвращает все подключения в системе (для синхронизации)
func (r *Repository) GetAllConnections() ([]models.WialonConnection, error) {
	var connections []models.WialonConnection
	if err := r.db.Find(&connections).Error; err != nil {
		return nil, err
	}
	return connections, nil
}
