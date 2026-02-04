package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

// Client - клиент для работы с Gemini API
type Client struct {
	genaiClient *genai.Client
	model       *genai.GenerativeModel
	modelName   string
	enabled     bool
}

// NewClient создаёт новый клиент Gemini
func NewClient(ctx context.Context, apiKey, modelName string) (*Client, error) {
	if apiKey == "" {
		log.Println("[AI] API ключ не указан, AI клиент отключён")
		return &Client{enabled: false}, nil
	}

	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("ошибка создания Gemini клиента: %w", err)
	}

	model := client.GenerativeModel(modelName)

	// Настройка модели для JSON ответов
	model.ResponseMIMEType = "application/json"
	model.SetTemperature(0.3) // Более детерминированные ответы

	log.Printf("[AI] Клиент Gemini инициализирован, модель: %s", modelName)

	return &Client{
		genaiClient: client,
		model:       model,
		modelName:   modelName,
		enabled:     true,
	}, nil
}

// IsEnabled возвращает true если клиент активен
func (c *Client) IsEnabled() bool {
	return c.enabled && c.genaiClient != nil
}

// Close закрывает клиент
func (c *Client) Close() error {
	if c.genaiClient != nil {
		return c.genaiClient.Close()
	}
	return nil
}

// GenerateResponse отправляет запрос и получает ответ
type GenerateResult struct {
	Response     string
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// Generate отправляет запрос к Gemini и возвращает ответ
func (c *Client) Generate(ctx context.Context, systemPrompt, userPrompt string) (*GenerateResult, error) {
	if !c.IsEnabled() {
		return nil, fmt.Errorf("AI клиент не инициализирован")
	}

	// Устанавливаем системный промпт
	c.model.SystemInstruction = &genai.Content{
		Parts: []genai.Part{genai.Text(systemPrompt)},
	}

	// Отправляем запрос
	resp, err := c.model.GenerateContent(ctx, genai.Text(userPrompt))
	if err != nil {
		return nil, fmt.Errorf("ошибка генерации: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("пустой ответ от Gemini")
	}

	// Собираем текст ответа
	var responseText strings.Builder
	for _, part := range resp.Candidates[0].Content.Parts {
		if text, ok := part.(genai.Text); ok {
			responseText.WriteString(string(text))
		}
	}

	// Получаем статистику токенов
	result := &GenerateResult{
		Response: responseText.String(),
	}

	if resp.UsageMetadata != nil {
		result.InputTokens = int(resp.UsageMetadata.PromptTokenCount)
		result.OutputTokens = int(resp.UsageMetadata.CandidatesTokenCount)
		result.TotalTokens = int(resp.UsageMetadata.TotalTokenCount)
	}

	return result, nil
}

// InsightResponse - структура ответа от AI
type InsightResponse struct {
	Severity        string  `json:"severity"`
	Title           string  `json:"title"`
	Description     string  `json:"description"`
	FinancialImpact float64 `json:"financial_impact"`
	InsightType     string  `json:"insight_type"`
}

// ParseInsightResponse парсит JSON ответ от Gemini
func ParseInsightResponse(response string) (*InsightResponse, error) {
	var insight InsightResponse
	if err := json.Unmarshal([]byte(response), &insight); err != nil {
		return nil, fmt.Errorf("ошибка парсинга ответа AI: %w", err)
	}

	// Валидация severity
	switch insight.Severity {
	case "info", "warning", "critical":
		// OK
	default:
		insight.Severity = "info"
	}

	// Валидация insight_type
	switch insight.InsightType {
	case "churn_risk", "growth", "financial_impact":
		// OK
	default:
		insight.InsightType = "financial_impact"
	}

	return &insight, nil
}
