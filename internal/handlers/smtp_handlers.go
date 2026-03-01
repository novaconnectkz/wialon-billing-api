package handlers

import (
	"fmt"
	"net/http"
	"time"

	"log"

	"github.com/gin-gonic/gin"
	"github.com/user/wialon-billing-api/internal/models"
	"github.com/user/wialon-billing-api/internal/repository"
	"github.com/user/wialon-billing-api/internal/services/email"
	"github.com/user/wialon-billing-api/internal/services/invoice"
)

// SMTPHandler - обработчики для SMTP эндпоинтов
type SMTPHandler struct {
	repo           *repository.Repository
	emailService   *email.Service
	invoiceService *invoice.Service
	pdfGenerator   *invoice.PDFGenerator
}

// NewSMTPHandler создаёт новый обработчик SMTP
func NewSMTPHandler(repo *repository.Repository, emailService *email.Service, invoiceService *invoice.Service) *SMTPHandler {
	return &SMTPHandler{
		repo:           repo,
		emailService:   emailService,
		invoiceService: invoiceService,
		pdfGenerator:   invoice.NewPDFGenerator(),
	}
}

// GetSMTPSettings возвращает настройки SMTP (без пароля)
func (h *SMTPHandler) GetSMTPSettings(c *gin.Context) {
	settings, err := h.repo.GetSMTPSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if settings == nil {
		// Дефолтные настройки
		c.JSON(http.StatusOK, gin.H{
			"enabled":      false,
			"host":         "",
			"port":         587,
			"username":     "",
			"from_email":   "",
			"from_name":    "",
			"use_tls":      true,
			"has_password": false,
			"copy_email":   "",
			"copy_enabled": false,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":           settings.ID,
		"enabled":      settings.Enabled,
		"host":         settings.Host,
		"port":         settings.Port,
		"username":     settings.Username,
		"from_email":   settings.FromEmail,
		"from_name":    settings.FromName,
		"use_tls":      settings.UseTLS,
		"has_password": settings.EncryptedPassword != "",
		"copy_email":   settings.CopyEmail,
		"copy_enabled": settings.CopyEnabled,
		"updated_at":   settings.UpdatedAt,
	})
}

// UpdateSMTPSettings сохраняет настройки SMTP
func (h *SMTPHandler) UpdateSMTPSettings(c *gin.Context) {
	var req struct {
		Enabled     bool   `json:"enabled"`
		Host        string `json:"host"`
		Port        int    `json:"port"`
		Username    string `json:"username"`
		Password    string `json:"password"` // Новый пароль (если передан)
		FromEmail   string `json:"from_email"`
		FromName    string `json:"from_name"`
		UseTLS      bool   `json:"use_tls"`
		CopyEmail   string `json:"copy_email"`
		CopyEnabled bool   `json:"copy_enabled"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	settings, err := h.repo.GetSMTPSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if settings == nil {
		settings = &models.SMTPSettings{}
	}

	settings.Enabled = req.Enabled
	settings.Host = req.Host
	settings.Port = req.Port
	settings.Username = req.Username
	settings.FromEmail = req.FromEmail
	settings.FromName = req.FromName
	settings.UseTLS = req.UseTLS
	settings.CopyEmail = req.CopyEmail
	settings.CopyEnabled = req.CopyEnabled

	// Шифруем пароль только если передан новый
	if req.Password != "" {
		encrypted, err := email.Encrypt(req.Password)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка шифрования пароля"})
			return
		}
		settings.EncryptedPassword = encrypted
	}

	if err := h.repo.SaveSMTPSettings(settings); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Настройки SMTP сохранены"})
}

// TestSMTPConnection отправляет тестовое письмо
func (h *SMTPHandler) TestSMTPConnection(c *gin.Context) {
	if err := h.emailService.TestConnection(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Автоматически включаем SMTP после успешного теста
	settings, err := h.repo.GetSMTPSettings()
	if err == nil && settings != nil && !settings.Enabled {
		settings.Enabled = true
		if saveErr := h.repo.SaveSMTPSettings(settings); saveErr != nil {
			log.Printf("[SMTP] Ошибка автовключения: %v", saveErr)
		} else {
			log.Printf("[SMTP] SMTP автоматически включён после успешного теста")
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "Тестовое письмо отправлено"})
}

// GetEmailTemplates возвращает все шаблоны писем
func (h *SMTPHandler) GetEmailTemplates(c *gin.Context) {
	templates, err := h.repo.GetEmailTemplates()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, templates)
}

// GetEmailTemplate возвращает шаблон по типу
func (h *SMTPHandler) GetEmailTemplate(c *gin.Context) {
	templateType := c.Param("type")
	tmpl, err := h.repo.GetEmailTemplateByType(templateType)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if tmpl == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Шаблон не найден"})
		return
	}
	c.JSON(http.StatusOK, tmpl)
}

// UpdateEmailTemplate обновляет шаблон письма
func (h *SMTPHandler) UpdateEmailTemplate(c *gin.Context) {
	templateType := c.Param("type")

	var req struct {
		Name     string `json:"name"`
		Subject  string `json:"subject"`
		HTMLBody string `json:"html_body"`
		IsActive bool   `json:"is_active"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tmpl, err := h.repo.GetEmailTemplateByType(templateType)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if tmpl == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Шаблон не найден"})
		return
	}

	tmpl.Name = req.Name
	tmpl.Subject = req.Subject
	tmpl.HTMLBody = req.HTMLBody
	tmpl.IsActive = req.IsActive

	if err := h.repo.SaveEmailTemplate(tmpl); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Шаблон обновлён"})
}

// PreviewEmailTemplate рендерит превью шаблона
func (h *SMTPHandler) PreviewEmailTemplate(c *gin.Context) {
	templateType := c.Param("type")

	var vars map[string]string
	if err := c.ShouldBindJSON(&vars); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	subject, body, err := h.emailService.RenderPreview(templateType, vars)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"subject": subject,
		"body":    body,
	})
}

