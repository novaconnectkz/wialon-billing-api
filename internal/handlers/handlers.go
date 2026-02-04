package handlers

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/user/wialon-billing-api/internal/models"
	"github.com/user/wialon-billing-api/internal/repository"
	"github.com/user/wialon-billing-api/internal/services/invoice"
	invoicesvc "github.com/user/wialon-billing-api/internal/services/invoice"
	"github.com/user/wialon-billing-api/internal/services/nbk"
	"github.com/user/wialon-billing-api/internal/services/snapshot"
	"github.com/user/wialon-billing-api/internal/services/wialon"
)

// Handler - обработчики HTTP-запросов
type Handler struct {
	repo     *repository.Repository
	wialon   *wialon.Client
	snapshot *snapshot.Service
	nbk      *nbk.Service
	invoice  *invoice.Service
}

// NewHandler создаёт новый обработчик
func NewHandler(
	repo *repository.Repository,
	wialon *wialon.Client,
	snapshot *snapshot.Service,
	nbk *nbk.Service,
	invoice *invoice.Service,
) *Handler {
	return &Handler{
		repo:     repo,
		wialon:   wialon,
		snapshot: snapshot,
		nbk:      nbk,
		invoice:  invoice,
	}
}

// === Auth ===

// LoginRequest - запрос на авторизацию
type LoginRequest struct {
	Token string `json:"token" binding:"required"`
}

// Login авторизует пользователя через Wialon токен
func (h *Handler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Требуется токен"})
		return
	}

	// Проверяем токен в Wialon
	// Для MVP - просто возвращаем токен как session token
	c.JSON(http.StatusOK, gin.H{
		"token":   req.Token,
		"message": "Авторизация успешна",
	})
}

// === Accounts ===

// GetAccounts возвращает все учётные записи
func (h *Handler) GetAccounts(c *gin.Context) {
	accounts, err := h.repo.GetAllAccounts()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, accounts)
}

// GetSelectedAccounts возвращает учётные записи, участвующие в биллинге
func (h *Handler) GetSelectedAccounts(c *gin.Context) {
	accounts, err := h.repo.GetSelectedAccounts()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, accounts)
}

// ToggleAccount переключает участие аккаунта в биллинге
func (h *Handler) ToggleAccount(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID"})
		return
	}

	if err := h.repo.ToggleAccountBilling(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Статус изменён"})
}

// UpdateAccountDetails обновляет реквизиты покупателя
func (h *Handler) UpdateAccountDetails(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID"})
		return
	}

	var req struct {
		BuyerName      string  `json:"buyer_name"`
		BuyerBIN       string  `json:"buyer_bin"`
		BuyerAddress   string  `json:"buyer_address"`
		BuyerEmail     string  `json:"buyer_email"`
		BuyerPhone     string  `json:"buyer_phone"`
		ContractNumber string  `json:"contract_number"`
		ContractDate   *string `json:"contract_date"` // формат: 2006-01-02
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	account, err := h.repo.GetAccountByID(uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Аккаунт не найден"})
		return
	}

	account.BuyerName = req.BuyerName
	account.BuyerBIN = req.BuyerBIN
	account.BuyerAddress = req.BuyerAddress
	account.BuyerEmail = req.BuyerEmail
	account.BuyerPhone = req.BuyerPhone
	account.ContractNumber = req.ContractNumber

	if req.ContractDate != nil && *req.ContractDate != "" {
		t, err := time.Parse("2006-01-02", *req.ContractDate)
		if err == nil {
			account.ContractDate = &t
		}
	} else {
		account.ContractDate = nil
	}

	if err := h.repo.UpdateAccount(account); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, account)
}

