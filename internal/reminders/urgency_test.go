package reminders

import "testing"

func TestIsUrgent(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"very very urgent reminder", true},
		{"max priority reminder", true},
		{"set an alarm for 6am", true},
		{"wake me at 6am", true},
		{"buy milk tomorrow", false},
		{"notify me about the meeting", false},
	} {
		if got := IsUrgent(tc.text); got != tc.want {
			t.Fatalf("IsUrgent(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}
