package handlers

import (
	"bytes"
	"fmt"
	"log"
	"math"
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
	"github.com/xuri/excelize/v2"
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
	userToken = strings.TrimPrefix(userToken, "Bearer ")

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

	// Группируем снимки по дате и считаем сумму АКТИВНЫХ объектов за каждый день
	dailyTotals := make(map[string]int)
	for _, s := range snapshots {
		dateKey := s.SnapshotDate.Format("2006-01-02")
		// Считаем только активные объекты (без деактивированных)
		activeUnits := s.TotalUnits - s.UnitsDeactivated
		if activeUnits < 0 {
			activeUnits = 0
		}
		dailyTotals[dateKey] += activeUnits
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

// GetSnapshots возвращает список снимков с серверной пагинацией
func (h *Handler) GetSnapshots(c *gin.Context) {
	// Параметры пагинации
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 5000 {
		pageSize = 20
	}

	// Фильтр по дате
	var from, to *time.Time
	if fromStr := c.Query("from"); fromStr != "" {
		if t, err := time.Parse("2006-01-02", fromStr); err == nil {
			from = &t
		}
	}
	if toStr := c.Query("to"); toStr != "" {
		if t, err := time.Parse("2006-01-02", toStr); err == nil {
			to = &t
		}
	}
	// Фильтр по аккаунту
	var accountID *uint
	if accStr := c.Query("account_id"); accStr != "" {
		if id, err := strconv.ParseUint(accStr, 10, 32); err == nil {
			aid := uint(id)
			accountID = &aid
		}
	}

	snapshots, total, err := h.repo.GetSnapshotsPaginated(page, pageSize, from, to, accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      snapshots,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
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

// CreateSnapshotsForRange создаёт снимки за диапазон дат с обратным расчётом TotalUnits
func (h *Handler) CreateSnapshotsForRange(c *gin.Context) {
	var req struct {
		From string `json:"from" binding:"required"` // формат: "2006-01-02"
		To   string `json:"to" binding:"required"`   // формат: "2006-01-02"
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Укажите from и to в формате YYYY-MM-DD"})
		return
	}

	fromDate, err := time.Parse("2006-01-02", req.From)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный формат from"})
		return
	}

	toDate, err := time.Parse("2006-01-02", req.To)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный формат to"})
		return
	}

	if fromDate.After(toDate) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "from должен быть раньше to"})
		return
	}

	snapshots, err := h.snapshot.CreateSnapshotsForRange(fromDate, toDate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message": "Снимки созданы с обратным расчётом",
		"count":   len(snapshots),
		"from":    req.From,
		"to":      req.To,
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

	// Подставляем актуальные коды и единицы модулей, если в строках они пустые
	allModules, _ := h.repo.GetAllModules()
	type moduleInfo struct {
		Code string
		Unit string
	}
	moduleMap := make(map[uint]moduleInfo)
	for _, m := range allModules {
		moduleMap[m.ID] = moduleInfo{Code: m.Code, Unit: m.Unit}
	}
	for i := range inv.Lines {
		if inv.Lines[i].ModuleID > 0 {
			if info, ok := moduleMap[inv.Lines[i].ModuleID]; ok {
				if inv.Lines[i].ModuleCode == "" && info.Code != "" {
					inv.Lines[i].ModuleCode = info.Code
				}
				if inv.Lines[i].ModuleUnit == "" && info.Unit != "" {
					inv.Lines[i].ModuleUnit = info.Unit
				}
			}
		}
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
	// Имя файла: используем номер счёта (заменяем / на _)
	invoiceNum := inv.Number
	if invoiceNum == "" {
		invoiceNum = fmt.Sprintf("%d", inv.ID)
	}
	filename := fmt.Sprintf("invoice_%s.pdf", strings.ReplaceAll(invoiceNum, "/", "_"))
	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}

// GenerateInvoices генерирует счета за указанный период
func (h *Handler) GenerateInvoices(c *gin.Context) {
	var req struct {
		Year      int   `json:"year"`
		Month     int   `json:"month"`
		AccountID *uint `json:"account_id,omitempty"` // опционально: для одного аккаунта
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		// Если не указано, берём предыдущий месяц
		now := time.Now()
		prevMonth := now.AddDate(0, -1, 0)
		req.Year = prevMonth.Year()
		req.Month = int(prevMonth.Month())
	}

	period := time.Date(req.Year, time.Month(req.Month), 1, 0, 0, 0, 0, time.Local)

	// Если указан конкретный аккаунт — генерируем только для него
	if req.AccountID != nil && *req.AccountID > 0 {
		inv, err := h.invoice.GenerateInvoiceForSingleAccount(*req.AccountID, period)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		count := 0
		var invoices []models.Invoice
		if inv != nil {
			count = 1
			invoices = append(invoices, *inv)
		}

		c.JSON(http.StatusCreated, gin.H{
			"message":  "Счёт сгенерирован",
			"count":    count,
			"period":   period.Format("01.2006"),
			"invoices": invoices,
		})
		return
	}

	// Генерация для всех аккаунтов
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

// ClearAllInvoices удаляет все счета (с защитным кодом)
func (h *Handler) ClearAllInvoices(c *gin.Context) {
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

	// Удаляем все счета
	count, err := h.repo.ClearAllInvoices()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	log.Printf("Удалено %d счетов", count)
	c.JSON(http.StatusOK, gin.H{
		"message": "Все счета удалены",
		"count":   count,
	})
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

// === Детализация начислений ===

// GetAccountCharges возвращает детализацию ежедневных начислений для аккаунта
func (h *Handler) GetAccountCharges(c *gin.Context) {
	idStr := c.Param("id")
	accountID, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID"})
		return
	}

	// Парсим период
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

	// Пересчитываем начисления (на случай если ещё не рассчитаны)
	if err := h.snapshot.CalculateDailyChargesForPeriod(uint(accountID), year, month); err != nil {
		log.Printf("GetAccountCharges: ошибка пересчёта: %v", err)
	}

	// Получаем начисления из БД
	charges, err := h.repo.GetDailyCharges(uint(accountID), year, month)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Получаем аккаунт
	account, err := h.repo.GetAccountByID(uint(accountID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Аккаунт не найден"})
		return
	}

	// Группируем по дням
	type DayCharges struct {
		Date               string               `json:"date"`
		TotalUnits         int                  `json:"total_units"`
		Charges            []models.DailyCharge `json:"charges"`
		DayTotalByCurrency map[string]float64   `json:"day_total_by_currency"`
		DayCostLocal       float64              `json:"day_cost_local,omitempty"`
		LocalCurrency      string               `json:"local_currency,omitempty"`
	}

	dayMap := make(map[string]*DayCharges)
	var dayOrder []string

	// Итоги по модулям
	type ModuleSummary struct {
		ModuleID     uint    `json:"module_id"`
		ModuleName   string  `json:"module_name"`
		PricingType  string  `json:"pricing_type"`
		UnitPrice    float64 `json:"unit_price"`
		TotalCost    float64 `json:"total_cost"`
		Currency     string  `json:"currency"`
		DaysCount    int     `json:"days_count"`
		DaysInMonth  int     `json:"days_in_month"`
		TotalUnits   int     `json:"total_units"`
		AvgUnits     float64 `json:"avg_units"`
		AvgDailyCost float64 `json:"avg_daily_cost"` // средняя стоимость за день
	}
	moduleTotals := make(map[uint]*ModuleSummary)
	costByCurrency := make(map[string]float64)

	for _, ch := range charges {
		dateKey := ch.ChargeDate.Format("2006-01-02")

		day, exists := dayMap[dateKey]
		if !exists {
			day = &DayCharges{
				Date:               dateKey,
				TotalUnits:         ch.TotalUnits,
				DayTotalByCurrency: make(map[string]float64),
			}
			dayMap[dateKey] = day
			dayOrder = append(dayOrder, dateKey)
		}
		day.Charges = append(day.Charges, ch)
		day.DayTotalByCurrency[ch.Currency] += math.Round(ch.DailyCost*100) / 100

		// Итоги по модулям
		mt, ok := moduleTotals[ch.ModuleID]
		if !ok {
			mt = &ModuleSummary{
				ModuleID:    ch.ModuleID,
				ModuleName:  ch.ModuleName,
				PricingType: ch.PricingType,
				UnitPrice:   ch.UnitPrice,
				Currency:    ch.Currency,
				DaysInMonth: ch.DaysInMonth,
			}
			moduleTotals[ch.ModuleID] = mt
		}
		mt.TotalCost += ch.DailyCost
		mt.TotalUnits += ch.TotalUnits
		mt.DaysCount++
		costByCurrency[ch.Currency] += ch.DailyCost
	}

	// Округляем итоги
	for k, v := range costByCurrency {
		costByCurrency[k] = math.Round(v*100) / 100
	}
	var moduleSummaries []ModuleSummary
	for _, mt := range moduleTotals {
		mt.TotalCost = math.Round(mt.TotalCost*100) / 100
		if mt.DaysCount > 0 {
			mt.AvgUnits = math.Round(float64(mt.TotalUnits)/float64(mt.DaysCount)*10) / 10
			mt.AvgDailyCost = math.Round(mt.TotalCost/float64(mt.DaysCount)*100) / 100
		}
		moduleSummaries = append(moduleSummaries, *mt)
	}

	// Собираем ответ в порядке дат
	var dailyBreakdown []DayCharges
	for _, dateKey := range dayOrder {
		dailyBreakdown = append(dailyBreakdown, *dayMap[dateKey])
	}

	daysInMonth := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()

	// Конвертация в валюту аккаунта (только для завершённых месяцев)
	// Формула-эталон 1С: round(avg_units) × round(eur_price × rate, 2) = sum_kzt
	var conversion gin.H
	nowTime := time.Now()
	reportEndDate := time.Date(year, time.Month(month)+1, 1, 0, 0, 0, 0, time.UTC)
	isMonthClosed := nowTime.After(reportEndDate) || nowTime.Equal(reportEndDate)
	billingCurrency := "KZT"
	if account != nil && account.BillingCurrency != "" {
		billingCurrency = account.BillingCurrency
	}

	if isMonthClosed && billingCurrency != "EUR" {
		rateDate := reportEndDate
		exchangeRate, err := h.repo.GetExchangeRateByDate("EUR", rateDate)
		if err == nil && exchangeRate != nil {
			rate := exchangeRate.Rate

			// Считаем KZT-итог по формуле 1С: для каждого модуля отдельно
			var totalKZT float64
			type ConvertedDetail struct {
				ModuleName   string  `json:"module_name"`
				Quantity     float64 `json:"quantity"`
				UnitPriceKZT float64 `json:"unit_price_kzt"`
				TotalKZT     float64 `json:"total_kzt"`
			}
			var convertedDetails []ConvertedDetail

			for _, ms := range moduleSummaries {
				qty := math.Round(ms.AvgUnits) // целое кол-во, как в 1С
				if ms.PricingType == "fixed" {
					qty = 1
				}
				priceKZT := math.Round(ms.UnitPrice*rate*100) / 100 // цена за единицу в KZT
				sumKZT := math.Round(qty*priceKZT*100) / 100        // Кол-во × Цена = Сумма
				totalKZT += sumKZT

				convertedDetails = append(convertedDetails, ConvertedDetail{
					ModuleName:   ms.ModuleName,
					Quantity:     qty,
					UnitPriceKZT: priceKZT,
					TotalKZT:     sumKZT,
				})
			}

			convertedTotals := map[string]float64{
				billingCurrency: math.Round(totalKZT*100) / 100,
			}

			// Ежедневные KZT-значения: распределяем totalKZT по дням равномерно
			if len(dailyBreakdown) > 0 {
				baseDailyKZT := math.Floor(totalKZT/float64(daysInMonth)*100) / 100
				distributedSum := baseDailyKZT * float64(len(dailyBreakdown)-1)
				lastDayKZT := math.Round((totalKZT-distributedSum)*100) / 100

				for i := range dailyBreakdown {
					if i < len(dailyBreakdown)-1 {
						dailyBreakdown[i].DayCostLocal = baseDailyKZT
					} else {
						dailyBreakdown[i].DayCostLocal = lastDayKZT
					}
					dailyBreakdown[i].LocalCurrency = billingCurrency
				}
			}

			conversion = gin.H{
				"rate":              rate,
				"rate_date":         rateDate.Format("2006-01-02"),
				"billing_currency":  billingCurrency,
				"converted_totals":  convertedTotals,
				"converted_details": convertedDetails,
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"account": gin.H{
			"id":        account.ID,
			"name":      account.Name,
			"wialon_id": account.WialonID,
		},
		"period": gin.H{
			"year":          year,
			"month":         month,
			"days_in_month": daysInMonth,
		},
		"daily_breakdown": dailyBreakdown,
		"monthly_totals": gin.H{
			"cost_by_currency": costByCurrency,
			"cost_details":     moduleSummaries,
		},
		"conversion": conversion,
	})
}

// GenerateChargesExcelBytes генерирует Excel-отчёт начислений и возвращает байты
func GenerateChargesExcelBytes(repo *repository.Repository, accountID uint, year, month int) ([]byte, error) {
	charges, err := repo.GetDailyCharges(accountID, year, month)
	if err != nil {
		return nil, err
	}

	account, _ := repo.GetAccountByID(accountID)
	accountName := "Аккаунт"
	if account != nil {
		accountName = account.Name
	}

	f := excelize.NewFile()
	sheet := "Детализация"
	f.SetSheetName("Sheet1", sheet)

	monthNames := []string{"", "Январь", "Февраль", "Март", "Апрель", "Май", "Июнь",
		"Июль", "Август", "Сентябрь", "Октябрь", "Ноябрь", "Декабрь"}
	title := fmt.Sprintf("Детализация начислений: %s — %s %d", accountName, monthNames[month], year)
	f.SetCellValue(sheet, "A1", title)

	headers := []string{"Дата", "Объектов", "Модуль", "Тип", "Цена", "Стоимость/день", "Валюта"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 3)
		f.SetCellValue(sheet, cell, h)
	}

	headerStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"#E2EFDA"}},
		Alignment: &excelize.Alignment{Horizontal: "center"},
	})
	f.SetCellStyle(sheet, "A3", "G3", headerStyle)

	row := 4
	totalByCurrency := make(map[string]float64)
	for _, ch := range charges {
		f.SetCellValue(sheet, fmt.Sprintf("A%d", row), ch.ChargeDate.Format("02.01.2006"))
		f.SetCellValue(sheet, fmt.Sprintf("B%d", row), ch.TotalUnits)
		f.SetCellValue(sheet, fmt.Sprintf("C%d", row), ch.ModuleName)
		pricingLabel := "за объект"
		if ch.PricingType == "fixed" {
			pricingLabel = "фиксир."
		}
		f.SetCellValue(sheet, fmt.Sprintf("D%d", row), pricingLabel)
		f.SetCellValue(sheet, fmt.Sprintf("E%d", row), ch.UnitPrice)
		cost := math.Round(ch.DailyCost*100) / 100
		f.SetCellValue(sheet, fmt.Sprintf("F%d", row), cost)
		f.SetCellValue(sheet, fmt.Sprintf("G%d", row), ch.Currency)
		totalByCurrency[ch.Currency] += ch.DailyCost
		row++
	}

	row++
	totalStyle, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true, Size: 11},
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"#E2EFDA"}},
	})
	i := 0
	for currency, total := range totalByCurrency {
		f.SetCellValue(sheet, fmt.Sprintf("D%d", row+i), "ИТОГО:")
		f.SetCellValue(sheet, fmt.Sprintf("F%d", row+i), math.Round(total*100)/100)
		f.SetCellValue(sheet, fmt.Sprintf("G%d", row+i), currency)
		f.SetCellStyle(sheet, fmt.Sprintf("D%d", row+i), fmt.Sprintf("G%d", row+i), totalStyle)
		i++
	}

	nowTime := time.Now()
	reportEndDate := time.Date(year, time.Month(month)+1, 1, 0, 0, 0, 0, time.UTC)
	isMonthClosed := nowTime.After(reportEndDate) || nowTime.Equal(reportEndDate)
	billingCurrency := "KZT"
	if account != nil && account.BillingCurrency != "" {
		billingCurrency = account.BillingCurrency
	}

	if isMonthClosed && billingCurrency != "EUR" {
		rateDate := reportEndDate
		exchangeRate, err := repo.GetExchangeRateByDate("EUR", rateDate)
		if err == nil && exchangeRate != nil {
			rate := exchangeRate.Rate

			type excelModule struct {
				ModuleID    uint
				UnitPrice   float64
				PricingType string
				TotalUnits  int
				DaysCount   int
			}
			excelModules := make(map[uint]*excelModule)

			for _, ch := range charges {
				em, ok := excelModules[ch.ModuleID]
				if !ok {
					em = &excelModule{
						ModuleID:    ch.ModuleID,
						UnitPrice:   ch.UnitPrice,
						PricingType: ch.PricingType,
					}
					excelModules[ch.ModuleID] = em
				}
				em.TotalUnits += ch.TotalUnits
				em.DaysCount++
			}

			var totalKZT float64
			for _, em := range excelModules {
				qty := math.Round(float64(em.TotalUnits) / float64(em.DaysCount))
				if em.PricingType == "fixed" {
					qty = 1
				}
				priceKZT := math.Round(em.UnitPrice*rate*100) / 100
				sumKZT := math.Round(qty*priceKZT*100) / 100
				totalKZT += sumKZT
			}
			totalKZT = math.Round(totalKZT*100) / 100

			row = row + i + 1

			convertStyle, _ := f.NewStyle(&excelize.Style{
				Font: &excelize.Font{Bold: true, Size: 11, Color: "#1F4E79"},
				Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"#DAEEF3"}},
			})

			f.SetCellValue(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("Курс EUR/%s на %s:", billingCurrency, rateDate.Format("02.01.2006")))
			f.SetCellValue(sheet, fmt.Sprintf("F%d", row), rate)
			f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("G%d", row), convertStyle)
			row++

			f.SetCellValue(sheet, fmt.Sprintf("D%d", row), fmt.Sprintf("ИТОГО (%s):", billingCurrency))
			f.SetCellValue(sheet, fmt.Sprintf("F%d", row), totalKZT)
			f.SetCellValue(sheet, fmt.Sprintf("G%d", row), billingCurrency)
			f.SetCellStyle(sheet, fmt.Sprintf("D%d", row), fmt.Sprintf("G%d", row), convertStyle)
		} else {
			log.Printf("GenerateChargesExcelBytes: курс EUR/%s на %s не найден: %v", billingCurrency, rateDate.Format("2006-01-02"), err)
		}
	}

	f.SetColWidth(sheet, "A", "A", 14)
	f.SetColWidth(sheet, "B", "B", 12)
	f.SetColWidth(sheet, "C", "C", 25)
	f.SetColWidth(sheet, "D", "D", 18)
	f.SetColWidth(sheet, "E", "E", 12)
	f.SetColWidth(sheet, "F", "F", 18)
	f.SetColWidth(sheet, "G", "G", 10)

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ExportAccountChargesExcel экспортирует детализацию начислений в Excel
func (h *Handler) ExportAccountChargesExcel(c *gin.Context) {
	idStr := c.Param("id")
	accountID, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID"})
		return
	}

	now := time.Now()
	year := now.Year()
	month := int(now.Month())
	if yearStr := c.Query("year"); yearStr != "" {
		if y, err := strconv.Atoi(yearStr); err == nil {
			year = y
		}
	}
	if monthStr := c.Query("month"); monthStr != "" {
		if m, err := strconv.Atoi(monthStr); err == nil {
			month = m
		}
	}

	h.snapshot.CalculateDailyChargesForPeriod(uint(accountID), year, month)

	excelData, err := GenerateChargesExcelBytes(h.repo, uint(accountID), year, month)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка генерации Excel"})
		return
	}

	account, _ := h.repo.GetAccountByID(uint(accountID))
	accountName := "Аккаунт"
	if account != nil {
		accountName = account.Name
	}

	filename := fmt.Sprintf("charges_%s_%d-%02d.xlsx", accountName, year, month)
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", excelData)
}