// GetAccountHistory возвращает историю изменений аккаунта из Wialon
func (h *Handler) GetAccountHistory(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID"})
		return
	}

	// Получаем аккаунт из БД для получения WialonID
	account, err := h.repo.GetAccountByID(uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Аккаунт не найден"})
		return
	}

	// Количество дней (по умолчанию 30)
	days := 30
	if daysStr := c.Query("days"); daysStr != "" {
		if d, err := strconv.Atoi(daysStr); err == nil && d > 0 && d <= 365 {
			days = d
		}
	}

	// Получаем токен пользователя
	userToken := c.GetHeader("Authorization")
	if strings.HasPrefix(userToken, "Bearer ") {
		userToken = strings.TrimPrefix(userToken, "Bearer ")
	}

	// Создаём Wialon клиент
	wialonURL := "https://hst-api.regwialon.com"
	wialonClient := wialon.NewClientWithToken(wialonURL, userToken)

	// Получаем историю
	history, err := wialonClient.GetAccountHistory(account.WialonID, days)
	if err != nil {
		log.Printf("Ошибка получения истории аккаунта %d: %v", account.WialonID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"account_id":   account.ID,
		"wialon_id":    account.WialonID,
		"account_name": account.Name,
		"days":         days,
		"history":      history,
	})
}

