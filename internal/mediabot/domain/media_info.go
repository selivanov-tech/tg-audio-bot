package domain

import "time"

// MediaInfo is the metadata a provider returns from FetchInfo, before any
// download happens. It is what the bot shows the user alongside the quality
// keyboard so they know what they asked for.
type MediaInfo struct {
	Title    string
	Duration time.Duration // zero when unknown
	Provider ProviderID
}
