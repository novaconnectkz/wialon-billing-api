package repository

import (
	"fmt"
	"time"

	"github.com/user/wialon-billing-api/internal/config"
	"github.com/user/wialon-billing-api/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Repository - интерфейс для работы с БД
type Repository struct {
	db *gorm.DB
}

// NewPostgresDB создаёт подключение к PostgreSQL
func NewPostgresDB(cfg config.DatabaseConfig) (*gorm.DB, error) {
	dsn := fmt.Sprintf(
		"host=%s user=%s password=%s dbname=%s port=%s sslmode=%s",
		cfg.Host, cfg.User, cfg.Password, cfg.DBName, cfg.Port, cfg.SSLMode,
	)

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}

	// Автомиграция моделей
	if err := db.AutoMigrate(
		&models.User{},
		&models.OTPCode{},
		&models.WialonConnection{},
		&models.BillingSettings{},
		&models.Module{},
		&models.Account{},
		&models.AccountModule{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.ExchangeRate{},
		&models.Snapshot{},
		&models.SnapshotUnit{},
		&models.Change{},
	); err != nil {
		return nil, err
	}

	return db, nil
}

// NewRepository создаёт новый репозиторий
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// === Accounts ===

// GetAllAccounts возвращает все учётные записи с модулями
func (r *Repository) GetAllAccounts() ([]models.Account, error) {
	var accounts []models.Account
	if err := r.db.Preload("Modules.Module").Find(&accounts).Error; err != nil {
		return nil, err
	}
	return accounts, nil
}

// GetSelectedAccounts возвращает учётные записи, участвующие в биллинге
func (r *Repository) GetSelectedAccounts() ([]models.Account, error) {
	var accounts []models.Account
	if err := r.db.Where("is_billing_enabled = ?", true).Preload("Modules.Module").Find(&accounts).Error; err != nil {
		return nil, err
	}
	return accounts, nil
}

// GetAccountByID возвращает учётную запись по ID
func (r *Repository) GetAccountByID(id uint) (*models.Account, error) {
	var account models.Account
	if err := r.db.Where("id = ?", id).First(&account).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

// ToggleAccountBilling переключает участие в биллинге
func (r *Repository) ToggleAccountBilling(id uint) error {
	return r.db.Model(&models.Account{}).Where("id = ?", id).
		Update("is_billing_enabled", gorm.Expr("NOT is_billing_enabled")).Error
}

// UpsertAccount создаёт или обновляет учётную запись
func (r *Repository) UpsertAccount(account *models.Account) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "wialon_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"name", "is_dealer", "is_active", "is_blocked", "parent_id"}),
	}).Create(account).Error
}

// DeleteAllAccounts удаляет все учётные записи (для полной пересинхронизации)
func (r *Repository) DeleteAllAccounts() error {
	return r.db.Exec("DELETE FROM accounts").Error
}

// DeactivateMissingAccounts помечает аккаунты как неактивные, если их WialonID нет в списке activeIDs
func (r *Repository) DeactivateMissingAccounts(activeIDs []int64) error {
	if len(activeIDs) == 0 {
		// Если список пуст, деактивируем все
		return r.db.Model(&models.Account{}).Where("1 = 1").Update("is_active", false).Error
	}
	return r.db.Model(&models.Account{}).
		Where("wialon_id NOT IN ?", activeIDs).
		Update("is_active", false).Error
}

