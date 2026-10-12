package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	TelegramToken                                                       string
	AllowedUserID                                                       int64
	TelegramAPIBase                                                     string
	TelegramFileCacheDir                                                string
	LLMBaseURL, LLMAPIKey, LLMModel                                     string
	LLMFallbackModels                                                   []string
	LLMDailyTokenBudget                                                 int
	LLMSearchIntent                                                     bool
	StorageBackend, LocalStorageDir                                     string
	GroqAPIKey, STTModel, STTLanguage                                   string
	R2AccountID, R2AccessKeyID, R2SecretAccessKey, R2Bucket, R2Endpoint string
	AgeRecipient, AgeIdentityFile, FirecrawlAPIKey                      string
	FirecrawlDailyLimit                                                 int
	NtfyURL, NtfyTopic, NtfyToken                                       string
	MSGraphTokenFile                                                    string
	OCRLangs                                                            string
	OCRDPI, OCRMinTextChars                                             int
	OCRImagePages                                                       bool
	Timezone, DataDir, TempDir, GoMemLimit                              string
	HTTPTimeout                                                         time.Duration
}

func Load() (Config, error) {
	c := Config{
		TelegramToken:   os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramAPIBase: value("TELEGRAM_API_BASE", "https://api.telegram.org"), TelegramFileCacheDir: os.Getenv("TELEGRAM_FILE_CACHE_DIR"),
		LLMBaseURL: os.Getenv("LLM_BASE_URL"), LLMAPIKey: os.Getenv("LLM_API_KEY"), LLMModel: os.Getenv("LLM_MODEL"),
		LLMFallbackModels:   listValue("LLM_FALLBACK_MODELS", "gemini-3.1-flash-lite,groq:openai/gpt-oss-120b"),
		LLMDailyTokenBudget: intValue("LLM_DAILY_TOKEN_BUDGET", 100000), LLMSearchIntent: boolValue("LLM_SEARCH_INTENT", false),
		GroqAPIKey: os.Getenv("GROQ_API_KEY"), STTModel: value("STT_MODEL", "whisper-large-v3"), STTLanguage: value("STT_LANGUAGE", "auto"),
		R2AccountID: os.Getenv("R2_ACCOUNT_ID"), R2AccessKeyID: os.Getenv("R2_ACCESS_KEY_ID"), R2SecretAccessKey: os.Getenv("R2_SECRET_ACCESS_KEY"),
		R2Bucket: os.Getenv("R2_BUCKET"), R2Endpoint: os.Getenv("R2_ENDPOINT"), AgeRecipient: os.Getenv("AGE_RECIPIENT"), AgeIdentityFile: value("AGE_IDENTITY_FILE", "/etc/secondbrain/age.key"),
		StorageBackend: value("FILE_STORAGE_BACKEND", "none"), LocalStorageDir: os.Getenv("LOCAL_STORAGE_DIR"),
		FirecrawlAPIKey: os.Getenv("FIRECRAWL_API_KEY"), FirecrawlDailyLimit: intValue("FIRECRAWL_DAILY_LIMIT", 25), NtfyURL: os.Getenv("NTFY_URL"), NtfyTopic: value("NTFY_TOPIC", "secondbrain-alerts"), NtfyToken: os.Getenv("NTFY_TOKEN"),
		MSGraphTokenFile: value("MSGRAPH_TOKEN_FILE", "/etc/secondbrain/msgraph.json"),
		OCRLangs: value("OCR_LANGS", "eng"), OCRDPI: intValue("OCR_DPI", 300), OCRMinTextChars: intValue("OCR_MIN_TEXT_CHARS", 30), OCRImagePages: boolValue("OCR_IMAGE_PAGES", true),
		Timezone: value("TZ", "Asia/Kolkata"), DataDir: value("DATA_DIR", "./data"), TempDir: value("TMP_DIR", "./tmp"), GoMemLimit: value("GOMEMLIMIT", "200MiB"), HTTPTimeout: 30 * time.Second,
	}
	uid, err := strconv.ParseInt(os.Getenv("TELEGRAM_ALLOWED_USER_ID"), 10, 64)
	if err != nil || uid <= 0 {
		return Config{}, fmt.Errorf("TELEGRAM_ALLOWED_USER_ID must be a positive numeric Telegram user ID")
	}
	c.AllowedUserID = uid
	if c.TelegramToken == "" {
		return Config{}, fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}
	if err := requireGroup("LLM", map[string]string{"LLM_BASE_URL": c.LLMBaseURL, "LLM_API_KEY": c.LLMAPIKey, "LLM_MODEL": c.LLMModel}); err != nil {
		return Config{}, err
	}
	if c.LocalStorageDir == "" {
		c.LocalStorageDir = filepath.Join(c.DataDir, "objects")
	}
	switch c.StorageBackend {
	case "none":
	case "local":
		if c.AgeRecipient == "" {
			return Config{}, fmt.Errorf("AGE_RECIPIENT is required when FILE_STORAGE_BACKEND=local")
		}
	case "r2":
		if err := requireGroup("R2 storage", map[string]string{"R2_ACCOUNT_ID": c.R2AccountID, "R2_ACCESS_KEY_ID": c.R2AccessKeyID, "R2_SECRET_ACCESS_KEY": c.R2SecretAccessKey, "R2_BUCKET": c.R2Bucket, "R2_ENDPOINT": c.R2Endpoint, "AGE_RECIPIENT": c.AgeRecipient}); err != nil {
			return Config{}, err
		}
	default:
		return Config{}, fmt.Errorf("FILE_STORAGE_BACKEND must be none, local, or r2")
	}
	for _, model := range c.LLMFallbackModels {
		if strings.HasPrefix(model, "groq:") && strings.TrimPrefix(model, "groq:") == "" {
			return Config{}, fmt.Errorf("LLM_FALLBACK_MODELS contains an empty Groq model")
		}
		if strings.HasPrefix(model, "groq:") && c.GroqAPIKey == "" {
			return Config{}, fmt.Errorf("GROQ_API_KEY is required for the configured Groq LLM fallback and voice transcription")
		}
	}
	if c.NtfyURL != "" || c.NtfyToken != "" {
		if err := requireGroup("ntfy", map[string]string{"NTFY_URL": c.NtfyURL, "NTFY_TOKEN": c.NtfyToken, "NTFY_TOPIC": c.NtfyTopic}); err != nil {
			return Config{}, err
		}
	}
	if c.OCRDPI < 72 || c.OCRDPI > 600 {
		return Config{}, fmt.Errorf("OCR_DPI must be between 72 and 600")
	}
	if c.OCRMinTextChars < 0 {
		return Config{}, fmt.Errorf("OCR_MIN_TEXT_CHARS cannot be negative")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return Config{}, fmt.Errorf("invalid TZ %q: %w", c.Timezone, err)
	}
	return c, nil
}

func requireGroup(feature string, values map[string]string) error {
	for key, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is partially configured: %s is required", feature, key)
		}
	}
	return nil
}

func value(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
func listValue(key, fallback string) []string {
	raw, ok := os.LookupEnv(key)
	if !ok {
		raw = fallback
	}
	var values []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}
func intValue(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}
func boolValue(key string, fallback bool) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}
