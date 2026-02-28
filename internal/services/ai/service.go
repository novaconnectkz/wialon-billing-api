package ai

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/user/wialon-billing-api/internal/models"
	"github.com/user/wialon-billing-api/internal/repository"
	"golang.org/x/time/rate"
)

// escapeJSON экранирует строку для безопасной вставки в JSON
func escapeJSON(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return s
}

// Service - сервис AI аналитики (DeepSeek)
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
		// Создаём настройки по умолчанию для DeepSeek
		settings = &models.AISettings{
			Enabled:          false,
			AnalysisModel:    ModelReasonerR1,
			SupportModel:     ModelChatV3,
			MaxTokens:        2500,
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
		client, err := NewClient(ctx, settings.APIKey, settings.MaxTokens)
		if err != nil {
			log.Printf("[AI] Ошибка инициализации клиента: %v", err)
			return err
		}
		s.mu.Lock()
		s.client = client
		s.mu.Unlock()
		log.Println("[AI] DeepSeek сервис успешно инициализирован")
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
	// Burst = requestsPerHour чтобы сразу можно было делать запросы
	s.rateLimiter = rate.NewLimiter(rate.Every(interval), requestsPerHour)
	log.Printf("[AI] Rate limiter обновлён: %d запросов/час", requestsPerHour)
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
		client, err := NewClient(ctx, settings.APIKey, settings.MaxTokens)
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

// GetAnalysisModel возвращает модель для анализа (R1)
func (s *Service) GetAnalysisModel() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.settings != nil && s.settings.AnalysisModel != "" {
		return s.settings.AnalysisModel
	}
	return ModelReasonerR1
}