// SendInvoiceEmail отправляет счёт по email
func (h *SMTPHandler) SendInvoiceEmail(c *gin.Context) {
	idStr := c.Param("id")
	var id uint
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный ID счёта"})
		return
	}

	// Получаем счёт с аккаунтом
	inv, err := h.repo.GetInvoiceByID(id)
	if err != nil || inv == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Счёт не найден"})
		return
	}

	// Проверяем email покупателя
	if inv.Account.BuyerEmail == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Email покупателя не указан в реквизитах аккаунта"})
		return
	}

	// Получаем настройки биллинга
	billingSettings, err := h.repo.GetSettings()
	if err != nil || billingSettings == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Настройки биллинга не найдены"})
		return
	}

	// Генерируем PDF
	pdfData, err := h.pdfGenerator.GenerateInvoicePDF(inv, billingSettings, &inv.Account)
	if err != nil {
		log.Printf("[EMAIL] Ошибка генерации PDF для счёта %d: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка генерации PDF"})
		return
	}

	// Отправляем клиенту (только PDF, без Excel-отчёта)
	if err := h.emailService.SendInvoice(inv.Account.BuyerEmail, inv, pdfData); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка отправки: " + err.Error()})
		return
	}

	// Отправляем копию если включено
	smtpSettings, _ := h.repo.GetSMTPSettings()
	if smtpSettings != nil && smtpSettings.CopyEnabled && smtpSettings.CopyEmail != "" {
		go func() {
			if err := h.emailService.SendInvoice(smtpSettings.CopyEmail, inv, pdfData); err != nil {
				log.Printf("[EMAIL] Ошибка отправки копии на %s: %v", smtpSettings.CopyEmail, err)
			} else {
				log.Printf("[EMAIL] Копия счёта отправлена на %s", smtpSettings.CopyEmail)
			}
		}()
	}

	// Обновляем статус счёта на 'sent'
	inv.Status = "sent"
	now := time.Now()
	if inv.SentAt == nil {
		inv.SentAt = &now
	}
	if err := h.repo.UpdateInvoice(inv); err != nil {
		log.Printf("[EMAIL] Письмо отправлено, но ошибка обновления статуса счёта %d: %v", id, err)
	}

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("Счёт отправлен на %s", inv.Account.BuyerEmail)})
}
