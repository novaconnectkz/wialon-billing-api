package nbk

import (
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/user/wialon-billing-api/internal/models"
	"github.com/user/wialon-billing-api/internal/repository"
)

const (
	// NBK API URL (открытые данные Казахстана)
	nbkAPIURL = "https://nationalbank.kz/rss/get_rates.cfm?fdate=%s"
)

// Service - сервис для работы с курсами валют НБК
type Service struct {
	repo   *repository.Repository
	client *http.Client
}

// NBKRate - курс валюты из API НБК
type NBKRate struct {
	Title       string  `json:"title"`
	Code        string  `json:"code"`
	Description float64 `json:"description"`
}

// NewService создаёт новый сервис НБК
func NewService(repo *repository.Repository) *Service {
	return &Service{
		repo:   repo,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// FetchExchangeRates получает курсы валют из НБК
func (s *Service) FetchExchangeRates() error {
	return s.FetchExchangeRatesForDate(time.Now())
}

// XMLRates - корневой элемент XML ответа НБК
type XMLRates struct {
	XMLName xml.Name  `xml:"rates"`
	Items   []XMLItem `xml:"item"`
}

// XMLItem - элемент валюты в XML
type XMLItem struct {
	Title       string `xml:"title"`       // код валюты (EUR, RUB)
	Description string `xml:"description"` // курс как строка
	Quant       int    `xml:"quant"`       // количество единиц (1 или 100)
}

// FetchExchangeRatesForDate получает курсы валют из НБК за конкретную дату
func (s *Service) FetchExchangeRatesForDate(date time.Time) error {
	dateStr := date.Format("02.01.2006")
	url := fmt.Sprintf(nbkAPIURL, dateStr)

	resp, err := s.client.Get(url)
	if err != nil {
		return fmt.Errorf("ошибка запроса к НБК: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("ошибка чтения ответа: %w", err)
	}

	// Парсим XML
	var xmlRates XMLRates
	if err := xml.Unmarshal(body, &xmlRates); err != nil {
		log.Printf("Ошибка парсинга XML за %s: %v", dateStr, err)
		return nil
	}

	// Сохраняем нужные курсы (EUR, RUB)
	saved := 0
	for _, item := range xmlRates.Items {
		if item.Title == "EUR" || item.Title == "RUB" {
			// Парсим курс из строки
			rate, err := strconv.ParseFloat(item.Description, 64)
			if err != nil {
				continue
			}

			// Если quant > 1 (например, 100 RUB), делим курс
			if item.Quant > 1 {
				rate = rate / float64(item.Quant)
			}

			exchangeRate := &models.ExchangeRate{
				CurrencyFrom: item.Title,
				CurrencyTo:   "KZT",
				Rate:         rate,
				RateDate:     date,
			}

			if err := s.repo.SaveExchangeRate(exchangeRate); err != nil {
				log.Printf("Ошибка сохранения курса %s: %v", item.Title, err)
				continue
			}
			saved++
		}
	}

	if saved > 0 {
		log.Printf("Сохранено %d курсов за %s", saved, dateStr)
	}

	return nil
}

// fetchFromAlternativeAPI - альтернативный API (резерв)
func (s *Service) fetchFromAlternativeAPI() error {
	// Используем альтернативный API nationalbank.kz
	url := "https://nationalbank.kz/rss/rates_all.xml"

	log.Printf("Запрос альтернативного API: %s", url)

	resp, err := s.client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Парсинг XML (упрощённый вариант, можно доработать)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	log.Printf("Получен ответ XML: %d байт", len(body))

	// Для MVP - сохраняем фиксированные значения с пометкой
	// В продакшене нужен XML-парсер
	now := time.Now()

	// EUR/KZT (примерное значение, в продакшене - из XML)
	eurRate := &models.ExchangeRate{
		CurrencyFrom: "EUR",
		CurrencyTo:   "KZT",
		Rate:         530.0, // placeholder, обновить при парсинге XML
		RateDate:     now,
	}
	s.repo.SaveExchangeRate(eurRate)

	// RUB/KZT
	rubRate := &models.ExchangeRate{
		CurrencyFrom: "RUB",
		CurrencyTo:   "KZT",
		Rate:         5.5, // placeholder
		RateDate:     now,
	}
	s.repo.SaveExchangeRate(rubRate)

	log.Println("Курсы валют сохранены (placeholder values)")
	return nil
}

// GetLatestRates возвращает последние курсы валют
func (s *Service) GetLatestRates() (map[string]float64, error) {
	rates, err := s.repo.GetExchangeRates(10)
	if err != nil {
		return nil, err
	}

	result := make(map[string]float64)
	for _, r := range rates {
		key := fmt.Sprintf("%s_%s", r.CurrencyFrom, r.CurrencyTo)
		if _, exists := result[key]; !exists {
			result[key] = r.Rate
		}
	}

	return result, nil
}
