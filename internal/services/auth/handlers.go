package auth

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/user/wialon-billing-api/internal/models"
	"github.com/user/wialon-billing-api/internal/repository"
)

const (
	// Админские настройки
	adminEmail = "chudin@glomos.kz"
	adminCode  = "220475"

	// Время жизни OTP кода
	otpExpirationMinutes = 5

	// Максимум подключений на пользователя
	maxConnections = 20
)

// AuthHandler - обработчики авторизации
type AuthHandler struct {
	repo *repository.Repository
}

// NewAuthHandler создаёт новый обработчик авторизации
func NewAuthHandler(repo *repository.Repository) *AuthHandler {
	return &AuthHandler{repo: repo}
}

// RequestCodeRequest - запрос на отправку кода
type RequestCodeRequest struct {
	Email string `json:"email" binding:"required,email"`
}

// RequestCode отправляет OTP код на email
func (h *AuthHandler) RequestCode(c *gin.Context) {
	var req RequestCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Введите корректный email"})
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))

	// Находим или создаём пользователя
	user, err := h.repo.GetUserByEmail(email)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка сервера"})
		return
	}

	if user == nil {
		// Создаём нового пользователя
		user = &models.User{
			Email:   email,
			IsAdmin: email == adminEmail,
		}
		if err := h.repo.CreateUser(user); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка создания пользователя"})
			return
		}
	}

	// Генерируем OTP код
	var code string
	if email == adminEmail {
		// Для админа — постоянный код
		code = adminCode
	} else {
		code = GenerateOTPCode()
	}

	// Сохраняем код в БД
	otp := &models.OTPCode{
		UserID:    user.ID,
		Code:      code,
		ExpiresAt: time.Now().Add(otpExpirationMinutes * time.Minute),
	}
	if err := h.repo.CreateOTPCode(otp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка создания кода"})
		return
	}

	// TODO: Отправка email (пока только для не-админов)
	// Для MVP просто логируем код
	if email != adminEmail {
		// В будущем здесь будет отправка email
		// email.Send(email, "Ваш код: " + code)
		println("OTP код для", email, ":", code)
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Код отправлен на " + email,
		"email":   email,
	})
}

// VerifyCodeRequest - запрос на проверку кода
type VerifyCodeRequest struct {
	Email string `json:"email" binding:"required,email"`
	Code  string `json:"code" binding:"required,len=6"`
}

// VerifyCode проверяет OTP код и выдаёт JWT
func (h *AuthHandler) VerifyCode(c *gin.Context) {
	var req VerifyCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный формат запроса"})
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))

	// Находим пользователя
	user, err := h.repo.GetUserByEmail(email)
	if err != nil || user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Пользователь не найден"})
		return
	}

	// Проверяем код
	otp, err := h.repo.VerifyOTPCode(user.ID, req.Code)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка проверки кода"})
		return
	}

	if otp == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Неверный или просроченный код"})
		return
	}

	// Помечаем код как использованный
	h.repo.MarkOTPCodeUsed(otp.ID)

	// Генерируем JWT токен
	token, err := GenerateJWT(user.ID, user.Email, user.IsAdmin, user.Role, user.DealerAccountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка генерации токена"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"user": gin.H{
			"id":                user.ID,
			"email":             user.Email,
			"is_admin":          user.IsAdmin,
			"role":              user.Role,
			"dealer_account_id": user.DealerAccountID,
		},
	})
}

// GetCurrentUser возвращает данные текущего пользователя
func (h *AuthHandler) GetCurrentUser(c *gin.Context) {
	userID, exists := c.Get("userID")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Не авторизован"})
		return
	}

	user, err := h.repo.GetUserByID(userID.(uint))
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Пользователь не найден"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":                user.ID,
		"email":             user.Email,
		"is_admin":          user.IsAdmin,
		"role":              user.Role,
		"dealer_account_id": user.DealerAccountID,
	})
}
