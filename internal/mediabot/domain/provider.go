package domain

import "context"

// ProviderID identifies a media source (e.g. "youtube", "instagram").
type ProviderID string

// DownloadOptions is everything a provider needs to fetch a media file.
// OnProgress is called on every progress line the provider observes; the
// application layer is responsible for throttling edits to the user-facing
// message (see CLAUDE.md: "Progress throttling lives in the service").
type DownloadOptions struct {
	URL        string
	Quality    Quality
	MediaType  MediaType
	OutputDir  string
	Filename   string // without extension; provider adds the appropriate one
	OnProgress func(Progress)
}

// Provider is implemented by each media source adapter in the infrastructure
// layer. New sources are added by implementing this interface and registering
// the instance in main.go — the application layer never changes.
type Provider interface {
	ID() ProviderID
	MatchesURL(rawURL string) bool
	FetchInfo(ctx context.Context, url string) (MediaInfo, error)
	Download(ctx context.Context, opts DownloadOptions) (filePath string, err error)
}

// ProviderRegistry routes an incoming URL to the first provider that claims it.
// Providers are tried in registration order.
type ProviderRegistry struct {
	providers []Provider
}

func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{}
}

func (r *ProviderRegistry) Register(p Provider) {
	r.providers = append(r.providers, p)
}

// Detect returns the provider whose MatchesURL accepts url, or ErrUnsupportedURL.
func (r *ProviderRegistry) Detect(url string) (Provider, error) {
	for _, p := range r.providers {
		if p.MatchesURL(url) {
			return p, nil
		}
	}
	return nil, ErrUnsupportedURL
}