// GetAccountStats возвращает статистику изменений объектов аккаунта по дням
func (h *Handler) GetAccountStats(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID"})
		return
	}

	// Получаем аккаунт из БД для получения WialonID
	account, err := h.repo.GetAccountByID(uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Аккаунт не найден"})
		return
	}

	// Парсим параметры периода (по умолчанию текущий месяц)
	now := time.Now()
	year := now.Year()
	month := int(now.Month())

	if yearStr := c.Query("year"); yearStr != "" {
		if y, err := strconv.Atoi(yearStr); err == nil && y > 2000 && y < 2100 {
			year = y
		}
	}
	if monthStr := c.Query("month"); monthStr != "" {
		if m, err := strconv.Atoi(monthStr); err == nil && m >= 1 && m <= 12 {
			month = m
		}
	}

	// Рассчитываем период: с 1-го числа месяца до последнего
	startOfMonth := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
	endOfMonth := startOfMonth.AddDate(0, 1, 0).Add(-time.Second)

	fromTime := startOfMonth.Unix()
	toTime := endOfMonth.Unix()

	// Выбираем Wialon клиент в зависимости от connection_id аккаунта
	var wialonClient *wialon.Client
	if account.ConnectionID != nil && *account.ConnectionID > 0 {
		// Получаем подключение из БД
		conn, err := h.repo.GetConnectionByID(*account.ConnectionID)
		if err == nil && conn != nil {
			wialonURL := "https://" + conn.WialonHost
			wialonClient = wialon.NewClientWithToken(wialonURL, conn.Token)
			if err := wialonClient.Login(); err != nil {
				log.Printf("Ошибка авторизации для подключения %d: %v", *account.ConnectionID, err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка авторизации Wialon"})
				return
			}
		} else {
			// Fallback на глобальный клиент
			wialonClient = h.wialon
		}
	} else {
		// Используем глобальный клиент (legacy)
		wialonClient = h.wialon
	}

	stats, err := wialonClient.GetStatistics([]int64{account.WialonID}, fromTime, toTime)
	if err != nil {
		log.Printf("Ошибка получения статистики аккаунта %d: %v", account.WialonID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Получаем статистику для этого аккаунта
	accountStats := stats[account.WialonID]

	c.JSON(http.StatusOK, gin.H{
		"account_id":   account.ID,
		"wialon_id":    account.WialonID,
		"account_name": account.Name,
		"year":         year,
		"month":        month,
		"stats":        accountStats,
	})
}

// SyncAccounts синхронизирует учётные записи с Wialon API через connections пользователя
func (h *Handler) SyncAccounts(c *gin.Context) {
	// Получаем userID из контекста (устанавливается middleware.Auth)
	userIDVal, exists := c.Get("userID")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Не авторизован"})
		return
	}
	userID := userIDVal.(uint)

	// Получаем все подключения пользователя
	connections, err := h.repo.GetConnectionsByUserID(userID)
	if err != nil {
		log.Printf("SyncAccounts ERROR: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка получения подключений"})
		return
	}

	if len(connections) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Нет подключений Wialon. Добавьте подключение в настройках."})
		return
	}

	var totalSynced int
	var totalDealers int
	var totalAccounts int
	var allActiveIDs []int64
	var syncErrors []string

	// Синхронизируем по каждому подключению
	for _, conn := range connections {
		log.Printf("SyncAccounts: обработка подключения %s (host: %s)", conn.Name, conn.WialonHost)

		// Формируем URL для API
		wialonURL := "https://" + conn.WialonHost

		// Создаём Wialon клиент с токеном из подключения
		wialonClient := wialon.NewClientWithToken(wialonURL, conn.Token)

		// Авторизуемся для получения ID текущего пользователя
		if err := wialonClient.Login(); err != nil {
			log.Printf("SyncAccounts ERROR login for %s: %v", conn.Name, err)
			syncErrors = append(syncErrors, conn.Name+": "+err.Error())
			continue
		}

		currentUserID := wialonClient.GetCurrentUserID()
		// ID аккаунта пользователя (обычно userID + 1)
		parentAccountID := currentUserID + 1
		log.Printf("SyncAccounts: %s - userID=%d, parentAccountID=%d", conn.Name, currentUserID, parentAccountID)

		// Получаем все учётные записи из Wialon
		accountsResp, err := wialonClient.GetAccounts()
		if err != nil {
			log.Printf("SyncAccounts ERROR for %s: %v", conn.Name, err)
			syncErrors = append(syncErrors, conn.Name+": "+err.Error())
			continue
		}

		log.Printf("SyncAccounts: %s - получено %d аккаунтов", conn.Name, len(accountsResp.Items))
		totalAccounts += len(accountsResp.Items)

		// Параллельная обработка GetAccountData с ограниченной конкурентностью
		type accountResult struct {
			item        wialon.WialonItem
			accountData *wialon.AccountDataResponse
		}

		results := make(chan accountResult, len(accountsResp.Items))
		sem := make(chan struct{}, 10) // Ограничиваем до 10 параллельных запросов

		for _, item := range accountsResp.Items {
			go func(it wialon.WialonItem) {
				sem <- struct{}{}        // Захватываем слот
				defer func() { <-sem }() // Освобождаем слот

				data, _ := wialonClient.GetAccountData(it.ID)
				results <- accountResult{item: it, accountData: data}
			}(item)
		}

		var synced int
		var dealers int
		processed := 0

		for range accountsResp.Items {
			res := <-results
			processed++

			// Логируем прогресс каждые 500 аккаунтов
			if processed%500 == 0 {
				log.Printf("SyncAccounts: %s - обработано %d/%d", conn.Name, processed, len(accountsResp.Items))
			}

			isDealer := false
			var parentID int64 = 0
			if res.accountData != nil {
				isDealer = res.accountData.DealerRights == 1
				parentID = res.accountData.ParentAccountId
			}

			// Фильтр: только дилерские аккаунты с родителем = наш аккаунт
			if !isDealer || parentID != parentAccountID {
				continue
			}

			dealers++

			// Создаём или обновляем аккаунт в БД
			var parentIDPtr *int64
			if parentID != 0 {
				parentIDPtr = &parentID
			}

			// Определяем статус блокировки
			isBlocked := false
			if res.accountData != nil && res.accountData.Enabled != nil && *res.accountData.Enabled == 0 {
				isBlocked = true
			}

			account := &models.Account{
				WialonID:         res.item.ID,
				Name:             res.item.Name,
				IsDealer:         isDealer,
				ParentID:         parentIDPtr,
				IsBillingEnabled: false,
				IsActive:         true,
				IsBlocked:        isBlocked,
				ConnectionID:     &conn.ID, // Привязываем к подключению
			}
			if err := h.repo.UpsertAccount(account); err == nil {
				synced++
				allActiveIDs = append(allActiveIDs, res.item.ID)
			}
		}

		totalSynced += synced
		totalDealers += dealers
		log.Printf("SyncAccounts: %s - завершено. Дилеров: %d, синхронизировано: %d", conn.Name, dealers, synced)
	}

	// Деактивируем аккаунты, которых нет в полученном списке
	if len(allActiveIDs) > 0 {
		if err := h.repo.DeactivateMissingAccounts(allActiveIDs); err != nil {
			log.Printf("SyncAccounts ERROR deactivate: %v", err)
		}
	}

	response := gin.H{
		"message":       "Синхронизация завершена",
		"total":         totalAccounts,
		"synced":        totalSynced,
		"dealers_found": totalDealers,
		"connections":   len(connections),
	}

	if len(syncErrors) > 0 {
		response["errors"] = syncErrors
	}

	log.Printf("SyncAccounts: завершено. Подключений: %d, всего: %d, синхронизировано: %d",
		len(connections), totalAccounts, totalSynced)

	c.JSON(http.StatusOK, response)
}

