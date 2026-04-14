// Package port defines the interfaces the application layer needs from the
// outside world. Adapters (infrastructure/*) implement these; main.go wires
// them in. The application layer never imports concrete adapters.
package port

import "context"

// KeyboardOption is one inline button on a Telegram keyboard.
// CallbackData is what the bot receives when the user taps it.
type KeyboardOption struct {
	Label        string
	CallbackData string
}

// Messenger is the bot's view of the chat platform. Phase 1 is Telegram only,
// but nothing in this interface is Telegram-specific on purpose.
//
// Message IDs are returned as int so the service can later Edit or Delete
// the same message (used for progress updates). 0 means "no such message".
type Messenger interface {
	SendText(ctx context.Context, chatID int64, text string) (messageID int, err error)
	EditText(ctx context.Context, chatID int64, messageID int, text string) error
	SendOptionsKeyboard(ctx context.Context, chatID int64, text string, opts []KeyboardOption) (messageID int, err error)
	SendAudio(ctx context.Context, chatID int64, filePath, title string) error
	// SendVideo is defined for Phase 2; current implementations may return an
	// error. Keeping it in the port avoids breaking the interface later.
	SendVideo(ctx context.Context, chatID int64, filePath, title string) error
	DeleteMessage(ctx context.Context, chatID int64, messageID int) error
}
