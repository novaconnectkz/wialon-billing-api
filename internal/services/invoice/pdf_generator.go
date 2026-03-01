package invoice

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
	"github.com/user/wialon-billing-api/internal/models"
)

// PDFGenerator - генератор PDF счетов
type PDFGenerator struct{}

// NewPDFGenerator создаёт новый генератор
func NewPDFGenerator() *PDFGenerator {
	return &PDFGenerator{}
}

// getFontsPath возвращает путь к папке шрифтов
func getFontsPath() string {
	// Библиотека fpdf обрезает первый слеш, добавляем два
	return "//Users/com/bill_wialon/wialon-billing-api/fonts"
}

// russianMonth возвращает название месяца на русском в родительном падеже
func russianMonth(m time.Month) string {
	months := map[time.Month]string{
		time.January:   "января",
		time.February:  "февраля",
		time.March:     "марта",
		time.April:     "апреля",
		time.May:       "мая",
		time.June:      "июня",
		time.July:      "июля",
		time.August:    "августа",
		time.September: "сентября",
		time.October:   "октября",
		time.November:  "ноября",
		time.December:  "декабря",
	}
	return months[m]
}

// russianMonthForPeriod возвращает название месяца для описания периода (именительный падеж)
func russianMonthForPeriod(m time.Month) string {
	months := map[time.Month]string{
		time.January:   "Январь",
		time.February:  "Февраль",
		time.March:     "Март",
		time.April:     "Апрель",
		time.May:       "Май",
		time.June:      "Июнь",
		time.July:      "Июль",
		time.August:    "Август",
		time.September: "Сентябрь",
		time.October:   "Октябрь",
		time.November:  "Ноябрь",
		time.December:  "Декабрь",
	}
	return months[m]
}

// formatDateRussian возвращает дату в формате «1 февраля 2026 г.»
func formatDateRussian(t time.Time) string {
	return fmt.Sprintf("%d %s %d г.", t.Day(), russianMonth(t.Month()), t.Year())
}

// GenerateInvoicePDF генерирует PDF счёта по образцу казахстанского «Счёт на оплату»
func (g *PDFGenerator) GenerateInvoicePDF(invoice *models.Invoice, settings *models.BillingSettings, account *models.Account) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(10, 10, 10)
	pdf.AddPage()

	// Шрифты с поддержкой кириллицы — Arial как в образце
	fontRegular := "./fonts/Arial.ttf"
	fontBold := "./fonts/Arial Bold.ttf"
	fontItalic := "./fonts/Arial Italic.ttf"
	pdf.AddUTF8Font("Arial", "", fontRegular)
	pdf.AddUTF8Font("Arial", "B", fontBold)
	pdf.AddUTF8Font("Arial", "I", fontItalic)

	// Предупреждение об условиях оплаты
	g.drawPaymentNotice(pdf)

	// Блок «Образец платёжного поручения»
	g.drawPaymentOrder(pdf, settings)

	// Заголовок счёта
	g.drawHeader(pdf, invoice, settings)

	// Реквизиты поставщика
	g.drawSupplier(pdf, settings)

	// Реквизиты покупателя
	g.drawBuyer(pdf, account)

	// Ссылка на договор
	g.drawContract(pdf, account)

	// Таблица позиций
	g.drawItemsTable(pdf, invoice)

	// Итоги
	g.drawTotals(pdf, invoice, settings)

	// Сумма прописью
	g.drawAmountInWords(pdf, invoice)

	// Подпись
	g.drawSignature(pdf, settings)

	// Генерируем PDF в буфер
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// drawPaymentNotice — предупреждение об условиях оплаты (верх документа, курсив, по центру)
func (g *PDFGenerator) drawPaymentNotice(pdf *fpdf.Fpdf) {
	pdf.SetFont("Arial", "I", 7)
	notice := "Внимание! Оплата данного счета означает согласие с условиями поставки товара. Уведомление об оплате обязательно, в противном случае не гарантируется наличие товара на складе. Товар отпускается по факту прихода денег на р/с Поставщика, самовывозом, при наличии доверенности и документов удостоверяющих личность."
	pdf.MultiCell(190, 3.5, notice, "", "C", false)
	pdf.Ln(4)
}

