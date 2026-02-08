package wialon

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/user/wialon-billing-api/internal/config"
)

// Client - клиент для Wialon API
type Client struct {
	baseURL  string
	token    string
	sid      string // Session ID
	userID   int64  // ID авторизованного пользователя
	userName string // Имя авторизованного пользователя
	client   *http.Client
}

// WialonUser - информация о пользователе Wialon
type WialonUser struct {
	ID   int64  `json:"id"`
	Name string `json:"nm"`
}

// LoginResponse - ответ на авторизацию
type LoginResponse struct {
	EID   string      `json:"eid"`
	User  *WialonUser `json:"user,omitempty"`
	Error *int        `json:"error,omitempty"`
}

// SearchItemsResponse - ответ на поиск элементов
type SearchItemsResponse struct {
	TotalItemsCount int          `json:"totalItemsCount"`
	Items           []WialonItem `json:"items"`
	Error           *int         `json:"error,omitempty"`
}

// WialonItem - элемент Wialon (объект, аккаунт и т.д.)
type WialonItem struct {
	ID              int64  `json:"id"`
	Name            string `json:"nm"`
	CreatorID       int64  `json:"crt"`
	AccountID       int64  `json:"bact"`
	Active          int    `json:"act"`   // 1 = активен, 0 = деактивирован
	DeactivatedTime int64  `json:"dactt"` // Unix timestamp деактивации
}

// AccountDataResponse - ответ на получение данных аккаунта
type AccountDataResponse struct {
	DealerRights    int                    `json:"dealerRights"`
	ParentAccountId int64                  `json:"parentAccountId"` // ID родительского аккаунта
	Settings        map[string]interface{} `json:"settings"`
	Enabled         *int                   `json:"enabled"` // 0 - заблокирован, 1 - активен
	Error           *int                   `json:"error,omitempty"`
}

// GetUnitUsage извлекает avl_unit.usage из settings.combined.services.avl_unit.usage
// Возвращает количество объектов ТОЛЬКО данного аккаунта (без дочерних)
func (r *AccountDataResponse) GetUnitUsage() int {
	if r == nil || r.Settings == nil {
		return 0
	}
	combined, ok := r.Settings["combined"]
	if !ok {
		return 0
	}
	combinedMap, ok := combined.(map[string]interface{})
	if !ok {
		return 0
	}
	services, ok := combinedMap["services"]
	if !ok {
		return 0
	}
	servicesMap, ok := services.(map[string]interface{})
	if !ok {
		return 0
	}
	avlUnit, ok := servicesMap["avl_unit"]
	if !ok {
		return 0
	}
	avlUnitMap, ok := avlUnit.(map[string]interface{})
	if !ok {
		return 0
	}
	usage, ok := avlUnitMap["usage"]
	if !ok {
		return 0
	}
	switch v := usage.(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// NewClient создаёт новый клиент Wialon API
func NewClient(cfg config.WialonConfig) *Client {
	return &Client{
		baseURL: cfg.BaseURL,
		token:   cfg.Token,
		client:  &http.Client{},
	}
}

// NewClientWithToken создаёт клиент с указанным токеном (для OAuth)
func NewClientWithToken(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		client:  &http.Client{},
	}
}

// Login выполняет авторизацию через токен
func (c *Client) Login() error {
	// Формируем JSON params
	params := map[string]string{"token": c.token}
	paramsJSON, _ := json.Marshal(params)

	// Формируем URL с params в query string
	reqURL := fmt.Sprintf("%s/wialon/ajax.html?svc=token/login&params=%s",
		c.baseURL, url.QueryEscape(string(paramsJSON)))

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var result LoginResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("ошибка парсинга ответа: %v, raw: %s", err, string(body)[:min(200, len(body))])
	}

	if result.Error != nil {
		return fmt.Errorf("ошибка авторизации Wialon: код %d", *result.Error)
	}

	c.sid = result.EID
	if result.User != nil {
		c.userID = result.User.ID
		c.userName = result.User.Name
	}
	return nil
}

