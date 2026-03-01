package email

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"mime/multipart"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"github.com/user/wialon-billing-api/internal/models"
	"github.com/user/wialon-billing-api/internal/repository"
)

// loginAuth реализует SMTP AUTH LOGIN (не поддерживается стандартной библиотекой Go)
type loginAuth struct {
	username, password string
}

func LoginAuth(username, password string) smtp.Auth {
	return &loginAuth{username, password}
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if more {
		challenge := strings.TrimSpace(strings.ToLower(string(fromServer)))
		switch {
		case strings.Contains(challenge, "username"), strings.Contains(challenge, "login"):
			return []byte(a.username), nil
		case strings.Contains(challenge, "password"):
			return []byte(a.password), nil
		default:
			return nil, errors.New("неизвестный запрос SMTP LOGIN: " + string(fromServer))
		}
	}
	return nil, nil
}

// Attachment - вложение к письму
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// Service - сервис отправки email
type Service struct {
	repo *repository.Repository
}

// NewService создаёт новый email-сервис
func NewService(repo *repository.Repository) *Service {
	return &Service{repo: repo}
}

// SendOTP отправляет OTP-код на email используя шаблон "otp"
func (s *Service) SendOTP(to, code string) error {
	tmpl, err := s.repo.GetEmailTemplateByType("otp")
	if err != nil || tmpl == nil {
		// Если шаблон не найден — используем простой текст
		subject := fmt.Sprintf("Код авторизации: %s", code)
		body := fmt.Sprintf("<p>Ваш код авторизации: <strong>%s</strong></p><p>Код действителен 5 минут.</p>", code)
		return s.send(to, subject, body)
	}

	vars := map[string]string{
		"code":            code,
		"email":           to,
		"expires_minutes": "5",
	}

	subject := renderTemplate(tmpl.Subject, vars)
	body := renderTemplate(tmpl.HTMLBody, vars)
	return s.send(to, subject, body)
}

// formatPeriodRu форматирует дату как "за февраль 2026"
func formatPeriodRu(t time.Time) string {
	months := []string{
		"", "январь", "февраль", "март", "апрель", "май", "июнь",
		"июль", "август", "сентябрь", "октябрь", "ноябрь", "декабрь",
	}
	return fmt.Sprintf("%s %d", months[t.Month()], t.Year())
}

// SendInvoice отправляет счёт с PDF-вложением
func (s *Service) SendInvoice(to string, invoice *models.Invoice, pdfData []byte) error {
	periodStr := formatPeriodRu(invoice.Period)

	// Номер счёта: если есть Number — используем его, иначе ID
	invoiceNumber := invoice.Number
	if invoiceNumber == "" {
		invoiceNumber = fmt.Sprintf("%d", invoice.ID)
	}

	tmpl, err := s.repo.GetEmailTemplateByType("invoice")
	if err != nil || tmpl == nil {
		// Фоллбэк без шаблона
		subject := fmt.Sprintf("Счёт на оплату №%s за %s", invoiceNumber, periodStr)
		body := fmt.Sprintf("<p>Во вложении счёт на оплату на сумму %.2f %s.</p>", invoice.TotalAmount, invoice.Currency)
		attachment := Attachment{
			Filename:    fmt.Sprintf("invoice_%s.pdf", invoice.Period.Format("2006_01")),
			ContentType: "application/pdf",
			Data:        pdfData,
		}
		return s.sendWithAttachments(to, subject, body, attachment)
	}

	vars := map[string]string{
		"company_name":   invoice.Account.Name,
		"period":         periodStr,
		"amount":         fmt.Sprintf("%.2f", invoice.TotalAmount),
		"currency":       invoice.Currency,
		"invoice_number": invoiceNumber,
	}

	subject := renderTemplate(tmpl.Subject, vars)
	body := renderTemplate(tmpl.HTMLBody, vars)
	attachment := Attachment{
		Filename:    fmt.Sprintf("invoice_%s.pdf", invoice.Period.Format("2006_01")),
		ContentType: "application/pdf",
		Data:        pdfData,
	}
	return s.sendWithAttachments(to, subject, body, attachment)
}

