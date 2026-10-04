package util

import "testing"

func TestJSONOutputRejectsTrailingData(t *testing.T) {
	for _, value := range []string{"[] diagnostic", "[] {}", "{}\nFAIL"} {
		if _, err := ParseJSONOutput(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	if _, err := ParseJSONOutput(" \n[]\n "); err != nil {
		t.Fatal(err)
	}
}
