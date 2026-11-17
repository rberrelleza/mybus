package store

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func s(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }

// An item exactly as the original Python/boto3 version of the skill wrote it.
func legacyItem() map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"userId": s("amzn1.ask.account.TEST"),
		"stops": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
			"15651": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
				"code":  s("15651"),
				"name":  s("Geary Blvd & Masonic Ave"),
				"buses": &types.AttributeValueMemberL{Value: []types.AttributeValue{s("38"), s("38R")}},
			}},
		}},
	}
}

func TestDecodeLegacyItem(t *testing.T) {
	stops, err := decodeItem(legacyItem())
	if err != nil {
		t.Fatal(err)
	}
	st, ok := stops["SF:15651"]
	if !ok {
		t.Fatalf("legacy stop not migrated to agency key: %v", stops)
	}
	if st.Agency != "SF" || st.Name != "Geary Blvd & Masonic Ave" || len(st.Buses) != 2 {
		t.Fatalf("unexpected stop %+v", st)
	}
}

func TestDecodeMissingItem(t *testing.T) {
	stops, err := decodeItem(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(stops) != 0 || stops.HasBuses() {
		t.Fatalf("expected no stops, got %v", stops)
	}
}

func TestAddAndRemove(t *testing.T) {
	stops := Stops{}
	stops.AddBus("SF", "15651", "Geary", "38")
	stops.AddBus("SF", "15651", "Geary", "38") // duplicate ignored
	stops.AddBus("SF", "15651", "Geary", "38R")
	stops.AddBus("AC", "55555", "Broadway", "51A")
	stops.AddBus("SF", "13915", "Mission", "38R")

	if got := len(stops["SF:15651"].Buses); got != 2 {
		t.Fatalf("expected 2 buses, got %d", got)
	}

	// Removing a line removes it from every stop, and drops empty stops.
	if !stops.RemoveBus("38 r") {
		t.Fatal("expected change")
	}
	if _, ok := stops["SF:13915"]; ok {
		t.Fatal("empty stop should be removed")
	}
	if got := stops["SF:15651"].Buses; len(got) != 1 || got[0] != "38" {
		t.Fatalf("unexpected buses %v", got)
	}
	if stops.RemoveBus("99") {
		t.Fatal("expected no change")
	}

	removed := stops.RemoveStop("55555")
	if len(removed) != 1 || removed[0].Agency != "AC" {
		t.Fatalf("unexpected removed %v", removed)
	}
}