// drawPaymentOrder — блок «Образец платёжного поручения» с банковскими реквизитами
// Разметка повторяет казахстанский стандарт: таблица с бенефициаром, ИИК, Кбе, БИК, КНП
func (g *PDFGenerator) drawPaymentOrder(pdf *fpdf.Fpdf, settings *models.BillingSettings) {
	marginL := 10.0 // левый отступ страницы
	pageW := 190.0  // ширина рабочей области (210 - 10 - 10)

	// Ширины колонок (пропорции из образца)
	leftW := 105.0 // Бенефициар / Банк бенефициара
	midW := 55.0   // ИИК / БИК
	rightW := 30.0 // Кбе / Код назначения платежа

	// Заголовок блока
	pdf.SetFont("Arial", "B", 9)
	pdf.CellFormat(pageW, 6, "Образец платёжного поручения", "", 1, "L", false, 0, "")
	pdf.Ln(1)

	// ===== ВЕРХНЯЯ СЕКЦИЯ: Бенефициар + ИИК + Кбе =====

	topY := pdf.GetY()

	// --- Строка заголовков ---
	pdf.SetFont("Arial", "B", 8)
	pdf.SetXY(marginL, topY)
	pdf.CellFormat(leftW, 5, "Бенефициар:", "LT", 0, "L", false, 0, "")
	pdf.CellFormat(midW, 5, "ИИК", "LT", 0, "C", false, 0, "")
	pdf.CellFormat(rightW, 5, "Кбе", "LTR", 1, "C", false, 0, "")

	// --- Строка данных: Компания | ИИК value | Кбе value ---
	// Вычисляем высоту строки по длине названия компании
	companyLines := pdf.SplitText(settings.CompanyName, leftW-4)
	dataRowH := float64(len(companyLines)) * 4.0
	if dataRowH < 8 {
		dataRowH = 8
	}

	dataY := pdf.GetY()

	// Рисуем рамки всех трёх ячеек одинаковой высоты
	pdf.SetXY(marginL, dataY)
	pdf.Rect(marginL, dataY, leftW, dataRowH, "D")             // левая ячейка (только боковые)
	pdf.Rect(marginL+leftW, dataY, midW, dataRowH, "D")        // средняя
	pdf.Rect(marginL+leftW+midW, dataY, rightW, dataRowH, "D") // правая

	// Текст названия компании (с переносом)
	pdf.SetFont("Arial", "B", 8)
	pdf.SetXY(marginL+2, dataY+1)
	pdf.MultiCell(leftW-4, 4, settings.CompanyName, "", "L", false)

	// ИИК значение
	pdf.SetFont("Arial", "B", 8)
	pdf.SetXY(marginL+leftW, dataY)
	pdf.CellFormat(midW, dataRowH, settings.BankIIK, "", 0, "C", false, 0, "")

	// Кбе значение
	pdf.SetXY(marginL+leftW+midW, dataY)
	pdf.CellFormat(rightW, dataRowH, settings.BankKbe, "", 0, "C", false, 0, "")

	// --- Строка БИН (продолжение левой колонки) ---
	binY := dataY + dataRowH
	pdf.SetFont("Arial", "", 8)
	pdf.SetXY(marginL, binY)
	pdf.CellFormat(leftW, 5, fmt.Sprintf("БИН: %s", settings.CompanyBIN), "LB", 0, "L", false, 0, "")
	pdf.CellFormat(midW, 5, "", "B", 0, "L", false, 0, "")
	pdf.CellFormat(rightW, 5, "", "BR", 1, "L", false, 0, "")

	// ===== НИЖНЯЯ СЕКЦИЯ: Банк + БИК + Код назначения =====
	// В образце нижняя секция имеет другие ширины: БИК уже, Код назначения шире
	bankLeftW := 105.0 // Банк бенефициара
	bankMidW := 35.0   // БИК
	bankRightW := 50.0 // Код назначения платежа

	bankHeaderY := pdf.GetY()

	// --- Строка заголовков банка ---
	pdf.SetFont("Arial", "B", 8)
	pdf.SetXY(marginL, bankHeaderY)
	pdf.CellFormat(bankLeftW, 5, "Банк бенефициара:", "LT", 0, "L", false, 0, "")
	pdf.CellFormat(bankMidW, 5, "БИК", "LT", 0, "C", false, 0, "")
	pdf.CellFormat(bankRightW, 5, "Код назначения платежа", "LTR", 1, "C", false, 0, "")

	// --- Строка данных банка ---
	pdf.SetFont("Arial", "", 8)
	bankDataY := pdf.GetY()
	pdf.SetXY(marginL, bankDataY)
	pdf.CellFormat(bankLeftW, 5, settings.BankName, "LB", 0, "L", false, 0, "")
	pdf.CellFormat(bankMidW, 5, settings.BankBIK, "LB", 0, "C", false, 0, "")
	pdf.CellFormat(bankRightW, 5, settings.PaymentCode, "LBR", 1, "C", false, 0, "")

	pdf.Ln(5)
}

