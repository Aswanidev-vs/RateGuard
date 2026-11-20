package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamodbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/Aswanidev-vs/RateGuard/internal/model"
)

const (
	// APIKeyPrefixIndex is the global secondary index used for key-prefix
	// tenant lookups.
	APIKeyPrefixIndex = "api_key_prefix-index"

	// usageTTL is how long hourly usage records remain in DynamoDB.
	usageTTL = 30 * 24 * time.Hour
)

// DynamoOptions configures a DynamoDB-backed Store.
type DynamoOptions struct {
	Region         string
	Endpoint       string
	TenantsTable   string
	UsageTable     string
	AnomaliesTable string
}

// Dynamo is a DynamoDB-backed Store.
type Dynamo struct {
	client         dynamoAPI
	tenantsTable   string
	usageTable     string
	anomaliesTable string
}

// dynamoAPI is the subset of the DynamoDB client used by Dynamo. Keeping the
// dependency behind an interface makes deterministic unit tests possible
// without a live AWS account.
type dynamoAPI interface {
	DescribeTable(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	Scan(context.Context, *dynamodb.ScanInput, ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
	UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
}

// usageRecord is the DynamoDB representation used when reading usage buckets.
// It is intentionally separate from model.UsageAggregate: the aggregate spans
// several stored buckets.
type usageRecord struct {
	TenantID     string           `dynamodbav:"tenant_id"`
	Bucket       string           `dynamodbav:"bucket"`
	Allowed      int64            `dynamodbav:"allowed"`
	Blocked      int64            `dynamodbav:"blocked"`
	Count        int64            `dynamodbav:"count"`
	SumLatencyMs int64            `dynamodbav:"sum_latency_ms"`
	LatencyHist  map[string]int64 `dynamodbav:"latency_hist"`
}

var _ Store = (*Dynamo)(nil)

// NewDynamo creates a DynamoDB-backed store. When opts.Endpoint is set the
// client targets that endpoint (for example DynamoDB Local) and uses static
// local-only credentials, because the SDK still requires a signing identity
// even for a local endpoint.
func NewDynamo(ctx context.Context, opts DynamoOptions) (*Dynamo, error) {
	if opts.TenantsTable == "" || opts.UsageTable == "" || opts.AnomaliesTable == "" {
		return nil, errors.New("dynamodb store: tenants, usage, and anomalies table names are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	loadOpts := make([]func(*awsconfig.LoadOptions) error, 0, 2)
	if opts.Region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(opts.Region))
	}
	if opts.Endpoint != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("local", "local", ""),
		))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	clientOpts := make([]func(*dynamodb.Options), 0, 1)
	if opts.Endpoint != "" {
		endpoint := opts.Endpoint
		clientOpts = append(clientOpts, func(o *dynamodb.Options) {
			o.BaseEndpoint = aws.String(endpoint)
		})
	}

	return &Dynamo{
		client:         dynamodb.NewFromConfig(cfg, clientOpts...),
		tenantsTable:   opts.TenantsTable,
		usageTable:     opts.UsageTable,
		anomaliesTable: opts.AnomaliesTable,
	}, nil
}

// CreateTenant stores a tenant if its ID is unused. It returns ErrConflict
// when the tenant already exists.
func (d *Dynamo) CreateTenant(ctx context.Context, t *model.Tenant) error {
	if t == nil {
		return errors.New("create tenant: nil tenant")
	}
	item, err := attributevalue.MarshalMap(t)
	if err != nil {
		return fmt.Errorf("marshal tenant %q: %w", t.TenantID, err)
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(d.tenantsTable),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(tenant_id)"),
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return fmt.Errorf("create tenant %q: %w", t.TenantID, ErrConflict)
		}
		return fmt.Errorf("put tenant %q: %w", t.TenantID, err)
	}
	return nil
}

// GetTenant loads a tenant by ID. It returns ErrNotFound when the item does not
// exist.
func (d *Dynamo) GetTenant(ctx context.Context, id string) (*model.Tenant, error) {
	out, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(d.tenantsTable),
		Key: map[string]dynamodbtypes.AttributeValue{
			"tenant_id": stringAttribute(id),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get tenant %q: %w", id, err)
	}
	if out == nil || len(out.Item) == 0 {
		return nil, fmt.Errorf("get tenant %q: %w", id, ErrNotFound)
	}
	var t model.Tenant
	if err := attributevalue.UnmarshalMap(out.Item, &t); err != nil {
		return nil, fmt.Errorf("unmarshal tenant %q: %w", id, err)
	}
	return &t, nil
}

