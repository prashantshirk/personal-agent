package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientToolCall(t *testing.T){
	server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		if r.URL.Path!="/v1/chat/completions"{t.Errorf("path=%s",r.URL.Path)}
		if r.Header.Get("Authorization")!="Bearer test-key"{t.Error("missing auth header")}
		body,_:=io.ReadAll(r.Body);if !strings.Contains(string(body),`"tools"`){t.Error("request did not include tool definitions")}
		w.Header().Set("Content-Type","application/json");_,_=w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"add_note","arguments":"{\"text\":\"hello\"}"}}]}}],"usage":{"prompt_tokens":12,"completion_tokens":4}}`))
	}));defer server.Close()
	c:=NewClient(server.URL+"/v1","test-key","test-model",time.Second)
	got,err:=c.Complete(context.Background(),[]Message{{Role:"user",Content:"save a note"}},toolDefinitions());if err!=nil{t.Fatal(err)}
	if len(got.Message.ToolCalls)!=1||got.Message.ToolCalls[0].Function.Name!="add_note"{t.Fatalf("unexpected response: %#v",got.Message)}
}
