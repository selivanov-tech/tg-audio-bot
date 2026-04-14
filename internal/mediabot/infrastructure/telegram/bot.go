// Package telegram adapts the go-telegram/bot library to the application's
// port.Messenger interface, and dispatches incoming updates to an
// UpdateHandler supplied by the application layer.
package telegram

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/rag-brothers/media-bot/internal/mediabot/application/port"
)

// UpdateHandler is what the application provides so the adapter can deliver
// incoming Telegram updates. Methods are called from goroutines managed by
// the underlying library; implementations must be safe for concurrent use.
type UpdateHandler interface {
	HandleMessage(ctx context.Context, chatID int64, text string, messageID int)
	HandleCallback(ctx context.Context, chatID int64, data string, messageID int, callbackQueryID string)
}

// Bot is the Telegram adapter. It owns the underlying *tgbot.Bot client and
// runs the polling loop when Start is called.
type Bot struct {
	b       *tgbot.Bot
	handler UpdateHandler
}

// NewBot constructs the adapter and registers default handlers that forward
// updates to h. Token is the bot API token from BotFather.
func NewBot(token string, h UpdateHandler) (*Bot, error) {
	if token == "" {
		return nil, fmt.Errorf("telegram: empty token")
	}
	wrap := &Bot{handler: h}

	opts := []tgbot.Option{
		tgbot.WithDefaultHandler(wrap.dispatch),
	}
	b, err := tgbot.New(token, opts...)
	if err != nil {
		return nil, fmt.Errorf("telegram: new bot: %w", err)
	}
	wrap.b = b
	return wrap, nil
}

// Start runs the long-polling loop until ctx is cancelled.
func (t *Bot) Start(ctx context.Context) {
	t.b.Start(ctx)
}

// dispatch is the single entry point for all updates. Message and
// callback-query shapes are routed to the application handler.
func (t *Bot) dispatch(ctx context.Context, _ *tgbot.Bot, update *models.Update) {
	if t.handler == nil {
		return
	}
	switch {
	case update.CallbackQuery != nil:
		data := update.CallbackQuery.Data
		var chatID int64
		var msgID int
		if update.CallbackQuery.Message.Message != nil {
			chatID = update.CallbackQuery.Message.Message.Chat.ID
			msgID = update.CallbackQuery.Message.Message.ID
		}
		t.handler.HandleCallback(ctx, chatID, data, msgID, update.CallbackQuery.ID)
	case update.Message != nil:
		t.handler.HandleMessage(ctx, update.Message.Chat.ID, update.Message.Text, update.Message.ID)
	}
}

// --- port.Messenger implementation -----------------------------------------

func (t *Bot) SendText(ctx context.Context, chatID int64, text string) (int, error) {
	msg, err := t.b.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	})
	if err != nil {
		return 0, err
	}
	return msg.ID, nil
}

func (t *Bot) EditText(ctx context.Context, chatID int64, messageID int, text string) error {
	_, err := t.b.EditMessageText(ctx, &tgbot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: messageID,
		Text:      text,
	})
	return err
}

func (t *Bot) SendOptionsKeyboard(ctx context.Context, chatID int64, text string, opts []port.KeyboardOption) (int, error) {
	msg, err := t.b.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ReplyMarkup: buildInlineKeyboard(opts),
	})
	if err != nil {
		return 0, err
	}
	return msg.ID, nil
}

func (t *Bot) SendAudio(ctx context.Context, chatID int64, filePath, title string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open audio: %w", err)
	}
	defer f.Close()

	_, err = t.b.SendAudio(ctx, &tgbot.SendAudioParams{
		ChatID: chatID,
		Audio: &models.InputFileUpload{
			Filename: filepath.Base(filePath),
			Data:     f,
		},
		Title: title,
	})
	return err
}

// SendVideo is reserved for Phase 2 (video media type). Current bot is
// audio-only; calling this is a programming error.
func (t *Bot) SendVideo(ctx context.Context, chatID int64, filePath, title string) error {
	return fmt.Errorf("telegram: SendVideo not implemented in phase 1")
}

func (t *Bot) DeleteMessage(ctx context.Context, chatID int64, messageID int) error {
	_, err := t.b.DeleteMessage(ctx, &tgbot.DeleteMessageParams{
		ChatID:    chatID,
		MessageID: messageID,
	})
	return err
}

// AnswerCallback acknowledges a callback query so Telegram stops retrying it.
// Exposed so the application layer can dismiss the spinner on the user's
// tapped button once it has started processing.
func (t *Bot) AnswerCallback(ctx context.Context, callbackQueryID string) error {
	_, err := t.b.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{
		CallbackQueryID: callbackQueryID,
	})
	return err
}
