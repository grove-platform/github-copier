package services

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// AuditEventType represents the type of audit event
type AuditEventType string

const (
	AuditEventCopy        AuditEventType = "copy"
	AuditEventDeprecation AuditEventType = "deprecation"
	AuditEventError       AuditEventType = "error"
)

// AuditEvent represents an audit log entry
type AuditEvent struct {
	ID             string         `bson:"_id,omitempty" json:"id,omitempty"`
	Timestamp      time.Time      `bson:"timestamp" json:"timestamp"`
	EventType      AuditEventType `bson:"event_type" json:"event_type"`
	RuleName       string         `bson:"rule_name,omitempty" json:"rule_name,omitempty"`
	SourceRepo     string         `bson:"source_repo" json:"source_repo"`
	SourcePath     string         `bson:"source_path" json:"source_path"`
	TargetRepo     string         `bson:"target_repo,omitempty" json:"target_repo,omitempty"`
	TargetPath     string         `bson:"target_path,omitempty" json:"target_path,omitempty"`
	CommitSHA      string         `bson:"commit_sha,omitempty" json:"commit_sha,omitempty"`
	PRNumber       int            `bson:"pr_number,omitempty" json:"pr_number,omitempty"`
	Success        bool           `bson:"success" json:"success"`
	ErrorMessage   string         `bson:"error_message,omitempty" json:"error_message,omitempty"`
	DurationMs     int64          `bson:"duration_ms,omitempty" json:"duration_ms,omitempty"`
	FileSize       int64          `bson:"file_size,omitempty" json:"file_size,omitempty"`
	AdditionalData map[string]any `bson:"additional_data,omitempty" json:"additional_data,omitempty"`
}

// AuditLogger handles audit logging to MongoDB
type AuditLogger interface {
	LogCopyEvent(ctx context.Context, event *AuditEvent) error
	LogDeprecationEvent(ctx context.Context, event *AuditEvent) error
	LogErrorEvent(ctx context.Context, event *AuditEvent) error
	GetRecentEvents(ctx context.Context, limit int) ([]AuditEvent, error)
	GetFailedEvents(ctx context.Context, limit int) ([]AuditEvent, error)
	GetEventsByRule(ctx context.Context, ruleName string, limit int) ([]AuditEvent, error)
	GetStatsByRule(ctx context.Context) (map[string]RuleStats, error)
	GetDailyVolume(ctx context.Context, days int) ([]DailyStats, error)
	QueryAuditEvents(ctx context.Context, q AuditListQuery) ([]AuditEvent, error)
	Ping(ctx context.Context) error
	Close(ctx context.Context) error
}

// AuditListQuery filters audit rows for operator dashboards and APIs.
type AuditListQuery struct {
	Limit      int
	EventType  string     // empty = any; otherwise copy | deprecation | error
	Success    *bool      // nil = any
	RuleName   string     // exact match when non-empty
	PRNumber   *int       // nil = any; exact match when set
	PathSearch string     // substring match on source_path OR target_path when non-empty
	Since      *time.Time // inclusive lower bound on timestamp when set
}

// RuleStats represents statistics for a specific rule
type RuleStats struct {
	RuleName     string  `bson:"_id" json:"rule_name"`
	TotalCopies  int     `bson:"total_copies" json:"total_copies"`
	SuccessCount int     `bson:"success_count" json:"success_count"`
	FailureCount int     `bson:"failure_count" json:"failure_count"`
	AvgDuration  float64 `bson:"avg_duration" json:"avg_duration_ms"`
}

// DailyStats represents daily copy volume statistics
type DailyStats struct {
	Date         string `bson:"_id" json:"date"`
	TotalCopies  int    `bson:"total_copies" json:"total_copies"`
	SuccessCount int    `bson:"success_count" json:"success_count"`
	FailureCount int    `bson:"failure_count" json:"failure_count"`
}

// MongoAuditLogger implements AuditLogger using MongoDB
type MongoAuditLogger struct {
	client     *mongo.Client
	collection *mongo.Collection
	enabled    bool
}

