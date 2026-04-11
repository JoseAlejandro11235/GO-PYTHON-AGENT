package openai

import "testing"

func TestStripMarkdownJSONFence(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{`{"ok":true}`, `{"ok":true}`},
		{"```json\n{\"ok\":true}\n```", `{"ok":true}`},
		{"```\n{\"a\":1}\n```", `{"a":1}`},
		{"  ```json\n{\"x\":\"y\"}\n```  ", `{"x":"y"}`},
	}
	for _, tt := range tests {
		got := StripMarkdownJSONFence(tt.in)
		if got != tt.want {
			t.Errorf("StripMarkdownJSONFence(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}
