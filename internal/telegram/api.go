package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"mime/multipart"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type User struct { ID int64 `json:"id"`; IsBot bool `json:"is_bot"`; FirstName string `json:"first_name"` }
type Chat struct { ID int64 `json:"id"` }
type Message struct { MessageID int64 `json:"message_id"`; From *User `json:"from"`; Chat Chat `json:"chat"`; Text string `json:"text"`; Caption string `json:"caption"`; Voice *Voice `json:"voice"`; Document *Document `json:"document"`; Photo []PhotoSize `json:"photo"`; Video *Video `json:"video"` }
type Voice struct { FileID string `json:"file_id"`; FileUniqueID string `json:"file_unique_id"`; Duration int `json:"duration"`; MimeType string `json:"mime_type"`; FileSize int64 `json:"file_size"` }
type Document struct { FileID string `json:"file_id"`; FileName string `json:"file_name"`; MimeType string `json:"mime_type"`; FileSize int64 `json:"file_size"` }
type PhotoSize struct { FileID string `json:"file_id"`; Width int `json:"width"`; Height int `json:"height"`; FileSize int64 `json:"file_size"` }
type Video struct { FileID string `json:"file_id"`; FileName string `json:"file_name"`; MimeType string `json:"mime_type"`; FileSize int64 `json:"file_size"` }
type CallbackQuery struct { ID string `json:"id"`; From User `json:"from"`; Message *Message `json:"message"`; Data string `json:"data"` }
type InlineKeyboardButton struct { Text string `json:"text"`; CallbackData string `json:"callback_data,omitempty"` }
type InlineKeyboardMarkup struct { InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"` }
type Update struct { UpdateID int64 `json:"update_id"`; Message *Message `json:"message"`; EditedMessage *Message `json:"edited_message"`; CallbackQuery *CallbackQuery `json:"callback_query"` }
type apiResponse[T any] struct { OK bool `json:"ok"`; Description string `json:"description"`; Result T `json:"result"` }
type File struct { FileID string `json:"file_id"`; FileUniqueID string `json:"file_unique_id"`; FileSize int64 `json:"file_size"`; FilePath string `json:"file_path"` }
type Client struct { token, base string; http *http.Client }

func New(token, base string, timeout time.Duration) *Client {
	return &Client{token:token, base:strings.TrimRight(base,"/"), http:&http.Client{Timeout:timeout}}
}
func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	body, err := json.Marshal(params); if err != nil { return err }
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/"+method, bytes.NewReader(body)); if err != nil { return err }
	req.Header.Set("Content-Type","application/json")
	resp, err := c.http.Do(req); if err != nil { return err }; defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)); if err != nil { return err }
	var envelope struct { OK bool `json:"ok"`; Description string `json:"description"`; Result json.RawMessage `json:"result"` }
	if err := json.Unmarshal(data,&envelope); err != nil { return fmt.Errorf("telegram %s returned HTTP %d: invalid JSON",method,resp.StatusCode) }
	if !envelope.OK { return fmt.Errorf("telegram %s: %s",method,envelope.Description) }
	if result != nil { return json.Unmarshal(envelope.Result,result) }; return nil
}
func (c *Client) GetUpdates(ctx context.Context, offset int64) ([]Update,error) {
	var result []Update
	params:=map[string]any{"offset":offset,"timeout":45,"limit":100,"allowed_updates":[]string{"message","edited_message","callback_query"}}
	// Long polling needs a request timeout longer than Telegram's server-side wait.
	return result, c.callLong(ctx,"getUpdates",params,&result)
}
func (c *Client) callLong(ctx context.Context, method string, params any, result any) error {
	body,err:=json.Marshal(params); if err!=nil{return err}
	req,err:=http.NewRequestWithContext(ctx,http.MethodPost,c.base+"/bot"+c.token+"/"+method,bytes.NewReader(body));if err!=nil{return err};req.Header.Set("Content-Type","application/json")
	client:=*c.http;client.Timeout=55*time.Second
	resp,err:=client.Do(req);if err!=nil{return err};defer resp.Body.Close();data,err:=io.ReadAll(io.LimitReader(resp.Body,4<<20));if err!=nil{return err}
	var envelope struct{OK bool `json:"ok"`;Description string `json:"description"`;Result json.RawMessage `json:"result"`};if err=json.Unmarshal(data,&envelope);err!=nil{return fmt.Errorf("telegram poll returned HTTP %d: invalid JSON",resp.StatusCode)};if !envelope.OK{return fmt.Errorf("telegram poll: %s",envelope.Description)};return json.Unmarshal(envelope.Result,result)
}
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error { return c.call(ctx,"sendMessage",map[string]any{"chat_id":chatID,"text":text},nil) }
func (c *Client) SendMessageID(ctx context.Context, chatID int64, text string)(int64,error){var message struct{MessageID int64 `json:"message_id"`};err:=c.call(ctx,"sendMessage",map[string]any{"chat_id":chatID,"text":text},&message);return message.MessageID,err}
func (c *Client) EditMessageText(ctx context.Context,chatID,messageID int64,text string)error{return c.call(ctx,"editMessageText",map[string]any{"chat_id":chatID,"message_id":messageID,"text":text},nil)}
func (c *Client) SendMessageWithKeyboard(ctx context.Context, chatID int64, text string, keyboard InlineKeyboardMarkup) error { return c.call(ctx,"sendMessage",map[string]any{"chat_id":chatID,"text":text,"reply_markup":keyboard},nil) }
func (c *Client) AnswerCallback(ctx context.Context, id, text string) error { return c.call(ctx,"answerCallbackQuery",map[string]any{"callback_query_id":id,"text":text},nil) }
func (c *Client) SendFile(ctx context.Context,chatID int64,path,name,mimeType string)error{
	method,field:="sendDocument","document";if strings.HasPrefix(mimeType,"image/"){method,field="sendPhoto","photo"}else if strings.HasPrefix(mimeType,"video/"){method,field="sendVideo","video"}
	pr,pw:=io.Pipe();mw:=multipart.NewWriter(pw);done:=make(chan error,1)
	go func(){defer close(done);defer pw.Close();if err:=mw.WriteField("chat_id",strconv.FormatInt(chatID,10));err!=nil{_ = pw.CloseWithError(err);done<-err;return};file,err:=os.Open(path);if err!=nil{_ = pw.CloseWithError(err);done<-err;return};defer file.Close();part,err:=mw.CreateFormFile(field,name);if err==nil{_,err=io.Copy(part,file)};if err==nil{err=mw.Close()};if err!=nil{_ = pw.CloseWithError(err)};done<-err}()
	req,err:=http.NewRequestWithContext(ctx,http.MethodPost,c.base+"/bot"+c.token+"/"+method,pr);if err!=nil{_ = pr.CloseWithError(err);return err};req.Header.Set("Content-Type",mw.FormDataContentType());client:=*c.http;client.Timeout=15*time.Minute;resp,err:=client.Do(req);if err!=nil{_ = pr.CloseWithError(err);return err};defer resp.Body.Close();writerErr:=<-done;if writerErr!=nil{return writerErr};data,err:=io.ReadAll(io.LimitReader(resp.Body,1<<20));if err!=nil{return err};var answer struct{OK bool `json:"ok"`;Description string `json:"description"`};if err=json.Unmarshal(data,&answer);err!=nil{return fmt.Errorf("Telegram file send returned invalid JSON")};if !answer.OK{return fmt.Errorf("Telegram file send failed: %s",answer.Description)};return nil
}
func (c *Client) GetFile(ctx context.Context,fileID string)(File,error){var f File;err:=c.call(ctx,"getFile",map[string]any{"file_id":fileID},&f);return f,err}
func (c *Client) FileURL(filePath string) string {
	base,err:=url.Parse(c.base);if err!=nil{return c.base+"/file/bot"+c.token+"/"+filePath}
	base.Path=strings.TrimRight(base.Path,"/")+"/file/bot"+c.token+"/"+filePath;base.RawPath="";return base.String()
}
func (c *Client) DownloadFile(ctx context.Context,fileID,dest string,maxBytes int64)(File,error){
	f,err:=c.GetFile(ctx,fileID);if err!=nil{return File{},err};if f.FilePath==""{return File{},fmt.Errorf("Telegram did not return a file path")};if f.FileSize>maxBytes&&maxBytes>0{return File{},fmt.Errorf("file exceeds the %d byte limit",maxBytes)}
	if filepath.IsAbs(f.FilePath){if _,err:=os.Stat(f.FilePath);err==nil{in,openErr:=os.Open(f.FilePath);if openErr!=nil{return File{},openErr};defer in.Close();out,openErr:=os.OpenFile(dest,os.O_CREATE|os.O_EXCL|os.O_WRONLY,0600);if openErr!=nil{return File{},openErr};n,copyErr:=io.Copy(out,io.LimitReader(in,maxBytes+1));closeErr:=out.Close();if copyErr!=nil{_ = os.Remove(dest);return File{},copyErr};if closeErr!=nil{_ = os.Remove(dest);return File{},closeErr};if maxBytes>0&&n>maxBytes{_ = os.Remove(dest);return File{},fmt.Errorf("download exceeds the %d byte limit",maxBytes)};return f,nil}}
	req,err:=http.NewRequestWithContext(ctx,http.MethodGet,c.FileURL(f.FilePath),nil);if err!=nil{return File{},err};client:=*c.http;client.Timeout=15*time.Minute;resp,err:=client.Do(req);if err!=nil{return File{},err};defer resp.Body.Close();if resp.StatusCode<200||resp.StatusCode>=300{return File{},fmt.Errorf("Telegram file download returned HTTP %d",resp.StatusCode)}
	out,err:=os.OpenFile(dest,os.O_CREATE|os.O_EXCL|os.O_WRONLY,0600);if err!=nil{return File{},err};defer out.Close()
	written,err:=io.Copy(out,io.LimitReader(resp.Body,maxBytes+1));if err!=nil{_ = out.Close();_ = os.Remove(dest);return File{},err};if maxBytes>0&&written>maxBytes{_ = out.Close();_ = os.Remove(dest);return File{},fmt.Errorf("download exceeds the %d byte limit",maxBytes)};return f,nil
}
