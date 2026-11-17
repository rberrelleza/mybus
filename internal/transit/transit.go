// Package transit is a client for the 511.org real-time transit API (SIRI
// StopMonitoring). It replaces the services.my511.org/Transit2.0 API used by
// earlier versions of the skill, which has been shut down.
package transit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const stopMonitoringURL = "https://api.511.org/transit/StopMonitoring"

// 511.org operator ids for the agencies the skill supports.
const (
	Muni      = "SF"
	ACTransit = "AC"
)

// Agencies lists the supported agencies in lookup order.
var Agencies = []string{Muni, ACTransit}

// AgencyName returns the spoken name of an agency.
func AgencyName(agency string) string {
	switch agency {
	case Muni:
		return "Muni"
	case ACTransit:
		return "AC Transit"
	}
	return agency
}

// ErrUnavailable means 511.org could not be reached or returned an error.
var ErrUnavailable = errors.New("511.org is unavailable")

var lineSeparators = regexp.MustCompile(`[\s.\-_]`)

var spokenSuffixes = strings.NewReplacer(" RAPID", "R", " EXPRESS", "X")

// NormalizeLine makes spoken line names comparable with 511 line refs:
// "38 r", "38-R", "38 rapid" and "38R" all become "38R".
func NormalizeLine(s string) string {
	s = spokenSuffixes.Replace(" " + strings.ToUpper(strings.TrimSpace(s)))
	return lineSeparators.ReplaceAllString(s, "")
}

// Arrival is a predicted arrival of a line at a stop.
type Arrival struct {
	Line        string
	Destination string // where the vehicle is headed, e.g. "Ocean Beach"; may be empty
	Minutes     int
}

// Stop is the real-time information for a stop.
type Stop struct {
	Agency   string
	Code     string
	Name     string
	Arrivals []Arrival
}

// Lines returns the distinct lines with upcoming arrivals, in natural order.
func (s *Stop) Lines() []string {
	seen := map[string]bool{}
	var lines []string
	for _, a := range s.Arrivals {
		if !seen[a.Line] {
			seen[a.Line] = true
			lines = append(lines, a.Line)
		}
	}
	sort.Slice(lines, func(i, j int) bool { return naturalLess(lines[i], lines[j]) })
	return lines
}