// GetCurrentUserID возвращает ID текущего авторизованного пользователя
func (c *Client) GetCurrentUserID() int64 {
	return c.userID
}

// GetCurrentUserName возвращает имя текущего авторизованного пользователя
func (c *Client) GetCurrentUserName() string {
	return c.userName
}

// GetUnits получает все объекты
func (c *Client) GetUnits() (*SearchItemsResponse, error) {
	params := map[string]interface{}{
		"spec": map[string]interface{}{
			"itemsType":     "avl_unit",
			"propName":      "sys_name",
			"propValueMask": "*",
			"sortType":      "sys_name",
			"propType":      "property",
		},
		"force": 1,
		"flags": 5, // 1 (основные) + 4 (биллинг)
		"from":  0,
		"to":    0,
	}

	paramsJSON, _ := json.Marshal(params)

	resp, err := c.requestWithSID("core/search_items", string(paramsJSON))
	if err != nil {
		return nil, err
	}

	var result SearchItemsResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("ошибка парсинга ответа: %v, raw: %s", err, string(resp)[:min(200, len(resp))])
	}

	if result.Error != nil {
		return nil, fmt.Errorf("ошибка получения объектов: код %d", *result.Error)
	}

	return &result, nil
}

// GetAllUnitsWithStatus получает все объекты с информацией о статусе активации
// Возвращает активные и деактивированные объекты с полями act и dactt
func (c *Client) GetAllUnitsWithStatus() (*SearchItemsResponse, error) {
	params := map[string]interface{}{
		"spec": map[string]interface{}{
			"itemsType":     "avl_unit",
			"propName":      "sys_name",
			"propValueMask": "*",
			"sortType":      "sys_name",
			"propType":      "property",
		},
		"force": 1,
		"flags": 1439, // 1 (базовые) + 4 (биллинг) + 128 (административные) + 256 (деактивация) + 1024 (расширенные)
		"from":  0,
		"to":    0,
	}

	paramsJSON, _ := json.Marshal(params)

	resp, err := c.requestWithSID("core/search_items", string(paramsJSON))
	if err != nil {
		return nil, err
	}

	var result SearchItemsResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("ошибка парсинга ответа: %v, raw: %s", err, string(resp)[:min(200, len(resp))])
	}

	if result.Error != nil {
		return nil, fmt.Errorf("ошибка получения объектов: код %d", *result.Error)
	}

	return &result, nil
}

// GetAccounts получает все учётные записи (ресурсы с rel_is_account=1)
func (c *Client) GetAccounts() (*SearchItemsResponse, error) {
	params := map[string]interface{}{
		"spec": map[string]interface{}{
			"itemsType":     "avl_resource",
			"propName":      "rel_is_account",
			"propValueMask": "1",
			"sortType":      "sys_name",
			"propType":      "property",
		},
		"force": 1,
		"flags": 5,
		"from":  0,
		"to":    0,
	}

	paramsJSON, _ := json.Marshal(params)

	resp, err := c.requestWithSID("core/search_items", string(paramsJSON))
	if err != nil {
		return nil, err
	}

	var result SearchItemsResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("ошибка парсинга ответа: %v, raw: %s", err, string(resp)[:min(200, len(resp))])
	}

	if result.Error != nil {
		return nil, fmt.Errorf("ошибка получения учётных записей: код %d", *result.Error)
	}

	return &result, nil
}

