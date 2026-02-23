package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	// DeepSeek API endpoint (OpenAI-совместимый)
	DefaultBaseURL = "https://api.deepseek.com"

	// Модели DeepSeek
	ModelReasonerR1 = "deepseek-reasoner" // Для сложных вычислений (Chain of Thought)
	ModelChatV3     = "deepseek-chat"     // Для быстрых ответов
)

// Client - клиент для работы с DeepSeek API
type Client struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
	maxTokens  int
	enabled    bool
}

// NewClient создаёт новый клиент DeepSeek
func NewClient(ctx context.Context, apiKey string, maxTokens int) (*Client, error) {
	if apiKey == "" {
		log.Println("[AI] API ключ не указан, AI клиент отключён")
		return &Client{enabled: false}, nil
	}

	if maxTokens <= 0 {
		maxTokens = 2500
	}

	log.Printf("[AI] Клиент DeepSeek инициализирован, max_tokens: %d", maxTokens)

	return &Client{
		httpClient: &http.Client{Timeout: 120 * time.Second}, // R1 может думать долго
		apiKey:     apiKey,
		baseURL:    DefaultBaseURL,
		maxTokens:  maxTokens,
		enabled:    true,
	}, nil
}

// IsEnabled возвращает true если клиент активен
func (c *Client) IsEnabled() bool {
	return c.enabled && c.apiKey != ""
}

// Close закрывает клиент
func (c *Client) Close() error {
	return nil
}

// ChatMessage - сообщение в чате
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest - запрос к DeepSeek API
type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
	Stream      bool          `json:"stream"`
}

// ChatResponse - ответ от DeepSeek API
type ChatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role             string `json:"role"`
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content,omitempty"` // Для R1 модели
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// GenerateResult - результат генерации
type GenerateResult struct {
	Response         string
	ReasoningContent string // Chain of Thought (только для R1)
	InputTokens      int
	OutputTokens     int
	TotalTokens      int
}

// Generate отправляет запрос к DeepSeek и возвращает ответ
func (c *Client) Generate(ctx context.Context, model, systemPrompt, userPrompt string) (*GenerateResult, error) {
	if !c.IsEnabled() {
		return nil, fmt.Errorf("AI клиент не инициализирован")
	}

	// Формируем запрос
	req := ChatRequest{
		Model: model,
		Messages: []ChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		MaxTokens:   c.maxTokens,
		Temperature: 0.3, // Более детерминированные ответы
		Stream:      false,
	}

	// Сериализуем
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("ошибка сериализации запроса: %w", err)
	}

	// Создаём HTTP запрос
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("ошибка создания запроса: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	// Отправляем запрос
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ошибка отправки запроса: %w", err)
	}
	defer resp.Body.Close()

	// Читаем ответ
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения ответа: %w", err)
	}

	// Проверяем статус
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ошибка API (статус %d): %s", resp.StatusCode, string(body))
	}

	// Парсим ответ
	var chatResp ChatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return nil, fmt.Errorf("ошибка парсинга ответа: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("пустой ответ от DeepSeek")
	}

	result := &GenerateResult{
		Response:         chatResp.Choices[0].Message.Content,
		ReasoningContent: chatResp.Choices[0].Message.ReasoningContent,
		InputTokens:      chatResp.Usage.PromptTokens,
		OutputTokens:     chatResp.Usage.CompletionTokens,
		TotalTokens:      chatResp.Usage.TotalTokens,
	}

	return result, nil
}

// InsightResponse - структура ответа от AI
type InsightResponse struct {
	Severity        string  `json:"severity"`
	Title           string  `json:"title"`
	Description     string  `json:"description"`
	Recommendation  string  `json:"recommendation"`
	FinancialImpact float64 `json:"financial_impact"`
	InsightType     string  `json:"insight_type"`
	Delta           int     `json:"delta"`
	DeltaPercent    float64 `json:"delta_percent"`
}

// ParseInsightResponse парсит JSON ответ от DeepSeek
// DeepSeek R1 может возвращать JSON в маркдаун-блоке (```json ... ```) или среди текста
func ParseInsightResponse(response string) (*InsightResponse, error) {
	if response == "" {
		return nil, fmt.Errorf("ошибка парсинга ответа AI: пустой ответ")
	}

	var insight InsightResponse

	// Попытка 1: прямой JSON
	if err := json.Unmarshal([]byte(response), &insight); err == nil {
		return validateInsight(&insight), nil
	}

	// Попытка 2: извлечь JSON из маркдаун-блока ```json ... ```
	if idx := strings.Index(response, "```json"); idx != -1 {
		start := idx + 7 // длина "```json"
		if end := strings.Index(response[start:], "```"); end != -1 {
			jsonStr := strings.TrimSpace(response[start : start+end])
			if err := json.Unmarshal([]byte(jsonStr), &insight); err == nil {
				return validateInsight(&insight), nil
			}
		}
	}

	// Попытка 3: извлечь JSON из ``` ... ```
	if idx := strings.Index(response, "```"); idx != -1 {
		start := idx + 3
		if end := strings.Index(response[start:], "```"); end != -1 {
			jsonStr := strings.TrimSpace(response[start : start+end])
			if err := json.Unmarshal([]byte(jsonStr), &insight); err == nil {
				return validateInsight(&insight), nil
			}
		}
	}

	// Попытка 4: найти первый { ... } блок в тексте
	if idx := strings.Index(response, "{"); idx != -1 {
		if end := strings.LastIndex(response, "}"); end > idx {
			jsonStr := response[idx : end+1]
			if err := json.Unmarshal([]byte(jsonStr), &insight); err == nil {
				return validateInsight(&insight), nil
			}
		}
	}

	return nil, fmt.Errorf("ошибка парсинга ответа AI: JSON не найден в ответе")
}

// validateInsight проверяет и корректирует поля инсайта
func validateInsight(insight *InsightResponse) *InsightResponse {
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

	return insight
}
