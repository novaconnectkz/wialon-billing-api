package snapshot

import (
	"log"
	"time"

	"github.com/user/wialon-billing-api/internal/models"
	"github.com/user/wialon-billing-api/internal/repository"
	"github.com/user/wialon-billing-api/internal/services/wialon"
)

// Service - сервис для работы со снимками
type Service struct {
	repo   *repository.Repository
	wialon *wialon.Client
}

// NewService создаёт новый сервис снимков
func NewService(repo *repository.Repository, wialon *wialon.Client) *Service {
	return &Service{
		repo:   repo,
		wialon: wialon,
	}
}

// resolveDeactivatedForDealers разрешает подсчёт деактивированных объектов для дилерских аккаунтов.
// Проблема: поле bact у объектов (avl_unit) указывает на суб-аккаунт (прямого владельца),
// а не на дилерский аккаунт. Эта функция получает parentAccountId для каждого bact
// и суммирует деактивированные из дочерних аккаунтов к родительскому (дилерскому).
func resolveDeactivatedForDealers(wialonClient *wialon.Client, deactivatedByAccount map[int64]int) map[int64]int {
	// Собираем уникальные bact с деактивированными объектами
	bactIDs := make([]int64, 0, len(deactivatedByAccount))
	for bact := range deactivatedByAccount {
		bactIDs = append(bactIDs, bact)
	}
	if len(bactIDs) == 0 {
		return deactivatedByAccount
	}

	// Получаем parentAccountId для каждого bact
	parentData, err := wialonClient.GetAccountsDataBatch(bactIDs)
	if err != nil {
		log.Printf("resolveDeactivatedForDealers: ошибка получения parentAccountId: %v", err)
		return deactivatedByAccount
	}

	// Создаём итоговую карту: для каждого родителя суммируем деактивированные из дочерних
	result := make(map[int64]int)
	for bact, count := range deactivatedByAccount {
		result[bact] += count
		if data, ok := parentData[bact]; ok && data.ParentAccountId > 0 {
			result[data.ParentAccountId] += count
		}
	}

	log.Printf("resolveDeactivatedForDealers: обработано %d bact, итого %d записей в карте",
		len(bactIDs), len(result))

	return result
}

// EnsureDailySnapshot — идемпотентная обёртка: создаёт снимки за вчерашний день,
// только если их ещё нет. Безопасна для повторного вызова.
// Использует CreateSnapshotsForDate (с Login и multi-connection поддержкой).
func (s *Service) EnsureDailySnapshot() error {
	yesterday := time.Now().UTC().AddDate(0, 0, -1)
	snapshotDate := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, time.UTC)

	exists, err := s.repo.HasSnapshotsForDate(snapshotDate)
	if err != nil {
		return err
	}
	if exists {
		log.Printf("Снимки за %s уже существуют, пропускаем", snapshotDate.Format("2006-01-02"))
		return nil
	}

	log.Printf("Снимков за %s нет, создаём...", snapshotDate.Format("2006-01-02"))
	snapshots, err := s.CreateSnapshotsForDate(snapshotDate)
	if err != nil {
		return err
	}
	log.Printf("EnsureDailySnapshot: создано %d снимков за %s", len(snapshots), snapshotDate.Format("2006-01-02"))
	return nil
}

// CreateDailySnapshot создаёт ежедневный снимок для всех активных аккаунтов
func (s *Service) CreateDailySnapshot() error {
	// Получаем аккаунты, участвующие в биллинге
	accounts, err := s.repo.GetSelectedAccounts()
	if err != nil {
		return err
	}

	if len(accounts) == 0 {
		log.Println("Нет аккаунтов для снимков")
		return nil
	}

	// Получаем все объекты из Wialon с информацией о статусе активации
	unitsResp, err := s.wialon.GetAllUnitsWithStatus()
	if err != nil {
		return err
	}

	log.Printf("Получено %d объектов из Wialon", unitsResp.TotalItemsCount)

	// Создаём снимки для каждого аккаунта
	for _, account := range accounts {
		if err := s.createSnapshotForAccount(account, unitsResp.Items); err != nil {
			log.Printf("Ошибка создания снимка для аккаунта %s: %v", account.Name, err)
			continue
		}
	}

	return nil
}

