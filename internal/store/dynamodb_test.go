package store

import (
	"context"
	"regexp"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/Aswanidev-vs/RateGuard/internal/model"
)

type capturingDynamo struct {
	dynamoAPI
	input *dynamodb.UpdateItemInput
}

func (c *capturingDynamo) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	c.input = in
	return &dynamodb.UpdateItemOutput{}, nil
}

// TestAddUsageExpressionPlaceholdersMatch guards against expression/value name
// drift (for example :sum_latency_ms vs :sum_latency), which DynamoDB rejects
// at runtime and local tests would otherwise never see.
func TestAddUsageExpressionPlaceholdersMatch(t *testing.T) {
	client := &capturingDynamo{}
	d := &Dynamo{client: client, usageTable: "api_guard_usage"}

	err := d.AddUsage(context.Background(), []model.UsageEntry{{
		TenantID: "tn_test",
		Bucket:   "2026-11-20T12",
		UsageDelta: model.UsageDelta{
			Allowed:       2,
			Blocked:       1,
			Count:         1,
			SumLatencyMs:  7,
			LatencyBucket: "5-9",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if client.input == nil || client.input.UpdateExpression == nil {
		t.Fatal("no UpdateItem call captured")
	}

	expr := *client.input.UpdateExpression
	for _, value := range regexp.MustCompile(`:[A-Za-z0-9_]+`).FindAllString(expr, -1) {
		if _, ok := client.input.ExpressionAttributeValues[value]; !ok {
			t.Errorf("expression references %s without a value", value)
		}
	}
	for _, name := range regexp.MustCompile(`#[A-Za-z0-9_]+`).FindAllString(expr, -1) {
		if _, ok := client.input.ExpressionAttributeNames[name]; !ok {
			t.Errorf("expression references %s without a name", name)
		}
	}
	for value := range client.input.ExpressionAttributeValues {
		found := false
		for _, used := range regexp.MustCompile(`:[A-Za-z0-9_]+`).FindAllString(expr, -1) {
			if used == value {
				found = true
			}
		}
		if !found {
			t.Errorf("unused expression value %s", value)
		}
	}
}