// drawHeader — заголовок «Счет на оплату № NN от DD месяца YYYY г.»
func (g *PDFGenerator) drawHeader(pdf *fpdf.Fpdf, invoice *models.Invoice, settings *models.BillingSettings) {
	pdf.Ln(3)

	// Заголовок
	pdf.SetFont("Arial", "B", 14)
	// Номер счёта: если есть Number — используем его, иначе ID
	invoiceNumber := invoice.Number
	if invoiceNumber == "" {
		invoiceNumber = fmt.Sprintf("%d", invoice.ID)
	}
	title := fmt.Sprintf("Счет на оплату № %s от %s", invoiceNumber, formatDateRussian(invoice.CreatedAt))
	pdf.CellFormat(190, 10, title, "", 1, "L", false, 0, "")

	// Нижняя тонкая линия-разделитель
	y := pdf.GetY()
	pdf.SetLineWidth(0.3)
	pdf.Line(10, y, 200, y)
	pdf.Ln(4)

	// Восстанавливаем толщину линии
	pdf.SetLineWidth(0.2)
}

// drawSupplier — информация о поставщике в однострочном формате
func (g *PDFGenerator) drawSupplier(pdf *fpdf.Fpdf, settings *models.BillingSettings) {
	labelW := 25.0
	dataW := 165.0

	pdf.SetFont("Arial", "", 9)
	pdf.CellFormat(labelW, 5, "Поставщик:", "", 0, "L", false, 0, "")

	// Формируем строку реквизитов
	var parts []string
	if settings.CompanyBIN != "" {
		parts = append(parts, fmt.Sprintf("БИН / ИИН %s", settings.CompanyBIN))
	}
	if settings.CompanyName != "" {
		parts = append(parts, settings.CompanyName)
	}
	if settings.CompanyAddress != "" {
		parts = append(parts, settings.CompanyAddress)
	}
	if settings.CompanyPhone != "" {
		parts = append(parts, fmt.Sprintf("тел.: %s", settings.CompanyPhone))
	}

	pdf.SetFont("Arial", "B", 9)
	supplierText := strings.Join(parts, ", ")

	// MultiCell для переноса длинного текста
	pdf.MultiCell(dataW, 5, supplierText, "", "L", false)
	pdf.Ln(2)
}

// drawBuyer — информация о покупателе в однострочном формате
func (g *PDFGenerator) drawBuyer(pdf *fpdf.Fpdf, account *models.Account) {
	labelW := 25.0
	dataW := 165.0

	pdf.SetFont("Arial", "", 9)
	pdf.CellFormat(labelW, 5, "Покупатель:", "", 0, "L", false, 0, "")

	// Формируем строку реквизитов покупателя
	var parts []string
	buyerName := account.BuyerName
	if buyerName == "" {
		buyerName = account.Name
	}
	buyerBIN := account.BuyerBIN

	if buyerBIN != "" {
		parts = append(parts, fmt.Sprintf("БИН / ИИН %s", buyerBIN))
	}
	if buyerName != "" {
		parts = append(parts, buyerName)
	}
	if account.BuyerAddress != "" {
		parts = append(parts, account.BuyerAddress)
	}
	if account.BuyerPhone != "" {
		parts = append(parts, fmt.Sprintf("тел.: %s", account.BuyerPhone))
	}
	if account.BuyerEmail != "" {
		parts = append(parts, account.BuyerEmail)
	}

	pdf.SetFont("Arial", "B", 9)
	buyerText := strings.Join(parts, ", ")

	pdf.MultiCell(dataW, 5, buyerText, "", "L", false)
	pdf.Ln(2)
}