// GetSupportModel возвращает модель для поддержки (V3)
func (s *Service) GetSupportModel() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.settings != nil && s.settings.SupportModel != "" {
		return s.settings.SupportModel
	}
	return ModelChatV3
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

	// Отправляем запрос к AI — используем V3 (chat) для стабильного JSON
	result, err := s.client.Generate(ctx, s.GetSupportModel(), AnalyticsSystemPrompt, userPrompt)
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
		// Фолбэк: пробуем reasoning_content (на случай если модель сменится)
		if result.ReasoningContent != "" {
			insightResp, err = ParseInsightResponse(result.ReasoningContent)
		}
		if err != nil {
			log.Printf("[AI] Не удалось распарсить JSON для %s: %v", account.Name, err)
			return nil, err
		}
	}

	// Создаём инсайт
	s.mu.RLock()
	cacheTTL := 24
	if s.settings != nil {
		cacheTTL = s.settings.CacheTTLHours
	}
	s.mu.RUnlock()

	// Формируем metadata с дополнительными полями
	metadataJSON := fmt.Sprintf(
		`{"recommendation":"%s","delta":%d,"delta_percent":%.1f}`,
		escapeJSON(insightResp.Recommendation),
		insightResp.Delta,
		insightResp.DeltaPercent,
	)

	insight := &models.AIInsight{
		AccountID:       account.ID,
		InsightType:     insightResp.InsightType,
		Severity:        insightResp.Severity,
		Title:           insightResp.Title,
		Description:     insightResp.Description,
		FinancialImpact: &insightResp.FinancialImpact,
		Currency:        currency,
		Metadata:        metadataJSON,
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

// === Анализ трендов флота ===

// FleetAnomaly - обнаруженная аномалия
type FleetAnomaly struct {
	Date        string  `json:"date"`
	AccountName string  `json:"account_name"`
	Type        string  `json:"type"` // mass_deletion, rapid_growth, churn_risk
	Severity    string  `json:"severity"`
	Description string  `json:"description"`
	Delta       int     `json:"delta"`
	Percentage  float64 `json:"percentage"`
}

// FleetTrendsData - данные о трендах для графиков
type FleetTrendsData struct {
	Date        string `json:"date"`
	TotalUnits  int    `json:"total_units"`
	Created     int    `json:"created"`
	Deleted     int    `json:"deleted"`
	Deactivated int    `json:"deactivated"`
}

// FleetAnalysisResult - результат анализа флота
type FleetAnalysisResult struct {
	Period          int               `json:"period"` // дней
	TotalAccounts   int               `json:"total_accounts"`
	CurrentFleet    int               `json:"current_fleet"`
	InitialFleet    int               `json:"initial_fleet"`
	NetChange       int               `json:"net_change"`
	ChangePercent   float64           `json:"change_percent"`
	Trend           string            `json:"trend"` // growth, stable, decline
	Anomalies       []FleetAnomaly    `json:"anomalies"`
	TrendsData      []FleetTrendsData `json:"trends_data"`
	ChurnRiskCount  int               `json:"churn_risk_count"`
	DormantUnits    int               `json:"dormant_units"`        // деактивированы >30 дней
	AIInsight       string            `json:"ai_insight,omitempty"` // AI анализ
	Recommendations []string          `json:"recommendations,omitempty"`
}

// GetFleetTrends возвращает данные о трендах флота за период
func (s *Service) GetFleetTrends(days int) (*FleetAnalysisResult, error) {
	// Получаем аккаунты с биллингом
	accounts, err := s.repo.GetSelectedAccounts()
	if err != nil {
		return nil, err
	}

	result := &FleetAnalysisResult{
		Period:        days,
		TotalAccounts: len(accounts),
		TrendsData:    make([]FleetTrendsData, 0),
		Anomalies:     make([]FleetAnomaly, 0),
	}

	now := time.Now()
	// Нормализуем до начала сегодняшнего дня, чтобы не включать текущий день (снимков ещё нет)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	startDate := today.AddDate(0, 0, -days)

	// Агрегируем данные по дням
	dailyStats := make(map[string]*FleetTrendsData)
	accountTrends := make(map[uint][]int) // история объектов по аккаунтам

	for _, account := range accounts {
		// Получаем все снимки за период (не включая сегодняшний день)
		for d := 0; d < days; d++ {
			date := startDate.AddDate(0, 0, d)
			dateStr := date.Format("2006-01-02")

			snapshot, err := s.repo.GetSnapshotForDate(account.ID, date)
			if err != nil || snapshot == nil {
				continue
			}

			// Агрегируем по дням
			if _, ok := dailyStats[dateStr]; !ok {
				dailyStats[dateStr] = &FleetTrendsData{Date: dateStr}
			}
			dailyStats[dateStr].TotalUnits += snapshot.TotalUnits
			dailyStats[dateStr].Created += snapshot.UnitsCreated
			dailyStats[dateStr].Deleted += snapshot.UnitsDeleted
			dailyStats[dateStr].Deactivated += snapshot.UnitsDeactivated

			// Track per account
			accountTrends[account.ID] = append(accountTrends[account.ID], snapshot.TotalUnits)

			// Проверяем аномалии (>2% удалений)
			if snapshot.TotalUnits > 0 && snapshot.UnitsDeleted > 0 {
				deletePercent := float64(snapshot.UnitsDeleted) / float64(snapshot.TotalUnits+snapshot.UnitsDeleted) * 100
				if deletePercent > 2.0 {
					result.Anomalies = append(result.Anomalies, FleetAnomaly{
						Date:        dateStr,
						AccountName: account.Name,
						Type:        "mass_deletion",
						Severity:    s.getSeverity(deletePercent),
						Description: fmt.Sprintf("Удалено %.1f%% объектов (%d из %d)", deletePercent, snapshot.UnitsDeleted, snapshot.TotalUnits+snapshot.UnitsDeleted),
						Delta:       -snapshot.UnitsDeleted,
						Percentage:  deletePercent,
					})
				}
			}

			// Проверяем резкий рост (>5%)
			if snapshot.TotalUnits > 0 && snapshot.UnitsCreated > 0 {
				growthPercent := float64(snapshot.UnitsCreated) / float64(snapshot.TotalUnits-snapshot.UnitsCreated) * 100
				if growthPercent > 5.0 {
					result.Anomalies = append(result.Anomalies, FleetAnomaly{
						Date:        dateStr,
						AccountName: account.Name,
						Type:        "rapid_growth",
						Severity:    "info",
						Description: fmt.Sprintf("Рост %.1f%% (+%d объектов)", growthPercent, snapshot.UnitsCreated),
						Delta:       snapshot.UnitsCreated,
						Percentage:  growthPercent,
					})
				}
			}
		}
	}

	// Конвертируем в срез и сортируем
	for _, data := range dailyStats {
		result.TrendsData = append(result.TrendsData, *data)
	}

	// Вычисляем общие метрики
	if len(result.TrendsData) > 0 {
		// Сортируем по дате
		for i := 0; i < len(result.TrendsData)-1; i++ {
			for j := i + 1; j < len(result.TrendsData); j++ {
				if result.TrendsData[i].Date > result.TrendsData[j].Date {
					result.TrendsData[i], result.TrendsData[j] = result.TrendsData[j], result.TrendsData[i]
				}
			}
		}

		result.InitialFleet = result.TrendsData[0].TotalUnits
		result.CurrentFleet = result.TrendsData[len(result.TrendsData)-1].TotalUnits
		result.NetChange = result.CurrentFleet - result.InitialFleet

		if result.InitialFleet > 0 {
			result.ChangePercent = float64(result.NetChange) / float64(result.InitialFleet) * 100
		}

		// Определяем тренд
		if result.ChangePercent > 2 {
			result.Trend = "growth"
		} else if result.ChangePercent < -2 {
			result.Trend = "decline"
		} else {
			result.Trend = "stable"
		}
	}

	// Подсчитываем churn-риски (падение 3+ недели)
	for _, history := range accountTrends {
		if len(history) >= 21 {
			declining := true
			for i := 1; i < len(history) && i < 21; i++ {
				if history[i] >= history[i-1] {
					declining = false
					break
				}
			}
			if declining {
				result.ChurnRiskCount++
			}
		}
	}

	return result, nil
}

// getSeverity определяет severity по проценту
func (s *Service) getSeverity(percent float64) string {
	if percent > 10 {
		return "critical"
	} else if percent > 5 {
		return "warning"
	}
	return "info"
}

// AnalyzeFleetTrends запускает AI анализ трендов флота
func (s *Service) AnalyzeFleetTrends(ctx context.Context, days int) (*FleetAnalysisResult, error) {
	if !s.IsEnabled() {
		return nil, fmt.Errorf("AI сервис отключён")
	}

	// Получаем данные о трендах
	result, err := s.GetFleetTrends(days)
	if err != nil {
		return nil, err
	}

	// Проверяем rate limit
	if !s.rateLimiter.Allow() {
		// Возвращаем данные без AI анализа
		return result, nil
	}

	// Формируем статистику для промпта
	dailyStats := ""
	for _, data := range result.TrendsData {
		dailyStats += fmt.Sprintf("- %s: %d объектов (+%d/-%d)\n", data.Date, data.TotalUnits, data.Created, data.Deleted)
	}

	// Топ изменений (берём аномалии)
	topChanges := ""
	for _, a := range result.Anomalies {
		topChanges += fmt.Sprintf("- %s: %s - %s\n", a.Date, a.AccountName, a.Description)
	}
	if topChanges == "" {
		topChanges = "Значительных изменений не обнаружено"
	}

	// Формируем промпт
	userPrompt := fmt.Sprintf(FleetTrendsUserPromptTemplate,
		days,
		result.TotalAccounts,
		result.CurrentFleet,
		days, result.InitialFleet,
		result.NetChange, result.ChangePercent,
		dailyStats,
		topChanges,
		0, // TODO: всего деактивировано
		result.DormantUnits,
	)

	// Отправляем запрос к AI
	aiResult, err := s.client.Generate(ctx, s.GetAnalysisModel(), FleetTrendsSystemPrompt, userPrompt)
	if err != nil {
		s.logUsage("fleet_analysis", 0, 0, 0, false, err.Error())
		// Возвращаем данные без AI анализа
		return result, nil
	}

	s.logUsage("fleet_analysis", aiResult.InputTokens, aiResult.OutputTokens, aiResult.TotalTokens, true, "")
	result.AIInsight = aiResult.Response

	return result, nil
}