// GetAccountsByCreatorName получает учётные записи по имени создателя (оптимизированный поиск)
func (c *Client) GetAccountsByCreatorName(creatorName string) (*SearchItemsResponse, error) {
	params := map[string]interface{}{
		"spec": map[string]interface{}{
			"itemsType":     "avl_resource",
			"propName":      "rel_is_account,rel_user_creator_name",
			"propValueMask": "1," + creatorName,
			"sortType":      "sys_name",
			"propType":      "property",
		},
		"force": 1,
		"flags": 5, // 1 (базовые) + 4 (биллинг: crt, bact)
		"from":  0,
		"to":    0,
	}

	paramsJSON, _ := json.Marshal(params)

	resp, err := c.requestWithSID("core/search_items", string(paramsJSON))
	if err != nil {
		return nil, err
	}

	var result SearchItemsResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("ошибка парсинга ответа: %v, raw: %s", err, string(resp)[:min(200, len(resp))])
	}

	if result.Error != nil {
		return nil, fmt.Errorf("ошибка поиска по создателю: код %d", *result.Error)
	}

	return &result, nil
}

// GetAccountData получает данные учётной записи
func (c *Client) GetAccountData(accountID int64) (*AccountDataResponse, error) {
	params := map[string]interface{}{
		"itemId": accountID,
		"type":   2, // usage с дочерними (type=6 показывает 0 для дилеров)
	}

	paramsJSON, _ := json.Marshal(params)

	resp, err := c.requestWithSID("account/get_account_data", string(paramsJSON))
	if err != nil {
		return nil, err
	}

	var result AccountDataResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}

	if result.Error != nil {
		return nil, fmt.Errorf("ошибка получения данных аккаунта: код %d", *result.Error)
	}

	return &result, nil
}

// GetAccountsDataBatch получает данные множества учётных записей батч-запросами (по 50 за раз)
func (c *Client) GetAccountsDataBatch(accountIDs []int64) (map[int64]*AccountDataResponse, error) {
	resultMap := make(map[int64]*AccountDataResponse)

	// Размер чанка (уменьшен для избежания HTTP/2 GOAWAY)
	const chunkSize = 50

	for start := 0; start < len(accountIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(accountIDs) {
			end = len(accountIDs)
		}

		chunk := accountIDs[start:end]

		// Формируем массив батч-запросов для чанка
		batchParams := make([]map[string]interface{}, len(chunk))
		for i, id := range chunk {
			batchParams[i] = map[string]interface{}{
				"svc": "account/get_account_data",
				"params": map[string]interface{}{
					"itemId": id,
					"type":   2, // usage с дочерними (type=6 показывает 0 для дилеров)
				},
			}
		}

		params := map[string]interface{}{
			"params": batchParams,
			"flags":  0, // Продолжать при ошибках
		}

		paramsJSON, _ := json.Marshal(params)

		resp, err := c.requestWithSID("core/batch", string(paramsJSON))
		if err != nil {
			return nil, fmt.Errorf("ошибка батч-запроса (chunk %d-%d): %v", start, end, err)
		}

		// Парсим массив ответов
		var results []AccountDataResponse
		if err := json.Unmarshal(resp, &results); err != nil {
			return nil, fmt.Errorf("ошибка парсинга батч-ответа: %v", err)
		}

		// Сопоставляем результаты с ID
		for i, result := range results {
			if i < len(chunk) && result.Error == nil {
				resultCopy := result
				resultMap[chunk[i]] = &resultCopy
			}
		}

		// Пауза между батчами для избежания перегрузки API
		time.Sleep(100 * time.Millisecond)
	}

	return resultMap, nil
}