// FindTenantsByKeyPrefix returns every tenant whose API key prefix begins with
// prefix. Prefix collisions naturally return multiple tenants.
func (d *Dynamo) FindTenantsByKeyPrefix(ctx context.Context, prefix string) ([]*model.Tenant, error) {
	input := &dynamodb.QueryInput{
		TableName:              aws.String(d.tenantsTable),
		IndexName:              aws.String(APIKeyPrefixIndex),
		KeyConditionExpression: aws.String("api_key_prefix = :prefix"),
		ExpressionAttributeValues: map[string]dynamodbtypes.AttributeValue{
			":prefix": stringAttribute(prefix),
		},
	}

	tenants := make([]*model.Tenant, 0)
	for {
		out, err := d.client.Query(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("query tenants by key prefix %q: %w", prefix, err)
		}
		for _, item := range out.Items {
			var t model.Tenant
			if err := attributevalue.UnmarshalMap(item, &t); err != nil {
				return nil, fmt.Errorf("unmarshal tenant from key prefix query: %w", err)
			}
			tenants = append(tenants, &t)
		}
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		input.ExclusiveStartKey = out.LastEvaluatedKey
	}
	return tenants, nil
}

// ListTenants returns all tenants sorted by creation time descending.
func (d *Dynamo) ListTenants(ctx context.Context) ([]*model.Tenant, error) {
	input := &dynamodb.ScanInput{TableName: aws.String(d.tenantsTable)}

	tenants := make([]*model.Tenant, 0)
	for {
		out, err := d.client.Scan(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("scan tenants: %w", err)
		}
		for _, item := range out.Items {
			var t model.Tenant
			if err := attributevalue.UnmarshalMap(item, &t); err != nil {
				return nil, fmt.Errorf("unmarshal tenant from scan: %w", err)
			}
			tenants = append(tenants, &t)
		}
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		input.ExclusiveStartKey = out.LastEvaluatedKey
	}
	sortTenantsNewestFirst(tenants)
	return tenants, nil
}

// UpdateTenant replaces an existing tenant. It returns ErrNotFound when the
// tenant does not exist.
func (d *Dynamo) UpdateTenant(ctx context.Context, t *model.Tenant) error {
	if t == nil {
		return errors.New("update tenant: nil tenant")
	}
	item, err := attributevalue.MarshalMap(t)
	if err != nil {
		return fmt.Errorf("marshal tenant %q: %w", t.TenantID, err)
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(d.tenantsTable),
		Item:                item,
		ConditionExpression: aws.String("attribute_exists(tenant_id)"),
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return fmt.Errorf("update tenant %q: %w", t.TenantID, ErrNotFound)
		}
		return fmt.Errorf("put tenant %q: %w", t.TenantID, err)
	}
	return nil
}

// AddUsage applies each entry as an atomic DynamoDB update. Entries share an
// hourly record and increment request counts, latency totals, and the latency
// histogram bucket. Usage records expire after 30 days.
func (d *Dynamo) AddUsage(ctx context.Context, entries []model.UsageEntry) error {
	if len(entries) == 0 {
		return nil
	}

	now := time.Now().UTC()
	updatedAt := now.Format(time.RFC3339)
	ttl := strconv.FormatInt(now.Add(usageTTL).Unix(), 10)

	const updateExpression = "SET updated_at = :updated_at, #ttl = :ttl " +
		"ADD allowed :allowed, blocked :blocked, #count :count, sum_latency_ms :sum_latency_ms"

	for _, e := range entries {
		names := map[string]string{
			"#count": "count",
			"#ttl":   "ttl",
		}
		values := map[string]dynamodbtypes.AttributeValue{
			":updated_at":     stringAttribute(updatedAt),
			":ttl":            numberAttribute(ttl),
			":allowed":        numberAttribute(strconv.FormatInt(e.Allowed, 10)),
			":blocked":        numberAttribute(strconv.FormatInt(e.Blocked, 10)),
			":count":          numberAttribute(strconv.FormatInt(e.Count, 10)),
			":sum_latency_ms": numberAttribute(strconv.FormatInt(e.SumLatencyMs, 10)),
		}
		expression := updateExpression
		if e.LatencyBucket != "" {
			names["#bucket"] = e.LatencyBucket
			values[":hist_count"] = numberAttribute(strconv.FormatInt(e.Count, 10))
			expression += ", latency_hist.#bucket :hist_count"
		}

		_, err := d.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName: aws.String(d.usageTable),
			Key: map[string]dynamodbtypes.AttributeValue{
				"tenant_id": stringAttribute(e.TenantID),
				"bucket":    stringAttribute(e.Bucket),
			},
			UpdateExpression:          aws.String(expression),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: values,
		})
		if err != nil {
			return fmt.Errorf("add usage for tenant %q bucket %q: %w", e.TenantID, e.Bucket, err)
		}
	}
	return nil
}