// === Partner Portal ===

// GetPartnerAccount возвращает данные аккаунта партнёра
func (h *Handler) GetPartnerAccount(c *gin.Context) {
	partnerWialonID, exists := c.Get("partnerWialonID")
	if !exists || partnerWialonID == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Нет привязки к аккаунту"})
		return
	}

	wialonID := partnerWialonID.(*int64)
	account, err := h.repo.GetAccountByWialonID(*wialonID)
	if err != nil || account == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Аккаунт не найден"})
		return
	}

	// Проверяем актуальный статус блокировки в Wialon API
	if h.wialon != nil {
		if accData, err := h.wialon.GetAccountData(*wialonID); err == nil && accData != nil && accData.Enabled != nil {
			newBlocked := *accData.Enabled == 0
			if account.IsBlocked != newBlocked {
				account.IsBlocked = newBlocked
				_ = h.repo.UpdateAccount(account)
				log.Printf("GetPartnerAccount: обновлён статус блокировки для %s (wialon_id=%d): is_blocked=%v",
					account.Name, *wialonID, newBlocked)
			}
		}
	}

	c.JSON(http.StatusOK, account)
}

// GetPartnerInvoices возвращает счета партнёра
func (h *Handler) GetPartnerInvoices(c *gin.Context) {
	partnerWialonID, exists := c.Get("partnerWialonID")
	if !exists || partnerWialonID == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Нет привязки к аккаунту"})
		return
	}

	wialonID := partnerWialonID.(*int64)
	invoices, err := h.repo.GetInvoicesByWialonID(*wialonID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, invoices)
}

