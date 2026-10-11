package firecrawl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"secondbrain/internal/db"
)

func TestSearchReturnsLimitedSnippetAndEnforcesDailyCap(t *testing.T){
	database,err:=db.Open(context.Background(),t.TempDir());if err!=nil{t.Fatal(err)};defer database.Close()
	server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){if r.Header.Get("Authorization")!="Bearer test-key"{t.Errorf("missing bearer auth")};var payload struct{Query string `json:"query"`;Limit int `json:"limit"`;Sources []string `json:"sources"`};if err:=json.NewDecoder(r.Body).Decode(&payload);err!=nil{t.Error(err)};if payload.Query!="sample topic"||payload.Limit!=5||len(payload.Sources)!=1||payload.Sources[0]!="web"{t.Errorf("unexpected request: %+v",payload)};w.Header().Set("Content-Type","application/json");_,_=w.Write([]byte(`{"success":true,"data":{"web":[{"title":"Example","url":"https://example.test","description":"A short result"}]}}`))}));defer server.Close()
	client:=New("test-key",time.Second);client.endpoint=server.URL;results,err:=client.Search(context.Background(),database.DB,"sample topic",50,1);if err!=nil{t.Fatal(err)};if len(results)!=1||results[0].Snippet!="A short result"{t.Fatalf("unexpected results: %+v",results)}
	if _,err=client.Search(context.Background(),database.DB,"sample topic",5,1);err==nil{t.Fatal("expected daily cap error")}
}
