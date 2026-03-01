package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/robfig/cron/v3"
	"github.com/user/wialon-billing-api/internal/config"
	"github.com/user/wialon-billing-api/internal/handlers"
	"github.com/user/wialon-billing-api/internal/middleware"
	"github.com/user/wialon-billing-api/internal/models"
	"github.com/user/wialon-billing-api/internal/repository"
	"github.com/user/wialon-billing-api/internal/services/ai"
	"github.com/user/wialon-billing-api/internal/services/auth"
	"github.com/user/wialon-billing-api/internal/services/email"
	"github.com/user/wialon-billing-api/internal/services/invoice"
	"github.com/user/wialon-billing-api/internal/services/nbk"
	"github.com/user/wialon-billing-api/internal/services/snapshot"
	"github.com/user/wialon-billing-api/internal/services/wialon"
	"gorm.io/gorm"
)

func main() {
	// Загрузка конфигурации
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("Ошибка загрузки конфигурации: %v", err)
	}

	// Подключение к БД
	db, err := repository.NewPostgresDB(cfg.Database)
	if err != nil {
		log.Fatalf("Ошибка подключения к БД: %v", err)
	}

	// Инициализация репозиториев
	repo := repository.NewRepository(db)

	// Инициализация сервисов
	wialonClient := wialon.NewClient(cfg.Wialon)
	snapshotService := snapshot.NewService(repo, wialonClient)
	nbkService := nbk.NewService(repo)
	invoiceService := invoice.NewService(db, repo, nbkService)

	// Инициализация AI сервиса
	aiService := ai.NewService(repo)
	if err := aiService.Initialize(context.Background()); err != nil {
		log.Printf("[AI] Предупреждение: ошибка инициализации AI: %v", err)
	}

	// Инициализация cron-задач
	c := cron.New(cron.WithLocation(time.UTC))

	// Снимки — каждый час, идемпотентно (проверяет наличие снимка за вчера)
	_, err = c.AddFunc("0 * * * *", func() {
		log.Println("[Cron] Проверка снимков...")
		if err := snapshotService.EnsureDailySnapshot(); err != nil {
			log.Printf("[Cron] Ошибка создания снимка: %v", err)
		}
	})
	if err != nil {
		log.Fatalf("Ошибка добавления cron-задачи снимков: %v", err)
	}

	// Курсы валют НБК - ежедневно в 04:00 UTC (09:00 по Казахстану)
	_, err = c.AddFunc("0 4 * * *", func() {
		log.Println("Запуск получения курсов НБК...")
		if err := nbkService.FetchExchangeRates(); err != nil {
			log.Printf("Ошибка получения курсов: %v", err)
		}
	})
	if err != nil {
		log.Fatalf("Ошибка добавления cron-задачи курсов: %v", err)
	}

	// Проверка снимков, курсов и AI анализ при запуске приложения
	go func() {
		log.Println("[Старт] Проверка курсов...")
		if err := nbkService.FetchExchangeRates(); err != nil {
			log.Printf("[Старт] Ошибка загрузки курсов: %v", err)
		}
		log.Println("[Старт] Проверка снимков за вчера...")
		if err := snapshotService.EnsureDailySnapshot(); err != nil {
			log.Printf("[Старт] Ошибка создания снимка: %v", err)
		}
		// Запуск AI анализа аккаунтов при старте
		log.Println("[Старт] Запуск AI анализа аккаунтов...")
		if err := aiService.AnalyzeLatestSnapshots(context.Background()); err != nil {
			log.Printf("[Старт] Ошибка AI анализа: %v", err)
		} else {
			log.Println("[Старт] AI анализ аккаунтов завершён")
		}
	}()

	// Генерация счетов — 1-го числа каждого месяца в 03:00 UTC
	// Если курсы НБК недоступны — повторяем каждый час
	_, err = c.AddFunc("0 3 1 * *", func() {
		log.Println("[Счета] Запуск автоматической генерации счетов...")
		go generateInvoicesWithRetry(invoiceService, nbkService)
	})
	if err != nil {
		log.Fatalf("Ошибка добавления cron-задачи счетов: %v", err)
	}

	// AI анализ аккаунтов — ежедневно в 05:00 UTC (после завершения снимков)
	_, err = c.AddFunc("0 5 * * *", func() {
		log.Println("[AI Cron] Запуск ежедневного анализа аккаунтов...")
		if err := aiService.AnalyzeLatestSnapshots(context.Background()); err != nil {
			log.Printf("[AI Cron] Ошибка анализа: %v", err)
		}
	})
	if err != nil {
		log.Fatalf("Ошибка добавления cron-задачи AI анализа: %v", err)
	}

	c.Start()
	defer c.Stop()

	// Инициализация HTTP-сервера
	router := gin.Default()

	// CORS middleware
	router.Use(middleware.CORS())

	// Инициализация Email-сервиса
	emailService := email.NewService(repo)

	// Сид дефолтных шаблонов писем
	seedEmailTemplates(db)

	// Auth handlers
	authHandler := auth.NewAuthHandler(repo, emailService)

	// API handlers
	h := handlers.NewHandler(repo, wialonClient, snapshotService, nbkService, invoiceService)
	connHandler := handlers.NewConnectionHandler(repo, wialonClient)
	aiHandler := handlers.NewAIHandler(aiService)
	smtpHandler := handlers.NewSMTPHandler(repo, emailService, invoiceService)

	// Маршруты API
	api := router.Group("/api")
	{
		// Авторизация (без middleware)
		api.POST("/auth/request-code", authHandler.RequestCode)
		api.POST("/auth/verify-code", authHandler.VerifyCode)
		api.GET("/auth/me", middleware.Auth(), authHandler.GetCurrentUser)

		// Wialon подключения (только для админов)
		connections := api.Group("/connections")
		connections.Use(middleware.Auth(), middleware.RequireAdmin())
		{
			connections.GET("", connHandler.GetConnections)
			connections.POST("", connHandler.CreateConnection)
			connections.PUT("/:id", connHandler.UpdateConnection)
			connections.DELETE("/:id", connHandler.DeleteConnection)
			connections.POST("/:id/test", connHandler.TestConnection)
		}

		// Учётные записи (общие для всех авторизованных)
		accounts := api.Group("/accounts")
		accounts.Use(middleware.Auth(), middleware.DealerContext())
		{
			accounts.GET("", h.GetAccounts)
			accounts.GET("/selected", h.GetSelectedAccounts)
			accounts.GET("/:id/history", h.GetAccountHistory)
			accounts.GET("/:id/stats", h.GetAccountStats)
			accounts.GET("/:id/charges", h.GetAccountCharges)
			accounts.GET("/:id/charges/excel", h.ExportAccountChargesExcel)
		}

		// Учётные записи (только для админов)
		adminAccounts := api.Group("/accounts")
		adminAccounts.Use(middleware.Auth(), middleware.RequireAdmin())
		{
			adminAccounts.POST("/sync", h.SyncAccounts)
			adminAccounts.PUT("/:id/toggle", h.ToggleAccount)
			adminAccounts.PUT("/:id/details", h.UpdateAccountDetails)
			adminAccounts.POST("/:id/modules", h.AssignModule)
			adminAccounts.POST("/:id/invite", h.InviteDealer)
		}

		// Модули (только для админов)
		modules := api.Group("/modules")
		modules.Use(middleware.Auth(), middleware.RequireAdmin())
		{
			modules.GET("", h.GetModules)
			modules.POST("", h.CreateModule)
			modules.PUT("/:id", h.UpdateModule)
			modules.DELETE("/:id", h.DeleteModule)
			modules.POST("/:id/assign-bulk", h.AssignModuleBulk)
			modules.POST("/:id/unassign-bulk", h.UnassignModuleBulk)
		}

		// Массовая установка валюты
		api.POST("/accounts/set-currency-bulk", middleware.Auth(), middleware.RequireAdmin(), h.SetCurrencyBulk)

		// Настройки (только для админов)
		settings := api.Group("/settings")
		settings.Use(middleware.Auth(), middleware.RequireAdmin())
		{
			settings.GET("", h.GetSettings)
			settings.PUT("", h.UpdateSettings)
		}

		// Курсы валют (только для админов)
		api.GET("/exchange-rates", middleware.Auth(), h.GetExchangeRates)
		api.POST("/exchange-rates/backfill", middleware.Auth(), middleware.RequireAdmin(), h.BackfillExchangeRates)

		// Dashboard (для всех авторизованных, с фильтрацией по дилеру)
		api.GET("/dashboard", middleware.Auth(), middleware.DealerContext(), h.GetDashboard)

		// Снимки: GET для всех (с фильтрацией для дилеров), POST только для админов
		api.GET("/snapshots", middleware.Auth(), middleware.DealerContext(), h.GetSnapshots)

		snapshotsAdmin := api.Group("/snapshots")
		snapshotsAdmin.Use(middleware.Auth(), middleware.RequireAdmin())
		{
			snapshotsAdmin.POST("", h.CreateSnapshot)
			snapshotsAdmin.POST("/date", h.CreateSnapshotsForDate)
			snapshotsAdmin.POST("/range", h.CreateSnapshotsForRange)
			snapshotsAdmin.DELETE("/clear", h.ClearAllSnapshots)
		}

		// Изменения (для всех авторизованных)
		api.GET("/changes", middleware.Auth(), middleware.DealerContext(), h.GetChanges)

		// Счета (только для админов)
		invoices := api.Group("/invoices")
		invoices.Use(middleware.Auth(), middleware.RequireAdmin())
		{
			invoices.GET("", h.GetInvoices)
			invoices.GET("/:id", h.GetInvoice)
			invoices.GET("/:id/pdf", h.GetInvoicePDF)
			invoices.POST("/generate", h.GenerateInvoices)
			invoices.PUT("/:id/status", h.UpdateInvoiceStatus)
			invoices.DELETE("/clear", h.ClearAllInvoices)
			invoices.POST("/:id/send", smtpHandler.SendInvoiceEmail)
		}

		// SMTP и шаблоны писем (только для админов)
		smtpRoutes := api.Group("/smtp")
		smtpRoutes.Use(middleware.Auth(), middleware.RequireAdmin())
		{
			smtpRoutes.GET("/settings", smtpHandler.GetSMTPSettings)
			smtpRoutes.PUT("/settings", smtpHandler.UpdateSMTPSettings)
			smtpRoutes.POST("/test", smtpHandler.TestSMTPConnection)
			smtpRoutes.GET("/templates", smtpHandler.GetEmailTemplates)
			smtpRoutes.GET("/templates/:type", smtpHandler.GetEmailTemplate)
			smtpRoutes.PUT("/templates/:type", smtpHandler.UpdateEmailTemplate)
			smtpRoutes.POST("/templates/:type/preview", smtpHandler.PreviewEmailTemplate)
		}

		// AI Analytics (настройки - для админов, инсайты - для всех)
		aiRoutes := api.Group("/ai")
		aiRoutes.Use(middleware.Auth())
		{
			// Инсайты - для всех авторизованных
			aiRoutes.GET("/insights", aiHandler.GetAIInsights)
			aiRoutes.GET("/insights/account/:account_id", aiHandler.GetAccountInsights)
			aiRoutes.POST("/insights/:id/feedback", aiHandler.SendInsightFeedback)

			// Тренды флота - для всех авторизованных
			aiRoutes.GET("/fleet-trends", aiHandler.GetFleetTrends)

			// Настройки и управление - только для админов
			aiAdmin := aiRoutes.Group("")
			aiAdmin.Use(middleware.RequireAdmin())
			{
				aiAdmin.GET("/settings", aiHandler.GetAISettings)
				aiAdmin.PUT("/settings", aiHandler.UpdateAISettings)
				aiAdmin.GET("/usage", aiHandler.GetAIUsage)
				aiAdmin.POST("/analyze", aiHandler.TriggerAnalysis)
				aiAdmin.POST("/fleet-analysis", aiHandler.AnalyzeFleetTrends)
			}
		}

		// Wialon OAuth авторизация для партнёров
		api.POST("/auth/wialon-login", authHandler.WialonLogin)

		// Партнёрский портал
		partner := api.Group("/partner")
		partner.Use(middleware.Auth(), middleware.PartnerContext(), middleware.RequirePartner())
		{
			partner.GET("/account", h.GetPartnerAccount)
			partner.GET("/invoices", h.GetPartnerInvoices)
			partner.GET("/invoices/:id/pdf", h.GetPartnerInvoicePDF)
			partner.GET("/charges", h.GetPartnerCharges)
			partner.GET("/balance", h.GetPartnerBalance)
			partner.GET("/snapshots", h.GetPartnerSnapshots)
		}
	}

	// Запуск сервера
	port := cfg.Server.Port
	if port == "" {
		port = "8080"
	}
	log.Printf("Сервер запущен на порту %s", port)
	if err := router.Run(":" + port); err != nil {
		log.Fatalf("Ошибка запуска сервера: %v", err)
		os.Exit(1)
	}
}

