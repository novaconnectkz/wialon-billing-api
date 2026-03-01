package invoice

import (
	"fmt"
	"math"
	"strings"
)

// AmountToWords конвертирует числовую сумму в текст на русском языке
// Поддерживает KZT (тенге/тиын) и RUB (рублей/копеек)
func AmountToWords(amount float64, currency string) string {
	whole := int64(math.Abs(amount))
	frac := int64(math.Round((math.Abs(amount) - float64(whole)) * 100))

	wholeWords := numberToWords(whole, getCurrencyGender(currency))
	wholeCurrency := getCurrencyName(whole, currency)
	fracStr := fmt.Sprintf("%02d", frac)
	fracCurrency := getFractionName(frac, currency)

	result := capitalize(wholeWords) + " " + wholeCurrency + " " + fracStr + " " + fracCurrency

	return result
}

// getCurrencyGender возвращает род валюты (0 — мужской, 1 — женский)
func getCurrencyGender(currency string) int {
	switch strings.ToUpper(currency) {
	case "RUB":
		return 0 // рубль — мужской род
	default:
		return 0 // тенге — мужской род
	}
}

// getCurrencyName возвращает название валюты с правильным склонением
func getCurrencyName(n int64, currency string) string {
	switch strings.ToUpper(currency) {
	case "RUB":
		return declension(n, "рубль", "рубля", "рублей")
	default: // KZT
		return "тенге" // тенге не склоняется
	}
}

// getFractionName возвращает название дробной части валюты
func getFractionName(n int64, currency string) string {
	switch strings.ToUpper(currency) {
	case "RUB":
		return declension(n, "копейка", "копейки", "копеек")
	default: // KZT
		return "тиын" // тиын не склоняется
	}
}

// declension выбирает правильное склонение для числа
func declension(n int64, form1, form2, form5 string) string {
	abs := n % 100
	if abs >= 11 && abs <= 19 {
		return form5
	}
	switch abs % 10 {
	case 1:
		return form1
	case 2, 3, 4:
		return form2
	default:
		return form5
	}
}

// capitalize делает первую букву заглавной
func capitalize(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
	return string(runes)
}

// numberToWords преобразует число в текст на русском языке
// gender: 0 — мужской, 1 — женский
func numberToWords(n int64, gender int) string {
	if n == 0 {
		return "ноль"
	}

	var parts []string

	// Миллиарды
	if n >= 1000000000 {
		billions := n / 1000000000
		parts = append(parts, hundredsToWords(billions, 0)+" "+declension(billions, "миллиард", "миллиарда", "миллиардов"))
		n %= 1000000000
	}

	// Миллионы
	if n >= 1000000 {
		millions := n / 1000000
		parts = append(parts, hundredsToWords(millions, 0)+" "+declension(millions, "миллион", "миллиона", "миллионов"))
		n %= 1000000
	}

	// Тысячи (тысяча — женский род)
	if n >= 1000 {
		thousands := n / 1000
		parts = append(parts, hundredsToWords(thousands, 1)+" "+declension(thousands, "тысяча", "тысячи", "тысяч"))
		n %= 1000
	}

	// Остаток (единицы/десятки/сотни)
	if n > 0 {
		parts = append(parts, hundredsToWords(n, gender))
	}

	return strings.Join(parts, " ")
}

// hundredsToWords конвертирует число от 1 до 999 в текст
// gender: 0 — мужской, 1 — женский
func hundredsToWords(n int64, gender int) string {
	hundreds := []string{
		"", "сто", "двести", "триста", "четыреста",
		"пятьсот", "шестьсот", "семьсот", "восемьсот", "девятьсот",
	}

	tens := []string{
		"", "", "двадцать", "тридцать", "сорок",
		"пятьдесят", "шестьдесят", "семьдесят", "восемьдесят", "девяносто",
	}

	teens := []string{
		"десять", "одиннадцать", "двенадцать", "тринадцать", "четырнадцать",
		"пятнадцать", "шестнадцать", "семнадцать", "восемнадцать", "девятнадцать",
	}

	unitsMale := []string{
		"", "один", "два", "три", "четыре",
		"пять", "шесть", "семь", "восемь", "девять",
	}

	unitsFemale := []string{
		"", "одна", "две", "три", "четыре",
		"пять", "шесть", "семь", "восемь", "девять",
	}

	var parts []string

	// Сотни
	if n >= 100 {
		parts = append(parts, hundreds[n/100])
		n %= 100
	}

	// Десятки и единицы
	if n >= 10 && n <= 19 {
		parts = append(parts, teens[n-10])
	} else {
		if n >= 20 {
			parts = append(parts, tens[n/10])
			n %= 10
		}
		if n > 0 {
			if gender == 1 {
				parts = append(parts, unitsFemale[n])
			} else {
				parts = append(parts, unitsMale[n])
			}
		}
	}

	return strings.Join(parts, " ")
}
