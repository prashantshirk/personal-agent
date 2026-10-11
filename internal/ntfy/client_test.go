package ntfy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPublishHeaders(t *testing.T){
	server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){if r.URL.Path!="/alerts"{t.Errorf("path=%s",r.URL.Path)};if r.Header.Get("Priority")!="urgent"{t.Error("missing urgent priority")};if r.Header.Get("Title")!="Reminder"{t.Error("missing title")};if r.Header.Get("Authorization")!="Bearer token"{t.Error("missing bearer token")};w.WriteHeader(http.StatusOK)}));defer server.Close()
	if err:=New(server.URL,"alerts","token").Publish(context.Background(),"Reminder","call bank","urgent");err!=nil{t.Fatal(err)}
}