// createSnapshotForAccount создаёт снимок для конкретного аккаунта
func (s *Service) createSnapshotForAccount(account models.Account, allUnits []wialon.WialonItem) error {
	// Фильтруем объекты по аккаунту
	var accountUnits []wialon.WialonItem
	for _, unit := range allUnits {
		if unit.AccountID == account.WialonID {
			accountUnits = append(accountUnits, unit)
		}
	}

	// Разделяем на активные и деактивированные
	var activeCount, deactivatedCount int
	for _, unit := range accountUnits {
		if unit.Active == 1 || unit.Active == 0 && unit.DeactivatedTime == 0 {
			// Active == 1 или не заполнено поле — считаем активным
			if unit.DeactivatedTime == 0 {
				activeCount++
			} else {
				deactivatedCount++
			}
		} else {
			deactivatedCount++
		}
	}

	// Пересчитываем корректно
	activeCount = 0
	deactivatedCount = 0
	for _, unit := range accountUnits {
		if unit.Active == 0 && unit.DeactivatedTime > 0 {
			deactivatedCount++
		} else {
			activeCount++
		}
	}

	// Получаем предыдущий снимок
	prevSnapshot, _ := s.repo.GetLastSnapshot(account.ID)

	// Снимок создаётся за вчерашний день
	yesterday := time.Now().UTC().AddDate(0, 0, -1)
	snapshotDate := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, time.UTC)

	// Создаём новый снимок (TotalUnits = только активные!)
	snapshot := &models.Snapshot{
		AccountID:        account.ID,
		SnapshotDate:     snapshotDate,
		TotalUnits:       activeCount,
		UnitsDeactivated: deactivatedCount,
	}

	if err := s.repo.CreateSnapshot(snapshot); err != nil {
		return err
	}

	// Сохраняем объекты снимка для отслеживания изменений
	for _, unit := range accountUnits {
		isActive := !(unit.Active == 0 && unit.DeactivatedTime > 0)
		var deactivatedAt *time.Time
		if unit.DeactivatedTime > 0 {
			t := time.Unix(unit.DeactivatedTime, 0)
			deactivatedAt = &t
		}

		snapshotUnit := &models.SnapshotUnit{
			SnapshotID:    snapshot.ID,
			WialonUnitID:  unit.ID,
			UnitName:      unit.Name,
			AccountID:     unit.AccountID,
			CreatorID:     unit.CreatorID,
			IsActive:      isActive,
			DeactivatedAt: deactivatedAt,
		}
		if err := s.repo.CreateSnapshotUnit(snapshotUnit); err != nil {
			log.Printf("Ошибка сохранения объекта %s: %v", unit.Name, err)
		}
	}

	// Сравниваем с предыдущим снимком
	if prevSnapshot != nil {
		s.detectChanges(prevSnapshot, snapshot, accountUnits)
	}

	log.Printf("Создан снимок для %s: %d активных, %d деактивированных", account.Name, activeCount, deactivatedCount)
	return nil
}

