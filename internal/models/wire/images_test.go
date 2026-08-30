package wire

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ataidesorg/ink/internal/core"
)

var imgMsg = core.Message{
	Role:    core.RoleUser,
	Content: "what is this?",
	Images:  []core.ImagePart{{MediaType: "image/png", Data: "aGk="}},
}

func TestCCMessageImageParts(t *testing.T) {
	b, err := json.Marshal(toCCMessages([]core.Message{imgMsg}))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{`"type":"image_url"`, `"url":"data:image/png;base64,aGk="`, `"type":"text"`, `"text":"what is this?"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
	// Plain messages keep the string content form.
	b, err = json.Marshal(toCCMessages([]core.Message{{Role: core.RoleUser, Content: "hi"}}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"content":"hi"`) {
		t.Fatalf("plain content changed shape: %s", b)
	}
}

func TestRespItemImageParts(t *testing.T) {
	b, err := json.Marshal(toRespInput([]core.Message{imgMsg}))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{`"type":"input_image"`, `"image_url":"data:image/png;base64,aGk="`, `"type":"input_text"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
	b, err = json.Marshal(toRespInput([]core.Message{{Role: core.RoleUser, Content: "hi"}}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"content":"hi"`) {
		t.Fatalf("plain content changed shape: %s", b)
	}
}

func TestAnthropicImageBlocks(t *testing.T) {
	req := buildAMRequest(core.CompletionRequest{Messages: []core.Message{imgMsg}}, "m", false)
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{`"type":"image"`, `"source":{"type":"base64","media_type":"image/png","data":"aGk="}`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
}

func TestBedrockRefusesImages(t *testing.T) {
	_, err := buildConverseRequest(core.CompletionRequest{Messages: []core.Message{imgMsg}})
	if !errors.Is(err, core.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented, got %v", err)
	}
}
