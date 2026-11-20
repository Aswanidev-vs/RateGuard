// Package config loads RateGuard runtime configuration from the environment
// and, when enabled, AWS SSM Parameter Store.
package config

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	AppEnv      string
	Port        string
	AWSRegion   string
	LogLevel    string
	DevMode     bool
	DemoEnabled bool

	StoreBackend   string
	DynamoEndpoint string
	TenantsTable   string
	UsageTable     string
	AnomaliesTable string

	SSMEnabled bool
	SSMPrefix  string

	UpstreamURL string
	AdminAPIKey string
	SNSTopicARN string

	SessionSecret          string
	SessionSecretGenerated bool
	CookieSecure           bool
	TrustProxy             bool

	UsageFlushInterval time.Duration
	UsageBatchSize     int
}

// Load reads configuration from environment variables and applies defaults.
// Call LoadSSM (when enabled) before Validate.
func Load() (*Config, error) {
	appEnv := env("APP_ENV", "development")
	c := &Config{
		AppEnv:      appEnv,
		Port:        env("PORT", "8080"),
		AWSRegion:   env("AWS_REGION", "ap-south-1"),
		LogLevel:    env("LOG_LEVEL", "info"),
		DevMode:     appEnv != "production",
		DemoEnabled: envBool("DEMO_ENABLED", true),

		StoreBackend:   env("STORE_BACKEND", "memory"),
		DynamoEndpoint: os.Getenv("DYNAMODB_ENDPOINT"),
		TenantsTable:   env("DYNAMODB_TENANTS_TABLE", "api_guard_tenants"),
		UsageTable:     env("DYNAMODB_USAGE_TABLE", "api_guard_usage"),
		AnomaliesTable: env("DYNAMODB_ANOMALIES_TABLE", "api_guard_cost_anomalies"),

		SSMEnabled: envBool("SSM_ENABLED", false),
		SSMPrefix:  env("SSM_PARAM_PREFIX", "/api-guard"),

		UpstreamURL: os.Getenv("PROXY_UPSTREAM_URL"),
		AdminAPIKey: os.Getenv("ADMIN_API_KEY"),
		SNSTopicARN: os.Getenv("SNS_TOPIC_ARN"),

		SessionSecret: os.Getenv("SESSION_SECRET"),
		CookieSecure:  envBool("COOKIE_SECURE", appEnv == "production"),
		TrustProxy:    envBool("TRUST_PROXY_HEADERS", false),

		UsageFlushInterval: envDuration("USAGE_FLUSH_INTERVAL", 10*time.Second),
		UsageBatchSize:     envInt("USAGE_BATCH_SIZE", 100),
	}

	if len(c.SessionSecret) < 32 {
		// Development-only convenience. Validate rejects this in production.
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, fmt.Errorf("generate session secret: %w", err)
		}
		c.SessionSecret = base64.RawURLEncoding.EncodeToString(secret)
		c.SessionSecretGenerated = true
	}
	return c, nil
}

// LoadSSM resolves secrets and endpoint settings from SSM Parameter Store when
// SSM_ENABLED is true. Missing optional parameters fall back to env values;
// missing required values are caught by Validate.
func (c *Config) LoadSSM(ctx context.Context) error {
	if !c.SSMEnabled {
		return nil
	}
	if !strings.HasPrefix(c.SSMPrefix, "/") {
		return errors.New("SSM_PARAM_PREFIX must start with /")
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(c.AWSRegion))
	if err != nil {
		return fmt.Errorf("load AWS config for SSM: %w", err)
	}
	client := ssm.NewFromConfig(awsCfg)

	params := []struct {
		name string
		dst  *string
	}{
		{"admin-api-key", &c.AdminAPIKey},
		{"upstream-url", &c.UpstreamURL},
		{"sns-topic-arn", &c.SNSTopicARN},
		{"session-secret", &c.SessionSecret},
	}
	for _, p := range params {
		name := strings.TrimRight(c.SSMPrefix, "/") + "/" + p.name
		out, err := client.GetParameter(ctx, &ssm.GetParameterInput{
			Name:           &name,
			WithDecryption: boolPtr(true),
		})
		if err != nil {
			var notFound *ssmtypes.ParameterNotFound
			if errors.As(err, &notFound) {
				slog.Warn("SSM parameter missing; falling back to environment", "name", name)
				continue
			}
			return fmt.Errorf("read SSM parameter %s: %w", name, err)
		}
		if out.Parameter != nil && out.Parameter.Value != nil && *out.Parameter.Value != "" {
			*p.dst = *out.Parameter.Value
			if p.name == "session-secret" {
				c.SessionSecretGenerated = false
			}
		}
	}
	return nil
}

// Validate enforces invariants that must hold before serving traffic.
func (c *Config) Validate() error {
	port, err := strconv.Atoi(c.Port)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("PORT must be 1-65535, got %q", c.Port)
	}
	switch c.StoreBackend {
	case "memory", "dynamodb":
	default:
		return fmt.Errorf("STORE_BACKEND must be memory or dynamodb, got %q", c.StoreBackend)
	}
	if c.DevMode == false && c.StoreBackend != "dynamodb" {
		return errors.New("STORE_BACKEND must be dynamodb when APP_ENV=production")
	}
	if c.TenantsTable == "" || c.UsageTable == "" || c.AnomaliesTable == "" {
		return errors.New("DynamoDB table names must not be empty")
	}
	if c.AdminAPIKey == "" {
		return errors.New("ADMIN_API_KEY is required (set env or SSM /admin-api-key)")
	}
	if len(c.AdminAPIKey) < 16 {
		return errors.New("ADMIN_API_KEY must be at least 16 characters")
	}
	if c.SessionSecretGenerated && !c.DevMode {
		return errors.New("SESSION_SECRET is required in production")
	}
	if c.SessionSecretGenerated {
		slog.Warn("SESSION_SECRET not set; generated an ephemeral secret (sessions reset on restart)")
	}
	if c.UpstreamURL != "" {
		u, err := url.Parse(c.UpstreamURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("PROXY_UPSTREAM_URL must be an absolute http(s) URL")
		}
	}
	if c.UsageFlushInterval < time.Second {
		return errors.New("USAGE_FLUSH_INTERVAL must be at least 1s")
	}
	if c.UsageBatchSize < 1 || c.UsageBatchSize > 10000 {
		return errors.New("USAGE_BATCH_SIZE must be 1-10000")
	}
	if c.SSMEnabled && !strings.HasPrefix(c.SSMPrefix, "/") {
		return errors.New("SSM_PARAM_PREFIX must start with /")
	}
	return nil
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envInt(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func boolPtr(b bool) *bool { return &b }