// detectChanges обнаруживает изменения между снимками
func (s *Service) detectChanges(prev, curr *models.Snapshot, currentUnits []wialon.WialonItem) {
	// Создаём карту предыдущих объектов
	prevUnits := make(map[int64]models.SnapshotUnit)
	for _, u := range prev.Units {
		prevUnits[u.WialonUnitID] = u
	}

	// Создаём карту текущих объектов
	currUnits := make(map[int64]wialon.WialonItem)
	for _, u := range currentUnits {
		currUnits[u.ID] = u
	}

	// Находим добавленные объекты
	for _, u := range currentUnits {
		if _, exists := prevUnits[u.ID]; !exists {
			change := &models.Change{
				PrevSnapshotID: &prev.ID,
				CurrSnapshotID: curr.ID,
				WialonUnitID:   u.ID,
				UnitName:       u.Name,
				ChangeType:     "added",
			}
			s.repo.CreateChange(change)
			log.Printf("Добавлен объект: %s", u.Name)
		}
	}

	// Находим удалённые объекты
	for _, u := range prev.Units {
		if _, exists := currUnits[u.WialonUnitID]; !exists {
			change := &models.Change{
				PrevSnapshotID: &prev.ID,
				CurrSnapshotID: curr.ID,
				WialonUnitID:   u.WialonUnitID,
				UnitName:       u.UnitName,
				ChangeType:     "removed",
			}
			s.repo.CreateChange(change)
			log.Printf("Удалён объект: %s", u.UnitName)
		}
	}
}

// CreateManualSnapshot создаёт ручной снимок (для API)
func (s *Service) CreateManualSnapshot(accountID uint) (*models.Snapshot, error) {
	// Получаем аккаунт
	accounts, err := s.repo.GetAllAccounts()
	if err != nil {
		return nil, err
	}

	var account *models.Account
	for _, a := range accounts {
		if a.ID == accountID {
			account = &a
			break
		}
	}

	if account == nil {
		return nil, nil
	}

	// Получаем объекты
	unitsResp, err := s.wialon.GetUnits()
	if err != nil {
		return nil, err
	}

	// Фильтруем по аккаунту
	var accountUnits []wialon.WialonItem
	for _, unit := range unitsResp.Items {
		if unit.AccountID == account.WialonID {
			accountUnits = append(accountUnits, unit)
		}
	}

	// Создаём снимок
	snapshot := &models.Snapshot{
		AccountID:  account.ID,
		TotalUnits: len(accountUnits),
	}

	if err := s.repo.CreateSnapshot(snapshot); err != nil {
		return nil, err
	}

	return snapshot, nil
}

// CreateSnapshotsForRange создаёт снимки за диапазон дат с обратным расчётом TotalUnits
// Алгоритм: берёт текущий avl_unit.usage, получает created/deleted за весь период,
// и рассчитывает usage для каждого прошлого дня:
// usage(день N) = usage(день N+1) - created(день N+1) + deleted(день N+1)
func (s *Service) CreateSnapshotsForRange(fromDate, toDate time.Time) ([]models.Snapshot, error) {
	// Получаем аккаунты, участвующие в биллинге
	accounts, err := s.repo.GetSelectedAccounts()
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, nil
	}

	// Группируем аккаунты по connection_id
	accountsByConnection := make(map[uint][]models.Account)
	for _, acc := range accounts {
		var connID uint
		if acc.ConnectionID != nil {
			connID = *acc.ConnectionID
		}
		accountsByConnection[connID] = append(accountsByConnection[connID], acc)
	}

	log.Printf("CreateSnapshotsForRange: %s — %s, %d аккаунтов в %d подключениях",
		fromDate.Format("2006-01-02"), toDate.Format("2006-01-02"),
		len(accounts), len(accountsByConnection))

	var allSnapshots []models.Snapshot

	for connID, connAccounts := range accountsByConnection {
		var wialonClient *wialon.Client

		if connID == 0 {
			wialonClient = s.wialon
		} else {
			conn, err := s.repo.GetConnectionByID(connID)
			if err != nil || conn == nil {
				log.Printf("CreateSnapshotsForRange: подключение %d не найдено, пропускаем", connID)
				continue
			}
			wialonURL := "https://" + conn.WialonHost
			wialonClient = wialon.NewClientWithToken(wialonURL, conn.Token)
		}

		if err := wialonClient.Login(); err != nil {
			log.Printf("CreateSnapshotsForRange: ошибка авторизации для подключения %d: %v", connID, err)
			continue
		}

		snapshots, err := s.createSnapshotsForConnectionRange(wialonClient, connAccounts, fromDate, toDate)
		if err != nil {
			log.Printf("CreateSnapshotsForRange: ошибка для подключения %d: %v", connID, err)
			continue
		}
		allSnapshots = append(allSnapshots, snapshots...)
	}

	return allSnapshots, nil
}