// GetUsage sums the usage records for tenantID from fromBucket through
// toBucket, inclusive.
func (d *Dynamo) GetUsage(ctx context.Context, tenantID, fromBucket, toBucket string) (model.UsageAggregate, error) {
	input := &dynamodb.QueryInput{
		TableName:              aws.String(d.usageTable),
		KeyConditionExpression: aws.String("tenant_id = :tenant AND #bucket BETWEEN :from AND :to"),
		ExpressionAttributeNames: map[string]string{
			"#bucket": "bucket",
		},
		ExpressionAttributeValues: map[string]dynamodbtypes.AttributeValue{
			":tenant": stringAttribute(tenantID),
			":from":   stringAttribute(fromBucket),
			":to":     stringAttribute(toBucket),
		},
	}

	var agg model.UsageAggregate
	for {
		out, err := d.client.Query(ctx, input)
		if err != nil {
			return model.UsageAggregate{}, fmt.Errorf("query usage for tenant %q: %w", tenantID, err)
		}
		for _, item := range out.Items {
			var rec usageRecord
			if err := attributevalue.UnmarshalMap(item, &rec); err != nil {
				return model.UsageAggregate{}, fmt.Errorf("unmarshal usage for tenant %q: %w", tenantID, err)
			}
			agg.Allowed += rec.Allowed
			agg.Blocked += rec.Blocked
			agg.Count += rec.Count
			agg.SumLatencyMs += rec.SumLatencyMs
			for name, n := range rec.LatencyHist {
				if agg.Histogram == nil {
					agg.Histogram = make(map[string]int64)
				}
				agg.Histogram[name] += n
			}
		}
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		input.ExclusiveStartKey = out.LastEvaluatedKey
	}
	return agg, nil
}

// PutAnomalyIfAbsent inserts an anomaly unless one already exists for the same
// tenant and detection time. The boolean reports whether a new record was
// inserted.
func (d *Dynamo) PutAnomalyIfAbsent(ctx context.Context, a *model.Anomaly) (bool, error) {
	if a == nil {
		return false, errors.New("put anomaly: nil anomaly")
	}
	item, err := attributevalue.MarshalMap(a)
	if err != nil {
		return false, fmt.Errorf("marshal anomaly for tenant %q: %w", a.TenantID, err)
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(d.anomaliesTable),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(tenant_id) AND attribute_not_exists(detected_at)"),
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return false, nil
		}
		return false, fmt.Errorf("put anomaly for tenant %q at %q: %w", a.TenantID, a.DetectedAt, err)
	}
	return true, nil
}

// MarkAnomalyAlerted sets the alerted flag on an anomaly. It returns
// ErrNotFound when the anomaly does not exist.
func (d *Dynamo) MarkAnomalyAlerted(ctx context.Context, tenantID, detectedAt string) error {
	_, err := d.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(d.anomaliesTable),
		Key: map[string]dynamodbtypes.AttributeValue{
			"tenant_id":   stringAttribute(tenantID),
			"detected_at": stringAttribute(detectedAt),
		},
		UpdateExpression:    aws.String("SET alerted = :true"),
		ConditionExpression: aws.String("attribute_exists(tenant_id) AND attribute_exists(detected_at)"),
		ExpressionAttributeValues: map[string]dynamodbtypes.AttributeValue{
			":true": &dynamodbtypes.AttributeValueMemberBOOL{Value: true},
		},
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return fmt.Errorf("mark anomaly alerted (%q, %q): %w", tenantID, detectedAt, ErrNotFound)
		}
		return fmt.Errorf("mark anomaly alerted (%q, %q): %w", tenantID, detectedAt, err)
	}
	return nil
}

// ListAnomalies returns anomalies sorted by detection time descending. A
// non-positive limit uses the default of 50; limits above 200 are capped.
func (d *Dynamo) ListAnomalies(ctx context.Context, limit int) ([]*model.Anomaly, error) {
	limit = normalizeAnomalyLimit(limit)
	input := &dynamodb.ScanInput{TableName: aws.String(d.anomaliesTable)}

	anomalies := make([]*model.Anomaly, 0)
	for {
		out, err := d.client.Scan(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("scan anomalies: %w", err)
		}
		for _, item := range out.Items {
			var a model.Anomaly
			if err := attributevalue.UnmarshalMap(item, &a); err != nil {
				return nil, fmt.Errorf("unmarshal anomaly from scan: %w", err)
			}
			anomalies = append(anomalies, &a)
		}
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		input.ExclusiveStartKey = out.LastEvaluatedKey
	}
	sortAnomaliesNewestFirst(anomalies)
	if len(anomalies) > limit {
		anomalies = anomalies[:limit]
	}
	return anomalies, nil
}

// Ping checks that the tenants table is reachable.
func (d *Dynamo) Ping(ctx context.Context) error {
	_, err := d.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(d.tenantsTable),
	})
	if err != nil {
		return fmt.Errorf("describe tenants table %q: %w", d.tenantsTable, err)
	}
	return nil
}

// Close releases no client-side resources; the AWS SDK client is stateless.
func (d *Dynamo) Close() error { return nil }

func isConditionalCheckFailed(err error) bool {
	var conditional *dynamodbtypes.ConditionalCheckFailedException
	return errors.As(err, &conditional)
}

func stringAttribute(v string) dynamodbtypes.AttributeValue {
	return &dynamodbtypes.AttributeValueMemberS{Value: v}
}

func numberAttribute(v string) dynamodbtypes.AttributeValue {
	return &dynamodbtypes.AttributeValueMemberN{Value: v}
}

// keep sort imported for deterministic output from helper-backed sorts in
// builds where a future refactor inlines the comparators.
var _ = sort.Slice