// SendNotification отправляет уведомление
func (s *Service) SendNotification(to, title, message string) error {
	tmpl, err := s.repo.GetEmailTemplateByType("notification")
	if err != nil || tmpl == nil {
		return s.send(to, title, fmt.Sprintf("<p>%s</p>", message))
	}

	vars := map[string]string{
		"title":   title,
		"message": message,
		"date":    "", // Заполняется автоматически
	}

	subject := renderTemplate(tmpl.Subject, vars)
	body := renderTemplate(tmpl.HTMLBody, vars)
	return s.send(to, subject, body)
}

// TestConnection отправляет тестовое письмо для проверки SMTP
func (s *Service) TestConnection() error {
	settings, err := s.repo.GetSMTPSettings()
	if err != nil {
		return fmt.Errorf("не удалось получить SMTP настройки: %w", err)
	}
	if settings == nil {
		return fmt.Errorf("SMTP настройки не найдены")
	}

	password, err := Decrypt(settings.EncryptedPassword)
	if err != nil {
		return fmt.Errorf("ошибка расшифровки пароля: %w", err)
	}

	client, err := s.connectAndAuth(settings, password)
	if err != nil {
		return err
	}
	defer client.Close()

	// Тестовое письмо
	subject := "Тест SMTP подключения"
	body := "<h2>✅ SMTP работает!</h2><p>Это тестовое письмо от Wialon Billing System.</p>"
	return s.sendMessage(client, settings, settings.FromEmail, subject, body, nil)
}

// IsEnabled проверяет включён ли SMTP
func (s *Service) IsEnabled() bool {
	settings, err := s.repo.GetSMTPSettings()
	if err != nil || settings == nil {
		return false
	}
	return settings.Enabled
}

// RenderPreview рендерит превью шаблона с тестовыми данными
func (s *Service) RenderPreview(templateType string, vars map[string]string) (string, string, error) {
	tmpl, err := s.repo.GetEmailTemplateByType(templateType)
	if err != nil {
		return "", "", fmt.Errorf("шаблон не найден: %w", err)
	}
	if tmpl == nil {
		return "", "", fmt.Errorf("шаблон типа '%s' не найден", templateType)
	}

	subject := renderTemplate(tmpl.Subject, vars)
	body := renderTemplate(tmpl.HTMLBody, vars)
	return subject, body, nil
}

// send отправляет простое HTML-письмо
func (s *Service) send(to, subject, htmlBody string) error {
	return s.sendWithAttachments(to, subject, htmlBody)
}

// connectAndAuth подключается к SMTP и авторизуется (LOGIN → переподключение → PLAIN)
func (s *Service) connectAndAuth(settings *models.SMTPSettings, password string) (*smtp.Client, error) {
	addr := net.JoinHostPort(settings.Host, fmt.Sprintf("%d", settings.Port))

	// Попытка 1: LOGIN auth
	client, err := s.dial(addr, settings)
	if err != nil {
		return nil, err
	}

	loginA := LoginAuth(settings.Username, password)
	if err := client.Auth(loginA); err != nil {
		log.Printf("[EMAIL] LOGIN auth не удался: %v, пробуем PLAIN...", err)
		client.Close()

		// Попытка 2: новое соединение + PLAIN auth
		client, err = s.dial(addr, settings)
		if err != nil {
			return nil, err
		}

		plainA := smtp.PlainAuth("", settings.Username, password, settings.Host)
		if err := client.Auth(plainA); err != nil {
			client.Close()
			return nil, fmt.Errorf("ошибка авторизации SMTP (LOGIN и PLAIN не сработали): %w", err)
		}
	}

	return client, nil
}

// dial устанавливает TCP-соединение, создаёт SMTP-клиент и делает STARTTLS
func (s *Service) dial(addr string, settings *models.SMTPSettings) (*smtp.Client, error) {
	conn, err := net.DialTimeout("tcp", addr, 10e9)
	if err != nil {
		return nil, fmt.Errorf("не удалось подключиться к SMTP %s: %w", addr, err)
	}

	client, err := smtp.NewClient(conn, settings.Host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ошибка SMTP клиента: %w", err)
	}

	if settings.UseTLS {
		tlsConfig := &tls.Config{ServerName: settings.Host}
		if err := client.StartTLS(tlsConfig); err != nil {
			client.Close()
			return nil, fmt.Errorf("ошибка STARTTLS: %w", err)
		}
	}

	return client, nil
}