// createSnapshotsForConnectionRange создаёт снимки за диапазон с обратным расчётом
func (s *Service) createSnapshotsForConnectionRange(wialonClient *wialon.Client, accounts []models.Account, fromDate, toDate time.Time) ([]models.Snapshot, error) {
	accountIDs := make([]int64, len(accounts))
	for i, acc := range accounts {
		accountIDs[i] = acc.WialonID
	}

	// 1. Текущий avl_unit.usage
	accountsData, err := wialonClient.GetAccountsDataBatch(accountIDs)
	if err != nil {
		return nil, err
	}

	// 2. Статистика created/deleted за весь диапазон (с запасом +1 день)
	statsFrom := fromDate.Unix()
	statsTo := toDate.Add(24 * time.Hour).Unix()
	stats, err := wialonClient.GetStatistics(accountIDs, statsFrom, statsTo)
	if err != nil {
		log.Printf("createSnapshotsForConnectionRange: ошибка GetStatistics: %v", err)
	}

	// 3. Деактивированные объекты
	unitsResp, _ := wialonClient.GetAllUnitsWithStatus()
	deactivatedByAccount := make(map[int64]int)
	if unitsResp != nil {
		for _, unit := range unitsResp.Items {
			if unit.Active == 0 && unit.DeactivatedTime > 0 {
				deactivatedByAccount[unit.AccountID]++
			}
		}
	}

	// Разрешаем деактивированные для дилерских аккаунтов (bact → parentAccountId)
	deactivatedByAccount = resolveDeactivatedForDealers(wialonClient, deactivatedByAccount)

	// 4. Собираем даты
	var dates []time.Time
	for d := fromDate; !d.After(toDate); d = d.AddDate(0, 0, 1) {
		dates = append(dates, d)
	}

	var allSnapshots []models.Snapshot

	for _, account := range accounts {
		wid := account.WialonID

		// Текущий usage (на сегодня/последний день)
		currentUsage := 0
		if accData, ok := accountsData[wid]; ok {
			currentUsage = accData.GetUnitUsage()
		}

		// Индексируем created/deleted по датам для этого аккаунта
		dailyStats := make(map[string]struct{ Created, Deleted int })
		if stats != nil {
			if accountStats, ok := stats[wid]; ok {
				for _, ds := range accountStats {
					dateKey := time.Unix(ds.Timestamp, 0).UTC().Format("2006-01-02")
					dailyStats[dateKey] = struct{ Created, Deleted int }{ds.UnitCreated, ds.UnitDeleted}
				}
			}
		}

		// Обратный расчёт: от последнего дня к первому
		usageByDate := make(map[string]int)
		usage := currentUsage

		// Последний день = текущий usage
		lastDateKey := dates[len(dates)-1].Format("2006-01-02")
		usageByDate[lastDateKey] = usage

		// Идём назад
		for i := len(dates) - 2; i >= 0; i-- {
			nextDateKey := dates[i+1].Format("2006-01-02")
			nextDayStats := dailyStats[nextDateKey]
			// usage_сегодня = usage_завтра - created_завтра + deleted_завтра
			usage = usageByDate[nextDateKey] - nextDayStats.Created + nextDayStats.Deleted
			if usage < 0 {
				usage = 0
			}
			dateKey := dates[i].Format("2006-01-02")
			usageByDate[dateKey] = usage
		}

		// Создаём снимки за каждый день
		for _, date := range dates {
			dateKey := date.Format("2006-01-02")
			ds := dailyStats[dateKey]

			snapshot := &models.Snapshot{
				AccountID:        account.ID,
				SnapshotDate:     date,
				TotalUnits:       usageByDate[dateKey],
				UnitsCreated:     ds.Created,
				UnitsDeleted:     ds.Deleted,
				UnitsDeactivated: deactivatedByAccount[wid],
			}

			if err := s.repo.UpsertSnapshot(snapshot); err != nil {
				log.Printf("createSnapshotsForConnectionRange: ошибка upsert снимка для %s за %s: %v",
					account.Name, dateKey, err)
				continue
			}

			allSnapshots = append(allSnapshots, *snapshot)
		}

		log.Printf("Создано %d снимков для %s (usage: %d→%d)",
			len(dates), account.Name, usageByDate[dates[0].Format("2006-01-02")], currentUsage)
	}

	return allSnapshots, nil
}