// === Modules ===

// GetModules возвращает все модули
func (h *Handler) GetModules(c *gin.Context) {
	modules, err := h.repo.GetAllModules()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, modules)
}

// CreateModule создаёт новый модуль
func (h *Handler) CreateModule(c *gin.Context) {
	var module models.Module
	if err := c.ShouldBindJSON(&module); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.repo.CreateModule(&module); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, module)
}

// UpdateModule обновляет модуль
func (h *Handler) UpdateModule(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID"})
		return
	}

	var module models.Module
	if err := c.ShouldBindJSON(&module); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	module.ID = uint(id)
	if err := h.repo.UpdateModule(&module); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, module)
}

// DeleteModule удаляет модуль
func (h *Handler) DeleteModule(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID"})
		return
	}

	if err := h.repo.DeleteModule(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Модуль удалён"})
}

// AssignModule привязывает модуль к учётной записи
func (h *Handler) AssignModule(c *gin.Context) {
	accountIDStr := c.Param("id")
	accountID, err := strconv.ParseUint(accountIDStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID аккаунта"})
		return
	}

	var req struct {
		ModuleID uint `json:"module_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.repo.AssignModuleToAccount(uint(accountID), req.ModuleID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Модуль привязан"})
}

// === Settings ===

// GetSettings возвращает настройки биллинга
func (h *Handler) GetSettings(c *gin.Context) {
	settings, err := h.repo.GetSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if settings == nil {
		// Возвращаем дефолтные настройки
		settings = &models.BillingSettings{
			WialonType: "hosting",
			UnitPrice:  2.0,
			Currency:   "EUR",
		}
	}

	c.JSON(http.StatusOK, settings)
}

// UpdateSettings обновляет настройки биллинга
func (h *Handler) UpdateSettings(c *gin.Context) {
	var settings models.BillingSettings
	if err := c.ShouldBindJSON(&settings); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.repo.SaveSettings(&settings); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, settings)
}

// === Exchange Rates ===

// GetExchangeRates возвращает историю курсов
func (h *Handler) GetExchangeRates(c *gin.Context) {
	rates, err := h.repo.GetExchangeRates(500)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, rates)
}

// BackfillExchangeRates заполняет курсы валют за период
func (h *Handler) BackfillExchangeRates(c *gin.Context) {
	var req struct {
		From string `json:"from"` // формат: 2025-11-01
		To   string `json:"to"`   // формат: 2026-01-30
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "укажите from и to в формате YYYY-MM-DD"})
		return
	}

	fromDate, err := time.Parse("2006-01-02", req.From)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "неверный формат from"})
		return
	}

	toDate, err := time.Parse("2006-01-02", req.To)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "неверный формат to"})
		return
	}

	// Запрашиваем курсы для каждого дня
	count := 0
	for d := fromDate; !d.After(toDate); d = d.AddDate(0, 0, 1) {
		if err := h.nbk.FetchExchangeRatesForDate(d); err != nil {
			log.Printf("Ошибка получения курсов за %s: %v", d.Format("2006-01-02"), err)
			continue
		}
		count++
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Курсы загружены",
		"days":    count,
		"from":    req.From,
		"to":      req.To,
	})
}

// === Dashboard ===

// GetDashboard возвращает данные для дашборда
func (h *Handler) GetDashboard(c *gin.Context) {
	// Проверяем, нужна ли фильтрация по дилеру
	filterByDealer, _ := c.Get("filterByDealer")
	dealerWialonID, _ := c.Get("dealerWialonID")

	var accounts []models.Account
	var err error

	if filterByDealer == true && dealerWialonID != nil {
		// Дилер видит ТОЛЬКО свой аккаунт
		wialonID := dealerWialonID.(*int64)
		if wialonID != nil {
			account, accErr := h.repo.GetAccountByDealer(*wialonID)
			if accErr == nil && account != nil {
				accounts = []models.Account{*account}
			}
		}
	} else {
		// Админ видит всё
		accounts, err = h.repo.GetSelectedAccounts()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	// Парсим параметры периода (по умолчанию текущий месяц)
	now := time.Now()
	year := now.Year()
	month := int(now.Month())

	if yearStr := c.Query("year"); yearStr != "" {
		if y, err := strconv.Atoi(yearStr); err == nil && y > 2000 && y < 2100 {
			year = y
		}
	}
	if monthStr := c.Query("month"); monthStr != "" {
		if m, err := strconv.Atoi(monthStr); err == nil && m >= 1 && m <= 12 {
			month = m
		}
	}

	// Получаем снимки за указанный период (с фильтрацией по дилеру если нужно)
	var snapshots []models.Snapshot
	if filterByDealer == true && dealerWialonID != nil {
		wialonID := dealerWialonID.(*int64)
		if wialonID != nil {
			snapshots, err = h.repo.GetSnapshotsByDealer(*wialonID, year, month)
		}
	} else {
		snapshots, err = h.repo.GetSnapshotsByPeriod(year, month)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// settings больше не нужен — цены из модулей

	// Группируем снимки по дате и считаем сумму объектов за каждый день
	dailyTotals := make(map[string]int)
	for _, s := range snapshots {
		dateKey := s.CreatedAt.Format("2006-01-02")
		dailyTotals[dateKey] += s.TotalUnits
	}

	// Считаем количество дней в выбранном месяце
	daysInMonth := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()

	// Считаем сумму объектов за все дни с данными
	totalUnitsSum := 0
	for _, dayTotal := range dailyTotals {
		totalUnitsSum += dayTotal
	}

	// Среднее количество объектов в день = сумма / кол-во дней в месяце
	var avgUnits float64
	if daysInMonth > 0 {
		avgUnits = float64(totalUnitsSum) / float64(daysInMonth)
	}

	// Рассчитываем стоимость по модулям
	// Для каждого уникального модуля: avgUnits × цена (для per_unit) или фикс цена (для fixed)
	costByCurrency := make(map[string]float64)
	usedModules := make(map[uint]bool)

	for _, acc := range accounts {
		if !acc.IsBillingEnabled {
			continue
		}

		for _, am := range acc.Modules {
			module := am.Module
			if module.ID == 0 || usedModules[module.ID] {
				continue
			}
			usedModules[module.ID] = true

			var moduleCost float64
			if module.PricingType == "fixed" {
				moduleCost = module.Price
			} else {
				// per_unit — среднее кол-во объектов × цена
				moduleCost = module.Price * avgUnits
			}

			costByCurrency[module.Currency] += moduleCost
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"accounts":         accounts,
		"total_units":      int(avgUnits + 0.5),
		"cost_by_currency": costByCurrency,
		"snapshots":        snapshots,
		"year":             year,
		"month":            month,
	})
}

// === Snapshots ===

// GetSnapshots возвращает список снимков
func (h *Handler) GetSnapshots(c *gin.Context) {
	var snapshots []models.Snapshot
	var err error

	// Проверяем, является ли пользователь дилером
	filterByDealer, _ := c.Get("filterByDealer")
	dealerWialonID, _ := c.Get("dealerWialonID")

	if filterByDealer == true && dealerWialonID != nil {
		// Для дилера — только его снимки (за все время)
		// dealerWialonID это *int64, нужно разыменовать
		wialonID := *dealerWialonID.(*int64)
		snapshots, err = h.repo.GetSnapshotsByDealerAll(wialonID, 500)
	} else {
		// Для админа — все снимки
		snapshots, err = h.repo.GetSnapshots(100)
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, snapshots)
}

// CreateSnapshot создаёт ручной снимок
func (h *Handler) CreateSnapshot(c *gin.Context) {
	var req struct {
		AccountID uint `json:"account_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	snapshot, err := h.snapshot.CreateManualSnapshot(req.AccountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, snapshot)
}

// CreateSnapshotsForDate создаёт снимки для всех аккаунтов за указанную дату
func (h *Handler) CreateSnapshotsForDate(c *gin.Context) {
	var req struct {
		Date string `json:"date" binding:"required"` // формат: "2006-01-02"
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Укажите дату в формате YYYY-MM-DD"})
		return
	}

	// Парсим дату
	snapshotDate, err := time.Parse("2006-01-02", req.Date)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный формат даты. Используйте YYYY-MM-DD"})
		return
	}

	snapshots, err := h.snapshot.CreateSnapshotsForDate(snapshotDate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if len(snapshots) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"message": "Нет аккаунтов с включённым биллингом",
			"count":   0,
		})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message":   "Снимки созданы",
		"count":     len(snapshots),
		"date":      req.Date,
		"snapshots": snapshots,
	})
}

