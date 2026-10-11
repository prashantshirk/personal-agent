package firecrawl

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Result struct { Title string `json:"title"`; URL string `json:"url"`; Description string `json:"description"`; Snippet string `json:"snippet"` }
type response struct { Success bool `json:"success"`; Data struct { Web []Result `json:"web"`; News []Result `json:"news"` } `json:"data"`; Error string `json:"error"` }
type Client struct { key,endpoint string; http *http.Client }
func New(key string, timeout time.Duration) *Client { if timeout<=0 { timeout=15*time.Second }; return &Client{key:key,endpoint:"https://api.firecrawl.dev/v2/search",http:&http.Client{Timeout:timeout}} }

// Search reserves one daily call before making the request, so concurrent callers cannot exceed the cap.
func(c *Client)Search(ctx context.Context,db *sql.DB,query string,limit int,dailyCap int)([]Result,error){
	if strings.TrimSpace(query)=="" { return nil,fmt.Errorf("search query is empty") }; if limit<1||limit>5 { limit=5 }; if dailyCap<1 { return nil,fmt.Errorf("web search is disabled by the daily call cap") }
	day:=time.Now().UTC().Format("2006-01-02");tx,err:=db.BeginTx(ctx,nil);if err!=nil{return nil,err};defer tx.Rollback()
	if _,err=tx.ExecContext(ctx,`INSERT INTO firecrawl_usage(day,calls) VALUES(?,0) ON CONFLICT(day) DO NOTHING`,day);err!=nil{return nil,err}
	res,err:=tx.ExecContext(ctx,`UPDATE firecrawl_usage SET calls=calls+1 WHERE day=? AND calls<?`,day,dailyCap);if err!=nil{return nil,err};n,_:=res.RowsAffected();if n==0{return nil,fmt.Errorf("daily web search limit reached")};if err=tx.Commit();err!=nil{return nil,err}
	payload,_:=json.Marshal(map[string]any{"query":query,"limit":limit,"sources":[]string{"web"},"scrapeOptions":map[string]any{}});req,err:=http.NewRequestWithContext(ctx,http.MethodPost,c.endpoint,bytes.NewReader(payload));if err!=nil{return nil,err};req.Header.Set("Authorization","Bearer "+c.key);req.Header.Set("Content-Type","application/json")
	resp,err:=c.http.Do(req);if err!=nil{return nil,fmt.Errorf("Firecrawl request failed: %w",err)};defer resp.Body.Close();body,err:=io.ReadAll(io.LimitReader(resp.Body,2<<20));if err!=nil{return nil,err};if resp.StatusCode<200||resp.StatusCode>=300{return nil,fmt.Errorf("Firecrawl returned HTTP %d",resp.StatusCode)}
	var decoded response;if err=json.Unmarshal(body,&decoded);err!=nil{return nil,fmt.Errorf("decode Firecrawl response: %w",err)};if !decoded.Success&&decoded.Error!=""{return nil,fmt.Errorf("Firecrawl search failed: %s",decoded.Error)}
	results:=decoded.Data.Web;if len(results)==0{results=decoded.Data.News};for i:=range results{results[i].Title=clip(results[i].Title,200);results[i].URL=clip(results[i].URL,500);results[i].Description=clip(results[i].Description,400);results[i].Snippet=clip(results[i].Snippet,400);if results[i].Snippet==""{results[i].Snippet=results[i].Description}}
	if len(results)>limit{results=results[:limit]};return results,nil
}
func clip(s string,n int)string{s=strings.TrimSpace(s);s=strings.ReplaceAll(s,"UNTRUSTED_WEB_RESULTS_BEGIN","UNTRUSTED WEB RESULTS BEGIN");s=strings.ReplaceAll(s,"UNTRUSTED_WEB_RESULTS_END","UNTRUSTED WEB RESULTS END");if len(s)>n{return s[:n]+"…"};return s}