// CreateSnapshotsForDate создаёт снимки для всех выбранных аккаунтов с указанной датой
// Поддерживает multi-connection: группирует аккаунты по connection_id
func (s *Service) CreateSnapshotsForDate(snapshotDate time.Time) ([]models.Snapshot, error) {
	// Получаем аккаунты, участвующие в биллинге
	accounts, err := s.repo.GetSelectedAccounts()
	if err != nil {
		return nil, err
	}

	if len(accounts) == 0 {
		return nil, nil
	}

	// Группируем аккаунты по connection_id
	accountsByConnection := make(map[uint][]models.Account)
	for _, acc := range accounts {
		var connID uint
		if acc.ConnectionID != nil {
			connID = *acc.ConnectionID
		}
		accountsByConnection[connID] = append(accountsByConnection[connID], acc)
	}

	log.Printf("CreateSnapshotsForDate: %d аккаунтов в %d подключениях",
		len(accounts), len(accountsByConnection))

	var allSnapshots []models.Snapshot

	// Обрабатываем каждое подключение отдельно
	for connID, connAccounts := range accountsByConnection {
		var wialonClient *wialon.Client

		if connID == 0 {
			// Если connection_id не задан — используем глобальный клиент (legacy)
			wialonClient = s.wialon
			log.Printf("CreateSnapshotsForDate: %d аккаунтов без подключения, используем глобальный токен",
				len(connAccounts))
		} else {
			// Получаем подключение из БД
			conn, err := s.repo.GetConnectionByID(connID)
			if err != nil || conn == nil {
				log.Printf("CreateSnapshotsForDate: подключение %d не найдено, пропускаем %d аккаунтов",
					connID, len(connAccounts))
				continue
			}

			// Создаём Wialon клиент с токеном подключения
			wialonURL := "https://" + conn.WialonHost
			wialonClient = wialon.NewClientWithToken(wialonURL, conn.Token)
			log.Printf("CreateSnapshotsForDate: подключение %s (%s), %d аккаунтов",
				conn.Name, conn.WialonHost, len(connAccounts))
		}

		// Авторизуемся
		if err := wialonClient.Login(); err != nil {
			log.Printf("CreateSnapshotsForDate: ошибка авторизации для подключения %d: %v", connID, err)
			continue
		}

		// Создаём снимки для аккаунтов этого подключения
		snapshots, err := s.createSnapshotsForConnection(wialonClient, connAccounts, snapshotDate)
		if err != nil {
			log.Printf("CreateSnapshotsForDate: ошибка для подключения %d: %v", connID, err)
			continue
		}

		allSnapshots = append(allSnapshots, snapshots...)
	}

	return allSnapshots, nil
}

