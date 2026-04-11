package agent

import "testing"

func TestMaxSteps(t *testing.T) {
	t.Setenv("AGENT_MAX_STEPS", "")
	if n := maxSteps(0); n != 12 {
		t.Errorf("default got %d want 12", n)
	}
	if n := maxSteps(99); n != 15 {
		t.Errorf("cap got %d want 15", n)
	}
	t.Setenv("AGENT_MAX_STEPS", "3")
	if n := maxSteps(0); n != 3 {
		t.Errorf("env default got %d want 3", n)
	}
	if n := maxSteps(5); n != 5 {
		t.Errorf("request got %d want 5", n)
	}
}