// GetPartnerCharges возвращает начисления партнёра за месяц
func (h *Handler) GetPartnerCharges(c *gin.Context) {
	partnerWialonID, exists := c.Get("partnerWialonID")
	if !exists || partnerWialonID == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Нет привязки к аккаунту"})
		return
	}

	wialonID := partnerWialonID.(*int64)

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

	// Пересчитываем начисления (на случай если ещё не рассчитаны за текущий/выбранный месяц)
	account, err := h.repo.GetAccountByWialonID(*wialonID)
	if err == nil && account != nil {
		if calcErr := h.snapshot.CalculateDailyChargesForPeriod(account.ID, year, month); calcErr != nil {
			log.Printf("GetPartnerCharges: ошибка пересчёта начислений для аккаунта %d: %v", account.ID, calcErr)
		}
	}

	charges, err := h.repo.GetDailyChargesByWialonID(*wialonID, year, month)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"charges": charges,
		"year":    year,
		"month":   month,
	})
}

// GetPartnerBalance возвращает сводку по балансу партнёра
func (h *Handler) GetPartnerBalance(c *gin.Context) {
	partnerWialonID, exists := c.Get("partnerWialonID")
	if !exists || partnerWialonID == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Нет привязки к аккаунту"})
		return
	}

	wialonID := partnerWialonID.(*int64)

	// Получаем аккаунт
	account, err := h.repo.GetAccountByWialonID(*wialonID)
	if err != nil || account == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Аккаунт не найден"})
		return
	}

	// Получаем все счета
	invoices, err := h.repo.GetInvoicesByWialonID(*wialonID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Считаем статистику по счетам
	var totalInvoiced float64
	var totalPaid float64
	var pendingCount int
	var paidCount int

	for _, inv := range invoices {
		totalInvoiced += inv.TotalAmount
		if inv.Status == "paid" {
			totalPaid += inv.TotalAmount
			paidCount++
		} else {
			pendingCount++
		}
	}

	// Получаем начисления за текущий месяц (с предварительным пересчётом)
	now := time.Now()
	if calcErr := h.snapshot.CalculateDailyChargesForPeriod(account.ID, now.Year(), int(now.Month())); calcErr != nil {
		log.Printf("GetPartnerBalance: ошибка пересчёта начислений за текущий месяц для аккаунта %d: %v", account.ID, calcErr)
	}
	charges, _ := h.repo.GetDailyChargesByWialonID(*wialonID, now.Year(), int(now.Month()))

	var currentMonthTotal float64
	for _, ch := range charges {
		currentMonthTotal += ch.DailyCost
	}

	c.JSON(http.StatusOK, gin.H{
		"account_name":        account.Name,
		"wialon_id":           account.WialonID,
		"billing_currency":    account.BillingCurrency,
		"total_invoiced":      math.Round(totalInvoiced*100) / 100,
		"total_paid":          math.Round(totalPaid*100) / 100,
		"outstanding_balance": math.Round((totalInvoiced-totalPaid)*100) / 100,
		"current_month_total": math.Round(currentMonthTotal*100) / 100,
		"invoices_count":      len(invoices),
		"pending_count":       pendingCount,
		"paid_count":          paidCount,
	})
}