// createSnapshotsForConnection создаёт снимки для аккаунтов одного подключения
// Гибридный подход:
//   - GetAccountsDataBatch для TotalUnits (avl_unit.usage — только свои объекты)
//   - GetStatistics для UnitsCreated/UnitsDeleted
//   - GetAllUnitsWithStatus для UnitsDeactivated
func (s *Service) createSnapshotsForConnection(wialonClient *wialon.Client, accounts []models.Account, snapshotDate time.Time) ([]models.Snapshot, error) {
	// Собираем WialonID всех аккаунтов
	accountIDs := make([]int64, len(accounts))
	for i, acc := range accounts {
		accountIDs[i] = acc.WialonID
	}

	// 1. Получаем avl_unit.usage через GetAccountsDataBatch (только свои объекты, без дочерних)
	accountsData, err := wialonClient.GetAccountsDataBatch(accountIDs)
	if err != nil {
		log.Printf("createSnapshotsForConnection: ошибка GetAccountsDataBatch: %v, используем fallback", err)
		return s.createSnapshotsViaUnits(wialonClient, accounts, snapshotDate)
	}

	// 2. Получаем статистику created/deleted через GetStatistics API
	fromTime := snapshotDate.Unix()
	toTime := snapshotDate.Add(24 * time.Hour).Unix()

	stats, err := wialonClient.GetStatistics(accountIDs, fromTime, toTime)
	if err != nil {
		log.Printf("createSnapshotsForConnection: ошибка GetStatistics: %v (created/deleted будут 0)", err)
		// Продолжаем без данных о created/deleted
	}

	// 3. Получаем все объекты с информацией о деактивации
	unitsResp, err := wialonClient.GetAllUnitsWithStatus()
	if err != nil {
		log.Printf("createSnapshotsForConnection: ошибка GetAllUnitsWithStatus: %v", err)
		unitsResp = nil
	}

	// Группируем деактивированные объекты по аккаунтам
	deactivatedByAccount := make(map[int64]int)
	if unitsResp != nil {
		for _, unit := range unitsResp.Items {
			if unit.Active == 0 && unit.DeactivatedTime > 0 {
				deactivatedByAccount[unit.AccountID]++
			}
		}
	}

	// Разрешаем деактивированные для дилерских аккаунтов (bact → parentAccountId)
	deactivatedByAccount = resolveDeactivatedForDealers(wialonClient, deactivatedByAccount)

	var snapshots []models.Snapshot

	for _, account := range accounts {
		// TotalUnits из avl_unit.usage (только свои объекты)
		var totalUnits int
		if accData, ok := accountsData[account.WialonID]; ok {
			totalUnits = accData.GetUnitUsage()
		}

		// Created/Deleted из GetStatistics
		var unitsCreated, unitsDeleted int
		if stats != nil {
			if accountStats, ok := stats[account.WialonID]; ok && len(accountStats) > 0 {
				unitsCreated = accountStats[0].UnitCreated
				unitsDeleted = accountStats[0].UnitDeleted
			}
		}

		// Деактивированные из GetAllUnitsWithStatus
		unitsDeactivated := deactivatedByAccount[account.WialonID]

		snapshot := &models.Snapshot{
			AccountID:        account.ID,
			SnapshotDate:     snapshotDate,
			TotalUnits:       totalUnits,
			UnitsCreated:     unitsCreated,
			UnitsDeleted:     unitsDeleted,
			UnitsDeactivated: unitsDeactivated,
		}

		if err := s.repo.CreateSnapshot(snapshot); err != nil {
			log.Printf("createSnapshotsForConnection: ошибка создания снимка для %s: %v", account.Name, err)
			continue
		}

		log.Printf("Создан снимок для %s: %d объектов (+%d/-%d), деактивировано: %d на %s",
			account.Name, totalUnits, unitsCreated, unitsDeleted, unitsDeactivated, snapshotDate.Format("2006-01-02"))
		snapshots = append(snapshots, *snapshot)
	}

	return snapshots, nil
}

