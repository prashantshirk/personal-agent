package router

import "testing"

func TestAllowedUser(t *testing.T) {
	for _,tc:=range []struct{name string;user,allowed int64;want bool}{{"allowed",42,42,true},{"other",7,42,false},{"missing",0,42,false}} {
		t.Run(tc.name,func(t *testing.T){if got:=AllowedUser(tc.user,tc.allowed);got!=tc.want{t.Fatalf("AllowedUser(%d,%d)=%v, want %v",tc.user,tc.allowed,got,tc.want)}})
	}
}
