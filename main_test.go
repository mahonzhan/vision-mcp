package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestExecuteDescribeImage_Anthropic(t *testing.T) {
	// Create temporary image
	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "test.png")
	if err := os.WriteFile(imgPath, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0644); err != nil {
		t.Fatalf("failed to write test img: %v", err)
	}

	// Change working directory to tmpDir during test
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	var receivedReq AnthropicRequest
	var receivedAuthHeader, receivedVersionHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("x-api-key")
		receivedVersionHeader = r.Header.Get("anthropic-version")

		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedReq)

		resp := AnthropicResponse{
			Content: []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{
				{Type: "text", Text: "Anthropic vision analysis result"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	os.Setenv("ANTHROPIC_API_KEY", "sk-ant-testkey")
	os.Setenv("ANTHROPIC_BASE_URL", server.URL)
	os.Setenv("OPENAI_API_KEY", "sk-openai-fallback") // Should be ignored because Anthropic is set
	defer os.Unsetenv("ANTHROPIC_API_KEY")
	defer os.Unsetenv("ANTHROPIC_BASE_URL")
	defer os.Unsetenv("OPENAI_API_KEY")

	result := executeDescribeImage(DescribeImageArgs{
		FilePath: "test.png",
		Prompt:   "Analyze this image",
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %v", result.Content)
	}
	if len(result.Content) == 0 || result.Content[0].Text != "Anthropic vision analysis result" {
		t.Fatalf("unexpected content: %+v", result.Content)
	}
	if receivedAuthHeader != "sk-ant-testkey" {
		t.Errorf("expected auth header 'sk-ant-testkey', got '%s'", receivedAuthHeader)
	}
	if receivedVersionHeader != "2023-06-01" {
		t.Errorf("expected anthropic-version '2023-06-01', got '%s'", receivedVersionHeader)
	}
	if len(receivedReq.Messages) == 0 || len(receivedReq.Messages[0].Content) < 2 {
		t.Fatalf("invalid request message content: %+v", receivedReq)
	}
}

func TestExecuteDescribeImage_OpenAIFallback(t *testing.T) {
	// Create temporary image
	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "test.jpg")
	if err := os.WriteFile(imgPath, []byte{0xFF, 0xD8, 0xFF, 0xE0}, 0644); err != nil {
		t.Fatalf("failed to write test img: %v", err)
	}

	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	var receivedReq OpenAIRequest
	var receivedAuthHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")

		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedReq)

		resp := OpenAIResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{
				{
					Message: struct {
						Content string `json:"content"`
					}{Content: "OpenAI vision analysis result"},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	os.Unsetenv("ANTHROPIC_API_KEY")
	os.Setenv("OPENAI_API_KEY", "sk-openai-key")
	os.Setenv("OPENAI_BASE_URL", server.URL)
	defer os.Unsetenv("OPENAI_API_KEY")
	defer os.Unsetenv("OPENAI_BASE_URL")

	result := executeDescribeImage(DescribeImageArgs{
		FilePath: "test.jpg",
		Prompt:   "Analyze this jpeg image",
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %v", result.Content)
	}
	if len(result.Content) == 0 || result.Content[0].Text != "OpenAI vision analysis result" {
		t.Fatalf("unexpected content: %+v", result.Content)
	}
	if receivedAuthHeader != "Bearer sk-openai-key" {
		t.Errorf("expected auth header 'Bearer sk-openai-key', got '%s'", receivedAuthHeader)
	}
}

func TestExecuteDescribeImage_AnthropicCustomHeaders(t *testing.T) {
	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "test.png")
	if err := os.WriteFile(imgPath, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0644); err != nil {
		t.Fatalf("failed to write test img: %v", err)
	}

	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	var receivedOrgId, receivedCustomKey string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedOrgId = r.Header.Get("X-Organization-Id")
		receivedCustomKey = r.Header.Get("X-Custom-Env")

		resp := AnthropicResponse{
			Content: []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{
				{Type: "text", Text: "Header test passed"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	os.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	os.Setenv("ANTHROPIC_BASE_URL", server.URL)
	os.Setenv("ANTHROPIC_CUSTOM_HEADERS", "X-Organization-Id: 301, X-Custom-Env: production")
	defer os.Unsetenv("ANTHROPIC_API_KEY")
	defer os.Unsetenv("ANTHROPIC_BASE_URL")
	defer os.Unsetenv("ANTHROPIC_CUSTOM_HEADERS")

	result := executeDescribeImage(DescribeImageArgs{
		FilePath: "test.png",
		Prompt:   "Testing headers",
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %v", result.Content)
	}
	if receivedOrgId != "301" {
		t.Errorf("expected X-Organization-Id '301', got '%s'", receivedOrgId)
	}
	if receivedCustomKey != "production" {
		t.Errorf("expected X-Custom-Env 'production', got '%s'", receivedCustomKey)
	}
}
