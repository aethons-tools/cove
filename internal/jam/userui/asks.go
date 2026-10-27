package userui

import (
	"errors"
	"net/http"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

// Asks in /app/user (intercom structured asks): an ask renders as a card where it
// was asked, showing its current state; its later events are system lines;
// open asks also list in a panel above the stream.

func askCard(p jam.Participant, d Deps, a intercom.Ask) AskCard {
	c := AskCard{ID: a.ID, Question: a.Question, Options: a.Options, Version: a.Version, State: string(a.State),
		Reason: a.Reason, Edited: a.Version > 1, CanAnswer: d.Intercom.MayAnswer(p.UserID, a)}
	if a.Answer != nil {
		c.AnsweredBy, c.Choice, c.Comment = d.Intercom.PartyOf(a.Answer.By).Label, a.ChoiceLabel(a.Answer.Choice), a.Answer.Comment
	}
	if len(a.Of) > 0 && a.State == intercom.AskOpen && !c.CanAnswer {
		names := make([]string, len(a.Of))
		for i, id := range a.Of {
			names[i] = d.Intercom.PartyOf(id).Label
		}
		c.For = strings.Join(names, ", ")
	}
	if c.Options == nil {
		c.Options = []intercom.AskOption{} // types.ts says options: [...]
	}
	return c
}

// decorateAsk fills row's ask fields from m: the card for an ask's own
// squawk, a system line for its later events, a re: chip for a reply to an
// ask.
func decorateAsk(row *MessageRow, p jam.Participant, d Deps, m intercom.Squawk) {
	if ev, ok := intercom.AskOf(m); ok {
		if ev.Event == intercom.AskEventAsk {
			if a, ok := d.Intercom.GetAsk(m.ID); ok {
				card := askCard(p, d, a)
				row.Ask = &card
				return
			}
		}
		row.System = true
		row.SysText = row.From + " " + m.Body // an answer's body already names who
		if ev.Event == intercom.AskEventAnswer {
			row.SysText = m.Body
		}
		return
	}
	if m.ReplyTo != "" {
		if a, ok := d.Intercom.GetAsk(m.ReplyTo); ok && a.Channel == m.Channel {
			row.ReAsk = &AskRef{ID: a.ID, Question: a.Question}
		}
	}
}

func openAskCards(p jam.Participant, d Deps, ch ident.ID) []AskCard {
	var out []AskCard
	for _, a := range d.Intercom.OpenAsksIn(ch) {
		out = append(out, askCard(p, d, a))
	}
	return out
}

// answerOutcome maps an AnswerAsk result to the API's HTTP status, code
// and message.
func answerOutcome(d Deps, id string, err error) (int, string, string) {
	switch {
	case err == nil:
		return http.StatusNoContent, "", ""
	case errors.Is(err, intercom.ErrAskChanged):
		return http.StatusConflict, "ask_changed", "this ask changed — review the new version"
	case errors.Is(err, intercom.ErrAskClosed):
		msg := "this ask is closed"
		if a, ok := d.Intercom.GetAsk(id); ok && a.Answer != nil {
			msg = "already answered by " + d.Intercom.PartyOf(a.Answer.By).Label + ": " + a.ChoiceLabel(a.Answer.Choice)
		}
		return http.StatusConflict, "ask_closed", msg
	case errors.Is(err, intercom.ErrInvalid):
		return http.StatusBadRequest, "bad_request", strings.TrimPrefix(err.Error(), "intercom: invalid squawk: ")
	case errors.Is(err, intercom.ErrAskNotFound):
		return http.StatusNotFound, "not_found", "not found"
	case errors.Is(err, jam.ErrSendDenied), errors.Is(err, intercom.ErrAskForbidden):
		return http.StatusForbidden, "forbidden", "not allowed"
	default:
		return http.StatusInternalServerError, "internal", "failed"
	}
}
