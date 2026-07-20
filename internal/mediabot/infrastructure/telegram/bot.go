// Package telegram adapts the go-telegram/bot library to the application's
// port.Messenger interface, and dispatches incoming updates to an
// UpdateHandler supplied by the application layer.
package telegram

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/application/port"
)

// UpdateHandler is what the application provides so the adapter can deliver
// incoming Telegram updates. Methods are called from goroutines managed by
// the underlying library; implementations must be safe for concurrent use.
type UpdateHandler interface {
	HandleMessage(ctx context.Context, chatID int64, text string, messageID int)
	HandleCallback(ctx context.Context, chatID int64, data string, messageID int, callbackQueryID string)
}

// Bot is the Telegram adapter. It owns the underlying *tgbot.Bot client and
// runs the webhook update-dispatcher when Start is called.
type Bot struct {
	b       *tgbot.Bot
	handler UpdateHandler
}

// NewBot constructs the adapter and registers default handlers that forward
// updates to h. Token is the bot API token from BotFather. webhookSecret is
// the random string Telegram echoes back in the X-Telegram-Bot-Api-Secret-Token
// header; the library validates it on every incoming POST and silently drops
// mismatches. Must be non-empty — we don't support an unauthenticated webhook.
func NewBot(token, webhookSecret string, h UpdateHandler) (*Bot, error) {
	if token == "" {
		return nil, fmt.Errorf("telegram: empty token")
	}
	if webhookSecret == "" {
		return nil, fmt.Errorf("telegram: empty webhook secret")
	}
	wrap := &Bot{handler: h}

	opts := []tgbot.Option{
		tgbot.WithDefaultHandler(wrap.dispatch),
		tgbot.WithWebhookSecretToken(webhookSecret),
	}
	b, err := tgbot.New(token, opts...)
	if err != nil {
		return nil, fmt.Errorf("telegram: new bot: %w", err)
	}
	wrap.b = b
	return wrap, nil
}

// Start runs the webhook update-dispatcher loop until ctx is cancelled. It
// reads from the internal updates channel populated by WebhookHandler and
// invokes dispatch() for each update.
func (t *Bot) Start(ctx context.Context) {
	t.b.StartWebhook(ctx)
}

// WebhookHandler returns the HTTP handler that receives Telegram webhook
// POSTs. Mount it on an HTTP mux at the path registered with RegisterWebhook.
// The handler validates the secret token and enqueues updates for Start to
// dispatch.
func (t *Bot) WebhookHandler() http.HandlerFunc {
	return t.b.WebhookHandler()
}

// RegisterWebhook tells Telegram where to POST updates. Idempotent: calling
// with the same URL replaces the existing registration with no effect on
// pending updates. Restricts update types to messages and callback queries
// so the bot is not woken by irrelevant events (edits, polls, etc.).
func (t *Bot) RegisterWebhook(ctx context.Context, url, secret string) error {
	_, err := t.b.SetWebhook(ctx, &tgbot.SetWebhookParams{
		URL:            url,
		SecretToken:    secret,
		AllowedUpdates: []string{"message", "callback_query"},
	})
	if err != nil {
		return fmt.Errorf("telegram: set webhook: %w", err)
	}
	return nil
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

// SetCommands registers the bot's "/" command menu with Telegram. Usually
// called once at startup; persists server-side so users see it in-chat.
func (t *Bot) SetCommands(ctx context.Context, cmds []port.Command) error {
	botCmds := make([]models.BotCommand, 0, len(cmds))
	for _, c := range cmds {
		botCmds = append(botCmds, models.BotCommand{
			Command:     c.Command,
			Description: c.Description,
		})
	}
	_, err := t.b.SetMyCommands(ctx, &tgbot.SetMyCommandsParams{
		Commands: botCmds,
	})
	return err
}
