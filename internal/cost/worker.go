package cost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"github.com/Aswanidev-vs/RateGuard/internal/model"
	"github.com/Aswanidev-vs/RateGuard/internal/store"
)

const (
	// BillingHistoryDays is the number of daily totals fetched from the
	// billing source: seven history days plus the current day.
	BillingHistoryDays = 8
	// MinBillingPoints is the minimum number of daily totals needed for a
	// check (the current day plus at least three history days).
	MinBillingPoints = 4
	// AlertSubject is the SNS subject used for anomaly notifications.
	AlertSubject = "RateGuard AWS cost anomaly"
	// DefaultMetric is used when Worker.Metric is empty.
	DefaultMetric = "EstimatedCharges"

	billingNamespace = "AWS/Billing"
	billingCurrency  = "USD"
	billingMetric    = "EstimatedCharges"
)

// BillingSource supplies daily AWS spend totals, oldest first. The last value
// is the current day (or the most recent available day).
type BillingSource interface {
	History(ctx context.Context, days int) ([]float64, error)
}

// Notifier publishes anomaly alerts. The production implementation is
// SNSNotifier.
type Notifier interface {
	Publish(ctx context.Context, subject, message string) error
}

// Result describes what one Worker run did. Checked is false when there was
// not enough billing history to evaluate.
type Result struct {
	Checked    bool       `json:"checked"`
	Current    float64    `json:"current"`
	Evaluation Evaluation `json:"evaluation"`
	Recorded   bool       `json:"recorded"`
	Alerted    bool       `json:"alerted"`
}

// Worker performs one cost anomaly check: fetch billing history, evaluate it,
// persist new anomalies with hour-level idempotency, and publish an alert.
// Store, Source, and Log are required; Notifier may be nil to record without
// alerting.
type Worker struct {
	Store    store.Store
	Source   BillingSource
	Notifier Notifier
	Log      *slog.Logger
	Metric   string
	Now      func() time.Time
}

// Run executes one anomaly check. A fresh anomaly is keyed by account tenant
// and the current UTC hour, so repeated invocations within the same hour do not
// duplicate records or alerts. When evaluation finds no anomaly, or when a
// record already exists for the hour, Run is a no-op beyond fetching history.
//
// A failed SNS publish or MarkAnomalyAlerted call is returned as an error; the
// anomaly record is deliberately kept so the dashboard still shows it.
func (w *Worker) Run(ctx context.Context) (Result, error) {
	if w == nil {
		return Result{}, errors.New("cost: nil worker")
	}
	if w.Source == nil {
		return Result{}, errors.New("cost: nil billing source")
	}
	if w.Store == nil {
		return Result{}, errors.New("cost: nil store")
	}

	log := w.Log
	if log == nil {
		log = slog.Default()
	}
	metric := w.metric()

	values, err := w.Source.History(ctx, BillingHistoryDays)
	if err != nil {
		return Result{}, fmt.Errorf("cost: fetch billing history: %w", err)
	}
	if len(values) < MinBillingPoints {
		log.Info("cost: not enough billing history; skipping check", "points", len(values))
		return Result{Checked: false}, nil
	}
	if len(values) > BillingHistoryDays {
		values = values[len(values)-BillingHistoryDays:]
	}

	current := values[len(values)-1]
	history := values[:len(values)-1]
	evaluation := Evaluate(current, history)
	result := Result{
		Checked:    true,
		Current:    current,
		Evaluation: evaluation,
	}
	if !evaluation.Anomaly {
		return result, nil
	}

	detectedAt := w.now().UTC().Truncate(time.Hour).Format(time.RFC3339)
	anomaly := &model.Anomaly{
		TenantID:   model.AccountTenantID,
		DetectedAt: detectedAt,
		Metric:     metric,
		Value:      current,
		Baseline:   evaluation.Baseline,
		Threshold:  evaluation.Threshold,
		Severity:   evaluation.Severity,
		Alerted:    false,
	}

	inserted, err := w.Store.PutAnomalyIfAbsent(ctx, anomaly)
	if err != nil {
		return result, fmt.Errorf("cost: record anomaly: %w", err)
	}
	if !inserted {
		log.Info("cost: anomaly already recorded for hour; skipping alert",
			"detected_at", detectedAt, "metric", metric)
		return result, nil
	}
	result.Recorded = true

	if w.Notifier == nil {
		log.Warn("cost: anomaly recorded without a notifier",
			"detected_at", detectedAt, "severity", evaluation.Severity)
		return result, nil
	}

	if err := w.Notifier.Publish(ctx, AlertSubject, formatAlert(anomaly)); err != nil {
		log.Error("cost: publish anomaly alert failed",
			"error", err, "detected_at", detectedAt, "severity", evaluation.Severity)
		return result, fmt.Errorf("cost: publish alert: %w", err)
	}
	result.Alerted = true

	if err := w.Store.MarkAnomalyAlerted(ctx, model.AccountTenantID, detectedAt); err != nil {
		return result, fmt.Errorf("cost: mark anomaly alerted: %w", err)
	}

	log.Info("cost: anomaly detected and alert published",
		"current", current,
		"baseline", evaluation.Baseline,
		"threshold", evaluation.Threshold,
		"severity", evaluation.Severity,
		"detected_at", detectedAt,
		"metric", metric,
	)
	return result, nil
}

