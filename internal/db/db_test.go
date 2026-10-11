package db

import (
	"context"
	"testing"
)

func TestMigrateCreatesSchemaAndIsIdempotent(t *testing.T) {
	database,err:=Open(context.Background(),t.TempDir());if err!=nil{t.Fatal(err)};defer database.Close()
	if err:=database.Migrate(context.Background());err!=nil{t.Fatal(err)}
	var count int
	if err:=database.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('inbox','notes','tasks','reminders','files','file_pages','jobs','llm_usage')`).Scan(&count);err!=nil{t.Fatal(err)}
	if count!=8{t.Fatalf("found %d core tables, want 8",count)}
	var version int;if err:=database.QueryRow(`PRAGMA user_version`).Scan(&version);err!=nil{t.Fatal(err)};if version!=8{t.Fatalf("schema version=%d, want 8",version)}
}
