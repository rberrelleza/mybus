// Package alexa contains the subset of the Alexa Skills Kit request/response
// JSON interface used by this skill, plus request verification for skills
// hosted as an HTTPS web service.
//
// Reference: https://developer.amazon.com/en-US/docs/alexa/custom-skills/request-and-response-json-reference.html
package alexa

import "strings"

// Request types.
const (
	LaunchRequest       = "LaunchRequest"
	IntentRequest       = "IntentRequest"
	SessionEndedRequest = "SessionEndedRequest"
)

// RequestEnvelope is the body Alexa sends to the skill.
type RequestEnvelope struct {
	Version string   `json:"version"`
	Session *Session `json:"session,omitempty"`
	Context Context  `json:"context"`
	Request Request  `json:"request"`
}

type Session struct {
	New         bool           `json:"new"`
	SessionID   string         `json:"sessionId"`
	Application Application    `json:"application"`
	User        User           `json:"user"`
	Attributes  map[string]any `json:"attributes,omitempty"`
}

type Application struct {
	ApplicationID string `json:"applicationId"`
}

type User struct {
	UserID string `json:"userId"`
}

type Context struct {
	System System `json:"System"`
}

type System struct {
	Application Application `json:"application"`
	User        User        `json:"user"`
}

type Request struct {
	Type        string        `json:"type"`
	RequestID   string        `json:"requestId"`
	Timestamp   string        `json:"timestamp"`
	Locale      string        `json:"locale"`
	DialogState string        `json:"dialogState,omitempty"`
	Intent      *Intent       `json:"intent,omitempty"`
	Reason      string        `json:"reason,omitempty"`
	Error       *RequestError `json:"error,omitempty"`
}

type RequestError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type Intent struct {
	Name               string          `json:"name"`
	ConfirmationStatus string          `json:"confirmationStatus,omitempty"`
	Slots              map[string]Slot `json:"slots,omitempty"`
}

type Slot struct {
	Name               string       `json:"name"`
	Value              string       `json:"value,omitempty"`
	ConfirmationStatus string       `json:"confirmationStatus,omitempty"`
	Resolutions        *Resolutions `json:"resolutions,omitempty"`
}

type Resolutions struct {
	ResolutionsPerAuthority []Authority `json:"resolutionsPerAuthority"`
}

type Authority struct {
	Authority string           `json:"authority"`
	Status    AuthorityStatus  `json:"status"`
	Values    []ResolvedValues `json:"values,omitempty"`
}

type AuthorityStatus struct {
	Code string `json:"code"`
}

type ResolvedValues struct {
	Value ResolvedValue `json:"value"`
}

type ResolvedValue struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

// UserID returns the Alexa user id of the request.
func (e *RequestEnvelope) UserID() string {
	if e.Context.System.User.UserID != "" {
		return e.Context.System.User.UserID
	}
	if e.Session != nil {
		return e.Session.User.UserID
	}
	return ""
}

// ApplicationID returns the skill id the request was sent to.
func (e *RequestEnvelope) ApplicationID() string {
	if e.Context.System.Application.ApplicationID != "" {
		return e.Context.System.Application.ApplicationID
	}
	if e.Session != nil {
		return e.Session.Application.ApplicationID
	}
	return ""
}

// IntentName returns the intent name, or "" for non-intent requests.
func (e *RequestEnvelope) IntentName() string {
	if e.Request.Intent == nil {
		return ""
	}
	return e.Request.Intent.Name
}

// SlotValue returns the spoken value of a slot, or "" if it is empty or was
// not understood ("?").
func (e *RequestEnvelope) SlotValue(name string) string {
	if e.Request.Intent == nil {
		return ""
	}
	v := strings.TrimSpace(e.Request.Intent.Slots[name].Value)
	if v == "?" {
		return ""
	}
	return v
}

