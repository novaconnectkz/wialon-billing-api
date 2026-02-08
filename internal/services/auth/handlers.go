package auth

import (
	"encoding/json"
	"fmt"
	"io"
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
		// Определяем роль нового пользователя
		role := "admin"
		var partnerWialonID *int64

		// Проверяем, совпадает ли email с buyer_email какого-либо аккаунта
		account, _ := h.repo.GetAccountByBuyerEmail(email)
		if account != nil {
			role = "partner"
			partnerWialonID = &account.WialonID
		}

		user = &models.User{
			Email:            email,
			IsAdmin:          email == adminEmail,
			Role:             role,
			PartnerAccountID: partnerWialonID,
		}
		if err := h.repo.CreateUser(user); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка создания пользователя"})
			return
		}
	} else {
		// Для существующего пользователя — обновляем привязку к аккаунту
		account, _ := h.repo.GetAccountByBuyerEmail(email)
		if account != nil {
			if user.Role != "partner" || user.PartnerAccountID == nil || *user.PartnerAccountID != account.WialonID {
				user.Role = "partner"
				user.PartnerAccountID = &account.WialonID
				h.repo.UpdateUser(user)
			}
		} else if user.Role == "partner" {
			// Email больше не привязан к аккаунту — сбрасываем роль
			user.Role = "admin"
			user.PartnerAccountID = nil
			h.repo.UpdateUser(user)
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
	token, err := GenerateJWT(user.ID, user.Email, user.IsAdmin, user.Role, user.DealerAccountID, user.PartnerAccountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка генерации токена"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"user": gin.H{
			"id":                 user.ID,
			"email":              user.Email,
			"is_admin":           user.IsAdmin,
			"role":               user.Role,
			"dealer_account_id":  user.DealerAccountID,
			"partner_account_id": user.PartnerAccountID,
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
		"id":                 user.ID,
		"email":              user.Email,
		"is_admin":           user.IsAdmin,
		"role":               user.Role,
		"dealer_account_id":  user.DealerAccountID,
		"partner_account_id": user.PartnerAccountID,
	})
}

// WialonLoginRequest - запрос на авторизацию через Wialon OAuth
type WialonLoginRequest struct {
	AccessToken string `json:"access_token" binding:"required"`
}

// WialonLogin авторизует партнёра через Wialon OAuth токен
func (h *AuthHandler) WialonLogin(c *gin.Context) {
	var req WialonLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Требуется access_token"})
		return
	}

	// Вызываем Wialon API token/login для получения информации о пользователе
	wialonUser, err := wialonTokenLogin(req.AccessToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Ошибка авторизации через Wialon: " + err.Error()})
		return
	}

	// Ищем аккаунт в нашей БД по Wialon ID
	account, err := h.repo.GetAccountByWialonID(wialonUser.AccountID)
	if err != nil || account == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Аккаунт не найден в системе биллинга"})
		return
	}

	// Определяем email для пользователя
	email := wialonUser.Email
	if email == "" {
		email = fmt.Sprintf("wialon_%d@partner.local", wialonUser.UserID)
	}

	// Находим или создаём пользователя
	user, err := h.repo.GetUserByEmail(email)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка сервера"})
		return
	}

	if user == nil {
		partnerWialonID := account.WialonID
		user = &models.User{
			Email:            email,
			IsAdmin:          false,
			Role:             "partner",
			PartnerAccountID: &partnerWialonID,
		}
		if err := h.repo.CreateUser(user); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка создания пользователя"})
			return
		}
	}

	// Генерируем JWT
	token, err := GenerateJWT(user.ID, user.Email, user.IsAdmin, user.Role, user.DealerAccountID, user.PartnerAccountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка генерации токена"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"user": gin.H{
			"id":                 user.ID,
			"email":              user.Email,
			"is_admin":           user.IsAdmin,
			"role":               user.Role,
			"partner_account_id": user.PartnerAccountID,
		},
	})
}

// wialonUserInfo - информация о пользователе Wialon
type wialonUserInfo struct {
	UserID    int64  `json:"id"`
	AccountID int64  `json:"au"` // ID аккаунта в Wialon
	Email     string `json:"em"`
	Name      string `json:"nm"`
}

// wialonTokenLogin вызывает Wialon API token/login
func wialonTokenLogin(accessToken string) (*wialonUserInfo, error) {
	// Используем стандартный Wialon Hosting URL
	url := fmt.Sprintf("https://hst-api.wialon.com/wialon/ajax.html?svc=token/login&params={%%22token%%22:%%22%s%%22}", accessToken)

	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("ошибка запроса к Wialon API: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения ответа: %v", err)
	}

	// Парсим ответ Wialon
	var result struct {
		EID  string `json:"eid"` // session ID
		User struct {
			ID   int64  `json:"id"`
			Name string `json:"nm"`
			Bact int64  `json:"bact"` // billing account ID
		} `json:"user"`
		Error int `json:"error"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("ошибка парсинга ответа Wialon: %v", err)
	}

	if result.Error != 0 {
		return nil, fmt.Errorf("Wialon вернул ошибку: %d", result.Error)
	}

	if result.EID == "" {
		return nil, fmt.Errorf("не удалось авторизоваться в Wialon")
	}

	return &wialonUserInfo{
		UserID:    result.User.ID,
		AccountID: result.User.Bact,
		Name:      result.User.Name,
	}, nil
}
