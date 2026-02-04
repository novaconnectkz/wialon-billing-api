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
	"github.com/user/wialon-billing-api/internal/repository"
	"github.com/user/wialon-billing-api/internal/services/ai"
	"github.com/user/wialon-billing-api/internal/services/auth"
	"github.com/user/wialon-billing-api/internal/services/invoice"
	"github.com/user/wialon-billing-api/internal/services/nbk"
	"github.com/user/wialon-billing-api/internal/services/snapshot"
	"github.com/user/wialon-billing-api/internal/services/wialon"
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
	invoiceService := invoice.NewService(db, repo)

	// Инициализация AI сервиса
	aiService := ai.NewService(repo)
	if err := aiService.Initialize(context.Background()); err != nil {
		log.Printf("[AI] Предупреждение: ошибка инициализации AI: %v", err)
	}

	// Инициализация cron-задач
	c := cron.New(cron.WithLocation(time.UTC))

	// Ежедневные снимки в 00:01 UTC (снимок создаётся за предыдущий день)
	_, err = c.AddFunc("1 0 * * *", func() {
		log.Println("Запуск ежедневного снимка...")
		if err := snapshotService.CreateDailySnapshot(); err != nil {
			log.Printf("Ошибка создания снимка: %v", err)
		}
	})
	if err != nil {
		log.Fatalf("Ошибка добавления cron-задачи снимков: %v", err)
	}

	// Курсы валют НБК - ежедневно в 04:00 UTC (07:00 по Москве, 09:00 по Казахстану)
	_, err = c.AddFunc("0 4 * * *", func() {
		log.Println("Запуск получения курсов НБК...")
		if err := nbkService.FetchExchangeRates(); err != nil {
			log.Printf("Ошибка получения курсов: %v", err)
		}
	})
	if err != nil {
		log.Fatalf("Ошибка добавления cron-задачи курсов: %v", err)
	}

	// Загружаем курсы при старте если их нет
	go func() {
		log.Println("Проверка курсов при запуске...")
		if err := nbkService.FetchExchangeRates(); err != nil {
			log.Printf("Ошибка загрузки курсов при старте: %v", err)
		}
	}()

	c.Start()
	defer c.Stop()

	// Инициализация HTTP-сервера
	router := gin.Default()

	// CORS middleware
	router.Use(middleware.CORS())

	// Auth handlers
	authHandler := auth.NewAuthHandler(repo)

	// API handlers
	h := handlers.NewHandler(repo, wialonClient, snapshotService, nbkService, invoiceService)
	connHandler := handlers.NewConnectionHandler(repo, wialonClient)
	aiHandler := handlers.NewAIHandler(aiService)

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
		}

		// AI Analytics (настройки - для админов, инсайты - для всех)
		aiRoutes := api.Group("/ai")
		aiRoutes.Use(middleware.Auth())
		{
			// Инсайты - для всех авторизованных
			aiRoutes.GET("/insights", aiHandler.GetAIInsights)
			aiRoutes.GET("/insights/account/:account_id", aiHandler.GetAccountInsights)
			aiRoutes.POST("/insights/:id/feedback", aiHandler.SendInsightFeedback)

			// Настройки и управление - только для админов
			aiAdmin := aiRoutes.Group("")
			aiAdmin.Use(middleware.RequireAdmin())
			{
				aiAdmin.GET("/settings", aiHandler.GetAISettings)
				aiAdmin.PUT("/settings", aiHandler.UpdateAISettings)
				aiAdmin.GET("/usage", aiHandler.GetAIUsage)
				aiAdmin.POST("/analyze", aiHandler.TriggerAnalysis)
			}
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
