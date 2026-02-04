package invoice

import (
	"log"
	"time"

	"github.com/user/wialon-billing-api/internal/models"
	"gorm.io/gorm"
)

// Repository - интерфейс для работы с БД
type Repository interface {
	GetSelectedAccounts() ([]models.Account, error)
	GetAccountModules(accountID uint) ([]models.AccountModule, error)
	GetSnapshotsByAccountAndPeriod(accountID uint, year, month int) ([]models.Snapshot, error)
	CreateInvoice(invoice *models.Invoice) error
	CreateInvoiceLine(line *models.InvoiceLine) error
	GetInvoiceByAccountAndPeriod(accountID uint, period time.Time) (*models.Invoice, error)
	DeleteInvoice(invoiceID uint) error
	DeleteInvoiceLines(invoiceID uint) error
}

// Service - сервис для работы со счетами
type Service struct {
	db   *gorm.DB
	repo Repository
}

// NewService создаёт новый сервис
func NewService(db *gorm.DB, repo Repository) *Service {
	return &Service{db: db, repo: repo}
}

// GenerateMonthlyInvoices генерирует счета за указанный месяц для всех аккаунтов
func (s *Service) GenerateMonthlyInvoices(period time.Time) ([]models.Invoice, error) {
	// Нормализуем период до 1-го числа месяца
	period = time.Date(period.Year(), period.Month(), 1, 0, 0, 0, 0, time.Local)

	// Получаем все аккаунты с включённым биллингом
	accounts, err := s.repo.GetSelectedAccounts()
	if err != nil {
		return nil, err
	}

	var invoices []models.Invoice

	for _, account := range accounts {
		invoice, err := s.generateInvoiceForAccount(account, period)
		if err != nil {
			log.Printf("Ошибка генерации счёта для %s: %v", account.Name, err)
			continue
		}
		if invoice != nil {
			invoices = append(invoices, *invoice)
		}
	}

	log.Printf("Сгенерировано %d счетов за %s", len(invoices), period.Format("01.2006"))
	return invoices, nil
}

// generateInvoiceForAccount создаёт счёт для одного аккаунта
func (s *Service) generateInvoiceForAccount(account models.Account, period time.Time) (*models.Invoice, error) {
	// Получаем модули аккаунта
	accountModules, err := s.repo.GetAccountModules(account.ID)
	if err != nil {
		return nil, err
	}

	if len(accountModules) == 0 {
		log.Printf("У аккаунта %s нет подключённых модулей", account.Name)
		return nil, nil
	}

	// Получаем среднее количество объектов за месяц
	avgUnits, err := s.calculateAverageUnits(account.ID, period.Year(), int(period.Month()))
	if err != nil {
		log.Printf("Ошибка расчёта среднего для %s: %v", account.Name, err)
		avgUnits = 0
	}

	// Проверяем, есть ли уже счёт за этот период
	existingInvoice, _ := s.repo.GetInvoiceByAccountAndPeriod(account.ID, period)
	if existingInvoice != nil {
		// Удаляем старый счёт (пересчёт)
		if err := s.repo.DeleteInvoiceLines(existingInvoice.ID); err != nil {
			return nil, err
		}
		if err := s.repo.DeleteInvoice(existingInvoice.ID); err != nil {
			return nil, err
		}
		log.Printf("Удалён старый счёт #%d для %s", existingInvoice.ID, account.Name)
	}

	// Рассчитываем стоимость по каждому модулю
	var totalAmount float64
	var currency string
	var lines []models.InvoiceLine

	for _, am := range accountModules {
		module := am.Module

		var quantity float64
		var totalPrice float64

		if module.PricingType == "fixed" {
			// Фиксированная цена
			quantity = 1
			totalPrice = module.Price
		} else {
			// per_unit — цена за объект
			quantity = avgUnits
			totalPrice = avgUnits * module.Price
		}

		line := models.InvoiceLine{
			ModuleID:    module.ID,
			ModuleName:  module.Name,
			Quantity:    quantity,
			UnitPrice:   module.Price,
			TotalPrice:  totalPrice,
			Currency:    module.Currency,
			PricingType: module.PricingType,
		}
		lines = append(lines, line)
		totalAmount += totalPrice
		currency = module.Currency // Берём валюту последнего модуля (TODO: валидация)
	}

	if totalAmount == 0 {
		log.Printf("Нулевой счёт для %s, пропускаем", account.Name)
		return nil, nil
	}

	// Создаём счёт
	invoice := &models.Invoice{
		AccountID:   account.ID,
		Period:      period,
		TotalAmount: totalAmount,
		Currency:    currency,
		Status:      "draft",
	}

	if err := s.repo.CreateInvoice(invoice); err != nil {
		return nil, err
	}

	// Создаём строки счёта
	for i := range lines {
		lines[i].InvoiceID = invoice.ID
		if err := s.repo.CreateInvoiceLine(&lines[i]); err != nil {
			log.Printf("Ошибка создания строки счёта: %v", err)
		}
	}

	invoice.Lines = lines
	log.Printf("Создан счёт #%d для %s: %.2f %s", invoice.ID, account.Name, totalAmount, currency)

	return invoice, nil
}

// calculateAverageUnits рассчитывает среднее количество объектов за месяц
func (s *Service) calculateAverageUnits(accountID uint, year, month int) (float64, error) {
	snapshots, err := s.repo.GetSnapshotsByAccountAndPeriod(accountID, year, month)
	if err != nil {
		return 0, err
	}

	if len(snapshots) == 0 {
		return 0, nil
	}

	// Считаем сумму объектов по всем дням
	var totalUnits int
	for _, s := range snapshots {
		totalUnits += s.TotalUnits
	}

	// Количество дней в месяце
	daysInMonth := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()

	// Среднее = сумма / дней в месяце
	return float64(totalUnits) / float64(daysInMonth), nil
}

// RecalculateCurrentPeriod пересчитывает счёт за текущий период
func (s *Service) RecalculateCurrentPeriod(accountID uint) (*models.Invoice, error) {
	now := time.Now()
	period := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)

	// Получаем аккаунт
	var account models.Account
	if err := s.db.First(&account, accountID).Error; err != nil {
		return nil, err
	}

	return s.generateInvoiceForAccount(account, period)
}