// ClearAllSnapshots удаляет все снимки (с защитным кодом)
func (h *Handler) ClearAllSnapshots(c *gin.Context) {
	var req struct {
		ConfirmCode string `json:"confirm_code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Укажите код подтверждения"})
		return
	}

	// Проверяем защитный код
	if req.ConfirmCode != "220475" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Неверный код подтверждения"})
		return
	}

	// Удаляем все снимки
	count, err := h.repo.ClearAllSnapshots()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	log.Printf("Удалено %d снимков", count)
	c.JSON(http.StatusOK, gin.H{
		"message": "Все снимки удалены",
		"count":   count,
	})
}

// === Changes ===

// GetChanges возвращает изменения
func (h *Handler) GetChanges(c *gin.Context) {
	changes, err := h.repo.GetChanges(100)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, changes)
}

// === Dealer Invite ===

// InviteDealerRequest - запрос на приглашение дилера
type InviteDealerRequest struct {
	Email string `json:"email" binding:"required,email"`
}

// InviteDealer отправляет приглашение дилеру на email
func (h *Handler) InviteDealer(c *gin.Context) {
	accountIDStr := c.Param("id")
	accountID, err := strconv.ParseUint(accountIDStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID аккаунта"})
		return
	}

	var req InviteDealerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Введите корректный email"})
		return
	}

	// Получаем аккаунт
	account, err := h.repo.GetAccountByID(uint(accountID))
	if err != nil || !account.IsDealer {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Аккаунт не найден или не является дилером"})
		return
	}

	// Сохраняем контактный email в аккаунт
	account.ContactEmail = &req.Email
	if err := h.repo.UpdateAccount(account); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка сохранения email"})
		return
	}

	// TODO: Интеграция с authRepo для создания пользователя и OTP
	// На данный момент возвращаем успешный ответ
	log.Printf("Приглашение дилера: email=%s, account_id=%d, wialon_id=%d", req.Email, account.ID, account.WialonID)

	c.JSON(http.StatusOK, gin.H{
		"message":    "Приглашение сохранено. Email: " + req.Email,
		"account_id": account.ID,
		"wialon_id":  account.WialonID,
	})
}

// === Invoices ===

// GetInvoices возвращает список счетов
func (h *Handler) GetInvoices(c *gin.Context) {
	invoices, err := h.repo.GetInvoices(100)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, invoices)
}

// GetInvoice возвращает счёт по ID
func (h *Handler) GetInvoice(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID"})
		return
	}

	invoice, err := h.repo.GetInvoiceByID(uint(id))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if invoice == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Счёт не найден"})
		return
	}

	c.JSON(http.StatusOK, invoice)
}

// GetInvoicePDF возвращает PDF счёта
func (h *Handler) GetInvoicePDF(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID"})
		return
	}

	// Получаем счёт
	inv, err := h.repo.GetInvoiceByID(uint(id))
	if err != nil || inv == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Счёт не найден"})
		return
	}

	// Получаем настройки
	settings, err := h.repo.GetSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка получения настроек"})
		return
	}

	// Получаем аккаунт
	account, err := h.repo.GetAccountByID(inv.AccountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка получения аккаунта"})
		return
	}

	// Генерируем PDF
	generator := invoicesvc.NewPDFGenerator()
	pdfBytes, err := generator.GenerateInvoicePDF(inv, settings, account)
	if err != nil {
		log.Printf("Ошибка генерации PDF для счёта %d: %v", inv.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка генерации PDF: " + err.Error()})
		return
	}

	// Отправляем PDF
	filename := fmt.Sprintf("invoice_%d.pdf", inv.ID)
	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}

// GenerateInvoices генерирует счета за указанный период
func (h *Handler) GenerateInvoices(c *gin.Context) {
	var req struct {
		Year  int `json:"year"`
		Month int `json:"month"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		// Если не указано, берём предыдущий месяц
		now := time.Now()
		prevMonth := now.AddDate(0, -1, 0)
		req.Year = prevMonth.Year()
		req.Month = int(prevMonth.Month())
	}

	period := time.Date(req.Year, time.Month(req.Month), 1, 0, 0, 0, 0, time.Local)

	invoices, err := h.invoice.GenerateMonthlyInvoices(period)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message":  "Счета сгенерированы",
		"count":    len(invoices),
		"period":   period.Format("01.2006"),
		"invoices": invoices,
	})
}

