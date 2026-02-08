package auth

import (
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWT секрет (в продакшене должен браться из конфига)
var jwtSecret = []byte("wialon-billing-secret-key-change-in-production")

// JWTClaims - claims для JWT токена
type JWTClaims struct {
	UserID           uint   `json:"user_id"`
	Email            string `json:"email"`
	IsAdmin          bool   `json:"is_admin"`
	Role             string `json:"role"`
	DealerAccountID  *int64 `json:"dealer_account_id,omitempty"`
	PartnerAccountID *int64 `json:"partner_account_id,omitempty"`
	jwt.RegisteredClaims
}

// GenerateOTPCode генерирует 6-значный код
func GenerateOTPCode() string {
	b := make([]byte, 4)
	rand.Read(b)
	// Берём число 0-999999 и форматируем как 6 цифр
	num := (int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])) % 1000000
	return fmt.Sprintf("%06d", num)
}

// GenerateJWT генерирует JWT токен
func GenerateJWT(userID uint, email string, isAdmin bool, role string, dealerAccountID *int64, partnerAccountID *int64) (string, error) {
	claims := JWTClaims{
		UserID:           userID,
		Email:            email,
		IsAdmin:          isAdmin,
		Role:             role,
		DealerAccountID:  dealerAccountID,
		PartnerAccountID: partnerAccountID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "wialon-billing",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

// ValidateJWT проверяет JWT токен и возвращает claims
func ValidateJWT(tokenString string) (*JWTClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &JWTClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("неверный метод подписи")
		}
		return jwtSecret, nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*JWTClaims); ok && token.Valid {
		return claims, nil
	}

	return nil, errors.New("неверный токен")
}