// createSnapshotsViaUnits - fallback через GetUnits (с сохранением SnapshotUnits и детекцией изменений)
func (s *Service) createSnapshotsViaUnits(wialonClient *wialon.Client, accounts []models.Account, snapshotDate time.Time) ([]models.Snapshot, error) {
	// Используем GetAllUnitsWithStatus для получения статуса деактивации
	unitsResp, err := wialonClient.GetAllUnitsWithStatus()
	if err != nil {
		// Fallback на обычный GetUnits
		unitsResp, err = wialonClient.GetUnits()
		if err != nil {
			return nil, err
		}
	}

	log.Printf("createSnapshotsViaUnits: получено %d объектов для %d аккаунтов",
		unitsResp.TotalItemsCount, len(accounts))

	var snapshots []models.Snapshot

	for _, account := range accounts {
		// Фильтруем объекты по аккаунту
		var accountUnits []wialon.WialonItem
		for _, unit := range unitsResp.Items {
			if unit.AccountID == account.WialonID {
				accountUnits = append(accountUnits, unit)
			}
		}

		// Разделяем на активные и деактивированные
		var activeCount, deactivatedCount int
		for _, unit := range accountUnits {
			if unit.Active == 0 && unit.DeactivatedTime > 0 {
				deactivatedCount++
			} else {
				activeCount++
			}
		}

		// Для дилерских аккаунтов: если нет прямых объектов, берём из общей карты
		if deactivatedCount == 0 {
			// Строим карту деактивированных по bact из всех объектов
			allDeactivated := make(map[int64]int)
			for _, unit := range unitsResp.Items {
				if unit.Active == 0 && unit.DeactivatedTime > 0 {
					allDeactivated[unit.AccountID]++
				}
			}
			resolved := resolveDeactivatedForDealers(wialonClient, allDeactivated)
			if resolved[account.WialonID] > 0 {
				deactivatedCount = resolved[account.WialonID]
			}
		}

		// Получаем предыдущий снимок для сравнения
		prevSnapshot, _ := s.repo.GetLastSnapshot(account.ID)

		// Создаём новый снимок (TotalUnits = только активные!)
		snapshot := &models.Snapshot{
			AccountID:        account.ID,
			SnapshotDate:     snapshotDate,
			TotalUnits:       activeCount,
			UnitsDeactivated: deactivatedCount,
		}

		if err := s.repo.CreateSnapshot(snapshot); err != nil {
			log.Printf("createSnapshotsViaUnits: ошибка создания снимка для %s: %v", account.Name, err)
			continue
		}

		// Сохраняем объекты снимка для отслеживания изменений
		for _, unit := range accountUnits {
			isActive := !(unit.Active == 0 && unit.DeactivatedTime > 0)
			var deactivatedAt *time.Time
			if unit.DeactivatedTime > 0 {
				t := time.Unix(unit.DeactivatedTime, 0)
				deactivatedAt = &t
			}

			snapshotUnit := &models.SnapshotUnit{
				SnapshotID:    snapshot.ID,
				WialonUnitID:  unit.ID,
				UnitName:      unit.Name,
				AccountID:     unit.AccountID,
				CreatorID:     unit.CreatorID,
				IsActive:      isActive,
				DeactivatedAt: deactivatedAt,
			}
			if err := s.repo.CreateSnapshotUnit(snapshotUnit); err != nil {
				log.Printf("createSnapshotsViaUnits: ошибка сохранения объекта %s: %v", unit.Name, err)
			}
		}

		// Сравниваем с предыдущим снимком и фиксируем изменения
		if prevSnapshot != nil && len(prevSnapshot.Units) > 0 {
			s.detectChanges(prevSnapshot, snapshot, accountUnits)
		}

		log.Printf("Создан снимок для %s: %d активных, %d деактивированных на %s",
			account.Name, activeCount, deactivatedCount, snapshotDate.Format("2006-01-02"))
		snapshots = append(snapshots, *snapshot)
	}

	return snapshots, nil
}