// SlotID returns the entity-resolved id of a custom slot value (for example
// "N Judah" resolves to "N"), or "" if there was no match.
func (e *RequestEnvelope) SlotID(name string) string {
	if e.Request.Intent == nil {
		return ""
	}
	slot := e.Request.Intent.Slots[name]
	if slot.Resolutions == nil {
		return ""
	}
	for _, a := range slot.Resolutions.ResolutionsPerAuthority {
		if a.Status.Code == "ER_SUCCESS_MATCH" && len(a.Values) > 0 {
			return a.Values[0].Value.ID
		}
	}
	return ""
}

// ResponseEnvelope is the body the skill returns to Alexa.
type ResponseEnvelope struct {
	Version           string         `json:"version"`
	SessionAttributes map[string]any `json:"sessionAttributes,omitempty"`
	Response          Response       `json:"response"`
}

type Response struct {
	OutputSpeech     *OutputSpeech `json:"outputSpeech,omitempty"`
	Card             *Card         `json:"card,omitempty"`
	Reprompt         *Reprompt     `json:"reprompt,omitempty"`
	Directives       []any         `json:"directives,omitempty"`
	ShouldEndSession *bool         `json:"shouldEndSession,omitempty"`
}

type OutputSpeech struct {
	Type string `json:"type"`
	SSML string `json:"ssml,omitempty"`
	Text string `json:"text,omitempty"`
}

type Reprompt struct {
	OutputSpeech OutputSpeech `json:"outputSpeech"`
}

type Card struct {
	Type    string `json:"type"`
	Title   string `json:"title,omitempty"`
	Content string `json:"content,omitempty"`
}

// ElicitSlotDirective asks the user for a slot value. UpdatedIntent carries the
// slots collected so far.
type ElicitSlotDirective struct {
	Type          string  `json:"type"`
	SlotToElicit  string  `json:"slotToElicit"`
	UpdatedIntent *Intent `json:"updatedIntent,omitempty"`
}

func ssml(fragment string) OutputSpeech {
	return OutputSpeech{Type: "SSML", SSML: "<speak>" + fragment + "</speak>"}
}

// Tell speaks and ends the session.
func Tell(speech string) *ResponseEnvelope {
	end := true
	s := ssml(speech)
	return &ResponseEnvelope{Version: "1.0", Response: Response{OutputSpeech: &s, ShouldEndSession: &end}}
}

// Ask speaks, keeps the session open and reprompts if the user says nothing.
func Ask(speech, reprompt string) *ResponseEnvelope {
	end := false
	s := ssml(speech)
	return &ResponseEnvelope{Version: "1.0", Response: Response{
		OutputSpeech:     &s,
		Reprompt:         &Reprompt{OutputSpeech: ssml(reprompt)},
		ShouldEndSession: &end,
	}}
}

// Empty is the response for requests that must not produce speech, such as
// SessionEndedRequest.
func Empty() *ResponseEnvelope {
	return &ResponseEnvelope{Version: "1.0"}
}

// WithCard adds a simple card shown in the Alexa app and on screen devices.
func (r *ResponseEnvelope) WithCard(title, content string) *ResponseEnvelope {
	r.Response.Card = &Card{Type: "Simple", Title: title, Content: content}
	return r
}

// ElicitSlot asks for slotName of the current intent while keeping the values
// of its other slots, except those listed in clear.
func ElicitSlot(req *RequestEnvelope, slotName, speech, reprompt string, clear ...string) *ResponseEnvelope {
	r := Ask(speech, reprompt)
	// Alexa decides whether the session stays open for dialog directives.
	r.Response.ShouldEndSession = nil

	cleared := map[string]bool{slotName: true}
	for _, c := range clear {
		cleared[c] = true
	}
	intent := &Intent{Name: req.IntentName(), ConfirmationStatus: "NONE", Slots: map[string]Slot{}}
	if req.Request.Intent != nil {
		for name, slot := range req.Request.Intent.Slots {
			value := slot.Value
			if cleared[name] || value == "?" {
				value = ""
			}
			intent.Slots[name] = Slot{Name: name, Value: value, ConfirmationStatus: "NONE"}
		}
	}
	r.Response.Directives = append(r.Response.Directives, ElicitSlotDirective{
		Type:          "Dialog.ElicitSlot",
		SlotToElicit:  slotName,
		UpdatedIntent: intent,
	})
	return r
}
