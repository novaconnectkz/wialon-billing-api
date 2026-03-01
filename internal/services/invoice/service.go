package invoice

import (
	"fmt"
	"log"
	"math"
	"time"

	"github.com/user/wialon-billing-api/internal/models"
	"github.com/user/wialon-billing-api/internal/repository"
	"github.com/user/wialon-billing-api/internal/services/nbk"
	"gorm.io/gorm"
)

// Service - сервис для работы со счетами
type Service struct {
	db   *gorm.DB
	repo *repository.Repository
	nbk  *nbk.Service
}

// NewService создаёт новый сервис
func NewService(db *gorm.DB, repo *repository.Repository, nbkService *nbk.Service) *Service {
	return &Service{db: db, repo: repo, nbk: nbkService}
}

// GenerateMonthlyInvoices генерирует счета за указанный месяц для всех аккаунтов
func (s *Service) GenerateMonthlyInvoices(period time.Time) ([]models.Invoice, error) {
	// Нормализуем период до 1-го числа месяца
	period = time.Date(period.Year(), period.Month(), 1, 0, 0, 0, 0, time.Local)

	// Дата курса — 1-е число следующего месяца
	rateDate := period.AddDate(0, 1, 0)

	// Загружаем курсы НБК за дату выставления счёта
	if err := s.nbk.FetchExchangeRatesForDate(rateDate); err != nil {
		log.Printf("Предупреждение: ошибка загрузки курсов за %s: %v", rateDate.Format("02.01.2006"), err)
	}

	// Получаем все аккаунты с включённым биллингом
	accounts, err := s.repo.GetSelectedAccounts()
	if err != nil {
		return nil, err
	}

	var invoices []models.Invoice

	for _, account := range accounts {
		invoice, err := s.generateInvoiceForAccount(account, period, rateDate)
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

// GenerateInvoiceForSingleAccount генерирует счёт для одного аккаунта
func (s *Service) GenerateInvoiceForSingleAccount(accountID uint, period time.Time) (*models.Invoice, error) {
	period = time.Date(period.Year(), period.Month(), 1, 0, 0, 0, 0, time.Local)
	rateDate := period.AddDate(0, 1, 0)

	// Загружаем курсы
	if err := s.nbk.FetchExchangeRatesForDate(rateDate); err != nil {
		log.Printf("Предупреждение: ошибка загрузки курсов за %s: %v", rateDate.Format("02.01.2006"), err)
	}

	var account models.Account
	if err := s.db.Preload("Modules.Module").First(&account, accountID).Error; err != nil {
		return nil, fmt.Errorf("аккаунт %d не найден: %w", accountID, err)
	}

	return s.generateInvoiceForAccount(account, period, rateDate)
}

// CheckRatesAvailable проверяет наличие курсов за указанную дату
func (s *Service) CheckRatesAvailable(date time.Time) bool {
	// Проверяем наличие курса EUR за дату
	rate, err := s.repo.GetExchangeRateByDate("EUR", date)
	if err != nil || rate == nil {
		return false
	}
	return true
}

// generateInvoiceForAccount создаёт счёт для одного аккаунта
func (s *Service) generateInvoiceForAccount(account models.Account, period, rateDate time.Time) (*models.Invoice, error) {
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

	// Определяем целевую валюту аккаунта
	targetCurrency := account.BillingCurrency
	if targetCurrency == "" {
		targetCurrency = "KZT"
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
	var lines []models.InvoiceLine

	for _, am := range accountModules {
		module := am.Module

		var quantity float64
		var unitPrice float64
		var totalPrice float64

		if module.PricingType == "fixed" {
			// Фиксированная цена
			quantity = 1
			unitPrice = module.Price

			// Конвертируем цену в валюту аккаунта
			if module.Currency != targetCurrency {
				converted, err := s.convertCurrency(unitPrice, module.Currency, targetCurrency, rateDate)
				if err != nil {
					log.Printf("Ошибка конвертации %s→%s для модуля %s: %v", module.Currency, targetCurrency, module.Name, err)
				} else {
					unitPrice = math.Round(converted*100) / 100
				}
			}
			totalPrice = unitPrice
		} else {
			// per_unit — формула 1С: цену → KZT, потом × кол-во
			quantity = math.Round(avgUnits) // целое число, как в 1С
			unitPrice = module.Price

			// Сначала конвертируем цену ЗА ЕДИНИЦУ в валюту аккаунта
			if module.Currency != targetCurrency {
				converted, err := s.convertCurrency(unitPrice, module.Currency, targetCurrency, rateDate)
				if err != nil {
					log.Printf("Ошибка конвертации %s→%s для модуля %s: %v", module.Currency, targetCurrency, module.Name, err)
				} else {
					unitPrice = math.Round(converted*100) / 100 // round(eur_price × rate, 2)
				}
			}

			// Потом: Кол-во × Цена_KZT = Сумма (как в 1С)
			totalPrice = math.Round(quantity*unitPrice*100) / 100
		}

		line := models.InvoiceLine{
			ModuleID:    module.ID,
			ModuleName:  module.Name,
			ModuleCode:  module.Code,
			ModuleUnit:  module.Unit,
			Quantity:    quantity,
			UnitPrice:   unitPrice,
			TotalPrice:  totalPrice,
			Currency:    targetCurrency,
			PricingType: module.PricingType,
		}
		lines = append(lines, line)
		totalAmount += totalPrice
	}

	if totalAmount == 0 {
		log.Printf("Нулевой счёт для %s, пропускаем", account.Name)
		return nil, nil
	}

	// Считаем порядковый номер счёта для аккаунта (до удаления старого)
	seqNum, _ := s.repo.CountInvoicesByAccount(account.ID)
	// +1 т.к. текущий ещё не создан, но старый уже удалён выше
	seqNum++

	// Создаём счёт
	invoice := &models.Invoice{
		AccountID:   account.ID,
		Period:      period,
		TotalAmount: totalAmount,
		Currency:    targetCurrency,
		Status:      "draft",
	}

	// Формируем номер: {номер_договора}/{порядковый_номер}
	if account.ContractNumber != "" {
		invoice.Number = fmt.Sprintf("%s/%d", account.ContractNumber, seqNum)
	} else {
		invoice.Number = fmt.Sprintf("%d", seqNum)
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
	log.Printf("Создан счёт %s для %s: %.2f %s", invoice.Number, account.Name, totalAmount, targetCurrency)

	return invoice, nil
}

// convertCurrency конвертирует сумму из одной валюты в другую через KZT
func (s *Service) convertCurrency(amount float64, from, to string, date time.Time) (float64, error) {
	if from == to {
		return amount, nil
	}

	// Получаем сумму в KZT
	var amountInKZT float64

	if from == "KZT" {
		amountInKZT = amount
	} else {
		// Получаем курс from → KZT
		rate, err := s.repo.GetExchangeRateByDate(from, date)
		if err != nil {
			return 0, fmt.Errorf("курс %s за %s не найден: %w", from, date.Format("02.01.2006"), err)
		}
		amountInKZT = amount * rate.Rate
	}

	// Конвертируем KZT → to
	if to == "KZT" {
		return amountInKZT, nil
	}

	rateToTarget, err := s.repo.GetExchangeRateByDate(to, date)
	if err != nil {
		return 0, fmt.Errorf("курс %s за %s не найден: %w", to, date.Format("02.01.2006"), err)
	}

	return amountInKZT / rateToTarget.Rate, nil
}

// calculateAverageUnits рассчитывает среднее количество АКТИВНЫХ объектов за месяц
func (s *Service) calculateAverageUnits(accountID uint, year, month int) (float64, error) {
	snapshots, err := s.repo.GetSnapshotsByAccountAndPeriod(accountID, year, month)
	if err != nil {
		return 0, err
	}

	if len(snapshots) == 0 {
		return 0, nil
	}

	// Считаем сумму АКТИВНЫХ объектов по всем дням (без деактивированных)
	var totalActiveUnits int
	for _, s := range snapshots {
		activeUnits := s.TotalUnits - s.UnitsDeactivated
		if activeUnits < 0 {
			activeUnits = 0
		}
		totalActiveUnits += activeUnits
	}

	// Количество дней в месяце
	daysInMonth := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()

	// Среднее = сумма активных / дней в месяце
	return float64(totalActiveUnits) / float64(daysInMonth), nil
}

// RecalculateCurrentPeriod пересчитывает счёт за текущий период
func (s *Service) RecalculateCurrentPeriod(accountID uint) (*models.Invoice, error) {
	now := time.Now()
	period := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)

	// Получаем аккаунт
	var account models.Account
	if err := s.db.Preload("Modules.Module").First(&account, accountID).Error; err != nil {
		return nil, err
	}

	rateDate := period.AddDate(0, 1, 0)
	return s.generateInvoiceForAccount(account, period, rateDate)
}