// CalculateDailyCharges рассчитывает ежедневные начисления для снэпшота
// per_unit: price × units / daysInMonth (ежедневно)
// fixed: полная цена 1-го числа месяца (разово)
func (s *Service) CalculateDailyCharges(snapshot *models.Snapshot, account *models.Account) error {
	if account == nil || len(account.Modules) == 0 {
		return nil
	}

	// Количество дней в месяце снэпшота
	year := snapshot.SnapshotDate.Year()
	month := snapshot.SnapshotDate.Month()
	daysInMonth := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	dayOfMonth := snapshot.SnapshotDate.Day()

	// Считаем только активные объекты (вычитаем деактивированные)
	// TotalUnits в снапшоте = все объекты из Wialon CMS (включая деактивированные)
	// Для начислений используем только активные
	activeUnits := snapshot.TotalUnits - snapshot.UnitsDeactivated
	if activeUnits < 0 {
		activeUnits = 0
	}

	var charges []models.DailyCharge

	for _, am := range account.Modules {
		module := am.Module
		if module.ID == 0 {
			continue
		}

		if module.PricingType == "fixed" {
			// Фиксированные пакеты начисляются разово 1-го числа
			if dayOfMonth != 1 {
				continue
			}
			charges = append(charges, models.DailyCharge{
				AccountID:   account.ID,
				SnapshotID:  snapshot.ID,
				ModuleID:    module.ID,
				ChargeDate:  snapshot.SnapshotDate,
				TotalUnits:  activeUnits,
				ModuleName:  module.Name,
				PricingType: module.PricingType,
				UnitPrice:   module.Price,
				DaysInMonth: daysInMonth,
				DailyCost:   module.Price, // полная стоимость за месяц
				Currency:    module.Currency,
			})
		} else {
			// per_unit: price × activeUnits / daysInMonth
			dailyCost := module.Price * float64(activeUnits) / float64(daysInMonth)
			charges = append(charges, models.DailyCharge{
				AccountID:   account.ID,
				SnapshotID:  snapshot.ID,
				ModuleID:    module.ID,
				ChargeDate:  snapshot.SnapshotDate,
				TotalUnits:  activeUnits,
				ModuleName:  module.Name,
				PricingType: module.PricingType,
				UnitPrice:   module.Price,
				DaysInMonth: daysInMonth,
				DailyCost:   dailyCost,
				Currency:    module.Currency,
			})
		}
	}

	if len(charges) > 0 {
		if err := s.repo.SaveDailyCharges(charges); err != nil {
			log.Printf("CalculateDailyCharges: ошибка сохранения %d записей для аккаунта %d: %v",
				len(charges), account.ID, err)
			return err
		}
		log.Printf("CalculateDailyCharges: сохранено %d начислений для %s за %s",
			len(charges), account.Name, snapshot.SnapshotDate.Format("2006-01-02"))
	}

	return nil
}

// CalculateDailyChargesForPeriod пересчитывает начисления для аккаунта за период
func (s *Service) CalculateDailyChargesForPeriod(accountID uint, year, month int) error {
	// Получаем аккаунт с модулями
	account, err := s.repo.GetAccountByID(accountID)
	if err != nil {
		return err
	}

	// Загружаем модули
	accountModules, err := s.repo.GetAccountModules(accountID)
	if err != nil {
		return err
	}
	account.Modules = accountModules

	// Удаляем старые начисления за период перед пересчётом
	startOfMonth := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	endOfMonth := startOfMonth.AddDate(0, 1, 0)
	if err := s.repo.DeleteDailyCharges(accountID, startOfMonth, endOfMonth); err != nil {
		log.Printf("CalculateDailyChargesForPeriod: ошибка очистки начислений: %v", err)
	}

	// Получаем снэпшоты за период
	snapshots, err := s.repo.GetSnapshotsByAccountAndPeriod(accountID, year, month)
	if err != nil {
		return err
	}

	for _, snap := range snapshots {
		if err := s.CalculateDailyCharges(&snap, account); err != nil {
			log.Printf("CalculateDailyChargesForPeriod: ошибка для снэпшота %d: %v", snap.ID, err)
		}
	}

	log.Printf("CalculateDailyChargesForPeriod: пересчитано %d снэпшотов для аккаунта %d за %d-%02d",
		len(snapshots), accountID, year, month)
	return nil
}