// generateInvoicesWithRetry генерирует счета с повтором при отсутствии курсов НБК
func generateInvoicesWithRetry(invoiceService *invoice.Service, nbkService *nbk.Service) {
	now := time.Now()
	// Период — предыдущий месяц
	prevMonth := now.AddDate(0, -1, 0)
	period := time.Date(prevMonth.Year(), prevMonth.Month(), 1, 0, 0, 0, 0, time.Local)
	// Дата курса — 1-е число текущего месяца (следующий после периода)
	rateDate := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	for attempt := 1; attempt <= 24; attempt++ {
		// Пробуем загрузить курсы
		nbkService.FetchExchangeRatesForDate(rateDate)

		if invoiceService.CheckRatesAvailable(rateDate) {
			log.Printf("[Счета] Курсы за %s доступны, генерируем счета (попытка %d)...",
				rateDate.Format("02.01.2006"), attempt)

			invoices, err := invoiceService.GenerateMonthlyInvoices(period)
			if err != nil {
				log.Printf("[Счета] Ошибка генерации: %v", err)
			} else {
				log.Printf("[Счета] Успешно сгенерировано %d счетов за %s",
					len(invoices), period.Format("01.2006"))
			}
			return
		}

		log.Printf("[Счета] Курсы за %s ещё недоступны, повтор через 1 час (попытка %d/24)...",
			rateDate.Format("02.01.2006"), attempt)
		time.Sleep(1 * time.Hour)
	}

	log.Println("[Счета] Курсы не появились за 24 часа. Генерация без конвертации...")
	invoices, err := invoiceService.GenerateMonthlyInvoices(period)
	if err != nil {
		log.Printf("[Счета] Ошибка генерации: %v", err)
	} else {
		log.Printf("[Счета] Сгенерировано %d счетов (без курсов)", len(invoices))
	}
}