// request выполняет HTTP-запрос к Wialon API
func (c *Client) request(svc string, params url.Values) ([]byte, error) {
	reqURL := fmt.Sprintf("%s/wialon/ajax.html?svc=%s", c.baseURL, svc)

	req, err := http.NewRequest("POST", reqURL, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

// requestWithSID выполняет запрос с session ID
func (c *Client) requestWithSID(svc string, paramsJSON string) ([]byte, error) {
	if c.sid == "" {
		if err := c.Login(); err != nil {
			return nil, err
		}
	}

	// Формируем URL с params в query string
	reqURL := fmt.Sprintf("%s/wialon/ajax.html?svc=%s&sid=%s&params=%s",
		c.baseURL, svc, c.sid, url.QueryEscape(paramsJSON))

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

// AccountHistoryItem - элемент истории аккаунта
type AccountHistoryItem struct {
	ActionType int    `json:"action_type"` // 1 — payment, 0 — charged
	Date       string `json:"date"`
	Service    string `json:"service"`
	Cost       string `json:"cost"`
	Quantity   int    `json:"quantity"`
	Info       string `json:"info"`
	RegTime    int64  `json:"reg_time"`
}

// AccountHistoryResponse - ответ истории аккаунта
type AccountHistoryResponse struct {
	Items []AccountHistoryItem
	Error *int `json:"error,omitempty"`
}

// GetAccountHistory получает историю изменений аккаунта за указанный период
func (c *Client) GetAccountHistory(accountID int64, days int) ([]AccountHistoryItem, error) {
	params := map[string]interface{}{
		"itemId": accountID,
		"days":   days,
		"tz":     14400, // UTC+4 (Казахстан)
	}

	paramsJSON, _ := json.Marshal(params)

	resp, err := c.requestWithSID("account/get_account_history", string(paramsJSON))
	if err != nil {
		return nil, err
	}

	// Ответ — это массив массивов [[action, date, service, cost, quantity, info, regTime], ...]
	var rawResult [][]interface{}
	if err := json.Unmarshal(resp, &rawResult); err != nil {
		// Попробуем распарсить как ошибку
		var errResp struct {
			Error *int `json:"error"`
		}
		if json.Unmarshal(resp, &errResp) == nil && errResp.Error != nil {
			return nil, fmt.Errorf("ошибка Wialon API: код %d", *errResp.Error)
		}
		return nil, fmt.Errorf("ошибка парсинга ответа истории: %v, raw: %s", err, string(resp)[:min(500, len(resp))])
	}

	// Преобразуем в структуры
	var items []AccountHistoryItem
	for _, row := range rawResult {
		if len(row) < 7 {
			continue
		}
		item := AccountHistoryItem{
			ActionType: int(row[0].(float64)),
			Date:       fmt.Sprintf("%v", row[1]),
			Service:    fmt.Sprintf("%v", row[2]),
			Cost:       fmt.Sprintf("%v", row[3]),
			Quantity:   int(row[4].(float64)),
			Info:       fmt.Sprintf("%v", row[5]),
			RegTime:    int64(row[6].(float64)),
		}
		items = append(items, item)
	}

	return items, nil
}

// DailyStats - статистика за один день
type DailyStats struct {
	Date                string `json:"date"`
	Timestamp           int64  `json:"timestamp"`
	UnitTotal           int    `json:"unit_total"`
	UnitCreated         int    `json:"unit_created"`
	UnitDeleted         int    `json:"unit_deleted"`
	UserCreated         int    `json:"user_created"`
	UserDeleted         int    `json:"user_deleted"`
	ResourceCreated     int    `json:"resource_created"`
	ResourceDeleted     int    `json:"resource_deleted"`
	GeozoneCreated      int    `json:"geozone_created"`
	GeozoneDeleted      int    `json:"geozone_deleted"`
	SensorCreated       int    `json:"sensor_created"`
	SensorDeleted       int    `json:"sensor_deleted"`
	NotificationCreated int    `json:"notification_created"`
}

// GetStatistics получает статистику изменений аккаунта по дням
func (c *Client) GetStatistics(accountIDs []int64, fromTime, toTime int64) (map[int64][]DailyStats, error) {
	result := make(map[int64][]DailyStats)

	// API принимает только один resourceId, поэтому делаем запросы для каждого аккаунта
	for _, accountID := range accountIDs {
		params := map[string]interface{}{
			"resourceId": accountID,
			"timeFrom":   fromTime,
			"timeTo":     toTime,
			"type":       "items", // "items" для статистики объектов
			"recursive":  0,       // 0 = только этот аккаунт, без дочерних
		}

		paramsJSON, _ := json.Marshal(params)

		// Первая попытка
		resp, err := c.requestWithSID("core/get_statistics", string(paramsJSON))
		if err != nil {
			return nil, err
		}

		// Проверяем на ошибку сессии (код 4) для retry
		var errResp struct {
			Error *int `json:"error"`
		}
		if json.Unmarshal(resp, &errResp) == nil && errResp.Error != nil && *errResp.Error == 4 {
			// Сессия истекла — перелогиниваемся и повторяем
			c.sid = "" // Сброс сессии
			resp, err = c.requestWithSID("core/get_statistics", string(paramsJSON))
			if err != nil {
				return nil, err
			}
		}

		// Парсим результат для этого аккаунта
		stats, err := c.parseStatisticsResponse(resp, accountID)
		if err != nil {
			log.Printf("Ошибка парсинга статистики аккаунта %d: %v", accountID, err)
			continue // Продолжаем с другими аккаунтами
		}
		result[accountID] = stats
	}

	return result, nil
}

// parseStatisticsResponse парсит ответ API статистики
func (c *Client) parseStatisticsResponse(resp []byte, accountID int64) ([]DailyStats, error) {
	// Проверяем на ошибку
	var errResp struct {
		Error *int `json:"error"`
	}
	if json.Unmarshal(resp, &errResp) == nil && errResp.Error != nil {
		return nil, fmt.Errorf("ошибка Wialon API: код %d", *errResp.Error)
	}

	// Ответ: { "timestamp": { "resourceId": { "avl_unit_total": 123, ... } }, "users": {...} }
	var rawResult map[string]json.RawMessage
	if err := json.Unmarshal(resp, &rawResult); err != nil {
		return nil, fmt.Errorf("ошибка парсинга статистики: %v, raw: %s", err, string(resp)[:min(500, len(resp))])
	}

	// Группируем по дате (DD.MM.YYYY), а не по timestamp
	dateMap := make(map[string]*DailyStats)

	for timestampStr, data := range rawResult {
		// Пропускаем служебные поля
		if timestampStr == "users" {
			continue
		}

		// Парсим timestamp
		var timestamp int64
		if _, err := fmt.Sscanf(timestampStr, "%d", &timestamp); err != nil {
			continue
		}

		// Парсим данные ресурсов
		var resourceData map[string]map[string]int
		if err := json.Unmarshal(data, &resourceData); err != nil {
			continue
		}

		// Формат даты для группировки
		t := time.Unix(timestamp, 0)
		dateStr := t.Format("02.01.2006")

		// Получаем или создаём запись для этой даты
		dailyStat, exists := dateMap[dateStr]
		if !exists {
			dailyStat = &DailyStats{
				Date:      dateStr,
				Timestamp: timestamp,
			}
			dateMap[dateStr] = dailyStat
		}

		// Суммируем статистику со всех ресурсов
		for _, stats := range resourceData {
			dailyStat.UnitTotal += stats["avl_unit_total"]
			dailyStat.UnitCreated += stats["avl_unit_created"]
			dailyStat.UnitDeleted += stats["avl_unit_deleted"]
			dailyStat.UserCreated += stats["storage_user_created"]
			dailyStat.UserDeleted += stats["storage_user_deleted"]
			dailyStat.ResourceCreated += stats["avl_resource_created"]
			dailyStat.ResourceDeleted += stats["avl_resource_deleted"]
			dailyStat.GeozoneCreated += stats["avl_geozone_created"]
			dailyStat.GeozoneDeleted += stats["avl_geozone_deleted"]
			dailyStat.SensorCreated += stats["avl_unit_sensor_created"]
			dailyStat.SensorDeleted += stats["avl_unit_sensor_deleted"]
			dailyStat.NotificationCreated += stats["avl_notification_created"]
		}
	}

	// Преобразуем map в slice
	var result []DailyStats
	for _, stat := range dateMap {
		result = append(result, *stat)
	}

	// Сортируем по дате
	for i := 0; i < len(result)-1; i++ {
		for j := i + 1; j < len(result); j++ {
			if result[i].Timestamp > result[j].Timestamp {
				result[i], result[j] = result[j], result[i]
			}
		}
	}

	return result, nil
}
