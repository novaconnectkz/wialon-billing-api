package ai

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/user/wialon-billing-api/internal/models"
	"github.com/user/wialon-billing-api/internal/repository"
	"golang.org/x/time/rate"
)

// Service - сервис AI аналитики
type Service struct {
	repo        *repository.Repository
	client      *Client
	rateLimiter *rate.Limiter
	settings    *models.AISettings
	mu          sync.RWMutex
}

// NewService создаёт новый сервис AI
func NewService(repo *repository.Repository) *Service {
	return &Service{
		repo:        repo,
		rateLimiter: rate.NewLimiter(rate.Every(time.Hour), 1), // 1 запрос в час по умолчанию
	}
}

// Initialize инициализирует AI клиент из настроек в БД
func (s *Service) Initialize(ctx context.Context) error {
	settings, err := s.repo.GetAISettings()
	if err != nil {
		log.Printf("[AI] Ошибка загрузки настроек: %v", err)
		return err
	}

	if settings == nil {
		// Создаём настройки по умолчанию
		settings = &models.AISettings{
			Enabled:          false,
			Model:            "gemini-1.5-flash",
			RateLimitPerHour: 1,
			CacheTTLHours:    24,
		}
		if err := s.repo.SaveAISettings(settings); err != nil {
			log.Printf("[AI] Ошибка сохранения настроек по умолчанию: %v", err)
		}
	}

	s.mu.Lock()
	s.settings = settings
	s.mu.Unlock()

	// Обновляем rate limiter
	s.updateRateLimiter(settings.RateLimitPerHour)

	if settings.Enabled && settings.APIKey != "" {
		client, err := NewClient(ctx, settings.APIKey, settings.Model)
		if err != nil {
			log.Printf("[AI] Ошибка инициализации клиента: %v", err)
			return err
		}
		s.mu.Lock()
		s.client = client
		s.mu.Unlock()
		log.Println("[AI] Сервис успешно инициализирован")
	} else {
		log.Println("[AI] Сервис отключён (нет API ключа или выключен)")
	}

	return nil
}

// updateRateLimiter обновляет лимитер запросов
func (s *Service) updateRateLimiter(requestsPerHour int) {
	if requestsPerHour <= 0 {
		requestsPerHour = 1
	}
	interval := time.Hour / time.Duration(requestsPerHour)
	s.rateLimiter = rate.NewLimiter(rate.Every(interval), 1)
}

// IsEnabled проверяет, активен ли AI сервис
func (s *Service) IsEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.client != nil && s.client.IsEnabled() && s.settings != nil && s.settings.Enabled
}

// GetSettings возвращает текущие настройки
func (s *Service) GetSettings() *models.AISettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// UpdateSettings обновляет настройки AI
func (s *Service) UpdateSettings(ctx context.Context, settings *models.AISettings) error {
	// Сохраняем в БД
	if err := s.repo.SaveAISettings(settings); err != nil {
		return err
	}

	s.mu.Lock()
	s.settings = settings
	s.mu.Unlock()

	// Обновляем rate limiter
	s.updateRateLimiter(settings.RateLimitPerHour)

	// Пересоздаём клиент если нужно
	if settings.Enabled && settings.APIKey != "" {
		client, err := NewClient(ctx, settings.APIKey, settings.Model)
		if err != nil {
			return err
		}
		s.mu.Lock()
		if s.client != nil {
			s.client.Close()
		}
		s.client = client
		s.mu.Unlock()
	}

	return nil
}

