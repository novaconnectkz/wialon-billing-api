package models

import (
	"time"
)

// BillingSettings - настройки биллинга и реквизиты поставщика
type BillingSettings struct {
	ID         uint    `gorm:"primaryKey" json:"id"`
	WialonType string  `gorm:"size:20;not null" json:"wialon_type"` // "hosting" или "local"
	UnitPrice  float64 `gorm:"not null" json:"unit_price"`          // стоимость за объект
	Currency   string  `gorm:"size:3;not null" json:"currency"`     // "EUR" или "RUB"

	// Реквизиты компании-поставщика
	CompanyName    string `gorm:"size:255" json:"company_name"`     // Название
	CompanyBIN     string `gorm:"size:20" json:"company_bin"`       // БИН/ИИН
	CompanyAddress string `gorm:"type:text" json:"company_address"` // Адрес
	CompanyPhone   string `gorm:"size:50" json:"company_phone"`     // Телефон

	// Банковские реквизиты
	BankName    string `gorm:"size:255" json:"bank_name"`   // Название банка
	BankIIK     string `gorm:"size:50" json:"bank_iik"`     // ИИК (расчётный счёт)
	BankBIK     string `gorm:"size:20" json:"bank_bik"`     // БИК
	BankKbe     string `gorm:"size:10" json:"bank_kbe"`     // Кбе
	PaymentCode string `gorm:"size:10" json:"payment_code"` // Код назначения платежа

	// Исполнитель и НДС
	ExecutorName string  `gorm:"size:255" json:"executor_name"` // ФИО исполнителя
	VATRate      float64 `gorm:"default:16" json:"vat_rate"`    // Ставка НДС (%)

	UpdatedAt time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}

// Module - модуль (услуга)
type Module struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	Name            string    `gorm:"size:255;not null" json:"name"`
	Description     string    `gorm:"type:text" json:"description"`
	Price           float64   `gorm:"not null" json:"price"`                          // цена за единицу (или фикса)
	ActivationPrice *float64  `json:"activation_price"`                               // цена подключения
	Currency        string    `gorm:"size:3;not null" json:"currency"`                // "EUR", "RUB", "KZT"
	PricingType     string    `gorm:"size:20;default:'per_unit'" json:"pricing_type"` // "per_unit" или "fixed"
	BillingType     string    `gorm:"size:20;not null" json:"billing_type"`           // "monthly" или "one_time"
	CreatedAt       time.Time `gorm:"autoCreateTime" json:"created_at"`
}

// Account - учётная запись Wialon
type Account struct {
	ID               uint    `gorm:"primaryKey" json:"id"`
	WialonID         int64   `gorm:"uniqueIndex;not null" json:"wialon_id"`
	Name             string  `gorm:"size:255" json:"name"`
	IsDealer         bool    `gorm:"default:false" json:"is_dealer"`
	ParentID         *int64  `json:"parent_id"`
	IsBillingEnabled bool    `gorm:"default:false" json:"is_billing_enabled"`
	IsActive         bool    `gorm:"default:true" json:"is_active"`
	IsBlocked        bool    `gorm:"default:false" json:"is_blocked"`
	BillingCurrency  string  `gorm:"size:3;default:'KZT'" json:"billing_currency"`
	ConnectionID     *uint   `json:"connection_id"`
	ContactEmail     *string `gorm:"size:255" json:"contact_email"` // Email дилера

	// Реквизиты покупателя
	BuyerName      string     `gorm:"size:255" json:"buyer_name"`     // Название компании
	BuyerBIN       string     `gorm:"size:20" json:"buyer_bin"`       // БИН/ИИН
	BuyerAddress   string     `gorm:"type:text" json:"buyer_address"` // Адрес
	BuyerEmail     string     `gorm:"size:255" json:"buyer_email"`    // Email (логин + рассылка)
	BuyerPhone     string     `gorm:"size:50" json:"buyer_phone"`     // Телефон
	ContractNumber string     `gorm:"size:50" json:"contract_number"` // Номер договора
	ContractDate   *time.Time `json:"contract_date"`                  // Дата договора

	CreatedAt time.Time       `gorm:"autoCreateTime" json:"created_at"`
	Modules   []AccountModule `gorm:"foreignKey:AccountID" json:"modules,omitempty"`
}

// AccountModule - привязка модуля к учётной записи
type AccountModule struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	AccountID   uint      `gorm:"not null" json:"account_id"`
	ModuleID    uint      `gorm:"not null" json:"module_id"`
	ActivatedAt time.Time `gorm:"autoCreateTime" json:"activated_at"`
	Account     Account   `gorm:"foreignKey:AccountID" json:"-"`
	Module      Module    `gorm:"foreignKey:ModuleID" json:"module,omitempty"`
}