// GetAccountByDealer возвращает только аккаунт самого дилера (без клиентов)
func (r *Repository) GetAccountByDealer(dealerWialonID int64) (*models.Account, error) {
	var account models.Account
	// Выбираем ТОЛЬКО аккаунт самого дилера
	if err := r.db.Where(
		"wialon_id = ? AND is_billing_enabled = ?",
		dealerWialonID, true,
	).Preload("Modules.Module").First(&account).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

// GetSnapshotsByDealer возвращает снимки только для аккаунта дилера
func (r *Repository) GetSnapshotsByDealer(dealerWialonID int64, year, month int) ([]models.Snapshot, error) {
	var snapshots []models.Snapshot

	startOfMonth := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	endOfMonth := startOfMonth.AddDate(0, 1, 0)

	// Получаем снимки только для аккаунта дилера
	if err := r.db.Joins("JOIN accounts ON accounts.id = snapshots.account_id").
		Where("accounts.wialon_id = ?", dealerWialonID).
		Where("snapshots.created_at >= ? AND snapshots.created_at < ?", startOfMonth, endOfMonth).
		Order("snapshots.created_at DESC").
		Preload("Account").
		Find(&snapshots).Error; err != nil {
		return nil, err
	}
	return snapshots, nil
}

// GetSnapshotsByDealerAll возвращает все снимки для аккаунта дилера (без фильтрации по месяцу)
func (r *Repository) GetSnapshotsByDealerAll(dealerWialonID int64, limit int) ([]models.Snapshot, error) {
	var snapshots []models.Snapshot

	if err := r.db.Joins("JOIN accounts ON accounts.id = snapshots.account_id").
		Where("accounts.wialon_id = ?", dealerWialonID).
		Order("snapshots.created_at DESC").
		Limit(limit).
		Preload("Account").
		Find(&snapshots).Error; err != nil {
		return nil, err
	}
	return snapshots, nil
}

// UpdateAccount обновляет учётную запись
func (r *Repository) UpdateAccount(account *models.Account) error {
	return r.db.Save(account).Error
}

// === Modules ===

// GetAllModules возвращает все модули
func (r *Repository) GetAllModules() ([]models.Module, error) {
	var modules []models.Module
	if err := r.db.Find(&modules).Error; err != nil {
		return nil, err
	}
	return modules, nil
}

// CreateModule создаёт новый модуль
func (r *Repository) CreateModule(module *models.Module) error {
	return r.db.Create(module).Error
}

// UpdateModule обновляет модуль
func (r *Repository) UpdateModule(module *models.Module) error {
	return r.db.Save(module).Error
}

// DeleteModule удаляет модуль
func (r *Repository) DeleteModule(id uint) error {
	return r.db.Delete(&models.Module{}, id).Error
}

// AssignModuleToAccount привязывает модуль к учётной записи
func (r *Repository) AssignModuleToAccount(accountID, moduleID uint) error {
	am := models.AccountModule{
		AccountID: accountID,
		ModuleID:  moduleID,
	}
	return r.db.Create(&am).Error
}

// === Settings ===

// GetSettings возвращает настройки биллинга
func (r *Repository) GetSettings() (*models.BillingSettings, error) {
	var settings models.BillingSettings
	if err := r.db.First(&settings).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &settings, nil
}

// SaveSettings сохраняет настройки биллинга
func (r *Repository) SaveSettings(settings *models.BillingSettings) error {
	return r.db.Save(settings).Error
}

// === Exchange Rates ===

// GetExchangeRates возвращает историю курсов
func (r *Repository) GetExchangeRates(limit int) ([]models.ExchangeRate, error) {
	var rates []models.ExchangeRate
	if err := r.db.Order("rate_date DESC").Limit(limit).Find(&rates).Error; err != nil {
		return nil, err
	}
	return rates, nil
}

// SaveExchangeRate сохраняет курс валют
func (r *Repository) SaveExchangeRate(rate *models.ExchangeRate) error {
	return r.db.Create(rate).Error
}

// === Snapshots ===

// GetSnapshots возвращает снимки
func (r *Repository) GetSnapshots(limit int) ([]models.Snapshot, error) {
	var snapshots []models.Snapshot
	if err := r.db.Order("created_at DESC").Limit(limit).Preload("Account").Find(&snapshots).Error; err != nil {
		return nil, err
	}
	return snapshots, nil
}

// GetSnapshotsByPeriod возвращает снимки за указанный месяц и год
func (r *Repository) GetSnapshotsByPeriod(year, month int) ([]models.Snapshot, error) {
	var snapshots []models.Snapshot

	// Вычисляем начало и конец месяца
	startOfMonth := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	endOfMonth := startOfMonth.AddDate(0, 1, 0)

	if err := r.db.Where("created_at >= ? AND created_at < ?", startOfMonth, endOfMonth).
		Order("created_at DESC").Preload("Account").Find(&snapshots).Error; err != nil {
		return nil, err
	}
	return snapshots, nil
}

// CreateSnapshot создаёт снимок
func (r *Repository) CreateSnapshot(snapshot *models.Snapshot) error {
	return r.db.Create(snapshot).Error
}

// CreateSnapshotUnit создаёт запись объекта в снимке
func (r *Repository) CreateSnapshotUnit(unit *models.SnapshotUnit) error {
	return r.db.Create(unit).Error
}

// GetLastSnapshot возвращает последний снимок для аккаунта
func (r *Repository) GetLastSnapshot(accountID uint) (*models.Snapshot, error) {
	var snapshot models.Snapshot
	if err := r.db.Where("account_id = ?", accountID).Order("created_at DESC").
		Preload("Units").First(&snapshot).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &snapshot, nil
}

// ClearAllSnapshots удаляет все снимки и связанные данные
func (r *Repository) ClearAllSnapshots() (int64, error) {
	// Сначала удаляем SnapshotUnits
	r.db.Exec("DELETE FROM snapshot_units")

	// Удаляем Changes
	r.db.Exec("DELETE FROM changes")

	// Удаляем Snapshots
	result := r.db.Exec("DELETE FROM snapshots")
	return result.RowsAffected, result.Error
}

// === Changes ===

// GetChanges возвращает изменения
func (r *Repository) GetChanges(limit int) ([]models.Change, error) {
	var changes []models.Change
	if err := r.db.Order("detected_at DESC").Limit(limit).Find(&changes).Error; err != nil {
		return nil, err
	}
	return changes, nil
}

// CreateChange создаёт запись об изменении
func (r *Repository) CreateChange(change *models.Change) error {
	return r.db.Create(change).Error
}

// === Invoices ===

// GetInvoices возвращает список счетов
func (r *Repository) GetInvoices(limit int) ([]models.Invoice, error) {
	var invoices []models.Invoice
	if err := r.db.Preload("Account").Preload("Lines").
		Order("created_at DESC").Limit(limit).Find(&invoices).Error; err != nil {
		return nil, err
	}
	return invoices, nil
}

// GetInvoiceByID возвращает счёт по ID
func (r *Repository) GetInvoiceByID(id uint) (*models.Invoice, error) {
	var invoice models.Invoice
	if err := r.db.Preload("Account").Preload("Lines").First(&invoice, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &invoice, nil
}

// GetInvoiceByAccountAndPeriod возвращает счёт по аккаунту и периоду
func (r *Repository) GetInvoiceByAccountAndPeriod(accountID uint, period time.Time) (*models.Invoice, error) {
	var invoice models.Invoice
	if err := r.db.Where("account_id = ? AND period = ?", accountID, period).First(&invoice).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &invoice, nil
}

// CreateInvoice создаёт счёт
func (r *Repository) CreateInvoice(invoice *models.Invoice) error {
	return r.db.Create(invoice).Error
}

// UpdateInvoice обновляет счёт
func (r *Repository) UpdateInvoice(invoice *models.Invoice) error {
	return r.db.Save(invoice).Error
}

// DeleteInvoice удаляет счёт
func (r *Repository) DeleteInvoice(invoiceID uint) error {
	return r.db.Delete(&models.Invoice{}, invoiceID).Error
}

// CreateInvoiceLine создаёт строку счёта
func (r *Repository) CreateInvoiceLine(line *models.InvoiceLine) error {
	return r.db.Create(line).Error
}

// DeleteInvoiceLines удаляет строки счёта
func (r *Repository) DeleteInvoiceLines(invoiceID uint) error {
	return r.db.Where("invoice_id = ?", invoiceID).Delete(&models.InvoiceLine{}).Error
}

// GetSnapshotsByAccountAndPeriod возвращает снимки аккаунта за месяц
func (r *Repository) GetSnapshotsByAccountAndPeriod(accountID uint, year, month int) ([]models.Snapshot, error) {
	startOfMonth := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
	endOfMonth := startOfMonth.AddDate(0, 1, 0)

	var snapshots []models.Snapshot
	if err := r.db.Where("account_id = ? AND created_at >= ? AND created_at < ?",
		accountID, startOfMonth, endOfMonth).Find(&snapshots).Error; err != nil {
		return nil, err
	}
	return snapshots, nil
}

// GetAccountModules возвращает модули аккаунта
func (r *Repository) GetAccountModules(accountID uint) ([]models.AccountModule, error) {
	var modules []models.AccountModule
	if err := r.db.Preload("Module").Where("account_id = ?", accountID).Find(&modules).Error; err != nil {
		return nil, err
	}
	return modules, nil
}

// === Массовая привязка модулей ===

// AssignModuleBulk привязывает модуль к нескольким аккаунтам
func (r *Repository) AssignModuleBulk(moduleID uint, accountIDs []uint) (int, error) {
	var created int
	for _, accountID := range accountIDs {
		// Проверяем, не привязан ли уже
		var existing models.AccountModule
		err := r.db.Where("account_id = ? AND module_id = ?", accountID, moduleID).First(&existing).Error
		if err == nil {
			continue // уже привязан
		}

		am := models.AccountModule{
			AccountID: accountID,
			ModuleID:  moduleID,
		}
		if err := r.db.Create(&am).Error; err == nil {
			created++
		}
	}
	return created, nil
}

// UnassignModuleBulk отвязывает модуль от нескольких аккаунтов
func (r *Repository) UnassignModuleBulk(moduleID uint, accountIDs []uint) (int, error) {
	result := r.db.Where("module_id = ? AND account_id IN ?", moduleID, accountIDs).Delete(&models.AccountModule{})
	return int(result.RowsAffected), result.Error
}

// SetCurrencyBulk устанавливает валюту для нескольких аккаунтов
func (r *Repository) SetCurrencyBulk(accountIDs []uint, currency string) (int, error) {
	result := r.db.Model(&models.Account{}).Where("id IN ?", accountIDs).Update("billing_currency", currency)
	return int(result.RowsAffected), result.Error
}
