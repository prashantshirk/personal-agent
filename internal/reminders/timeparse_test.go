package reminders

import (
	"testing"
	"time"
)

func TestParseWhen(t *testing.T){
	loc,_:=time.LoadLocation("Asia/Kolkata");now:=time.Date(2026,10,7,10,0,0,0,loc)
	cases:=[]struct{input,want string}{{"in 2h","2026-10-07T12:00:00+05:30"},{"after 3 hours","2026-10-07T13:00:00+05:30"},{"tomorrow 18:00","2026-10-08T18:00:00+05:30"},{"tomorrow 6pm","2026-10-08T18:00:00+05:30"},{"2026-10-09 09:30","2026-10-09T09:30:00+05:30"},{"Friday 8:00","2026-10-09T08:00:00+05:30"},{"7 am","2026-10-08T07:00:00+05:30"},{"7am","2026-10-08T07:00:00+05:30"},{"6 am","2026-10-08T06:00:00+05:30"},{"9 pm","2026-10-07T21:00:00+05:30"},{"at 9 pm","2026-10-07T21:00:00+05:30"},{"7 pm tomorrow","2026-10-08T19:00:00+05:30"},{"tomorrow at 7 am","2026-10-08T07:00:00+05:30"}}
	for _,tc:=range cases{t.Run(tc.input,func(t *testing.T){got,err:=ParseWhen(tc.input,now,loc);if err!=nil{t.Fatal(err)};if got.Format(time.RFC3339)!=tc.want{t.Fatalf("got %s want %s",got.Format(time.RFC3339),tc.want)}})}
}