// drawContract — ссылка на договор
func (g *PDFGenerator) drawContract(pdf *fpdf.Fpdf, account *models.Account) {
	if account.ContractNumber == "" {
		return
	}

	labelW := 25.0

	pdf.SetFont("Arial", "", 9)
	pdf.CellFormat(labelW, 5, "Договор:", "", 0, "L", false, 0, "")

	contractDate := ""
	if account.ContractDate != nil {
		contractDate = fmt.Sprintf(" от %s", formatDateRussian(*account.ContractDate))
	}

	pdf.SetFont("Arial", "B", 9)
	pdf.CellFormat(165, 5, fmt.Sprintf("%s%s", account.ContractNumber, contractDate), "", 1, "L", false, 0, "")
	pdf.Ln(3)
}

// drawItemsTable — таблица позиций с 7 колонками
func (g *PDFGenerator) drawItemsTable(pdf *fpdf.Fpdf, invoice *models.Invoice) {
	// Ширины колонок (всего 190mm)
	colNum := 10.0   // №
	colCode := 25.0  // Код
	colName := 70.0  // Наименование
	colQty := 20.0   // Кол-во
	colUnit := 15.0  // Ед.
	colPrice := 25.0 // Цена
	colTotal := 25.0 // Сумма

	// Верхняя толстая линия таблицы
	y := pdf.GetY()
	pdf.SetLineWidth(0.6)
	pdf.Line(10, y, 200, y)
	pdf.SetLineWidth(0.2)

	// Заголовок таблицы
	pdf.SetFont("Arial", "B", 8)
	pdf.SetFillColor(255, 255, 255)

	// Рисуем ячейки заголовка: левая и правая рамки толстые
	pdf.CellFormat(colNum, 7, "№", "LTB", 0, "C", false, 0, "")
	pdf.CellFormat(colCode, 7, "Код", "1", 0, "C", false, 0, "")
	pdf.CellFormat(colName, 7, "Наименование", "1", 0, "C", false, 0, "")
	pdf.CellFormat(colQty, 7, "Кол-во", "1", 0, "C", false, 0, "")
	pdf.CellFormat(colUnit, 7, "Ед.", "1", 0, "C", false, 0, "")
	pdf.CellFormat(colPrice, 7, "Цена", "1", 0, "C", false, 0, "")
	pdf.CellFormat(colTotal, 7, "Сумма", "1", 1, "C", false, 0, "")

	// Позиции
	pdf.SetFont("Arial", "", 8)
	lineHeight := 5.0

	// Определяем месяц периода для описания позиций
	periodMonth := russianMonthForPeriod(invoice.Period.Month())

	for i, line := range invoice.Lines {
		startY := pdf.GetY()
		startX := pdf.GetX()

		// Формируем наименование с указанием месяца
		itemName := line.ModuleName
		// Убираем завершающий "/месяц" если есть, чтобы не дублировать
		itemName = strings.TrimSuffix(itemName, " /месяц")
		itemName = strings.TrimSuffix(itemName, "/месяц")
		// Добавляем " / месяц за {Месяц}" если ещё нет
		if !strings.Contains(strings.ToLower(itemName), "за "+strings.ToLower(periodMonth)) {
			itemName = fmt.Sprintf("%s / месяц за %s", itemName, periodMonth)
		}

		// Код модуля из настроек
		moduleCode := line.ModuleCode

		// Вычисляем высоту наименования
		lines := pdf.SplitText(itemName, colName-2)
		cellHeight := float64(len(lines)) * lineHeight
		if cellHeight < lineHeight {
			cellHeight = lineHeight
		}

		// № — порядковый номер
		pdf.CellFormat(colNum, cellHeight, fmt.Sprintf("%d", i+1), "1", 0, "C", false, 0, "")

		// Код — по центру
		pdf.CellFormat(colCode, cellHeight, moduleCode, "1", 0, "C", false, 0, "")

		// Наименование (с переносом)
		pdf.MultiCell(colName, lineHeight, itemName, "1", "L", false)

		// Возвращаемся и рисуем оставшиеся колонки
		pdf.SetXY(startX+colNum+colCode+colName, startY)

		// Кол-во — формат с тремя знаками через запятую
		qtyStr := formatQuantity(line.Quantity)
		pdf.CellFormat(colQty, cellHeight, qtyStr, "1", 0, "R", false, 0, "")

		// Единица измерения
		// Единица измерения из настроек модуля
		unitName := line.ModuleUnit
		if unitName == "" {
			unitName = "услуга"
		}
		pdf.CellFormat(colUnit, cellHeight, unitName, "1", 0, "C", false, 0, "")

		// Цена
		pdf.CellFormat(colPrice, cellHeight, formatMoney(line.UnitPrice), "1", 0, "R", false, 0, "")

		// Сумма
		pdf.CellFormat(colTotal, cellHeight, formatMoney(line.TotalPrice), "1", 1, "R", false, 0, "")
	}

	// Нижняя толстая линия таблицы
	y = pdf.GetY()
	pdf.SetLineWidth(0.6)
	pdf.Line(10, y, 200, y)
	pdf.SetLineWidth(0.2)
	pdf.Ln(2)
}

