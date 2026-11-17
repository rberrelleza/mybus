// Package skill implements the My Bus Alexa skill: it tells you when the next
// Muni or AC Transit bus is coming to the stops you saved.
package skill

import (
	"context"
	"errors"
	"log/slog"
	"regexp"

	"github.com/rberrelleza/mybus/internal/alexa"
	"github.com/rberrelleza/mybus/internal/store"
	"github.com/rberrelleza/mybus/internal/transit"
)

// Intent and slot names; they must match skill-package/interactionModels.
const (
	intentGetTimes   = "GetBusTimesIntent"
	intentAddStop    = "AddStopIntent"
	intentListBuses  = "ListBusesIntent"
	intentRemoveBus  = "RemoveBusIntent"
	intentRemoveStop = "RemoveStopIntent"

	slotStop   = "StopID"
	slotBus    = "BusID"
	slotAgency = "Agency"
)

// ErrWrongSkill is returned for requests addressed to another skill id.
var ErrWrongSkill = errors.New("request is for a different skill")

// Skill handles Alexa requests.
type Skill struct {
	ID      string // expected skill id; empty disables the check
	Transit transit.Source
	Store   store.Store
	Log     *slog.Logger
}

func (s *Skill) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Handle dispatches a request. Errors from handlers become a spoken apology;
// only ErrWrongSkill is returned to the caller.
func (s *Skill) Handle(ctx context.Context, req *alexa.RequestEnvelope) (*alexa.ResponseEnvelope, error) {
	if s.ID != "" && req.ApplicationID() != s.ID {
		return nil, ErrWrongSkill
	}
	s.log().Info("request", "type", req.Request.Type, "intent", req.IntentName(), "id", req.Request.RequestID)

	resp, err := s.dispatch(ctx, req)
	if err != nil {
		s.log().Error("handler failed", "type", req.Request.Type, "intent", req.IntentName(), "err", err)
		return alexa.Ask(errorText, helpReprompt), nil
	}
	return resp, nil
}

func (s *Skill) dispatch(ctx context.Context, req *alexa.RequestEnvelope) (*alexa.ResponseEnvelope, error) {
	switch req.Request.Type {
	case alexa.LaunchRequest:
		return s.getTimes(ctx, req)
	case alexa.SessionEndedRequest:
		s.log().Info("session ended", "reason", req.Request.Reason, "error", req.Request.Error)
		return alexa.Empty(), nil
	case alexa.IntentRequest:
	default:
		return alexa.Empty(), nil
	}

	switch req.IntentName() {
	case intentGetTimes:
		return s.getTimes(ctx, req)
	case intentAddStop:
		return s.addStop(ctx, req)
	case intentListBuses:
		return s.listBuses(ctx, req)
	case intentRemoveBus:
		return s.removeBus(ctx, req)
	case intentRemoveStop:
		return s.removeStop(ctx, req)
	case "AMAZON.HelpIntent", "AMAZON.NavigateHomeIntent":
		return alexa.Ask(helpText, helpReprompt), nil
	case "AMAZON.CancelIntent", "AMAZON.StopIntent", "AMAZON.NoIntent":
		return alexa.Tell(goodbye), nil
	default: // AMAZON.FallbackIntent and anything unexpected
		return alexa.Ask(fallbackText, helpReprompt), nil
	}
}

func withCard(r *alexa.ResponseEnvelope, speech string) *alexa.ResponseEnvelope {
	return r.WithCard(skillName, plain(speech))
}

// getTimes reads out arrivals for every followed line. Each stop is fetched
// once regardless of how many of its lines the user follows.
func (s *Skill) getTimes(ctx context.Context, req *alexa.RequestEnvelope) (*alexa.ResponseEnvelope, error) {
	stops, err := s.Store.Load(ctx, req.UserID())
	if err != nil {
		return nil, err
	}
	if !stops.HasBuses() {
		return alexa.Ask(welcomeNoStops, welcomeNoStopsReprompt), nil
	}

	var results []arrivalResult
	agencies := map[string]bool{}
	ok := false
	for _, st := range stops.List() {
		if len(st.Buses) == 0 {
			continue
		}
		agencies[st.Agency] = true
		info, err := s.Transit.Stop(ctx, st.Agency, st.Code)
		if err != nil {
			s.log().Error("fetching arrivals", "agency", st.Agency, "stop", st.Code, "err", err)
		} else {
			ok = true
		}
		for _, line := range st.Buses {
			r := arrivalResult{Stop: st, Line: line, Err: err != nil}
			if err == nil {
				r.Times = info.Minutes(line, 3)
			}
			results = append(results, r)
		}
	}
	if !ok {
		return alexa.Tell(serviceDown), nil
	}
	speech := departures(results, len(agencies) > 1)
	return withCard(alexa.Tell(speech), speech), nil
}

var allDigits = regexp.MustCompile(`^\d+$`)

func stopCode(req *alexa.RequestEnvelope) string {
	if v := req.SlotValue(slotStop); allDigits.MatchString(v) {
		return v
	}
	return ""
}