// metric returns the configured billing metric name, defaulting to
// EstimatedCharges.
func (w *Worker) metric() string {
	if w.Metric == "" {
		return DefaultMetric
	}
	return w.Metric
}

// now returns the worker clock, defaulting to time.Now.
func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// formatAlert renders the human-readable SNS message for an anomaly.
func formatAlert(a *model.Anomaly) string {
	return fmt.Sprintf(
		"RateGuard detected an AWS cost anomaly.\n\n"+
			"Metric: %s\n"+
			"Current: %.2f USD\n"+
			"Baseline: %.2f USD\n"+
			"Threshold: %.2f USD\n"+
			"Severity: %s\n"+
			"Detected at: %s\n"+
			"Tenant: %s\n",
		a.Metric, a.Value, a.Baseline, a.Threshold, a.Severity, a.DetectedAt, a.TenantID,
	)
}

// CloudWatchBilling reads the AWS/Billing EstimatedCharges metric from
// CloudWatch. Billing metrics are published only in us-east-1 regardless of
// where the workload runs.
type CloudWatchBilling struct {
	client *cloudwatch.Client
	region string
	now    func() time.Time
}

// NewCloudWatchBilling returns a BillingSource backed by CloudWatch. The
// client must be configured for the billing region (us-east-1).
func NewCloudWatchBilling(cw *cloudwatch.Client, region string) *CloudWatchBilling {
	return &CloudWatchBilling{client: cw, region: region, now: time.Now}
}

// History returns up to days daily totals, oldest first. CloudWatch datapoints
// are grouped by UTC day, taking the maximum estimate per day; days without
// data are skipped. The EstimatedCharges metric is a running estimate, so the
// maximum for a day is the best approximation of that day's spend.
func (c *CloudWatchBilling) History(ctx context.Context, days int) ([]float64, error) {
	if c == nil {
		return nil, errors.New("cost: cloudwatch billing: nil receiver")
	}
	if c.client == nil {
		return nil, fmt.Errorf("cost: cloudwatch billing (%s): nil client", c.region)
	}
	if days < 1 {
		days = 1
	}

	now := time.Now
	if c.now != nil {
		now = c.now
	}
	end := now().UTC()
	start := end.Add(-time.Duration(days+1) * 24 * time.Hour)

	out, err := c.client.GetMetricStatistics(ctx, &cloudwatch.GetMetricStatisticsInput{
		Namespace:  aws.String(billingNamespace),
		MetricName: aws.String(billingMetric),
		Dimensions: []cwtypes.Dimension{{
			Name:  aws.String("Currency"),
			Value: aws.String(billingCurrency),
		}},
		StartTime:  aws.Time(start),
		EndTime:    aws.Time(end),
		Period:     aws.Int32(86400),
		Statistics: []cwtypes.Statistic{cwtypes.StatisticMaximum},
	})
	if err != nil {
		return nil, fmt.Errorf("cost: get CloudWatch billing metric: %w", err)
	}

	// Reduce datapoints to one value per UTC calendar day.
	daily := make(map[string]float64, days)
	for _, point := range out.Datapoints {
		if point.Timestamp == nil || point.Maximum == nil {
			continue
		}
		value := *point.Maximum
		if !validSample(value) {
			continue
		}
		day := point.Timestamp.UTC().Format("2006-01-02")
		if existing, ok := daily[day]; !ok || value > existing {
			daily[day] = value
		}
	}

	values := make([]float64, 0, days)
	for i := days - 1; i >= 0; i-- {
		day := end.AddDate(0, 0, -i).Format("2006-01-02")
		if value, ok := daily[day]; ok {
			values = append(values, value)
		}
	}
	return values, nil
}

// SNSNotifier publishes anomaly alerts to an SNS topic.
type SNSNotifier struct {
	client   *sns.Client
	topicARN string
}

// NewSNSNotifier returns a Notifier that publishes to topicARN.
func NewSNSNotifier(client *sns.Client, topicARN string) *SNSNotifier {
	return &SNSNotifier{client: client, topicARN: topicARN}
}

// Publish sends one alert to the configured SNS topic.
func (n *SNSNotifier) Publish(ctx context.Context, subject, message string) error {
	if n == nil || n.client == nil {
		return errors.New("cost: sns notifier: nil client")
	}
	if n.topicARN == "" {
		return errors.New("cost: sns notifier: empty topic ARN")
	}
	_, err := n.client.Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(n.topicARN),
		Subject:  aws.String(subject),
		Message:  aws.String(message),
	})
	if err != nil {
		return fmt.Errorf("cost: sns publish: %w", err)
	}
	return nil
}

var (
	_ BillingSource = (*CloudWatchBilling)(nil)
	_ Notifier      = (*SNSNotifier)(nil)
)