// AnalyzeAccount анализирует изменения для одного аккаунта
func (s *Service) AnalyzeAccount(ctx context.Context, account *models.Account, currentSnapshot *models.Snapshot) (*models.AIInsight, error) {
	if !s.IsEnabled() {
		return nil, fmt.Errorf("AI сервис отключён")
	}

	// Проверяем rate limit
	if !s.rateLimiter.Allow() {
		return nil, fmt.Errorf("превышен лимит запросов к AI")
	}

	// Получаем данные для сравнения
	snapshot7dAgo, _ := s.repo.GetSnapshotForDate(account.ID, time.Now().AddDate(0, 0, -7))
	snapshot30dAgo, _ := s.repo.GetSnapshotForDate(account.ID, time.Now().AddDate(0, 0, -30))

	units7dAgo := 0
	units30dAgo := 0
	if snapshot7dAgo != nil {
		units7dAgo = snapshot7dAgo.TotalUnits
	}
	if snapshot30dAgo != nil {
		units30dAgo = snapshot30dAgo.TotalUnits
	}

	// Получаем настройки биллинга для цены
	billingSettings, _ := s.repo.GetSettings()
	unitPrice := 1.0
	currency := "EUR"
	if billingSettings != nil {
		unitPrice = billingSettings.UnitPrice
		currency = billingSettings.Currency
	}

	// Формируем промпт
	userPrompt := fmt.Sprintf(AnalyticsUserPromptTemplate,
		account.Name,
		account.BillingCurrency,
		unitPrice, currency,
		currentSnapshot.TotalUnits,
		currentSnapshot.UnitsCreated,
		currentSnapshot.UnitsDeleted,
		currentSnapshot.UnitsDeactivated,
		units7dAgo, currentSnapshot.TotalUnits-units7dAgo,
		units30dAgo, currentSnapshot.TotalUnits-units30dAgo,
	)

	// Отправляем запрос к AI
	result, err := s.client.Generate(ctx, AnalyticsSystemPrompt, userPrompt)
	if err != nil {
		// Логируем ошибку
		s.logUsage("analyze", 0, 0, 0, false, err.Error())
		return nil, err
	}

	// Логируем успешный запрос
	s.logUsage("analyze", result.InputTokens, result.OutputTokens, result.TotalTokens, true, "")

	// Парсим ответ
	insightResp, err := ParseInsightResponse(result.Response)
	if err != nil {
		return nil, err
	}

	// Создаём инсайт
	s.mu.RLock()
	cacheTTL := 24
	if s.settings != nil {
		cacheTTL = s.settings.CacheTTLHours
	}
	s.mu.RUnlock()

	insight := &models.AIInsight{
		AccountID:       account.ID,
		InsightType:     insightResp.InsightType,
		Severity:        insightResp.Severity,
		Title:           insightResp.Title,
		Description:     insightResp.Description,
		FinancialImpact: &insightResp.FinancialImpact,
		Currency:        currency,
		ExpiresAt:       time.Now().Add(time.Duration(cacheTTL) * time.Hour),
	}

	// Сохраняем инсайт
	if err := s.repo.CreateAIInsight(insight); err != nil {
		return nil, err
	}

	return insight, nil
}

// GetActiveInsights возвращает активные инсайты
func (s *Service) GetActiveInsights() ([]models.AIInsight, error) {
	return s.repo.GetActiveAIInsights()
}

// GetInsightsByAccount возвращает инсайты по аккаунту
func (s *Service) GetInsightsByAccount(accountID uint) ([]models.AIInsight, error) {
	return s.repo.GetAIInsightsByAccount(accountID)
}

// SendFeedback сохраняет обратную связь по инсайту
func (s *Service) SendFeedback(insightID uint, helpful bool, comment string) error {
	return s.repo.UpdateAIInsightFeedback(insightID, helpful, comment)
}

// GetUsageStats возвращает статистику использования
func (s *Service) GetUsageStats(days int) (*UsageStats, error) {
	logs, err := s.repo.GetAIUsageLogs(days)
	if err != nil {
		return nil, err
	}

	stats := &UsageStats{}
	for _, log := range logs {
		stats.TotalRequests++
		stats.TotalTokens += log.TotalTokens
		stats.InputTokens += log.InputTokens
		stats.OutputTokens += log.OutputTokens
		if log.Success {
			stats.SuccessfulRequests++
		} else {
			stats.FailedRequests++
		}
	}

	return stats, nil
}

// UsageStats - статистика использования AI
type UsageStats struct {
	TotalRequests      int `json:"total_requests"`
	SuccessfulRequests int `json:"successful_requests"`
	FailedRequests     int `json:"failed_requests"`
	TotalTokens        int `json:"total_tokens"`
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
}

// logUsage логирует использование AI
func (s *Service) logUsage(requestType string, input, output, total int, success bool, errorMsg string) {
	usageLog := &models.AIUsageLog{
		RequestType:  requestType,
		InputTokens:  input,
		OutputTokens: output,
		TotalTokens:  total,
		Success:      success,
		ErrorMessage: errorMsg,
	}
	if err := s.repo.CreateAIUsageLog(usageLog); err != nil {
		log.Printf("[AI] Ошибка сохранения лога: %v", err)
	}
}

// AnalyzeLatestSnapshots анализирует последние снимки (вызывается из cron)
func (s *Service) AnalyzeLatestSnapshots(ctx context.Context) error {
	if !s.IsEnabled() {
		return nil
	}

	log.Println("[AI] Запуск анализа последних снимков...")

	// Получаем аккаунты с биллингом
	accounts, err := s.repo.GetSelectedAccounts()
	if err != nil {
		return err
	}

	analyzed := 0
	for _, account := range accounts {
		// Проверяем rate limit
		if !s.rateLimiter.Allow() {
			log.Printf("[AI] Rate limit достигнут, проанализировано %d аккаунтов", analyzed)
			break
		}

		// Получаем последний снимок
		snapshot, err := s.repo.GetLastSnapshot(account.ID)
		if err != nil || snapshot == nil {
			continue
		}

		// Анализируем
		_, err = s.AnalyzeAccount(ctx, &account, snapshot)
		if err != nil {
			log.Printf("[AI] Ошибка анализа аккаунта %s: %v", account.Name, err)
			continue
		}
		analyzed++
	}

	log.Printf("[AI] Анализ завершён, обработано %d аккаунтов", analyzed)
	return nil
}