// Invoice - счёт на оплату
type Invoice struct {
	ID          uint          `gorm:"primaryKey" json:"id"`
	AccountID   uint          `gorm:"not null" json:"account_id"`
	Period      time.Time     `gorm:"type:date;not null" json:"period"`      // 1-е число месяца (за какой период)
	TotalAmount float64       `gorm:"not null" json:"total_amount"`          // итоговая сумма
	Currency    string        `gorm:"size:3;not null" json:"currency"`       // валюта
	Status      string        `gorm:"size:20;default:'draft'" json:"status"` // "draft", "sent", "paid", "overdue"
	CreatedAt   time.Time     `gorm:"autoCreateTime" json:"created_at"`
	SentAt      *time.Time    `json:"sent_at,omitempty"` // когда отправлен
	PaidAt      *time.Time    `json:"paid_at,omitempty"` // когда оплачен
	Account     Account       `gorm:"foreignKey:AccountID" json:"account,omitempty"`
	Lines       []InvoiceLine `gorm:"foreignKey:InvoiceID" json:"lines,omitempty"`
}

// InvoiceLine - строка счёта (детализация)
type InvoiceLine struct {
	ID          uint    `gorm:"primaryKey" json:"id"`
	InvoiceID   uint    `gorm:"not null" json:"invoice_id"`
	ModuleID    uint    `gorm:"not null" json:"module_id"`
	ModuleName  string  `gorm:"size:255;not null" json:"module_name"` // название на момент создания
	Quantity    float64 `gorm:"not null" json:"quantity"`             // кол-во (среднее объектов или 1)
	UnitPrice   float64 `gorm:"not null" json:"unit_price"`           // цена за единицу на момент создания
	TotalPrice  float64 `gorm:"not null" json:"total_price"`          // итого по строке
	Currency    string  `gorm:"size:3;not null" json:"currency"`
	PricingType string  `gorm:"size:20;not null" json:"pricing_type"` // "per_unit" или "fixed"
}

// ExchangeRate - курс валюты НБК
type ExchangeRate struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	CurrencyFrom string    `gorm:"size:3;not null" json:"currency_from"` // EUR, RUB
	CurrencyTo   string    `gorm:"size:3;default:'KZT'" json:"currency_to"`
	Rate         float64   `gorm:"not null" json:"rate"`
	RateDate     time.Time `gorm:"type:date;not null" json:"rate_date"`
	CreatedAt    time.Time `gorm:"autoCreateTime" json:"created_at"`
}

// Snapshot - снимок состояния
type Snapshot struct {
	ID               uint           `gorm:"primaryKey" json:"id"`
	AccountID        uint           `gorm:"not null;uniqueIndex:idx_snapshot_unique" json:"account_id"`
	SnapshotDate     time.Time      `gorm:"type:date;not null;uniqueIndex:idx_snapshot_unique" json:"snapshot_date"` // дата, за которую снимок
	TotalUnits       int            `gorm:"not null" json:"total_units"`
	UnitsCreated     int            `gorm:"default:0" json:"units_created"`     // добавлено объектов
	UnitsDeleted     int            `gorm:"default:0" json:"units_deleted"`     // удалено объектов
	UnitsDeactivated int            `gorm:"default:0" json:"units_deactivated"` // деактивировано объектов
	CreatedAt        time.Time      `gorm:"autoCreateTime" json:"created_at"`
	Account          Account        `gorm:"foreignKey:AccountID" json:"account,omitempty"`
	Units            []SnapshotUnit `gorm:"foreignKey:SnapshotID" json:"units,omitempty"`
}

// SnapshotUnit - объект в снимке
type SnapshotUnit struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	SnapshotID    uint       `gorm:"not null" json:"snapshot_id"`
	WialonUnitID  int64      `gorm:"not null" json:"wialon_unit_id"`
	UnitName      string     `gorm:"size:255" json:"unit_name"`
	AccountID     int64      `json:"account_id"`
	CreatorID     int64      `json:"creator_id"`
	IsActive      bool       `gorm:"default:true" json:"is_active"` // Статус активности объекта
	DeactivatedAt *time.Time `json:"deactivated_at,omitempty"`      // Время деактивации
}

// Change - изменение между снимками
type Change struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	PrevSnapshotID *uint     `json:"prev_snapshot_id"`
	CurrSnapshotID uint      `gorm:"not null" json:"curr_snapshot_id"`
	WialonUnitID   int64     `gorm:"not null" json:"wialon_unit_id"`
	UnitName       string    `gorm:"size:255" json:"unit_name"`
	ChangeType     string    `gorm:"size:10;not null" json:"change_type"` // "added" или "removed"
	DetectedAt     time.Time `gorm:"autoCreateTime" json:"detected_at"`
}

