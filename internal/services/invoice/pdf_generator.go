package invoice

import (
	"bytes"
	"fmt"
	"math"
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

// GenerateInvoicePDF генерирует PDF счёта
func (g *PDFGenerator) GenerateInvoicePDF(invoice *models.Invoice, settings *models.BillingSettings, account *models.Account) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(15, 15, 15)
	pdf.AddPage()

	// Шрифты с поддержкой кириллицы
	fontRegular := "./fonts/DejaVuSans.ttf"
	fontBold := "./fonts/DejaVuSans-Bold.ttf"
	pdf.AddUTF8Font("DejaVu", "", fontRegular)
	pdf.AddUTF8Font("DejaVu", "B", fontBold)

	// Заголовок
	g.drawHeader(pdf, invoice, settings)

	// Реквизиты поставщика
	g.drawSupplier(pdf, settings)

	// Реквизиты покупателя
	g.drawBuyer(pdf, account)

	// Таблица позиций
	g.drawItemsTable(pdf, invoice)

	// Итоги
	g.drawTotals(pdf, invoice, settings)

	// Подпись
	g.drawSignature(pdf, settings)

	// Генерируем PDF в буфер
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func (g *PDFGenerator) drawHeader(pdf *fpdf.Fpdf, invoice *models.Invoice, settings *models.BillingSettings) {
	pdf.SetFont("DejaVu", "B", 16)
	pdf.CellFormat(180, 10, fmt.Sprintf("СЧЁТ НА ОПЛАТУ № %d", invoice.ID), "", 1, "C", false, 0, "")

	pdf.SetFont("DejaVu", "", 10)
	pdf.CellFormat(180, 6, fmt.Sprintf("от %s", invoice.Period.Format("02.01.2006")), "", 1, "C", false, 0, "")
	pdf.Ln(5)
}

func (g *PDFGenerator) drawSupplier(pdf *fpdf.Fpdf, settings *models.BillingSettings) {
	pdf.SetFont("DejaVu", "B", 11)
	pdf.CellFormat(180, 7, "ПОСТАВЩИК:", "", 1, "L", false, 0, "")

	pdf.SetFont("DejaVu", "", 10)
	pdf.CellFormat(180, 5, settings.CompanyName, "", 1, "L", false, 0, "")
	pdf.CellFormat(180, 5, fmt.Sprintf("БИН: %s", settings.CompanyBIN), "", 1, "L", false, 0, "")
	pdf.CellFormat(180, 5, fmt.Sprintf("Адрес: %s", settings.CompanyAddress), "", 1, "L", false, 0, "")
	pdf.CellFormat(180, 5, fmt.Sprintf("Тел: %s", settings.CompanyPhone), "", 1, "L", false, 0, "")
	pdf.Ln(2)

	// Банковские реквизиты
	pdf.CellFormat(180, 5, fmt.Sprintf("ИИК: %s", settings.BankIIK), "", 1, "L", false, 0, "")
	pdf.CellFormat(180, 5, fmt.Sprintf("БИК: %s | Банк: %s", settings.BankBIK, settings.BankName), "", 1, "L", false, 0, "")
	pdf.CellFormat(180, 5, fmt.Sprintf("Кбе: %s | Код платежа: %s", settings.BankKbe, settings.PaymentCode), "", 1, "L", false, 0, "")
	pdf.Ln(5)

	// Линия разделитель
	pdf.Line(15, pdf.GetY(), 195, pdf.GetY())
	pdf.Ln(3)
}

func (g *PDFGenerator) drawBuyer(pdf *fpdf.Fpdf, account *models.Account) {
	pdf.SetFont("DejaVu", "B", 11)
	pdf.CellFormat(180, 7, "ПОКУПАТЕЛЬ:", "", 1, "L", false, 0, "")

	pdf.SetFont("DejaVu", "", 10)
	if account.BuyerName != "" {
		pdf.CellFormat(180, 5, account.BuyerName, "", 1, "L", false, 0, "")
	} else {
		pdf.CellFormat(180, 5, account.Name, "", 1, "L", false, 0, "")
	}

	if account.BuyerBIN != "" {
		pdf.CellFormat(180, 5, fmt.Sprintf("БИН: %s", account.BuyerBIN), "", 1, "L", false, 0, "")
	}
	if account.BuyerAddress != "" {
		pdf.CellFormat(180, 5, fmt.Sprintf("Адрес: %s", account.BuyerAddress), "", 1, "L", false, 0, "")
	}
	if account.ContractNumber != "" {
		contractDate := ""
		if account.ContractDate != nil {
			contractDate = account.ContractDate.Format("02.01.2006")
		}
		pdf.CellFormat(180, 5, fmt.Sprintf("Договор № %s от %s", account.ContractNumber, contractDate), "", 1, "L", false, 0, "")
	}
	pdf.Ln(5)

	// Линия разделитель
	pdf.Line(15, pdf.GetY(), 195, pdf.GetY())
	pdf.Ln(3)
}

func (g *PDFGenerator) drawItemsTable(pdf *fpdf.Fpdf, invoice *models.Invoice) {
	// Заголовок таблицы
	pdf.SetFont("DejaVu", "B", 9)
	pdf.SetFillColor(240, 240, 240)

	pdf.CellFormat(10, 7, "№", "1", 0, "C", true, 0, "")
	pdf.CellFormat(90, 7, "Наименование", "1", 0, "C", true, 0, "")
	pdf.CellFormat(20, 7, "Кол-во", "1", 0, "C", true, 0, "")
	pdf.CellFormat(25, 7, "Цена", "1", 0, "C", true, 0, "")
	pdf.CellFormat(35, 7, "Сумма", "1", 1, "C", true, 0, "")

	// Позиции
	pdf.SetFont("DejaVu", "", 9)
	lineHeight := 6.0

	for i, line := range invoice.Lines {
		// Сохраняем начальную позицию
		startY := pdf.GetY()
		startX := pdf.GetX()

		// Вычисляем высоту наименования (сколько строк займёт текст)
		lines := pdf.SplitText(line.ModuleName, 90)
		cellHeight := float64(len(lines)) * lineHeight
		if cellHeight < lineHeight {
			cellHeight = lineHeight
		}

		// Рисуем все ячейки с одинаковой высотой
		pdf.CellFormat(10, cellHeight, fmt.Sprintf("%d", i+1), "1", 0, "C", false, 0, "")

		// Наименование с переносом
		pdf.MultiCell(90, lineHeight, line.ModuleName, "1", "L", false)

		// Возвращаемся на позицию после наименования и рисуем остальные ячейки
		pdf.SetXY(startX+100, startY)
		pdf.CellFormat(20, cellHeight, fmt.Sprintf("%.0f", line.Quantity), "1", 0, "C", false, 0, "")
		pdf.CellFormat(25, cellHeight, formatMoney(line.UnitPrice), "1", 0, "R", false, 0, "")
		pdf.CellFormat(35, cellHeight, formatMoney(line.TotalPrice), "1", 1, "R", false, 0, "")
	}
	pdf.Ln(3)
}

func (g *PDFGenerator) drawTotals(pdf *fpdf.Fpdf, invoice *models.Invoice, settings *models.BillingSettings) {
	pdf.SetFont("DejaVu", "B", 10)

	// Вычисляем НДС
	vatRate := settings.VATRate
	if vatRate == 0 {
		vatRate = 16 // По умолчанию 16% для Казахстана
	}
	vatAmount := invoice.TotalAmount * vatRate / (100 + vatRate)

	// Итого
	pdf.CellFormat(145, 6, "Итого:", "", 0, "R", false, 0, "")
	pdf.CellFormat(35, 6, formatMoney(invoice.TotalAmount)+" "+invoice.Currency, "", 1, "R", false, 0, "")

	pdf.SetFont("DejaVu", "", 10)
	pdf.CellFormat(145, 6, fmt.Sprintf("в т.ч. НДС (%.0f%%):", vatRate), "", 0, "R", false, 0, "")
	pdf.CellFormat(35, 6, formatMoney(vatAmount)+" "+invoice.Currency, "", 1, "R", false, 0, "")

	pdf.SetFont("DejaVu", "B", 11)
	pdf.CellFormat(145, 7, "ВСЕГО К ОПЛАТЕ:", "", 0, "R", false, 0, "")
	pdf.CellFormat(35, 7, formatMoney(invoice.TotalAmount)+" "+invoice.Currency, "", 1, "R", false, 0, "")

	pdf.SetFont("DejaVu", "", 9)
	pdf.CellFormat(180, 5, "(НДС включён в стоимость)", "", 1, "R", false, 0, "")
	pdf.Ln(10)
}

func (g *PDFGenerator) drawSignature(pdf *fpdf.Fpdf, settings *models.BillingSettings) {
	pdf.SetFont("DejaVu", "", 10)
	if settings.ExecutorName != "" {
		pdf.CellFormat(50, 6, "Исполнитель:", "", 0, "L", false, 0, "")
		pdf.CellFormat(80, 6, settings.ExecutorName, "", 0, "L", false, 0, "")
		pdf.CellFormat(50, 6, "______________", "", 1, "L", false, 0, "")
	}

	// Дата генерации
	pdf.Ln(10)
	pdf.SetFont("DejaVu", "", 8)
	pdf.SetTextColor(128, 128, 128)
	pdf.CellFormat(180, 5, fmt.Sprintf("Документ сформирован: %s", time.Now().Format("02.01.2006 15:04")), "", 1, "R", false, 0, "")
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