// drawTotals — итоги: Итого и НДС
func (g *PDFGenerator) drawTotals(pdf *fpdf.Fpdf, invoice *models.Invoice, settings *models.BillingSettings) {
	// Ширины колонок (выравниваем с таблицей)
	labelW := 165.0
	valueW := 25.0

	pdf.SetFont("Arial", "B", 9)

	// Итого
	pdf.CellFormat(labelW, 6, "Итого:", "", 0, "R", false, 0, "")
	pdf.CellFormat(valueW, 6, formatMoney(invoice.TotalAmount), "", 1, "R", false, 0, "")

	// НДС
	vatRate := settings.VATRate
	if vatRate == 0 {
		vatRate = 16 // по умолчанию 16% для Казахстана
	}
	vatAmount := invoice.TotalAmount * vatRate / (100 + vatRate)

	pdf.SetFont("Arial", "B", 9)
	pdf.CellFormat(labelW, 6, fmt.Sprintf("В том числе НДС:"), "", 0, "R", false, 0, "")
	pdf.CellFormat(valueW, 6, formatMoney(vatAmount), "", 1, "R", false, 0, "")

	pdf.Ln(3)
}

// drawAmountInWords — сумма прописью
func (g *PDFGenerator) drawAmountInWords(pdf *fpdf.Fpdf, invoice *models.Invoice) {
	lineCount := len(invoice.Lines)

	// «Всего наименований N, на сумму XXX KZT»
	pdf.SetFont("Arial", "", 9)
	summary := fmt.Sprintf("Всего наименований %d, на сумму %s %s",
		lineCount, formatMoney(invoice.TotalAmount), invoice.Currency)
	pdf.CellFormat(190, 5, summary, "", 1, "L", false, 0, "")

	// «Всего к оплате: Сумма прописью»
	pdf.SetFont("Arial", "B", 9)
	amountWords := AmountToWords(invoice.TotalAmount, invoice.Currency)
	pdf.MultiCell(190, 5, fmt.Sprintf("Всего к оплате: %s", amountWords), "", "L", false)

	// Горизонтальная линия-разделитель
	pdf.Ln(2)
	y := pdf.GetY()
	pdf.SetLineWidth(0.5)
	pdf.Line(10, y, 200, y)
	pdf.SetLineWidth(0.2)
	pdf.Ln(5)
}

