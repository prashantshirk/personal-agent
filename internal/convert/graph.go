package convert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	graphTokenURL = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
	graphBase     = "https://graph.microsoft.com/v1.0"
	graphScope    = "Files.ReadWrite offline_access openid profile"
)

var graphMu sync.Mutex

type graphToken struct {
	ClientID     string `json:"client_id"`
	RefreshToken string `json:"refresh_token"`
}

// GraphEnabled reports whether a usable Microsoft Graph token file is present.
func GraphEnabled(tokenFile string) bool {
	if tokenFile == "" {
		return false
	}
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return false
	}
	var t graphToken
	return json.Unmarshal(data, &t) == nil && t.ClientID != "" && t.RefreshToken != ""
}

// graphAccessToken refreshes and returns an access token, persisting the rotated refresh token.
func graphAccessToken(ctx context.Context, tokenFile string) (string, error) {
	graphMu.Lock()
	defer graphMu.Unlock()
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return "", err
	}
	var t graphToken
	if err := json.Unmarshal(data, &t); err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("client_id", t.ClientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", t.RefreshToken)
	form.Set("scope", graphScope)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphTokenURL, bytes.NewReader([]byte(form.Encode())))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("graph token refresh HTTP %d: %s", resp.StatusCode, string(body))
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.RefreshToken != "" && out.RefreshToken != t.RefreshToken {
		t.RefreshToken = out.RefreshToken
		if updated, mErr := json.Marshal(t); mErr == nil {
			tmp := tokenFile + ".tmp"
			if os.WriteFile(tmp, updated, 0600) == nil {
				_ = os.Rename(tmp, tokenFile)
			}
		}
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("graph token refresh returned no access token")
	}
	return out.AccessToken, nil
}

// PLACEHOLDER_GRAPH_CONVERT
// GraphDocxToPDF converts a document to PDF using Microsoft Graph (real Office rendering).
func GraphDocxToPDF(ctx context.Context, tokenFile, src, dst, ext string) error {
	at, err := graphAccessToken(ctx, tokenFile)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if ext == "" {
		ext = ".docx"
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	remote := fmt.Sprintf("SecondBrainTmp/conv-%d%s", time.Now().UnixNano(), strings.ToLower(ext))
	id, err := graphUpload(ctx, at, remote, data)
	if err != nil {
		return err
	}
	defer func() { graphDelete(context.Background(), at, id) }()
	return graphDownloadPDF(ctx, at, id, dst)
}

func graphUpload(ctx context.Context, at, remotePath string, data []byte) (string, error) {
	u := graphBase + "/me/drive/root:/" + remotePath + ":/content"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+at)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := graphClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("graph upload HTTP %d: %s", resp.StatusCode, string(body))
	}
	var item struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &item); err != nil {
		return "", err
	}
	if item.ID == "" {
		return "", fmt.Errorf("graph upload returned no item id")
	}
	return item.ID, nil
}
// PLACEHOLDER_GRAPH_DOWNLOAD
func graphDownloadPDF(ctx context.Context, at, id, dst string) error {
	u := fmt.Sprintf("%s/me/drive/items/%s/content?format=pdf", graphBase, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+at)
	resp, err := graphClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("graph convert HTTP %d: %s", resp.StatusCode, string(body))
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, resp.Body)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return closeErr
	}
	return nil
}

func graphDelete(ctx context.Context, at, id string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, graphBase+"/me/drive/items/"+id, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+at)
	if resp, err := graphClient().Do(req); err == nil {
		resp.Body.Close()
	}
}

func graphClient() *http.Client { return &http.Client{Timeout: 2 * time.Minute} }
