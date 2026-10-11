package reminders

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"secondbrain/internal/db"
	"secondbrain/internal/ntfy"
	"secondbrain/internal/telegram"
)

type Service struct { db *db.DB; api *telegram.Client; loc *time.Location; log *slog.Logger; chatIDValue int64; notifier *ntfy.Client }
func New(database *db.DB,api *telegram.Client,timezone string,chatID int64,logger *slog.Logger)(*Service,error){loc,err:=time.LoadLocation(timezone);if err!=nil{return nil,err};return &Service{db:database,api:api,loc:loc,chatIDValue:chatID,log:logger},nil}
func(s *Service)Location()*time.Location{return s.loc}
func(s *Service)SetNotifier(client *ntfy.Client){s.notifier=client}

func(s *Service)AddReminder(ctx context.Context,when,text,repeat string,critical bool)(int64,time.Time,error){
	return s.AddReminderFrom(ctx,when,text,repeat,critical,0)
}
func(s *Service)AddTask(ctx context.Context,body string)(int64,error){return s.AddTaskFrom(ctx,body,0)}
func(s *Service)AddTaskFrom(ctx context.Context,body string,origin int64)(int64,error){res,err:=s.db.ExecContext(ctx,`INSERT INTO tasks(created_at,body,origin_inbox_id) VALUES(?,?,NULLIF(?,0))`,time.Now().UTC().Format(time.RFC3339),strings.TrimSpace(body),origin);if err!=nil{return 0,err};return res.LastInsertId()}
func(s *Service)AddReminderFrom(ctx context.Context,when,text,repeat string,critical bool,origin int64)(int64,time.Time,error){
	due,err:=ParseWhen(when,time.Now(),s.loc);if err!=nil{return 0,time.Time{},err};if strings.TrimSpace(text)==""{return 0,time.Time{},fmt.Errorf("reminder text is required")}
	var repeatValue any;if repeat!=""{if repeat!="daily"&&repeat!="weekly"&&repeat!="monthly"{p:=cron.NewParser(cron.Minute|cron.Hour|cron.Dom|cron.Month|cron.Dow);if _,err:=p.Parse(repeat);err!=nil{return 0,time.Time{},fmt.Errorf("invalid recurrence rule: %w",err)}};repeatValue=repeat}
	res,err:=s.db.ExecContext(ctx,`INSERT INTO reminders(body,due_at,repeat_rule,critical,status,origin_inbox_id) VALUES(?,?,?,?,'scheduled',NULLIF(?,0))`,text,due.UTC().Format(time.RFC3339Nano),repeatValue,boolInt(critical),origin);if err!=nil{return 0,time.Time{},err};id,err:=res.LastInsertId();return id,due,err
}

func(s *Service)ListReminders(ctx context.Context)(string,error){
	rows,err:=s.db.QueryContext(ctx,`SELECT id,body,due_at,critical,repeat_rule FROM reminders WHERE status IN ('scheduled','fired') ORDER BY due_at LIMIT 25`);if err!=nil{return "",err};defer rows.Close();var b strings.Builder;for rows.Next(){var id int64;var body,due string;var critical int;var repeat sql.NullString;if err:=rows.Scan(&id,&body,&due,&critical,&repeat);err!=nil{return "",err};t,_:=time.Parse(time.RFC3339Nano,due);fmt.Fprintf(&b,"%d. %s — %s",id,body,t.In(s.loc).Format("Mon Jan 2 15:04"));if critical!=0{b.WriteString(" [critical]")};if repeat.Valid{fmt.Fprintf(&b," [%s]",repeat.String)};b.WriteByte('\n')};if err:=rows.Err();err!=nil{return "",err};if b.Len()==0{return "No upcoming reminders.",nil};return b.String(),nil
}
func(s *Service)ListTasks(ctx context.Context)(string,error){rows,err:=s.db.QueryContext(ctx,`SELECT id,body,due_at FROM tasks WHERE status='open' ORDER BY id DESC LIMIT 25`);if err!=nil{return "",err};defer rows.Close();var b strings.Builder;for rows.Next(){var id int64;var body string;var due sql.NullString;if err:=rows.Scan(&id,&body,&due);err!=nil{return "",err};fmt.Fprintf(&b,"%d. %s",id,body);if due.Valid{fmt.Fprintf(&b," — due %s",due.String)};b.WriteByte('\n')};if err:=rows.Err();err!=nil{return "",err};if b.Len()==0{return "No open tasks.",nil};return b.String(),nil}

func(s *Service)Done(ctx context.Context,id int64)error{
	res,err:=s.db.ExecContext(ctx,`UPDATE reminders SET status='acked',acked_at=?,next_attempt_at=NULL WHERE id=? AND status IN ('scheduled','fired')`,time.Now().UTC().Format(time.RFC3339Nano),id);if err!=nil{return err};n,_:=res.RowsAffected();if n>0{return s.scheduleNext(ctx,id)}
	_,err=s.db.ExecContext(ctx,`UPDATE tasks SET status='done',done_at=? WHERE id=? AND status='open'`,time.Now().UTC().Format(time.RFC3339Nano),id);return err
}
func(s *Service)CompleteTask(ctx context.Context,id int64)error{res,err:=s.db.ExecContext(ctx,`UPDATE tasks SET status='done',done_at=? WHERE id=? AND status='open'`,time.Now().UTC().Format(time.RFC3339Nano),id);if err!=nil{return err};n,_:=res.RowsAffected();if n==0{return fmt.Errorf("task not found or already complete")};return nil}
func(s *Service)Cancel(ctx context.Context,id int64)error{_,err:=s.db.ExecContext(ctx,`UPDATE reminders SET status='cancelled' WHERE id=? AND status IN ('scheduled','fired')`,id);return err}
func(s *Service)Snooze(ctx context.Context,id int64,d time.Duration)error{_,err:=s.db.ExecContext(ctx,`UPDATE reminders SET status='scheduled',due_at=?,attempts=0,next_attempt_at=NULL WHERE id=? AND status='fired'`,time.Now().Add(d).UTC().Format(time.RFC3339Nano),id);return err}

