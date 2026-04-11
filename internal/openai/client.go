package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
)

func StripMarkdownJSONFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	rest := strings.TrimSpace(s[3:])
	if strings.HasPrefix(strings.ToLower(rest), "json") {
		rest = strings.TrimSpace(rest[len("json"):])
	}
	if i := strings.Index(rest, "```"); i >= 0 {
		rest = rest[:i]
	}
	return strings.TrimSpace(rest)
}

const defaultModel = "gpt-4o-mini"

func ResolveModel() string {
	if m := strings.TrimSpace(os.Getenv("OPENAI_MODEL")); m != "" {
		return m
	}
	return defaultModel
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []Message      `json:"messages"`
	Stream         bool           `json:"stream"`
	ResponseFormat *respFormat    `json:"response_format,omitempty"`
	MaxTokens      int            `json:"max_tokens,omitempty"`
}

type respFormat struct {
	Type string `json:"type"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func CompleteChat(baseURL, apiKey, model string, messages []Message, jsonMode bool) (string, error) {
	body, status, err := postChatCompletions(baseURL, apiKey, model, messages, jsonMode)
	if err != nil {
		return "", err
	}
	if jsonMode && status == http.StatusBadRequest && shouldRetryChatWithoutJSON(body) {
		body, status, err = postChatCompletions(baseURL, apiKey, model, messages, false)
		if err != nil {
			return "", err
		}
	}
	if status < 200 || status >= 300 {
		return "", errors.New("api: " + string(bytes.TrimSpace(body)))
	}
	var out chatResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", errors.New(out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", errors.New("no completion")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

func shouldRetryChatWithoutJSON(apiBody []byte) bool {
	s := strings.ToLower(string(apiBody))
	return strings.Contains(s, "response_format") || strings.Contains(s, "json_object")
}

func postChatCompletions(baseURL, apiKey, model string, messages []Message, jsonMode bool) ([]byte, int, error) {
	reqObj := chatRequest{
		Model:     model,
		Messages:  messages,
		Stream:    false,
		MaxTokens: 8192,
	}
	if jsonMode {
		reqObj.ResponseFormat = &respFormat{Type: "json_object"}
	}
	payload, err := json.Marshal(reqObj)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimSuffix(baseURL, "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return body, resp.StatusCode, nil
}
