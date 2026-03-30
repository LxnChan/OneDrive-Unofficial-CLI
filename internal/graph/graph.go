package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Client struct {
	AccessToken func(ctx context.Context) (string, error)
	HTTP        *http.Client
	UserAgent   string
	Verbose     bool
}

type GraphError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type User struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	UserPrincipalName string `json:"userPrincipalName"`
	Mail              string `json:"mail"`
}

type Drive struct {
	ID        string `json:"id"`
	DriveType string `json:"driveType"`
	Owner     struct {
		User struct {
			DisplayName string `json:"displayName"`
			ID          string `json:"id"`
		} `json:"user"`
	} `json:"owner"`
	Quota struct {
		Total     int64  `json:"total"`
		Used      int64  `json:"used"`
		Remaining int64  `json:"remaining"`
		Deleted   int64  `json:"deleted"`
		State     string `json:"state"`
	} `json:"quota"`
}

type DriveItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"@microsoft.graph.downloadUrl"`
	File        *struct {
		MimeType string `json:"mimeType"`
	} `json:"file"`
	Folder *struct {
		ChildCount int `json:"childCount"`
	} `json:"folder"`
}

type createUploadSessionResponse struct {
	UploadURL string `json:"uploadUrl"`
}

type driveItemListResponse struct {
	Value []DriveItem `json:"value"`
}

type TransferCallbacks struct {
	OnBytes func(n int64)
	OnChunk func()
}

type TransferOptions struct {
	ChunkSize int64
	Threads   int
	Callbacks TransferCallbacks
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) do(ctx context.Context, method, u string, body io.Reader, extraHeaders map[string]string) (*http.Response, []byte, error) {
	if c.AccessToken == nil {
		return nil, nil, errors.New("access token provider is required")
	}
	token, err := c.AccessToken(ctx)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if extraHeaders != nil {
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
	}
	if c.Verbose {
		fmt.Fprintln(os.Stderr, method, u)
	}
	res, err := c.httpClient().Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if res.Body != nil {
			_, _ = io.Copy(io.Discard, res.Body)
		}
	}()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		res.Body.Close()
		return nil, nil, err
	}
	res.Body.Close()
	if c.Verbose {
		fmt.Fprintln(os.Stderr, "Status", res.Status)
	}
	return res, b, nil
}

func (c *Client) doJSON(ctx context.Context, method, u string, body any, out any) error {
	var rdr io.Reader
	headers := map[string]string{
		"Accept": "application/json",
	}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
		headers["Content-Type"] = "application/json"
	}
	res, b, err := c.do(ctx, method, u, rdr, headers)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var ge GraphError
		_ = json.Unmarshal(b, &ge)
		if ge.Error.Message != "" || ge.Error.Code != "" {
			return fmt.Errorf("graph api failed: %s: %s", ge.Error.Code, ge.Error.Message)
		}
		return fmt.Errorf("graph api failed: %s", strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

type countingReader struct {
	r  io.Reader
	on func(n int64)
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	if n > 0 && cr.on != nil {
		cr.on(int64(n))
	}
	return n, err
}

type writerAt struct {
	f   *os.File
	off int64
}

func (w *writerAt) Write(p []byte) (int, error) {
	n, err := w.f.WriteAt(p, w.off)
	if n > 0 {
		w.off += int64(n)
	}
	return n, err
}

type countingWriter struct {
	w  io.Writer
	on func(n int64)
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	if n > 0 && cw.on != nil {
		cw.on(int64(n))
	}
	return n, err
}

func normalizeUploadChunkSize(n int64) int64 {
	const min = 5 * 1024 * 1024
	const max = 60 * 1024 * 1024
	if n <= 0 {
		return 10 * 1024 * 1024
	}
	if n < min {
		n = min
	}
	if n > max {
		n = max
	}
	n = (n / (1 * 1024 * 1024)) * (1 * 1024 * 1024)
	if n < min {
		n = min
	}
	return n
}

func normalizeDownloadChunkSize(n int64) int64 {
	const min = 5 * 1024 * 1024
	const max = 60 * 1024 * 1024
	if n <= 0 {
		return 10 * 1024 * 1024
	}
	if n < min {
		n = min
	}
	if n > max {
		n = max
	}
	return n
}

func normalizeThreads(n int) int {
	if n <= 0 {
		return 2
	}
	if n > 32 {
		return 32
	}
	return n
}

func normalizeRemotePath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "root:")
	p = strings.Trim(p, " ")
	if p == "" || p == "/" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = strings.TrimSuffix(p, "/")
	return p
}

func graphPathEscape(p string) string {
	p = normalizeRemotePath(p)
	if p == "" {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "/" + strings.Join(parts, "/")
}

func (c *Client) Me(ctx context.Context) (*User, error) {
	var out User
	if err := c.doJSON(ctx, http.MethodGet, "https://graph.microsoft.com/v1.0/me", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Drive(ctx context.Context) (*Drive, error) {
	var out Drive
	if err := c.doJSON(ctx, http.MethodGet, "https://graph.microsoft.com/v1.0/me/drive", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Root(ctx context.Context) (*DriveItem, error) {
	var out DriveItem
	u := "https://graph.microsoft.com/v1.0/me/drive/root?$select=id,name,size,folder,file"
	if err := c.doJSON(ctx, http.MethodGet, u, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Recent(ctx context.Context, top int) ([]DriveItem, error) {
	if top <= 0 {
		top = 5
	}
	u := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/recent?$top=%d", top)
	var resp driveItemListResponse
	if err := c.doJSON(ctx, http.MethodGet, u, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Value, nil
}

func (c *Client) GetItemByPath(ctx context.Context, remotePath string) (*DriveItem, error) {
	remotePath = normalizeRemotePath(remotePath)
	u := "https://graph.microsoft.com/v1.0/me/drive/root"
	if remotePath != "" {
		u += ":" + graphPathEscape(remotePath) + ":"
	}
	var out DriveItem
	if err := c.doJSON(ctx, http.MethodGet, u, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListChildren(ctx context.Context, remotePath string) ([]DriveItem, error) {
	remotePath = normalizeRemotePath(remotePath)
	u := "https://graph.microsoft.com/v1.0/me/drive/root"
	if remotePath == "" {
		u += "/children"
	} else {
		u += ":" + graphPathEscape(remotePath) + ":/children"
	}

	var resp driveItemListResponse
	if err := c.doJSON(ctx, http.MethodGet, u, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Value, nil
}

func (c *Client) CreateFolder(ctx context.Context, parentRemotePath, name string) error {
	parentRemotePath = normalizeRemotePath(parentRemotePath)
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("folder name is required")
	}

	u := "https://graph.microsoft.com/v1.0/me/drive/root"
	if parentRemotePath == "" {
		u += "/children"
	} else {
		u += ":" + graphPathEscape(parentRemotePath) + ":/children"
	}

	body := map[string]any{
		"name":                              name,
		"folder":                            map[string]any{},
		"@microsoft.graph.conflictBehavior": "fail",
	}

	res, b, err := c.do(ctx, http.MethodPost, u, bytes.NewReader(mustJSON(body)), map[string]string{
		"Accept":       "application/json",
		"Content-Type": "application/json",
	})
	if err != nil {
		return err
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	if res.StatusCode == http.StatusConflict {
		return nil
	}
	var ge GraphError
	_ = json.Unmarshal(b, &ge)
	if ge.Error.Code != "" || ge.Error.Message != "" {
		return fmt.Errorf("create folder failed: %s: %s", ge.Error.Code, ge.Error.Message)
	}
	return fmt.Errorf("create folder failed: %s", strings.TrimSpace(string(b)))
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (c *Client) EnsureRemoteFolder(ctx context.Context, remotePath string) error {
	remotePath = normalizeRemotePath(remotePath)
	if remotePath == "" {
		return nil
	}
	segments := strings.Split(strings.TrimPrefix(remotePath, "/"), "/")
	parent := ""
	for _, seg := range segments {
		if seg == "" {
			continue
		}
		if err := c.CreateFolder(ctx, parent, seg); err != nil {
			return err
		}
		if parent == "" {
			parent = "/" + seg
		} else {
			parent = parent + "/" + seg
		}
	}
	return nil
}

func (c *Client) UploadFile(ctx context.Context, localPath, remotePath string) error {
	return c.UploadFileWithOptions(ctx, localPath, remotePath, TransferOptions{})
}

func (c *Client) UploadFileWithOptions(ctx context.Context, localPath, remotePath string, opt TransferOptions) error {
	st, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	if st.IsDir() {
		return errors.New("local path is a directory; use UploadFolder")
	}
	if remotePath == "" {
		return errors.New("remote path is required")
	}
	remotePath = normalizeRemotePath(remotePath)

	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	const maxSimpleUpload = 4 * 1024 * 1024
	if st.Size() <= maxSimpleUpload {
		return c.simpleUpload(ctx, f, st.Size(), remotePath, opt)
	}
	return c.uploadSession(ctx, f, st.Size(), remotePath, opt)
}

func (c *Client) simpleUpload(ctx context.Context, r io.Reader, size int64, remotePath string, opt TransferOptions) error {
	u := "https://graph.microsoft.com/v1.0/me/drive/root:" + graphPathEscape(remotePath) + ":/content"
	ct := mime.TypeByExtension(filepath.Ext(remotePath))
	if ct == "" {
		ct = "application/octet-stream"
	}
	if opt.Callbacks.OnBytes != nil {
		r = &countingReader{r: r, on: opt.Callbacks.OnBytes}
	}

	res, b, err := c.do(ctx, http.MethodPut, u, r, map[string]string{
		"Content-Type":   ct,
		"Content-Length": strconv.FormatInt(size, 10),
	})
	if err != nil {
		return err
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	var ge GraphError
	_ = json.Unmarshal(b, &ge)
	if ge.Error.Message != "" || ge.Error.Code != "" {
		return fmt.Errorf("upload failed: %s: %s", ge.Error.Code, ge.Error.Message)
	}
	return fmt.Errorf("upload failed: %s", strings.TrimSpace(string(b)))
}

func (c *Client) uploadSession(ctx context.Context, f *os.File, size int64, remotePath string, opt TransferOptions) error {
	u := "https://graph.microsoft.com/v1.0/me/drive/root:" + graphPathEscape(remotePath) + ":/createUploadSession"
	var sess createUploadSessionResponse
	if err := c.doJSON(ctx, http.MethodPost, u, map[string]any{
		"item": map[string]any{
			"@microsoft.graph.conflictBehavior": "replace",
			"name":                              filepath.Base(remotePath),
		},
	}, &sess); err != nil {
		return err
	}
	if sess.UploadURL == "" {
		return errors.New("createUploadSession missing uploadUrl")
	}

	chunkSize := normalizeUploadChunkSize(opt.ChunkSize)
	threads := normalizeThreads(opt.Threads)
	totalChunks := int((size + chunkSize - 1) / chunkSize)
	if totalChunks <= 1 {
		threads = 1
	}

	type chunk struct {
		index int
		start int64
		end   int64
		size  int
	}
	chunks := make(chan chunk, threads)
	errCh := make(chan error, 1)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	hc := c.httpClient()
	for i := 0; i < threads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, chunkSize)
			for ck := range chunks {
				n, err := f.ReadAt(buf[:ck.size], ck.start)
				if err != nil && !errors.Is(err, io.EOF) {
					select {
					case errCh <- err:
					default:
					}
					cancel()
					return
				}
				if n != ck.size {
					select {
					case errCh <- fmt.Errorf("read size mismatch: got %d, want %d", n, ck.size):
					default:
					}
					cancel()
					return
				}

				req, err := http.NewRequestWithContext(ctx, http.MethodPut, sess.UploadURL, bytes.NewReader(buf[:ck.size]))
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					cancel()
					return
				}
				req.Header.Set("Content-Length", strconv.Itoa(ck.size))
				req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", ck.start, ck.end, size))
				if c.UserAgent != "" {
					req.Header.Set("User-Agent", c.UserAgent)
				}
				if c.Verbose {
					fmt.Fprintln(os.Stderr, "PUT", sess.UploadURL)
				}

				res, err := hc.Do(req)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					cancel()
					return
				}
				b, err := io.ReadAll(res.Body)
				res.Body.Close()
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					cancel()
					return
				}
				if c.Verbose {
					fmt.Fprintln(os.Stderr, "Status", res.Status)
				}

				if res.StatusCode == http.StatusAccepted || (res.StatusCode >= 200 && res.StatusCode < 300) {
					if opt.Callbacks.OnBytes != nil {
						opt.Callbacks.OnBytes(int64(ck.size))
					}
					if opt.Callbacks.OnChunk != nil {
						opt.Callbacks.OnChunk()
					}
					continue
				}
				var ge GraphError
				_ = json.Unmarshal(b, &ge)
				if ge.Error.Message != "" || ge.Error.Code != "" {
					select {
					case errCh <- fmt.Errorf("chunk upload failed: %s: %s", ge.Error.Code, ge.Error.Message):
					default:
					}
					cancel()
					return
				}
				select {
				case errCh <- fmt.Errorf("chunk upload failed: %s", strings.TrimSpace(string(b))):
				default:
				}
				cancel()
				return
			}
		}()
	}

	for i := 0; i < totalChunks; i++ {
		start := int64(i) * chunkSize
		end := start + chunkSize - 1
		if end >= size {
			end = size - 1
		}
		sz := int(end - start + 1)
		select {
		case <-ctx.Done():
			break
		case chunks <- chunk{index: i, start: start, end: end, size: sz}:
		}
	}
	close(chunks)
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
	}
	return nil
}

func (c *Client) DownloadFile(ctx context.Context, remotePath, localPath string) error {
	return c.DownloadFileByPath(ctx, remotePath, localPath)
}

func (c *Client) downloadToFile(ctx context.Context, method, u, localPath string) error {
	return c.downloadToFileWithCallbacks(ctx, method, u, localPath, TransferOptions{})
}

func (c *Client) downloadToFileWithCallbacks(ctx context.Context, method, u, localPath string, opt TransferOptions) error {
	token, err := c.AccessToken(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Verbose {
		fmt.Fprintln(os.Stderr, method, u)
	}

	res, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if c.Verbose {
		fmt.Fprintln(os.Stderr, "Status", res.Status)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		var ge GraphError
		_ = json.Unmarshal(b, &ge)
		if ge.Error.Message != "" || ge.Error.Code != "" {
			return fmt.Errorf("download failed: %s: %s", ge.Error.Code, ge.Error.Message)
		}
		return fmt.Errorf("download failed: %s", strings.TrimSpace(string(b)))
	}

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return err
	}
	tmp := localPath + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := io.Writer(out)
	if opt.Callbacks.OnBytes != nil && opt.Callbacks.OnChunk != nil {
		cs := normalizeDownloadChunkSize(opt.ChunkSize)
		var acc int64
		w = &countingWriter{
			w: w,
			on: func(n int64) {
				opt.Callbacks.OnBytes(n)
				acc += n
				for acc >= cs {
					acc -= cs
					opt.Callbacks.OnChunk()
				}
			},
		}
	} else if opt.Callbacks.OnBytes != nil {
		w = &countingWriter{w: w, on: opt.Callbacks.OnBytes}
	}
	if _, err := io.Copy(w, res.Body); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if opt.Callbacks.OnChunk != nil {
		opt.Callbacks.OnChunk()
	}
	return os.Rename(tmp, localPath)
}

func (c *Client) DownloadItem(ctx context.Context, remotePath, localPath string) error {
	item, err := c.GetItemByPath(ctx, remotePath)
	if err != nil {
		return err
	}
	if item.Folder != nil {
		return c.DownloadFolder(ctx, remotePath, localPath)
	}
	return c.DownloadFileByPath(ctx, remotePath, localPath)
}

func (c *Client) DownloadFileByPath(ctx context.Context, remotePath, localPath string) error {
	return c.DownloadFileByPathWithOptions(ctx, remotePath, localPath, TransferOptions{})
}

func (c *Client) DownloadFileByPathWithOptions(ctx context.Context, remotePath, localPath string, opt TransferOptions) error {
	remotePath = normalizeRemotePath(remotePath)
	if remotePath == "" {
		return errors.New("remote path is required")
	}
	if localPath == "" {
		localPath = filepath.Base(remotePath)
	}

	chunkSize := normalizeDownloadChunkSize(opt.ChunkSize)
	threads := normalizeThreads(opt.Threads)

	u := "https://graph.microsoft.com/v1.0/me/drive/root"
	if remotePath != "" {
		u += ":" + graphPathEscape(remotePath) + ":"
	}
	u += "?$select=id,name,size,@microsoft.graph.downloadUrl"
	var it DriveItem
	if err := c.doJSON(ctx, http.MethodGet, u, nil, &it); err != nil {
		return err
	}
	if it.DownloadURL == "" || it.Size <= 0 {
		u2 := "https://graph.microsoft.com/v1.0/me/drive/root:" + graphPathEscape(remotePath) + ":/content"
		return c.downloadToFileWithCallbacks(ctx, http.MethodGet, u2, localPath, opt)
	}

	size := it.Size
	totalChunks := int((size + chunkSize - 1) / chunkSize)
	if totalChunks <= 1 {
		threads = 1
	}
	if threads > totalChunks {
		threads = totalChunks
		if threads < 1 {
			threads = 1
		}
	}

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return err
	}
	tmp := localPath + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := out.Truncate(size); err != nil {
		out.Close()
		return err
	}

	type chunk struct {
		start int64
		end   int64
	}

	chunks := make(chan chunk, threads)
	errCh := make(chan error, 1)
	errRangeNotSupported := errors.New("range not supported")

	baseCtx := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	hc := c.httpClient()
	var wg sync.WaitGroup
	for i := 0; i < threads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 256*1024)
			for ck := range chunks {
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, it.DownloadURL, nil)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					cancel()
					return
				}
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", ck.start, ck.end))
				if c.UserAgent != "" {
					req.Header.Set("User-Agent", c.UserAgent)
				}
				if c.Verbose {
					fmt.Fprintln(os.Stderr, "GET", it.DownloadURL)
				}

				res, err := hc.Do(req)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					cancel()
					return
				}
				if c.Verbose {
					fmt.Fprintln(os.Stderr, "Status", res.Status)
				}
				if res.StatusCode == http.StatusOK && (ck.start != 0 || ck.end != size-1) {
					res.Body.Close()
					select {
					case errCh <- errRangeNotSupported:
					default:
					}
					cancel()
					return
				}
				if res.StatusCode != http.StatusPartialContent && res.StatusCode != http.StatusOK {
					b, _ := io.ReadAll(res.Body)
					res.Body.Close()
					select {
					case errCh <- fmt.Errorf("download failed: %s", strings.TrimSpace(string(b))):
					default:
					}
					cancel()
					return
				}

				w := &writerAt{f: out, off: ck.start}
				if opt.Callbacks.OnBytes != nil {
					wc := &countingWriter{w: w, on: opt.Callbacks.OnBytes}
					if _, err := io.CopyBuffer(wc, res.Body, buf); err != nil {
						res.Body.Close()
						select {
						case errCh <- err:
						default:
						}
						cancel()
						return
					}
				} else {
					if _, err := io.CopyBuffer(w, res.Body, buf); err != nil {
						res.Body.Close()
						select {
						case errCh <- err:
						default:
						}
						cancel()
						return
					}
				}
				res.Body.Close()

				if opt.Callbacks.OnChunk != nil {
					opt.Callbacks.OnChunk()
				}
			}
		}()
	}

	for i := 0; i < totalChunks; i++ {
		start := int64(i) * chunkSize
		end := start + chunkSize - 1
		if end >= size {
			end = size - 1
		}
		select {
		case <-ctx.Done():
			break
		case chunks <- chunk{start: start, end: end}:
		}
	}
	close(chunks)
	wg.Wait()

	out.Close()
	select {
	case err := <-errCh:
		_ = os.Remove(tmp)
		if errors.Is(err, errRangeNotSupported) {
			u2 := "https://graph.microsoft.com/v1.0/me/drive/root:" + graphPathEscape(remotePath) + ":/content"
			return c.downloadToFileWithCallbacks(baseCtx, http.MethodGet, u2, localPath, opt)
		}
		return err
	default:
	}
	return os.Rename(tmp, localPath)
}

func (c *Client) DownloadFolder(ctx context.Context, remoteFolderPath, localDir string) error {
	remoteFolderPath = normalizeRemotePath(remoteFolderPath)
	if localDir == "" {
		base := filepath.Base(remoteFolderPath)
		if base == "" {
			base = "onedrive_root"
		}
		localDir = base
	}
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return err
	}

	children, err := c.ListChildren(ctx, remoteFolderPath)
	if err != nil {
		return err
	}
	for _, it := range children {
		childRemote := remoteFolderPath
		if childRemote == "" {
			childRemote = "/" + it.Name
		} else {
			childRemote = childRemote + "/" + it.Name
		}
		childLocal := filepath.Join(localDir, it.Name)
		if it.Folder != nil {
			if err := c.DownloadFolder(ctx, childRemote, childLocal); err != nil {
				return err
			}
			continue
		}
		if err := c.DownloadFileByPath(ctx, childRemote, childLocal); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) UploadFolder(ctx context.Context, localDir, remoteDir string) error {
	st, err := os.Stat(localDir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return errors.New("local path is not a directory; use UploadFile")
	}
	remoteDir = normalizeRemotePath(remoteDir)
	if remoteDir != "" {
		if err := c.EnsureRemoteFolder(ctx, remoteDir); err != nil {
			return err
		}
	}

	entries, err := os.ReadDir(localDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		lp := filepath.Join(localDir, e.Name())
		rp := remoteDir
		if rp == "" {
			rp = "/" + e.Name()
		} else {
			rp = rp + "/" + e.Name()
		}
		if e.IsDir() {
			if err := c.UploadFolder(ctx, lp, rp); err != nil {
				return err
			}
			continue
		}
		if err := c.UploadFile(ctx, lp, rp); err != nil {
			return err
		}
	}
	return nil
}

func FormatBytes(n int64) string {
	if n < 0 {
		return strconv.FormatInt(n, 10)
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n >= div*unit && exp < 5 {
		div *= unit
		exp++
	}
	value := float64(n) / float64(div)
	suffix := []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}[exp]
	return fmt.Sprintf("%.2f %s", value, suffix)
}

func FormatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format(time.RFC3339)
}