// NewMongoAuditLogger creates a new MongoDB audit logger
func NewMongoAuditLogger(ctx context.Context, mongoURI, database, collection string, enabled bool) (AuditLogger, error) {
	if !enabled {
		return &NoOpAuditLogger{}, nil
	}

	if mongoURI == "" {
		return nil, fmt.Errorf("MONGO_URI is required when audit logging is enabled")
	}

	clientOptions := options.Client().
		ApplyURI(mongoURI).
		SetServerSelectionTimeout(5 * time.Second).
		SetConnectTimeout(5 * time.Second).
		SetTimeout(10 * time.Second).
		SetMaxPoolSize(10).
		SetRetryWrites(true).
		SetBSONOptions(&options.BSONOptions{
			ObjectIDAsHexString: true,
		})
	client, err := mongo.Connect(clientOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MongoDB: %w", err)
	}

	// Ping to verify connection
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("failed to ping MongoDB: %w", err)
	}

	coll := client.Database(database).Collection(collection)

	// Create indexes
	logger := &MongoAuditLogger{
		client:     client,
		collection: coll,
		enabled:    enabled,
	}

	if err := logger.createIndexes(ctx); err != nil {
		return nil, fmt.Errorf("failed to create indexes: %w", err)
	}

	return logger, nil
}

// createIndexes creates necessary indexes for the audit collection
func (mal *MongoAuditLogger) createIndexes(ctx context.Context) error {
	indexes := []mongo.IndexModel{
		{
			Keys: bson.D{{Key: "timestamp", Value: -1}},
		},
		{
			Keys: bson.D{{Key: "event_type", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "rule_name", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "success", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "source_repo", Value: 1}},
		},
	}

	_, err := mal.collection.Indexes().CreateMany(ctx, indexes)
	return err
}

// LogCopyEvent logs a file copy event
func (mal *MongoAuditLogger) LogCopyEvent(ctx context.Context, event *AuditEvent) error {
	event.EventType = AuditEventCopy
	event.Timestamp = time.Now()
	_, err := mal.collection.InsertOne(ctx, event)
	return err
}

// LogDeprecationEvent logs a file deprecation event
func (mal *MongoAuditLogger) LogDeprecationEvent(ctx context.Context, event *AuditEvent) error {
	event.EventType = AuditEventDeprecation
	event.Timestamp = time.Now()
	_, err := mal.collection.InsertOne(ctx, event)
	return err
}

// LogErrorEvent logs an error event
func (mal *MongoAuditLogger) LogErrorEvent(ctx context.Context, event *AuditEvent) error {
	event.EventType = AuditEventError
	event.Timestamp = time.Now()
	event.Success = false
	_, err := mal.collection.InsertOne(ctx, event)
	return err
}

// QueryAuditEvents retrieves audit events matching the given filter criteria.
func (mal *MongoAuditLogger) QueryAuditEvents(ctx context.Context, q AuditListQuery) ([]AuditEvent, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	filter := bson.M{}
	if q.EventType != "" {
		filter["event_type"] = AuditEventType(q.EventType)
	}
	if q.Success != nil {
		filter["success"] = *q.Success
	}
	if q.RuleName != "" {
		filter["rule_name"] = q.RuleName
	}
	if q.PRNumber != nil {
		filter["pr_number"] = *q.PRNumber
	}
	if q.PathSearch != "" {
		filter["$or"] = bson.A{
			bson.M{"source_path": bson.M{"$regex": q.PathSearch, "$options": "i"}},
			bson.M{"target_path": bson.M{"$regex": q.PathSearch, "$options": "i"}},
		}
	}
	if q.Since != nil {
		filter["timestamp"] = bson.M{"$gte": *q.Since}
	}
	opts := options.Find().SetSort(bson.D{{Key: "timestamp", Value: -1}}).SetLimit(int64(limit))
	cursor, err := mal.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var events []AuditEvent
	if err := cursor.All(ctx, &events); err != nil {
		return nil, err
	}
	return events, nil
}