// Minutes returns up to limit upcoming arrival times for a line, soonest first.
func (s *Stop) Minutes(line string, limit int) []int {
	want := NormalizeLine(line)
	var out []int
	for _, a := range s.Arrivals {
		if NormalizeLine(a.Line) == want {
			out = append(out, a.Minutes)
		}
	}
	sort.Ints(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Destinations returns where a line's upcoming vehicles are headed, in order
// of the soonest arrival for each destination.
func (s *Stop) Destinations(line string) []string {
	want := NormalizeLine(line)
	arrivals := append([]Arrival(nil), s.Arrivals...)
	sort.SliceStable(arrivals, func(i, j int) bool { return arrivals[i].Minutes < arrivals[j].Minutes })
	seen := map[string]bool{}
	var out []string
	for _, a := range arrivals {
		if NormalizeLine(a.Line) == want && !seen[a.Destination] {
			seen[a.Destination] = true
			out = append(out, a.Destination)
		}
	}
	return out
}

// MinutesTo returns up to limit arrival times for a line toward one destination.
func (s *Stop) MinutesTo(line, destination string, limit int) []int {
	want := NormalizeLine(line)
	var out []int
	for _, a := range s.Arrivals {
		if NormalizeLine(a.Line) == want && a.Destination == destination {
			out = append(out, a.Minutes)
		}
	}
	sort.Ints(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

var numberPrefix = regexp.MustCompile(`^(\d+)(.*)$`)

func naturalLess(a, b string) bool {
	ma, mb := numberPrefix.FindStringSubmatch(a), numberPrefix.FindStringSubmatch(b)
	switch {
	case ma != nil && mb != nil:
		na, _ := strconv.Atoi(ma[1])
		nb, _ := strconv.Atoi(mb[1])
		if na != nb {
			return na < nb
		}
		return ma[2] < mb[2]
	case ma != nil:
		return true
	case mb != nil:
		return false
	}
	return a < b
}

// siri mirrors the parts of the 511 StopMonitoring response the skill uses.
type siri struct {
	ServiceDelivery struct {
		StopMonitoringDelivery json.RawMessage
	}
}

type delivery struct {
	MonitoredStopVisit []struct {
		MonitoredVehicleJourney struct {
			LineRef           string
			PublishedLineName string
			DirectionRef      string
			DestinationName   siriText
			MonitoredCall     struct {
				StopPointName         string
				ExpectedArrivalTime   string
				ExpectedDepartureTime string
				AimedArrivalTime      string
				AimedDepartureTime    string
			}
		}
	}
}

// siriText is a SIRI natural-language field, which may be encoded as a
// string, a list of strings, or objects with a "value".
type siriText string

func (t *siriText) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*t = siriText(s)
		return nil
	}
	var list []json.RawMessage
	if json.Unmarshal(b, &list) == nil && len(list) > 0 {
		return t.UnmarshalJSON(list[0])
	}
	var obj struct{ Value string }
	if json.Unmarshal(b, &obj) == nil {
		*t = siriText(obj.Value)
	}
	return nil
}

// destination returns where a journey is headed, falling back to the
// direction when 511 gives no destination name.
func destination(name siriText, direction string) string {
	if d := strings.TrimSpace(string(name)); d != "" {
		return d
	}
	switch strings.ToUpper(direction) {
	case "IB", "INBOUND":
		return "inbound"
	case "OB", "OUTBOUND":
		return "outbound"
	}
	return ""
}

// Parse turns a StopMonitoring JSON payload into a Stop.
func Parse(body []byte, agency, code string, now time.Time) (*Stop, error) {
	// 511.org prefixes its JSON with a UTF-8 byte order mark.
	body = bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))
	var s siri
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("decoding 511 response: %w", err)
	}

	stop := &Stop{Agency: agency, Code: code}
	raw := bytes.TrimSpace(s.ServiceDelivery.StopMonitoringDelivery)
	if len(raw) == 0 || string(raw) == "null" {
		return stop, nil
	}
	// SIRI allows a single delivery or a list of them.
	var deliveries []delivery
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, &deliveries); err != nil {
			return nil, fmt.Errorf("decoding 511 response: %w", err)
		}
	} else {
		var d delivery
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, fmt.Errorf("decoding 511 response: %w", err)
		}
		deliveries = []delivery{d}
	}

	for _, d := range deliveries {
		for _, visit := range d.MonitoredStopVisit {
			j := visit.MonitoredVehicleJourney
			line := j.LineRef
			if line == "" {
				line = j.PublishedLineName
			}
			if line == "" {
				continue
			}
			call := j.MonitoredCall
			if stop.Name == "" {
				stop.Name = call.StopPointName
			}
			var when time.Time
			for _, v := range []string{call.ExpectedArrivalTime, call.ExpectedDepartureTime, call.AimedArrivalTime, call.AimedDepartureTime} {
				if t, err := time.Parse(time.RFC3339, v); err == nil {
					when = t
					break
				}
			}
			if when.IsZero() {
				continue
			}
			wait := when.Sub(now)
			if wait < -time.Minute { // already gone
				continue
			}
			minutes := int(wait / time.Minute)
			if minutes < 0 {
				minutes = 0
			}
			stop.Arrivals = append(stop.Arrivals, Arrival{Line: line, Destination: destination(j.DestinationName, j.DirectionRef), Minutes: minutes})
		}
	}
	return stop, nil
}

// Source looks up real-time information for a stop.
type Source interface {
	Stop(ctx context.Context, agency, code string) (*Stop, error)
}

// Client talks to 511.org. 511 rate limits API keys (60 requests per hour by
// default), so responses are cached briefly and each stop is fetched once per
// request no matter how many of its lines the user follows.
type Client struct {
	APIKey   string
	HTTP     *http.Client
	CacheTTL time.Duration
	BaseURL  string

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	at   time.Time
	stop *Stop
}

// NewClient returns a client with sensible defaults.
func NewClient(apiKey string) *Client {
	return &Client{
		APIKey:   apiKey,
		HTTP:     &http.Client{Timeout: 3 * time.Second},
		CacheTTL: 30 * time.Second,
		BaseURL:  stopMonitoringURL,
	}
}

// Stop returns real-time information for a stop. An unknown stop returns a
// Stop with no arrivals; ErrUnavailable is returned when 511 can't be used.
func (c *Client) Stop(ctx context.Context, agency, code string) (*Stop, error) {
	key := agency + ":" + code
	c.mu.Lock()
	if e, ok := c.cache[key]; ok && time.Since(e.at) < c.CacheTTL {
		c.mu.Unlock()
		return e.stop, nil
	}
	c.mu.Unlock()

	q := url.Values{"api_key": {c.APIKey}, "agency": {agency}, "stopcode": {code}, "format": {"json"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	var stop *Stop
	switch {
	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound:
		stop = &Stop{Agency: agency, Code: code} // unknown stop for this agency
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	default:
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(resp.Body); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		stop, err = Parse(buf.Bytes(), agency, code, time.Now())
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
	}

	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[string]cached{}
	}
	c.cache[key] = cached{at: time.Now(), stop: stop}
	c.mu.Unlock()
	return stop, nil
}
