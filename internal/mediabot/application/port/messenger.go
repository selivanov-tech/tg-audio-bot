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

// Command is one entry in the platform's "/" command menu.
// Command is the name without the leading slash (e.g. "start");
// Description is the short human-readable hint shown next to it.
type Command struct {
	Command     string
	Description string
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
	// AnswerCallback acknowledges an inline-button tap so the platform
	// stops retrying the callback. Generic enough to belong in the port.
	AnswerCallback(ctx context.Context, callbackQueryID string) error
	// SetCommands registers the "/" command menu shown in chat clients.
	SetCommands(ctx context.Context, cmds []Command) error
}
