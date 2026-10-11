package stt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const MaxFreeRequestBytes int64=25<<20
type Service struct{apiKey,model,language string;http *http.Client}
type APIError struct{Status int;RetryAfter time.Duration;Body string}
func(e *APIError)Error()string{return fmt.Sprintf("Groq transcription returned HTTP %d",e.Status)}

func New(apiKey,model,language string)*Service{return &Service{apiKey:apiKey,model:model,language:language,http:&http.Client{Timeout:90*time.Second}}}
func(s *Service)Transcribe(ctx context.Context,path,language,tempDir string)(string,error){
	if s.apiKey==""{return "",errors.New("GROQ_API_KEY is not configured")};info,err:=os.Stat(path);if err!=nil{return "",err};if info.Size()>MaxFreeRequestBytes{return "",fmt.Errorf("voice file is over Groq's 25 MB free-tier request limit")}
	if language==""||language=="auto"{language=s.language};text,err:=s.transcribeWithRetry(ctx,path,language);var apiErr *APIError
	if errors.As(err,&apiErr)&&(apiErr.Status==400||apiErr.Status==415){converted:=filepath.Join(tempDir,"voice-converted.flac");if convertErr:=convert(ctx,path,converted);convertErr!=nil{return "",fmt.Errorf("Groq rejected the OGG voice file (%v), and ffmpeg conversion failed: %w",err,convertErr)};defer os.Remove(converted);text,err=s.transcribeWithRetry(ctx,converted,language)}
	return text,err
}
func(s *Service)transcribeWithRetry(ctx context.Context,path,language string)(string,error){
	var last error
	for attempt:=0;attempt<4;attempt++{text,apiErr:=s.transcribeOnce(ctx,path,language);if apiErr==nil{return text,nil};last=apiErr;var e *APIError;if !errors.As(apiErr,&e)||!(e.Status==429||e.Status>=500)||attempt==3{return "",apiErr};wait:=e.RetryAfter;if wait<=0{wait=time.Duration(1<<attempt)*time.Second};select{case<-ctx.Done():return "",ctx.Err();case<-time.After(wait):}}
	return "",last
}
func(s *Service)transcribeOnce(ctx context.Context,path,language string)(string,error){
	pr,pw:=io.Pipe();writerDone:=make(chan error,1);mw:=multipart.NewWriter(pw)
	go func(){defer close(writerDone);defer pw.Close();var err error
		_ = mw.WriteField("model",s.model);_ = mw.WriteField("response_format","json");if language!=""&&language!="auto"{_ = mw.WriteField("language",language)}
		file,e:=os.Open(path);if e!=nil{err=e;_ = pw.CloseWithError(e);writerDone<-err;return};defer file.Close()
		part,e:=mw.CreateFormFile("file",filepath.Base(path));if e==nil{_,e=io.Copy(part,file)};if e==nil{e=mw.Close()};err=e;if err!=nil{_ = pw.CloseWithError(err)};writerDone<-err
	}()
	req,err:=http.NewRequestWithContext(ctx,http.MethodPost,"https://api.groq.com/openai/v1/audio/transcriptions",pr);if err!=nil{_ = pr.CloseWithError(err);return "",err};req.Header.Set("Authorization","Bearer "+s.apiKey);req.Header.Set("Content-Type",mw.FormDataContentType())
	resp,err:=s.http.Do(req);if err!=nil{_ = pr.CloseWithError(err);return "",err};defer resp.Body.Close();writerErr:=<-writerDone;if writerErr!=nil{return "",writerErr};data,err:=io.ReadAll(io.LimitReader(resp.Body,1<<20));if err!=nil{return "",err};if resp.StatusCode<200||resp.StatusCode>=300{return "",&APIError{Status:resp.StatusCode,RetryAfter:parseRetryAfter(resp.Header.Get("Retry-After")),Body:string(data)}}
	var out struct{Text string `json:"text"`};if err:=json.Unmarshal(data,&out);err!=nil{return "",fmt.Errorf("invalid Groq transcription response: %w",err)};if strings.TrimSpace(out.Text)==""{return "",errors.New("Groq returned an empty transcript")};return strings.TrimSpace(out.Text),nil
}
func parseRetryAfter(v string)time.Duration{if n,err:=strconv.Atoi(v);err==nil&&n>0{return time.Duration(n)*time.Second};if t,err:=http.ParseTime(v);err==nil{if d:=time.Until(t);d>0{return d}};return 0}
func convert(ctx context.Context,input,output string)error{cmdCtx,cancel:=context.WithTimeout(ctx,30*time.Second);defer cancel();cmd:=exec.CommandContext(cmdCtx,"ffmpeg","-nostdin","-v","error","-y","-i",input,"-ar","16000","-ac","1","-c:a","flac",output);if out,err:=cmd.CombinedOutput();err!=nil{return fmt.Errorf("ffmpeg: %w (%s)",err,strings.TrimSpace(string(out)))};return nil}
