package memorycache

import (
	"bytes"
	"strings"
	"time"

	"github.com/megaease/easegateway/pkg/context"
	"github.com/megaease/easegateway/pkg/logger"
	"github.com/megaease/easegateway/pkg/util/httpheader"
	"github.com/megaease/easegateway/pkg/util/stringtool"

	cache "github.com/patrickmn/go-cache"
)

const (
	cleanupIntervalFactor = 2
	cleanupIntervalMin    = 1 * time.Minute
)

type (
	// MemoryCache is an utility MemoryCache.
	MemoryCache struct {
		spec *Spec

		cache *cache.Cache
	}

	// Spec describes the MemoryCache.
	Spec struct {
		Expiration    string   `yaml:"expiration" jsonschema:"required,format=duration"`
		MaxEntryBytes uint32   `yaml:"maxEntryBytes" jsonschema:"required,minimum=1"`
		Codes         []int    `yaml:"codes" jsonschema:"required,minItems=1,uniqueItems=true,format=httpcode-array"`
		Methods       []string `yaml:"methods" jsonschema:"required,minItems=1,uniqueItems=true,format=httpmethod-array"`
	}

	cacheEntry struct {
		statusCode int
		header     *httpheader.HTTPHeader
		body       []byte
	}
)

// New creates a MemoryCache.
func New(spec *Spec) *MemoryCache {
	expiration, err := time.ParseDuration(spec.Expiration)
	if err != nil {
		logger.Errorf("BUG: parse duraion %s failed: %v", spec.Expiration, err)
		expiration = 10 * time.Second
	}

	cleanupInterval := expiration * cleanupIntervalFactor
	if cleanupInterval < cleanupIntervalMin {
		cleanupInterval = cleanupIntervalMin
	}
	cache := cache.New(expiration, cleanupInterval)

	return &MemoryCache{
		spec:  spec,
		cache: cache,
	}
}

func (mc *MemoryCache) key(ctx context.HTTPContext) string {
	r := ctx.Request()
	return stringtool.Cat(r.Scheme(), r.Host(), r.Path(), r.Method())
}

// Load tries to load cache for HTTPContext.
func (mc *MemoryCache) Load(ctx context.HTTPContext) (loaded bool) {
	// Reference: https://tools.ietf.org/html/rfc7234#section-5.2
	r, w := ctx.Request(), ctx.Response()

	matchMethod := false
	for _, method := range mc.spec.Methods {
		if r.Method() == method {
			matchMethod = true
			break
		}
	}
	if !matchMethod {
		return false
	}

	for _, value := range r.Header().GetAll(httpheader.KeyCacheControl) {
		if strings.Contains(value, "no-cache") {
			return false
		}
	}

	v, ok := mc.cache.Get(mc.key(ctx))
	if ok {
		entry := v.(*cacheEntry)
		w.SetStatusCode(entry.statusCode)
		w.Header().AddFrom(entry.header)
		w.SetBody(bytes.NewReader(entry.body))
		ctx.AddTag("cacheLoad")
	}

	return ok
}

// Store tries to store cache for HTTPContext.
func (mc *MemoryCache) Store(ctx context.HTTPContext) {
	r, w := ctx.Request(), ctx.Response()

	matchMethod := false
	for _, method := range mc.spec.Methods {
		if r.Method() == method {
			matchMethod = true
			break
		}
	}
	if !matchMethod {
		return
	}

	matchCode := false
	for _, code := range mc.spec.Codes {
		if w.StatusCode() == code {
			matchCode = true
			break
		}
	}
	if !matchCode {
		return
	}

	for _, value := range r.Header().GetAll(httpheader.KeyCacheControl) {
		if strings.Contains(value, "no-store") ||
			strings.Contains(value, "no-cache") {
			return
		}
	}
	for _, value := range w.Header().GetAll(httpheader.KeyCacheControl) {
		if strings.Contains(value, "no-store") ||
			strings.Contains(value, "no-cache") ||
			strings.Contains(value, "must-revalidate") {
			return
		}
	}

	key := mc.key(ctx)
	entry := &cacheEntry{
		statusCode: w.StatusCode(),
		header:     w.Header().Copy(),
	}
	bodyLength := 0
	ctx.Response().OnFlushBody(func(body []byte, complete bool) []byte {
		bodyLength += len(body)
		if bodyLength > int(mc.spec.MaxEntryBytes) {
			return body
		}

		entry.body = append(entry.body, body...)
		if complete {
			mc.cache.SetDefault(key, entry)
			ctx.AddTag("cacheStore")
		}

		return body
	})
}
