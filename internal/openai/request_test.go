package openai

import (
	"errors"
	"testing"
)

func TestDecodeRoutingFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		body       string
		wantModel  string
		wantStream bool
		wantUsage  bool
		wantMaxOut int
		wantErr    error
	}{
		{
			name:      "minimal chat request",
			body:      `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			wantModel: "m",
		},
		{
			name:       "streaming with usage",
			body:       `{"model":"m","stream":true,"stream_options":{"include_usage":true}}`,
			wantModel:  "m",
			wantStream: true,
			wantUsage:  true,
		},
		{
			name:       "max_completion_tokens wins over max_tokens",
			body:       `{"model":"m","max_tokens":10,"max_completion_tokens":20}`,
			wantModel:  "m",
			wantMaxOut: 20,
		},
		{
			name:       "legacy max_tokens",
			body:       `{"model":"m","max_tokens":10}`,
			wantModel:  "m",
			wantMaxOut: 10,
		},
		{
			name:    "missing model",
			body:    `{"messages":[]}`,
			wantErr: ErrMissingModel,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := DecodeRoutingFields([]byte(tt.body))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("DecodeRoutingFields(%s) error = %v, want %v", tt.body, err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got.Model != tt.wantModel || got.Stream != tt.wantStream ||
				got.WantsUsage() != tt.wantUsage || got.MaxOutputTokens() != tt.wantMaxOut {
				t.Errorf("DecodeRoutingFields(%s) = {model %q, stream %v, usage %v, maxOut %d}, want {%q, %v, %v, %d}",
					tt.body, got.Model, got.Stream, got.WantsUsage(), got.MaxOutputTokens(),
					tt.wantModel, tt.wantStream, tt.wantUsage, tt.wantMaxOut)
			}
		})
	}
}

func TestDecodeRoutingFieldsRejectsMalformedJSON(t *testing.T) {
	t.Parallel()
	for _, body := range []string{``, `[]`, `{"model":`, `"model"`} {
		if _, err := DecodeRoutingFields([]byte(body)); err == nil {
			t.Errorf("DecodeRoutingFields(%q) error = nil, want an error", body)
		}
	}
}
