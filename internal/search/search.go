package search

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
)

var lineBreakHyphen = regexp.MustCompile(`-\s*\r?\n\s*`)
var whitespace = regexp.MustCompile(`\s+`)
var searchToken = regexp.MustCompile(`"(?:[^"]|"")*"|\S+`)

type Filter struct { Type, Tag string; After, Before time.Time }
type Result struct { Kind string; RefID int64; Page int; Title, Snippet string; CreatedAt string }
func Normalize(s string) string {
	s=norm.NFC.String(s)
	s=lineBreakHyphen.ReplaceAllString(s,"")
	lines:=strings.Split(s,"\n");clean:=lines[:0]
	for _,line:=range lines { line=strings.TrimSpace(line);if line!=""{clean=append(clean,line)} }
	s=whitespace.ReplaceAllString(strings.Join(clean," ")," ")
	return strings.ToLower(strings.TrimSpace(s))
}

// Parse extracts date/type/tag filters and preserves the remaining phrase.
func Parse(raw string)(string,Filter,error){
	var f Filter;var terms []string
	for _,part:=range searchToken.FindAllString(raw,-1){
		lower:=strings.ToLower(part)
		switch {
		case strings.HasPrefix(lower,"\""):terms=append(terms,part)
		case strings.HasPrefix(lower,"type:"):f.Type=strings.TrimPrefix(lower,"type:")
		case strings.HasPrefix(lower,"tag:"):f.Tag=part[len("tag:"):]
		case strings.HasPrefix(lower,"after:"):t,e:=time.Parse("2006-01-02",strings.TrimPrefix(lower,"after:"));if e!=nil{return "",f,fmt.Errorf("invalid after date %q",part)};f.After=t
		case strings.HasPrefix(lower,"before:"):t,e:=time.Parse("2006-01-02",strings.TrimPrefix(lower,"before:"));if e!=nil{return "",f,fmt.Errorf("invalid before date %q",part)};f.Before=t
		default:terms=append(terms,part)
		}
	}
	if f.Type!=""&&f.Type!="note"&&f.Type!="file"&&f.Type!="pdf"&&f.Type!="photo"&&f.Type!="video"{return "",f,fmt.Errorf("unsupported type filter %q",f.Type)}
	return Normalize(strings.Join(terms," ")),f,nil
}

func Query(ctx context.Context, db *sql.DB, raw string, limit, offset int)([]Result,string,error){
	query,filter,err:=Parse(raw);if err!=nil{return nil,"",err};if query==""{return nil,"and",nil};if limit<1||limit>100{limit=10};if offset<0{offset=0}
	terms:=searchToken.FindAllString(query,-1);andQ:=joinTerms(terms," AND ");orQ:=joinTerms(terms," OR ")
	for _,candidate:=range []struct{mode,table,q string}{{"and","search_fts",andQ},{"or","search_fts",orQ},{"trigram","search_tri",trigramQuery(query)}}{
		if candidate.q==""{continue}
		rows,e:=runQuery(ctx,db,candidate.table,candidate.q,filter,limit,offset);if e!=nil { if candidate.table=="search_tri"{return nil,candidate.mode,e};continue };if len(rows)>0{return rows,candidate.mode,nil}
	}
	return nil,"and",nil
}

func joinTerms(terms []string,op string)string{quoted:=make([]string,0,len(terms));for _,term:=range terms{quoted=append(quoted,quoteToken(term))};return strings.Join(quoted,op)}
func quoteToken(s string)string{if strings.HasPrefix(s,`"`)&&strings.HasSuffix(s,`"`){s=strings.Trim(s,`"`);s=strings.ReplaceAll(s,`""`,`"`)};if strings.HasSuffix(s,"*"){prefix:=strings.TrimSuffix(s,"*");if prefix!=""&&strings.Trim(prefix,"abcdefghijklmnopqrstuvwxyz0123456789_")==""{return prefix+"*"}};return `"`+strings.ReplaceAll(s,`"`,`""`)+`"`}
func trigramQuery(s string)string{seen:=map[string]bool{};var grams []string;for _,token:=range searchToken.FindAllString(s,-1){token=strings.Trim(token,`"`);runes:=[]rune(token);for i:=0;i+3<=len(runes);i++{gram:=string(runes[i:i+3]);if !seen[gram]{seen[gram]=true;grams=append(grams,quote(gram))}}};return strings.Join(grams," OR ")}
func quote(s string)string{return `"`+strings.ReplaceAll(s,`"`,`""`)+`"`}

