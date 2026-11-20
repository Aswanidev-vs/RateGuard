// Command lambda runs RateGuard's scheduled AWS cost anomaly check. It is
// invoked hourly by EventBridge, reads the AWS/Billing EstimatedCharges metric
// from CloudWatch (us-east-1), records new anomalies in DynamoDB, and
// publishes SNS alerts.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"github.com/Aswanidev-vs/RateGuard/internal/cost"
	"github.com/Aswanidev-vs/RateGuard/internal/store"
)

// handlerTimeout bounds the whole check so a hung downstream call cannot
// exhaust the Lambda timeout.
const handlerTimeout = 30 * time.Second

// config is the Lambda's environment-derived runtime configuration.
type config struct {
	region         string
	billingRegion  string
	dynamoEndpoint string
	tenantsTable   string
	usageTable     string
	anomaliesTable string
	snsTopicARN    string
	metric         string

	// fakeCurrent and fakeHistory enable the M4 demo path: a synthetic
	// billing history is used instead of calling CloudWatch.
	fakeCurrent *float64
	fakeHistory []float64
}

// main wires the Lambda handler. AWS clients and the store are constructed
// once per cold start so connection pools are reused across invocations.
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid lambda configuration", "error", err)
		os.Exit(1)
	}

	worker, err := newWorker(context.Background(), cfg, logger)
	if err != nil {
		logger.Error("initialise cost worker", "error", err)
		os.Exit(1)
	}

	lambda.Start(func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, handlerTimeout)
		defer cancel()

		result, err := worker.Run(ctx)
		if err != nil {
			logger.Error("cost anomaly check failed", "error", err, "result", result)
			return err
		}
		logger.Info("cost anomaly check complete", "result", result)
		return nil
	})
}

// loadConfig reads Lambda environment variables with defaults matching
// internal/config.
func loadConfig() (*config, error) {
	c := &config{
		region:         env("AWS_REGION", "ap-south-1"),
		billingRegion:  env("COST_BILLING_REGION", "us-east-1"),
		dynamoEndpoint: os.Getenv("DYNAMODB_ENDPOINT"),
		tenantsTable:   env("DYNAMODB_TENANTS_TABLE", "api_guard_tenants"),
		usageTable:     env("DYNAMODB_USAGE_TABLE", "api_guard_usage"),
		anomaliesTable: env("DYNAMODB_ANOMALIES_TABLE", "api_guard_cost_anomalies"),
		snsTopicARN:    os.Getenv("SNS_TOPIC_ARN"),
		metric:         env("COST_METRIC_NAME", cost.DefaultMetric),
	}

	rawCurrent := strings.TrimSpace(os.Getenv("COST_FAKE_CURRENT"))
	if rawCurrent == "" {
		return c, nil
	}
	current, err := strconv.ParseFloat(rawCurrent, 64)
	if err != nil {
		return nil, fmt.Errorf("parse COST_FAKE_CURRENT %q: %w", rawCurrent, err)
	}
	c.fakeCurrent = &current

	rawHistory := strings.TrimSpace(os.Getenv("COST_FAKE_HISTORY"))
	if rawHistory == "" {
		return c, nil
	}
	for _, field := range strings.Split(rawHistory, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		value, err := strconv.ParseFloat(field, 64)
		if err != nil {
			return nil, fmt.Errorf("parse COST_FAKE_HISTORY value %q: %w", field, err)
		}
		c.fakeHistory = append(c.fakeHistory, value)
	}
	return c, nil
}

// newWorker builds the cost worker with production AWS clients and the
// DynamoDB store, or with a static billing source when fake mode is enabled.
func newWorker(ctx context.Context, cfg *config, logger *slog.Logger) (*cost.Worker, error) {
	if cfg == nil {
		return nil, errors.New("nil configuration")
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.region))
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	var notifier cost.Notifier
	if cfg.snsTopicARN != "" {
		notifier = cost.NewSNSNotifier(sns.NewFromConfig(awsCfg), cfg.snsTopicARN)
	} else {
		logger.Warn("SNS_TOPIC_ARN is empty; anomalies will be recorded but not published")
	}

	st, err := openStore(ctx, cfg)
	if err != nil {
		return nil, err
	}

	var source cost.BillingSource
	if cfg.fakeCurrent != nil {
		values := make([]float64, 0, len(cfg.fakeHistory)+1)
		values = append(values, cfg.fakeHistory...)
		values = append(values, *cfg.fakeCurrent)
		source = &staticBillingSource{values: values}
		logger.Warn("using fake billing history", "points", len(values), "current", *cfg.fakeCurrent)
	} else {
		billingCfg := awsCfg
		billingCfg.Region = cfg.billingRegion
		source = cost.NewCloudWatchBilling(cloudwatch.NewFromConfig(billingCfg), cfg.billingRegion)
	}

	return &cost.Worker{
		Store:    st,
		Source:   source,
		Notifier: notifier,
		Log:      logger,
		Metric:   cfg.metric,
		Now:      time.Now,
	}, nil
}

// openStore creates the DynamoDB-backed store used for anomaly records.
func openStore(ctx context.Context, cfg *config) (store.Store, error) {
	st, err := store.NewDynamo(ctx, store.DynamoOptions{
		Region:         cfg.region,
		Endpoint:       cfg.dynamoEndpoint,
		TenantsTable:   cfg.tenantsTable,
		UsageTable:     cfg.usageTable,
		AnomaliesTable: cfg.anomaliesTable,
	})
	if err != nil {
		return nil, fmt.Errorf("open DynamoDB store: %w", err)
	}
	return st, nil
}

// staticBillingSource is an in-memory BillingSource for COST_FAKE_* demo runs.
type staticBillingSource struct {
	values []float64
}

// History returns the configured synthetic daily totals, oldest first.
func (s *staticBillingSource) History(_ context.Context, _ int) ([]float64, error) {
	return append([]float64(nil), s.values...), nil
}

// env returns key from the environment, or fallback when unset/empty.
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
