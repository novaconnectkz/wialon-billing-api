package handlers

import (
	"context"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/user/wialon-billing-api/internal/models"
	"github.com/user/wialon-billing-api/internal/services/ai"
)

// AIHandler - обработчики для AI эндпоинтов
type AIHandler struct {
	aiService *ai.Service
}

// NewAIHandler создаёт новый обработчик AI
func NewAIHandler(aiService *ai.Service) *AIHandler {
	return &AIHandler{
		aiService: aiService,
	}
}

// GetAISettings возвращает настройки AI
func (h *AIHandler) GetAISettings(c *gin.Context) {
	settings := h.aiService.GetSettings()
	if settings == nil {
		// Возвращаем дефолтные настройки для DeepSeek
		settings = &models.AISettings{
			Enabled:          false,
			AnalysisModel:    ai.ModelReasonerR1,
			SupportModel:     ai.ModelChatV3,
			MaxTokens:        2500,
			RateLimitPerHour: 1,
			CacheTTLHours:    24,
		}
	}

	// Маскируем API ключ для безопасности
	response := gin.H{
		"id":                  settings.ID,
		"enabled":             settings.Enabled,
		"analysis_model":      settings.AnalysisModel,
		"support_model":       settings.SupportModel,
		"max_tokens":          settings.MaxTokens,
		"rate_limit_per_hour": settings.RateLimitPerHour,
		"cache_ttl_hours":     settings.CacheTTLHours,
		"privacy_mode":        settings.PrivacyMode,
		"updated_at":          settings.UpdatedAt,
		"has_api_key":         settings.APIKey != "",
	}

	c.JSON(http.StatusOK, response)
}

// UpdateAISettings обновляет настройки AI
func (h *AIHandler) UpdateAISettings(c *gin.Context) {
	var req struct {
		Enabled          bool   `json:"enabled"`
		APIKey           string `json:"api_key"`
		AnalysisModel    string `json:"analysis_model"`
		SupportModel     string `json:"support_model"`
		MaxTokens        int    `json:"max_tokens"`
		RateLimitPerHour int    `json:"rate_limit_per_hour"`
		CacheTTLHours    int    `json:"cache_ttl_hours"`
		PrivacyMode      bool   `json:"privacy_mode"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Получаем текущие настройки
	settings := h.aiService.GetSettings()
	if settings == nil {
		settings = &models.AISettings{}
	}

	// Обновляем поля
	settings.Enabled = req.Enabled
	settings.AnalysisModel = req.AnalysisModel
	settings.SupportModel = req.SupportModel
	settings.MaxTokens = req.MaxTokens
	settings.RateLimitPerHour = req.RateLimitPerHour
	settings.CacheTTLHours = req.CacheTTLHours
	settings.PrivacyMode = req.PrivacyMode

	// Обновляем API ключ только если передан новый
	if req.APIKey != "" {
		settings.APIKey = req.APIKey
	}

	// Сохраняем и переинициализируем
	if err := h.aiService.UpdateSettings(c.Request.Context(), settings); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Настройки AI обновлены"})
}

// GetAIUsage возвращает статистику использования AI
func (h *AIHandler) GetAIUsage(c *gin.Context) {
	// Период в днях (по умолчанию 30)
	days := 30
	if daysStr := c.Query("days"); daysStr != "" {
		if d, err := strconv.Atoi(daysStr); err == nil && d > 0 && d <= 365 {
			days = d
		}
	}

	stats, err := h.aiService.GetUsageStats(days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"days":  days,
		"stats": stats,
	})
}

// GetAIInsights возвращает активные инсайты
func (h *AIHandler) GetAIInsights(c *gin.Context) {
	insights, err := h.aiService.GetActiveInsights()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, insights)
}

// GetAccountInsights возвращает инсайты для конкретного аккаунта
func (h *AIHandler) GetAccountInsights(c *gin.Context) {
	idStr := c.Param("account_id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID аккаунта"})
		return
	}

	insights, err := h.aiService.GetInsightsByAccount(uint(id))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, insights)
}

// TriggerAnalysis запускает ручной анализ
func (h *AIHandler) TriggerAnalysis(c *gin.Context) {
	if !h.aiService.IsEnabled() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "AI сервис не настроен. Укажите API ключ DeepSeek в настройках."})
		return
	}

	// Запускаем анализ асинхронно с фоновым контекстом
	// Важно: используем context.Background() вместо c.Request.Context()
	// потому что HTTP запрос завершится раньше анализа
	go func() {
		ctx := context.Background()
		if err := h.aiService.AnalyzeLatestSnapshots(ctx); err != nil {
			log.Printf("[AI] Ошибка фонового анализа: %v", err)
		}
	}()

	c.JSON(http.StatusOK, gin.H{"message": "Анализ запущен"})
}

// SendInsightFeedback сохраняет обратную связь по инсайту
func (h *AIHandler) SendInsightFeedback(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID инсайта"})
		return
	}

	var req struct {
		Helpful bool   `json:"helpful"`
		Comment string `json:"comment"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.aiService.SendFeedback(uint(id), req.Helpful, req.Comment); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Спасибо за обратную связь!"})
}