func runQuery(ctx context.Context,db *sql.DB,table,match string,f Filter,limit,offset int)([]Result,error){
	if table!="search_fts"&&table!="search_tri"{return nil,fmt.Errorf("invalid index")}
	query:=`SELECT kind,CAST(ref_id AS INTEGER),COALESCE(CAST(page AS INTEGER),0),title,snippet(`+table+`,2,'','',' … ',12),
COALESCE((SELECT created_at FROM notes n WHERE n.id=CAST(`+table+`.ref_id AS INTEGER) AND `+table+`.kind='note'),(SELECT created_at FROM files f WHERE f.id=CAST(`+table+`.ref_id AS INTEGER) AND `+table+`.kind='file'),'')
FROM `+table+` WHERE `+table+` MATCH ?
AND (?='' OR kind=?) AND (?='' OR lower(tags) LIKE '%'||lower(?)||'%')
AND (?='' OR EXISTS(SELECT 1 FROM files x WHERE x.id=CAST(`+table+`.ref_id AS INTEGER) AND x.mime LIKE ?||'%'))
AND (?='' OR COALESCE((SELECT created_at FROM notes n WHERE n.id=CAST(`+table+`.ref_id AS INTEGER)),(SELECT created_at FROM files f WHERE f.id=CAST(`+table+`.ref_id AS INTEGER)), '')>=?)
AND (?='' OR COALESCE((SELECT created_at FROM notes n WHERE n.id=CAST(`+table+`.ref_id AS INTEGER)),(SELECT created_at FROM files f WHERE f.id=CAST(`+table+`.ref_id AS INTEGER)), '')<?)
ORDER BY bm25(`+table+`,8.0,5.0,1.0) LIMIT ? OFFSET ?`
	typeName:=f.Type;if typeName=="photo"{typeName="file"};if typeName=="pdf"||typeName=="video"{typeName="file"}
	tag:=f.Tag;mime:="";if f.Type=="pdf"{mime="application/pdf"};if f.Type=="photo"{mime="image/"};if f.Type=="video"{mime="video/"}
	after,before:="","";if !f.After.IsZero(){after=f.After.Format("2006-01-02")};if !f.Before.IsZero(){before=f.Before.AddDate(0,0,1).Format("2006-01-02")}
	rows,err:=db.QueryContext(ctx,query,match,typeName,typeName,tag,tag,mime,mime,after,after,before,before,limit,offset);if err!=nil{return nil,err};defer rows.Close()
	var result []Result;for rows.Next(){var r Result;if err:=rows.Scan(&r.Kind,&r.RefID,&r.Page,&r.Title,&r.Snippet,&r.CreatedAt);err!=nil{return nil,err};result=append(result,r)};return result,rows.Err()
}

func IndexNote(ctx context.Context,tx *sql.Tx,id int64,body,tags string)error{
	clean:=Normalize(body);tagText:=Normalize(tags)
	if _,err:=tx.ExecContext(ctx,`DELETE FROM search_fts WHERE kind='note' AND ref_id=?`,id);err!=nil{return err}
	if _,err:=tx.ExecContext(ctx,`DELETE FROM search_tri WHERE kind='note' AND ref_id=?`,id);err!=nil{return err}
	for _,table:=range []string{"search_fts","search_tri"}{if _,err:=tx.ExecContext(ctx,`INSERT INTO `+table+`(title,tags,body,kind,ref_id,page) VALUES(?,?,?,?,?,0)`,body,tagText,clean,"note",id);err!=nil{return err}}
	return nil
}

func IndexFileMetadata(ctx context.Context,tx *sql.Tx,id int64,title,caption,tags string)error{
	for _,table:=range []string{"search_fts","search_tri"}{if _,err:=tx.ExecContext(ctx,`INSERT INTO `+table+`(title,tags,body,kind,ref_id,page) VALUES(?,?,?,?,?,0)`,Normalize(title),Normalize(tags),Normalize(caption),"file",id);err!=nil{return err}}
	return nil
}

func IndexFilePage(ctx context.Context,tx *sql.Tx,id int64,page int,title,tags,body string)error{
	for _,table:=range []string{"search_fts","search_tri"}{if _,err:=tx.ExecContext(ctx,`DELETE FROM `+table+` WHERE kind='file' AND CAST(ref_id AS INTEGER)=? AND CAST(page AS INTEGER)=?`,id,page);err!=nil{return err};if _,err:=tx.ExecContext(ctx,`INSERT INTO `+table+`(title,tags,body,kind,ref_id,page) VALUES(?,?,?,?,?,?)`,Normalize(title),Normalize(tags),Normalize(body),"file",id,page);err!=nil{return err}}
	return nil
}