func spokenBus(req *alexa.RequestEnvelope) string {
	if id := req.SlotID(slotBus); id != "" {
		return id
	}
	return req.SlotValue(slotBus)
}

func matchLine(spoken string, lines []string) string {
	want := transit.NormalizeLine(spoken)
	for _, l := range lines {
		if transit.NormalizeLine(l) == want {
			return l
		}
	}
	return ""
}

// addStop handles "add stop 15651", "add the 38 at stop 15651", "add bus 38"
// and "add AC Transit stop 55555". Missing details are collected with
// Dialog.ElicitSlot on this same intent, so no session state is needed.
func (s *Skill) addStop(ctx context.Context, req *alexa.RequestEnvelope) (*alexa.ResponseEnvelope, error) {
	code := stopCode(req)
	if code == "" {
		return alexa.ElicitSlot(req, slotStop, askStop, askStopReprompt), nil
	}

	candidates := transit.Agencies
	if a := req.SlotID(slotAgency); a == transit.Muni || a == transit.ACTransit {
		candidates = []string{a}
	}

	var found []*transit.Stop
	failures := 0
	for _, agency := range candidates {
		info, err := s.Transit.Stop(ctx, agency, code)
		if err != nil {
			s.log().Error("looking up stop", "agency", agency, "stop", code, "err", err)
			failures++
			continue
		}
		if len(info.Lines()) > 0 {
			found = append(found, info)
		}
	}
	switch {
	case len(found) == 0 && failures == len(candidates):
		return alexa.Tell(serviceDown), nil
	case len(found) == 0:
		return alexa.ElicitSlot(req, slotStop, stopNotFound(code), askStopReprompt, slotAgency), nil
	case len(found) > 1:
		return alexa.ElicitSlot(req, slotAgency, whichAgency(code), askAgency), nil
	}

	info := found[0]
	st := &store.Stop{Agency: info.Agency, Code: info.Code, Name: info.Name}
	lines := info.Lines()

	var chosen []string
	if requested := spokenBus(req); requested != "" {
		line := matchLine(requested, lines)
		if line == "" {
			return alexa.ElicitSlot(req, slotBus, busNotAtStop(requested, st, lines), whichBusReprompt(lines)), nil
		}
		chosen = []string{line}
	} else if len(lines) == 1 {
		chosen = lines
	} else {
		return alexa.ElicitSlot(req, slotBus, whichBus(st, lines), whichBusReprompt(lines)), nil
	}

	stops, err := s.Store.Load(ctx, req.UserID())
	if err != nil {
		return nil, err
	}
	for _, line := range chosen {
		stops.AddBus(st.Agency, st.Code, st.Name, line)
	}
	if err := s.Store.Save(ctx, req.UserID(), stops); err != nil {
		return nil, err
	}
	speech := added(chosen, st)
	return withCard(alexa.Tell(speech), speech), nil
}

func (s *Skill) listBuses(ctx context.Context, req *alexa.RequestEnvelope) (*alexa.ResponseEnvelope, error) {
	stops, err := s.Store.Load(ctx, req.UserID())
	if err != nil {
		return nil, err
	}
	if !stops.HasBuses() {
		return alexa.Ask(welcomeNoStops, welcomeNoStopsReprompt), nil
	}
	speech := listBuses(stops.List())
	return withCard(alexa.Tell(speech), speech), nil
}

func (s *Skill) removeBus(ctx context.Context, req *alexa.RequestEnvelope) (*alexa.ResponseEnvelope, error) {
	stops, err := s.Store.Load(ctx, req.UserID())
	if err != nil {
		return nil, err
	}
	if !stops.HasBuses() {
		return alexa.Tell(noBusesToRemove), nil
	}
	requested := spokenBus(req)
	if requested == "" {
		return alexa.ElicitSlot(req, slotBus, askBusToRemove, askBusToRemove), nil
	}
	var saved []string
	for _, st := range stops {
		saved = append(saved, st.Buses...)
	}
	line := matchLine(requested, saved)
	if line == "" {
		return alexa.Tell(busNotSaved(requested)), nil
	}
	stops.RemoveBus(line)
	if err := s.Store.Save(ctx, req.UserID(), stops); err != nil {
		return nil, err
	}
	speech := removedBus(line)
	return withCard(alexa.Tell(speech), speech), nil
}

func (s *Skill) removeStop(ctx context.Context, req *alexa.RequestEnvelope) (*alexa.ResponseEnvelope, error) {
	code := stopCode(req)
	if code == "" {
		return alexa.ElicitSlot(req, slotStop, askStopToRemove, askStopToRemove), nil
	}
	stops, err := s.Store.Load(ctx, req.UserID())
	if err != nil {
		return nil, err
	}
	removed := stops.RemoveStop(code)
	if len(removed) == 0 {
		return alexa.Tell(stopNotSaved(code)), nil
	}
	if err := s.Store.Save(ctx, req.UserID(), stops); err != nil {
		return nil, err
	}
	speech := removedStops(removed)
	return withCard(alexa.Tell(speech), speech), nil
}