// drawSignature — подпись исполнителя с изображениями подписи и печати
func (g *PDFGenerator) drawSignature(pdf *fpdf.Fpdf, settings *models.BillingSettings) {
	if settings.ExecutorName == "" {
		return
	}

	// Запоминаем Y-координату начала блока подписи
	blockY := pdf.GetY()

	pdf.SetFont("Arial", "B", 9)
	pdf.CellFormat(28, 6, "Исполнитель", "", 0, "L", false, 0, "")

	// Длинная линия для подписи (через большую часть ширины)
	lineY := pdf.GetY() + 6
	pdf.SetLineWidth(0.3)
	pdf.Line(38, lineY, 145, lineY)

	//  /ФИО/
	pdf.CellFormat(120, 6, "", "", 0, "L", false, 0, "")
	pdf.SetFont("Arial", "", 9)
	pdf.CellFormat(42, 6, fmt.Sprintf("/%s/", settings.ExecutorName), "", 1, "L", false, 0, "")

	pdf.SetLineWidth(0.2)

	// Вставка PNG подписи (если загружена)
	if settings.SignatureImage != "" {
		sigX := settings.SignatureX
		if sigX == 0 {
			sigX = 50
		}
		sigW := settings.SignatureW
		if sigW == 0 {
			sigW = 40
		}
		sigY := blockY + settings.SignatureY
		insertBase64Image(pdf, settings.SignatureImage, "signature_img", sigX, sigY, sigW)
	}

	// Вставка PNG печати (если загружена)
	if settings.StampImage != "" {
		stX := settings.StampX
		if stX == 0 {
			stX = 90
		}
		stW := settings.StampW
		if stW == 0 {
			stW = 30
		}
		stY := blockY + settings.StampY
		insertBase64Image(pdf, settings.StampImage, "stamp_img", stX, stY, stW)
	}
}

// insertBase64Image декодирует Base64 PNG и вставляет в PDF по координатам
func insertBase64Image(pdf *fpdf.Fpdf, base64Data string, name string, x, y, w float64) {
	// Убираем data URI префикс если есть (data:image/png;base64,...)
	if idx := strings.Index(base64Data, ","); idx >= 0 {
		base64Data = base64Data[idx+1:]
	}

	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return // Тихо пропускаем — не ломаем генерацию
	}

	reader := io.Reader(bytes.NewReader(data))
	opts := fpdf.ImageOptions{ImageType: "PNG", ReadDpi: true}
	pdf.RegisterImageOptionsReader(name, opts, reader)
	pdf.ImageOptions(name, x, y, w, 0, false, opts, 0, "")
}

// formatQuantity форматирует количество (например: 970,350 или 1,000)
func formatQuantity(qty float64) string {
	// Формат с тремя знаками после запятой
	whole := int64(qty)
	frac := int64(math.Round((qty - float64(whole)) * 1000))
	if frac < 0 {
		frac = -frac
	}

	sign := ""
	if whole < 0 {
		sign = "-"
		whole = -whole
	}

	str := fmt.Sprintf("%d", whole)
	n := len(str)
	if n > 3 {
		var result []byte
		for i, c := range str {
			if i > 0 && (n-i)%3 == 0 {
				result = append(result, ' ')
			}
			result = append(result, byte(c))
		}
		return fmt.Sprintf("%s%s,%03d", sign, string(result), frac)
	}
	return fmt.Sprintf("%s%s,%03d", sign, str, frac)
}

func formatMoney(amount float64) string {
	// Форматируем с разделителем тысяч (пробел) и десятичной запятой
	whole := int64(amount)
	frac := int64(math.Round((amount - float64(whole)) * 100))
	if frac < 0 {
		frac = -frac
	}

	// Форматируем целую часть с пробелами
	sign := ""
	if whole < 0 {
		sign = "-"
		whole = -whole
	}
	str := fmt.Sprintf("%d", whole)
	n := len(str)
	if n > 3 {
		var result []byte
		for i, c := range str {
			if i > 0 && (n-i)%3 == 0 {
				result = append(result, ' ')
			}
			result = append(result, byte(c))
		}
		return fmt.Sprintf("%s%s,%02d", sign, string(result), frac)
	}
	return fmt.Sprintf("%s%s,%02d", sign, str, frac)
}
