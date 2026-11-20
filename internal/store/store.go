// Package store persists each user's saved stops.
//
// The DynamoDB layout is unchanged from earlier versions of the skill so
// existing users keep their stops: table "sfbus", partition key "userId", and
// a "stops" map of stop entries. Earlier versions only supported Muni, keyed
// entries by bare stop code and had no "agency" field; those load as Muni.
package store

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/rberrelleza/mybus/internal/transit"
)

// Stop is a stop the user follows and the lines they care about there.
type Stop struct {
	Agency string   `dynamodbav:"agency,omitempty"`
	Code   string   `dynamodbav:"code"`
	Name   string   `dynamodbav:"name"`
	Buses  []string `dynamodbav:"buses"`
}

// Stops is a user's saved stops keyed by "<agency>:<code>".
type Stops map[string]*Stop

func key(agency, code string) string { return agency + ":" + code }

// normalize upgrades entries written by earlier versions.
func normalize(raw map[string]*Stop) Stops {
	out := Stops{}
	for _, s := range raw {
		if s == nil || s.Code == "" {
			continue
		}
		if s.Agency == "" {
			s.Agency = transit.Muni
		}
		if s.Name == "" {
			s.Name = s.Code
		}
		out[key(s.Agency, s.Code)] = s
	}
	return out
}

// List returns the stops in a stable order.
func (s Stops) List() []*Stop {
	out := make([]*Stop, 0, len(s))
	for _, st := range s {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return key(out[i].Agency, out[i].Code) < key(out[j].Agency, out[j].Code) })
	return out
}

// HasBuses reports whether the user follows any line.
func (s Stops) HasBuses() bool {
	for _, st := range s {
		if len(st.Buses) > 0 {
			return true
		}
	}
	return false
}

// AddBus follows a line at a stop.
func (s Stops) AddBus(agency, code, name, line string) {
	st, ok := s[key(agency, code)]
	if !ok {
		st = &Stop{Agency: agency, Code: code, Name: name}
		s[key(agency, code)] = st
	}
	if name != "" {
		st.Name = name
	}
	for _, b := range st.Buses {
		if transit.NormalizeLine(b) == transit.NormalizeLine(line) {
			return
		}
	}
	st.Buses = append(st.Buses, line)
}

// Follows reports whether the user follows a line at a stop.
func (s Stops) Follows(agency, code, line string) bool {
	st, ok := s[key(agency, code)]
	if !ok {
		return false
	}
	for _, b := range st.Buses {
		if transit.NormalizeLine(b) == transit.NormalizeLine(line) {
			return true
		}
	}
	return false
}

// RemoveBus stops following a line at every stop, dropping stops left
// empty. It returns whether anything changed.
func (s Stops) RemoveBus(line string) bool {
	want := transit.NormalizeLine(line)
	changed := false
	for k, st := range s {
		kept := st.Buses[:0]
		for _, b := range st.Buses {
			if transit.NormalizeLine(b) == want {
				changed = true
				continue
			}
			kept = append(kept, b)
		}
		st.Buses = kept
		if len(st.Buses) == 0 {
			delete(s, k)
		}
	}
	return changed
}

// RemoveStop removes every saved stop with this code and returns them.
func (s Stops) RemoveStop(code string) []*Stop {
	var removed []*Stop
	for k, st := range s {
		if st.Code == code {
			removed = append(removed, st)
			delete(s, k)
		}
	}
	return removed
}

// Store loads and saves a user's stops.
type Store interface {
	Load(ctx context.Context, userID string) (Stops, error)
	Save(ctx context.Context, userID string, stops Stops) error
}

// DynamoDB is a Store backed by a DynamoDB table.
type DynamoDB struct {
	Client *dynamodb.Client
	Table  string
}

func (d *DynamoDB) Load(ctx context.Context, userID string) (Stops, error) {
	out, err := d.Client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(d.Table),
		Key:            map[string]types.AttributeValue{"userId": &types.AttributeValueMemberS{Value: userID}},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("loading stops: %w", err)
	}
	return decodeItem(out.Item)
}

func decodeItem(item map[string]types.AttributeValue) (Stops, error) {
	var decoded struct {
		Stops map[string]*Stop `dynamodbav:"stops"`
	}
	if err := attributevalue.UnmarshalMap(item, &decoded); err != nil {
		return nil, fmt.Errorf("decoding stops: %w", err)
	}
	return normalize(decoded.Stops), nil
}

func (d *DynamoDB) Save(ctx context.Context, userID string, stops Stops) error {
	av, err := attributevalue.Marshal(map[string]*Stop(stops))
	if err != nil {
		return fmt.Errorf("encoding stops: %w", err)
	}
	_, err = d.Client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(d.Table),
		Key:                       map[string]types.AttributeValue{"userId": &types.AttributeValueMemberS{Value: userID}},
		UpdateExpression:          aws.String("SET stops = :s"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":s": av},
	})
	if err != nil {
		return fmt.Errorf("saving stops: %w", err)
	}
	return nil
}

// Memory is an in-memory Store for tests and local experiments.
type Memory struct {
	mu   sync.Mutex
	data map[string]map[string]Stop
}

func (m *Memory) Load(_ context.Context, userID string) (Stops, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw := map[string]*Stop{}
	for k, s := range m.data[userID] {
		s.Buses = append([]string(nil), s.Buses...)
		raw[k] = &s
	}
	return normalize(raw), nil
}

func (m *Memory) Save(_ context.Context, userID string, stops Stops) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = map[string]map[string]Stop{}
	}
	copied := map[string]Stop{}
	for k, s := range stops {
		c := *s
		c.Buses = append([]string(nil), s.Buses...)
		copied[k] = c
	}
	m.data[userID] = copied
	return nil
}

// Seed stores raw entries for a user, as an earlier version might have.
func (m *Memory) Seed(userID string, entries map[string]Stop) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = map[string]map[string]Stop{}
	}
	m.data[userID] = entries
}
