package transit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 17, 18, 0, 0, 0, time.UTC)

// Shape of a 511.org StopMonitoring response, including the leading BOM.
const sample = "\xef\xbb\xbf" + `{"ServiceDelivery":{"ResponseTimestamp":"2026-11-17T18:00:00Z","StopMonitoringDelivery":{"version":"1.4","MonitoredStopVisit":[
 {"MonitoredVehicleJourney":{"LineRef":"38R","PublishedLineName":"GEARY RAPID","MonitoredCall":{"StopPointRef":"15651","StopPointName":"Geary Blvd & Masonic Ave","AimedArrivalTime":"2026-11-17T18:04:00Z","ExpectedArrivalTime":"2026-11-17T18:05:30Z"}}},
 {"MonitoredVehicleJourney":{"LineRef":"38","PublishedLineName":"GEARY","MonitoredCall":{"StopPointName":"Geary Blvd & Masonic Ave","ExpectedArrivalTime":"2026-11-17T18:12:00Z"}}},
 {"MonitoredVehicleJourney":{"LineRef":"38","MonitoredCall":{"ExpectedArrivalTime":"2026-11-17T18:00:20Z"}}},
 {"MonitoredVehicleJourney":{"LineRef":"38","MonitoredCall":{"ExpectedArrivalTime":"2026-11-17T17:55:00Z"}}},
 {"MonitoredVehicleJourney":{"LineRef":"38","MonitoredCall":{"ExpectedArrivalTime":null,"AimedArrivalTime":"2026-11-17T18:30:00Z"}}},
 {"MonitoredVehicleJourney":{"LineRef":"5","MonitoredCall":{"ExpectedArrivalTime":"2026-11-17T18:07:00Z"}}}
]}}}`

func TestParse(t *testing.T) {
	stop, err := Parse([]byte(sample), Muni, "15651", now)
	if err != nil {
		t.Fatal(err)
	}
	if stop.Name != "Geary Blvd & Masonic Ave" {
		t.Errorf("name = %q", stop.Name)
	}
	if got, want := stop.Lines(), []string{"5", "38", "38R"}; !reflect.DeepEqual(got, want) {
		t.Errorf("lines = %v, want %v", got, want)
	}
	// Departed bus (17:55) is skipped; times are sorted and capped at 3.
	if got, want := stop.Minutes("38", 3), []int{0, 12, 30}; !reflect.DeepEqual(got, want) {
		t.Errorf("38 minutes = %v, want %v", got, want)
	}
	if got, want := stop.Minutes("38 r", 3), []int{5}; !reflect.DeepEqual(got, want) {
		t.Errorf("38R minutes = %v, want %v", got, want)
	}
}

func TestParseDeliveryList(t *testing.T) {
	body := `{"ServiceDelivery":{"StopMonitoringDelivery":[{"MonitoredStopVisit":[{"MonitoredVehicleJourney":{"LineRef":"51A","MonitoredCall":{"StopPointName":"Broadway & 14th St","ExpectedArrivalTime":"2026-11-17T10:03:00-08:00"}}}]}]}}`
	stop, err := Parse([]byte(body), ACTransit, "55555", now)
	if err != nil {
		t.Fatal(err)
	}
	if got := stop.Minutes("51A", 3); !reflect.DeepEqual(got, []int{3}) {
		t.Errorf("minutes = %v", got)
	}
}

func TestParseEmpty(t *testing.T) {
	for _, body := range []string{
		`{"ServiceDelivery":{"StopMonitoringDelivery":{"MonitoredStopVisit":[]}}}`,
		`{"ServiceDelivery":{}}`,
	} {
		stop, err := Parse([]byte(body), Muni, "1", now)
		if err != nil {
			t.Fatal(err)
		}
		if len(stop.Lines()) != 0 {
			t.Errorf("expected no lines for %s", body)
		}
	}
}

func TestNaturalOrder(t *testing.T) {
	stop := &Stop{Arrivals: []Arrival{{Line: "N"}, {Line: "10"}, {Line: "2"}, {Line: "1AX"}, {Line: "1"}, {Line: "F"}}}
	if got, want := stop.Lines(), []string{"1", "1AX", "2", "10", "F", "N"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestClient(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		if q.Get("api_key") != "key" || q.Get("agency") != "SF" || q.Get("format") != "json" {
			t.Errorf("unexpected query %v", q)
		}
		switch q.Get("stopcode") {
		case "15651":
			_, _ = w.Write([]byte(sample))
		case "404":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()

	c := NewClient("key")
	c.BaseURL = srv.URL
	ctx := context.Background()

	stop, err := c.Stop(ctx, Muni, "15651")
	if err != nil || stop.Name == "" {
		t.Fatalf("stop=%v err=%v", stop, err)
	}
	if _, err := c.Stop(ctx, Muni, "15651"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Errorf("expected cached second call, got %d calls", calls.Load())
	}

	stop, err = c.Stop(ctx, Muni, "404")
	if err != nil || len(stop.Lines()) != 0 {
		t.Errorf("unknown stop: stop=%v err=%v", stop, err)
	}

	if _, err := c.Stop(ctx, Muni, "401"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("expected ErrUnavailable, got %v", err)
	}
}

func TestParseDestinations(t *testing.T) {
	body := `{"ServiceDelivery":{"StopMonitoringDelivery":{"MonitoredStopVisit":[
	 {"MonitoredVehicleJourney":{"LineRef":"38","DirectionRef":"OB","DestinationName":"Ocean Beach","MonitoredCall":{"ExpectedArrivalTime":"2026-11-17T18:03:00Z"}}},
	 {"MonitoredVehicleJourney":{"LineRef":"38","DirectionRef":"OB","DestinationName":["Fort Miley"],"MonitoredCall":{"ExpectedArrivalTime":"2026-11-17T18:07:00Z"}}},
	 {"MonitoredVehicleJourney":{"LineRef":"38","DirectionRef":"OB","DestinationName":"Ocean Beach","MonitoredCall":{"ExpectedArrivalTime":"2026-11-17T18:15:00Z"}}},
	 {"MonitoredVehicleJourney":{"LineRef":"43","DirectionRef":"IB","MonitoredCall":{"ExpectedArrivalTime":"2026-11-17T18:09:00Z"}}}
	]}}}`
	stop, err := Parse([]byte(body), Muni, "15651", now)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stop.Destinations("38"), []string{"Ocean Beach", "Fort Miley"}; !reflect.DeepEqual(got, want) {
		t.Errorf("destinations = %v, want %v", got, want)
	}
	if got := stop.MinutesTo("38", "Ocean Beach", 3); !reflect.DeepEqual(got, []int{3, 15}) {
		t.Errorf("minutes = %v", got)
	}
	if got := stop.Destinations("43"); !reflect.DeepEqual(got, []string{"inbound"}) {
		t.Errorf("direction fallback = %v", got)
	}
}

func TestNormalizeSpokenLines(t *testing.T) {
	for in, want := range map[string]string{"38 rapid": "38R", "38 r": "38R", "8 express": "8X", "k owl": "KOWL", "N": "N", " 51a ": "51A"} {
		if got := NormalizeLine(in); got != want {
			t.Errorf("NormalizeLine(%q) = %q, want %q", in, got, want)
		}
	}
}