// DailyCharge - ежедневное начисление по модулю для аккаунта
type DailyCharge struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	AccountID   uint      `gorm:"not null;uniqueIndex:idx_daily_charge_unique" json:"account_id"`
	SnapshotID  uint      `gorm:"not null" json:"snapshot_id"`
	ModuleID    uint      `gorm:"not null;uniqueIndex:idx_daily_charge_unique" json:"module_id"`
	ChargeDate  time.Time `gorm:"type:date;not null;uniqueIndex:idx_daily_charge_unique;index:idx_daily_charge_period" json:"charge_date"`
	TotalUnits  int       `gorm:"not null" json:"total_units"`          // объектов на дату
	ModuleName  string    `gorm:"size:255;not null" json:"module_name"` // зафиксированное название
	PricingType string    `gorm:"size:20;not null" json:"pricing_type"` // per_unit или fixed
	UnitPrice   float64   `gorm:"not null" json:"unit_price"`           // цена модуля
	DaysInMonth int       `gorm:"not null" json:"days_in_month"`        // дней в месяце
	DailyCost   float64   `gorm:"not null" json:"daily_cost"`           // стоимость за день
	Currency    string    `gorm:"size:3;not null" json:"currency"`      // EUR, RUB, KZT
	CreatedAt   time.Time `gorm:"autoCreateTime" json:"created_at"`
	Account     Account   `gorm:"foreignKey:AccountID" json:"account,omitempty"`
	Module      Module    `gorm:"foreignKey:ModuleID" json:"module,omitempty"`
}

// === AI Analytics ===

// AISettings - настройки DeepSeek AI (редактируется через UI)
type AISettings struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	Enabled          bool      `gorm:"default:false" json:"enabled"`
	APIKey           string    `gorm:"size:255" json:"api_key,omitempty"`                         // шифруется при хранении
	AnalysisModel    string    `gorm:"size:50;default:'deepseek-reasoner'" json:"analysis_model"` // модель для сложных задач (R1)
	SupportModel     string    `gorm:"size:50;default:'deepseek-chat'" json:"support_model"`      // модель для быстрых ответов (V3)
	MaxTokens        int       `gorm:"default:2500" json:"max_tokens"`                            // лимит токенов
	RateLimitPerHour int       `gorm:"default:1" json:"rate_limit_per_hour"`                      // лимит запросов в час
	CacheTTLHours    int       `gorm:"default:24" json:"cache_ttl_hours"`                         // время жизни кэша инсайтов
	PrivacyMode      bool      `gorm:"default:false" json:"privacy_mode"`                         // заменять названия на ID
	UpdatedAt        time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}

// AIUsageLog - лог использования AI (для контроля токенов)
type AIUsageLog struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	RequestType  string    `gorm:"size:50" json:"request_type"`    // "analyze", "insight"
	InputTokens  int       `gorm:"default:0" json:"input_tokens"`  // входные токены
	OutputTokens int       `gorm:"default:0" json:"output_tokens"` // выходные токены
	TotalTokens  int       `gorm:"default:0" json:"total_tokens"`  // всего токенов
	Success      bool      `gorm:"default:true" json:"success"`    // успешный запрос
	ErrorMessage string    `gorm:"type:text" json:"error_message,omitempty"`
	CreatedAt    time.Time `gorm:"autoCreateTime" json:"created_at"`
}

// AIInsight - результат AI-анализа
type AIInsight struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	AccountID       uint      `gorm:"not null;index" json:"account_id"`
	InsightType     string    `gorm:"size:50;not null" json:"insight_type"` // "churn_risk", "growth", "financial_impact"
	Severity        string    `gorm:"size:20;not null" json:"severity"`     // "info", "warning", "critical"
	Title           string    `gorm:"size:255;not null" json:"title"`       // заголовок инсайта
	Description     string    `gorm:"type:text" json:"description"`         // подробное описание
	FinancialImpact *float64  `json:"financial_impact,omitempty"`           // финансовое влияние
	Currency        string    `gorm:"size:3" json:"currency,omitempty"`     // валюта влияния
	Metadata        string    `gorm:"type:jsonb" json:"metadata,omitempty"` // дополнительные данные (JSON)
	IsHelpful       *bool     `json:"is_helpful,omitempty"`                 // обратная связь: полезно?
	FeedbackComment string    `gorm:"type:text" json:"feedback_comment,omitempty"`
	CreatedAt       time.Time `gorm:"autoCreateTime" json:"created_at"`
	ExpiresAt       time.Time `gorm:"not null;index" json:"expires_at"` // автоочистка
	Account         Account   `gorm:"foreignKey:AccountID" json:"account,omitempty"`
}
