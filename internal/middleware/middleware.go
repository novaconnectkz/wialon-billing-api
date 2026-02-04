package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/user/wialon-billing-api/internal/services/auth"
)

// CORS middleware для кроссдоменных запросов
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// Auth middleware для проверки JWT авторизации
func Auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Требуется авторизация"})
			c.Abort()
			return
		}

		// Проверка Bearer токена
		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Неверный формат токена"})
			c.Abort()
			return
		}

		tokenString := parts[1]
		if tokenString == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Пустой токен"})
			c.Abort()
			return
		}

		// Валидация JWT
		claims, err := auth.ValidateJWT(tokenString)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Неверный или просроченный токен"})
			c.Abort()
			return
		}

		// Сохраняем данные пользователя в контексте
		c.Set("userID", claims.UserID)
		c.Set("email", claims.Email)
		c.Set("isAdmin", claims.IsAdmin)
		c.Set("role", claims.Role)
		c.Set("dealerAccountID", claims.DealerAccountID)
		c.Set("token", tokenString)
		c.Next()
	}
}

// DealerContext добавляет dealer_account_id в контекст для фильтрации данных
func DealerContext() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Получаем данные из JWT (установлены в Auth middleware)
		role, _ := c.Get("role")
		dealerAccountID, _ := c.Get("dealerAccountID")

		// Устанавливаем флаг для handlers
		if role == "dealer" && dealerAccountID != nil {
			c.Set("filterByDealer", true)
			c.Set("dealerWialonID", dealerAccountID)
		} else {
			c.Set("filterByDealer", false)
		}

		c.Next()
	}
}

// RequireAdmin проверяет, что пользователь — администратор
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, exists := c.Get("role")
		if !exists || (role != "admin" && role != "") {
			// Если роль не задана (пустая) — это legacy admin, пропускаем
			if role != "" {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
					"error": "Доступ запрещён. Требуются права администратора.",
				})
				return
			}
		}
		c.Next()
	}
}
