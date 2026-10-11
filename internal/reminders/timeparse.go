package reminders

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var inDuration=regexp.MustCompile(`(?i)^in\s+(\d+)\s*(m|min|mins|minutes|h|hr|hours|d|days)$`)
var timeOfDay=regexp.MustCompile(`(?i)^(\d{1,2})(?::(\d{2}))?\s*(am|pm)?$`)

// ParseWhen resolves supported reminder phrases in the user's configured zone.
func ParseWhen(input string,now time.Time,loc *time.Location)(time.Time,error){
	s:=strings.TrimSpace(strings.ToLower(input));if loc==nil{return time.Time{},fmt.Errorf("timezone is required")};localNow:=now.In(loc)
	if m:=inDuration.FindStringSubmatch(s);m!=nil{n,_:=strconv.Atoi(m[1]);unit:=m[2];d:=time.Duration(n)*time.Minute;if strings.HasPrefix(unit,"h"){d=time.Duration(n)*time.Hour}else if strings.HasPrefix(unit,"d"){d=time.Duration(n)*24*time.Hour};if n<1{return time.Time{},fmt.Errorf("duration must be positive")};return now.Add(d),nil}
	if t,err:=time.ParseInLocation(time.RFC3339,s,loc);err==nil{return t,nil}
	if t,err:=time.ParseInLocation("2006-01-02 15:04",s,loc);err==nil{return t,nil}
	parts:=strings.Fields(s);base:=localNow
	if len(parts)>0{switch parts[0]{case "tomorrow":base=localNow.AddDate(0,0,1);parts=parts[1:];case "today":parts=parts[1:];default:
		if wd,ok:=weekday(parts[0]);ok{delta:=(int(wd)-int(localNow.Weekday())+7)%7;if delta==0{delta=7};base=localNow.AddDate(0,0,delta);parts=parts[1:]}
	}}
	if len(parts)==1{if t,err:=time.ParseInLocation("2006-01-02",parts[0],loc);err==nil{return t,nil}}
	if len(parts)>1{return time.Time{},fmt.Errorf("unsupported time expression %q",input)}
	tod:="09:00";if len(parts)==1{tod=parts[0]}
	m:=timeOfDay.FindStringSubmatch(tod);if m==nil{return time.Time{},fmt.Errorf("unsupported time expression %q",input)}
	h,_:=strconv.Atoi(m[1]);minute:=0;if m[2]!=""{minute,_=strconv.Atoi(m[2])};if minute>59{return time.Time{},fmt.Errorf("minute must be below 60")};ampm:=m[3]
	if ampm!=""{if h<1||h>12{return time.Time{},fmt.Errorf("invalid 12-hour time")};if ampm=="pm"&&h<12{h+=12};if ampm=="am"&&h==12{h=0}}else if h>23{return time.Time{},fmt.Errorf("hour must be below 24")}
	result:=time.Date(base.Year(),base.Month(),base.Day(),h,minute,0,0,loc)
	if result.Before(localNow)&&len(parts)==0{result=result.AddDate(0,0,1)}
	return result,nil
}
func weekday(s string)(time.Weekday,bool){for i,name:=range []string{"sunday","monday","tuesday","wednesday","thursday","friday","saturday"}{if s==name||s==name[:3]{return time.Weekday(i),true}};return 0,false}
