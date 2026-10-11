package ntfy

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct{baseURL,topic,token string;http *http.Client}
func New(baseURL,topic,token string)*Client{return &Client{baseURL:strings.TrimRight(baseURL,"/"),topic:topic,token:token,http:&http.Client{Timeout:10*time.Second}}}
func(c *Client)Publish(ctx context.Context,title,message,priority string)error{
	if strings.Contains(c.topic,"/")||strings.TrimSpace(c.topic)==""{return fmt.Errorf("invalid ntfy topic")};endpoint:=c.baseURL+"/"+url.PathEscape(c.topic);req,err:=http.NewRequestWithContext(ctx,http.MethodPost,endpoint,strings.NewReader(message));if err!=nil{return err};req.Header.Set("Authorization","Bearer "+c.token);req.Header.Set("Priority",priority);req.Header.Set("Title",title)
	resp,err:=c.http.Do(req);if err!=nil{return err};defer resp.Body.Close();if resp.StatusCode<200||resp.StatusCode>=300{return fmt.Errorf("ntfy returned HTTP %d",resp.StatusCode)};return nil
}