func (mal *MongoAuditLogger) GetRecentEvents(ctx context.Context, limit int) ([]AuditEvent, error) {
	opts := options.Find().SetSort(bson.D{{Key: "timestamp", Value: -1}}).SetLimit(int64(limit))
	cursor, err := mal.collection.Find(ctx, bson.M{}, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var events []AuditEvent
	if err := cursor.All(ctx, &events); err != nil {
		return nil, err
	}
	return events, nil
}

// GetFailedEvents retrieves recent failed events
func (mal *MongoAuditLogger) GetFailedEvents(ctx context.Context, limit int) ([]AuditEvent, error) {
	filter := bson.M{"success": false}
	opts := options.Find().SetSort(bson.D{{Key: "timestamp", Value: -1}}).SetLimit(int64(limit))
	cursor, err := mal.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var events []AuditEvent
	if err := cursor.All(ctx, &events); err != nil {
		return nil, err
	}
	return events, nil
}

// GetEventsByRule retrieves events for a specific rule
func (mal *MongoAuditLogger) GetEventsByRule(ctx context.Context, ruleName string, limit int) ([]AuditEvent, error) {
	filter := bson.M{"rule_name": ruleName}
	opts := options.Find().SetSort(bson.D{{Key: "timestamp", Value: -1}}).SetLimit(int64(limit))
	cursor, err := mal.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var events []AuditEvent
	if err := cursor.All(ctx, &events); err != nil {
		return nil, err
	}
	return events, nil
}

// GetStatsByRule retrieves statistics grouped by rule
func (mal *MongoAuditLogger) GetStatsByRule(ctx context.Context) (map[string]RuleStats, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"event_type": AuditEventCopy}}},
		{{Key: "$group", Value: bson.M{
			"_id":           "$rule_name",
			"total_copies":  bson.M{"$sum": 1},
			"success_count": bson.M{"$sum": bson.M{"$cond": []any{"$success", 1, 0}}},
			"failure_count": bson.M{"$sum": bson.M{"$cond": []any{"$success", 0, 1}}},
			"avg_duration":  bson.M{"$avg": "$duration_ms"},
		}}},
	}

	cursor, err := mal.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var stats []RuleStats
	if err := cursor.All(ctx, &stats); err != nil {
		return nil, err
	}

	result := make(map[string]RuleStats)
	for _, stat := range stats {
		result[stat.RuleName] = stat
	}
	return result, nil
}

// GetDailyVolume retrieves daily copy volume statistics
func (mal *MongoAuditLogger) GetDailyVolume(ctx context.Context, days int) ([]DailyStats, error) {
	startDate := time.Now().AddDate(0, 0, -days)

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"event_type": AuditEventCopy,
			"timestamp":  bson.M{"$gte": startDate},
		}}},
		{{Key: "$group", Value: bson.M{
			"_id": bson.M{
				"$dateToString": bson.M{
					"format": "%Y-%m-%d",
					"date":   "$timestamp",
				},
			},
			"total_copies":  bson.M{"$sum": 1},
			"success_count": bson.M{"$sum": bson.M{"$cond": []any{"$success", 1, 0}}},
			"failure_count": bson.M{"$sum": bson.M{"$cond": []any{"$success", 0, 1}}},
		}}},
		{{Key: "$sort", Value: bson.M{"_id": 1}}},
	}

	cursor, err := mal.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var stats []DailyStats
	if err := cursor.All(ctx, &stats); err != nil {
		return nil, err
	}
	return stats, nil
}

// Ping checks MongoDB connectivity.
func (mal *MongoAuditLogger) Ping(ctx context.Context) error {
	return mal.client.Ping(ctx, nil)
}

// Close closes the MongoDB connection.
func (mal *MongoAuditLogger) Close(ctx context.Context) error {
	return mal.client.Disconnect(ctx)
}

// NoOpAuditLogger is a no-op implementation when audit logging is disabled
type NoOpAuditLogger struct{}

func (nal *NoOpAuditLogger) LogCopyEvent(ctx context.Context, event *AuditEvent) error { return nil }
func (nal *NoOpAuditLogger) LogDeprecationEvent(ctx context.Context, event *AuditEvent) error {
	return nil
}
func (nal *NoOpAuditLogger) LogErrorEvent(ctx context.Context, event *AuditEvent) error { return nil }
func (nal *NoOpAuditLogger) GetRecentEvents(ctx context.Context, limit int) ([]AuditEvent, error) {
	return []AuditEvent{}, nil
}
func (nal *NoOpAuditLogger) GetFailedEvents(ctx context.Context, limit int) ([]AuditEvent, error) {
	return []AuditEvent{}, nil
}
func (nal *NoOpAuditLogger) GetEventsByRule(ctx context.Context, ruleName string, limit int) ([]AuditEvent, error) {
	return []AuditEvent{}, nil
}
func (nal *NoOpAuditLogger) GetStatsByRule(ctx context.Context) (map[string]RuleStats, error) {
	return map[string]RuleStats{}, nil
}
func (nal *NoOpAuditLogger) GetDailyVolume(ctx context.Context, days int) ([]DailyStats, error) {
	return []DailyStats{}, nil
}
func (nal *NoOpAuditLogger) QueryAuditEvents(ctx context.Context, q AuditListQuery) ([]AuditEvent, error) {
	return []AuditEvent{}, nil
}
func (nal *NoOpAuditLogger) Ping(ctx context.Context) error  { return nil }
func (nal *NoOpAuditLogger) Close(ctx context.Context) error { return nil }
