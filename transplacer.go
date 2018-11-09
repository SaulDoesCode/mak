package mak

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/cornelk/hashmap"
)

var (
	// Compressable - list of compressable file types, append to it if needed
	Compressable = []string{"", ".txt", ".htm", ".html", ".css", ".toml", ".php", ".js", ".json", ".md", ".mdown", ".xml", ".svg", ".go", ".cgi", ".py", ".pl", ".aspx", ".asp"}
)

// HashMap is an alias of cornelk/hashmap
type HashMap = hashmap.HashMap

// AssetCache is a store for assets
type AssetCache struct {
	Dir   string
	Cache *HashMap

	Expire   time.Duration
	Interval time.Duration

	CacheControl string

	Ticker *time.Ticker
}

// MakeAssetCache prepares a new *AssetCache for use
func MakeAssetCache(dir string, expire time.Duration, interval time.Duration) (*AssetCache, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	a := &AssetCache{
		Dir:          dir,
		Cache:        &HashMap{},
		Expire:       expire,
		CacheControl: "private, must-revalidate",
	}

	a.SetInterval(interval)

	go func() {
		for now := range a.Ticker.C {
			for kv := range a.Cache.Iter() {
				asset := kv.Value.(*Asset)
				if asset.Loaded.Add(a.Expire).After(now) {
					a.Cache.Del(kv.Key)
				}
			}
		}
	}()

	return a, err
}

// SetInterval generates a new ticker with a set interval
func (a *AssetCache) SetInterval(interval time.Duration) {
	if a.Ticker != nil {
		a.Ticker.Stop()
	}
	a.Interval = interval
	a.Ticker = time.NewTicker(interval)
}

// Handler serves the assets
func (a *AssetCache) Handler(c *Ctx) error {
	name := path.Clean(a.Dir + c.R.URL.Path)

	asset, ok := a.Get(name)
	if ok {
		return asset.Serve(c)
	}

	err := ErrNotFound.Envoy(c)
	if c.instance.ErrorHandler != nil {
		return c.instance.ErrorHandler(c, err)
	}
	return err
}

// Close stops and clears the AssetCache
func (a *AssetCache) Close() error {
	a.Cache = &HashMap{}
	if a.Ticker != nil {
		a.Ticker.Stop()
	}
	return nil
}

// Gen generates a new Asset
func (a *AssetCache) Gen(name string) (*Asset, error) {
	name = path.Clean(a.Dir + name)

	fs, err := os.Stat(name)
	if err != nil {
		return nil, err
	}

	if fs.IsDir() {
		return a.Gen(name + "/index.html")
	}

	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	content, err := ioutil.ReadFile(name)
	if err != nil {
		return nil, err
	}

	ext := filepath.Ext(name)

	ContentType := mime.TypeByExtension(ext)

	Compressed := stringsContainsCI(Compressable, ext)

	asset := &Asset{
		ContentType: ContentType,
		Content:     content,
		Compressed:  Compressed,
		ModTime:     fs.ModTime(),
	}

	if Compressed {
		compressed, err := gzipBytes(content, 9)
		if err != nil {
			return nil, err
		}

		var et []byte
		h := sha256.New()
		_, err = io.Copy(h, bytes.NewReader(compressed))
		if err != nil {
			return nil, err
		}
		if et == nil {
			et = h.Sum(nil)
		}
		asset.EtagCompressed = fmt.Sprintf(`"%x"`, et)

		asset.ContentCompressed = compressed
	}

	var et []byte
	h := sha256.New()
	_, err = io.Copy(h, f)
	if err != nil {
		return nil, err
	}
	if et == nil {
		et = h.Sum(nil)
	}
	asset.Etag = fmt.Sprintf(`"%x"`, et)

	if err == nil {
		asset.Loaded = time.Now()
		a.Cache.Set(name, asset)
	}

	return asset, err
}

// Get fetches an asset
func (a *AssetCache) Get(name string) (*Asset, bool) {
	name = path.Clean(a.Dir + name)

	raw, ok := a.Cache.GetStringKey(name)
	if ok {
		asset, err := a.Gen(name)
		return asset, err == nil
	}
	return raw.(*Asset), ok
}

// Del removes an asset, nb. not the file, the file is fine
func (a *AssetCache) Del(name string) {
	name = path.Clean(a.Dir + name)
	a.Cache.Del(name)
}

// Asset is an http servable resource
type Asset struct {
	ContentType string

	Loaded time.Time

	ModTime time.Time

	Content           []byte
	ContentCompressed []byte

	Etag           string
	EtagCompressed string

	Compressed bool
}

// Serve an asset through c *Ctx
func (as *Asset) Serve(c *Ctx) error {
	c.SetContentType(as.ContentType)
	if c.GetHeader("last-modified") == "" {
		c.SetHeader("last-modified", as.ModTime.UTC().Format(http.TimeFormat))
	}

	var n int
	var err error
	if as.Compressed && strings.Contains(c.Header("accept-encoding"), "gzip") {
		c.SetHeader("etag", as.EtagCompressed)
		n, err = c.Write(as.ContentCompressed)
	} else {
		c.SetHeader("etag", as.Etag)
		n, err = c.Write(as.Content)
	}

	if err == nil {
		c.ContentLength += int64(n)
	}
	return err
}

func gzipBytes(content []byte, level int) ([]byte, error) {
	var b bytes.Buffer

	gz, err := gzip.NewWriterLevel(&b, level)
	if err != nil {
		return nil, err
	}

	if _, err := gz.Write(content); err != nil {
		return nil, err
	}
	if err := gz.Flush(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}

	return b.Bytes(), nil
}