// UpdateInvoiceStatus обновляет статус счёта
func (h *Handler) UpdateInvoiceStatus(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID"})
		return
	}

	var req struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Укажите статус"})
		return
	}

	// Проверка допустимых статусов
	validStatuses := map[string]bool{"draft": true, "sent": true, "paid": true, "overdue": true}
	if !validStatuses[req.Status] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Недопустимый статус"})
		return
	}

	invoice, err := h.repo.GetInvoiceByID(uint(id))
	if err != nil || invoice == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Счёт не найден"})
		return
	}

	invoice.Status = req.Status
	now := time.Now()

	if req.Status == "sent" && invoice.SentAt == nil {
		invoice.SentAt = &now
	}
	if req.Status == "paid" && invoice.PaidAt == nil {
		invoice.PaidAt = &now
	}

	if err := h.repo.UpdateInvoice(invoice); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, invoice)
}

// === Массовая привязка модулей ===

// AssignModuleBulk привязывает модуль к нескольким аккаунтам
func (h *Handler) AssignModuleBulk(c *gin.Context) {
	moduleIDStr := c.Param("id")
	moduleID, err := strconv.ParseUint(moduleIDStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID модуля"})
		return
	}

	var req struct {
		AccountIDs []uint `json:"account_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Укажите account_ids"})
		return
	}

	created, err := h.repo.AssignModuleBulk(uint(moduleID), req.AccountIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Модуль привязан",
		"created": created,
		"total":   len(req.AccountIDs),
	})
}

// UnassignModuleBulk отвязывает модуль от нескольких аккаунтов
func (h *Handler) UnassignModuleBulk(c *gin.Context) {
	moduleIDStr := c.Param("id")
	moduleID, err := strconv.ParseUint(moduleIDStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID модуля"})
		return
	}

	var req struct {
		AccountIDs []uint `json:"account_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Укажите account_ids"})
		return
	}

	removed, err := h.repo.UnassignModuleBulk(uint(moduleID), req.AccountIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Модуль отвязан",
		"removed": removed,
		"total":   len(req.AccountIDs),
	})
}

