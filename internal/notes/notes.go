package notes

import (
	"context"
	"database/sql"
	"strings"

	"secondbrain/internal/db"
	"secondbrain/internal/search"
)

type Service struct { db *db.DB }
func New(database *db.DB)*Service{return &Service{db:database}}

func(s *Service)Add(ctx context.Context,body,source string)(int64,error){
	return s.AddWithOrigin(ctx,body,source,0)
}
func(s *Service)AddWithOrigin(ctx context.Context,body,source string,origin int64)(int64,error){
	body=strings.TrimSpace(body);if body==""{return 0,sql.ErrNoRows};if source!="voice"{source="text"}
	tx,err:=s.db.BeginTx(ctx,nil);if err!=nil{return 0,err};defer tx.Rollback()
	res,err:=tx.ExecContext(ctx,`INSERT INTO notes(created_at,body,source,origin_inbox_id) VALUES(datetime('now'),?,?,NULLIF(?,0))`,body,source,origin);if err!=nil{return 0,err};id,err:=res.LastInsertId();if err!=nil{return 0,err}
	if err=search.IndexNote(ctx,tx,id,body,"");err!=nil{return 0,err};return id,tx.Commit()
}

func(s *Service)Search(ctx context.Context,query string,limit,offset int)([]search.Result,string,error){query+=" type:note";return search.Query(ctx,s.db.DB,query,limit,offset)}
