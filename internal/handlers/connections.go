package handlers

import (
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/user/wialon-billing-api/internal/models"
	"github.com/user/wialon-billing-api/internal/repository"
	"github.com/user/wialon-billing-api/internal/services/wialon"
)

const maxConnections = 20

// ConnectionHandler - обработчики для Wialon подключений
type ConnectionHandler struct {
	repo   *repository.Repository
	wialon *wialon.Client
}

// NewConnectionHandler создаёт новый обработчик подключений
func NewConnectionHandler(repo *repository.Repository, wialonClient *wialon.Client) *ConnectionHandler {
	return &ConnectionHandler{
		repo:   repo,
		wialon: wialonClient,
	}
}

// GetConnections возвращает список подключений пользователя
func (h *ConnectionHandler) GetConnections(c *gin.Context) {
	userID, exists := c.Get("userID")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Не авторизован"})
		return
	}

	connections, err := h.repo.GetConnectionsByUserID(userID.(uint))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, connections)
}

// CreateConnectionRequest - запрос на создание подключения
type CreateConnectionRequest struct {
	Name       string `json:"name" binding:"required"`
	WialonHost string `json:"host" binding:"required"`
	Token      string `json:"token" binding:"required"`
}

// CreateConnection создаёт новое подключение
func (h *ConnectionHandler) CreateConnection(c *gin.Context) {
	userID, exists := c.Get("userID")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Не авторизован"})
		return
	}

	var req CreateConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный формат запроса"})
		return
	}

	// Проверка лимита подключений
	count, err := h.repo.CountConnectionsByUserID(userID.(uint))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if count >= maxConnections {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Достигнут лимит подключений (максимум 20)"})
		return
	}

	// Проверка токена через Wialon API (получаем данные пользователя)
	// TODO: Валидация токена через Wialon API
	// Пока сохраняем без проверки

	conn := &models.WialonConnection{
		UserID:     userID.(uint),
		Name:       req.Name,
		WialonHost: req.WialonHost,
		Token:      req.Token,
	}

	if err := h.repo.CreateConnection(conn); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка создания подключения"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"id":      conn.ID,
		"name":    conn.Name,
		"host":    conn.WialonHost,
		"message": "Подключение создано",
	})
}

// UpdateConnectionRequest - запрос на обновление подключения
type UpdateConnectionRequest struct {
	Name  string `json:"name"`
	Token string `json:"token"`
}

// UpdateConnection обновляет подключение
func (h *ConnectionHandler) UpdateConnection(c *gin.Context) {
	userID, exists := c.Get("userID")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Не авторизован"})
		return
	}

	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID"})
		return
	}

	// Получаем подключение
	conn, err := h.repo.GetConnectionByID(uint(id))
	if err != nil || conn == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Подключение не найдено"})
		return
	}

	// Проверка принадлежности
	if conn.UserID != userID.(uint) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Нет доступа"})
		return
	}

	var req UpdateConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный формат запроса"})
		return
	}

	if req.Name != "" {
		conn.Name = req.Name
	}
	if req.Token != "" {
		conn.Token = req.Token
	}

	if err := h.repo.UpdateConnection(conn); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка обновления"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Подключение обновлено"})
}

// DeleteConnection удаляет подключение
func (h *ConnectionHandler) DeleteConnection(c *gin.Context) {
	userID, exists := c.Get("userID")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Не авторизован"})
		return
	}

	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID"})
		return
	}

	// Получаем подключение
	conn, err := h.repo.GetConnectionByID(uint(id))
	if err != nil || conn == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Подключение не найдено"})
		return
	}

	// Проверка принадлежности
	if conn.UserID != userID.(uint) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Нет доступа"})
		return
	}

	if err := h.repo.DeleteConnection(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка удаления"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Подключение удалено"})
}

// TestConnection проверяет подключение к Wialon
func (h *ConnectionHandler) TestConnection(c *gin.Context) {
	userID, exists := c.Get("userID")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Не авторизован"})
		return
	}

	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID"})
		return
	}

	// Получаем подключение
	conn, err := h.repo.GetConnectionByID(uint(id))
	if err != nil || conn == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Подключение не найдено"})
		return
	}

	// Проверка принадлежности
	if conn.UserID != userID.(uint) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Нет доступа"})
		return
	}

	// Создаём Wialon клиент и проверяем подключение
	wialonURL := "https://" + conn.WialonHost
	wialonClient := wialon.NewClientWithToken(wialonURL, conn.Token)

	log.Printf("[TestConnection] Testing connection %d: URL=%s, TokenPrefix=%s", conn.ID, wialonURL, conn.Token[:20])

	if err := wialonClient.Login(); err != nil {
		log.Printf("[TestConnection] Error for connection %d: %v", conn.ID, err)
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Ошибка подключения: " + err.Error(),
		})
		return
	}

	// Получаем информацию о пользователе
	userName := wialonClient.GetCurrentUserName()
	userWialonID := wialonClient.GetCurrentUserID()

	// Обновляем аккаунт Wialon в подключении
	conn.AccountName = userName
	conn.WialonUserID = userWialonID
	_ = h.repo.UpdateConnection(conn)
	c.JSON(http.StatusOK, gin.H{
		"success":     true,
		"message":     "Подключение успешно",
		"wialon_user": userName,
		"wialon_id":   userWialonID,
	})
}
