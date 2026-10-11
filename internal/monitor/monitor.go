package monitor

import (
	"context"
	"log/slog"
	"time"

	"secondbrain/internal/db"
	"secondbrain/internal/ntfy"
)

func Run(ctx context.Context,path string,database *db.DB,client *ntfy.Client,logger *slog.Logger){ticker:=time.NewTicker(5*time.Minute);defer ticker.Stop();lastFailed:=0;lastDisk:=false
	check:=func(){if client==nil{return};var failed int;if err:=database.QueryRowContext(ctx,`SELECT count(*) FROM jobs WHERE status='failed'`).Scan(&failed);err==nil{if failed<lastFailed{lastFailed=failed};if failed>lastFailed{if err:=client.Publish(ctx,"Second Brain job failure","One or more background jobs have failed. Check the service journal.","default");err!=nil{logger.Warn("failed-job alert failed","error",err)}else{lastFailed=failed}}}
		percent,err:=DiskPercent(path);if err==nil{over:=percent>=80;if over&&!lastDisk{if err:=client.Publish(ctx,"Second Brain disk warning","Data volume usage is above 80%.","default");err!=nil{logger.Warn("disk alert failed","error",err)}else{lastDisk=true}};if !over{lastDisk=false}}}
	check();for{select{case<-ctx.Done():return;case<-ticker.C:check()}}
}