// sendWithAttachments отправляет письмо с опциональными вложениями
func (s *Service) sendWithAttachments(to, subject, htmlBody string, attachments ...Attachment) error {
	settings, err := s.repo.GetSMTPSettings()
	if err != nil || settings == nil {
		return fmt.Errorf("SMTP не настроен")
	}
	if !settings.Enabled {
		log.Printf("[EMAIL] SMTP отключён, пропускаем отправку на %s", to)
		return nil
	}

	password, err := Decrypt(settings.EncryptedPassword)
	if err != nil {
		return fmt.Errorf("ошибка расшифровки пароля: %w", err)
	}

	client, err := s.connectAndAuth(settings, password)
	if err != nil {
		return err
	}
	defer client.Close()

	return s.sendMessage(client, settings, to, subject, htmlBody, attachments)
}

// sendMessage формирует и отправляет MIME-сообщение
func (s *Service) sendMessage(client *smtp.Client, settings *models.SMTPSettings, to, subject, htmlBody string, attachments []Attachment) error {
	from := settings.FromEmail

	if err := client.Mail(from); err != nil {
		return fmt.Errorf("ошибка MAIL FROM: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("ошибка RCPT TO: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("ошибка DATA: %w", err)
	}
	defer w.Close()

	var buf bytes.Buffer

	if len(attachments) == 0 {
		// Простое HTML-письмо
		buf.WriteString(fmt.Sprintf("From: %s <%s>\r\n", settings.FromName, from))
		buf.WriteString(fmt.Sprintf("To: %s\r\n", to))
		buf.WriteString(fmt.Sprintf("Subject: =?utf-8?B?%s?=\r\n", base64.StdEncoding.EncodeToString([]byte(subject))))
		buf.WriteString("MIME-Version: 1.0\r\n")
		buf.WriteString("Content-Type: text/html; charset=\"utf-8\"\r\n")
		buf.WriteString("\r\n")
		buf.WriteString(htmlBody)
	} else {
		// MIME с вложениями
		writer := multipart.NewWriter(&buf)
		boundary := writer.Boundary()

		buf.Reset()
		buf.WriteString(fmt.Sprintf("From: %s <%s>\r\n", settings.FromName, from))
		buf.WriteString(fmt.Sprintf("To: %s\r\n", to))
		buf.WriteString(fmt.Sprintf("Subject: =?utf-8?B?%s?=\r\n", base64.StdEncoding.EncodeToString([]byte(subject))))
		buf.WriteString("MIME-Version: 1.0\r\n")
		buf.WriteString(fmt.Sprintf("Content-Type: multipart/mixed; boundary=\"%s\"\r\n", boundary))
		buf.WriteString("\r\n")

		// HTML-часть
		buf.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		buf.WriteString("Content-Type: text/html; charset=\"utf-8\"\r\n")
		buf.WriteString("\r\n")
		buf.WriteString(htmlBody)
		buf.WriteString("\r\n")

		// Вложения
		for _, att := range attachments {
			header := make(textproto.MIMEHeader)
			header.Set("Content-Type", att.ContentType)
			header.Set("Content-Transfer-Encoding", "base64")
			header.Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", att.Filename))

			buf.WriteString(fmt.Sprintf("--%s\r\n", boundary))
			for k, v := range header {
				buf.WriteString(fmt.Sprintf("%s: %s\r\n", k, v[0]))
			}
			buf.WriteString("\r\n")
			buf.WriteString(base64.StdEncoding.EncodeToString(att.Data))
			buf.WriteString("\r\n")
		}

		buf.WriteString(fmt.Sprintf("--%s--\r\n", boundary))
	}

	_, err = w.Write(buf.Bytes())
	if err != nil {
		return fmt.Errorf("ошибка записи данных: %w", err)
	}

	log.Printf("[EMAIL] Письмо отправлено на %s: %s", to, subject)
	return nil
}

// renderTemplate заменяет {{переменные}} в шаблоне на значения
func renderTemplate(template string, vars map[string]string) string {
	result := template
	for key, value := range vars {
		result = strings.ReplaceAll(result, "{{"+key+"}}", value)
	}
	return result
}