// seedEmailTemplates создаёт дефолтные шаблоны писем при первом запуске
func seedEmailTemplates(db *gorm.DB) {
	templates := []models.EmailTemplate{
		{
			Type:    "otp",
			Name:    "Код авторизации",
			Subject: "Ваш код авторизации: {{code}}",
			HTMLBody: `<div style="font-family: Arial, sans-serif; max-width: 600px; margin: 0 auto; padding: 20px;">
<h2 style="color: #333;">Код авторизации</h2>
<p>Ваш код для входа в систему:</p>
<div style="background: #f4f4f4; padding: 20px; text-align: center; border-radius: 8px; margin: 20px 0;">
<span style="font-size: 32px; font-weight: bold; letter-spacing: 8px; color: #2196F3;">{{code}}</span>
</div>
<p style="color: #666; font-size: 14px;">Код действителен {{expires_minutes}} минут.</p>
<hr style="border: none; border-top: 1px solid #eee; margin: 20px 0;">
<p style="color: #999; font-size: 12px;">Если вы не запрашивали код, проигнорируйте это письмо.</p>
</div>`,
			Variables: `["code", "email", "expires_minutes"]`,
			IsActive:  true,
		},
		{
			Type:    "invoice",
			Name:    "Счёт на оплату",
			Subject: "Счёт на оплату №{{invoice_number}} за {{period}}",
			HTMLBody: `<div style="font-family: Arial, sans-serif; max-width: 600px; margin: 0 auto; padding: 20px;">
<h2 style="color: #333;">Счёт на оплату</h2>
<p>Уважаемый клиент <strong>{{company_name}}</strong>,</p>
<p>Во вложении счёт на оплату услуг за {{period}}:</p>
<div style="background: #f8f9fa; padding: 15px; border-radius: 8px; margin: 15px 0;">
<p><strong>Сумма:</strong> {{amount}} {{currency}}</p>
<p><strong>Период:</strong> {{period}}</p>
<p><strong>Номер счёта:</strong> {{invoice_number}}</p>
</div>
<p>Просим оплатить счёт в установленные сроки.</p>
<hr style="border: none; border-top: 1px solid #eee; margin: 20px 0;">
<p style="color: #999; font-size: 12px;">Это автоматическое уведомление от системы Wialon Billing.</p>
</div>`,
			Variables: `["company_name", "sender_company_name", "sender_phone", "period", "amount", "currency", "invoice_number"]`,
			IsActive:  true,
		},
		{
			Type:    "notification",
			Name:    "Уведомление",
			Subject: "{{title}}",
			HTMLBody: `<div style="font-family: Arial, sans-serif; max-width: 600px; margin: 0 auto; padding: 20px;">
<h2 style="color: #333;">{{title}}</h2>
<div style="padding: 15px 0;">{{message}}</div>
<hr style="border: none; border-top: 1px solid #eee; margin: 20px 0;">
<p style="color: #999; font-size: 12px;">Wialon Billing System • {{date}}</p>
</div>`,
			Variables: `["title", "message", "date"]`,
			IsActive:  true,
		},
	}

	for _, tmpl := range templates {
		var existing models.EmailTemplate
		if err := db.Where("type = ?", tmpl.Type).First(&existing).Error; err != nil {
			// Шаблон не найден — создаём
			if err := db.Create(&tmpl).Error; err != nil {
				log.Printf("[Сид] Ошибка создания шаблона '%s': %v", tmpl.Type, err)
			} else {
				log.Printf("[Сид] Создан шаблон письма: %s", tmpl.Type)
			}
		}
	}
}
