package skill

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/rberrelleza/mybus/internal/store"
	"github.com/rberrelleza/mybus/internal/transit"
)

// Prompts are SSML fragments. Anything from outside the skill (stop names
// such as "Geary Blvd & Masonic Ave", spoken values) must go through esc: an
// unescaped "&" makes Alexa reject the whole response.

const (
	skillName   = "My Bus"
	exampleStop = "15651"
)

func esc(s string) string { return html.EscapeString(s) }

// digits reads a stop code digit by digit: "one five six five one".
func digits(s string) string {
	return `<say-as interpret-as="digits">` + esc(s) + `</say-as>`
}

var tags = regexp.MustCompile(`<[^>]+>`)

// plain strips SSML for card text.
func plain(ssml string) string {
	return strings.TrimSpace(html.UnescapeString(tags.ReplaceAllString(ssml, "")))
}

func join(words []string, conj string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " " + conj + " " + words[len(words)-1]
}

func bus(line string) string { return "the " + esc(line) }
func Bus(line string) string { return "The " + esc(line) }

func buses(lines []string, conj string) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = bus(l)
	}
	return join(out, conj)
}

func stopLabel(s *store.Stop) string {
	if s.Name != "" && s.Name != s.Code {
		return esc(s.Name)
	}
	return "stop " + digits(s.Code)
}

func minutes(times []int) string {
	if len(times) == 1 && times[0] == 0 {
		return "arriving now"
	}
	if len(times) == 1 && times[0] == 1 {
		return "arriving in 1 minute"
	}
	spoken := make([]string, len(times))
	for i, t := range times {
		if t == 0 {
			spoken[i] = "now"
		} else {
			spoken[i] = strconv.Itoa(t)
		}
	}
	return "arriving in " + join(spoken, "and") + " minutes"
}

var (
	addStopExample = "add stop " + digits(exampleStop)

	welcomeNoStops = "Welcome to My Bus. I can tell you when your next Muni or AC Transit bus is coming. " +
		"First, tell me the stop number printed on the bus stop sign. For example, say: " + addStopExample + "."
	welcomeNoStopsReprompt = "What's your stop number? For example, say: " + addStopExample + "."

	helpText = "My Bus tells you when the next Muni or AC Transit bus is coming to your saved stops. " +
		"To save a stop, say " + addStopExample + ". You'll find the stop number on the sign or shelter at your stop. " +
		"To hear arrival times, just say: next bus. " +
		"You can also say list my buses, or remove bus 38. What would you like to do?"
	helpReprompt = "You can say next bus, add a stop, list my buses, or remove a bus. What would you like to do?"

	fallbackText = "Sorry, I didn't get that. You can say next bus, " + addStopExample +
		", list my buses, or remove a bus. What would you like to do?"

	goodbye     = "Goodbye."
	errorText   = "Sorry, something went wrong on my end. Please try again in a moment."
	serviceDown = "Sorry, I can't reach the 511 transit service right now. Please try again in a few minutes."

	askStop         = "Which stop? Say the stop number printed on the bus stop sign. For example: " + addStopExample + "."
	askStopReprompt = "What's the stop number? For example: " + addStopExample + "."
	askAgency       = "Muni or AC Transit?"

	askBusToRemove  = "Which bus should I remove? For example, say: remove bus 38."
	askStopToRemove = "Which stop number should I remove?"
	noBusesToRemove = "You don't have any saved buses to remove."
)

func stopNotFound(code string) string {
	return "I couldn't find any Muni or AC Transit buses coming to stop " + digits(code) + ". " +
		"Please check the number on the bus stop sign. If it's late at night, the stop might have no service " +
		"right now, so try again during service hours. Which stop would you like to add?"
}

func whichAgency(code string) string {
	return "Stop " + digits(code) + " exists for both Muni and AC Transit. Which one do you mean?"
}

func whichBus(s *store.Stop, lines []string) string {
	return fmt.Sprintf("%s is served by %s. Which bus should I add? For example, say: the %s.",
		stopLabel(s), buses(lines, "and"), esc(lines[0]))
}

func whichBusReprompt(lines []string) string {
	return "Which bus should I add: " + buses(lines, "or") + "?"
}

func busNotAtStop(line string, s *store.Stop, lines []string) string {
	return fmt.Sprintf("%s doesn't stop at %s. The buses there are %s. Which one should I add?",
		Bus(line), stopLabel(s), buses(lines, "and"))
}

func added(lines []string, s *store.Stop) string {
	return fmt.Sprintf("Done. I added %s at %s. Next time, just say: Alexa, ask My Bus when's my next bus.",
		buses(lines, "and"), stopLabel(s))
}

func removedBus(line string) string  { return "I removed " + bus(line) + " from your list." }
func busNotSaved(line string) string { return Bus(line) + " isn't on your list." }
func stopNotSaved(code string) string {
	return "Stop " + digits(code) + " isn't on your list."
}

func removedStops(stops []*store.Stop) string {
	labels := make([]string, len(stops))
	for i, s := range stops {
		labels[i] = stopLabel(s)
	}
	return "I removed " + join(labels, "and") + " from your list."
}

func listBuses(stops []*store.Stop) string {
	var parts []string
	for _, s := range stops {
		if len(s.Buses) > 0 {
			parts = append(parts, buses(s.Buses, "and")+" at "+stopLabel(s))
		}
	}
	return "You're following " + join(parts, "and") + "."
}

// arrivalResult is one followed line and its upcoming arrivals; Times is nil
// when the stop could not be fetched.
type arrivalResult struct {
	Stop  *store.Stop
	Line  string
	Times []int
	Err   bool
}

func departures(results []arrivalResult, multiAgency bool) string {
	codes := map[string]bool{}
	for _, r := range results {
		codes[r.Stop.Agency+r.Stop.Code] = true
	}
	var sentences []string
	for _, r := range results {
		name := Bus(r.Line)
		if multiAgency {
			name = transit.AgencyName(r.Stop.Agency) + " " + esc(r.Line)
		}
		where := ""
		if len(codes) > 1 {
			where = " at " + stopLabel(r.Stop)
		}
		switch {
		case r.Err:
			sentences = append(sentences, "I couldn't get times for "+name+where+".")
		case len(r.Times) == 0:
			sentences = append(sentences, name+" has no upcoming arrivals"+where+".")
		default:
			sentences = append(sentences, name+" is "+minutes(r.Times)+where+".")
		}
	}
	return strings.Join(sentences, " ")
}
