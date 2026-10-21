package intercom

import (
	"errors"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
)

func TestValidatePartsReferences(t *testing.T) {
	ref := func() Part { return Part{Type: "image/png", Ref: string(ident.New(ident.Blob)), Name: "a.png", Size: 3} }
	if err := validateParts([]Part{ref(), ref(), AskPart(AskEvent{Event: AskEventAsk, Question: "q", Options: yesNo})}); err != nil {
		t.Fatalf("refs and an ask: %v", err)
	}
	var eleven []Part
	for range MaxAttachments + 1 {
		eleven = append(eleven, ref())
	}
	bad := map[string][]Part{
		"not a blob id": {{Type: "image/png", Ref: string(ident.New(ident.Channel))}},
		"no type":       {{Ref: string(ident.New(ident.Blob))}},
		"ref with data": {{Type: "image/png", Ref: string(ident.New(ident.Blob)), Data: []byte(`{}`)}},
		"eleven":        eleven,
	}
	for name, parts := range bad {
		if err := validateParts(parts); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
}
