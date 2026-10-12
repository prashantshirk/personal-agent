package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

type DB struct { *sql.DB }

func Open(ctx context.Context, dir string) (*DB, error) {
	if err := os.MkdirAll(dir, 0700); err != nil { return nil, fmt.Errorf("create data directory: %w", err) }
	path := filepath.Join(dir, "secondbrain.db")
	sqlDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil { return nil, fmt.Errorf("open SQLite: %w", err) }
	sqlDB.SetMaxOpenConns(1)
	if err := sqlDB.PingContext(ctx); err != nil { sqlDB.Close(); return nil, fmt.Errorf("connect SQLite: %w", err) }
	d := &DB{sqlDB}
	if err := d.Migrate(ctx); err != nil { sqlDB.Close(); return nil, err }
	return d, nil
}

func (d *DB) Migrate(ctx context.Context) error {
	var version int
	if err := d.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil { return err }
	for version < 10 {
		next:=version+1
		filename:=fmt.Sprintf("migrations/%03d_initial.sql",next);if next==2{filename="migrations/002_actions.sql"};if next==3{filename="migrations/003_transcripts.sql"};if next==4{filename="migrations/004_undo_origin.sql"};if next==5{filename="migrations/005_transcript_origin.sql"};if next==6{filename="migrations/006_action_origin.sql"};if next==7{filename="migrations/007_index_progress.sql"};if next==8{filename="migrations/008_firecrawl_backups.sql"};if next==9{filename="migrations/009_chat_history.sql"};if next==10{filename="migrations/010_schedules.sql"}
		sqlText,err:=migrations.ReadFile(filename);if err!=nil{return err}
		tx,err:=d.BeginTx(ctx,nil);if err!=nil{return err}
		if _,err=tx.ExecContext(ctx,string(sqlText));err!=nil{tx.Rollback();return fmt.Errorf("apply migration %d: %w",next,err)}
		if _,err=tx.ExecContext(ctx,fmt.Sprintf("PRAGMA user_version = %d",next));err!=nil{tx.Rollback();return err}
		if err=tx.Commit();err!=nil{return err};version=next
	}
	return nil
}

func (d *DB) LogInbox(ctx context.Context, kind, raw string) (int64, error) {
	res, err := d.ExecContext(ctx, `INSERT INTO inbox(received_at,kind,raw) VALUES(datetime('now'),?,?)`, kind, raw)
	if err != nil { return 0, err }; return res.LastInsertId()
}

func (d *DB) MarkInbox(ctx context.Context, id int64, err error) {
	if err != nil { _, _ = d.ExecContext(ctx, `UPDATE inbox SET error=? WHERE id=?`, err.Error(), id); return }
	_, _ = d.ExecContext(ctx, `UPDATE inbox SET handled=1 WHERE id=?`, id)
}