// RemoveModuleFromAccount отвязывает модуль от аккаунта (индивидуально)
func (h *Handler) RemoveModuleFromAccount(c *gin.Context) {
	accountIDStr := c.Param("id")
	accountID, err := strconv.ParseUint(accountIDStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID аккаунта"})
		return
	}

	moduleIDStr := c.Param("moduleId")
	moduleID, err := strconv.ParseUint(moduleIDStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID модуля"})
		return
	}

	removed, err := h.repo.UnassignModuleBulk(uint(moduleID), []uint{uint(accountID)})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if removed == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Модуль не был привязан"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Модуль отвязан"})
}

// SetCurrencyBulk массово устанавливает валюту для аккаунтов
func (h *Handler) SetCurrencyBulk(c *gin.Context) {
	var req struct {
		AccountIDs []uint `json:"account_ids" binding:"required"`
		Currency   string `json:"currency" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Проверка валюты
	validCurrencies := map[string]bool{"EUR": true, "RUB": true, "KZT": true}
	if !validCurrencies[req.Currency] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверная валюта. Допустимые: EUR, RUB, KZT"})
		return
	}

	updated, err := h.repo.SetCurrencyBulk(req.AccountIDs, req.Currency)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Валюта установлена",
		"updated": updated,
	})
}
