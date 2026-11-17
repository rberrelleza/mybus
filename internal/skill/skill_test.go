package skill

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rberrelleza/mybus/internal/alexa"
	"github.com/rberrelleza/mybus/internal/store"
	"github.com/rberrelleza/mybus/internal/transit"
)

const (
	skillID = "amzn1.ask.skill.test"
	userID  = "amzn1.ask.account.test"
)

type fakeTransit struct {
	stops map[string]*transit.Stop // "<agency>:<code>"
	down  bool
	calls int
}

func (f *fakeTransit) Stop(_ context.Context, agency, code string) (*transit.Stop, error) {
	f.calls++
	if f.down {
		return nil, transit.ErrUnavailable
	}
	if s, ok := f.stops[agency+":"+code]; ok {
		return s, nil
	}
	return &transit.Stop{Agency: agency, Code: code}, nil
}

func geary() *transit.Stop {
	return &transit.Stop{Agency: "SF", Code: "15651", Name: "Geary Blvd & Masonic Ave", Arrivals: []transit.Arrival{
		{Line: "38", Minutes: 3}, {Line: "38", Minutes: 11}, {Line: "38R", Minutes: 0}, {Line: "38", Minutes: 25}, {Line: "38", Minutes: 40},
	}}
}

func newSkill() (*Skill, *fakeTransit, *store.Memory) {
	ft := &fakeTransit{stops: map[string]*transit.Stop{
		"SF:15651": geary(),
		"SF:13915": {Agency: "SF", Code: "13915", Name: "Mission St & 16th St", Arrivals: []transit.Arrival{{Line: "14", Minutes: 4}}},
		"AC:55555": {Agency: "AC", Code: "55555", Name: "Broadway & 14th St", Arrivals: []transit.Arrival{{Line: "51A", Minutes: 6}, {Line: "NL", Minutes: 9}}},
		// A stop code that exists for both agencies.
		"SF:12345": {Agency: "SF", Code: "12345", Name: "Muni stop", Arrivals: []transit.Arrival{{Line: "5", Minutes: 1}}},
		"AC:12345": {Agency: "AC", Code: "12345", Name: "AC stop", Arrivals: []transit.Arrival{{Line: "72", Minutes: 1}}},
	}}
	mem := &store.Memory{}
	return &Skill{ID: skillID, Transit: ft, Store: mem}, ft, mem
}

func envelope(reqType, intent string, slots map[string]alexa.Slot) *alexa.RequestEnvelope {
	e := &alexa.RequestEnvelope{Version: "1.0"}
	e.Context.System.Application.ApplicationID = skillID
	e.Context.System.User.UserID = userID
	e.Request.Type = reqType
	e.Request.RequestID = "req"
	if intent != "" {
		e.Request.Intent = &alexa.Intent{Name: intent, Slots: slots}
	}
	return e
}

func intent(name string, values map[string]string) *alexa.RequestEnvelope {
	slots := map[string]alexa.Slot{}
	for k, v := range values {
		slots[k] = alexa.Slot{Name: k, Value: v}
	}
	return envelope(alexa.IntentRequest, name, slots)
}

func resolved(e *alexa.RequestEnvelope, slot, id string) *alexa.RequestEnvelope {
	s := e.Request.Intent.Slots[slot]
	s.Resolutions = &alexa.Resolutions{ResolutionsPerAuthority: []alexa.Authority{{
		Status: alexa.AuthorityStatus{Code: "ER_SUCCESS_MATCH"},
		Values: []alexa.ResolvedValues{{Value: alexa.ResolvedValue{ID: id}}},
	}}}
	e.Request.Intent.Slots[slot] = s
	return e
}

