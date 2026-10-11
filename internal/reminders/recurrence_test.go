package reminders

import (
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

func TestAddMonthClamped(t *testing.T){
	loc,_:=time.LoadLocation("Asia/Kolkata");input:=time.Date(2026,time.January,31,9,15,0,0,loc);got:=addMonthClamped(input)
	if want:=time.Date(2026,time.February,28,9,15,0,0,loc);!got.Equal(want){t.Fatalf("got %s want %s",got,want)}
}

func TestCronRecurrence(t *testing.T){
	loc,_:=time.LoadLocation("Asia/Kolkata");parser:=cron.NewParser(cron.Minute|cron.Hour|cron.Dom|cron.Month|cron.Dow);schedule,err:=parser.Parse("0 9 * * *");if err!=nil{t.Fatal(err)}
	from:=time.Date(2026,time.October,7,9,1,0,0,loc);got:=schedule.Next(from);want:=time.Date(2026,time.October,8,9,0,0,0,loc)
	if !got.Equal(want){t.Fatalf("got %s want %s",got,want)}
}
