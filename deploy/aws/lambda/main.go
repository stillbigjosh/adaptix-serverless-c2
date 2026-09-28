package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
)

var (
	dbClient      *dynamodb.Client
	inboundTable  string
	outboundTable string
	ttlHours      = 24
	relayAPIKey   string
)

type inboundRecord struct {
	ID        string `dynamodbav:"id"`
	AgentID   string `dynamodbav:"agent_id"`
	Timestamp string `dynamodbav:"timestamp"`
	RawData   []byte `dynamodbav:"raw_data"`
	Processed bool   `dynamodbav:"processed"`
	TTL       int64  `dynamodbav:"ttl"`
}

func init() {
	inboundTable = os.Getenv("INBOUND_TABLE")
	if inboundTable == "" {
		inboundTable = "adaptix-inbound"
	}
	outboundTable = os.Getenv("OUTBOUND_TABLE")
	if outboundTable == "" {
		outboundTable = "adaptix-outbound"
	}

	relayAPIKey = os.Getenv("RELAY_API_KEY")

	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}
	dbClient = dynamodb.NewFromConfig(cfg)
}

func handler(ctx context.Context, req events.LambdaFunctionURLRequest) (events.LambdaFunctionURLResponse, error) {
	if relayAPIKey != "" {
		key := req.Headers["x-relay-key"]
		if key == "" {
			key = req.QueryStringParameters["key"]
		}
		if key != relayAPIKey {
			return errorResponse(http.StatusUnauthorized, "unauthorized"), nil
		}
	}

	switch req.RawPath {
	case "/relay":
		return handleRelay(ctx, req)
	default:
		return events.LambdaFunctionURLResponse{
			StatusCode: http.StatusNotFound,
			Body:       "Not Found",
		}, nil
	}
}

func handleRelay(ctx context.Context, req events.LambdaFunctionURLRequest) (events.LambdaFunctionURLResponse, error) {
	var body []byte

	// POST: data is in the HTTP body (Function URL base64-wraps binary bodies).
	// GET:  data is in the "id" query parameter (profile client_output.format = "base64").
	if req.IsBase64Encoded {
		var err error
		body, err = base64.StdEncoding.DecodeString(req.Body)
		if err != nil {
			body = []byte(req.Body)
		}
	} else {
		body = []byte(req.Body)
	}

	// For GET requests the body is typically empty; extract from query params.
	if len(body) == 0 {
		if idParam, ok := req.QueryStringParameters["id"]; ok && len(idParam) > 0 {
			body = []byte(idParam)
		}
	}

	if len(body) == 0 {
		return errorResponse(http.StatusBadRequest, "empty body"), nil
	}

	agentID := extractAgentID(body)

	rec := inboundRecord{
		ID:        uuid.New().String(),
		AgentID:   agentID,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		RawData:   body,
		Processed: false,
		TTL:       time.Now().Add(time.Duration(ttlHours) * time.Hour).Unix(),
	}

	item, err := attributevalue.MarshalMap(rec)
	if err != nil {
		log.Printf("marshal error: %v", err)
		return errorResponse(http.StatusInternalServerError, "internal"), nil
	}

	_, err = dbClient.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &inboundTable,
		Item:      item,
	})
	if err != nil {
		log.Printf("put inbound error: %v", err)
		return errorResponse(http.StatusInternalServerError, "internal"), nil
	}

	// Check for pending outbound response for this agent.
	// The listener polls DynamoDB asynchronously, so the response may not be
	// ready yet (e.g. during registration). Poll for a few seconds to let the
	// listener process the inbound record and queue a response.
	var response []byte
	for attempt := 0; attempt < 8; attempt++ {
		var err error
		response, err = getPendingResponse(ctx, agentID)
		if err != nil {
			log.Printf("get outbound error: %v", err)
			return errorResponse(http.StatusInternalServerError, "internal"), nil
		}
		if response != nil {
			break
		}
		if attempt < 7 {
			time.Sleep(1 * time.Second)
		}
	}

	// Response data from the listener is already encoded per the malleable
	// profile's server_output.format (e.g. base64 text). Send it as-is.
	// Do NOT set IsBase64Encoded=true, which would cause Lambda Function URL
	// to decode the base64 and send raw binary, breaking the profile contract.
	return events.LambdaFunctionURLResponse{
		StatusCode: http.StatusOK,
		Body:       string(response),
	}, nil
}

// extractAgentID tries to get the 8-byte agent ID from the raw body.
// For POST (raw binary), the first 8 bytes are ASCII hex agent ID.
// For GET (base64 text), decode first then read the 8-byte prefix.
func extractAgentID(body []byte) string {
	// Try raw binary first: check if first 8 bytes are hex chars
	if len(body) >= 8 && isHexString(body[:8]) {
		return string(body[:8])
	}

	// Try base64 decode (GET requests where agent base64-encodes its output)
	if decoded, err := base64.StdEncoding.DecodeString(string(body)); err == nil && len(decoded) >= 8 {
		if isHexString(decoded[:8]) {
			return string(decoded[:8])
		}
	}

	return "unknown"
}

func isHexString(b []byte) bool {
	for _, c := range b {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

func getPendingResponse(ctx context.Context, agentID string) ([]byte, error) {
	indexName := "agent_id-index"
	out, err := dbClient.Query(ctx, &dynamodb.QueryInput{
		TableName:              &outboundTable,
		IndexName:              &indexName,
		KeyConditionExpression: aws.String("agent_id = :aid"),
		FilterExpression:       aws.String("#del = :f"),
		ExpressionAttributeNames: map[string]string{"#del": "delivered"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":aid": &types.AttributeValueMemberS{Value: agentID},
			":f":   &types.AttributeValueMemberBOOL{Value: false},
		},
		ScanIndexForward: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("query outbound: %w", err)
	}

	if len(out.Items) == 0 {
		return nil, nil
	}

	newestIdx := 0
	newestTS := ""
	for i, item := range out.Items {
		if v, ok := item["timestamp"].(*types.AttributeValueMemberS); ok {
			if v.Value > newestTS {
				newestTS = v.Value
				newestIdx = i
			}
		}
	}

	var data []byte
	if v, ok := out.Items[newestIdx]["response_data"].(*types.AttributeValueMemberB); ok {
		data = v.Value
	}

	for _, item := range out.Items {
		var id string
		if v, ok := item["id"].(*types.AttributeValueMemberS); ok {
			id = v.Value
		}
		if id == "" {
			continue
		}
		_, markErr := dbClient.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName: &outboundTable,
			Key: map[string]types.AttributeValue{
				"id": &types.AttributeValueMemberS{Value: id},
			},
			UpdateExpression:         aws.String("SET #del = :t"),
			ExpressionAttributeNames: map[string]string{"#del": "delivered"},
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":t": &types.AttributeValueMemberBOOL{Value: true},
			},
		})
		if markErr != nil {
			log.Printf("mark delivered error for %s: %v", id, markErr)
		}
	}

	return data, nil
}

func errorResponse(code int, msg string) events.LambdaFunctionURLResponse {
	return events.LambdaFunctionURLResponse{
		StatusCode: code,
		Body:       msg,
	}
}

func main() {
	lambda.Start(handler)
}