func handle(t *testing.T, s *Skill, e *alexa.RequestEnvelope) *alexa.ResponseEnvelope {
	t.Helper()
	resp, err := s.Handle(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	// Every response must serialize to valid JSON with well-formed SSML.
	if _, err := json.Marshal(resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func speech(r *alexa.ResponseEnvelope) string {
	if r.Response.OutputSpeech == nil {
		return ""
	}
	return r.Response.OutputSpeech.SSML
}

func ends(r *alexa.ResponseEnvelope) bool {
	return r.Response.ShouldEndSession != nil && *r.Response.ShouldEndSession
}

func elicited(r *alexa.ResponseEnvelope) string {
	for _, d := range r.Response.Directives {
		if e, ok := d.(alexa.ElicitSlotDirective); ok {
			return e.SlotToElicit
		}
	}
	return ""
}

func contains(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("speech %q does not contain %q", got, w)
		}
	}
}

func TestRejectsOtherSkills(t *testing.T) {
	s, _, _ := newSkill()
	e := envelope(alexa.LaunchRequest, "", nil)
	e.Context.System.Application.ApplicationID = "amzn1.ask.skill.other"
	if _, err := s.Handle(context.Background(), e); !errors.Is(err, ErrWrongSkill) {
		t.Fatalf("expected ErrWrongSkill, got %v", err)
	}
}

func TestLaunchWithoutStopsExplainsSetup(t *testing.T) {
	s, _, _ := newSkill()
	r := handle(t, s, envelope(alexa.LaunchRequest, "", nil))
	contains(t, speech(r), "stop number", `<say-as interpret-as="digits">15651</say-as>`)
	if ends(r) || r.Response.Reprompt == nil {
		t.Error("expected the session to stay open with a reprompt")
	}
}

func TestAddStopWithOneLineSavesIt(t *testing.T) {
	s, _, mem := newSkill()
	r := handle(t, s, intent(intentAddStop, map[string]string{slotStop: "13915"}))
	contains(t, speech(r), "I added the 14 at Mission St &amp; 16th St")
	stops, _ := mem.Load(context.Background(), userID)
	if got := stops["SF:13915"]; got == nil || got.Buses[0] != "14" {
		t.Fatalf("stop not saved: %v", stops)
	}
}

func TestAddStopAsksWhichBusThenSaves(t *testing.T) {
	s, _, mem := newSkill()
	r := handle(t, s, intent(intentAddStop, map[string]string{slotStop: "15651"}))
	if elicited(r) != slotBus {
		t.Fatalf("expected to elicit %s, got %+v", slotBus, r.Response)
	}
	// Stop names are XML-escaped, otherwise Alexa rejects the SSML.
	contains(t, speech(r), "Geary Blvd &amp; Masonic Ave is served by the 38 and the 38R")
	if strings.Contains(speech(r), " & ") {
		t.Error("unescaped ampersand in SSML")
	}
	// The directive keeps the stop the user already gave.
	d := r.Response.Directives[0].(alexa.ElicitSlotDirective)
	if d.UpdatedIntent.Slots[slotStop].Value != "15651" {
		t.Errorf("stop slot not carried over: %+v", d.UpdatedIntent)
	}

	// The user answers "the 38 rapid", resolved by the slot type to 38R.
	next := resolved(intent(intentAddStop, map[string]string{slotStop: "15651", slotBus: "38 rapid"}), slotBus, "38R")
	r = handle(t, s, next)
	contains(t, speech(r), "I added the 38R")
	stops, _ := mem.Load(context.Background(), userID)
	if got := stops["SF:15651"].Buses; len(got) != 1 || got[0] != "38R" {
		t.Fatalf("unexpected buses %v", got)
	}
}

func TestAddBusNotAtStopReasks(t *testing.T) {
	s, _, _ := newSkill()
	r := handle(t, s, intent(intentAddStop, map[string]string{slotStop: "15651", slotBus: "22"}))
	if elicited(r) != slotBus {
		t.Fatalf("expected to re-elicit bus, got %+v", r.Response)
	}
	contains(t, speech(r), "The 22 doesn't stop at Geary")
}

func TestAddBusWithoutStopAsksForStop(t *testing.T) {
	s, _, _ := newSkill()
	r := handle(t, s, intent(intentAddStop, map[string]string{slotBus: "38"}))
	if elicited(r) != slotStop {
		t.Fatalf("expected to elicit stop, got %+v", r.Response)
	}
	d := r.Response.Directives[0].(alexa.ElicitSlotDirective)
	if d.UpdatedIntent.Slots[slotBus].Value != "38" {
		t.Error("bus slot not carried over")
	}
}

func TestAddUnknownStopReasks(t *testing.T) {
	s, _, _ := newSkill()
	r := handle(t, s, intent(intentAddStop, map[string]string{slotStop: "99999"}))
	if elicited(r) != slotStop {
		t.Fatalf("expected to re-elicit stop, got %+v", r.Response)
	}
	contains(t, speech(r), "couldn't find")
}

func TestAddStopNotUnderstood(t *testing.T) {
	s, _, _ := newSkill()
	r := handle(t, s, intent(intentAddStop, map[string]string{slotStop: "?"}))
	if elicited(r) != slotStop {
		t.Fatalf("expected to elicit stop, got %+v", r.Response)
	}
}

func TestACTransitStop(t *testing.T) {
	s, _, mem := newSkill()
	r := handle(t, s, intent(intentAddStop, map[string]string{slotStop: "55555", slotBus: "51 a"}))
	contains(t, speech(r), "I added the 51A at Broadway")
	stops, _ := mem.Load(context.Background(), userID)
	if stops["AC:55555"] == nil {
		t.Fatalf("AC Transit stop not saved: %v", stops)
	}
}

func TestAmbiguousStopAsksAgency(t *testing.T) {
	s, _, mem := newSkill()
	r := handle(t, s, intent(intentAddStop, map[string]string{slotStop: "12345"}))
	if elicited(r) != slotAgency {
		t.Fatalf("expected to elicit agency, got %+v", r.Response)
	}
	contains(t, speech(r), "both Muni and AC Transit")

	r = handle(t, s, resolved(intent(intentAddStop, map[string]string{slotStop: "12345", slotAgency: "alameda county"}), slotAgency, "AC"))
	contains(t, speech(r), "I added the 72")
	stops, _ := mem.Load(context.Background(), userID)
	if stops["AC:12345"] == nil || stops["SF:12345"] != nil {
		t.Fatalf("wrong agency saved: %v", stops)
	}
}

func TestGetTimesLegacyData(t *testing.T) {
	s, ft, mem := newSkill()
	// Data as saved by the original version: keyed by stop code, no agency.
	mem.Seed(userID, map[string]store.Stop{"15651": {Code: "15651", Name: "Geary Blvd & Masonic Ave", Buses: []string{"38", "38R"}}})

	r := handle(t, s, envelope(alexa.LaunchRequest, "", nil))
	contains(t, speech(r), "The 38 is arriving in 3, 11 and 25 minutes.", "The 38R is arriving now.")
	if !ends(r) || r.Response.Card == nil {
		t.Error("expected a card and the session to end")
	}
	if strings.Contains(r.Response.Card.Content, "<") {
		t.Errorf("card has SSML: %q", r.Response.Card.Content)
	}
	if ft.calls != 1 {
		t.Errorf("expected one 511 call per stop, got %d", ft.calls)
	}
}

func TestGetTimesMultipleAgencies(t *testing.T) {
	s, _, mem := newSkill()
	stops := store.Stops{}
	stops.AddBus("SF", "15651", "Geary Blvd & Masonic Ave", "38")
	stops.AddBus("AC", "55555", "Broadway & 14th St", "NL")
	stops.AddBus("AC", "55555", "Broadway & 14th St", "99")
	_ = mem.Save(context.Background(), userID, stops)

	r := handle(t, s, intent(intentGetTimes, nil))
	contains(t, speech(r),
		"Muni 38 is arriving in 3, 11 and 25 minutes at Geary",
		"AC Transit NL is arriving in 9 minutes at Broadway",
		"AC Transit 99 has no upcoming arrivals",
	)
}

func TestGetTimesServiceDown(t *testing.T) {
	s, ft, mem := newSkill()
	stops := store.Stops{}
	stops.AddBus("SF", "15651", "Geary", "38")
	_ = mem.Save(context.Background(), userID, stops)
	ft.down = true

	r := handle(t, s, intent(intentGetTimes, nil))
	contains(t, speech(r), "can't reach the 511")
	if !ends(r) {
		t.Error("expected session to end")
	}
}

func TestListAndRemove(t *testing.T) {
	s, _, mem := newSkill()
	stops := store.Stops{}
	stops.AddBus("SF", "15651", "Geary Blvd & Masonic Ave", "38")
	stops.AddBus("SF", "15651", "Geary Blvd & Masonic Ave", "38R")
	stops.AddBus("AC", "55555", "", "51A")
	_ = mem.Save(context.Background(), userID, stops)

	r := handle(t, s, intent(intentListBuses, nil))
	contains(t, speech(r), "the 51A at stop <say-as", "the 38 and the 38R at Geary")

	r = handle(t, s, intent(intentRemoveBus, map[string]string{slotBus: "38 r"}))
	contains(t, speech(r), "I removed the 38R")

	r = handle(t, s, intent(intentRemoveBus, map[string]string{slotBus: "22"}))
	contains(t, speech(r), "The 22 isn't on your list")

	r = handle(t, s, intent(intentRemoveStop, map[string]string{slotStop: "55555"}))
	contains(t, speech(r), "I removed stop")

	got, _ := mem.Load(context.Background(), userID)
	if len(got) != 1 || len(got["SF:15651"].Buses) != 1 {
		t.Fatalf("unexpected stops %v", got)
	}
}

func TestRemoveBusAsksWhichBus(t *testing.T) {
	s, _, mem := newSkill()
	stops := store.Stops{}
	stops.AddBus("SF", "15651", "Geary", "38")
	_ = mem.Save(context.Background(), userID, stops)
	r := handle(t, s, intent(intentRemoveBus, map[string]string{slotBus: ""}))
	if elicited(r) != slotBus {
		t.Fatalf("expected to elicit bus, got %+v", r.Response)
	}
}

func TestBuiltInIntents(t *testing.T) {
	s, _, _ := newSkill()
	for name, wantEnd := range map[string]bool{
		"AMAZON.HelpIntent":     false,
		"AMAZON.FallbackIntent": false,
		"AMAZON.StopIntent":     true,
		"AMAZON.CancelIntent":   true,
		"SomethingUnexpected":   false,
	} {
		r := handle(t, s, intent(name, nil))
		if ends(r) != wantEnd {
			t.Errorf("%s: shouldEndSession=%v, want %v", name, ends(r), wantEnd)
		}
		if speech(r) == "" {
			t.Errorf("%s: no speech", name)
		}
	}
}

func TestSessionEndedReturnsEmptyResponse(t *testing.T) {
	s, _, _ := newSkill()
	r := handle(t, s, envelope(alexa.SessionEndedRequest, "", nil))
	if r.Response.OutputSpeech != nil {
		t.Error("SessionEndedRequest must not return speech")
	}
}

type failingStore struct{}

func (failingStore) Load(context.Context, string) (store.Stops, error) {
	return nil, errors.New("dynamodb down")
}
func (failingStore) Save(context.Context, string, store.Stops) error {
	return errors.New("dynamodb down")
}

func TestStorageErrorsApologize(t *testing.T) {
	s, _, _ := newSkill()
	s.Store = failingStore{}
	r := handle(t, s, envelope(alexa.LaunchRequest, "", nil))
	contains(t, speech(r), "something went wrong")
}