// GetPartnerSnapshots возвращает снимки (данные по дням) для партнёра
func (h *Handler) GetPartnerSnapshots(c *gin.Context) {
	partnerWialonID, exists := c.Get("partnerWialonID")
	if !exists || partnerWialonID == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Нет привязки к аккаунту"})
		return
	}

	wialonID := partnerWialonID.(*int64)

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

	snapshots, err := h.repo.GetSnapshotsByWialonID(*wialonID, year, month)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Формируем ответ с нужными полями
	type SnapshotDay struct {
		Date             string `json:"date"`
		TotalUnits       int    `json:"total_units"`
		UnitsCreated     int    `json:"units_created"`
		UnitsDeleted     int    `json:"units_deleted"`
		UnitsDeactivated int    `json:"units_deactivated"`
	}

	var days []SnapshotDay
	for _, s := range snapshots {
		days = append(days, SnapshotDay{
			Date:             s.SnapshotDate.Format("2006-01-02"),
			TotalUnits:       s.TotalUnits,
			UnitsCreated:     s.UnitsCreated,
			UnitsDeleted:     s.UnitsDeleted,
			UnitsDeactivated: s.UnitsDeactivated,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"snapshots": days,
		"year":      year,
		"month":     month,
	})
}

// GetPartnerInvoicePDF возвращает PDF счёта партнёра
func (h *Handler) GetPartnerInvoicePDF(c *gin.Context) {
	partnerWialonID, exists := c.Get("partnerWialonID")
	if !exists || partnerWialonID == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Нет привязки к аккаунту"})
		return
	}

	wialonID := partnerWialonID.(*int64)

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

	// Проверяем принадлежность счёта партнёру
	account, err := h.repo.GetAccountByID(inv.AccountID)
	if err != nil || account == nil || account.WialonID != *wialonID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Счёт не принадлежит вашему аккаунту"})
		return
	}

	// Получаем настройки
	settings, err := h.repo.GetSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка получения настроек"})
		return
	}

	// Генерируем PDF
	generator := invoicesvc.NewPDFGenerator()
	pdfBytes, err := generator.GenerateInvoicePDF(inv, settings, account)
	if err != nil {
		log.Printf("Ошибка генерации PDF для партнёрского счёта %d: %v", inv.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка генерации PDF"})
		return
	}

	// Имя файла: используем номер счёта (заменяем / на _)
	partnerInvoiceNum := inv.Number
	if partnerInvoiceNum == "" {
		partnerInvoiceNum = fmt.Sprintf("%d", inv.ID)
	}
	filename := fmt.Sprintf("invoice_%s.pdf", strings.ReplaceAll(partnerInvoiceNum, "/", "_"))
	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}