func(s *Service)Run(ctx context.Context){ticker:=time.NewTicker(20*time.Second);defer ticker.Stop();s.fireDue(ctx);for{select{case<-ctx.Done():return;case<-ticker.C:s.fireDue(ctx)}}}
func(s *Service)fireDue(ctx context.Context){
	now:=time.Now().UTC();rows,err:=s.db.QueryContext(ctx,`SELECT id,body,due_at,critical,attempts FROM reminders WHERE (status='scheduled' AND due_at<=?) OR (status='fired' AND critical=1 AND attempts<12 AND next_attempt_at<=?) ORDER BY due_at LIMIT 20`,now.Format(time.RFC3339Nano),now.Format(time.RFC3339Nano));if err!=nil{s.log.Error("select reminders failed","error",err);return}
	type dueReminder struct{id int64;body,due string;critical,attempts int};var due []dueReminder;for rows.Next(){var r dueReminder;if err:=rows.Scan(&r.id,&r.body,&r.due,&r.critical,&r.attempts);err!=nil{s.log.Error("scan reminder failed","error",err);break};due=append(due,r)};rows.Close()
	for _,r:=range due{parsed,_:=time.Parse(time.RFC3339Nano,r.due);late:=now.Sub(parsed)>time.Minute;label:="Reminder";if late{label="Reminder (late)"};message:=fmt.Sprintf("%s: %s",label,r.body)
		keyboard:=telegram.InlineKeyboardMarkup{InlineKeyboard:[][]telegram.InlineKeyboardButton{{{Text:"Done",CallbackData:fmt.Sprintf("reminder:done:%d",r.id)},{Text:"Snooze 10 min",CallbackData:fmt.Sprintf("reminder:snooze10:%d",r.id)},{Text:"Snooze 1 h",CallbackData:fmt.Sprintf("reminder:snooze60:%d",r.id)}}}}
		if r.critical!=0&&s.notifier!=nil{if err:=s.notifier.Publish(ctx,"Critical reminder",r.body,"urgent");err!=nil{s.log.Error("ntfy alert failed","reminder_id",r.id,"error",err)}}
		if err:=s.api.SendMessageWithKeyboard(ctx,s.chatID(ctx),message,keyboard);err!=nil{s.log.Error("send reminder failed","reminder_id",r.id,"error",err);continue}
		if r.critical!=0{_,err=s.db.ExecContext(ctx,`UPDATE reminders SET status='fired',attempts=attempts+1,next_attempt_at=? WHERE id=?`,now.Add(5*time.Minute).Format(time.RFC3339Nano),r.id)}else{_,err=s.db.ExecContext(ctx,`UPDATE reminders SET status='fired' WHERE id=?`,r.id)};if err!=nil{s.log.Error("update fired reminder failed","reminder_id",r.id,"error",err)}
	}
}

// The private chat ID equals the allowlisted Telegram user ID for this single-user bot.
func(s *Service)chatID(context.Context)int64{return s.chatIDValue}

func(s *Service)scheduleNext(ctx context.Context,id int64)error{
	var due,rule string;err:=s.db.QueryRowContext(ctx,`SELECT due_at,COALESCE(repeat_rule,'') FROM reminders WHERE id=?`,id).Scan(&due,&rule);if err!=nil{return err};if rule==""{return nil};previous,err:=time.Parse(time.RFC3339Nano,due);if err!=nil{return err};local:=previous.In(s.loc);var next time.Time
	switch rule{case "daily":next=local.AddDate(0,0,1);case "weekly":next=local.AddDate(0,0,7);case "monthly":next=addMonthClamped(local);default:p:=cron.NewParser(cron.Minute|cron.Hour|cron.Dom|cron.Month|cron.Dow);schedule,e:=p.Parse(rule);if e!=nil{return e};next=schedule.Next(local)}
	_,err=s.db.ExecContext(ctx,`UPDATE reminders SET due_at=?,status='scheduled',attempts=0,next_attempt_at=NULL,acked_at=NULL WHERE id=?`,next.UTC().Format(time.RFC3339Nano),id);return err
}
func boolInt(v bool)int{if v{return 1};return 0}
func parseID(s string)(int64,error){return strconv.ParseInt(strings.TrimSpace(s),10,64)}

func addMonthClamped(t time.Time)time.Time{
	y,m,_:=t.Date();target:=m+1;if target>time.December{y++;target=time.January};last:=time.Date(y,target+1,0,0,0,0,0,t.Location()).Day();day:=t.Day();if day>last{day=last};return time.Date(y,target,day,t.Hour(),t.Minute(),t.Second(),t.Nanosecond(),t.Location())
}
